package domain

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
	"github.com/Masterminds/semver/v3"
)

type Error struct {
	Code, Message string
	Status        int
}

func (e *Error) Error() string { return e.Message }
func Err(code, message string, status ...int) *Error {
	s := 400
	if len(status) > 0 {
		s = status[0]
	}
	return &Error{code, message, s}
}
func ParseVersion(v string) (*semver.Version, error) {
	p, e := semver.StrictNewVersion(v)
	if e != nil {
		return nil, Err("invalid_version", "Version must be a normalized SemVer.")
	}
	return p, nil
}

var Platforms = map[string]bool{"windows-x86_64": true, "linux-x86_64": true, "linux-aarch64": true, "darwin-x86_64": true, "darwin-aarch64": true}
var PackagePlatforms = map[string]bool{"windows": true, "macos": true, "linux": true}

func ChannelValid(s string) bool { return s == "release" || s == "beta" }
func SafeComponent(s string) bool {
	if s == "" || strings.ContainsAny(s, "/\\") || strings.Contains(s, "..") {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

// EscapeSegment encodes a path component using RFC3986 unreserved characters.
func EscapeSegment(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
func HashValid(s string) bool       { b, e := hex.DecodeString(s); return e == nil && len(b) == 32 }

func ArtifactPlatforms(a model.Artifact, all []model.Artifact) model.Strings {
	if len(a.TargetPlatforms) > 0 {
		return a.TargetPlatforms
	}
	if a.Kind == "updater" && a.Platform == "darwin-aarch64" && a.Architecture == "universal" {
		for _, b := range all {
			if b.ID == a.ID || b.Kind != "updater" {
				continue
			}
			for _, p := range b.TargetPlatforms {
				if p == "darwin-x86_64" {
					return model.Strings{"darwin-aarch64"}
				}
			}
			if b.Platform == "darwin-x86_64" || (b.Platform == "darwin-aarch64" && b.Architecture == "universal" && b.ID < a.ID) {
				return model.Strings{"darwin-aarch64"}
			}
		}
		return model.Strings{"darwin-aarch64", "darwin-x86_64"}
	}
	if a.Platform == "" {
		return model.Strings{}
	}
	return model.Strings{a.Platform}
}

func Normalize(v *model.Version) {
	if v.Artifacts == nil {
		v.Artifacts = []model.Artifact{}
	}
	for i := range v.Artifacts {
		v.Artifacts[i].TargetPlatforms = ArtifactPlatforms(v.Artifacts[i], v.Artifacts)
	}
}

func ArtifactValid(v model.Version, a model.Artifact) bool {
	return SafeComponent(a.Filename) && a.RelativePath == "dist/"+v.Version+"/"+a.Filename && a.Size >= 0 && HashValid(a.SHA256)
}
func Signed(a model.Artifact) bool {
	return a.Kind == "updater" && a.Signature != nil && strings.TrimSpace(*a.Signature) != ""
}

type PlatformEntry struct {
	Signature string `json:"signature"`
	URL       string `json:"url"`
}
type Manifest struct {
	Version     string                   `json:"version"`
	Notes       string                   `json:"notes"`
	PubDate     string                   `json:"pub_date"`
	PublishedAt string                   `json:"published_at"`
	ForceUpdate bool                     `json:"force_update"`
	Platforms   map[string]PlatformEntry `json:"platforms"`
}

func BuildManifest(v model.Version, base, platform string, now time.Time) (Manifest, error) {
	at := now
	if v.PublishedAt != nil {
		at = *v.PublishedAt
	}
	date := at.UTC().Format(time.RFC3339Nano)
	m := Manifest{v.Version, v.Notes, date, date, v.ForceUpdate, map[string]PlatformEntry{}}
	for _, a := range v.Artifacts {
		if !Signed(a) || !ArtifactValid(v, a) {
			continue
		}
		for _, p := range ArtifactPlatforms(a, v.Artifacts) {
			if platform != "" && platform != p {
				continue
			}
			entry := PlatformEntry{*a.Signature, base + "/" + a.RelativePath}
			if old, ok := m.Platforms[p]; ok && old != entry {
				return m, Err("ambiguous_updater_platform", fmt.Sprintf("Multiple updater artifacts target %s.", p), 500)
			}
			m.Platforms[p] = entry
		}
	}
	return m, nil
}

func Eligible(v model.Version, channel string) bool {
	if v.Status != "published" {
		return false
	}
	p, e := ParseVersion(v.Version)
	if e != nil {
		return false
	}
	if channel == "release" {
		return v.Channel == "release" && p.Prerelease() == ""
	}
	return (v.Channel == "release" && p.Prerelease() == "") || (v.Channel == "beta" && p.Prerelease() != "")
}
