package analytics

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/testutil"
	"github.com/gin-gonic/gin"
)

func TestStorageDedupRetentionAndView(t *testing.T) {
	s := testutil.Database(t)
	storage := &Storage{Store: s}
	ctx := context.Background()
	now := time.Now().UTC()
	if _, e := storage.Maintain(ctx, now, false); e != nil {
		t.Fatal(e)
	}
	events := []Event{{OccurredAt: now, UserAgent: "Axolotl/1.9.4", ClientIP: model.Ptr("::ffff:192.0.2.1"), Route: "/latest", Channel: model.Ptr("release")}, {OccurredAt: now.Add(-time.Hour), UserAgent: "Axolotl/1.9.4", Route: "/api/health"}, {OccurredAt: now.Add(-14*24*time.Hour + time.Second), UserAgent: "older", Route: "/latest"}}
	if e := storage.Write(ctx, events); e != nil {
		t.Fatal(e)
	}
	et, ut := storage.tables()
	var agents, requests int64
	s.DB.Table(ut).Count(&agents)
	s.DB.Table(et).Count(&requests)
	if agents != 2 || requests != 3 {
		t.Fatal(agents, requests)
	}
	view := "analytics_requests_v1"
	if s.Dialect == "postgres" {
		view = "analytics.requests_v1"
	}
	var ip string
	if e := s.DB.Table(view).Select("client_ip").Where("client_ip IS NOT NULL").Scan(&ip).Error; e != nil {
		t.Fatal(e)
	}
	if ip != "192.0.2.1" {
		t.Fatal(ip)
	}
	// Move the oldest row just beyond the strict view horizon, within its existing partition.
	if e := s.DB.Table(et).Where("route = ? AND user_agent_id <> ?", "/latest", events[0].UserAgentID).Update("occurred_at", now.Add(-14*24*time.Hour-time.Second)).Error; e != nil {
		t.Fatal(e)
	}
	s.DB.Table(view).Count(&requests)
	if requests != 2 {
		t.Fatal("strict horizon", requests)
	}
	before := requests
	if _, e := storage.Maintain(ctx, now.Add(48*time.Hour), true); e != nil {
		t.Fatal(e)
	}
	s.DB.Table(view).Count(&requests)
	if requests != before {
		t.Fatal("dry run mutated data")
	}
	if _, e := storage.Maintain(ctx, now.Add(48*time.Hour), false); e != nil {
		t.Fatal(e)
	}
	s.DB.Table(et).Count(&requests)
	s.DB.Table(ut).Count(&agents)
	if requests != 2 || agents != 1 {
		t.Fatal("cleanup", requests, agents)
	}
	// A retained UA must remain reusable after garbage collection.
	if e := storage.Write(ctx, []Event{{OccurredAt: now, UserAgent: "Axolotl/1.9.4", Route: "/latest"}}); e != nil {
		t.Fatal(e)
	}
}

func TestExtractWhitelist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		path, route string
		header      map[string]string
		want        bool
		check       func(Event) bool
	}{
		{"/latest?channel=beta&current_version=1.2.3&platform=windows-x86_64&secret=no&version=9.9.9", "/latest", nil, true, func(e Event) bool {
			return e.Channel != nil && *e.Channel == "beta" && e.ClientVersion != nil && *e.ClientVersion == "1.2.3" && e.Platform != nil && e.TargetVersion == nil
		}},
		{"/latest?channel=beta&current_version=1.2.3", "/latest", map[string]string{"X-Axolotl-Channel": "release", "X-Axolotl-Version": "2.0.0"}, true, func(e Event) bool { return *e.Channel == "release" && *e.ClientVersion == "2.0.0" }},
		{"/latest?channel=garbage&current_version=bad&platform=bad", "/latest", nil, true, func(e Event) bool { return e.Channel == nil && e.ClientVersion == nil && e.Platform == nil }},
		{"/api/downloads/latest?include_updater=true&include_revoked=invalid&foo=no", "/api/downloads/latest", nil, true, func(e Event) bool { return *e.Channel == "release" && *e.IncludeUpdater && e.IncludeRevoked == nil }},
		{"/api/unknown/private?token=secret", "", nil, true, func(e Event) bool { return e.Route == "unmatched" && e.Channel == nil }},
		{"/api/health?channel=beta&version=1.2.3", "/api/health", nil, true, func(e Event) bool { return e.Channel == nil && e.ClientVersion == nil }},
		{"/dist/1.2.3/manifest.json", "/dist/:version/*filename", nil, false, nil},
		{"/latest/", "", nil, false, nil},
	} {
		w := httptest.NewRecorder()
		r := gin.New()
		r.RedirectTrailingSlash = false
		h := func(c *gin.Context) {
			c.Set("client_ip", "192.0.2.1")
			e, ok := Extract(c, time.Now())
			if ok != tc.want || (ok && !tc.check(e)) {
				t.Errorf("%s: %+v %v", tc.path, e, ok)
			}
		}
		if tc.route != "" {
			r.GET(tc.route, h)
		} else {
			r.NoRoute(h)
		}
		req := httptest.NewRequest("GET", tc.path, strings.NewReader(""))
		for k, v := range tc.header {
			req.Header.Set(k, v)
		}
		req.Header.Set("User-Agent", "Axolotl")
		r.ServeHTTP(w, req)
	}
}

