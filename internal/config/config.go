package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Env, DatabaseURL, DistRoot, PublicBaseURL, DistServer, Host, Port string
	AdminToken, WebhookSecret                                         string
	DistCloudflareOnly                                                bool
	DistOriginSecret                                                  string
	TrustedProxyCIDRs                                                 []string
	MaxBodyBytes                                                      int64
	WebhookMaxAge, LatestCacheTTL                                     time.Duration
	CDNHeaderCacheKey                                                 bool
	RateLimit                                                         int
	DownloadConnectTimeout, DownloadReadTimeout                       time.Duration
	DownloadRetries                                                   int
	ArtifactDownloadTimeout                                           time.Duration
	AnalyticsEnabled                                                  bool
	AnalyticsQueueSize, AnalyticsBatchSize                            int
	AnalyticsFlushInterval, AnalyticsShutdownTimeout                  time.Duration
	GeoIPCityDB, GeoIPASNDB                                           string
	AnalyticsIncludeStatsRequests                                     bool
	StatsQueryTimeout                                                 time.Duration
}

var CloudflareCIDRs = []string{
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22", "2400:cb00::/32",
	"2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32",
	"2a06:98c0::/29", "2c0f:f248::/32",
}

func Default() Config {
	return Config{Env: "development", DatabaseURL: "sqlite://instance/update-server.db", DistRoot: "dist", PublicBaseURL: "https://update.axlmc.org", Host: "127.0.0.1", Port: "8082",
		TrustedProxyCIDRs: []string{"127.0.0.1/32", "::1/128"}, MaxBodyBytes: 512 << 20,
		WebhookMaxAge: 300 * time.Second, LatestCacheTTL: 30 * time.Second, RateLimit: 120,
		DownloadConnectTimeout: 10 * time.Second, DownloadReadTimeout: 60 * time.Second, DownloadRetries: 3, ArtifactDownloadTimeout: 30 * time.Minute, DistCloudflareOnly: true,
		AnalyticsEnabled: true, AnalyticsQueueSize: 10000, AnalyticsBatchSize: 500, AnalyticsFlushInterval: time.Second, AnalyticsShutdownTimeout: 10 * time.Second, StatsQueryTimeout: 15 * time.Second}
}

func Load() (Config, error) {
	_ = godotenv.Load()
	c := Default()
	c.DistOriginSecret = os.Getenv("DIST_ORIGIN_SECRET")
	if raw, ok := os.LookupEnv("DIST_CLOUDFLARE_ONLY"); ok {
		b, e := strconv.ParseBool(raw)
		if e != nil {
			return c, fmt.Errorf("DIST_CLOUDFLARE_ONLY must be a boolean")
		}
		c.DistCloudflareOnly = b
	}
	if raw, ok := os.LookupEnv("GITHUB_ARTIFACT_DOWNLOAD_TIMEOUT_SECONDS"); ok {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 86400 {
			return c, fmt.Errorf("GITHUB_ARTIFACT_DOWNLOAD_TIMEOUT_SECONDS must be between 1 and 86400")
		}
		c.ArtifactDownloadTimeout = time.Duration(n) * time.Second
	}
	c.GeoIPCityDB = os.Getenv("ANALYTICS_GEOIP_CITY_DB")
	c.GeoIPASNDB = os.Getenv("ANALYTICS_GEOIP_ASN_DB")
	if raw, ok := os.LookupEnv("ANALYTICS_INCLUDE_STATS_REQUESTS"); ok {
		b, e := strconv.ParseBool(raw)
		if e != nil {
			return c, fmt.Errorf("ANALYTICS_INCLUDE_STATS_REQUESTS must be a boolean")
		}
		c.AnalyticsIncludeStatsRequests = b
	}
	if raw, ok := os.LookupEnv("STATS_QUERY_TIMEOUT_SECONDS"); ok {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 120 {
			return c, fmt.Errorf("STATS_QUERY_TIMEOUT_SECONDS must be between 1 and 120")
		}
		c.StatsQueryTimeout = time.Duration(n) * time.Second
	}
	for key, dst := range map[string]*string{"APP_ENV": &c.Env, "DATABASE_URL": &c.DatabaseURL, "DIST_ROOT": &c.DistRoot, "PUBLIC_BASE_URL": &c.PublicBaseURL, "DIST_SERVER": &c.DistServer, "UPDATE_SERVER_HOST": &c.Host, "UPDATE_SERVER_PORT": &c.Port, "ADMIN_TOKEN": &c.AdminToken, "WEBHOOK_SECRET": &c.WebhookSecret} {
		if v, ok := os.LookupEnv(key); ok {
			*dst = v
		}
	}
	if v, ok := os.LookupEnv("TRUSTED_PROXY_CIDRS"); ok {
		c.TrustedProxyCIDRs = nil
		for _, cidr := range strings.Split(v, ",") {
			if cidr = strings.TrimSpace(cidr); cidr != "" {
				c.TrustedProxyCIDRs = append(c.TrustedProxyCIDRs, cidr)
			}
		}
	}
	for key, dst := range map[string]*int{"RATE_LIMIT_PER_MINUTE": &c.RateLimit, "GITHUB_DOWNLOAD_RETRIES": &c.DownloadRetries, "ANALYTICS_QUEUE_SIZE": &c.AnalyticsQueueSize, "ANALYTICS_BATCH_SIZE": &c.AnalyticsBatchSize} {
		if v, ok := os.LookupEnv(key); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return c, fmt.Errorf("%s must be an integer", key)
			}
			*dst = n
		}
	}
	for key, dst := range map[string]*time.Duration{"WEBHOOK_MAX_AGE_SECONDS": &c.WebhookMaxAge, "LATEST_CACHE_SECONDS": &c.LatestCacheTTL, "GITHUB_DOWNLOAD_CONNECT_TIMEOUT_SECONDS": &c.DownloadConnectTimeout, "GITHUB_DOWNLOAD_READ_TIMEOUT_SECONDS": &c.DownloadReadTimeout, "ANALYTICS_FLUSH_SECONDS": &c.AnalyticsFlushInterval, "ANALYTICS_SHUTDOWN_SECONDS": &c.AnalyticsShutdownTimeout} {
		if v, ok := os.LookupEnv(key); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return c, fmt.Errorf("%s must be integer seconds", key)
			}
			*dst = time.Duration(n) * time.Second
		}
	}
	for key, dst := range map[string]*bool{"LATEST_CDN_HEADER_CACHE_KEY": &c.CDNHeaderCacheKey, "ANALYTICS_ENABLED": &c.AnalyticsEnabled} {
		if v, ok := os.LookupEnv(key); ok {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return c, fmt.Errorf("%s must be a boolean", key)
			}
			*dst = b
		}
	}
	if v, ok := os.LookupEnv("MAX_UPLOAD_SIZE"); ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return c, fmt.Errorf("MAX_UPLOAD_SIZE must be an integer")
		}
		c.MaxBodyBytes = n
	}
	return c, c.Validate()
}

