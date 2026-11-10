package analytics

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/domain"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/store"
	ipdb "github.com/ipipdotnet/ipdb-go"
	"github.com/mileusna/useragent"
	maxminddb "github.com/oschwald/maxminddb-golang/v2"
)

const uaClassificationVersion = 1

var axolotlAgent = regexp.MustCompile(`(?i)(?:^|[ (;])axolotl(?:[- ]launcher)?(?:/|\s+)([0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.+-]+)?)`)

func applicationVersion(raw string) string {
	if match := axolotlAgent.FindStringSubmatch(raw); len(match) > 1 {
		return match[1]
	}
	if _, err := domain.ParseVersion(strings.TrimSpace(raw)); err == nil {
		return strings.TrimSpace(raw)
	}
	return ""
}

func applicationAgent(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	return axolotlAgent.MatchString(raw) || lower == "axolotl" || lower == "axolotl-launcher" || applicationVersion(raw) != ""
}

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
	case applicationAgent(raw):
		name, device = "Axolotl", "application"
		u.Version = ""
		u.Bot = false
		lower := strings.ToLower(raw)
		switch {
		case strings.Contains(lower, "windows"):
			osName = "Windows"
		case strings.Contains(lower, "macos") || strings.Contains(lower, "darwin"):
			osName = "macOS"
		case strings.Contains(lower, "linux"):
			osName = "Linux"
		}
	case u.Bot:
		device = "bot"
	case strings.HasPrefix(raw, "curl/") || strings.HasPrefix(raw, "Wget/"):
		device = "cli"
	case u.Tablet:
		device = "tablet"
	case u.Mobile:
		device = "mobile"
	case u.Desktop:
		device = "desktop"
	}
	return userAgent{UserAgent: raw, Browser: &name, BrowserVersion: optionalString(u.Version), OS: &osName, OSVersion: optionalString(u.OSVersion), DeviceType: &device, IsBot: model.Ptr(u.Bot), ClassificationVersion: uaClassificationVersion}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// Local lookups and cached API results are used by collection. Network fallback
// is queued separately and never runs inside a database write transaction.
type GeoIP struct {
	City, ASN    *maxminddb.Reader
	qqwry        *ipdb.City
	qqLookup     func(string) ([]string, error)
	apiURL       string
	mu           sync.Mutex
	cache        map[string]geoCacheEntry
	pending      map[string]bool
	calls        []time.Time
	blockedUntil time.Time
	queue        chan netip.Addr
	cancel       context.CancelFunc
	done         chan struct{}
	rateStore    *store.Store
}

func OpenGeoIP(cityPath, asnPath string) (*GeoIP, error) {
	return OpenGeoIPWithOptions(cityPath, asnPath, "", "")
}

func OpenGeoIPWithOptions(cityPath, asnPath, qqwryPath, apiURL string) (*GeoIP, error) {
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
	if qqwryPath != "" {
		g.qqwry, err = ipdb.NewCity(qqwryPath)
		if err != nil {
			g.Close()
			return nil, fmt.Errorf("open QQWry IPDB: %w", err)
		}
		g.qqLookup = func(ip string) ([]string, error) { return g.qqwry.Find(ip, "CN") }
		cn := false
		for _, language := range g.qqwry.Languages() {
			cn = cn || language == "CN"
		}
		if !cn {
			g.Close()
			return nil, fmt.Errorf("QQWry IPDB requires CN language")
		}
	}
	if err = g.startAPI(apiURL); err != nil {
		g.Close()
		return nil, err
	}
	return g, nil
}
func (g *GeoIP) Close() {
	if g == nil {
		return
	}
	if g.cancel != nil {
		g.cancel()
		<-g.done
	}
	if g.City != nil {
		_ = g.City.Close()
	}
	if g.ASN != nil {
		_ = g.ASN.Close()
	}
}
func (g *GeoIP) enrichMMDB(e *Event, ip netip.Addr) {
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
			if name := geoName(row.City.Names); name != "" {
				e.City = &name
			}
			if len(row.Subdivisions) > 0 {
				if name := geoName(row.Subdivisions[len(row.Subdivisions)-1].Names); name != "" {
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
