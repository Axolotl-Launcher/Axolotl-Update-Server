package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"time"

	"gorm.io/gorm"
)

type RelocateOptions struct {
	Start, End time.Time
	After      time.Time
	AfterID    int64
	BatchSize  int
	Pause      time.Duration
	DryRun     bool
}

type RelocateReport struct {
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Scanned  int64     `json:"scanned"`
	Resolved int64     `json:"resolved"`
	Updated  int64     `json:"updated"`
	Skipped  int64     `json:"skipped"`
	After    time.Time `json:"after"`
	AfterID  int64     `json:"after_id"`
	DryRun   bool      `json:"dry_run"`
	Complete bool      `json:"complete"`
}

type relocateRow struct {
	ID         int64     `json:"id"`
	OccurredAt time.Time `json:"occurred_at"`
	ClientIP   string    `json:"client_ip"`
	Country    *string   `json:"country"`
	Continent  *string   `json:"continent"`
	Region     *string   `json:"region"`
	City       *string   `json:"city"`
	Latitude   *float64  `json:"latitude"`
	Longitude  *float64  `json:"longitude"`
	ASN        *int64    `json:"asn"`
	ASNOrg     *string   `json:"asn_org"`
	HasGeo     bool      `json:"has_geo"`
	HasASN     bool      `json:"has_asn"`
}

// Relocate scans a fixed time window using the existing (occurred_at,id) index.
// Lookup happens outside a transaction; only a small update batch holds locks.
// No table/schema/partition maintenance runs, so serve may continue collecting.
func (s *Storage) Relocate(ctx context.Context, o RelocateOptions, progress func(RelocateReport) error) (RelocateReport, error) {
	r := RelocateReport{Start: o.Start.UTC(), End: o.End.UTC(), After: o.After.UTC(), AfterID: o.AfterID, DryRun: o.DryRun}
	if s.Geo == nil || (s.Geo.City == nil && s.Geo.ASN == nil && s.Geo.qqLookup == nil && s.Geo.apiURL == "") {
		return r, fmt.Errorf("configure ANALYTICS_QQWRY_DB, ANALYTICS_GEOIP_CITY_DB/ASN_DB or ANALYTICS_GEOIP_API_URL before relocate")
	}
	if !o.End.After(o.Start) || o.BatchSize < 1 || o.BatchSize > 1000 || o.Pause < 0 || o.AfterID < 0 ||
		(!o.After.IsZero() && (o.After.Before(o.Start) || !o.After.Before(o.End))) || (o.After.IsZero() && o.AfterID != 0) {
		return r, fmt.Errorf("invalid relocation time window, cursor, batch size (1..1000) or pause")
	}
	et, _ := s.tables()
	// The bounded cache reduces repeated local lookups without loading all IPs.
	cache := map[string]Event{}
	for {
		query := s.Store.DB.WithContext(ctx).Table(et).Where("occurred_at >= ? AND occurred_at < ? AND client_ip IS NOT NULL", r.Start, r.End)
		if !r.After.IsZero() {
			query = query.Where("(occurred_at, id) > (?, ?)", r.After, r.AfterID)
		}
		ipColumn := "client_ip"
		if s.Store.Dialect == "postgres" {
			ipColumn = "host(client_ip) AS client_ip"
		}
		rows := []relocateRow{}
		readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		e := query.WithContext(readCtx).Select("id, occurred_at, country, continent, region, city, latitude, longitude, " + ipColumn).Order("occurred_at, id").Limit(o.BatchSize).Find(&rows).Error
		cancel()
		if e != nil {
			return r, e
		}
		if len(rows) == 0 {
			r.Complete = true
			if progress != nil {
				return r, progress(r)
			}
			return r, nil
		}
		updates := make([]relocateRow, 0, len(rows))
		for _, row := range rows {
			if e := ctx.Err(); e != nil {
				return r, e
			}
			ip, e := netip.ParseAddr(row.ClientIP)
			if e != nil || !publicGeoIP(ip.Unmap()) {
				continue
			}
			key := ip.Unmap().String()
			loc, ok := cache[key]
			if !ok {
				loc = s.Geo.Recompute(ctx, ip)
				if len(cache) >= geoCacheLimit {
					clear(cache)
				}
				// Do not cache transient failure/rate-limit misses for the whole job.
				if hasGeoDetail(loc) || loc.ASN != nil {
					cache[key] = loc
				}
			}
			row.HasGeo, row.HasASN = hasGeo(loc), loc.ASN != nil
			if !row.HasGeo && !row.HasASN {
				continue
			}
			// A temporarily unavailable API must not erase more detailed data
			// already recorded for the same country.
			if !hasGeoDetail(loc) && loc.Country != nil && row.Country != nil && *loc.Country == *row.Country {
				loc.Region, loc.City = row.Region, row.City
				loc.Latitude, loc.Longitude = row.Latitude, row.Longitude
				if loc.Continent == nil {
					loc.Continent = row.Continent
				}
			}
			row.Country, row.Continent, row.Region, row.City = loc.Country, loc.Continent, loc.Region, loc.City
			row.Latitude, row.Longitude, row.ASN, row.ASNOrg = loc.Latitude, loc.Longitude, loc.ASN, loc.ASNOrg
			updates = append(updates, row)
		}
		var updated int64
		if !o.DryRun && len(updates) > 0 {
			writeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			updated, e = s.relocateBatch(writeCtx, et, updates, r.Start, r.End)
			cancel()
			if e != nil {
				return r, e
			}
		}
		r.Scanned += int64(len(rows))
		r.Resolved += int64(len(updates))
		r.Skipped += int64(len(rows) - len(updates))
		r.Updated += updated
		last := rows[len(rows)-1]
		r.After, r.AfterID = last.OccurredAt.UTC(), last.ID
		if progress != nil {
			if e = progress(r); e != nil {
				return r, e
			}
		}
		if o.Pause > 0 {
			timer := time.NewTimer(o.Pause)
			select {
			case <-ctx.Done():
				timer.Stop()
				return r, ctx.Err()
			case <-timer.C:
			}
		}
	}
}