func (c *Config) Validate() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	if c.ArtifactDownloadTimeout <= 0 {
		return fmt.Errorf("invalid artifact download timeout")
	}
	if c.DistOriginSecret != "" && (len(c.DistOriginSecret) < 32 || strings.ContainsAny(c.DistOriginSecret, " \t\r\n")) {
		return fmt.Errorf("DIST_ORIGIN_SECRET must contain at least 32 characters without whitespace")
	}
	if c.Env != "development" && c.Env != "test" && c.Env != "production" {
		return fmt.Errorf("APP_ENV must be development, test or production")
	}
	if c.Env == "production" {
		if !strings.HasPrefix(c.DatabaseURL, "postgres://") && !strings.HasPrefix(c.DatabaseURL, "postgresql://") {
			return fmt.Errorf("production requires PostgreSQL")
		}
		for name, value := range map[string]string{"ADMIN_TOKEN": c.AdminToken, "WEBHOOK_SECRET": c.WebhookSecret} {
			if value == "" || strings.HasPrefix(value, "change-") || value == "dev-only-change-me" {
				return fmt.Errorf("production requires a non-placeholder %s", name)
			}
		}
	}
	if c.AnalyticsQueueSize < 1 || c.AnalyticsBatchSize < 1 || c.AnalyticsBatchSize > 1000 || c.AnalyticsFlushInterval <= 0 || c.AnalyticsShutdownTimeout <= 0 || c.MaxBodyBytes <= 0 || c.RateLimit < 0 || c.DownloadRetries < 0 || c.DownloadRetries > 10 || c.LatestCacheTTL < 0 || c.WebhookMaxAge <= 0 || c.DownloadConnectTimeout <= 0 || c.DownloadReadTimeout <= 0 {
		return fmt.Errorf("invalid limits, batch size (1..1000), or timeouts")
	}
	if p, err := strconv.Atoi(c.Port); err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("invalid UPDATE_SERVER_PORT")
	}
	for _, s := range c.TrustedProxyCIDRs {
		if _, _, err := net.ParseCIDR(s); err != nil {
			return fmt.Errorf("invalid trusted proxy CIDR: %s", s)
		}
	}
	c.PublicBaseURL = strings.TrimRight(c.PublicBaseURL, "/")
	if _, err := validURL(c.PublicBaseURL); err != nil {
		return fmt.Errorf("invalid PUBLIC_BASE_URL: %w", err)
	}
	c.DistServer = strings.TrimRight(strings.TrimSpace(c.DistServer), "/")
	if c.DistServer != "" {
		u, err := validURL(c.DistServer)
		if err != nil {
			return fmt.Errorf("invalid DIST_SERVER: %w", err)
		}
		p, _ := url.Parse(c.PublicBaseURL)
		if SameEndpoint(u, p) && u.Path == p.Path {
			return fmt.Errorf("DIST_SERVER points to PUBLIC_BASE_URL")
		}
	}
	root, err := filepath.Abs(c.DistRoot)
	if err != nil {
		return err
	}
	c.DistRoot = root
	return nil
}

func validURL(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil {
		return nil, err
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("invalid URL port")
		}
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(value, "\\ \t\r\n") {
		return nil, fmt.Errorf("expected HTTP(S) URL without credentials/query/fragment")
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return nil, fmt.Errorf("unsafe URL path")
		}
	}
	return u, nil
}

func SameEndpoint(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	pa, pb := port(a), port(b)
	return strings.EqualFold(strings.TrimRight(a.Hostname(), "."), strings.TrimRight(b.Hostname(), ".")) && (pa == pb || ((pa == "80" || pa == "443") && (pb == "80" || pb == "443")))
}
