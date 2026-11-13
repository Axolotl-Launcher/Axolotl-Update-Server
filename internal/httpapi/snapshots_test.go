package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/analytics"
)

func TestSnapshotReadDoesNotStartMaintenance(t *testing.T) {
	api, svc := setup(t)
	api.Snapshots = &analytics.Snapshots{Store: svc.Store}
	now := time.Now().UTC()
	if _, e := (&analytics.Storage{Store: svc.Store}).Maintain(context.Background(), now, false); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"/api/admin/stats", "/api/admin/stats/status"} {
		w := call(api, "GET", path, "", admin)
		if w.Code != 202 {
			t.Fatal(w.Code, w.Body)
		}
	}
	var n int64
	table := "analytics_stats_snapshots"
	if svc.Store.Dialect == "postgres" {
		table = "analytics.stats_snapshots"
	}
	svc.Store.DB.Table(table).Count(&n)
	if n != 0 {
		t.Fatal("request triggered a refresh")
	}
	if e := api.Snapshots.Refresh(context.Background(), now); e != nil {
		t.Fatal(e)
	}
	w := call(api, "GET", "/api/admin/stats?dimensions=route,browser&limit=8", "", admin)
	var body struct {
		Cache struct{ Scheduled bool }
		Data  struct {
			Breakdowns map[string]analytics.Breakdown
		}
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || !body.Cache.Scheduled || len(body.Data.Breakdowns) != 2 {
		t.Fatal(w.Code, w.Body)
	}
	w = call(api, "GET", "/api/admin/stats?dimensions=bad", "", admin)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
	w = call(api, "GET", "/api/admin/stats/requests", "", admin)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
}
