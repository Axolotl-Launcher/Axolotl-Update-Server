package analytics

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/store"
)

type Statistics struct{ Store *store.Store }
type Metrics struct {
	Requests            int64    `json:"requests"`
	UniqueIPs           int64    `json:"unique_ips"`
	UniqueUserAgents    int64    `json:"unique_user_agents"`
	StatusSamples       int64    `json:"status_samples"`
	Informational       int64    `json:"status_1xx"`
	Successful          int64    `json:"status_2xx"`
	Redirects           int64    `json:"status_3xx"`
	ClientErrors        int64    `json:"status_4xx"`
	ServerErrors        int64    `json:"status_5xx"`
	RateLimited         int64    `json:"rate_limited_requests"`
	AccessDenied        int64    `json:"access_denied_requests"`
	ResponseBytes       int64    `json:"response_bytes"`
	RequestBytes        int64    `json:"request_bytes"`
	ResponseByteSamples int64    `json:"response_byte_samples"`
	RequestByteSamples  int64    `json:"request_byte_samples"`
	DurationSamples     int64    `json:"duration_samples"`
	AverageDurationUS   *float64 `json:"-"`
	MaxDurationUS       *float64 `json:"-"`
	AverageDurationMS   *float64 `json:"avg_duration_ms" gorm:"-"`
	MaxDurationMS       *float64 `json:"max_duration_ms" gorm:"-"`
	BotSamples          int64    `json:"bot_samples"`
	BotRequests         int64    `json:"bot_requests"`
	CountrySamples      int64    `json:"country_samples"`
	CacheHits           int64    `json:"app_cache_hits"`
	CacheMisses         int64    `json:"app_cache_misses"`
	CacheBypass         int64    `json:"app_cache_bypass"`
	ErrorRate           *float64 `json:"error_rate" gorm:"-"`
	CacheHitRate        *float64 `json:"app_cache_hit_rate" gorm:"-"`
	RequestsPerSecond   float64  `json:"requests_per_second" gorm:"-"`
}
type Percentiles struct {
	P50 *float64 `json:"p50_ms"`
	P90 *float64 `json:"p90_ms"`
	P95 *float64 `json:"p95_ms"`
	P99 *float64 `json:"p99_ms"`
}
type Overview struct {
	Metrics
	Latency Percentiles `json:"latency"`
}
type TimePoint struct {
	BucketUnix int64     `json:"-"`
	Time       time.Time `json:"time"`
	Metrics
}
type BreakdownItem struct {
	Value *string `json:"value"`
	Metrics
}
type Breakdown struct {
	Dimension     string          `json:"dimension"`
	Items         []BreakdownItem `json:"items"`
	TotalRequests int64           `json:"total_requests"`
	OtherRequests int64           `json:"other_requests"`
	Limit         int             `json:"limit"`
}

func (s *Statistics) from() string {
	if s.Store.Dialect == "postgres" {
		return " FROM analytics.request_events e JOIN analytics.user_agents u ON u.id=e.user_agent_id "
	}
	return " FROM analytics_request_events e JOIN analytics_user_agents u ON u.id=e.user_agent_id "
}

const aggregateSQL = `COUNT(*) AS requests,COUNT(DISTINCT e.client_ip) AS unique_ips,COUNT(DISTINCT e.user_agent_id) AS unique_user_agents,
 COUNT(e.status_code) AS status_samples,
 COALESCE(SUM(CASE WHEN e.status_code BETWEEN 100 AND 199 THEN 1 ELSE 0 END),0) AS informational,
 COALESCE(SUM(CASE WHEN e.status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END),0) AS successful,
 COALESCE(SUM(CASE WHEN e.status_code BETWEEN 300 AND 399 THEN 1 ELSE 0 END),0) AS redirects,
 COALESCE(SUM(CASE WHEN e.status_code BETWEEN 400 AND 499 THEN 1 ELSE 0 END),0) AS client_errors,
 COALESCE(SUM(CASE WHEN e.status_code BETWEEN 500 AND 599 THEN 1 ELSE 0 END),0) AS server_errors,
 COALESCE(SUM(CASE WHEN e.status_code=429 THEN 1 ELSE 0 END),0) AS rate_limited,
 COALESCE(SUM(CASE WHEN e.status_code IN (401,403) THEN 1 ELSE 0 END),0) AS access_denied,
 COALESCE(SUM(e.response_bytes),0) AS response_bytes,COALESCE(SUM(e.request_bytes),0) AS request_bytes,
 COUNT(e.response_bytes) AS response_byte_samples,COUNT(e.request_bytes) AS request_byte_samples,
 COUNT(e.duration_us) AS duration_samples,AVG(e.duration_us) AS average_duration_us,MAX(e.duration_us) AS max_duration_us,
 COUNT(u.is_bot) AS bot_samples,COALESCE(SUM(CASE WHEN u.is_bot THEN 1 ELSE 0 END),0) AS bot_requests,
 COUNT(e.country) AS country_samples,
 COALESCE(SUM(CASE WHEN e.app_cache='hit' THEN 1 ELSE 0 END),0) AS cache_hits,
 COALESCE(SUM(CASE WHEN e.app_cache='miss' THEN 1 ELSE 0 END),0) AS cache_misses,
 COALESCE(SUM(CASE WHEN e.app_cache='bypass' THEN 1 ELSE 0 END),0) AS cache_bypass`