func (s *Storage) relocateBatch(ctx context.Context, table string, rows []relocateRow, start, end time.Time) (int64, error) {
	var count int64
	err := s.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if s.Store.Dialect == "postgres" {
			if e := tx.Exec("SET LOCAL lock_timeout = '1s'").Error; e != nil {
				return e
			}
			if e := tx.Exec("SET LOCAL statement_timeout = '10s'").Error; e != nil {
				return e
			}
			data, e := json.Marshal(rows)
			if e != nil {
				return e
			}
			result := tx.Exec(`UPDATE `+table+` AS e SET
 country=CASE WHEN x.has_geo THEN x.country ELSE e.country END,
 continent=CASE WHEN x.has_geo THEN x.continent ELSE e.continent END,
 region=CASE WHEN x.has_geo THEN x.region ELSE e.region END,
 city=CASE WHEN x.has_geo THEN x.city ELSE e.city END,
 latitude=CASE WHEN x.has_geo THEN x.latitude ELSE e.latitude END,
 longitude=CASE WHEN x.has_geo THEN x.longitude ELSE e.longitude END,
 asn=CASE WHEN x.has_asn THEN x.asn ELSE e.asn END,
 asn_org=CASE WHEN x.has_asn THEN x.asn_org ELSE e.asn_org END
 FROM jsonb_to_recordset(?::jsonb) AS x(id bigint,occurred_at timestamptz,client_ip text,
 country text,continent text,region text,city text,latitude double precision,longitude double precision,
 asn bigint,asn_org text,has_geo boolean,has_asn boolean)
 WHERE e.occurred_at >= ? AND e.occurred_at < ? AND e.occurred_at=x.occurred_at
 AND e.id=x.id AND e.client_ip=x.client_ip::inet`, string(data), start, end)
			count = result.RowsAffected
			return result.Error
		}
		for _, row := range rows {
			fields := map[string]any{}
			if row.HasGeo {
				fields = map[string]any{"country": row.Country, "continent": row.Continent, "region": row.Region, "city": row.City, "latitude": row.Latitude, "longitude": row.Longitude}
			}
			if row.HasASN {
				fields["asn"], fields["asn_org"] = row.ASN, row.ASNOrg
			}
			result := tx.Table(table).Where("id = ? AND occurred_at = ? AND client_ip = ?", row.ID, row.OccurredAt, row.ClientIP).Updates(fields)
			if result.Error != nil {
				return result.Error
			}
			count += result.RowsAffected
		}
		return nil
	})
	return count, err
}
