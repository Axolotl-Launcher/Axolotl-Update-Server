package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestStatisticsUpgradeKeepsOldMetricsUnknown(t *testing.T) {
	s, e := Open("sqlite://" + filepath.Join(t.TempDir(), "upgrade.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	provider, e := s.provider()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = provider.UpTo(ctx, 2); e != nil {
		t.Fatal(e)
	}
	if e = s.DB.Exec("INSERT INTO analytics_user_agents(id,sha256,user_agent) VALUES(1,?,?)", "old-hash", "old-agent").Error; e != nil {
		t.Fatal(e)
	}
	at := time.Now().UTC().Add(-time.Minute)
	if e = s.DB.Exec("INSERT INTO analytics_request_events(occurred_at,user_agent_id,route) VALUES(?,1,'/latest')", at).Error; e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	var row struct {
		Method        *string
		StatusCode    *int
		DurationUS    *int64
		ResponseBytes *int64
		Browser       *string
	}
	if e = s.DB.Table("analytics_requests_v2").First(&row).Error; e != nil {
		t.Fatal(e)
	}
	if row.Method != nil || row.StatusCode != nil || row.DurationUS != nil || row.ResponseBytes != nil || row.Browser != nil {
		t.Fatal("upgrade invented old metrics", row)
	}
	var n int64
	if e = s.DB.Table("analytics_requests_v1").Count(&n).Error; e != nil || n != 1 {
		t.Fatal("v1 compatibility", n, e)
	}
}
