package store_test

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/store"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/testutil"
)

func TestLegacyRevisionsReadOnlyImport(t *testing.T) {
	for _, revision := range []string{"0001_initial", "0004_artifact_signature_filename", "0009_version_artifact_storage"} {
		t.Run(revision, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "legacy.db")
			source, e := store.Open("sqlite://" + path)
			if e != nil {
				t.Fatal(e)
			}
			if e = source.Migrate(ctx); e != nil {
				t.Fatal(e)
			}
			queries := []string{"CREATE TABLE alembic_version(version_num TEXT)", "INSERT INTO alembic_version VALUES ('" + revision + "')", "CREATE TABLE usage_event(id INTEGER)", "INSERT INTO usage_event VALUES (1)"}
			for _, q := range queries {
				if e = source.DB.Exec(q).Error; e != nil {
					t.Fatal(e)
				}
			}
			now := time.Date(2026, 8, 29, 15, 19, 0, 0, time.UTC)
			v := model.Version{ID: 42, Version: "1.2.3", Channel: "release", Status: "published", ReleaseTag: "v1.2.3", ReleaseID: "123", PublishedAt: &now, CreatedAt: now, UpdatedAt: now}
			if e = source.DB.Create(&v).Error; e != nil {
				t.Fatal(e)
			}
			sig := "sig"
			a := model.Artifact{ID: 77, VersionID: 42, Platform: "darwin-aarch64", Architecture: "universal", Filename: "updater", RelativePath: "dist/1.2.3/updater", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Signature: &sig, ContentType: "application/octet-stream", Kind: "updater", IsPublic: true, UploadedAt: now, CreatedAt: now, UpdatedAt: now}
			if e = source.DB.Create(&a).Error; e != nil {
				t.Fatal(e)
			}
			ev := model.WebhookEvent{EventID: "github-v1.2.3-123", PayloadHash: "hash", EventType: "release", ReceivedAt: now, Status: "processed"}
			if e = source.DB.Create(&ev).Error; e != nil {
				t.Fatal(e)
			}
			if revision != "0009_version_artifact_storage" {
				for _, q := range []string{"DROP INDEX webhook_event_version", "ALTER TABLE webhook_event DROP COLUMN version", "ALTER TABLE artifact DROP COLUMN target_platforms"} {
					if e = source.DB.Exec(q).Error; e != nil {
						t.Fatal(e)
					}
				}
			}
			if revision == "0001_initial" {
				for _, q := range []string{"ALTER TABLE version DROP COLUMN force_update", "ALTER TABLE artifact DROP COLUMN kind", "ALTER TABLE artifact DROP COLUMN variant", "ALTER TABLE artifact DROP COLUMN display_name", "ALTER TABLE artifact DROP COLUMN sort_order", "ALTER TABLE artifact DROP COLUMN is_public", "ALTER TABLE artifact DROP COLUMN signature_filename"} {
					if e = source.DB.Exec(q).Error; e != nil {
						t.Fatal(e)
					}
				}
			}
			if e = source.Close(); e != nil {
				t.Fatal(e)
			}
			before, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			snap, report, e := store.ReadLegacy(ctx, path)
			if e != nil {
				t.Fatal(e)
			}
			after, _ := os.ReadFile(path)
			if sha256.Sum256(before) != sha256.Sum256(after) {
				t.Fatal("source mutated")
			}
			if report.Versions != 1 || len(snap.Artifacts[0].TargetPlatforms) != 2 || snap.Events[0].Version == nil || *snap.Events[0].Version != "1.2.3" {
				t.Fatal(snap, report)
			}
			target := testutil.Database(t)
			if e = target.Import(ctx, snap, true); e != nil {
				t.Fatal(e)
			}
			if e = target.Import(ctx, snap, false); e != nil {
				t.Fatal(e)
			}
			if e = target.Verify(ctx, &snap); e != nil {
				t.Fatal(e)
			}
			v2 := model.Version{Version: "1.2.4", Channel: "release", Status: "draft"}
			if e = target.DB.Create(&v2).Error; e != nil {
				t.Fatal(e)
			}
			if v2.ID <= 42 {
				t.Fatal("identity sequence not advanced")
			}
		})
	}
}
