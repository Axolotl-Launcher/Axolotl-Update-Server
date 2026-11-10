package analytics

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type geoRateState struct {
	ID           int
	Calls        string
	BlockedUntil *time.Time
}

// BindRateStore shares one sliding-window quota between serve, relocation and
// other processes connected to this database. Any limiter failure denies calls.
func (g *GeoIP) BindRateStore(s *store.Store) {
	g.rateStore = s
}

func (g *GeoIP) sharedRate(ctx context.Context, blockUntil *time.Time) bool {
	s := g.rateStore
	if s == nil {
		return true
	}
	table := "analytics_geoip_api_limiter"
	if s.Dialect == "postgres" {
		table = "analytics.geoip_api_limiter"
	}
	allowed := false
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Table(table).Where("id = 1")
		if s.Dialect == "postgres" {
			if e := tx.Exec("SET LOCAL lock_timeout = '500ms'").Error; e != nil {
				return e
			}
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var row geoRateState
		if e := query.Take(&row).Error; e != nil {
			return e
		}
		if blockUntil != nil {
			if row.BlockedUntil == nil || blockUntil.After(*row.BlockedUntil) {
				return tx.Table(table).Where("id = 1").Update("blocked_until", blockUntil).Error
			}
			return nil
		}
		now := time.Now().UTC()
		if row.BlockedUntil != nil && now.Before(*row.BlockedUntil) {
			return nil
		}
		var calls []time.Time
		if e := json.Unmarshal([]byte(row.Calls), &calls); e != nil {
			return e
		}
		valid := make([]time.Time, 0, 40)
		for _, at := range calls {
			if at.After(now.Add(-time.Minute)) {
				valid = append(valid, at)
			}
		}
		if len(valid) >= 40 {
			return nil
		}
		valid = append(valid, now)
		data, e := json.Marshal(valid)
		if e != nil {
			return e
		}
		if e = tx.Table(table).Where("id = 1").Update("calls", string(data)).Error; e != nil {
			return e
		}
		allowed = true
		return nil
	})
	return err == nil && allowed
}
