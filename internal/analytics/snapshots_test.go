package analytics

import (
	"context"
	"testing"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/testutil"
)

func TestScheduledSnapshotsExactAndPersistent(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	now := time.Now().UTC()
	storage := &Storage{Store: db}
	if _, e := storage.Maintain(ctx, now, false); e != nil {
		t.Fatal(e)
	}
	for _, at := range []time.Time{now.Add(-2 * time.Hour), now.Add(-time.Hour)} {
		if e := storage.Write(ctx, []Event{{OccurredAt: at, ClientIP: model.Ptr("1.1.1.1"), Route: "/latest", UserAgent: "Axolotl/1.10.5", StatusCode: model.Ptr(200), DurationUS: model.Ptr(int64(1000))}}); e != nil {
			t.Fatal(e)
		}
	}
	cache := &Snapshots{Store: db}
	if snap, e := cache.Load(ctx, 24); e != nil || snap != nil {
		t.Fatal("read should not build", snap, e)
	}
	if e := cache.Refresh(ctx, now); e != nil {
		t.Fatal(e)
	}
	snap, e := cache.Load(ctx, 24)
	if e != nil || snap == nil {
		t.Fatal(e)
	}
	if snap.Overview.Requests != 2 || snap.Overview.UniqueIPs != 1 || snap.Overview.UniqueUserAgents != 1 || *snap.Overview.Latency.P95 != 1 {
		t.Fatal(snap.Overview)
	}
	if len(snap.Breakdowns) != len(Dimensions) || snap.OldestRequest == nil || snap.NewestRequest == nil {
		t.Fatal("incomplete snapshot")
	}
	other := &Snapshots{Store: db}
	persisted, e := other.Load(ctx, 24)
	if e != nil || persisted.Overview.Requests != 2 {
		t.Fatal("cache did not persist", e)
	}
	// New writes do not force recomputation on read, or within the hourly period.
	if e = storage.Write(ctx, []Event{{OccurredAt: now.Add(-time.Minute), Route: "/latest", UserAgent: "new"}}); e != nil {
		t.Fatal(e)
	}
	if e = cache.Refresh(ctx, now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	snap, e = cache.Load(ctx, 24)
	if e != nil || snap.Overview.Requests != 2 {
		t.Fatal("unexpected request-driven refresh", e)
	}
	if e = cache.Refresh(ctx, now.Add(2*time.Hour)); e != nil {
		t.Fatal(e)
	}
	snap, e = cache.Load(ctx, 24)
	if e != nil || snap.Overview.Requests != 3 {
		t.Fatal("scheduled refresh did not update", e)
	}
}

func TestSnapshotPresetEligibility(t *testing.T) {
	now := time.Now().UTC()
	q, e := ParseQuery(mapValues(now, 336), now)
	if e != nil {
		t.Fatal(e)
	}
	if SnapshotRange(q, now) != 336 {
		t.Fatal("14d preset not recognized")
	}
	q.Filters = map[string]string{"country": "CN"}
	if SnapshotRange(q, now) != 0 {
		t.Fatal("filtered snapshot is not exact")
	}
	q.Filters = nil
	q.Start = q.Start.Add(time.Hour)
	if SnapshotRange(q, now) != 0 {
		t.Fatal("custom window accepted")
	}
}
