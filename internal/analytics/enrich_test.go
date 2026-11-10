package analytics

import "testing"

func TestUserAgentClassification(t *testing.T) {
	for _, tc := range []struct{ raw, device, browser string }{{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36", "desktop", "Chrome"}, {"curl/8.0.1", "cli", ""}, {"Axolotl-Launcher/1.9.4", "application", ""}, {"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", "bot", ""}} {
		ua := classifyUA(tc.raw)
		if ua.DeviceType == nil || *ua.DeviceType != tc.device || ua.Browser == nil || (tc.browser != "" && *ua.Browser != tc.browser) {
			t.Fatal(tc, ua)
		}
		if tc.device == "bot" && (ua.IsBot == nil || !*ua.IsBot) {
			t.Fatal(ua)
		}
	}
}
func TestGeoIPOptionalAndBadPath(t *testing.T) {
	g, e := OpenGeoIP("", "")
	if e != nil {
		t.Fatal(e)
	}
	g.Close()
	if _, e = OpenGeoIP("does-not-exist.mmdb", ""); e == nil {
		t.Fatal("invalid path accepted")
	}
}

func TestApplicationUAClassification(t *testing.T) {
	for _, tc := range []struct{ raw, os, version string }{
		{"axolotl/1.10.0", "Unknown", "1.10.0"},
		{"Axolotl Launcher/1.10.5 (windows)", "Windows", "1.10.5"},
		{"Axolotl-Launcher/1.10.5 (macos)", "macOS", "1.10.5"},
		{"Axolotl-Launcher/1.10.5 (linux)", "Linux", "1.10.5"},
		{"1.10.5", "Unknown", "1.10.5"},
	} {
		u := classifyUA(tc.raw)
		if *u.Browser != "Axolotl" || u.BrowserVersion != nil || *u.DeviceType != "application" || *u.OS != tc.os || applicationVersion(tc.raw) != tc.version {
			t.Fatalf("%s: %+v", tc.raw, u)
		}
	}
	if applicationAgent("Mozilla/5.0 Chrome/120.0.0.0 Safari/537.36") || applicationAgent("https://axolotl.example") {
		t.Fatal("misclassified browser or URL")
	}
}
