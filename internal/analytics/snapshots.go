package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var SnapshotHours = []int{24, 168, 336, 72, 6, 1}

func mapValues(now time.Time, hours int) url.Values {
	return url.Values{"start": {now.Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339Nano)}, "end": {now.Format(time.RFC3339Nano)}}
}

type Snapshot struct {
	Query         Query                `json:"query"`
	GeneratedAt   time.Time            `json:"generated_at"`
	Overview      Overview             `json:"overview"`
	Timeseries    []TimePoint          `json:"timeseries"`
	Breakdowns    map[string]Breakdown `json:"breakdowns"`
	OldestRequest *time.Time           `json:"oldest_request"`
	NewestRequest *time.Time           `json:"newest_request"`
}

type snapshotRow struct {
	Key         string
	Spec        string
	Payload     *string
	GeneratedAt *time.Time
	RequestedAt time.Time
	AttemptedAt *time.Time
	Failure     *string
}

type Snapshots struct{ Store *store.Store }

func (s *Snapshots) table() string {
	if s.Store.Dialect == "postgres" {
		return "analytics.stats_snapshots"
	}
	return "analytics_stats_snapshots"
}

// SnapshotRange recognizes only unfiltered rolling presets. Exact historical
// ranges, custom intervals and filters retain the existing live SQL path.
func SnapshotRange(q Query, now time.Time) int {
	if len(q.Filters) > 0 || q.Cursor != nil || q.End.After(now) || now.Sub(q.End) > 2*time.Minute {
		return 0
	}
	for _, hours := range SnapshotHours {
		if hours == 336 && q.RetentionClamped && q.End.Sub(q.Start) > 334*time.Hour {
			return 336
		}
		delta := q.End.Sub(q.Start) - time.Duration(hours)*time.Hour
		if delta < 0 {
			delta = -delta
		}
		if delta < time.Minute {
			return hours
		}
	}
	return 0
}

func (s *Snapshots) Load(ctx context.Context, hours int) (*Snapshot, error) {
	var row snapshotRow
	err := s.Store.DB.WithContext(ctx).Table(s.table()).Where("key = ?", fmt.Sprint(hours)).Take(&row).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.Payload == nil {
		return nil, nil
	}
	var snap Snapshot
	if err = json.Unmarshal([]byte(*row.Payload), &snap); err != nil {
		return nil, err
	}
	// Never serve obsolete snapshots indefinitely when maintenance is failing.
	if time.Since(snap.GeneratedAt) > 3*time.Hour {
		return nil, nil
	}
	return &snap, nil
}

// Refresh is scheduled, never called by the HTTP handler. Each complete preset
// is committed atomically, so readers cannot mix different refresh generations.
func (s *Snapshots) Refresh(ctx context.Context, now time.Time) error {
	if s.Store.Dialect == "postgres" {
		conn, err := s.Store.SQL.Conn(ctx)
		if err != nil {
			return err
		}
		defer conn.Close()
		var locked bool
		if err = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(728314216)").Scan(&locked); err != nil {
			return err
		}
		if !locked {
			return nil
		}
		defer conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock(728314216)")
	}
	stats := &Statistics{Store: s.Store}
	var firstError error
	for _, hours := range SnapshotHours {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		cached, loadErr := s.Load(ctx, hours)
		if loadErr != nil {
			return loadErr
		}
		if cached != nil && now.Sub(cached.Query.End) < time.Hour {
			continue
		}
		buildCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		err := func() error {
			values := mapValues(now, hours)
			q, err := ParseQuery(values, now)
			if err != nil {
				return err
			}
			q.Limit = 100
			snap := Snapshot{Query: q, Breakdowns: map[string]Breakdown{}}
			for _, edge := range []struct {
				order string
				dst   **time.Time
			}{{"ASC", &snap.OldestRequest}, {"DESC", &snap.NewestRequest}} {
				et, _ := (&Storage{Store: s.Store}).tables()
				var rows []struct{ OccurredAt time.Time }
				if e := s.Store.DB.WithContext(buildCtx).Table(et).Select("occurred_at").Where("occurred_at >= ? AND occurred_at < ?", q.Start, q.End).Order("occurred_at " + edge.order).Limit(1).Find(&rows).Error; e != nil {
					return e
				}
				if len(rows) > 0 {
					t := rows[0].OccurredAt
					*edge.dst = &t
				}
			}
			snap.Overview, err = stats.Overview(buildCtx, q)
			if err != nil {
				return err
			}
			snap.Timeseries, err = stats.Timeseries(buildCtx, q)
			if err != nil {
				return err
			}
			dims := DimensionNames()
			for i := 0; i < len(dims); i += 8 {
				groups, e := stats.Breakdowns(buildCtx, q, dims[i:min(i+8, len(dims))])
				if e != nil {
					return e
				}
				for name, group := range groups {
					snap.Breakdowns[name] = group
				}
			}
			snap.GeneratedAt = time.Now().UTC()
			data, err := json.Marshal(snap)
			if err != nil {
				return err
			}
			payload := string(data)
			row := snapshotRow{Key: fmt.Sprint(hours), Spec: "hourly-v1", Payload: &payload, GeneratedAt: &snap.GeneratedAt, RequestedAt: now}
			return s.Store.DB.WithContext(buildCtx).Table(s.table()).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"payload", "generated_at", "requested_at", "spec"})}).Create(&row).Error
		}()
		cancel()
		if err != nil {
			if firstError == nil {
				firstError = err
			}
			slog.Warn("scheduled statistics refresh failed", "hours", hours, "error", err)
		}
	}
	return firstError
}

// Run starts immediately in the background, then refreshes hourly. A failed
// initial build retries after a minute; successful stored results survive restart.
func (s *Snapshots) Run(ctx context.Context) {
	delay := time.Duration(0)
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		started := time.Now()
		err := s.Refresh(ctx, started.UTC())
		delay = max(time.Minute, time.Hour-time.Since(started))
		if err != nil {
			delay = time.Minute
		}
	}
}
