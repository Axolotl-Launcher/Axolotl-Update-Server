package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/domain"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ReleasePayload struct {
	EventID     string         `json:"event_id"`
	Version     string         `json:"version"`
	Tag         string         `json:"tag"`
	Channel     string         `json:"channel"`
	ForceUpdate *bool          `json:"force_update"`
	Release     *GitHubRelease `json:"release"`
	Catalog     *Catalog       `json:"catalog"`
}
type GitHubRelease struct {
	ID          json.Number   `json:"id"`
	TagName     string        `json:"tag_name"`
	Body        string        `json:"body"`
	PublishedAt string        `json:"published_at"`
	Draft       *bool         `json:"draft"`
	Assets      []GitHubAsset `json:"assets"`
}
type GitHubAsset struct {
	Name   string `json:"name"`
	Size   *int64 `json:"size"`
	Digest string `json:"digest"`
	URL    string `json:"browser_download_url"`
}
type Catalog struct {
	Version   string            `json:"version"`
	Files     []CatalogFile     `json:"files"`
	Artifacts []CatalogArtifact `json:"artifacts"`
}
type CatalogFile struct {
	Filename string `json:"filename"`
	Size     *int64 `json:"size"`
	SHA256   string `json:"sha256"`
	URL      string `json:"downloadUrl"`
}
type CatalogArtifact struct {
	Filename               string         `json:"filename"`
	Platform               string         `json:"platform"`
	Architecture           string         `json:"architecture"`
	Kind                   string         `json:"kind"`
	Variant                string         `json:"variant"`
	DisplayName            string         `json:"display_name"`
	SortOrder              int            `json:"sort_order"`
	IsPublic               *bool          `json:"is_public"`
	TargetPlatforms        *model.Strings `json:"targetPlatforms"`
	TargetPlatformsSnake   *model.Strings `json:"target_platforms"`
	SignatureFilename      *string        `json:"signatureFilename"`
	SignatureFilenameSnake *string        `json:"signature_filename"`
}