func ratio(n, d int64) *float64 {
	if d == 0 {
		return nil
	}
	v := float64(n) / float64(d)
	return &v
}
func (m *Metrics) finish(seconds float64) {
	if m.AverageDurationUS != nil {
		v := *m.AverageDurationUS / 1000
		m.AverageDurationMS = &v
	}
	if m.MaxDurationUS != nil {
		v := *m.MaxDurationUS / 1000
		m.MaxDurationMS = &v
	}
	m.ErrorRate = ratio(m.ClientErrors+m.ServerErrors, m.StatusSamples)
	m.CacheHitRate = ratio(m.CacheHits, m.CacheHits+m.CacheMisses)
	if seconds > 0 {
		m.RequestsPerSecond = float64(m.Requests) / seconds
	}
}
func (s *Statistics) Overview(ctx context.Context, q Query) (Overview, error) {
	var out Overview
	where, args := q.where()
	if s.Store.Dialect == "postgres" {
		var row struct {
			Metrics
			P50, P90, P95, P99 *float64
		}
		percentiles := `,percentile_cont(0.5) WITHIN GROUP(ORDER BY e.duration_us)/1000.0 AS p50,percentile_cont(0.9) WITHIN GROUP(ORDER BY e.duration_us)/1000.0 AS p90,percentile_cont(0.95) WITHIN GROUP(ORDER BY e.duration_us)/1000.0 AS p95,percentile_cont(0.99) WITHIN GROUP(ORDER BY e.duration_us)/1000.0 AS p99`
		e := s.Store.DB.WithContext(ctx).Raw("SELECT "+aggregateSQL+percentiles+s.from()+" WHERE "+where, args...).Scan(&row).Error
		out.Metrics = row.Metrics
		out.Metrics.finish(q.End.Sub(q.Start).Seconds())
		out.Latency = Percentiles{row.P50, row.P90, row.P95, row.P99}
		return out, e
	}
	sql := "SELECT " + aggregateSQL + s.from() + " WHERE " + where
	if e := s.Store.DB.WithContext(ctx).Raw(sql, args...).Scan(&out.Metrics).Error; e != nil {
		return out, e
	}
	out.Metrics.finish(q.End.Sub(q.Start).Seconds())
	if out.DurationSamples == 0 {
		return out, nil
	}
	// SQLite debug path computes the same continuous percentiles in SQL.
	columns := []string{}
	for _, v := range []struct {
		p    float64
		name string
	}{{0.5, "p50"}, {0.9, "p90"}, {0.95, "p95"}, {0.99, "p99"}} {
		position := fmt.Sprintf("(1+(n-1)*%g)", v.p)
		floor := "CAST(" + position + " AS INTEGER)"
		fraction := "(" + position + "-" + floor + ")"
		columns = append(columns, "SUM(CASE WHEN rn="+floor+" THEN duration_us*(1-"+fraction+") WHEN rn="+floor+"+1 THEN duration_us*"+fraction+" ELSE 0 END)/1000.0 AS "+v.name)
	}
	sql = "WITH ranked AS (SELECT e.duration_us,ROW_NUMBER() OVER(ORDER BY e.duration_us) AS rn,COUNT(*) OVER() AS n" + s.from() + " WHERE " + where + " AND e.duration_us IS NOT NULL) SELECT " + join(columns, ",") + " FROM ranked"
	e := s.Store.DB.WithContext(ctx).Raw(sql, args...).Scan(&out.Latency).Error
	return out, e
}
func join(values []string, sep string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += sep
		}
		out += v
	}
	return out
}
func (s *Statistics) Timeseries(ctx context.Context, q Query) ([]TimePoint, error) {
	where, args := q.where()
	bucket := fmt.Sprintf("CAST(strftime('%%s',e.occurred_at) AS INTEGER)/%d*%d", q.Seconds, q.Seconds)
	if s.Store.Dialect == "postgres" {
		bucket = fmt.Sprintf("CAST(FLOOR(EXTRACT(EPOCH FROM e.occurred_at)/%d)*%d AS BIGINT)", q.Seconds, q.Seconds)
	}
	var rows []TimePoint
	sql := "SELECT " + bucket + " AS bucket_unix," + aggregateSQL + s.from() + " WHERE " + where + " GROUP BY " + bucket + " ORDER BY bucket_unix"
	if s.Store.Dialect == "postgres" {
		sql = "WITH partial AS (SELECT " + bucket + " AS bucket_unix,e.client_ip,e.user_agent_id," + partialAggregateSQL() + s.from() + " WHERE " + where + " GROUP BY " + bucket + ",e.client_ip,e.user_agent_id) SELECT p.bucket_unix," + weightedAggregateSQL() + " FROM partial p GROUP BY p.bucket_unix ORDER BY p.bucket_unix"
	}
	if e := s.Store.DB.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; e != nil {
		return nil, e
	}
	byTime := map[int64]Metrics{}
	for _, row := range rows {
		byTime[row.BucketUnix] = row.Metrics
	}
	out := []TimePoint{}
	for at := q.Start.Unix() / q.Seconds * q.Seconds; at < q.End.Unix() || (at == q.End.Unix() && q.End.Nanosecond() > 0); at += q.Seconds {
		m := byTime[at]
		start := math.Max(float64(at), float64(q.Start.UnixNano())/1e9)
		end := math.Min(float64(at+q.Seconds), float64(q.End.UnixNano())/1e9)
		m.finish(end - start)
		out = append(out, TimePoint{BucketUnix: at, Time: time.Unix(at, 0).UTC(), Metrics: m})
	}
	return out, nil
}
func (s *Statistics) dimension(name string) string {
	column := Dimensions[name]
	if name == "client_ip" && s.Store.Dialect == "postgres" {
		column = "host(e.client_ip)"
	}
	if name == "is_bot" || name == "include_updater" || name == "include_revoked" {
		return "CASE WHEN " + column + " IS NULL THEN NULL WHEN " + column + " THEN 'true' ELSE 'false' END"
	}
	return "CAST((" + column + ") AS TEXT)"
}
func (s *Statistics) Breakdown(ctx context.Context, q Query, dimension string) (Breakdown, error) {
	out := Breakdown{Dimension: dimension, Items: []BreakdownItem{}, Limit: q.Limit}
	if e := q.validateDimension(dimension); e != nil {
		return out, e
	}
	if s.Store.Dialect == "postgres" {
		groups, e := s.Breakdowns(ctx, q, []string{dimension})
		if e != nil {
			return out, e
		}
		return groups[dimension], nil
	}
	where, args := q.where()
	column := s.dimension(dimension)
	sql := "SELECT " + column + " AS value," + aggregateSQL + ",SUM(COUNT(*)) OVER() AS total_requests" + s.from() + " WHERE " + where + " GROUP BY " + column + " ORDER BY requests DESC," + column + " ASC NULLS LAST LIMIT ?"
	args = append(args, q.Limit)
	var rows []struct {
		BreakdownItem
		TotalRequests int64
	}
	if e := s.Store.DB.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; e != nil {
		return out, e
	}
	for _, row := range rows {
		out.Items = append(out.Items, row.BreakdownItem)
		out.TotalRequests = row.TotalRequests
	}
	returned := int64(0)
	for i := range out.Items {
		out.Items[i].Metrics.finish(q.End.Sub(q.Start).Seconds())
		returned += out.Items[i].Requests
	}
	out.OtherRequests = out.TotalRequests - returned
	return out, nil
}

