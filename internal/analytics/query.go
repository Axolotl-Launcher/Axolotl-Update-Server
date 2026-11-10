package analytics

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/domain"
)

var Dimensions = map[string]string{
	"route": "e.route", "method": "e.method", "host": "e.host", "scheme": "e.scheme", "http_version": "e.http_version", "tls_version": "e.tls_version",
	"status_code": "e.status_code", "status_class": "CASE WHEN e.status_code IS NOT NULL THEN CAST(e.status_code / 100 AS TEXT) || 'xx' END",
	"country": "e.country", "continent": "e.continent", "region": "e.region", "city": "e.city", "asn": "e.asn", "asn_org": "e.asn_org",
	"channel": "e.channel", "client_version": "e.client_version", "platform": "e.platform", "target_version": "e.target_version",
	"client_ip": "e.client_ip", "ip_version": "e.ip_version", "user_agent": "u.user_agent", "browser": "u.browser", "browser_version": "u.browser_version",
	"os": "u.os", "os_version": "u.os_version", "device_type": "u.device_type", "is_bot": "u.is_bot",
	"referer_host": "e.referer_host", "content_type": "e.content_type", "app_cache": "e.app_cache", "error_code": "e.error_code",
	"include_updater": "e.include_updater", "include_revoked": "e.include_revoked",
}

type Query struct {
	Start            time.Time         `json:"start"`
	End              time.Time         `json:"end"`
	Interval         string            `json:"interval"`
	Seconds          int64             `json:"interval_seconds"`
	Filters          map[string]string `json:"filters"`
	Limit            int               `json:"limit"`
	RetentionClamped bool              `json:"retention_clamped"`
	Cursor           *Cursor           `json:"-"`
}
type Cursor struct {
	Start, End, At time.Time
	ID             int64
	FilterHash     string
}

