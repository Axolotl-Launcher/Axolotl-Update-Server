package analytics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Event struct {
	OccurredAt                                                                                   time.Time
	ClientIP                                                                                     *string
	UserAgent                                                                                    string `gorm:"-"`
	UserAgentID                                                                                  int64
	Route                                                                                        string
	Channel, ClientVersion, Platform, TargetVersion                                              *string
	IncludeUpdater, IncludeRevoked                                                               *bool
	Method, Host, Scheme, HTTPVersion, TLSVersion, ContentType, RefererHost, AppCache, ErrorCode *string
	StatusCode, IPVersion                                                                        *int
	DurationUS, RequestBytes, ResponseBytes                                                      *int64
	Country, Continent, Region, City, ASNOrg                                                     *string
	Latitude, Longitude                                                                          *float64
	ASN                                                                                          *int64
}
type userAgent struct {
	ID                                                 int64
	SHA256                                             string `gorm:"column:sha256"`
	UserAgent                                          string
	Browser, BrowserVersion, OS, OSVersion, DeviceType *string
	IsBot                                              *bool
	ClassificationVersion                              int
}
type Storage struct {
	Store *store.Store
	once  sync.Once
	gate  chan struct{}
	Geo   *GeoIP
}

func (s *Storage) lock(ctx context.Context) error {
	s.once.Do(func() { s.gate = make(chan struct{}, 1) })
	select {
	case s.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Storage) unlock() { <-s.gate }

func (s *Storage) tables() (string, string) {
	if s.Store.Dialect == "postgres" {
		return "analytics.request_events", "analytics.user_agents"
	}
	return "analytics_request_events", "analytics_user_agents"
}

func (s *Storage) Write(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	if e := s.lock(ctx); e != nil {
		return e
	}
	defer s.unlock()
	et, ut := s.tables()
	return s.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		unique := map[string]string{}
		hashes := []string{}
		for i := range events {
			events[i].OccurredAt = events[i].OccurredAt.UTC()
			events[i].UserAgent = strings.ToValidUTF8(events[i].UserAgent, "\uFFFD")
			events[i].UserAgent = strings.ReplaceAll(events[i].UserAgent, "\x00", "\uFFFD")
			if !utf8.ValidString(events[i].UserAgent) {
				return fmt.Errorf("invalid user agent")
			}
			hash := sha256.Sum256([]byte(events[i].UserAgent))
			key := hex.EncodeToString(hash[:])
			if old, ok := unique[key]; ok && old != events[i].UserAgent {
				return fmt.Errorf("user agent hash collision")
			}
			if _, ok := unique[key]; !ok {
				hashes = append(hashes, key)
			}
			unique[key] = events[i].UserAgent
			if events[i].ClientIP != nil {
				a, e := netip.ParseAddr(*events[i].ClientIP)
				if e != nil {
					events[i].ClientIP = nil
				} else {
					normalized := a.Unmap().String()
					events[i].ClientIP = &normalized
					version := 6
					if a.Unmap().Is4() {
						version = 4
					}
					events[i].IPVersion = &version
					if s.Geo != nil {
						s.Geo.Enrich(&events[i], a.Unmap())
					}
				}
			}
		}
		// Stable insert order prevents dictionary deadlocks if maintenance overlaps.
		sort.Strings(hashes)
		rows := []userAgent{}
		for _, hash := range hashes {
			row := classifyUA(unique[hash])
			row.SHA256 = hash
			rows = append(rows, row)
		}
		if e := tx.Table(ut).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "sha256"}}, DoNothing: true}).CreateInBatches(&rows, 100).Error; e != nil {
			return e
		}
		stored := []userAgent{}
		if e := tx.Table(ut).Where("sha256 IN ?", hashes).Find(&stored).Error; e != nil {
			return e
		}
		ids := map[string]int64{}
		for _, row := range stored {
			if unique[row.SHA256] != row.UserAgent {
				return fmt.Errorf("user agent hash collision")
			}
			ids[row.SHA256] = row.ID
			if row.Browser == nil || row.ClassificationVersion < uaClassificationVersion {
				parsed := classifyUA(row.UserAgent)
				if e := tx.Table(ut).Where("id = ?", row.ID).Updates(classificationFields(parsed)).Error; e != nil {
					return e
				}
			}
		}
		for i := range events {
			hash := sha256.Sum256([]byte(events[i].UserAgent))
			events[i].UserAgentID = ids[hex.EncodeToString(hash[:])]
			if events[i].UserAgentID == 0 {
				return fmt.Errorf("missing user agent dictionary entry")
			}
		}
		// Keep the expanded event below SQLite's conservative parameter limit.
		return tx.Table(et).CreateInBatches(&events, 20).Error
	})
}

