package analytics

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/store"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/testutil"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
)

func testStoreConnection(s *store.Store) (*store.Store, error) {
	switch d := s.DB.Dialector.(type) {
	case *postgres.Dialector:
		return store.Open(d.Config.DSN)
	case *sqlite.Dialector:
		return store.Open("sqlite://" + d.DSN)
	default:
		return nil, fmt.Errorf("unsupported test dialect")
	}
}

func TestRelocateResumeAndConcurrentCollection(t *testing.T) {
	s := testutil.Database(t)
	storage := &Storage{Store: s}
	ctx := context.Background()
	end := time.Now().UTC().Truncate(time.Microsecond)
	start := end.Add(-time.Hour)
	if _, e := storage.Maintain(ctx, end, false); e != nil {
		t.Fatal(e)
	}
	old := []Event{}
	for i := 0; i < 5; i++ {
		old = append(old, Event{OccurredAt: start.Add(time.Duration(i) * time.Minute), ClientIP: model.Ptr("123.129.103.68"), UserAgent: "Axolotl/1", Route: "/latest", Country: model.Ptr("CN"), City: model.Ptr("旧城市"), Latitude: model.Ptr(1.0), Longitude: model.Ptr(2.0), ASN: model.Ptr(int64(123)), ASNOrg: model.Ptr("existing ASN"), ResponseBytes: model.Ptr(int64(999))})
	}
	old = append(old, Event{OccurredAt: start.Add(5 * time.Minute), ClientIP: model.Ptr("127.0.0.1"), Route: "/api/health", City: model.Ptr("保留私有地址记录")})
	if e := storage.Write(ctx, old); e != nil {
		t.Fatal(e)
	}
	g, e := OpenGeoIPWithOptions("", "", testIPDB(t), "")
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	storage.Geo = g
	opts := RelocateOptions{Start: start, End: end, BatchSize: 2}
	stop := errors.New("simulated interruption")
	r, e := storage.Relocate(ctx, opts, func(r RelocateReport) error {
		// A second process writes through a separate Store while relocate is active.
		other, e := testStoreConnection(s)
		if e != nil {
			return e
		}
		defer other.Close()
		live := &Storage{Store: other, Geo: g}
		if e = live.Write(ctx, []Event{{OccurredAt: end.Add(time.Second), ClientIP: model.Ptr("123.129.103.68"), UserAgent: "new request", Route: "/latest"}}); e != nil {
			return e
		}
		return stop
	})
	if !errors.Is(e, stop) || r.Updated != 2 || r.AfterID == 0 || r.Complete {
		t.Fatal(r, e)
	}
	opts.After, opts.AfterID = r.After, r.AfterID
	r, e = storage.Relocate(ctx, opts, nil)
	if e != nil || !r.Complete || r.Scanned != 4 || r.Updated != 3 || r.Skipped != 1 {
		t.Fatal(r, e)
	}
	et, _ := storage.tables()
	var rows []struct {
		City          *string
		Latitude      *float64
		ASN           *int64
		ResponseBytes *int64
	}
	if e = s.DB.Table(et).Select("city,latitude,asn,response_bytes").Where("occurred_at < ? AND client_ip = ?", end, "123.129.103.68").Find(&rows).Error; e != nil {
		t.Fatal(e)
	}
	if len(rows) != 5 {
		t.Fatal(len(rows))
	}
	for _, row := range rows {
		if row.City == nil || *row.City != "济南市" || row.Latitude != nil || row.ASN == nil || *row.ASN != 123 || row.ResponseBytes == nil || *row.ResponseBytes != 999 {
			t.Fatalf("unrelated fields changed: %+v", row)
		}
	}
	var count int64
	if e = s.DB.Table(et).Where("occurred_at >= ?", end).Count(&count).Error; e != nil || count != 1 {
		t.Fatal("new request lost", count, e)
	}
}

func TestRelocateDryRunAndUnavailableLookup(t *testing.T) {
	s := testutil.Database(t)
	storage := &Storage{Store: s}
	ctx := context.Background()
	end := time.Now().UTC()
	start := end.Add(-time.Hour)
	if _, e := storage.Maintain(ctx, end, false); e != nil {
		t.Fatal(e)
	}
	if e := storage.Write(ctx, []Event{{OccurredAt: start, ClientIP: model.Ptr("8.8.8.8"), UserAgent: "curl", Route: "/latest", Country: model.Ptr("US"), City: model.Ptr("原有城市")}}); e != nil {
		t.Fatal(e)
	}
	g := &GeoIP{qqLookup: func(string) ([]string, error) { return []string{"美国", "", ""}, nil }}
	storage.Geo = g
	opts := RelocateOptions{Start: start, End: end, BatchSize: 2}
	r, e := storage.Relocate(ctx, opts, nil)
	if e != nil || r.Updated != 0 || r.Skipped != 1 {
		t.Fatal(r, e)
	}
	g.qqLookup = func(string) ([]string, error) { return []string{"中国", "山东省", "济南市"}, nil }
	opts.DryRun = true
	r, e = storage.Relocate(ctx, opts, nil)
	if e != nil || r.Updated != 0 || r.Resolved != 1 {
		t.Fatal(r, e)
	}
	et, _ := storage.tables()
	var row struct{ City string }
	if e = s.DB.Table(et).Select("city").Take(&row).Error; e != nil || row.City != "原有城市" {
		t.Fatal(row, e)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = storage.Relocate(canceled, opts, nil); e == nil {
		t.Fatal("canceled job ran")
	}
}