func (s *Service) AuthenticateWebhook(raw []byte, timestamp, signature string) error {
	ts, e := strconv.ParseInt(timestamp, 10, 64)
	if e != nil {
		return domain.Err("invalid_webhook_timestamp", "Webhook timestamp is required.")
	}
	if diff := s.Now().Sub(time.Unix(ts, 0)); diff > s.Config.WebhookMaxAge || diff < -s.Config.WebhookMaxAge {
		return domain.Err("stale_webhook", "Webhook timestamp is outside the accepted window.")
	}
	if s.Config.WebhookSecret == "" {
		return domain.Err("auth_not_configured", "Webhook authentication is not configured.", 503)
	}
	mac := hmac.New(sha256.New, []byte(s.Config.WebhookSecret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(raw)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(strings.TrimPrefix(signature, "sha256=")), []byte(expected)) {
		return domain.Err("invalid_webhook_signature", "Webhook signature is invalid.", 401)
	}
	return nil
}

func (s *Service) Publish(ctx context.Context, raw []byte) (result map[string]any, err error) {
	var p ReleasePayload
	if e := json.Unmarshal(raw, &p); e != nil {
		return nil, domain.Err("invalid_webhook", "Invalid release payload.")
	}
	var rawFields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &rawFields)
	if b, ok := rawFields["force_update"]; ok && string(b) == "null" {
		return nil, domain.Err("invalid_force_update", "force_update must be a boolean.")
	}
	if p.Catalog != nil {
		var catalogFields struct {
			Artifacts []map[string]json.RawMessage `json:"artifacts"`
		}
		_ = json.Unmarshal(rawFields["catalog"], &catalogFields)
		for _, artifact := range catalogFields.Artifacts {
			if b, ok := artifact["is_public"]; ok && string(b) == "null" {
				return nil, domain.Err("invalid_artifact_metadata", "is_public must be a boolean.")
			}
			for _, key := range []string{"targetPlatforms", "target_platforms"} {
				if b, ok := artifact[key]; ok && string(b) == "null" {
					return nil, domain.Err("invalid_artifact_platform", "Updater target platforms must be a non-empty list.")
				}
			}
		}
	}
	if p.EventID == "" || len(p.EventID) > 255 {
		return nil, domain.Err("invalid_webhook", "event_id is required and must not exceed 255 bytes.")
	}
	if _, e := domain.ParseVersion(p.Version); e != nil {
		return nil, e
	}
	unlock, e := s.Lock(p.Version)
	if e != nil {
		return nil, e
	}
	defer func() {
		unlock()
		if err == nil && s.Config.DistServer == "" {
			if _, cleanupErr := s.PruneDist(ctx); cleanupErr != nil {
				slog.Warn("distribution retention incomplete; retry dist prune", "error", cleanupErr)
			}
		}
	}()
	if e = s.EnsureCleanup(p.Version); e != nil {
		return nil, e
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	var existing model.WebhookEvent
	e = s.Store.DB.WithContext(ctx).Where("event_id = ?", p.EventID).First(&existing).Error
	if e == nil {
		if existing.PayloadHash != hash {
			return nil, domain.Err("webhook_event_conflict", "event_id was already used with a different payload.", 409)
		}
		if existing.Status == "processed" {
			return map[string]any{"status": "processed", "idempotent": true}, nil
		}
	} else if !errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, e
	}
	// Claim an event ID before network I/O, including across concurrent versions.
	event := model.WebhookEvent{EventID: p.EventID, Version: model.Ptr(p.Version), EventType: "release", PayloadHash: hash, ReceivedAt: s.Now(), Status: "received"}
	created := s.Store.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&event)
	if created.Error != nil {
		return nil, created.Error
	}
	if created.RowsAffected == 0 {
		if e = s.Store.DB.WithContext(ctx).Where("event_id = ?", p.EventID).First(&existing).Error; e != nil {
			return nil, e
		}
		if existing.PayloadHash != hash {
			return nil, domain.Err("webhook_event_conflict", "event_id was already used with a different payload.", 409)
		}
	}
	defer func() {
		if err != nil {
			message := "Webhook validation or publication failed."
			var apiErr *domain.Error
			if errors.As(err, &apiErr) {
				message = apiErr.Message
			}
			if e := s.Store.DB.WithContext(context.Background()).Model(&model.WebhookEvent{}).Where("event_id = ? AND payload_hash = ? AND status <> ?", p.EventID, hash, "processed").Updates(map[string]any{"status": "failed", "error_message": message}).Error; e != nil {
				err = fmt.Errorf("%w (could not persist failed event: %v)", err, e)
			}
		}
	}()
	prepared, published, e := s.prepare(ctx, p)
	if e != nil {
		return nil, e
	}
	var staged string
	if s.Config.DistServer == "" {
		staged, e = s.stageArtifacts(ctx, p.Version, p.Tag, prepared)
		if e != nil {
			return nil, e
		}
		defer os.RemoveAll(staged)
	}
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	tx := s.Store.DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	defer tx.Rollback()
	var v model.Version
	e = tx.Preload("Artifacts").Where("version = ?", p.Version).First(&v).Error
	if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, e
	}
	if e == nil && v.Status == "published" {
		return nil, domain.Err("version_immutable", "Published versions cannot be imported again.", 409)
	}
	oldArtifacts := v.Artifacts
	v.Artifacts = nil
	v.Version = p.Version
	v.Channel = p.Channel
	v.Status = "published"
	v.Notes = p.Release.Body
	v.ReleaseTag = p.Tag
	v.ReleaseID = string(p.Release.ID)
	v.PublishedAt = &published
	v.ForceUpdate = p.ForceUpdate != nil && *p.ForceUpdate
	v.RevokedAt = nil
	v.RevokeReason = nil
	if e = tx.Omit("Artifacts").Save(&v).Error; e != nil {
		return nil, e
	}
	byName := map[string]model.Artifact{}
	for _, a := range oldArtifacts {
		byName[a.Filename] = a
	}
	for _, a := range prepared {
		a.VersionID = v.ID
		if old, ok := byName[a.Filename]; ok {
			if old.Size != a.Size || old.SHA256 != a.SHA256 {
				return nil, domain.Err("artifact_exists", "Artifact metadata has changed.", 409)
			}
			a.ID = old.ID
			a.CreatedAt = old.CreatedAt
		}
		if e = tx.Save(&a).Error; e != nil {
			return nil, e
		}
	}
	if e = tx.Preload("Artifacts").First(&v, v.ID).Error; e != nil {
		return nil, e
	}
	domain.Normalize(&v)
	seen := map[string]bool{}
	for _, a := range v.Artifacts {
		if !domain.Signed(a) {
			continue
		}
		for _, platform := range a.TargetPlatforms {
			if seen[platform] {
				return nil, domain.Err("duplicate_artifact", "Updater platforms must be unique.")
			}
			seen[platform] = true
		}
	}
	m, e := domain.BuildManifest(v, s.Config.DistributionBaseURL(), "", s.Now())
	if e != nil {
		return nil, e
	}
	if len(m.Platforms) == 0 {
		return nil, domain.Err("missing_artifacts", "At least one signed updater artifact is required.")
	}
	if e = recomputePointers(tx, s.Now()); e != nil {
		return nil, e
	}
	if e = tx.Model(&model.WebhookEvent{}).Where("event_id = ?", p.EventID).Updates(map[string]any{"version": p.Version, "processed_at": s.Now(), "status": "processed", "error_message": nil}).Error; e != nil {
		return nil, e
	}
	path, e := s.manifestPath(p.Version)
	if e != nil {
		return nil, e
	}
	original, e := os.ReadFile(path)
	existed := e == nil
	if e != nil && !os.IsNotExist(e) {
		return nil, e
	}
	data, e := json.MarshalIndent(m, "", "  ")
	if e != nil {
		return nil, e
	}
	undo, e := s.installArtifacts(p.Version, staged, prepared)
	if e != nil {
		return nil, e
	}
	defer func() {
		if err != nil {
			undo()
		}
	}()
	if e = atomicWrite(path, data); e != nil {
		return nil, domain.Err("webhook_failed", "Could not write version manifest.", 500)
	}
	if e = s.commit(tx, "publish"); e != nil {
		return nil, s.logRestore(path, original, existed, domain.Err("webhook_failed", "Webhook validation or publication failed.", 500))
	}
	s.invalidate()
	return map[string]any{"status": "published", "version": p.Version}, nil
}

