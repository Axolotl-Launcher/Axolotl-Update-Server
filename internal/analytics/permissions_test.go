package analytics

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/testutil"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestPostgresReaderViewOnly(t *testing.T) {
	s := testutil.Database(t)
	if s.Dialect != "postgres" {
		t.Skip("PostgreSQL role permissions")
	}
	ctx := context.Background()
	storage := &Storage{Store: s}
	if _, e := storage.Maintain(ctx, time.Now(), false); e != nil {
		t.Fatal(e)
	}
	if e := storage.Write(ctx, []Event{{OccurredAt: time.Now(), UserAgent: "reader-test", Route: "/latest"}}); e != nil {
		t.Fatal(e)
	}
	role := "axolotl_reader_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if e := s.DB.Exec("CREATE ROLE " + role).Error; e != nil {
		t.Fatal(e)
	}
	defer func() {
		if e := s.DB.Exec("DROP OWNED BY " + role).Error; e != nil {
			t.Error(e)
		}
		if e := s.DB.Exec("DROP ROLE " + role).Error; e != nil {
			t.Error(e)
		}
	}()
	for _, q := range []string{"GRANT USAGE ON SCHEMA analytics TO " + role, "GRANT SELECT ON analytics.requests_v1 TO " + role} {
		if e := s.DB.Exec(q).Error; e != nil {
			t.Fatal(e)
		}
	}
	if e := s.DB.Transaction(func(tx *gorm.DB) error {
		if e := tx.Exec("SET LOCAL ROLE " + role).Error; e != nil {
			return e
		}
		var n int64
		if e := tx.Table("analytics.requests_v1").Count(&n).Error; e != nil {
			return e
		}
		if n != 1 {
			t.Error(n)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{"SELECT * FROM version", "SELECT * FROM analytics.request_events", "SELECT * FROM analytics.user_agents", "DELETE FROM analytics.request_events"} {
		e := s.DB.Transaction(func(tx *gorm.DB) error {
			if e := tx.Exec("SET LOCAL ROLE " + role).Error; e != nil {
				return e
			}
			return tx.Exec(q).Error
		})
		if e == nil {
			t.Fatal("reader unexpectedly authorized", q)
		}
	}
	if e := s.DB.Transaction(func(tx *gorm.DB) error {
		if e := tx.Exec("SET LOCAL ROLE " + role).Error; e != nil {
			return e
		}
		return tx.Exec("SELECT * FROM analytics.requests_v2").Error
	}); e == nil {
		t.Fatal("new v2 view was implicitly granted")
	}
	if e := s.DB.Exec("GRANT SELECT ON analytics.requests_v2 TO " + role).Error; e != nil {
		t.Fatal(e)
	}
	if e := s.DB.Transaction(func(tx *gorm.DB) error {
		if e := tx.Exec("SET LOCAL ROLE " + role).Error; e != nil {
			return e
		}
		return tx.Exec("SELECT * FROM analytics.requests_v2").Error
	}); e != nil {
		t.Fatal(e)
	}
}
