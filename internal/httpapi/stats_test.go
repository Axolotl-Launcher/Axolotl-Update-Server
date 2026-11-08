package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/analytics"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
)

func TestStatisticsEndpoints(t *testing.T) {
	api, svc := setup(t)
	storage := &analytics.Storage{Store: svc.Store}
	now := time.Now().UTC()
	if _, e := storage.Maintain(context.Background(), now, false); e != nil {
		t.Fatal(e)
	}
	if e := storage.Write(context.Background(), []analytics.Event{{OccurredAt: now.Add(-time.Minute), UserAgent: "curl/8", Route: "/latest", ClientIP: model.Ptr("192.0.2.1"), StatusCode: model.Ptr(200), DurationUS: model.Ptr(int64(1500)), ResponseBytes: model.Ptr(int64(123))}}); e != nil {
		t.Fatal(e)
	}
	for _, suffix := range []string{"", "/overview", "/timeseries", "/breakdown?dimension=country", "/requests", "/meta", "/status"} {
		path := "/api/admin/stats" + suffix
		w := call(api, "GET", path, "", nil)
		if w.Code != 401 {
			t.Fatal(path, w.Code, w.Body)
		}
		w = call(api, "GET", path, "", admin)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(path, w.Code, w.Body)
		}
		var body map[string]any
		if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
			t.Fatal(e)
		}
		if suffix == "/overview" {
			data := body["data"].(map[string]any)
			if data["requests"] != float64(1) || data["response_bytes"] != float64(123) {
				t.Fatal(data)
			}
		}
	}
	for _, path := range []string{"/api/admin/stats/breakdown?dimension=not-a-column", "/api/admin/stats?dimensions=route);DROP+TABLE+version;--", "/api/admin/stats/overview?client_ip=not-ip"} {
		w := call(api, "GET", path, "", admin)
		if w.Code != 400 {
			t.Fatal(path, w.Code, w.Body)
		}
	}
	w := call(api, "HEAD", "/api/admin/stats/overview", "", admin)
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal(w.Code, w.Body)
	}
	api.statsGate <- struct{}{}
	api.statsGate <- struct{}{}
	w = call(api, "GET", "/api/admin/stats/overview", "", admin)
	<-api.statsGate
	<-api.statsGate
	if w.Code != 429 || !strings.Contains(w.Body.String(), "stats_busy") {
		t.Fatal(w.Code, w.Body)
	}
}
