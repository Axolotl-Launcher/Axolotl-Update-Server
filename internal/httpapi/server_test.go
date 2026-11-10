package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/config"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/domain"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/service"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/testutil"
)

func setup(t *testing.T) (*Server, *service.Service) {
	t.Helper()
	s := testutil.Database(t)
	c := config.Default()
	c.Env = "test"
	c.DistRoot = filepath.Join(t.TempDir(), "dist")
	c.AdminToken = "admin"
	c.WebhookSecret = "webhook"
	c.RateLimit = 0
	c.DistCloudflareOnly = false
	svc := service.New(s, c)
	svc.FetchSignature = func(_ context.Context, _ string, _ int64, _ string) (string, error) { return "tauri-signature", nil }
	svc.FetchArtifact = func(_ context.Context, value string, _ int64, _ string, target string) error {
		body := "package"
		if strings.HasSuffix(value, ".sig") {
			body = "tauri-signature"
		} else if strings.HasSuffix(value, "installer.exe") {
			body = "installer"
		}
		return os.WriteFile(target, []byte(body), 0644)
	}
	return New(svc, nil), svc
}
func call(api *Server, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "198.51.100.20:1000"
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	api.Router.ServeHTTP(w, r)
	return w
}
func payload(version, channel string) []byte {
	files := []map[string]any{}
	assets := []map[string]any{}
	for _, f := range []struct{ name, body string }{{"update.tar.gz", "package"}, {"update.tar.gz.sig", "tauri-signature"}, {"installer.exe", "installer"}} {
		sum := sha256.Sum256([]byte(f.body))
		hash := hex.EncodeToString(sum[:])
		u := "https://github.com/Axolotl-Launcher/Axolotl/releases/download/v" + version + "/" + f.name
		files = append(files, map[string]any{"filename": f.name, "size": len(f.body), "sha256": hash, "downloadUrl": u})
		assets = append(assets, map[string]any{"name": f.name, "size": len(f.body), "digest": "sha256:" + hash, "browser_download_url": u})
	}
	p := map[string]any{"event_id": "github-v" + version + "-123", "version": version, "tag": "v" + version, "channel": channel, "release": map[string]any{"id": 123, "tag_name": "v" + version, "draft": false, "body": "Release notes", "published_at": "2026-08-29T15:19:00Z", "assets": assets}, "catalog": map[string]any{"version": version, "files": files, "artifacts": []map[string]any{{"filename": "update.tar.gz", "kind": "updater", "platform": "darwin-aarch64", "architecture": "universal", "targetPlatforms": []string{"darwin-aarch64", "darwin-x86_64"}, "signatureFilename": "update.tar.gz.sig"}, {"filename": "installer.exe", "kind": "installer", "platform": "windows", "architecture": "x86_64"}}}}
	b, _ := json.Marshal(p)
	return b
}
func signed(api *Server, svc *service.Service, raw []byte) *httptest.ResponseRecorder {
	ts := fmt.Sprint(svc.Now().Unix())
	mac := hmac.New(sha256.New, []byte(svc.Config.WebhookSecret))
	mac.Write([]byte(ts + "."))
	mac.Write(raw)
	return call(api, "POST", "/api/webhook/release", string(raw), map[string]string{"Content-Type": "application/json", "X-Webhook-Timestamp": ts, "X-Webhook-Signature": "sha256=" + hex.EncodeToString(mac.Sum(nil))})
}
func publish(t *testing.T, api *Server, svc *service.Service, version, channel string) {
	t.Helper()
	w := signed(api, svc, payload(version, channel))
	if w.Code != 200 {
		t.Fatalf("publish: %d %s", w.Code, w.Body)
	}
}

var admin = map[string]string{"Authorization": "Bearer admin", "Content-Type": "application/json"}

