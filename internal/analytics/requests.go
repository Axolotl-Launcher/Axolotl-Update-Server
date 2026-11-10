package analytics

import (
	"context"
	"strings"
	"time"
)

type Request struct {
	ID             int64     `json:"id"`
	OccurredAt     time.Time `json:"occurred_at"`
	ClientIP       *string   `json:"client_ip"`
	UserAgent      string    `json:"user_agent"`
	Route          string    `json:"route"`
	Channel        *string   `json:"channel"`
	ClientVersion  *string   `json:"client_version"`
	Platform       *string   `json:"platform"`
	TargetVersion  *string   `json:"target_version"`
	IncludeUpdater *bool     `json:"include_updater"`
	IncludeRevoked *bool     `json:"include_revoked"`
	Method         *string   `json:"method"`
	StatusCode     *int      `json:"status_code"`
	DurationUS     *int64    `json:"-"`
	DurationMS     *float64  `json:"duration_ms" gorm:"-"`
	RequestBytes   *int64    `json:"request_bytes"`
	ResponseBytes  *int64    `json:"response_bytes"`
	Host           *string   `json:"host"`
	Scheme         *string   `json:"scheme"`
	HTTPVersion    *string   `json:"http_version"`
	TLSVersion     *string   `json:"tls_version"`
	ContentType    *string   `json:"content_type"`
	RefererHost    *string   `json:"referer_host"`
	AppCache       *string   `json:"app_cache"`
	ErrorCode      *string   `json:"error_code"`
	IPVersion      *int      `json:"ip_version"`
	Country        *string   `json:"country"`
	Continent      *string   `json:"continent"`
	Region         *string   `json:"region"`
	City           *string   `json:"city"`
	Latitude       *float64  `json:"latitude"`
	Longitude      *float64  `json:"longitude"`
	ASN            *int64    `json:"asn"`
	ASNOrg         *string   `json:"asn_org"`
	Browser        *string   `json:"browser"`
	BrowserVersion *string   `json:"browser_version"`
	OS             *string   `json:"os"`
	OSVersion      *string   `json:"os_version"`
	DeviceType     *string   `json:"device_type"`
	IsBot          *bool     `json:"is_bot"`
}
type RequestPage struct {
	Items      []Request `json:"items"`
	NextCursor *string   `json:"next_cursor"`
	HasMore    bool      `json:"has_more"`
	Limit      int       `json:"limit"`
}

const requestColumns = `e.id,e.occurred_at,e.route,e.channel,e.client_version,e.platform,e.target_version,e.include_updater,e.include_revoked,
 e.method,e.status_code,e.duration_us,e.request_bytes,e.response_bytes,e.host,e.scheme,e.http_version,e.tls_version,
 e.content_type,e.referer_host,e.app_cache,e.error_code,e.ip_version,e.country,e.continent,e.region,e.city,
 e.latitude,e.longitude,e.asn,e.asn_org,u.user_agent,u.browser,u.browser_version,u.os,u.os_version,u.device_type,u.is_bot,`

func (s *Statistics) Requests(ctx context.Context, q Query) (RequestPage, error) {
	out := RequestPage{Items: []Request{}, Limit: q.Limit}
	where, args := q.where()
	if q.Cursor != nil {
		where += " AND (e.occurred_at < ? OR (e.occurred_at = ? AND e.id < ?))"
		args = append(args, q.Cursor.At, q.Cursor.At, q.Cursor.ID)
	}
	ip := "e.client_ip"
	if s.Store.Dialect == "postgres" {
		ip = "host(e.client_ip)"
	}
	columns := requestColumns
	for _, name := range []string{"browser", "browser_version", "os", "device_type", "referer_host", "content_type", "error_code"} {
		raw := "e." + name
		if name == "browser" || name == "browser_version" || name == "os" || name == "device_type" {
			raw = "u." + name
		}
		columns = strings.Replace(columns, raw+",", Dimensions[name]+" AS "+name+",", 1)
	}
	sql := "SELECT " + columns + ip + " AS client_ip" + s.from() + " WHERE " + where + " ORDER BY e.occurred_at DESC,e.id DESC LIMIT ?"
	args = append(args, q.Limit+1)
	if e := s.Store.DB.WithContext(ctx).Raw(sql, args...).Scan(&out.Items).Error; e != nil {
		return out, e
	}
	if len(out.Items) > q.Limit {
		out.HasMore = true
		out.Items = out.Items[:q.Limit]
		last := out.Items[len(out.Items)-1]
		cursor := q.nextCursor(last.OccurredAt, last.ID)
		out.NextCursor = &cursor
	}
	for i := range out.Items {
		r := &out.Items[i]
		r.OccurredAt = r.OccurredAt.UTC()
		if r.DurationUS != nil {
			v := float64(*r.DurationUS) / 1000
			r.DurationMS = &v
		}
	}
	return out, nil
}
func (s *Statistics) RetentionStatus(ctx context.Context, now time.Time) (map[string]any, error) {
	q := Query{Start: now.UTC().Add(-14 * 24 * time.Hour), End: now.UTC(), Filters: map[string]string{}}
	where, args := q.where()
	var counts struct{ Requests, Enriched int64 }
	if e := s.Store.DB.WithContext(ctx).Raw("SELECT COUNT(*) AS requests,COUNT(e.status_code) AS enriched"+s.from()+" WHERE "+where, args...).Scan(&counts).Error; e != nil {
		return nil, e
	}
	times := map[string]*time.Time{"oldest": nil, "newest": nil}
	for name, order := range map[string]string{"oldest": "ASC", "newest": "DESC"} {
		var rows []struct{ OccurredAt time.Time }
		if e := s.Store.DB.WithContext(ctx).Raw("SELECT e.occurred_at"+s.from()+" WHERE "+where+" ORDER BY e.occurred_at "+order+" LIMIT 1", args...).Scan(&rows).Error; e != nil {
			return nil, e
		}
		if len(rows) > 0 {
			t := rows[0].OccurredAt.UTC()
			times[name] = &t
		}
	}
	return map[string]any{"retention_days": 14, "requests": counts.Requests, "requests_with_http_metrics": counts.Enriched, "oldest_request": times["oldest"], "newest_request": times["newest"]}, nil
}
