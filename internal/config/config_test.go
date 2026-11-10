package config

import "testing"

func TestProductionAndDistValidation(t *testing.T) {
	c := Default()
	c.Env = "production"
	if c.Validate() == nil {
		t.Fatal("SQLite accepted in production")
	}
	c.DatabaseURL = "postgres://localhost/app"
	if c.Validate() == nil {
		t.Fatal("missing secrets")
	}
	c.AdminToken = "admin"
	c.WebhookSecret = "secret"
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, value := range []string{"https://update.axlmc.org", "https://user:pass@example.com", "https://example.com?x=y", "ftp://example.com", "https://example.com/../bad"} {
		c.DistServer = value
		if c.Validate() == nil {
			t.Fatal("invalid dist", value)
		}
	}
}

func TestDistConfig(t *testing.T) {
	c := Default()
	if !c.DistCloudflareOnly {
		t.Fatal("Cloudflare guard must default on")
	}
	c.DistOriginSecret = "short"
	if c.Validate() == nil {
		t.Fatal("short secret accepted")
	}
	c.DistOriginSecret = "0123456789abcdef0123456789abcdef"
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	t.Setenv("DIST_CLOUDFLARE_ONLY", "false")
	t.Setenv("DIST_ORIGIN_SECRET", "")
	t.Setenv("GITHUB_ARTIFACT_DOWNLOAD_TIMEOUT_SECONDS", "120")
	c, e := Load()
	if e != nil || c.DistCloudflareOnly || c.ArtifactDownloadTimeout.Seconds() != 120 {
		t.Fatal(c.DistCloudflareOnly, c.ArtifactDownloadTimeout, e)
	}
	for _, key := range []string{"DIST_CLOUDFLARE_ONLY", "GITHUB_ARTIFACT_DOWNLOAD_TIMEOUT_SECONDS"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "invalid")
			if _, e := Load(); e == nil {
				t.Fatal("invalid config accepted", key)
			}
		})
	}
}
