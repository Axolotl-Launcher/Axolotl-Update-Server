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
