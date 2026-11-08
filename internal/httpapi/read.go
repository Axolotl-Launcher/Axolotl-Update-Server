package httpapi

import (
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/config"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/domain"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
	"github.com/gin-gonic/gin"
)

func (v *Server) latest(c *gin.Context) {
	channel := variant(c, "X-Axolotl-Channel", "channel", "release")
	platform := variant(c, "X-Axolotl-Platform", "platform", "")
	current := variant(c, "X-Axolotl-Version", "current_version", "")
	if !domain.ChannelValid(channel) {
		v.fail(c, domain.Err("invalid_channel", "Only release and beta channels are supported."))
		return
	}
	if current != "" {
		if _, e := domain.ParseVersion(current); e != nil {
			v.fail(c, e)
			return
		}
	}
	payload, e := v.Service.Latest(c.Request.Context(), channel, platform, current)
	if e != nil {
		v.fail(c, e)
		return
	}
	if payload == nil {
		c.Status(204)
		v.latestHeaders(c)
		return
	}
	tag := etag(payload)
	c.Header("ETag", `"`+tag+`"`)
	for _, candidate := range strings.Split(c.GetHeader("If-None-Match"), ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == `"`+tag+`"` {
			c.Status(304)
			v.latestHeaders(c)
			return
		}
	}
	c.Status(200)
	v.latestHeaders(c)
	v.respond(c, 200, payload)
}
func (v *Server) versions(c *gin.Context) {
	items, e := v.Service.Versions(c.Request.Context())
	if e != nil {
		v.fail(c, e)
		return
	}
	v.respond(c, 200, gin.H{"versions": items})
}
func (v *Server) version(c *gin.Context) {
	item, e := v.Service.Version(c.Request.Context(), c.Param("version"))
	if e != nil {
		v.fail(c, e)
		return
	}
	v.respond(c, 200, item)
}

func (v *Server) downloadPayload(item model.Version, include bool) gin.H {
	artifacts := []model.Artifact{}
	for _, a := range item.Artifacts {
		if !a.IsPublic || a.Kind == "signature" || (!include && a.Kind == "updater") || !domain.ArtifactValid(item, a) {
			continue
		}
		artifacts = append(artifacts, a)
	}
	order := func(m map[string]int, k string) int {
		if n, ok := m[k]; ok {
			return n
		}
		return 99
	}
	po := map[string]int{"windows": 0, "macos": 1, "linux": 2}
	ko := map[string]int{"installer": 0, "portable": 1, "updater": 2}
	sort.SliceStable(artifacts, func(i, j int) bool {
		a, b := artifacts[i], artifacts[j]
		if x, y := order(po, a.Platform), order(po, b.Platform); x != y {
			return x < y
		}
		if x, y := order(ko, a.Kind), order(ko, b.Kind); x != y {
			return x < y
		}
		if a.SortOrder != b.SortOrder {
			return a.SortOrder < b.SortOrder
		}
		return label(a) < label(b)
	})
	downloads := []gin.H{}
	for _, a := range artifacts {
		downloads = append(downloads, gin.H{"id": a.ID, "kind": a.Kind, "platform": a.Platform, "target_platforms": a.TargetPlatforms, "architecture": a.Architecture, "variant": a.Variant, "label": label(a), "display_name": label(a), "sort_order": a.SortOrder, "filename": a.Filename, "url": v.Service.Config.PublicBaseURL + "/" + a.RelativePath, "size": a.Size, "sha256": a.SHA256, "signature": a.Signature, "signature_filename": a.SignatureFilename})
	}
	return gin.H{"version": item.Version, "channel": item.Channel, "status": item.Status, "published_at": item.PublishedAt, "force_update": item.ForceUpdate, "downloads": downloads}
}
func label(a model.Artifact) string {
	if a.DisplayName != "" {
		return a.DisplayName
	}
	return a.Filename
}
func boolQuery(c *gin.Context, key string) bool {
	return strings.EqualFold(c.DefaultQuery(key, "false"), "true")
}
func (v *Server) downloadLatest(c *gin.Context) {
	channel := c.DefaultQuery("channel", "release")
	if !domain.ChannelValid(channel) {
		v.fail(c, domain.Err("invalid_channel", "Only release and beta channels are supported."))
		return
	}
	if boolQuery(c, "include_revoked") && !v.admin(c) {
		return
	}
	include := boolQuery(c, "include_updater")
	items, e := v.Service.Versions(c.Request.Context())
	if e != nil {
		v.fail(c, e)
		return
	}
	for _, item := range items {
		if !domain.Eligible(item, channel) || item.PublishedAt == nil {
			continue
		}
		complete := false
		for _, a := range item.Artifacts {
			if a.IsPublic && (a.Kind == "installer" || a.Kind == "portable" || (include && a.Kind == "updater")) && domain.ArtifactValid(item, a) {
				complete = true
				break
			}
		}
		if complete {
			v.catalogResponse(c, v.downloadPayload(item, include))
			return
		}
	}
	c.Status(204)
}
func (v *Server) downloadVersion(c *gin.Context) {
	name := c.Param("version")
	if _, e := domain.ParseVersion(name); e != nil {
		v.fail(c, e)
		return
	}
	revoked := boolQuery(c, "include_revoked")
	if revoked && !v.admin(c) {
		return
	}
	item, e := v.Service.Version(c.Request.Context(), name)
	if e != nil {
		v.fail(c, e)
		return
	}
	if item.Status != "published" && !(revoked && item.Status == "revoked") {
		v.fail(c, domain.Err("version_not_found", "Published version does not exist.", 404))
		return
	}
	v.catalogResponse(c, v.downloadPayload(item, boolQuery(c, "include_updater")))
}
func (v *Server) catalogResponse(c *gin.Context, p gin.H) {
	c.Header("ETag", etag(p))
	c.Header("Cache-Control", "no-cache, max-age=0, must-revalidate")
	v.respond(c, 200, p)
}