type MaintenanceReport struct {
	CreatePartitions, DropPartitions []string
	Cutoff                           time.Time
	DryRun                           bool
	DeletedEvents, DeletedUserAgents int64
}

func (s *Storage) Maintain(ctx context.Context, now time.Time, dryRun bool) (MaintenanceReport, error) {
	now = now.UTC()
	cutoff := now.Add(-14 * 24 * time.Hour)
	report := MaintenanceReport{CreatePartitions: []string{}, DropPartitions: []string{}, Cutoff: cutoff, DryRun: dryRun}
	if e := s.lock(ctx); e != nil {
		return report, e
	}
	defer s.unlock()
	et, ut := s.tables()
	if s.Store.Dialect != "postgres" {
		if dryRun {
			if e := s.Store.DB.WithContext(ctx).Table(et).Where("occurred_at < ?", cutoff).Count(&report.DeletedEvents).Error; e != nil {
				return report, e
			}
			return report, nil
		}
		e := s.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			r := tx.Exec("DELETE FROM "+et+" WHERE occurred_at < ?", cutoff)
			report.DeletedEvents = r.RowsAffected
			if r.Error != nil {
				return r.Error
			}
			r = tx.Exec("DELETE FROM " + ut + " WHERE NOT EXISTS (SELECT 1 FROM " + et + " e WHERE e.user_agent_id = " + ut + ".id)")
			report.DeletedUserAgents = r.RowsAffected
			return r.Error
		})
		return report, e
	}
	var existing []string
	if e := s.Store.DB.WithContext(ctx).Raw(`SELECT child.relname FROM pg_inherits i JOIN pg_class parent ON parent.oid=i.inhparent JOIN pg_namespace n ON n.oid=parent.relnamespace JOIN pg_class child ON child.oid=i.inhrelid WHERE n.nspname='analytics' AND parent.relname='request_events'`).Scan(&existing).Error; e != nil {
		return report, e
	}
	known := map[string]bool{}
	for _, name := range existing {
		known[name] = true
		if strings.HasPrefix(name, "request_events_") {
			day, e := time.Parse("20060102", strings.TrimPrefix(name, "request_events_"))
			if e == nil && !day.Add(24*time.Hour).After(cutoff) {
				report.DropPartitions = append(report.DropPartitions, name)
			}
		}
	}
	sort.Strings(report.DropPartitions)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for day := today.AddDate(0, 0, -14); !day.After(today.AddDate(0, 0, 2)); day = day.AddDate(0, 0, 1) {
		if !day.Add(24 * time.Hour).After(cutoff) {
			continue
		}
		name := "request_events_" + day.Format("20060102")
		if !known[name] {
			report.CreatePartitions = append(report.CreatePartitions, name)
		}
	}
	if dryRun {
		return report, nil
	}
	e := s.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Exec("SET LOCAL lock_timeout = '2s'").Error; e != nil {
			return e
		}
		for _, name := range report.CreatePartitions {
			day, _ := time.Parse("20060102", strings.TrimPrefix(name, "request_events_"))
			q := fmt.Sprintf("CREATE TABLE analytics.%s PARTITION OF analytics.request_events FOR VALUES FROM ('%s') TO ('%s')", name, day.Format(time.RFC3339), day.Add(24*time.Hour).Format(time.RFC3339))
			if e := tx.Exec(q).Error; e != nil {
				return e
			}
		}
		for _, name := range report.DropPartitions {
			if e := tx.Exec("DROP TABLE analytics." + name).Error; e != nil {
				return e
			}
		}
		r := tx.Exec("DELETE FROM " + ut + " WHERE NOT EXISTS (SELECT 1 FROM " + et + " e WHERE e.user_agent_id = " + ut + ".id)")
		report.DeletedUserAgents = r.RowsAffected
		return r.Error
	})
	return report, e
}
