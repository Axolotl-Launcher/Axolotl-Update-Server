package httpapi

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/analytics"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/domain"
	"github.com/gin-gonic/gin"
)

func (v *Server) statsHandler(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		if !v.admin(c) {
			return
		}
		select {
		case v.statsGate <- struct{}{}:
			defer func() { <-v.statsGate }()
		default:
			v.fail(c, domain.Err("stats_busy", "Statistics queries are busy; retry shortly.", 429))
			return
		}
		timeout := v.Service.Config.StatsQueryTimeout
		if timeout <= 0 {
			timeout = 15 * time.Second
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		defer cancel()
		if kind == "meta" {
			v.respond(c, 200, gin.H{"schema_version": 2, "scope": "origin", "retention_days": 14, "dimensions": analytics.DimensionNames(), "intervals": []string{"auto", "1m", "5m", "15m", "1h", "6h", "1d"}, "metrics": []string{"requests", "unique_ips", "unique_user_agents", "response_bytes", "request_bytes", "status_1xx", "status_2xx", "status_3xx", "status_4xx", "status_5xx", "error_rate", "avg_duration_ms", "max_duration_ms", "latency", "bot_requests", "rate_limited_requests", "access_denied_requests", "app_cache_hit_rate"}, "capabilities": gin.H{"traffic": true, "performance": true, "user_agents": true, "geoip_city": v.Service.Config.GeoIPCityDB != "", "geoip_asn": v.Service.Config.GeoIPASNDB != "", "application_cache": true, "edge_cache": false, "edge_waf": false, "dns": false, "download_completion": false}, "collection_paths": []string{"/latest", "/api/*"}, "includes_stats_requests": v.Service.Config.AnalyticsIncludeStatsRequests, "unique_ip_semantics": "distinct network addresses, not people", "sampling": "none; asynchronous queue may drop events", "bytes_semantics": "HTTP body bytes read/written by the application, excluding headers, TLS and proxy compression", "latency_semantics": "handler duration, excluding proxy and network time", "unknown_metrics": "null fields and sample counts; pre-upgrade HTTP metrics cannot be reconstructed"})
			return
		}
		if kind == "status" {
			data, e := v.statistics.RetentionStatus(ctx, v.Service.Now())
			if e != nil {
				v.statsFail(c, ctx, e)
				return
			}
			data["enabled"] = v.Service.Config.AnalyticsEnabled
			data["scope"] = "origin"
			data["collector_attached"] = v.Collector != nil
			if v.Collector != nil {
				data["collector"] = v.Collector.Stats()
			}
			v.respond(c, 200, gin.H{"schema_version": 2, "data": data})
			return
		}
		values, parseError := url.ParseQuery(c.Request.URL.RawQuery)
		if parseError != nil {
			v.fail(c, domain.Err("invalid_stats_query", "Malformed query parameters."))
			return
		}
		q, e := analytics.ParseQuery(values, v.Service.Now())
		if e != nil {
			v.fail(c, e)
			return
		}
		var data any
		switch kind {
		case "overview":
			data, e = v.statistics.Overview(ctx, q)
		case "timeseries":
			data, e = v.statistics.Timeseries(ctx, q)
		case "breakdown":
			data, e = v.statistics.Breakdown(ctx, q, c.Query("dimension"))
		case "requests":
			if c.Query("limit") == "" {
				q.Limit = 100
			}
			data, e = v.statistics.Requests(ctx, q)
		case "dashboard":
			dimensions := []string{"route", "status_code", "country", "browser", "os", "device_type", "channel", "client_version"}
			if raw := c.Query("dimensions"); raw != "" {
				dimensions = strings.Split(raw, ",")
			}
			if len(dimensions) > 8 {
				v.fail(c, domain.Err("invalid_stats_query", "A dashboard request supports at most 8 dimensions."))
				return
			}
			for _, dim := range dimensions {
				if _, ok := analytics.Dimensions[dim]; !ok {
					v.fail(c, domain.Err("invalid_stats_query", "Unsupported dashboard dimension."))
					return
				}
			}
			var overview analytics.Overview
			var points []analytics.TimePoint
			groups := map[string]analytics.Breakdown{}
			overview, e = v.statistics.Overview(ctx, q)
			if e == nil {
				points, e = v.statistics.Timeseries(ctx, q)
			}
			if e == nil {
				groups, e = v.statistics.Breakdowns(ctx, q, dimensions)
			}
			data = gin.H{"overview": overview, "timeseries": points, "breakdowns": groups}
		}
		if e != nil {
			v.statsFail(c, ctx, e)
			return
		}
		v.respond(c, 200, gin.H{"schema_version": 2, "scope": "origin", "generated_at": v.Service.Now(), "window": q.Window(), "data": data})
	}
}
func (v *Server) statsFail(c *gin.Context, ctx context.Context, e error) {
	if errors.Is(e, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		v.fail(c, domain.Err("stats_timeout", "Statistics query timed out; narrow the time range or filters.", 503))
		return
	}
	v.fail(c, e)
}
