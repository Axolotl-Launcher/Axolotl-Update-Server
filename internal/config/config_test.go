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

func TestDistributionPublicBaseURL(t *testing.T) {
	c := Default()
	if c.DistributionBaseURL() != c.PublicBaseURL {
		t.Fatal("missing backwards-compatible URL")
	}
	c.DistPublicBaseURL = " https://dist.axlmc.org/ "
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	if c.DistributionBaseURL() != "https://dist.axlmc.org" || c.DistServer != "" {
		t.Fatal(c.DistributionBaseURL(), c.DistServer)
	}
	c.DistServer = "https://dist.axlmc.org"
	if c.Validate() == nil {
		t.Fatal("distribution redirect loop accepted")
	}
	for _, invalid := range []string{"ftp://dist.axlmc.org", "https://user:pass@dist.axlmc.org", "https://dist.axlmc.org?x=1", "https://dist.axlmc.org/../bad"} {
		c := Default()
		c.DistPublicBaseURL = invalid
		if c.Validate() == nil {
			t.Fatal("invalid public distribution URL", invalid)
		}
	}
	t.Setenv("DIST_PUBLIC_BASE_URL", "https://dist.axlmc.org/")
	c, e := Load()
	if e != nil || c.DistributionBaseURL() != "https://dist.axlmc.org" {
		t.Fatal(c.DistributionBaseURL(), e)
	}
}