type recordingSink struct {
	mu     sync.Mutex
	events []Event
	fail   bool
	gate   chan struct{}
}

func (s *recordingSink) Write(ctx context.Context, events []Event) error {
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return fmt.Errorf("database unavailable")
	}
	s.events = append(s.events, events...)
	return nil
}
func TestCollectorFlushAndShutdown(t *testing.T) {
	sink := &recordingSink{}
	c := NewCollector(sink, 20, 5, time.Hour)
	for i := 0; i < 12; i++ {
		c.Enqueue(Event{Route: "/latest"})
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e := c.Close(ctx); e != nil {
		t.Fatal(e)
	}
	if n := c.Stats(); n.Written != 12 || n.Dropped != 0 {
		t.Fatal(n)
	}
	c.Enqueue(Event{})
	if c.Stats().Dropped != 1 {
		t.Fatal("closed collector accepted event")
	}
}
func TestCollectorBackpressureAndFailure(t *testing.T) {
	gate := make(chan struct{})
	sink := &recordingSink{gate: gate, fail: true}
	c := NewCollector(sink, 2, 1, time.Millisecond)
	for i := 0; i < 20; i++ {
		c.Enqueue(Event{})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if e := c.Close(ctx); e == nil {
		t.Fatal("expected deadline")
	}
	if n := c.Stats(); n.Dropped != 20 || n.FailedBatches == 0 {
		t.Fatal(n)
	}
}
func TestCollectorRetries(t *testing.T) {
	sink := &recordingSink{fail: true}
	c := NewCollector(sink, 10, 1, 10*time.Millisecond)
	c.Enqueue(Event{})
	deadline := time.Now().Add(time.Second)
	for c.Stats().FailedBatches == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	sink.mu.Lock()
	sink.fail = false
	sink.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if e := c.Close(ctx); e != nil {
		t.Fatal(e)
	}
	if n := c.Stats(); n.Written != 1 || n.Dropped != 0 || n.FailedBatches != 1 {
		t.Fatal(n)
	}
}

func BenchmarkBatchWrite(b *testing.B) {
	s := testutil.Database(b)
	storage := &Storage{Store: s}
	now := time.Now().UTC()
	if _, e := storage.Maintain(context.Background(), now, false); e != nil {
		b.Fatal(e)
	}
	events := make([]Event, 500)
	for i := range events {
		events[i] = Event{OccurredAt: now.Add(-time.Duration(i) * time.Minute), UserAgent: fmt.Sprintf("Axolotl/1.9.%d windows-x86_64", i%10), Route: "/latest", ClientIP: model.Ptr(fmt.Sprintf("192.0.2.%d", i%250+1)), Channel: model.Ptr("release"), ClientVersion: model.Ptr("1.9.4"), Platform: model.Ptr("windows-x86_64"), Method: model.Ptr("GET"), StatusCode: model.Ptr(200), DurationUS: model.Ptr(int64(1000 + i*10)), RequestBytes: model.Ptr(int64(0)), ResponseBytes: model.Ptr(int64(600)), Host: model.Ptr("update.example.com"), Scheme: model.Ptr("https"), HTTPVersion: model.Ptr("HTTP/1.1"), ContentType: model.Ptr("application/json"), AppCache: model.Ptr("hit"), Country: model.Ptr("CN"), Continent: model.Ptr("AS"), ASN: model.Ptr(int64(12345)), ASNOrg: model.Ptr("Test network")}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if e := storage.Write(context.Background(), events); e != nil {
			b.Fatal(e)
		}
	}
	b.StopTimer()
	var size int64
	if s.Dialect == "postgres" {
		s.DB.Raw("SELECT pg_database_size(current_database())").Scan(&size)
	} else {
		var pages, pageSize int64
		s.DB.Raw("PRAGMA page_count").Scan(&pages)
		s.DB.Raw("PRAGMA page_size").Scan(&pageSize)
		size = pages * pageSize
	}
	b.ReportMetric(float64(size)/float64(b.N*500), "database-bytes/event")
	b.ReportMetric(float64(b.N*500)/b.Elapsed().Seconds(), "events/s")
	if s.Dialect == "postgres" {
		stats := &Statistics{Store: s}
		if e := s.DB.Exec("ANALYZE analytics.request_events").Error; e != nil {
			b.Fatal(e)
		}
		if e := s.DB.Exec("ANALYZE analytics.user_agents").Error; e != nil {
			b.Fatal(e)
		}
		query := Query{Start: now.Add(-24 * time.Hour), End: now.Add(time.Second), Seconds: 3600, Interval: "1h", Limit: 20, Filters: map[string]string{}}
		started := time.Now()
		overview, e := stats.Overview(context.Background(), query)
		if e != nil || overview.Requests != int64(b.N*500) {
			b.Fatal(overview, e)
		}
		if _, e = stats.Timeseries(context.Background(), query); e != nil {
			b.Fatal(e)
		}
		if _, e = stats.Breakdowns(context.Background(), query, []string{"route", "status_code", "country", "browser", "os", "device_type", "channel", "client_version"}); e != nil {
			b.Fatal(e)
		}
		b.ReportMetric(float64(time.Since(started).Microseconds())/1000, "dashboard-ms")
	}
}
