// Package testutil creates isolated databases for integration tests.
package testutil

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/store"
	"github.com/google/uuid"
)

func Database(t testing.TB) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_URL")
	if dsn == "" {
		dsn = "sqlite://" + filepath.Join(t.TempDir(), "test.db")
	} else {
		admin, e := store.Open(dsn)
		if e != nil {
			t.Fatal(e)
		}
		name := "axolotl_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		if e = admin.DB.Exec("CREATE DATABASE " + name).Error; e != nil {
			admin.Close()
			t.Fatal(e)
		}
		u, e := url.Parse(dsn)
		if e != nil {
			t.Fatal(e)
		}
		u.Path = "/" + name
		dsn = u.String()
		t.Cleanup(func() {
			if e := admin.DB.Exec("DROP DATABASE " + name + " WITH (FORCE)").Error; e != nil {
				t.Error(e)
			}
			admin.Close()
		})
	}
	s, e := store.Open(dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	if e = s.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	return s
}