// PostgreSQL computes all dashboard distributions with a single scan and GROUPING SETS.
func (s *Statistics) Breakdowns(ctx context.Context, q Query, dimensions []string) (map[string]Breakdown, error) {
	out := map[string]Breakdown{}
	unique := []string{}
	for _, dim := range dimensions {
		if e := q.validateDimension(dim); e != nil {
			return nil, e
		}
		if _, ok := out[dim]; ok {
			continue
		}
		unique = append(unique, dim)
		out[dim] = Breakdown{Dimension: dim, Items: []BreakdownItem{}, Limit: q.Limit}
	}
	if len(unique) == 0 {
		return out, nil
	}
	if s.Store.Dialect != "postgres" {
		for _, dim := range unique {
			group, e := s.Breakdown(ctx, q, dim)
			if e != nil {
				return nil, e
			}
			out[dim] = group
		}
		return out, nil
	}
	names, values, sets, selected, groupBy := []string{}, []string{}, []string{}, []string{}, []string{}
	for i, dim := range unique {
		expression := s.dimension(dim)
		alias := fmt.Sprintf("d%d", i)
		col := "p." + alias
		selected = append(selected, expression+" AS "+alias)
		groupBy = append(groupBy, expression)
		names = append(names, "WHEN GROUPING("+col+")=0 THEN '"+dim+"'")
		values = append(values, "WHEN GROUPING("+col+")=0 THEN "+col)
		sets = append(sets, "("+col+")")
	}
	where, args := q.where()
	sql := "WITH partial AS (SELECT " + join(selected, ",") + ",e.client_ip,e.user_agent_id," + partialAggregateSQL() + s.from() + " WHERE " + where + " GROUP BY " + join(groupBy, ",") + ",e.client_ip,e.user_agent_id),grouped AS (SELECT CASE " + join(names, " ") + " END AS dimension,CASE " + join(values, " ") + " END AS value," + weightedAggregateSQL() + " FROM partial p GROUP BY GROUPING SETS (" + join(sets, ",") + ")),ranked AS (SELECT *,ROW_NUMBER() OVER(PARTITION BY dimension ORDER BY requests DESC,value ASC NULLS LAST) AS rank,SUM(requests) OVER(PARTITION BY dimension) AS total_requests FROM grouped) SELECT * FROM ranked WHERE rank<=? ORDER BY dimension,rank"
	args = append(args, q.Limit)
	var rows []struct {
		Dimension string
		BreakdownItem
		TotalRequests int64
	}
	if e := s.Store.DB.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; e != nil {
		return nil, e
	}
	for _, row := range rows {
		group := out[row.Dimension]
		item := row.BreakdownItem
		item.Metrics.finish(q.End.Sub(q.Start).Seconds())
		group.Items = append(group.Items, item)
		group.TotalRequests = row.TotalRequests
		out[row.Dimension] = group
	}
	for key, group := range out {
		shown := int64(0)
		for _, item := range group.Items {
			shown += item.Requests
		}
		group.OtherRequests = group.TotalRequests - shown
		out[key] = group
	}
	return out, nil
}

