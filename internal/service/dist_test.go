package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/config"
)

func TestArtifactStreamIntegrityAndHTTPFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body, expected string
		size                 int64
		status               int
		want                 bool
	}{
		{"valid", "package", "package", 7, 200, true},
		{"empty", "", "", 0, 200, true},
		{"truncated", "pack", "package", 7, 200, false},
		{"too long", "package-extra", "package", 7, 200, false},
		{"wrong digest", "package", "different", 7, 200, false},
		{"not found", "", "", 0, 404, false},
		{"throttled", "", "", 0, 429, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Accept-Encoding") != "identity" {
					t.Error("artifact compression not disabled")
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c := config.Default()
			c.DownloadRetries = 0
			s := New(nil, c)
			h := sha256.Sum256([]byte(tc.expected))
			path := filepath.Join(t.TempDir(), "staged")
			e := s.fetchArtifact(context.Background(), server.URL, tc.size, hex.EncodeToString(h[:]), path)
			if (e == nil) != tc.want {
				t.Fatal(e)
			}
			if tc.want {
				body, e := os.ReadFile(path)
				if e != nil || string(body) != tc.body {
					t.Fatal(string(body), e)
				}
			}
		})
	}
}

func TestArtifactRetryTruncatesStagingFile(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Content-Length", "7")
			w.Write([]byte("part"))
			return
		}
		w.Write([]byte("package"))
	}))
	defer server.Close()
	c := config.Default()
	c.DownloadRetries = 1
	s := New(nil, c)
	h := sha256.Sum256([]byte("package"))
	path := filepath.Join(t.TempDir(), "staged")
	if e := s.fetchArtifact(context.Background(), server.URL, 7, hex.EncodeToString(h[:]), path); e != nil {
		t.Fatal(e)
	}
	if body, e := os.ReadFile(path); e != nil || string(body) != "package" || calls != 2 {
		t.Fatal(string(body), calls, e)
	}
}

func TestDistPathAndRedirectValidation(t *testing.T) {
	c := config.Default()
	c.DistRoot = t.TempDir()
	s := New(nil, c)
	for _, name := range []string{"../a", "sub/a", "a\\b", "C:stream", "", ".."} {
		if _, e := s.ArtifactPath("1.2.3", name); e == nil {
			t.Fatal("unsafe path accepted", name)
		}
	}
	for _, value := range []string{"http://github.com/a", "https://evil.example/a", "https://github.com:444/a", "https://user@github.com/a"} {
		u, _ := url.Parse(value)
		if e := assetRedirect(&http.Request{URL: u}, nil); e == nil {
			t.Fatal("untrusted redirect", value)
		}
	}
	want := "https://github.com/Axolotl-Launcher/Axolotl/releases/download/v1.2.3%2Bbuild/a%20b%2B.zip"
	if got := GitHubAssetURL("v1.2.3+build", "a b+.zip"); got != want || strings.Contains(got, "?") {
		t.Fatal(got)
	}
}
