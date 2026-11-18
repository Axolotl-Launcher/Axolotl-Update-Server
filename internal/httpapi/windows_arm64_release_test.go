package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/domain"
)

func windowsReleasePayload(t *testing.T, version string) (map[string]any, map[string]string) {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(payload(version, "release"), &p); err != nil {
		t.Fatal(err)
	}
	assets := []map[string]any{}
	files := []map[string]any{}
	artifacts := []map[string]any{}
	bodies := map[string]string{}
	addFile := func(name, body string) {
		sum := sha256.Sum256([]byte(body))
		hash := hex.EncodeToString(sum[:])
		u := "https://github.com/Axolotl-Launcher/Axolotl/releases/download/v" + version + "/" + name
		bodies[u] = body
		assets = append(assets, map[string]any{"name": name, "size": len(body), "digest": "sha256:" + hash, "browser_download_url": u})
		files = append(files, map[string]any{"filename": name, "size": len(body), "sha256": hash, "downloadUrl": u})
	}
	for _, arch := range []struct{ suffix, name string }{{"arm64", "aarch64"}, {"x64", "x86_64"}} {
		updater := "Axolotl_" + arch.suffix + "-setup.nsis.zip"
		platforms := []string{"windows-" + arch.name}
		addFile(updater, "package-"+arch.suffix)
		addFile(updater+".sig", "tauri-signature-"+arch.suffix)
		artifacts = append(artifacts, map[string]any{
			"filename": updater, "kind": "updater", "platform": platforms[0], "architecture": arch.name,
			"targetPlatforms": platforms, "signatureFilename": updater + ".sig", "variant": "tauri",
		})
		for _, pkg := range []struct{ suffix, kind, variant string }{{"_modern-setup.exe", "installer", "modern"}, {"_nsis-setup.exe", "installer", "native"}, {"_portable.zip", "portable", "portable"}} {
			name := "Axolotl_" + arch.suffix + pkg.suffix
			sortOrder := 10
			if arch.name == "aarch64" {
				sortOrder = -10
			}
			addFile(name, "package")
			artifacts = append(artifacts, map[string]any{
				"filename": name, "kind": pkg.kind, "platform": "windows", "architecture": arch.name, "variant": pkg.variant, "sort_order": sortOrder,
			})
		}
	}
	p["release"].(map[string]any)["assets"] = assets
	p["catalog"] = map[string]any{"version": version, "files": files, "artifacts": artifacts}
	return p, bodies
}

