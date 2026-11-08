package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/analytics"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/domain"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Observer func(*gin.Context, time.Time)
type bucket struct {
	times []time.Time
	last  time.Time
}
type Server struct {
	Service    *service.Service
	Router     *gin.Engine
	observer   Observer
	ip         *IPResolver
	mu         sync.Mutex
	buckets    map[string]bucket
	lastSweep  time.Time
	statistics *analytics.Statistics
	statsGate  chan struct{}
	Collector  *analytics.Collector
}

func New(s *service.Service, observer Observer) *Server {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	v := &Server{Service: s, Router: r, observer: observer, ip: NewIPResolver(s.Config.TrustedProxyCIDRs), buckets: map[string]bucket{}}
	v.statistics = &analytics.Statistics{Store: s.Store}
	v.statsGate = make(chan struct{}, 2)
	r.RedirectTrailingSlash = false
	r.RedirectFixedPath = false
	r.HandleMethodNotAllowed = true
	r.Use(v.middleware())
	read := func(path string, h gin.HandlerFunc) { r.GET(path, h); r.HEAD(path, h) }
	read("/api/health", func(c *gin.Context) { v.respond(c, 200, gin.H{"status": "ok", "service": "axolotl-update-server"}) })
	read("/latest", v.latest)
	read("/api/versions", v.versions)
	read("/api/versions/:version", v.version)
	read("/api/downloads/latest", v.downloadLatest)
	read("/api/downloads/:version", v.downloadVersion)
	r.OPTIONS("/api/downloads/:version", func(c *gin.Context) { c.Status(200) })
	read("/dist/:version/*filename", v.dist)
	r.POST("/api/webhook/release", v.webhook)
	r.POST("/api/admin/versions/:version/revoke", v.revoke)
	r.POST("/api/admin/versions/:version/restore", v.restore)
	r.PATCH("/api/admin/versions/:version/force-update", v.forceUpdate)
	r.DELETE("/api/admin/versions/:version", v.deleteVersion)
	read("/api/admin/audit-logs", v.audit)
	read("/api/admin/stats", v.statsHandler("dashboard"))
	for _, kind := range []string{"overview", "timeseries", "breakdown", "requests", "meta", "status"} {
		read("/api/admin/stats/"+kind, v.statsHandler(kind))
	}
	r.NoRoute(func(c *gin.Context) {
		v.fail(c, domain.Err("not_found", "The requested endpoint does not exist.", 404))
	})
	r.NoMethod(func(c *gin.Context) { v.fail(c, domain.Err("method_not_allowed", "Method is not allowed.", 405)) })
	return v
}
func (v *Server) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := v.Service.Now()
		realStart := time.Now()
		body := &countedBody{ReadCloser: c.Request.Body}
		c.Request.Body = body
		c.Set("analytics_include_stats", v.Service.Config.AnalyticsIncludeStatsRequests)
		scheme := "http"
		if c.Request.TLS != nil {
			scheme = "https"
		} else if v.ip.TrustedPeer(c.Request.RemoteAddr) {
			if forwarded := c.GetHeader("X-Forwarded-Proto"); forwarded == "http" || forwarded == "https" {
				scheme = forwarded
			}
		}
		c.Set("client_scheme", scheme)
		rid := c.GetHeader("X-Request-ID")
		if len(rid) == 0 || len(rid) > 64 {
			rid = uuid.NewString()
		}
		c.Set("request_id", rid)
		c.Header("X-Request-ID", rid)
		c.Set("client_ip", v.ip.Resolve(c.Request.RemoteAddr, c.GetHeader("X-Forwarded-For"), c.GetHeader("CF-Connecting-IP")))
		if strings.HasPrefix(c.Request.URL.Path, "/api/downloads/") && (c.Request.Method == "GET" || c.Request.Method == "HEAD" || c.Request.Method == "OPTIONS") {
			c.Header("Access-Control-Allow-Origin", "*")
			c.Header("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Content-Type")
			c.Header("Access-Control-Expose-Headers", "ETag, X-Request-ID")
		}
		defer func() {
			if p := recover(); p != nil {
				slog.Error("request panic", "request_id", rid, "panic", p)
				if !c.Writer.Written() {
					v.fail(c, domain.Err("internal_error", "An internal error occurred.", 500))
				}
			}
			if c.Request.URL.Path == "/latest" {
				v.latestHeaders(c)
			}
			if v.observer != nil {
				c.Set("analytics_duration_us", max(int64(0), time.Since(realStart).Microseconds()))
				c.Set("analytics_request_bytes", body.Count)
				v.observer(c, start)
			}
		}()
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, v.Service.Config.MaxBodyBytes)
		if strings.HasPrefix(c.Request.URL.Path, "/api/") && !v.allow(c.GetString("client_ip"), start) {
			v.fail(c, domain.Err("rate_limited", "Too many requests; retry later.", 429))
			c.Abort()
			return
		}
		c.Next()
	}
}
func (v *Server) allow(ip string, now time.Time) bool {
	limit := v.Service.Config.RateLimit
	if limit == 0 {
		return true
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if now.Sub(v.lastSweep) >= time.Minute {
		for k, b := range v.buckets {
			if now.Sub(b.last) >= time.Minute {
				delete(v.buckets, k)
			}
		}
		v.lastSweep = now
	}
	b := v.buckets[ip]
	i := 0
	for i < len(b.times) && now.Sub(b.times[i]) >= time.Minute {
		i++
	}
	b.times = append([]time.Time(nil), b.times[i:]...)
	b.last = now
	if len(b.times) >= limit {
		v.buckets[ip] = b
		return false
	}
	b.times = append(b.times, now)
	v.buckets[ip] = b
	return true
}
func (v *Server) respond(c *gin.Context, status int, value any) {
	data, e := json.Marshal(value)
	if e != nil {
		v.fail(c, e)
		return
	}
	c.Status(status)
	if c.Request.URL.Path == "/latest" {
		v.latestHeaders(c)
	}
	c.Header("Content-Type", "application/json; charset=utf-8")
	if c.Request.Method == "HEAD" {
		c.Header("Content-Length", strconv.Itoa(len(data)))
		return
	}
	c.Data(status, "application/json; charset=utf-8", data)
}
func (v *Server) fail(c *gin.Context, e error) {
	var api *domain.Error
	if !errors.As(e, &api) {
		slog.Error("request failed", "request_id", c.GetString("request_id"), "error", e)
		api = domain.Err("internal_error", "An internal error occurred.", 500)
	}
	c.Set("error_code", api.Code)
	v.respond(c, api.Status, gin.H{"error": gin.H{"code": api.Code, "message": api.Message, "request_id": c.GetString("request_id")}})
}

type countedBody struct {
	io.ReadCloser
	Count int64
}

func (b *countedBody) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	b.Count += int64(n)
	return n, e
}
func (v *Server) admin(c *gin.Context) bool {
	expected := v.Service.Config.AdminToken
	if expected == "" {
		v.fail(c, domain.Err("auth_not_configured", "Authentication is not configured.", 503))
		return false
	}
	supplied := c.GetHeader("Authorization")
	if !strings.HasPrefix(supplied, "Bearer ") || !hmac.Equal([]byte(strings.TrimPrefix(supplied, "Bearer ")), []byte(expected)) {
		v.fail(c, domain.Err("invalid_admin_token", "Authentication required.", 401))
		return false
	}
	return true
}
func readBody(c *gin.Context) ([]byte, error) {
	b, e := io.ReadAll(c.Request.Body)
	var max *http.MaxBytesError
	if errors.As(e, &max) {
		return nil, domain.Err("payload_too_large", "Request body exceeds the configured limit.", 413)
	}
	return b, e
}
func etag(value any) string {
	b, _ := json.Marshal(value)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func variant(c *gin.Context, header, query, def string) string {
	if s := c.GetHeader(header); s != "" {
		return s
	}
	return c.DefaultQuery(query, def)
}
func (v *Server) latestHeaders(c *gin.Context) {
	c.Header("Vary", "X-Axolotl-Channel, X-Axolotl-Platform, X-Axolotl-Version")
	status := c.Writer.Status()
	if (status != 200 && status != 204 && status != 304) || c.GetHeader("Authorization") != "" || c.GetHeader("Cookie") != "" {
		c.Header("Cache-Control", "no-store")
		c.Header("Cloudflare-CDN-Cache-Control", "no-store")
		return
	}
	headers := c.GetHeader("X-Axolotl-Channel") != "" || c.GetHeader("X-Axolotl-Platform") != "" || c.GetHeader("X-Axolotl-Version") != ""
	if headers && !v.Service.Config.CDNHeaderCacheKey {
		c.Header("Cache-Control", "private, max-age=0, must-revalidate")
		c.Header("Cloudflare-CDN-Cache-Control", "no-store")
		return
	}
	ttl := strconv.Itoa(int(v.Service.Config.LatestCacheTTL.Seconds()))
	c.Header("Cache-Control", "public, max-age=0, s-maxage="+ttl+", must-revalidate")
	c.Header("Cloudflare-CDN-Cache-Control", "public, max-age="+ttl)
}
