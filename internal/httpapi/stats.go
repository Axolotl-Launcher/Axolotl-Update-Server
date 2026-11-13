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
			v.respond(c, 200, gin.H{"schema_version": 2, "scope": "origin", "retention_days": 14, "dimensions": analytics.DimensionNames(), "intervals": []string{"auto", "1m", "5m", "15m", "1h", "6h", "1d"}, "metrics": []string{"requests", "unique_ips", "unique_user_agents", "response_bytes", "request_bytes", "status_1xx", "status_2xx", "status_3xx", "status_4xx", "status_5xx", "error_rate", "avg_duration_ms", "max_duration_ms", "latency", "bot_requests", "rate_limited_requests", "access_denied_requests", "app_cache_hit_rate"}, "capabilities": gin.H{"traffic": true, "performance": true, "user_agents": true, "geoip_city": v.Service.Config.GeoIPCityDB != "" || v.Service.Config.GeoIPQQWryDB != "" || v.Service.Config.GeoIPAPIURL != "", "geoip_asn": v.Service.Config.GeoIPASNDB != "", "application_cache": true, "edge_cache": false, "edge_waf": false, "dns": false, "download_completion": false}, "collection_paths": []string{"/latest", "/api/*"}, "includes_stats_requests": v.Service.Config.AnalyticsIncludeStatsRequests, "unique_ip_semantics": "distinct network addresses, not people", "sampling": "none; asynchronous queue may drop events", "bytes_semantics": "HTTP body bytes read/written by the application, excluding headers, TLS and proxy compression", "latency_semantics": "handler duration, excluding proxy and network time", "unknown_metrics": "null fields and sample counts; pre-upgrade HTTP metrics cannot be reconstructed"})
			return
		}
		if kind == "status" {
			var data map[string]any
			var e error
			if v.Snapshots != nil {
				var snap *analytics.Snapshot
				snap, e = v.Snapshots.Load(ctx, 336)
				if e == nil && snap == nil {
					v.statsPending(c)
					return
				}
				if snap != nil {
					data = map[string]any{"retention_days": 14, "requests": snap.Overview.Requests, "requests_with_http_metrics": snap.Overview.StatusSamples, "oldest_request": snap.OldestRequest, "newest_request": snap.NewestRequest, "generated_at": snap.GeneratedAt, "scheduled_snapshot": true}
				}
			} else {
				data, e = v.statistics.RetentionStatus(ctx, v.Service.Now())
			}
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
		if v.Snapshots != nil && kind != "requests" && q.Limit <= 100 && (values.Get("interval") == "" || values.Get("interval") == "auto") {
			selectionTime := v.Service.Now()
			if values.Get("scheduled") == "true" {
				selectionTime = q.End
			}
			if hours := analytics.SnapshotRange(q, selectionTime); hours > 0 {
				v.statsSnapshot(c, ctx, q, kind, hours)
				return
			}
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

func (v *Server) statsPending(c *gin.Context) {
	c.Header("Retry-After", "5")
	v.respond(c, 202, gin.H{"status": "pending", "code": "stats_warming", "message": "Scheduled statistics are being prepared; retry shortly."})
}

func (v *Server) statsSnapshot(c *gin.Context, ctx context.Context, q analytics.Query, kind string, hours int) {
	dims := []string{"route", "status_code", "country", "browser", "os", "device_type", "channel", "client_version"}
	if kind == "dashboard" && c.Query("dimensions") != "" {
		dims = strings.Split(c.Query("dimensions"), ",")
	}
	if kind == "breakdown" {
		dims = []string{c.Query("dimension")}
	}
	if len(dims) > 8 {
		v.fail(c, domain.Err("invalid_stats_query", "A dashboard request supports at most 8 dimensions."))
		return
	}
	for _, dim := range dims {
		if _, ok := analytics.Dimensions[dim]; !ok {
			v.fail(c, domain.Err("invalid_stats_query", "Unsupported dimension."))
			return
		}
	}
	snap, err := v.Snapshots.Load(ctx, hours)
	if err != nil {
		v.statsFail(c, ctx, err)
		return
	}
	if snap == nil {
		v.statsPending(c)
		return
	}
	groups := map[string]analytics.Breakdown{}
	for _, dim := range dims {
		group := snap.Breakdowns[dim]
		if q.Limit > len(group.Items) && group.OtherRequests > 0 {
			// Only the top 100 values are stored. Larger requested lists retain
			// live query semantics instead of silently truncating the contract.
			v.fail(c, domain.Err("invalid_stats_query", "Scheduled breakdowns support limits up to 100."))
			return
		}
		group.Limit = q.Limit
		group.Items = group.Items[:min(len(group.Items), q.Limit)]
		shown := int64(0)
		for _, item := range group.Items {
			shown += item.Requests
		}
		group.OtherRequests = group.TotalRequests - shown
		groups[dim] = group
	}
	var data any
	switch kind {
	case "overview":
		data = snap.Overview
	case "timeseries":
		data = snap.Timeseries
	case "breakdown":
		data = groups[dims[0]]
	default:
		data = gin.H{"overview": snap.Overview, "timeseries": snap.Timeseries, "breakdowns": groups}
	}
	v.respond(c, 200, gin.H{"schema_version": 2, "scope": "origin", "generated_at": snap.GeneratedAt, "window": snap.Query.Window(), "data": data, "cache": gin.H{"scheduled": true, "refresh_seconds": 3600, "stale": time.Since(snap.GeneratedAt) > time.Hour}})
}
func (v *Server) statsFail(c *gin.Context, ctx context.Context, e error) {
	if errors.Is(e, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		v.fail(c, domain.Err("stats_timeout", "Statistics query timed out; narrow the time range or filters.", 503))
		return
	}
	v.fail(c, e)
}