func TestWindowsARM64Release(t *testing.T) {
	api, svc := setup(t)
	p, bodies := windowsReleasePayload(t, "1.11.0")
	svc.FetchSignature = func(_ context.Context, u string, _ int64, _ string) (string, error) {
		body, ok := bodies[u]
		if !ok || !strings.HasSuffix(u, ".sig") {
			return "", fmt.Errorf("unexpected signature URL: %s", u)
		}
		return body, nil
	}
	svc.FetchArtifact = func(_ context.Context, u string, _ int64, _ string, target string) error {
		body, ok := bodies[u]
		if !ok {
			return fmt.Errorf("unexpected artifact URL: %s", u)
		}
		return os.WriteFile(target, []byte(body), 0644)
	}
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
		// Before ARM64 support, Windows clients (including x64 under emulation)
		// send these headers. They must keep receiving the x64 updater.
		{"/latest", "windows-x86_64", "x64", map[string]string{"X-Axolotl-Platform": "windows-x86_64", "X-Axolotl-Version": "1.10.5", "X-Axolotl-Channel": "release"}},
		{"/latest?platform=windows-aarch64", "windows-x86_64", "x64", map[string]string{"X-Axolotl-Platform": "windows-x86_64", "X-Axolotl-Version": "1.10.5"}},
		{"/latest?platform=windows-x86_64&current_version=1.10.5", "windows-x86_64", "x64", nil},
	} {
		w := call(api, "GET", tc.path, "", tc.headers)
		var m domain.Manifest
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &m) != nil {
			t.Fatal(tc.platform, w.Code, w.Body)
		}
		entry, ok := m.Platforms[tc.platform]
		if !ok || m.Version != "1.11.0" || len(m.Platforms) != 1 || entry.Signature != "tauri-signature-"+tc.suffix || !strings.HasSuffix(entry.URL, "/Axolotl_"+tc.suffix+"-setup.nsis.zip") {
			t.Fatalf("wrong updater for %s: %+v", tc.platform, m)
		}
		if w = call(api, "GET", entry.URL, "", nil); w.Code != 200 || w.Body.String() != "package-"+tc.suffix {
			t.Fatal(w.Code, w.Body)
		}
	}
	for _, path := range []string{"/latest", "/dist/1.11.0/manifest.json"} {
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
	for _, path := range []string{"/api/downloads/latest", "/api/downloads/1.11.0"} {
		w := call(api, "GET", path, "", nil)
		var catalog struct {
			Downloads []struct{ Kind, Platform, Architecture, Filename string }
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &catalog) != nil || len(catalog.Downloads) != 6 {
			t.Fatal(path, w.Code, w.Body)
		}
		wantNames := []string{
			"Axolotl_x64_modern-setup.exe", "Axolotl_x64_nsis-setup.exe",
			"Axolotl_arm64_modern-setup.exe", "Axolotl_arm64_nsis-setup.exe",
			"Axolotl_x64_portable.zip", "Axolotl_arm64_portable.zip",
		}
		counts := map[string]int{}
		for i, a := range catalog.Downloads {
			if a.Platform != "windows" || (a.Kind != "installer" && a.Kind != "portable") {
				t.Fatalf("unexpected package: %+v", a)
			}
			if a.Filename != wantNames[i] {
				t.Fatalf("%s: download %d = %s, want %s", path, i, a.Filename, wantNames[i])
			}
			counts[a.Architecture]++
		}
		if counts["aarch64"] != 3 || counts["x86_64"] != 3 {
			t.Fatal("missing Windows packages", counts)
		}
	}
	// A newer ARM-only release must never displace the latest compatible x64
	// release for an old Windows client, even after the ARM cache is populated.
	p, bodies = windowsReleasePayload(t, "1.11.1")
	catalog := p["catalog"].(map[string]any)
	release := p["release"].(map[string]any)
	for _, field := range []struct {
		object           map[string]any
		key, filenameKey string
	}{{catalog, "files", "filename"}, {catalog, "artifacts", "filename"}, {release, "assets", "name"}} {
		rows := []map[string]any{}
		for _, row := range field.object[field.key].([]map[string]any) {
			if !strings.Contains(row[field.filenameKey].(string), "_x64") {
				rows = append(rows, row)
			}
		}
		field.object[field.key] = rows
	}
	data, err = json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if w := signed(api, svc, data); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	for _, tc := range []struct{ platform, version, suffix string }{
		{"windows-aarch64", "1.11.1", "arm64"},
		{"windows-x86_64", "1.11.0", "x64"},
	} {
		w := call(api, "GET", "/latest", "", map[string]string{"X-Axolotl-Platform": tc.platform, "X-Axolotl-Version": "1.10.5"})
		var m domain.Manifest
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &m) != nil || m.Version != tc.version || len(m.Platforms) != 1 {
			t.Fatal(tc.platform, w.Code, w.Body)
		}
		entry := m.Platforms[tc.platform]
		if entry.Signature != "tauri-signature-"+tc.suffix || !strings.HasSuffix(entry.URL, "/Axolotl_"+tc.suffix+"-setup.nsis.zip") {
			t.Fatal("incompatible updater", tc.platform, m)
		}
	}
	if w := call(api, "GET", "/latest", "", map[string]string{"X-Axolotl-Platform": "windows-x86_64", "X-Axolotl-Version": "1.11.0"}); w.Code != 204 {
		t.Fatal("x64 client received an incompatible newer release", w.Code, w.Body)
	}

}
