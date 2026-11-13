package httpapi

import (
	"strings"
	"testing"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
	"github.com/gin-gonic/gin"
)

func TestLegacyWindowsDownloadOrder(t *testing.T) {
	api, _ := setup(t)
	for _, kind := range []string{"installer", "portable"} {
		for _, architecture := range []string{"", "unknown", "x86_64"} {
			t.Run(kind+"/"+architecture, func(t *testing.T) {
				artifact := func(name, arch string, sortOrder int) model.Artifact {
					return model.Artifact{
						Filename: name, RelativePath: "dist/1.2.3/" + name,
						Platform: "windows", Architecture: arch, Kind: kind,
						IsPublic: true, SHA256: strings.Repeat("a", 64), SortOrder: sortOrder,
					}
				}
				// ARM64 wins on input order, name and sort_order unless the
				// architecture rank keeps legacy Windows packages with x64.
				v := model.Version{Version: "1.2.3", Artifacts: []model.Artifact{
					artifact("a_arm64", "aarch64", -10),
					artifact("x_x64", "x86_64", 10),
					artifact("z_legacy", architecture, 5),
				}}
				downloads := api.downloadPayload(v, false)["downloads"].([]gin.H)
				if len(downloads) != 3 {
					t.Fatal(downloads)
				}
				for i, want := range []string{"z_legacy", "x_x64", "a_arm64"} {
					if downloads[i]["filename"] != want {
						t.Fatalf("download %d = %v, want %s", i, downloads[i]["filename"], want)
					}
				}
				if downloads[0]["architecture"] != architecture {
					t.Fatal("sorting changed stored architecture", downloads[0])
				}
			})
		}
	}
}