func badQuery(message string) error { return domain.Err("invalid_stats_query", message) }
func DimensionNames() []string {
	out := make([]string, 0, len(Dimensions))
	for k := range Dimensions {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func ParseQuery(values url.Values, now time.Time) (Query, error) {
	now = now.UTC()
	q := Query{Start: now.Add(-24 * time.Hour), End: now, Interval: "auto", Limit: 20, Filters: map[string]string{}}
	if raw := values.Get("cursor"); raw != "" {
		if len(raw) > 2048 {
			return q, badQuery("Invalid cursor.")
		}
		b, e := base64.RawURLEncoding.DecodeString(raw)
		if e != nil {
			return q, badQuery("Invalid cursor.")
		}
		var c Cursor
		if json.Unmarshal(b, &c) != nil || c.ID < 1 || c.Start.IsZero() || !c.End.After(c.Start) || c.At.Before(c.Start) || !c.At.Before(c.End) {
			return q, badQuery("Invalid cursor.")
		}
		q.Cursor = &c
		q.Start = c.Start
		q.End = c.End
	}
	for key, dst := range map[string]*time.Time{"start": &q.Start, "end": &q.End} {
		if raw := values.Get(key); raw != "" {
			v, e := time.Parse(time.RFC3339Nano, raw)
			if e != nil {
				return q, badQuery("start and end must be RFC3339 timestamps with timezone.")
			}
			*dst = v.UTC()
		}
	}
	if q.Cursor != nil && (!q.Cursor.Start.Equal(q.Start) || !q.Cursor.End.Equal(q.End)) {
		return q, badQuery("Cursor time window has changed; start a new request.")
	}
	if q.End.After(now.Add(time.Minute)) {
		return q, badQuery("end must not be in the future.")
	}
	if q.End.After(now) {
		q.End = now
	}
	cutoff := now.Add(-14 * 24 * time.Hour)
	if q.Start.Before(cutoff) {
		q.Start = cutoff
		q.RetentionClamped = true
	}
	if !q.End.After(q.Start) {
		return q, badQuery("end must be after start within the retained 14-day window.")
	}
	if raw := values.Get("limit"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 1000 {
			return q, badQuery("limit must be between 1 and 1000.")
		}
		q.Limit = n
	}
	if raw := values.Get("interval"); raw != "" {
		q.Interval = raw
	}
	intervals := map[string]int64{"1m": 60, "5m": 300, "15m": 900, "1h": 3600, "6h": 21600, "1d": 86400}
	if q.Interval == "auto" {
		q.Interval = "1m"
		for _, option := range []string{"1m", "5m", "15m", "1h", "6h", "1d"} {
			q.Interval = option
			if q.End.Sub(q.Start).Seconds()/float64(intervals[option]) <= 240 {
				break
			}
		}
	}
	seconds, ok := intervals[q.Interval]
	if !ok {
		return q, badQuery("interval must be auto, 1m, 5m, 15m, 1h, 6h or 1d.")
	}
	q.Seconds = seconds
	if q.End.Add(-time.Nanosecond).Unix()/seconds-q.Start.Unix()/seconds+1 > 2000 {
		return q, badQuery("Requested interval creates more than 2000 buckets.")
	}
	for key := range Dimensions {
		raw, exists := values[key]
		if !exists {
			continue
		}
		if len(raw) != 1 || len(raw[0]) > 8192 || strings.ContainsAny(raw[0], "\x00\r\n") {
			return q, badQuery("Invalid filter value.")
		}
		value := raw[0]
		switch key {
		case "client_ip":
			ip, e := netip.ParseAddr(value)
			if e != nil {
				return q, badQuery("client_ip must be a single IPv4 or IPv6 address.")
			}
			value = ip.Unmap().String()
		case "status_code":
			n, e := strconv.Atoi(value)
			if e != nil || n < 100 || n > 599 {
				return q, badQuery("status_code must be between 100 and 599.")
			}
		case "status_class":
			if len(value) != 3 || value[0] < '1' || value[0] > '5' || value[1:] != "xx" {
				return q, badQuery("status_class must be 1xx through 5xx.")
			}
		case "asn":
			n, e := strconv.ParseInt(value, 10, 64)
			if e != nil || n < 1 || n > 4294967295 {
				return q, badQuery("asn must be an unsigned 32-bit AS number.")
			}
		case "ip_version":
			if value != "4" && value != "6" {
				return q, badQuery("ip_version must be 4 or 6.")
			}
		case "include_updater", "include_revoked":
			if value != "true" && value != "false" && value != "@not_applicable" {
				return q, badQuery(key + " must be true, false or @not_applicable.")
			}
		case "is_bot":
			if value != "true" && value != "false" {
				return q, badQuery(key + " must be true or false.")
			}
		case "country", "continent":
			value = strings.ToUpper(value)
			if len(value) != 2 || value[0] < 'A' || value[0] > 'Z' || value[1] < 'A' || value[1] > 'Z' {
				return q, badQuery(key + " must be a two-letter code.")
			}
		}
		q.Filters[key] = value
	}
	if q.Cursor != nil && q.Cursor.FilterHash != q.filterHash() {
		return q, badQuery("Cursor filters do not match this request.")
	}
	return q, nil
}
func (q Query) filterHash() string {
	b, _ := json.Marshal(q.Filters)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (q Query) nextCursor(at time.Time, id int64) string {
	c := Cursor{q.Start, q.End, at, id, q.filterHash()}
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}
func (q Query) Window() map[string]any {
	return map[string]any{"start": q.Start, "end": q.End, "interval": q.Interval, "interval_seconds": q.Seconds, "filters": q.Filters, "retention_clamped": q.RetentionClamped}
}
func (q Query) where() (string, []any) {
	parts := []string{"e.occurred_at >= ?", "e.occurred_at < ?"}
	args := []any{q.Start, q.End}
	keys := make([]string, 0, len(q.Filters))
	for k := range q.Filters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		column := Dimensions[key]
		value := q.Filters[key]
		var bound any = value
		if key == "is_bot" {
			bound = value == "true"
		}
		parts = append(parts, "("+column+") = ?")
		args = append(args, bound)
	}
	return strings.Join(parts, " AND "), args
}
func (q Query) validateDimension(name string) error {
	if _, ok := Dimensions[name]; !ok {
		return badQuery(fmt.Sprintf("Unsupported dimension. Use one of: %s", strings.Join(DimensionNames(), ", ")))
	}
	return nil
}
