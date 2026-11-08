package analytics

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
	"github.com/mileusna/useragent"
	maxminddb "github.com/oschwald/maxminddb-golang/v2"
)

func classifyUA(raw string) userAgent {
	u := useragent.Parse(raw)
	name, osName := u.Name, u.OS
	if name == "" {
		name = "Unknown"
	}
	if osName == "" {
		osName = "Unknown"
	}
	device := "unknown"
	switch {
	case u.Bot:
		device = "bot"
	case strings.Contains(strings.ToLower(raw), "axolotl"):
		device = "application"
	case strings.HasPrefix(raw, "curl/") || strings.HasPrefix(raw, "Wget/"):
		device = "cli"
	case u.Tablet:
		device = "tablet"
	case u.Mobile:
		device = "mobile"
	case u.Desktop:
		device = "desktop"
	}
	return userAgent{UserAgent: raw, Browser: &name, BrowserVersion: model.Ptr(u.Version), OS: &osName, OSVersion: model.Ptr(u.OSVersion), DeviceType: &device, IsBot: model.Ptr(u.Bot)}
}

// GeoIP uses operator-provided MMDB files; no network calls occur while collecting requests.
type GeoIP struct{ City, ASN *maxminddb.Reader }

func OpenGeoIP(cityPath, asnPath string) (*GeoIP, error) {
	g := &GeoIP{}
	var err error
	if cityPath != "" {
		g.City, err = maxminddb.Open(cityPath)
		if err != nil {
			return nil, fmt.Errorf("open GeoIP city database: %w", err)
		}
	}
	if asnPath != "" {
		g.ASN, err = maxminddb.Open(asnPath)
		if err != nil {
			g.Close()
			return nil, fmt.Errorf("open GeoIP ASN database: %w", err)
		}
	}
	return g, nil
}
func (g *GeoIP) Close() {
	if g == nil {
		return
	}
	if g.City != nil {
		_ = g.City.Close()
	}
	if g.ASN != nil {
		_ = g.ASN.Close()
	}
}
func (g *GeoIP) Enrich(e *Event, ip netip.Addr) {
	if g == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return
	}
	if g.City != nil {
		var row struct {
			Country struct {
				Code string `maxminddb:"iso_code"`
			} `maxminddb:"country"`
			Continent struct {
				Code string `maxminddb:"code"`
			} `maxminddb:"continent"`
			City struct {
				Names map[string]string `maxminddb:"names"`
			} `maxminddb:"city"`
			Subdivisions []struct {
				Names map[string]string `maxminddb:"names"`
			} `maxminddb:"subdivisions"`
			Location struct {
				Latitude  *float64 `maxminddb:"latitude"`
				Longitude *float64 `maxminddb:"longitude"`
			} `maxminddb:"location"`
		}
		if err := g.City.Lookup(ip).Decode(&row); err == nil {
			if row.Country.Code != "" {
				e.Country = &row.Country.Code
			}
			if row.Continent.Code != "" {
				e.Continent = &row.Continent.Code
			}
			if name := row.City.Names["en"]; name != "" {
				e.City = &name
			}
			if len(row.Subdivisions) > 0 {
				if name := row.Subdivisions[0].Names["en"]; name != "" {
					e.Region = &name
				}
			}
			e.Latitude = row.Location.Latitude
			e.Longitude = row.Location.Longitude
		}
	}
	if g.ASN != nil {
		var row struct {
			Number       uint   `maxminddb:"autonomous_system_number"`
			Organization string `maxminddb:"autonomous_system_organization"`
		}
		if err := g.ASN.Lookup(ip).Decode(&row); err == nil && row.Number > 0 {
			e.ASN = model.Ptr(int64(row.Number))
			if row.Organization != "" {
				e.ASNOrg = &row.Organization
			}
		}
	}
}
