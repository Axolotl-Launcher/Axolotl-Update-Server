package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/domain"
)

func TestWindowsARM64Release(t *testing.T) {
	api, svc := setup(t)
	var p map[string]any
	if err := json.Unmarshal(payload("1.2.3", "release"), &p); err != nil {
		t.Fatal(err)
	}
	assets := []map[string]any{}
	files := []map[string]any{}
	artifacts := []map[string]any{}
	addFile := func(name, body string) {
		sum := sha256.Sum256([]byte(body))
		hash := hex.EncodeToString(sum[:])
		u := "https://github.com/Axolotl-Launcher/Axolotl/releases/download/v1.2.3/" + name
		assets = append(assets, map[string]any{"name": name, "size": len(body), "digest": "sha256:" + hash, "browser_download_url": u})
		files = append(files, map[string]any{"filename": name, "size": len(body), "sha256": hash, "downloadUrl": u})
	}
	for _, arch := range []struct{ suffix, name string }{{"x64", "x86_64"}, {"arm64", "aarch64"}} {
		updater := "Axolotl_" + arch.suffix + "-setup.nsis.zip"
		platforms := []string{"windows-" + arch.name}
		addFile(updater, "package")
		addFile(updater+".sig", "tauri-signature")
		artifacts = append(artifacts, map[string]any{
			"filename": updater, "kind": "updater", "platform": platforms[0], "architecture": arch.name,
			"targetPlatforms": platforms, "signatureFilename": updater + ".sig", "variant": "tauri",
		})
		for _, pkg := range []struct{ suffix, kind, variant string }{{"_modern-setup.exe", "installer", "modern"}, {"_portable.zip", "portable", "portable"}} {
			name := "Axolotl_" + arch.suffix + pkg.suffix
			addFile(name, "package")
			artifacts = append(artifacts, map[string]any{
				"filename": name, "kind": pkg.kind, "platform": "windows", "architecture": arch.name, "variant": pkg.variant,
			})
		}
	}
	p["release"].(map[string]any)["assets"] = assets
	p["catalog"] = map[string]any{"version": "1.2.3", "files": files, "artifacts": artifacts}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if w := signed(api, svc, data); w.Code != 200 {
		t.Fatalf("publish Windows x64 and ARM64: %d %s", w.Code, w.Body)
	}
	for _, tc := range []struct {
		path, platform, suffix string
		headers                map[string]string
	}{
		{"/latest?platform=windows-aarch64", "windows-aarch64", "arm64", nil},
		{"/latest", "windows-aarch64", "arm64", map[string]string{"X-Axolotl-Platform": "windows-aarch64"}},
		{"/latest?platform=windows-x86_64", "windows-x86_64", "x64", nil},
	} {
		w := call(api, "GET", tc.path, "", tc.headers)
		var m domain.Manifest
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &m) != nil {
			t.Fatal(tc.platform, w.Code, w.Body)
		}
		entry, ok := m.Platforms[tc.platform]
		if !ok || len(m.Platforms) != 1 || entry.Signature != "tauri-signature" || !strings.HasSuffix(entry.URL, "/Axolotl_"+tc.suffix+"-setup.nsis.zip") {
			t.Fatalf("wrong updater for %s: %+v", tc.platform, m)
		}
		if w = call(api, "GET", entry.URL, "", nil); w.Code != 200 || w.Body.String() != "package" {
			t.Fatal(w.Code, w.Body)
		}
	}
	for _, path := range []string{"/latest", "/dist/1.2.3/manifest.json"} {
		w := call(api, "GET", path, "", nil)
		var m domain.Manifest
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &m) != nil || len(m.Platforms) != 2 {
			t.Fatal(path, w.Code, w.Body)
		}
		for _, platform := range []string{"windows-x86_64", "windows-aarch64"} {
			if _, ok := m.Platforms[platform]; !ok {
				t.Fatal("missing updater", platform, m)
			}
		}
	}
	w := call(api, "GET", "/api/downloads/latest", "", nil)
	var catalog struct {
		Downloads []struct{ Kind, Platform, Architecture, Filename string }
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &catalog) != nil || len(catalog.Downloads) != 4 {
		t.Fatal(w.Code, w.Body)
	}
	counts := map[string]int{}
	for _, a := range catalog.Downloads {
		if a.Platform != "windows" || (a.Kind != "installer" && a.Kind != "portable") {
			t.Fatalf("unexpected package: %+v", a)
		}
		counts[a.Architecture]++
	}
	if counts["aarch64"] != 2 || counts["x86_64"] != 2 {
		t.Fatal("missing Windows packages", counts)
	}
}
