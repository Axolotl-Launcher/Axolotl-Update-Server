package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/domain"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
)

const retainedPerChannel = 3

// GitHubAssetURL is built from validated metadata, never from request headers/query.
func GitHubAssetURL(tag, filename string) string {
	return "https://github.com/Axolotl-Launcher/Axolotl/releases/download/" + domain.EscapeSegment(tag) + "/" + domain.EscapeSegment(filename)
}

func (s *Service) ArtifactPath(version, filename string) (string, error) {
	manifest, e := s.manifestPath(version)
	if e != nil {
		return "", e
	}
	if !domain.SafeComponent(filename) || !filepath.IsLocal(filename) || strings.Contains(filename, ":") {
		return "", domain.Err("invalid_filename", "Filename must be a single safe path component.")
	}
	path := filepath.Join(filepath.Dir(manifest), filename)
	if st, e := os.Lstat(path); e == nil {
		if !st.Mode().IsRegular() {
			return "", domain.Err("unsafe_storage_path", "Artifact path is unsafe.", 409)
		}
	} else if !os.IsNotExist(e) {
		return "", e
	}
	return path, nil
}

func verifyArtifact(path string, a model.Artifact) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return e
	}
	if !st.Mode().IsRegular() || st.Size() != a.Size {
		return domain.Err("github_asset_integrity_failed", "Local artifact size does not match metadata.", 409)
	}
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return e
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), a.SHA256) {
		return domain.Err("github_asset_integrity_failed", "Local artifact digest does not match metadata.", 409)
	}
	return nil
}

// Packages are streamed to disk. The metadata size bounds the bytes written;
// retries truncate their staging file and never expose partial public files.
func (s *Service) fetchArtifact(ctx context.Context, value string, size int64, hash, path string) error {
	if size < 0 || !domain.HashValid(hash) {
		return domain.Err("invalid_github_digest", "Invalid artifact size or digest.")
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DisableCompression: true,
		DialContext:         (&net.Dialer{Timeout: s.Config.DownloadConnectTimeout}).DialContext,
		TLSHandshakeTimeout: s.Config.DownloadConnectTimeout, ResponseHeaderTimeout: s.Config.DownloadReadTimeout}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: s.Config.ArtifactDownloadTimeout, CheckRedirect: assetRedirect}
	for attempt := 0; attempt <= s.Config.DownloadRetries; attempt++ {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, value, nil)
		if e != nil {
			return e
		}
		req.Header.Set("User-Agent", "Axolotl-Update-Server")
		req.Header.Set("Accept-Encoding", "identity")
		resp, e := client.Do(req)
		if e == nil {
			if resp.StatusCode == http.StatusOK {
				f, openErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
				if openErr != nil {
					resp.Body.Close()
					return openErr
				}
				h := sha256.New()
				n, readErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, size))
				var extra int
				if readErr == nil {
					var b [1]byte
					extra, readErr = io.ReadFull(resp.Body, b[:])
					if readErr == io.EOF {
						readErr = nil
					}
				}
				resp.Body.Close()
				if readErr == nil && (n != size || extra != 0 || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), hash)) {
					f.Close()
					return domain.Err("github_asset_integrity_failed", "GitHub asset metadata does not match downloaded content.")
				}
				if readErr == nil {
					syncErr := f.Sync()
					closeErr := f.Close()
					if syncErr != nil {
						return syncErr
					}
					return closeErr
				}
				f.Close()
			} else {
				resp.Body.Close()
				if resp.StatusCode < 500 && resp.StatusCode != 429 {
					return domain.Err("github_asset_download_failed", "GitHub asset download failed.", 502)
				}
			}
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		if attempt < s.Config.DownloadRetries {
			timer := time.NewTimer(time.Second * time.Duration(1<<attempt))
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return domain.Err("github_asset_download_failed", "GitHub asset download failed.", 502)
}

func assetRedirect(req *http.Request, via []*http.Request) error {
	host := req.URL.Hostname()
	if len(via) > 3 || req.URL.Scheme != "https" || req.URL.User != nil || (req.URL.Port() != "" && req.URL.Port() != "443") ||
		(host != "github.com" && host != "release-assets.githubusercontent.com" && host != "objects.githubusercontent.com") {
		return fmt.Errorf("untrusted asset redirect")
	}
	return nil
}

func (s *Service) stageArtifacts(ctx context.Context, version, tag string, rows []model.Artifact) (staged string, err error) {
	if tag != "v"+version {
		return "", domain.Err("invalid_github_release", "Release tag must match the version.")
	}
	if _, e := s.manifestPath(version); e != nil {
		return "", e
	}
	root := filepath.Join(s.Config.DistRoot, ".staging")
	if e := safeDir(root); e != nil {
		return "", e
	}
	if e := os.MkdirAll(root, 0755); e != nil {
		return "", e
	}
	staged, err = os.MkdirTemp(root, "release-*")
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(staged)
		}
	}()
	for _, a := range rows {
		if !domain.ArtifactValid(model.Version{Version: version}, a) || a.Filename == "manifest.json" {
			return staged, domain.Err("invalid_catalog", "Invalid stored artifact metadata.")
		}
		path, e := s.ArtifactPath(version, a.Filename)
		if e != nil {
			return staged, e
		}
		if e = verifyArtifact(path, a); e == nil {
			continue
		} else if !os.IsNotExist(e) {
			return staged, e
		}
		url := GitHubAssetURL(tag, a.Filename)
		if e = validateAssetURL(url, tag, a.Filename); e != nil {
			return staged, e
		}
		target := filepath.Join(staged, a.Filename)
		if e = s.FetchArtifact(ctx, url, a.Size, a.SHA256, target); e != nil {
			return staged, e
		}
		if e = verifyArtifact(target, a); e != nil {
			return staged, e
		}
	}
	return staged, nil
}

