package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/domain"
)

func TestDedicatedDistributionDomain(t *testing.T) {
	api, svc := setup(t)
	svc.Config.DistPublicBaseURL = "https://dist.axlmc.org"
	publish(t, api, svc, "1.2.3", "release")
	assertManifest := func(data []byte) {
		t.Helper()
		var m domain.Manifest
		if e := json.Unmarshal(data, &m); e != nil {
			t.Fatal(e)
		}
		for _, entry := range m.Platforms {
			if entry.URL != "https://dist.axlmc.org/dist/1.2.3/update.tar.gz" {
				t.Fatal(entry.URL)
			}
		}
		if len(m.Platforms) == 0 {
			t.Fatal("missing updater platforms")
		}
	}
	w := call(api, "GET", "/latest", "", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	assertManifest(w.Body.Bytes())
	manifestPath := filepath.Join(svc.Config.DistRoot, "1.2.3", "manifest.json")
	data, e := os.ReadFile(manifestPath)
	if e != nil {
		t.Fatal(e)
	}
	assertManifest(data)
	w = call(api, "GET", "/api/downloads/1.2.3?include_updater=true", "", nil)
	var catalog struct {
		Downloads []struct{ Filename, URL string }
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &catalog) != nil || len(catalog.Downloads) == 0 {
		t.Fatal(w.Code, w.Body)
	}
	for _, a := range catalog.Downloads {
		if a.URL != "https://dist.axlmc.org/dist/1.2.3/"+a.Filename {
			t.Fatal(a.URL)
		}
	}
	if e = svc.Rebuild(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = svc.VerifyManifests(context.Background()); e != nil {
		t.Fatal(e)
	}
	data, e = os.ReadFile(manifestPath)
	if e != nil {
		t.Fatal(e)
	}
	assertManifest(data)
	// The advertised domain does not activate DIST_SERVER redirect mode.
	for _, host := range []string{"update.axlmc.org", "dist.axlmc.org"} {
		w = call(api, "GET", "https://"+host+"/dist/1.2.3/installer.exe", "", nil)
		if w.Code != 200 || w.Body.String() != "installer" || w.Header().Get("Location") != "" {
			t.Fatal(host, w.Code, w.Body)
		}
	}
}
