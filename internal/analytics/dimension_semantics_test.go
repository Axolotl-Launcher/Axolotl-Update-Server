package analytics

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/testutil"
)

func TestDimensionMeaningAndMatchingFilters(t *testing.T) {
	s := testutil.Database(t)
	storage := &Storage{Store: s}
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if _, e := storage.Maintain(ctx, now, false); e != nil {
		t.Fatal(e)
	}
	events := []Event{
		{OccurredAt: now.Add(-time.Minute), Route: "/latest", UserAgent: "1.10.5", Platform: model.Ptr("windows-x86_64"), ClientVersion: model.Ptr("1.10.5"), StatusCode: model.Ptr(204), ResponseBytes: model.Ptr(int64(0)), Method: model.Ptr("GET")},
		{OccurredAt: now.Add(-time.Minute), Route: "/latest", UserAgent: "axolotl/1.10.0", Platform: model.Ptr("darwin-aarch64"), StatusCode: model.Ptr(304), ResponseBytes: model.Ptr(int64(0)), Method: model.Ptr("GET")},
		{OccurredAt: now.Add(-time.Minute), Route: "/api/downloads/latest", UserAgent: "curl/8.0", StatusCode: model.Ptr(200), ContentType: model.Ptr("application/json"), RefererHost: model.Ptr("example.com"), IncludeUpdater: model.Ptr(false), IncludeRevoked: model.Ptr(false)},
		{OccurredAt: now.Add(-time.Minute), Route: "/api/versions", UserAgent: "Axolotl-Launcher/1.10.5 (linux)", StatusCode: model.Ptr(200), ContentType: model.Ptr("application/json")},
		{OccurredAt: now.Add(-time.Minute), Route: "unmatched", UserAgent: "curl/8.0", StatusCode: model.Ptr(404), ErrorCode: model.Ptr("not_found"), ContentType: model.Ptr("application/json")},
		{OccurredAt: now.Add(-time.Minute), Route: "/latest", UserAgent: "legacy-unknown"},
	}
	if e := storage.Write(ctx, events); e != nil {
		t.Fatal(e)
	}
	stats := &Statistics{Store: s}
	q := Query{Start: now.Add(-time.Hour), End: now, Limit: 100, Seconds: 900, Filters: map[string]string{}}
	for _, tc := range []struct {
		dim, value string
		want       int64
	}{
		{"browser", "Axolotl", 3}, {"browser_version", "@application", 3}, {"device_type", "application", 3},
		{"os", "Windows", 1}, {"os", "macOS", 1}, {"os", "Linux", 1},
		{"referer_host", "@application", 3}, {"referer_host", "example.com", 1}, {"referer_host", "@direct", 1},
		{"content_type", "@empty", 2}, {"content_type", "application/json", 3},
		{"error_code", "@none", 4}, {"error_code", "not_found", 1},
		{"include_updater", "@not_applicable", 5}, {"include_updater", "false", 1}, {"include_revoked", "@not_applicable", 5},
	} {
		t.Run(tc.dim+"/"+tc.value, func(t *testing.T) {
			group, e := stats.Breakdown(ctx, q, tc.dim)
			if e != nil {
				t.Fatal(e)
			}
			found := false
			for _, item := range group.Items {
				if item.Value != nil && *item.Value == tc.value {
					found = true
					if item.Requests != tc.want {
						t.Fatal(item.Requests, tc.want)
					}
				}
			}
			if !found {
				t.Fatal("missing normalized bucket", group)
			}
			filtered, e := ParseQuery(url.Values{"start": {q.Start.Format(time.RFC3339)}, "end": {now.Format(time.RFC3339)}, tc.dim: {tc.value}, "limit": {"100"}}, now)
			if e != nil {
				t.Fatal(e)
			}
			overview, e := stats.Overview(ctx, filtered)
			if e != nil || overview.Requests != tc.want {
				t.Fatal(overview.Requests, tc.want, e)
			}
			page, e := stats.Requests(ctx, filtered)
			if e != nil || int64(len(page.Items)) != tc.want {
				t.Fatal(page, e)
			}
		})
	}
	page, e := stats.Requests(ctx, q)
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range page.Items {
		if r.UserAgent == "1.10.5" && (r.Browser == nil || *r.Browser != "Axolotl" || r.OS == nil || *r.OS != "Windows" || r.ErrorCode == nil || *r.ErrorCode != "@none") {
			t.Fatalf("detail differs from aggregation: %+v", r)
		}
	}
}

