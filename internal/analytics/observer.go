package analytics

import (
	"crypto/tls"
	"mime"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/domain"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
	"github.com/gin-gonic/gin"
)

func (c *Collector) Observe(ctx *gin.Context, at time.Time) {
	if e, ok := Extract(ctx, at); ok {
		c.Enqueue(e)
	}
}
func Extract(c *gin.Context, at time.Time) (Event, bool) {
	path := c.Request.URL.Path
	if path != "/latest" && !strings.HasPrefix(path, "/api/") {
		return Event{}, false
	}
	if (path == "/api/admin/stats" || strings.HasPrefix(path, "/api/admin/stats/")) && !c.GetBool("analytics_include_stats") {
		return Event{}, false
	}
	route := c.FullPath()
	if route == "" {
		route = "unmatched"
	}
	e := Event{OccurredAt: at.UTC(), Route: route, UserAgent: c.GetHeader("User-Agent")}
	e.Method = model.Ptr(c.Request.Method)
	e.StatusCode = model.Ptr(c.Writer.Status())
	e.ResponseBytes = model.Ptr(int64(max(0, c.Writer.Size())))
	e.HTTPVersion = model.Ptr(c.Request.Proto)
	e.Host = normalizedHost(c.Request.Host)
	if scheme := c.GetString("client_scheme"); scheme != "" {
		e.Scheme = &scheme
	}
	if value, ok := c.Get("analytics_duration_us"); ok {
		if n, ok := value.(int64); ok {
			e.DurationUS = &n
		}
	}
	if value, ok := c.Get("analytics_request_bytes"); ok {
		if n, ok := value.(int64); ok {
			e.RequestBytes = &n
		}
	}
	if c.Request.TLS != nil {
		version := tls.VersionName(c.Request.TLS.Version)
		e.TLSVersion = &version
	}
	if media, _, err := mime.ParseMediaType(c.Writer.Header().Get("Content-Type")); err == nil {
		e.ContentType = &media
	}
	if ref, err := url.Parse(c.GetHeader("Referer")); err == nil && (ref.Scheme == "http" || ref.Scheme == "https") {
		e.RefererHost = normalizedHost(ref.Host)
	}
	if cache := c.GetString("app_cache"); cache != "" {
		e.AppCache = &cache
	}
	if code := c.GetString("error_code"); code != "" {
		e.ErrorCode = &code
	}
	if ip := c.GetString("client_ip"); ip != "" {
		e.ClientIP = model.Ptr(ip)
	}
	variant := func(header, key, def string) string {
		if v := c.GetHeader(header); v != "" {
			return v
		}
		return c.DefaultQuery(key, def)
	}
	if route == "/latest" {
		if ch := variant("X-Axolotl-Channel", "channel", "release"); domain.ChannelValid(ch) {
			e.Channel = &ch
		}
		if p := variant("X-Axolotl-Platform", "platform", ""); domain.Platforms[p] {
			e.Platform = &p
		}
		if version := variant("X-Axolotl-Version", "current_version", ""); version != "" {
			if _, err := domain.ParseVersion(version); err == nil {
				e.ClientVersion = &version
			}
		}
	}
	if route == "/api/downloads/latest" {
		if ch := c.DefaultQuery("channel", "release"); domain.ChannelValid(ch) {
			e.Channel = &ch
		}
	}
	if route == "/api/downloads/latest" || route == "/api/downloads/:version" {
		parse := func(key string) *bool {
			raw := c.DefaultQuery(key, "false")
			if strings.EqualFold(raw, "true") {
				return model.Ptr(true)
			}
			if strings.EqualFold(raw, "false") {
				return model.Ptr(false)
			}
			return nil
		}
		e.IncludeUpdater = parse("include_updater")
		e.IncludeRevoked = parse("include_revoked")
	}
	if version := c.Param("version"); version != "" {
		if _, err := domain.ParseVersion(version); err == nil {
			e.TargetVersion = &version
		}
	}
	return e, true
}

func normalizedHost(raw string) *string {
	host := raw
	if h, _, err := net.SplitHostPort(raw); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]."))
	if host == "" || strings.ContainsAny(host, "/\\\x00\r\n ") {
		return nil
	}
	return &host
}
