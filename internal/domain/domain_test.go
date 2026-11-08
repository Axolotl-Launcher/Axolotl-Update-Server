package domain

import (
	"testing"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
)

func TestStrictSemverAndSelection(t *testing.T) {
	for _, v := range []string{"v1.2.3", "1.2", "01.2.3", "1.2.3-beta.01", "1.2.3 ", "1.2.3.4"} {
		if _, e := ParseVersion(v); e == nil {
			t.Fatal("accepted", v)
		}
	}
	stable := model.Version{Version: "1.10.0", Channel: "release", Status: "published"}
	beta := model.Version{Version: "1.11.0-beta.2", Channel: "beta", Status: "published"}
	if !Eligible(stable, "release") || !Eligible(stable, "beta") || Eligible(beta, "release") || !Eligible(beta, "beta") {
		t.Fatal("channel selection")
	}
	a, _ := ParseVersion("1.10.0")
	b, _ := ParseVersion("1.9.9")
	if !a.GreaterThan(b) {
		t.Fatal("lexicographic comparison")
	}
}
func TestLegacyUniversalAndAmbiguity(t *testing.T) {
	a := model.Artifact{ID: 1, Platform: "darwin-aarch64", Architecture: "universal", Kind: "updater"}
	if len(ArtifactPlatforms(a, []model.Artifact{a})) != 2 {
		t.Fatal("universal mapping")
	}
	b := model.Artifact{ID: 2, Platform: "darwin-x86_64", Kind: "updater"}
	if len(ArtifactPlatforms(a, []model.Artifact{a, b})) != 1 {
		t.Fatal("explicit Intel collision")
	}
	sig := "sig"
	a.Signature = &sig
	a.Filename = "a"
	a.RelativePath = "dist/1.2.3/a"
	a.SHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	b = a
	b.ID = 2
	b.Filename = "b"
	b.RelativePath = "dist/1.2.3/b"
	b.TargetPlatforms = model.Strings{"darwin-aarch64"}
	_, e := BuildManifest(model.Version{Version: "1.2.3", Artifacts: []model.Artifact{a, b}}, "https://example.com", "", time.Now())
	if e == nil {
		t.Fatal("ambiguous manifest accepted")
	}
}