func (v *Server) dist(c *gin.Context) {
	version := c.Param("version")
	filename := strings.TrimPrefix(c.Param("filename"), "/")
	for _, part := range append([]string{version}, strings.Split(filename, "/")...) {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00\r\n") {
			v.fail(c, domain.Err("invalid_dist_path", "Distribution path contains unsafe components."))
			return
		}
		for _, r := range part {
			if r < 32 || r == 127 {
				v.fail(c, domain.Err("invalid_dist_path", "Distribution path contains unsafe components."))
				return
			}
		}
	}
	if v.Service.Config.DistServer != "" {
		base, _ := url.Parse(v.Service.Config.DistServer)
		requestURL := &url.URL{Host: c.Request.Host, Scheme: "https"}
		if config.SameEndpoint(base, requestURL) && base.Path == "" {
			v.fail(c, domain.Err("dist_server_loop", "DIST_SERVER points back to this distribution endpoint.", 503))
			return
		}
		base.Path = strings.TrimRight(base.Path, "/") + "/dist/" + version + "/" + filename
		base.RawPath = ""
		base.RawQuery = c.Request.URL.RawQuery
		c.Header("Cache-Control", "no-store")
		c.Header("Cloudflare-CDN-Cache-Control", "no-store")
		c.Redirect(302, base.String())
		return
	}
	if filename != "manifest.json" {
		v.fail(c, domain.Err("artifact_not_found", "The requested artifact does not exist.", 404))
		return
	}
	if _, e := domain.ParseVersion(version); e != nil {
		v.fail(c, domain.Err("artifact_not_found", "The requested artifact does not exist.", 404))
		return
	}
	// Rebuild/management validates paths as well; do not follow a directory or manifest link here.
	path, e := v.Service.ManifestPath(version)
	if e != nil {
		v.fail(c, e)
		return
	}
	f, e := os.Open(path)
	if e != nil {
		v.fail(c, domain.Err("artifact_not_found", "The requested artifact does not exist.", 404))
		return
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		v.fail(c, e)
		return
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(c.Writer, c.Request, "manifest.json", st.ModTime(), f)
}
