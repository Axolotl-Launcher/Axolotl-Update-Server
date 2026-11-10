package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
)

func TestDistCloudflareAccess(t *testing.T) {
	api, svc := setup(t)
	publish(t, api, svc, "1.2.3", "release")
	svc.Config.DistCloudflareOnly = true
	for _, tc := range []struct {
		name, remote, xff string
		want              int
	}{
		{"direct", "198.51.100.1:42", "", 403},
		{"forged headers", "198.51.100.1:42", "173.245.48.1", 403},
		{"Cloudflare ipv4", "173.245.48.1:42", "203.0.113.1", 200},
		{"Cloudflare ipv6", "[2400:cb00::1]:42", "203.0.113.1", 200},
		{"Cloudflare through nginx", "127.0.0.1:42", "203.0.113.1, 173.245.48.1", 200},
		{"direct through nginx", "127.0.0.1:42", "173.245.48.1, 198.51.100.1", 403},
		{"local without chain", "127.0.0.1:42", "", 403},
		{"malformed chain", "127.0.0.1:42", "173.245.48.1, bad", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/dist/1.2.3/installer.exe", nil)
			r.RemoteAddr = tc.remote
			r.Header.Set("X-Forwarded-For", tc.xff)
			r.Header.Set("CF-Connecting-IP", "203.0.113.1")
			r.Header.Set("CF-Ray", "forged")
			w := httptest.NewRecorder()
			api.Router.ServeHTTP(w, r)
			if w.Code != tc.want || (w.Code == 403 && w.Header().Get("Cloudflare-CDN-Cache-Control") != "no-store") {
				t.Fatal(w.Code, w.Header(), w.Body)
			}
		})
	}
	svc.Config.DistOriginSecret = strings.Repeat("s", 32)
	r := httptest.NewRequest("GET", "/dist/1.2.3/installer.exe", nil)
	r.RemoteAddr = "173.245.48.1:42"
	w := httptest.NewRecorder()
	api.Router.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	r.Header.Set("X-Axolotl-Origin-Secret", svc.Config.DistOriginSecret)
	w = httptest.NewRecorder()
	api.Router.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := call(api, "GET", "/latest", "", nil); w.Code != 200 {
		t.Fatal("dist-only policy affected latest", w.Code)
	}
}

