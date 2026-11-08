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
