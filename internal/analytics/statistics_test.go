package analytics

import (
	"context"
	"math"
	"net/url"
	"testing"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/testutil"
)

func fixture(t *testing.T) (*Statistics, Query, time.Time) {
	t.Helper()
	s := testutil.Database(t)
	storage := &Storage{Store: s}
	now := time.Now().UTC().Truncate(time.Hour)
	if _, e := storage.Maintain(context.Background(), now, false); e != nil {
		t.Fatal(e)
	}
	events := []Event{}
	for i, status := range []int{200, 304, 404, 429, 503} {
		ip := "192.0.2.1"
		if i >= 3 {
			ip = "2001:db8::1"
		}
		ua := "curl/8.0"
		if i == 4 {
			ua = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
		}
		cache := "miss"
		if i == 0 {
			cache = "hit"
		}
		events = append(events, Event{OccurredAt: now.Add(time.Duration(i) * 10 * time.Minute), Route: "/latest", ClientIP: &ip, UserAgent: ua, StatusCode: &status, Method: model.Ptr("GET"), DurationUS: model.Ptr(int64((i + 1) * 1000)), RequestBytes: model.Ptr(int64(0)), ResponseBytes: model.Ptr(int64((i + 1) * 100)), AppCache: &cache, Channel: model.Ptr("release"), Country: model.Ptr("CN")})
	}
	events = append(events, Event{OccurredAt: now.Add(50 * time.Minute), Route: "/api/health", ClientIP: model.Ptr("192.0.2.1"), UserAgent: "old-client"})
	if e := storage.Write(context.Background(), events); e != nil {
		t.Fatal(e)
	}
	return &Statistics{Store: s}, Query{Start: now, End: now.Add(time.Hour), Interval: "15m", Seconds: 900, Limit: 2, Filters: map[string]string{}}, now
}
func TestOverviewAndPercentiles(t *testing.T) {
	s, q, _ := fixture(t)
	got, e := s.Overview(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	if got.Requests != 6 || got.UniqueIPs != 2 || got.UniqueUserAgents != 3 || got.ResponseBytes != 1500 || got.StatusSamples != 5 || got.DurationSamples != 5 || got.ClientErrors != 2 || got.ServerErrors != 1 || got.RateLimited != 1 || got.BotRequests != 1 || got.CountrySamples != 5 {
		t.Fatalf("%+v", got)
	}
	for _, tc := range []struct {
		value *float64
		want  float64
	}{{got.AverageDurationMS, 3}, {got.MaxDurationMS, 5}, {got.Latency.P50, 3}, {got.Latency.P90, 4.6}, {got.Latency.P95, 4.8}, {got.Latency.P99, 4.96}, {got.ErrorRate, 0.6}, {got.CacheHitRate, 0.2}} {
		if tc.value == nil || math.Abs(*tc.value-tc.want) > 1e-8 {
			t.Fatalf("metric %v want %g, latency %+v", tc.value, tc.want, got.Latency)
		}
	}
}
func TestSeriesAndDimensions(t *testing.T) {
	s, q, _ := fixture(t)
	points, e := s.Timeseries(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	if len(points) != 4 || points[0].Requests != 2 || points[1].Requests != 1 || points[2].Requests != 2 || points[3].Requests != 1 {
		t.Fatal(points)
	}
	if points[0].AverageDurationMS == nil || math.Abs(*points[0].AverageDurationMS-1.5) > 1e-8 {
		t.Fatal("weighted bucket average", points[0])
	}
	countries, e := s.Breakdown(context.Background(), q, "country")
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range countries.Items {
		if item.Value != nil && *item.Value == "CN" {
			if item.Requests != 5 || item.UniqueIPs != 2 || item.AverageDurationMS == nil || math.Abs(*item.AverageDurationMS-3) > 1e-8 || item.ClientErrors != 2 {
				t.Fatal("weighted group metrics", item)
			}
		}
	}
	for _, dim := range DimensionNames() {
		group, e := s.Breakdown(context.Background(), q, dim)
		if e != nil {
			t.Fatal(dim, e)
		}
		if group.TotalRequests != 6 {
			t.Fatal(group)
		}
		sum := group.OtherRequests
		for _, item := range group.Items {
			sum += item.Requests
		}
		if sum != 6 {
			t.Fatal(dim, group)
		}
	}
	for i := 0; i < len(DimensionNames()); i += 8 {
		dims := DimensionNames()[i:min(i+8, len(DimensionNames()))]
		groups, e := s.Breakdowns(context.Background(), q, dims)
		if e != nil {
			t.Fatal(e)
		}
		for _, dim := range dims {
			single, e := s.Breakdown(context.Background(), q, dim)
			if e != nil {
				t.Fatal(e)
			}
			if groups[dim].TotalRequests != single.TotalRequests || len(groups[dim].Items) != len(single.Items) {
				t.Fatal(dim, groups[dim], single)
			}
		}
	}
	q.Filters = map[string]string{"status_class": "4xx", "country": "CN"}
	overview, e := s.Overview(context.Background(), q)
	if e != nil || overview.Requests != 2 {
		t.Fatal(overview, e)
	}
	q.Filters = map[string]string{"is_bot": "true"}
	overview, e = s.Overview(context.Background(), q)
	if e != nil || overview.Requests != 1 {
		t.Fatal(overview, e)
	}
}
func TestRequestsPaginationAndEmptyData(t *testing.T) {
	s, q, now := fixture(t)
	seen := map[int64]bool{}
	for {
		page, e := s.Requests(context.Background(), q)
		if e != nil {
			t.Fatal(e)
		}
		for _, r := range page.Items {
			if seen[r.ID] {
				t.Fatal("duplicate cursor row", r.ID)
			}
			seen[r.ID] = true
		}
		if !page.HasMore {
			break
		}
		values := url.Values{"start": {q.Start.Format(time.RFC3339Nano)}, "end": {q.End.Format(time.RFC3339Nano)}, "cursor": {*page.NextCursor}, "limit": {"2"}}
		q, e = ParseQuery(values, now.Add(2*time.Hour))
		if e != nil {
			t.Fatal(e)
		}
	}
	if len(seen) != 6 {
		t.Fatal(seen)
	}
	q.Filters = map[string]string{"route": "/none"}
	q.Cursor = nil
	overview, e := s.Overview(context.Background(), q)
	if e != nil || overview.Requests != 0 || overview.Latency.P95 != nil || overview.ErrorRate != nil {
		t.Fatal(overview, e)
	}
	points, e := s.Timeseries(context.Background(), q)
	if e != nil || len(points) == 0 {
		t.Fatal(points, e)
	}
	for _, point := range points {
		if point.Requests != 0 {
			t.Fatal(point)
		}
	}
}
func TestQueryValidation(t *testing.T) {
	now := time.Now().UTC()
	for _, raw := range []string{"start=bad", "status_code=99", "dimension=x&status_class=6xx", "client_ip=192.0.2.1%27", "asn=-1", "is_bot=1", "country=abc", "limit=0", "interval=1s", "cursor=not-base64"} {
		values, _ := url.ParseQuery(raw)
		if _, e := ParseQuery(values, now); e == nil {
			t.Fatal("accepted", raw)
		}
	}
	values := url.Values{"start": {now.Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano)}}
	q, e := ParseQuery(values, now)
	if e != nil || !q.RetentionClamped || q.Start.Before(now.Add(-14*24*time.Hour)) {
		t.Fatal(q, e)
	}
	q = Query{Start: now.Add(-time.Hour), End: now, Seconds: 60, Interval: "1m", Filters: map[string]string{"route": "/latest"}}
	cursor := q.nextCursor(now.Add(-time.Minute), 1)
	values = url.Values{"cursor": {cursor}, "route": {"/api/health"}}
	if _, e = ParseQuery(values, now); e == nil {
		t.Fatal("cursor accepted changed filters")
	}
}