func TestReleaseLifecycle(t *testing.T) {
	api, svc := setup(t)
	publish(t, api, svc, "1.9.4", "release")
	publish(t, api, svc, "1.9.5-beta.1", "beta")
	for _, platform := range []string{"darwin-aarch64", "darwin-x86_64"} {
		w := call(api, "GET", "/latest?platform="+platform, "", nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"version":"1.9.4"`) {
			t.Fatal(w.Code, w.Body)
		}
		if w.Header().Get("Cache-Control") != "public, max-age=0, s-maxage=30, must-revalidate" {
			t.Fatal(w.Header())
		}
		tag := w.Header().Get("ETag")
		w = call(api, "GET", "/latest?platform="+platform, "", map[string]string{"If-None-Match": tag})
		if w.Code != 304 || w.Body.Len() != 0 || w.Result().Header.Get("Vary") == "" {
			t.Fatal(w.Code, w.Header(), w.Body)
		}
	}
	w := call(api, "GET", "/latest?channel=beta", "", nil)
	if !strings.Contains(w.Body.String(), "1.9.5-beta.1") {
		t.Fatal(w.Body)
	}
	w = call(api, "GET", "/latest?channel=release&current_version=1.9.4", "", nil)
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	w = call(api, "GET", "/latest?channel=release", "", map[string]string{"X-Axolotl-Channel": "beta"})
	if !strings.Contains(w.Body.String(), "beta.1") || w.Result().Header.Get("Cloudflare-CDN-Cache-Control") != "no-store" {
		t.Fatal(w.Header(), w.Body)
	}
	w = call(api, "GET", "/api/downloads/latest", "", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), `"kind":"updater"`) || w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal(w.Code, w.Body)
	}
	w = call(api, "HEAD", "/latest", "", nil)
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") == "" {
		t.Fatal(w.Code, w.Body)
	}
	w = signed(api, svc, payload("1.9.4", "release"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"idempotent":true`) {
		t.Fatal(w.Code, w.Body)
	}
	w = call(api, "PATCH", "/api/admin/versions/1.9.4/force-update", `{"force_update":true}`, admin)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	w = call(api, "GET", "/latest", "", nil)
	if !strings.Contains(w.Body.String(), `"force_update":true`) {
		t.Fatal("cache not invalidated", w.Body)
	}
	w = call(api, "PATCH", "/api/admin/versions/1.9.4/force-update", `{"force_update":true}`, admin)
	if !strings.Contains(w.Body.String(), `"idempotent":true`) {
		t.Fatal(w.Body)
	}
	w = call(api, "POST", "/api/admin/versions/1.9.4/revoke", "", admin)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w = call(api, "GET", "/latest", "", nil); w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	w = call(api, "POST", "/api/admin/versions/1.9.4/restore", "", admin)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	w = call(api, "GET", "/dist/1.9.4/manifest.json", "", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	w = call(api, "GET", "/dist/1.9.4/installer.exe", "", nil)
	if w.Code != 200 || w.Body.String() != "installer" {
		t.Fatal(w.Code)
	}
	w = call(api, "DELETE", "/api/admin/versions/1.9.4", "", admin)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if _, e := os.Stat(filepath.Join(svc.Config.DistRoot, "1.9.4")); !os.IsNotExist(e) {
		t.Fatal("version files remain")
	}
	var n int64
	svc.Store.DB.Model(&model.Artifact{}).Where("version_id NOT IN (SELECT id FROM version)").Count(&n)
	if n != 0 {
		t.Fatal("orphan artifacts")
	}
	w = call(api, "GET", "/api/admin/audit-logs", "", admin)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "delete_version") {
		t.Fatal(w.Code, w.Body)
	}
}

func TestRollbackAndCleanupRetry(t *testing.T) {
	api, svc := setup(t)
	publish(t, api, svc, "1.2.3", "release")
	path := filepath.Join(svc.Config.DistRoot, "1.2.3", "manifest.json")
	before, _ := os.ReadFile(path)
	svc.BeforeCommit = func(string) error { return fmt.Errorf("injected commit failure") }
	w := call(api, "PATCH", "/api/admin/versions/1.2.3/force-update", `{"force_update":true}`, admin)
	if w.Code != 500 {
		t.Fatal(w.Code, w.Body)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("manifest not restored")
	}
	v, e := svc.Version(context.Background(), "1.2.3")
	if e != nil || v.ForceUpdate {
		t.Fatal(v, e)
	}
	w = call(api, "DELETE", "/api/admin/versions/1.2.3", "", admin)
	if w.Code != 500 {
		t.Fatal(w.Code, w.Body)
	}
	if _, e = os.Stat(path); e != nil {
		t.Fatal("delete rollback lost files", e)
	}
	svc.BeforeCommit = nil
	originalRemove := svc.RemoveAll
	svc.RemoveAll = func(string) error { return fmt.Errorf("injected cleanup failure") }
	w = call(api, "DELETE", "/api/admin/versions/1.2.3", "", admin)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"files_cleanup_pending":true`) {
		t.Fatal(w.Code, w.Body)
	}
	w = signed(api, svc, payload("1.2.3", "release"))
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	svc.RemoveAll = originalRemove
	w = call(api, "DELETE", "/api/admin/versions/1.2.3", "", admin)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"idempotent":true`) {
		t.Fatal(w.Code, w.Body)
	}
}

func TestPublishRollbackPreservesExistingManifest(t *testing.T) {
	api, svc := setup(t)
	svc.BeforeCommit = func(string) error { return fmt.Errorf("failed") }
	w := signed(api, svc, payload("1.2.3", "release"))
	if w.Code != 500 {
		t.Fatal(w.Code, w.Body)
	}
	if _, e := os.Stat(filepath.Join(svc.Config.DistRoot, "1.2.3", "manifest.json")); !os.IsNotExist(e) {
		t.Fatal("failed publish left manifest")
	}
	var n int64
	svc.Store.DB.Model(&model.Version{}).Count(&n)
	if n != 0 {
		t.Fatal("failed publish left version")
	}
	var ev model.WebhookEvent
	svc.Store.DB.First(&ev)
	if ev.Status != "failed" {
		t.Fatal(ev)
	}
	svc.BeforeCommit = nil
	publish(t, api, svc, "1.2.3", "release")
}

