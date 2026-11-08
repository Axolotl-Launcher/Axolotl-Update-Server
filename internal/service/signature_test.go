package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/config"
)

func TestFetchSignatureIntegrityAndLimits(t *testing.T) {
	for _, tc := range []struct {
		name, body, hashOverride string
		size                     int64
		want                     bool
	}{{"valid", " signature\n", "", 11, true}, {"invalid utf8", string([]byte{255}), "", 1, false}, {"empty", " \n", "", 2, false}, {"wrong digest", "signature", strings.Repeat("a", 64), 9, false}, {"wrong size", "signature", "", 8, false}, {"oversize", strings.Repeat("x", 65537), "", 65536, false}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(tc.body)) }))
			defer server.Close()
			sum := sha256.Sum256([]byte(tc.body))
			hash := hex.EncodeToString(sum[:])
			if tc.hashOverride != "" {
				hash = tc.hashOverride
			}
			c := config.Default()
			c.DownloadRetries = 0
			s := New(nil, c)
			signature, e := s.fetchSignature(context.Background(), server.URL, tc.size, hash)
			if (e == nil) != tc.want {
				t.Fatal(signature, e)
			}
		})
	}
}
func TestAssetURLValidation(t *testing.T) {
	if e := validateAssetURL("https://github.com/Axolotl-Launcher/Axolotl/releases/download/v1.2.3%2Bbuild/a%2Bb.sig", "v1.2.3+build", "a+b.sig"); e != nil {
		t.Fatal(e)
	}
	for _, u := range []string{"http://github.com/Axolotl-Launcher/Axolotl/releases/download/v1.2.3/a.sig", "https://evil.example/a.sig", "https://github.com/Axolotl-Launcher/Axolotl/releases/download/v1.2.3/a.sig?x=y"} {
		if validateAssetURL(u, "v1.2.3", "a.sig") == nil {
			t.Fatal(u)
		}
	}
	if e := validateAssetURL("https://github.com/Axolotl-Launcher/Axolotl/releases/download/v1.2.3/a%20b.sig", "v1.2.3", "a b.sig"); e != nil {
		t.Fatal(e)
	}
}
