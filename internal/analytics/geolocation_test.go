package analytics

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/testutil"
)

func testIPDB(t *testing.T) string {
	t.Helper()
	// Minimal valid IPDB with a single leaf for both address families.
	text := []byte("中国\t山东省\t济南市")
	data := make([]byte, 11+len(text))
	binary.BigEndian.PutUint32(data[0:4], 2)
	binary.BigEndian.PutUint32(data[4:8], 2)
	binary.BigEndian.PutUint16(data[9:11], uint16(len(text)))
	copy(data[11:], text)
	meta, _ := json.Marshal(map[string]any{"build": 1, "ip_version": 3, "languages": map[string]int{"CN": 0}, "node_count": 1, "total_size": len(data), "fields": []string{"country_name", "region_name", "city_name"}})
	file := make([]byte, 4)
	binary.BigEndian.PutUint32(file, uint32(len(meta)))
	file = append(append(file, meta...), data...)
	path := filepath.Join(t.TempDir(), "qqwry.ipdb")
	if e := os.WriteFile(path, file, 0644); e != nil {
		t.Fatal(e)
	}
	return path
}

func TestQQWryPriorityAndCountryCode(t *testing.T) {
	var calls atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"status":"success","countryCode":"US","city":"wrong"}`))
	}))
	defer api.Close()
	g, e := OpenGeoIPWithOptions("", "", testIPDB(t), api.URL+"/{ip}")
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	for _, value := range []string{"123.129.103.68", "2001:250::1"} {
		loc := g.Recompute(context.Background(), netip.MustParseAddr(value))
		if loc.Country == nil || *loc.Country != "CN" || loc.Region == nil || *loc.Region != "山东省" || loc.City == nil || *loc.City != "济南市" || loc.Latitude != nil {
			t.Fatalf("%+v", loc)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("China lookup used API")
	}
	if hasGeo(g.Recompute(context.Background(), netip.MustParseAddr("127.0.0.1"))) {
		t.Fatal("private IP resolved")
	}
}

func TestActualQQWry(t *testing.T) {
	path := os.Getenv("TEST_QQWRY_DB")
	if path == "" {
		t.Skip("set TEST_QQWRY_DB for real database smoke test")
	}
	g, e := OpenGeoIPWithOptions("", "", path, "")
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	loc := g.Recompute(context.Background(), netip.MustParseAddr("123.129.103.68"))
	if loc.Country == nil || *loc.Country != "CN" || loc.City == nil {
		t.Fatalf("%+v", loc)
	}
	t.Log(*loc.Country, *loc.Region, *loc.City)
}

func TestGeoAPICacheRateAndSharedQuota(t *testing.T) {
	s := testutil.Database(t)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"status":"success","countryCode":"us","continentCode":"na","regionName":"加利福尼亚州","city":"洛杉矶"}`))
	}))
	defer server.Close()
	first, e := OpenGeoIPWithOptions("", "", "", server.URL+"/{ip}")
	if e != nil {
		t.Fatal(e)
	}
	defer first.Close()
	first.BindRateStore(s)
	second, e := OpenGeoIPWithOptions("", "", "", server.URL+"/{ip}")
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	second.BindRateStore(s)
	for i := 0; i < 55; i++ {
		g := first
		if i%2 != 0 {
			g = second
		}
		g.Recompute(context.Background(), netip.MustParseAddr(fmt.Sprintf("8.8.8.%d", i+1)))
	}
	if calls.Load() != 40 {
		t.Fatal("shared quota", calls.Load())
	}
	loc := first.Recompute(context.Background(), netip.MustParseAddr("8.8.8.1"))
	if loc.Country == nil || *loc.Country != "US" || loc.City == nil || *loc.City != "洛杉矶" || calls.Load() != 40 {
		t.Fatal("cached result", loc, calls.Load())
	}
	// Expiring the stored window permits new calls, independently of processes.
	table := "analytics_geoip_api_limiter"
	if s.Dialect == "postgres" {
		table = "analytics.geoip_api_limiter"
	}
	if e := s.DB.Table(table).Where("id=1").Update("calls", "[]").Error; e != nil {
		t.Fatal(e)
	}
	third, e := OpenGeoIPWithOptions("", "", "", server.URL+"/{ip}")
	if e != nil {
		t.Fatal(e)
	}
	defer third.Close()
	third.BindRateStore(s)
	if !hasGeo(third.Recompute(context.Background(), netip.MustParseAddr("9.9.9.9"))) || calls.Load() != 41 {
		t.Fatal("new window", calls.Load())
	}
}

func TestGeoAPIConcurrentReservations(t *testing.T) {
	s := testutil.Database(t)
	a, b := &GeoIP{}, &GeoIP{}
	a.BindRateStore(s)
	b.BindRateStore(s)
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 80; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			g := a
			if i%2 != 0 {
				g = b
			}
			if g.sharedRate(context.Background(), nil) {
				admitted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if admitted.Load() != 40 {
		t.Fatal("concurrent quota", admitted.Load())
	}
}

func TestGeoAPI429IsSharedAndFailuresKeepOldData(t *testing.T) {
	s := testutil.Database(t)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("X-Ttl", "60")
		w.WriteHeader(429)
	}))
	defer server.Close()
	a, e := OpenGeoIPWithOptions("", "", "", server.URL+"/{ip}")
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	a.BindRateStore(s)
	b, e := OpenGeoIPWithOptions("", "", "", server.URL+"/{ip}")
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	b.BindRateStore(s)
	a.Recompute(context.Background(), netip.MustParseAddr("8.8.8.8"))
	b.Recompute(context.Background(), netip.MustParseAddr("9.9.9.9"))
	if calls.Load() != 1 {
		t.Fatal("shared backoff", calls.Load())
	}
	event := Event{Country: model.Ptr("CN"), City: model.Ptr("旧城市")}
	a.Enrich(&event, netip.MustParseAddr("8.8.8.8"))
	if event.City == nil || *event.City != "旧城市" {
		t.Fatal("failed lookup erased data")
	}
}

func TestGeoAPIAsyncCollection(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Write([]byte(`{"status":"success","countryCode":"US","city":"洛杉矶"}`))
	}))
	defer server.Close()
	g, e := OpenGeoIPWithOptions("", "", "", server.URL+"/{ip}")
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	ip := netip.MustParseAddr("8.8.8.8")
	event := Event{}
	g.Enrich(&event, ip)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("fallback not queued")
	}
	// A second collection call returns even though the network task is blocked.
	done := make(chan struct{})
	go func() { g.Enrich(&event, ip); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("collection waited on network")
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if loc, ok := g.cachedAPI(ip.String()); ok && loc.City != nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("API result not cached")
}