func TestBadInputsAndRemovedEndpoints(t *testing.T) {
	api, svc := setup(t)
	for _, tc := range []struct {
		method, path, body string
		status             int
		headers            map[string]string
	}{{"GET", "/latest?channel=no", "", 400, nil}, {"GET", "/latest?current_version=1.2", "", 400, nil}, {"PUT", "/api/artifacts/1.2.3/a", "", 404, nil}, {"GET", "/api/admin/stats", "", 401, nil}, {"GET", "/api/admin/audit-logs", "", 401, nil}, {"OPTIONS", "/api/downloads/latest", "", 200, nil}, {"PATCH", "/api/admin/versions/1.2.3/force-update", `{"force_update":"true"}`, 400, admin}} {
		w := call(api, tc.method, tc.path, tc.body, tc.headers)
		if w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body)
		}
	}
	w := call(api, "GET", "/latest?channel=bad", "", nil)
	if w.Result().Header.Get("Cache-Control") != "no-store" {
		t.Fatal(w.Result().Header)
	}
	raw := payload("1.2.3", "release")
	var p map[string]any
	json.Unmarshal(raw, &p)
	p["force_update"] = "true"
	raw, _ = json.Marshal(p)
	if w = signed(api, svc, raw); w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
	svc.Config.MaxBodyBytes = 4
	w = call(api, "POST", "/api/webhook/release", "123456", nil)
	if w.Code != 413 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestSameVersionLockAndCacheConcurrency(t *testing.T) {
	api, svc := setup(t)
	publish(t, api, svc, "1.2.3", "release")
	unlock, e := svc.Lock("1.2.3")
	if e != nil {
		t.Fatal(e)
	}
	w := call(api, "POST", "/api/admin/versions/1.2.3/revoke", "", admin)
	unlock()
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := call(api, "GET", "/latest", "", nil); w.Code != 200 {
				t.Error(w.Code)
			}
		}()
	}
	wg.Wait()
}

func TestTrustedIP(t *testing.T) {
	r := NewIPResolver([]string{"127.0.0.1/32", "173.245.48.0/20"})
	for _, tc := range []struct{ remote, xff, cf, want string }{{"198.51.100.1:42", "1.1.1.1", "2.2.2.2", "198.51.100.1"}, {"127.0.0.1:42", "198.51.100.1, 173.245.48.1", "203.0.113.2", "203.0.113.2"}, {"127.0.0.1:42", "bad", "2.2.2.2", "127.0.0.1"}, {"[::ffff:198.51.100.1]:42", "", "", "198.51.100.1"}} {
		if got := r.Resolve(tc.remote, tc.xff, tc.cf); got != tc.want {
			t.Fatal(tc, got)
		}
	}
}

func TestDistRedirectAndRateLimit(t *testing.T) {
	api, svc := setup(t)
	svc.Config.DistServer = "https://downloads.example.com/base"
	plus := call(api, "GET", "/dist/1.2.3%2Bbuild/a%2Bb.exe", "", nil)
	if plus.Code != 302 || plus.Header().Get("Location") != "https://downloads.example.com/base/dist/1.2.3%2Bbuild/a%2Bb.exe" {
		t.Fatal(plus.Code, plus.Header())
	}
	w := call(api, "GET", "/dist/1.2.3/a%20b.exe?x=y", "", nil)
	if w.Code != 302 || w.Header().Get("Location") != "https://downloads.example.com/base/dist/1.2.3/a%20b.exe?x=y" {
		t.Fatal(w.Code, w.Header(), w.Body)
	}
	svc.Config.RateLimit = 1
	call(api, "GET", "/api/health", "", nil)
	w = call(api, "GET", "/api/downloads/latest", "", nil)
	if w.Code != 429 || w.Result().Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal(w.Code, w.Header())
	}
}

func TestWebhookAuthentication(t *testing.T) {
	_, svc := setup(t)
	if e := svc.AuthenticateWebhook([]byte("{}"), "bad", ""); e == nil {
		t.Fatal("bad timestamp")
	}
	if e := svc.AuthenticateWebhook([]byte("{}"), fmt.Sprint(time.Now().Add(-time.Hour).Unix()), ""); e == nil {
		t.Fatal("stale request")
	}
	if e := svc.AuthenticateWebhook([]byte("{}"), fmt.Sprint(time.Now().Unix()), "bad"); e == nil {
		t.Fatal("bad signature")
	}
	var apiErr *domain.Error
	if !errorsAs(svc.AuthenticateWebhook(nil, "0", ""), &apiErr) {
		t.Fatal("wrong error type")
	}
}
func errorsAs(e error, target **domain.Error) bool {
	v, ok := e.(*domain.Error)
	*target = v
	return ok
}
