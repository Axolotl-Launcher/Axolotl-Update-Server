package analytics

import (
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
	route := c.FullPath()
	if route == "" {
		route = "unmatched"
	}
	e := Event{OccurredAt: at.UTC(), Route: route, UserAgent: c.GetHeader("User-Agent")}
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