func TestDistCacheRangeFallbackAndVisibility(t *testing.T) {
	api, svc := setup(t)
	publish(t, api, svc, "1.2.3", "release")
	path := "/dist/1.2.3/installer.exe"
	w := call(api, "GET", path, "", nil)
	etag := w.Header().Get("ETag")
	if w.Code != 200 || w.Body.String() != "installer" || etag == "" || w.Header().Get("Cloudflare-CDN-Cache-Control") != "public, max-age=86400, must-revalidate" {
		t.Fatal(w.Code, w.Header(), w.Body)
	}
	for _, tc := range []struct {
		method  string
		headers map[string]string
		status  int
		body    string
	}{
		{"HEAD", nil, 200, ""},
		{"GET", map[string]string{"If-None-Match": etag}, 304, ""},
		{"GET", map[string]string{"Range": "bytes=1-3", "If-Range": etag}, 206, "nst"},
		{"GET", map[string]string{"Range": "bytes=1-3", "If-Range": `"wrong"`}, 200, "installer"},
	} {
		w := call(api, tc.method, path, "", tc.headers)
		if w.Code != tc.status || w.Body.String() != tc.body {
			t.Fatal(tc, w.Code, w.Body)
		}
	}
	w = call(api, "GET", path, "", map[string]string{"Range": "bytes=999-"})
	if w.Code != 416 || w.Header().Get("Cloudflare-CDN-Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Header())
	}
	w = call(api, "GET", "/dist/1.2.3/manifest.json", "", nil)
	if w.Code != 200 || strings.Contains(w.Header().Get("Cache-Control"), "immutable") || w.Header().Get("Cloudflare-CDN-Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Header())
	}
	manifestTag := w.Header().Get("ETag")
	if w := call(api, "PATCH", "/api/admin/versions/1.2.3/force-update", `{"force_update":true}`, admin); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	w = call(api, "GET", "/dist/1.2.3/manifest.json", "", map[string]string{"If-None-Match": manifestTag})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"force_update": true`) {
		t.Fatal(w.Code, w.Body)
	}
	if e := os.Remove(filepath.Join(svc.Config.DistRoot, "1.2.3", "installer.exe")); e != nil {
		t.Fatal(e)
	}
	for _, method := range []string{"GET", "HEAD"} {
		w = call(api, method, path+"?ignored=secret", "", nil)
		if w.Code != 302 || w.Header().Get("Location") != "https://github.com/Axolotl-Launcher/Axolotl/releases/download/v1.2.3/installer.exe" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, w.Header())
		}
	}
	if w := call(api, "GET", "/dist/1.2.3/not-registered.exe", "", nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
	svc.Store.DB.Model(&model.Artifact{}).Where("filename = ?", "update.tar.gz").Update("is_public", false)
	for _, name := range []string{"update.tar.gz", "update.tar.gz.sig"} {
		if w := call(api, "GET", "/dist/1.2.3/"+name, "", nil); w.Code != 404 {
			t.Fatal("private artifact exposed", name, w.Code)
		}
	}
	if w := call(api, "POST", "/api/admin/versions/1.2.3/revoke", "", admin); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := call(api, "GET", path, "", nil); w.Code != 404 {
		t.Fatal("missing revoked file redirected", w.Code)
	}
}

func TestDistThreePerChannelAndSync(t *testing.T) {
	api, svc := setup(t)
	for i := 1; i <= 4; i++ {
		publish(t, api, svc, fmt.Sprintf("1.0.%d", i), "release")
		publish(t, api, svc, fmt.Sprintf("2.0.0-beta.%d", i), "beta")
	}
	var count int64
	svc.Store.DB.Model(&model.Version{}).Count(&count)
	if count != 8 {
		t.Fatal("retention removed metadata", count)
	}
	for _, version := range []string{"1.0.1", "2.0.0-beta.1"} {
		if _, e := os.Stat(filepath.Join(svc.Config.DistRoot, version, "installer.exe")); !os.IsNotExist(e) {
			t.Fatal("old package retained", version, e)
		}
		if w := call(api, "GET", "/dist/"+version+"/manifest.json", "", nil); w.Code != 200 {
			t.Fatal("retention removed manifest", version, w.Code)
		}
		if w := call(api, "GET", "/dist/"+version+"/installer.exe", "", nil); w.Code != 302 {
			t.Fatal(version, w.Code)
		}
	}
	for _, version := range []string{"1.0.2", "1.0.3", "1.0.4", "2.0.0-beta.2", "2.0.0-beta.3", "2.0.0-beta.4"} {
		if w := call(api, "GET", "/dist/"+version+"/installer.exe", "", nil); w.Code != 200 {
			t.Fatal(version, w.Code)
		}
	}
	// A migrated metadata-only version is served from GitHub until offline sync.
	if e := os.Remove(filepath.Join(svc.Config.DistRoot, "1.0.4", "installer.exe")); e != nil {
		t.Fatal(e)
	}
	synced, e := svc.SyncDist(context.Background())
	if e != nil || len(synced) != 6 {
		t.Fatal(synced, e)
	}
	if w := call(api, "GET", "/dist/1.0.4/installer.exe", "", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := call(api, "GET", "/dist/1.0.1/installer.exe", "", nil); w.Code != 302 {
		t.Fatal("sync restored pruned version", w.Code)
	}
}

func TestDistPublicationFailureLeavesNoPackages(t *testing.T) {
	for _, fault := range []string{"download", "integrity", "commit"} {
		t.Run(fault, func(t *testing.T) {
			api, svc := setup(t)
			switch fault {
			case "download":
				svc.FetchArtifact = func(context.Context, string, int64, string, string) error { return fmt.Errorf("download failed") }
			case "integrity":
				svc.FetchArtifact = func(_ context.Context, _ string, _ int64, _ string, target string) error {
					return os.WriteFile(target, []byte("wrong"), 0644)
				}
			case "commit":
				svc.BeforeCommit = func(string) error { return fmt.Errorf("commit failed") }
			}
			if w := signed(api, svc, payload("1.2.3", "release")); w.Code < 400 {
				t.Fatal(w.Code)
			}
			for _, name := range []string{"installer.exe", "update.tar.gz", "update.tar.gz.sig", "manifest.json"} {
				if _, e := os.Stat(filepath.Join(svc.Config.DistRoot, "1.2.3", name)); !os.IsNotExist(e) {
					t.Fatal("failed publication exposed files", name, e)
				}
			}
			entries, _ := os.ReadDir(filepath.Join(svc.Config.DistRoot, ".staging"))
			if len(entries) != 0 {
				t.Fatal("staging leak", entries)
			}
		})
	}
}