func TestReclassifyHistoricalDictionary(t *testing.T) {
	s := testutil.Database(t)
	storage := &Storage{Store: s}
	ctx := context.Background()
	now := time.Now().UTC()
	if _, e := storage.Maintain(ctx, now, false); e != nil {
		t.Fatal(e)
	}
	if e := storage.Write(ctx, []Event{{OccurredAt: now, Route: "/latest", UserAgent: "axolotl/1.10.0"}, {OccurredAt: now, Route: "/latest", UserAgent: "Axolotl Launcher/1.10.5 (windows)"}}); e != nil {
		t.Fatal(e)
	}
	et, ut := storage.tables()
	if e := s.DB.Table(ut).Where("1=1").Updates(map[string]any{"browser": "1.10.5", "browser_version": "axolotl/1.10.0", "os": "Unknown", "device_type": "unknown", "classification_version": 0}).Error; e != nil {
		t.Fatal(e)
	}
	r, e := storage.Reclassify(ctx, 1, 0, true, nil)
	if e != nil || r.Scanned != 2 || r.Updated != 0 {
		t.Fatal(r, e)
	}
	stop := errors.New("stop after committed batch")
	r, e = storage.Reclassify(ctx, 1, 0, false, func(ReclassifyReport) error { return stop })
	if !errors.Is(e, stop) || r.Updated != 1 {
		t.Fatal(r, e)
	}
	r, e = storage.Reclassify(ctx, 1, r.AfterID, false, nil)
	if e != nil || r.Updated != 1 || !r.Complete {
		t.Fatal(r, e)
	}
	var agents []userAgent
	if e = s.DB.Table(ut).Find(&agents).Error; e != nil {
		t.Fatal(e)
	}
	for _, a := range agents {
		if *a.Browser != "Axolotl" || a.BrowserVersion != nil || *a.DeviceType != "application" {
			t.Fatal(a)
		}
	}
	var n int64
	s.DB.Table(et).Count(&n)
	if n != 2 {
		t.Fatal("reclassification changed events", n)
	}
	r, e = storage.Reclassify(ctx, 10, 0, false, nil)
	if e != nil || r.Updated != 0 {
		t.Fatal("non-idempotent reclassify", r, e)
	}
}

func TestApplicationOSPrefersReportedUAAndKeepsMissing(t *testing.T) {
	s := testutil.Database(t)
	storage := &Storage{Store: s}
	ctx := context.Background()
	now := time.Now().UTC()
	if _, e := storage.Maintain(ctx, now, false); e != nil {
		t.Fatal(e)
	}
	if e := storage.Write(ctx, []Event{
		{OccurredAt: now.Add(-time.Second), Route: "/latest", UserAgent: "Axolotl-Launcher/1.10.5 (windows)", Platform: model.Ptr("linux-x86_64"), StatusCode: model.Ptr(200)},
		{OccurredAt: now.Add(-time.Second), Route: "/api/versions", UserAgent: "axolotl/1.10.0", StatusCode: model.Ptr(200)},
	}); e != nil {
		t.Fatal(e)
	}
	stats := &Statistics{Store: s}
	q := Query{Start: now.Add(-time.Hour), End: now, Limit: 10, Filters: map[string]string{}}
	page, e := stats.Requests(ctx, q)
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range page.Items {
		if r.UserAgent == "Axolotl-Launcher/1.10.5 (windows)" && (r.OS == nil || *r.OS != "Windows") {
			t.Fatal("target platform overrode reported OS", r.OS)
		}
		if r.UserAgent == "axolotl/1.10.0" && (r.OS == nil || *r.OS != "@not_provided") {
			t.Fatal("missing OS was invented", r.OS)
		}
		if r.ContentType != nil {
			t.Fatal("missing body data invented MIME", r.ContentType)
		}
	}
}