// Only missing files are promoted. Existing bytes must already match metadata;
// publication rollback removes only files added by this operation.
func (s *Service) installArtifacts(version, staged string, rows []model.Artifact) (undo func(), err error) {
	added := []string{}
	undo = func() {
		for _, path := range added {
			if e := os.Remove(path); e != nil && !os.IsNotExist(e) {
				slog.Error("artifact rollback failed", "path", path, "error", e)
			}
		}
	}
	defer func() {
		if err != nil {
			undo()
		}
	}()
	if staged == "" {
		return undo, nil
	}
	for _, a := range rows {
		source := filepath.Join(staged, a.Filename)
		if _, e := os.Stat(source); os.IsNotExist(e) {
			continue
		} else if e != nil {
			return undo, e
		}
		target, e := s.ArtifactPath(version, a.Filename)
		if e != nil {
			return undo, e
		}
		if e = os.MkdirAll(filepath.Dir(target), 0755); e != nil {
			return undo, e
		}
		if e = os.Rename(source, target); e != nil {
			return undo, e
		}
		added = append(added, target)
	}
	return undo, nil
}

func retainedVersions(items []model.Version) map[string]bool {
	keep := map[string]bool{}
	counts := map[string]int{}
	// Versions() sorts by descending SemVer, independently of publication time.
	for _, v := range items {
		parsed, e := domain.ParseVersion(v.Version)
		if e != nil || v.Status != "published" || !domain.ChannelValid(v.Channel) ||
			(v.Channel == "release" && parsed.Prerelease() != "") || (v.Channel == "beta" && parsed.Prerelease() == "") {
			continue
		}
		if counts[v.Channel] < retainedPerChannel {
			keep[v.Version] = true
			counts[v.Channel]++
		}
	}
	return keep
}

// PruneDist retains metadata and manifests (needed for force-update operations).
// It only removes registered package files, never unknown paths/directories.
func (s *Service) PruneDist(ctx context.Context) ([]string, error) {
	removed := []string{}
	if s.Config.DistServer != "" {
		return removed, nil
	}
	items, e := s.Versions(ctx)
	if e != nil {
		return nil, e
	}
	for _, item := range items {
		unlock, e := s.Lock(item.Version)
		if e != nil {
			continue // A publication or admin mutation owns this version; next run retries.
		}
		e = func() error {
			defer unlock()
			s.commitMu.Lock()
			defer s.commitMu.Unlock()
			current, e := s.Versions(ctx)
			if e != nil {
				return e
			}
			if retainedVersions(current)[item.Version] {
				return nil
			}
			for _, v := range current {
				if v.Version != item.Version || v.Status != "published" {
					continue
				}
				for _, a := range v.Artifacts {
					if a.Filename == "manifest.json" || !domain.ArtifactValid(v, a) {
						continue
					}
					path, e := s.ArtifactPath(v.Version, a.Filename)
					if e != nil {
						return e
					}
					if e = os.Remove(path); e == nil {
						removed = append(removed, v.Version+"/"+a.Filename)
					} else if !os.IsNotExist(e) {
						return e
					}
				}
			}
			return nil
		}()
		if e != nil {
			return removed, e
		}
	}
	return removed, nil
}

// SyncDist is an offline maintenance command; only the six retained versions
// are downloaded. Existing files are hash-checked, never silently overwritten.
func (s *Service) SyncDist(ctx context.Context) ([]string, error) {
	if s.Config.DistServer != "" {
		return nil, fmt.Errorf("dist sync requires an empty DIST_SERVER")
	}
	items, e := s.Versions(ctx)
	if e != nil {
		return nil, e
	}
	keep := retainedVersions(items)
	synced := []string{}
	for _, v := range items {
		if !keep[v.Version] {
			continue
		}
		if e = s.syncVersion(ctx, v); e != nil {
			return synced, fmt.Errorf("sync %s: %w", v.Version, e)
		}
		synced = append(synced, v.Version)
	}
	_, e = s.PruneDist(ctx)
	return synced, e
}

func (s *Service) syncVersion(ctx context.Context, v model.Version) error {
	unlock, e := s.Lock(v.Version)
	if e != nil {
		return e
	}
	defer unlock()
	staged, e := s.stageArtifacts(ctx, v.Version, v.ReleaseTag, v.Artifacts)
	if e != nil {
		return e
	}
	defer os.RemoveAll(staged)
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	_, e = s.installArtifacts(v.Version, staged, v.Artifacts)
	return e
}
