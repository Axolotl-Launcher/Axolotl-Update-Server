package httpapi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanupCannotDeleteRepublishedVersion(t *testing.T) {
	api, svc := setup(t)
	publish(t, api, svc, "1.2.3", "release")
	w := call(api, "POST", "/api/admin/versions/1.2.3/cleanup", "{}", admin)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "version_exists") {
		t.Fatal(w.Code, w.Body)
	}
	if _, e := os.Stat(filepath.Join(svc.Config.DistRoot, "1.2.3", "manifest.json")); e != nil {
		t.Fatal(e)
	}
	remove := svc.RemoveAll
	svc.RemoveAll = func(string) error { return fmt.Errorf("cleanup failed") }
	w = call(api, "DELETE", "/api/admin/versions/1.2.3", "{}", admin)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"files_cleanup_pending":true`) {
		t.Fatal(w.Code, w.Body)
	}
	svc.RemoveAll = remove
	w = call(api, "POST", "/api/admin/versions/1.2.3/cleanup", "{}", admin)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	w = call(api, "POST", "/api/admin/versions/1.2.3/cleanup", "{}", admin)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	publish(t, api, svc, "1.2.3", "release")
	w = call(api, "POST", "/api/admin/versions/1.2.3/cleanup", "{}", admin)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
}
