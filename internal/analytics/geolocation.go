package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
)

const geoCacheLimit = 10000

type geoCacheEntry struct {
	location Event
	expires  time.Time
}

func geoName(names map[string]string) string {
	if name := names["zh-CN"]; name != "" {
		return name
	}
	return names["en"]
}

func geoString(s string) *string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ToValidUTF8(s, ""), "\x00", ""))
	if s == "" || s == "0" {
		return nil
	}
	return &s
}

func geoCode(s string) *string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) != 2 || s[0] < 'A' || s[0] > 'Z' || s[1] < 'A' || s[1] > 'Z' {
		return nil
	}
	return &s
}

func hasGeo(e Event) bool       { return e.Country != nil || e.Region != nil || e.City != nil }
func hasGeoDetail(e Event) bool { return e.Region != nil || e.City != nil }

func applyLocation(e *Event, loc Event) {
	e.Country, e.Continent, e.Region, e.City = loc.Country, loc.Continent, loc.Region, loc.City
	e.Latitude, e.Longitude = loc.Latitude, loc.Longitude
}

func applyFallback(e *Event, loc Event) {
	if loc.Country == nil {
		loc.Country = e.Country
	}
	if loc.Continent == nil {
		loc.Continent = e.Continent
	}
	applyLocation(e, loc)
}

func publicGeoIP(ip netip.Addr) bool {
	return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback()
}

// China is answered exclusively by QQWry if it matches. MMDB still supplies ASN,
// but its approximate city coordinates are not mixed with QQWry city names.
func (g *GeoIP) localLocation(ip netip.Addr) (loc Event, needsAPI bool) {
	g.enrichMMDB(&loc, ip)
	if g.qqLookup != nil {
		if row, err := g.qqLookup(ip.String()); err == nil && len(row) >= 3 && row[0] == "中国" {
			loc.Country, loc.Continent = model.Ptr("CN"), model.Ptr("AS")
			loc.Region, loc.City = geoString(row[1]), geoString(row[2])
			loc.Latitude, loc.Longitude = nil, nil
			return loc, false
		}
	}
	return loc, !hasGeoDetail(loc)
}

// Enrich never performs a network request. An initial miss uses local data and
// warms the optional fallback cache for subsequent requests.
func (g *GeoIP) Enrich(e *Event, ip netip.Addr) {
	if g == nil || !publicGeoIP(ip.Unmap()) {
		return
	}
	ip = ip.Unmap()
	loc, needsAPI := g.localLocation(ip)
	if needsAPI && g.apiURL != "" {
		if cached, ok := g.cachedAPI(ip.String()); ok {
			if hasGeoDetail(cached) {
				applyFallback(&loc, cached)
			}
		} else {
			g.enqueueAPI(ip)
		}
	}
	if hasGeo(loc) {
		applyLocation(e, loc)
	}
	if loc.ASN != nil {
		e.ASN, e.ASNOrg = loc.ASN, loc.ASNOrg
	}
}

// Recompute performs optional fallback synchronously, outside database locks.
// Failed/limited fallback returns available local data; old data is not erased.
func (g *GeoIP) Recompute(ctx context.Context, ip netip.Addr) Event {
	if g == nil || !publicGeoIP(ip.Unmap()) {
		return Event{}
	}
	ip = ip.Unmap()
	loc, needsAPI := g.localLocation(ip)
	if needsAPI && g.apiURL != "" {
		api := g.lookupAPI(ctx, ip)
		if hasGeoDetail(api) || (!hasGeo(loc) && hasGeo(api)) {
			applyFallback(&loc, api)
		}
	}
	return loc
}

func (g *GeoIP) startAPI(value string) error {
	if value == "" {
		return nil
	}
	if strings.Count(value, "{ip}") != 1 {
		return fmt.Errorf("ANALYTICS_GEOIP_API_URL requires exactly one {ip} placeholder")
	}
	u, e := url.Parse(strings.ReplaceAll(value, "{ip}", "8.8.8.8"))
	if e != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("invalid ANALYTICS_GEOIP_API_URL")
	}
	g.apiURL = value
	g.cache, g.pending = map[string]geoCacheEntry{}, map[string]bool{}
	g.queue, g.done = make(chan netip.Addr, 256), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	go func() {
		defer close(g.done)
		for {
			select {
			case <-ctx.Done():
				return
			case ip := <-g.queue:
				g.lookupAPI(ctx, ip)
				g.mu.Lock()
				delete(g.pending, ip.String())
				g.mu.Unlock()
			}
		}
	}()
	return nil
}

func (g *GeoIP) cachedAPI(ip string) (Event, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	entry, ok := g.cache[ip]
	return entry.location, ok && time.Now().Before(entry.expires)
}

func (g *GeoIP) enqueueAPI(ip netip.Addr) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := ip.String()
	if g.pending[key] {
		return
	}
	select {
	case g.queue <- ip:
		g.pending[key] = true
	default:
	}
}

func (g *GeoIP) storeAPI(key string, loc Event, ttl time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.cache) >= geoCacheLimit {
		// Bound memory even when a large number of IPs arrives in one day.
		for k := range g.cache {
			delete(g.cache, k)
			break
		}
	}
	g.cache[key] = geoCacheEntry{loc, time.Now().Add(ttl)}
}

func (g *GeoIP) lookupAPI(ctx context.Context, ip netip.Addr) Event {
	key := ip.String()
	if loc, ok := g.cachedAPI(key); ok {
		return loc
	}
	now := time.Now()
	g.mu.Lock()
	i := 0
	for i < len(g.calls) && g.calls[i].Before(now.Add(-time.Minute)) {
		i++
	}
	g.calls = g.calls[i:]
	if now.Before(g.blockedUntil) || len(g.calls) >= 40 {
		g.mu.Unlock()
		return Event{}
	}
	g.calls = append(g.calls, now)
	g.mu.Unlock()
	if !g.sharedRate(ctx, nil) {
		return Event{}
	}
	loc := Event{}
	defer func() {
		ttl := 5 * time.Minute
		if hasGeoDetail(loc) {
			ttl = 24 * time.Hour
		}
		g.storeAPI(key, loc, ttl)
	}()
	req, e := http.NewRequestWithContext(ctx, "GET", strings.ReplaceAll(g.apiURL, "{ip}", url.PathEscape(key)), nil)
	if e != nil {
		return loc
	}
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(req)
	if e != nil {
		return loc
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 || resp.Header.Get("X-Rl") == "0" {
		seconds, _ := strconv.Atoi(resp.Header.Get("X-Ttl"))
		if seconds < 1 {
			seconds = 60
		}
		if seconds > 3600 {
			seconds = 3600
		}
		g.mu.Lock()
		g.blockedUntil = time.Now().Add(time.Duration(seconds) * time.Second)
		until := g.blockedUntil
		g.mu.Unlock()
		g.sharedRate(ctx, &until)
	}
	if resp.StatusCode != 200 {
		return loc
	}
	var data struct {
		Status, CountryCode, ContinentCode, RegionName, City string
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&data) != nil || data.Status != "success" {
		return loc
	}
	loc.Country, loc.Continent, loc.Region, loc.City = geoCode(data.CountryCode), geoCode(data.ContinentCode), geoString(data.RegionName), geoString(data.City)
	return loc
}
