package httpapi

import (
	"strings"
	"testing"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/analytics"
	"github.com/gin-gonic/gin"
)

func TestExpandedRequestCapture(t *testing.T) {
	_, svc := setup(t)
	events := []analytics.Event{}
	api := New(svc, func(c *gin.Context, at time.Time) {
		if e, ok := analytics.Extract(c, at); ok {
			events = append(events, e)
		}
	})
	publish(t, api, svc, "1.2.3", "release")
	for i := 0; i < 2; i++ {
		w := call(api, "GET", "/latest?channel=release&secret=never-stored", "", map[string]string{"Referer": "https://example.com/private?token=secret", "User-Agent": "Mozilla/5.0", "X-Forwarded-Proto": "https"})
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
	}
	for i, e := range events[len(events)-2:] {
		if e.StatusCode == nil || *e.StatusCode != 200 || e.DurationUS == nil || e.ResponseBytes == nil || *e.ResponseBytes <= 0 || e.RefererHost == nil || *e.RefererHost != "example.com" || e.Scheme == nil || *e.Scheme != "http" {
			t.Fatalf("capture %+v", e)
		}
		want := "miss"
		if i == 1 {
			want = "hit"
		}
		if e.AppCache == nil || *e.AppCache != want {
			t.Fatal(e.AppCache)
		}
	}
	call(api, "PATCH", "/api/admin/versions/1.2.3/force-update", `{"force_update":true}`, admin)
	e := events[len(events)-1]
	if e.RequestBytes == nil || *e.RequestBytes != int64(len(`{"force_update":true}`)) {
		t.Fatal("request bytes", e.RequestBytes)
	}
	call(api, "GET", "/api/unknown/secret-path?token=secret", "", nil)
	e = events[len(events)-1]
	if e.StatusCode == nil || *e.StatusCode != 404 || e.Route != "unmatched" || e.ErrorCode == nil || *e.ErrorCode != "not_found" {
		t.Fatal(e)
	}
	before := len(events)
	call(api, "GET", "/api/admin/stats", "", admin)
	if len(events) != before {
		t.Fatal("stats request recorded")
	}
	for _, e := range events {
		if strings.Contains(e.Route, "secret") {
			t.Fatal("raw URL leaked")
		}
	}
}