func (s *Service) prepare(ctx context.Context, p ReleasePayload) ([]model.Artifact, time.Time, error) {
	var zero time.Time
	parsed, e := domain.ParseVersion(p.Version)
	if e != nil {
		return nil, zero, e
	}
	if p.Tag != "v"+p.Version || !domain.ChannelValid(p.Channel) {
		return nil, zero, domain.Err("invalid_webhook", "Version, tag, and channel are inconsistent.")
	}
	if p.Channel == "release" && parsed.Prerelease() != "" {
		return nil, zero, domain.Err("prerelease_release", "Release channel cannot publish prerelease versions.")
	}
	if p.Channel == "beta" && parsed.Prerelease() == "" {
		return nil, zero, domain.Err("stable_beta", "Beta channel requires a prerelease version.")
	}
	if p.Release == nil || p.Catalog == nil {
		return nil, zero, domain.Err("invalid_webhook", "release and catalog are required.")
	}
	r, c := p.Release, p.Catalog
	if r.Draft == nil || *r.Draft || r.TagName != p.Tag || r.ID == "" {
		return nil, zero, domain.Err("invalid_github_release", "GitHub Release must be public and match the webhook tag.")
	}
	if id, e := r.ID.Int64(); e != nil || id <= 0 {
		return nil, zero, domain.Err("invalid_github_release", "GitHub Release id is required.")
	}
	published, e := time.Parse(time.RFC3339Nano, r.PublishedAt)
	if e != nil {
		return nil, zero, domain.Err("invalid_github_release", "GitHub Release published_at is invalid.")
	}
	published = published.UTC()
	if c.Version != p.Version || c.Files == nil || c.Artifacts == nil {
		return nil, zero, domain.Err("invalid_catalog", "Catalog version, files, and artifacts are required.")
	}
	if len(c.Artifacts) == 0 {
		return nil, zero, domain.Err("missing_artifacts", "At least one complete artifact is required.")
	}
	assets := map[string]GitHubAsset{}
	for _, a := range r.Assets {
		if a.Name == "" {
			return nil, zero, domain.Err("invalid_github_release", "GitHub Release assets must have unique names.")
		}
		if _, ok := assets[a.Name]; ok {
			return nil, zero, domain.Err("invalid_github_release", "GitHub Release assets must have unique names.")
		}
		assets[a.Name] = a
	}
	files := map[string]CatalogFile{}
	for _, f := range c.Files {
		if !domain.SafeComponent(f.Filename) || f.Filename == "manifest.json" {
			return nil, zero, domain.Err("invalid_filename", "Filename must be a single safe path component.")
		}
		asset, ok := assets[f.Filename]
		if _, duplicate := files[f.Filename]; !ok || duplicate {
			return nil, zero, domain.Err("invalid_catalog", "Catalog file does not match a unique GitHub asset.")
		}
		digest := strings.TrimPrefix(asset.Digest, "sha256:")
		if !strings.HasPrefix(asset.Digest, "sha256:") || !domain.HashValid(digest) {
			return nil, zero, domain.Err("invalid_github_digest", "GitHub asset digest must be SHA-256.")
		}
		if f.Size == nil || asset.Size == nil || *asset.Size < 0 || *f.Size != *asset.Size || strings.ToLower(f.SHA256) != strings.ToLower(digest) || f.URL != asset.URL {
			return nil, zero, domain.Err("github_asset_metadata_mismatch", "Catalog file metadata does not match GitHub Release.")
		}
		if e = validateAssetURL(f.URL, p.Tag, f.Filename); e != nil {
			return nil, zero, e
		}
		f.SHA256 = strings.ToLower(f.SHA256)
		files[f.Filename] = f
	}
	rows := []model.Artifact{}
	names := map[string]bool{}
	targets := map[string]bool{}
	attachments := map[string]bool{}
	signatures := map[string]bool{}
	for _, a := range c.Artifacts {
		f, ok := files[a.Filename]
		if !ok {
			return nil, zero, domain.Err("invalid_catalog", "Every artifact must reference a catalog file.")
		}
		if names[a.Filename] {
			return nil, zero, domain.Err("duplicate_artifact", "Catalog artifact filenames must be unique.")
		}
		names[a.Filename] = true
		if a.Kind == "" {
			a.Kind = "updater"
		}
		if a.Kind != "updater" && a.Kind != "installer" && a.Kind != "portable" {
			return nil, zero, domain.Err("invalid_artifact_kind", "Catalog artifact kind is not supported.")
		}
		if a.Architecture == "" {
			a.Architecture = "x86_64"
			if strings.Contains(a.Platform, "aarch64") {
				a.Architecture = "aarch64"
			}
		}
		if (!domain.Platforms[a.Platform] && !domain.PackagePlatforms[a.Platform]) || (a.Architecture != "x86_64" && a.Architecture != "aarch64" && a.Architecture != "universal") {
			return nil, zero, domain.Err("invalid_artifact_platform", "Artifact platform or architecture is invalid.")
		}
		if len([]rune(a.Variant)) > 32 || len([]rune(a.DisplayName)) > 160 {
			return nil, zero, domain.Err("invalid_artifact_metadata", "Artifact display metadata is too long.")
		}
		sig := a.SignatureFilename
		if sig == nil {
			sig = a.SignatureFilenameSnake
		}
		platforms := model.Strings(nil)
		if a.Kind == "updater" {
			if !domain.UpdaterSupports(a.Platform, a.Architecture) {
				return nil, zero, domain.Err("invalid_artifact_platform", "Updater platform and architecture are incompatible.")
			}
			platforms = model.Strings{a.Platform}
			if a.TargetPlatforms != nil {
				platforms = *a.TargetPlatforms
			} else if a.TargetPlatformsSnake != nil {
				platforms = *a.TargetPlatformsSnake
			}
			local := map[string]bool{}
			includesSelf := false
			for _, target := range platforms {
				if !domain.UpdaterSupports(target, a.Architecture) || local[target] {
					return nil, zero, domain.Err("invalid_artifact_platform", "Updater target platforms must match the architecture and be unique.")
				}
				if targets[target] {
					return nil, zero, domain.Err("duplicate_artifact", "Updater platforms must be unique.")
				}
				local[target] = true
				targets[target] = true
				if target == a.Platform {
					includesSelf = true
				}
			}
			if len(platforms) == 0 || !includesSelf {
				return nil, zero, domain.Err("invalid_artifact_platform", "Updater platform must be a target platform.")
			}
			if sig == nil || *sig != a.Filename+".sig" {
				return nil, zero, domain.Err("missing_signature", "Updater artifacts require their corresponding .sig file.")
			}
			sf, ok := files[*sig]
			if !ok {
				return nil, zero, domain.Err("missing_signature", "Updater artifacts require a catalog signature file.")
			}
			if *sf.Size > 65536 {
				return nil, zero, domain.Err("github_asset_too_large", "GitHub asset exceeds the configured download limit.")
			}
			signatures[*sig] = true
		}
		if sig != nil && *sig != "" {
			if _, ok := files[*sig]; !ok {
				return nil, zero, domain.Err("invalid_signature", "Artifact signature file is not in the catalog.")
			}
			attachments[*sig] = true
		}
		isPublic := true
		if a.IsPublic != nil {
			isPublic = *a.IsPublic
		}
		if a.DisplayName == "" {
			a.DisplayName = a.Filename
		}
		contentType := mime.TypeByExtension(filepath.Ext(a.Filename))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		rows = append(rows, model.Artifact{Platform: a.Platform, TargetPlatforms: platforms, Architecture: a.Architecture, Filename: a.Filename, RelativePath: "dist/" + p.Version + "/" + a.Filename, Size: *f.Size, SHA256: f.SHA256, SignatureFilename: sig, ContentType: contentType, Kind: a.Kind, Variant: a.Variant, DisplayName: a.DisplayName, SortOrder: a.SortOrder, IsPublic: isPublic, UploadedAt: s.Now()})
	}
	for name := range files {
		if !names[name] && !attachments[name] {
			return nil, zero, domain.Err("invalid_catalog", "Every catalog file must be an artifact or signature attachment.")
		}
	}
	// Validate all metadata before fetching any signatures. Buffers are bounded to 64 KiB.
	decoded := map[string]string{}
	for name := range signatures {
		f := files[name]
		sig, e := s.FetchSignature(ctx, f.URL, *f.Size, f.SHA256)
		if e != nil {
			return nil, zero, e
		}
		decoded[name] = sig
	}
	for i := range rows {
		if rows[i].Kind == "updater" {
			rows[i].Signature = model.Ptr(decoded[*rows[i].SignatureFilename])
		}
	}
	for name := range attachments {
		if names[name] {
			continue
		}
		f := files[name]
		rows = append(rows, model.Artifact{Filename: name, RelativePath: "dist/" + p.Version + "/" + name, Size: *f.Size, SHA256: f.SHA256, ContentType: "application/octet-stream", Kind: "signature", DisplayName: name, IsPublic: false, UploadedAt: s.Now()})
	}
	return rows, published, nil
}

