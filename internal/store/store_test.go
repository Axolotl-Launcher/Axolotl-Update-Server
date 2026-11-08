package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
)

func TestMigrationImportAndVerification(t *testing.T) {
	s, e := Open("sqlite://" + filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.CheckSchema(ctx); e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	snap := Snapshot{Versions: []model.Version{{ID: 42, Version: "1.2.3", Channel: "release", Status: "draft", CreatedAt: now, UpdatedAt: now}}}
	if e = s.Import(ctx, snap, true); e != nil {
		t.Fatal(e)
	}
	var count int64
	s.DB.Model(&model.Version{}).Count(&count)
	if count != 0 {
		t.Fatal("dry run wrote data")
	}
	if e = s.Import(ctx, snap, false); e != nil {
		t.Fatal(e)
	}
	got, e := s.Snapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if len(got.Versions) != 1 || got.Versions[0].ID != 42 {
		t.Fatal(got)
	}
	// Empty slices are normalized by the reader; compare populated source fields as well.
	snap = got
	if e = s.Verify(ctx, &snap); e != nil {
		t.Fatal(e)
	}
	if e = s.Import(ctx, snap, false); e == nil {
		t.Fatal("non-empty target accepted")
	}
	v := model.Version{Version: "1.2.4", Channel: "release", Status: "draft", CreatedAt: now, UpdatedAt: now}
	if e = s.DB.Create(&v).Error; e != nil {
		t.Fatal(e)
	}
	if v.ID <= 42 {
		t.Fatal("sequence not advanced")
	}
}