// Collapse repeated client/dimension combinations before DISTINCT aggregation.
// Counts remain exact; latency averages use sums/sample counts, never averages of averages.
func partialAggregateSQL() string {
	sql := strings.Replace(aggregateSQL, "COUNT(DISTINCT e.client_ip) AS unique_ips,COUNT(DISTINCT e.user_agent_id) AS unique_user_agents,", "", 1)
	return strings.Replace(sql, "AVG(e.duration_us) AS average_duration_us", "SUM(e.duration_us) AS duration_sum_us", 1)
}
func weightedAggregateSQL() string {
	columns := []string{"requests", "status_samples", "informational", "successful", "redirects", "client_errors", "server_errors", "rate_limited", "access_denied", "response_bytes", "request_bytes", "response_byte_samples", "request_byte_samples", "duration_samples", "bot_samples", "bot_requests", "country_samples", "cache_hits", "cache_misses", "cache_bypass"}
	parts := []string{"COUNT(DISTINCT p.client_ip) AS unique_ips", "COUNT(DISTINCT p.user_agent_id) AS unique_user_agents", "SUM(p.duration_sum_us)*1.0/NULLIF(SUM(p.duration_samples),0) AS average_duration_us", "MAX(p.max_duration_us) AS max_duration_us"}
	for _, col := range columns {
		parts = append(parts, "SUM(p."+col+") AS "+col)
	}
	return join(parts, ",")
}