func validateAssetURL(value, tag, filename string) error {
	u, e := url.Parse(value)
	expected := "/Axolotl-Launcher/Axolotl/releases/download/" + domain.EscapeSegment(tag) + "/" + domain.EscapeSegment(filename)
	if e != nil || u.Scheme != "https" || u.Host != "github.com" || u.EscapedPath() != expected || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.User != nil {
		return domain.Err("invalid_github_asset_url", "GitHub asset URL is not an allowed release download URL.")
	}
	return nil
}

func (s *Service) fetchSignature(ctx context.Context, value string, size int64, hash string) (string, error) {
	if size < 0 || size > 65536 {
		return "", domain.Err("github_asset_too_large", "GitHub asset exceeds the configured download limit.")
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: s.Config.DownloadConnectTimeout}).DialContext, TLSHandshakeTimeout: s.Config.DownloadConnectTimeout, ResponseHeaderTimeout: s.Config.DownloadReadTimeout}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: s.Config.DownloadReadTimeout, CheckRedirect: assetRedirect}
	for attempt := 0; attempt <= s.Config.DownloadRetries; attempt++ {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, value, nil)
		if e != nil {
			return "", e
		}
		req.Header.Set("User-Agent", "Axolotl-Update-Server")
		resp, e := client.Do(req)
		if e == nil {
			if resp.StatusCode == http.StatusOK {
				data, readErr := io.ReadAll(io.LimitReader(resp.Body, 65537))
				resp.Body.Close()
				if readErr == nil {
					if len(data) > 65536 {
						return "", domain.Err("github_asset_too_large", "GitHub asset exceeds the configured download limit.")
					}
					sum := sha256.Sum256(data)
					if int64(len(data)) != size || hex.EncodeToString(sum[:]) != hash {
						return "", domain.Err("github_asset_integrity_failed", "GitHub asset metadata does not match downloaded content.")
					}
					if !utf8.Valid(data) {
						return "", domain.Err("invalid_signature", "Updater signature file is invalid.")
					}
					signature := strings.TrimSpace(string(data))
					if signature == "" {
						return "", domain.Err("invalid_signature", "Updater signature file is empty.")
					}
					return signature, nil
				}
			} else {
				resp.Body.Close()
				if resp.StatusCode < 500 && resp.StatusCode != 429 {
					return "", domain.Err("github_asset_download_failed", "GitHub asset download failed.", 502)
				}
			}
		}
		if attempt < s.Config.DownloadRetries {
			timer := time.NewTimer(time.Second * time.Duration(1<<attempt))
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
		}
	}
	return "", domain.Err("github_asset_download_failed", "GitHub asset download failed.", 502)
}
