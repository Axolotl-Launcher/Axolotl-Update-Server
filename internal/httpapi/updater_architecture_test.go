package httpapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
)

func TestUpdaterPlatformArchitectures(t *testing.T) {
	for _, tc := range []struct {
		platform, architecture string
		targets                []string
		valid                  bool
	}{
		{"windows-aarch64", "aarch64", nil, true},
		{"windows-aarch64", "", nil, true},
		{"windows-x86_64", "x86_64", nil, true},
		{"linux-aarch64", "aarch64", nil, true},
		{"linux-x86_64", "x86_64", nil, true},
		{"darwin-aarch64", "aarch64", nil, true},
		{"darwin-x86_64", "x86_64", nil, true},
		{"darwin-aarch64", "universal", []string{"darwin-aarch64", "darwin-x86_64"}, true},
		{"windows-aarch64", "x86_64", nil, false},
		{"windows-x86_64", "aarch64", nil, false},
		{"windows-aarch64", "universal", nil, false},
		{"linux-x86_64", "aarch64", nil, false},
		{"linux-aarch64", "universal", nil, false},
		{"darwin-x86_64", "aarch64", nil, false},
		{"darwin-aarch64", "x86_64", nil, false},
		{"windows-aarch64", "aarch64", []string{"windows-aarch64", "windows-x86_64"}, false},
		{"darwin-aarch64", "aarch64", []string{"darwin-aarch64", "darwin-x86_64"}, false},
	} {
		t.Run(tc.platform+"/"+tc.architecture, func(t *testing.T) {
			api, svc := setup(t)
			var p map[string]any
			if err := json.Unmarshal(payload("1.2.3", "release"), &p); err != nil {
				t.Fatal(err)
			}
			a := p["catalog"].(map[string]any)["artifacts"].([]any)[0].(map[string]any)
			a["platform"] = tc.platform
			a["architecture"] = tc.architecture
			targets := tc.targets
			if targets == nil {
				targets = []string{tc.platform}
			}
			a["targetPlatforms"] = targets
			if tc.architecture == "" {
				delete(a, "architecture")
			}
			if !tc.valid {
				svc.FetchSignature = func(context.Context, string, int64, string) (string, error) {
					t.Fatal("invalid metadata fetched a signature")
					return "", nil
				}
				svc.FetchArtifact = func(context.Context, string, int64, string, string) error {
					t.Fatal("invalid metadata fetched an artifact")
					return nil
				}
			}
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			w := signed(api, svc, raw)
			if tc.valid {
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body)
				}
				var artifact model.Artifact
				if err = svc.Store.DB.Where("kind = ?", "updater").First(&artifact).Error; err != nil {
					t.Fatal(err)
				}
				want := tc.architecture
				if want == "" {
					want = "aarch64"
				}
				if artifact.Architecture != want {
					t.Fatal("wrong inferred architecture", artifact.Architecture)
				}
				return
			}
			var response struct {
				Error struct{ Code string }
			}
			if w.Code != 400 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Error.Code != "invalid_artifact_platform" {
				t.Fatal(w.Code, w.Body)
			}
			var count int64
			if err = svc.Store.DB.Model(&model.Version{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("invalid release persisted", count, err)
			}
		})
	}
}
