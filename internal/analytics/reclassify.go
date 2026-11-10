package analytics

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

func classificationFields(u userAgent) map[string]any {
	return map[string]any{"browser": u.Browser, "browser_version": u.BrowserVersion, "os": u.OS, "os_version": u.OSVersion, "device_type": u.DeviceType, "is_bot": u.IsBot, "classification_version": uaClassificationVersion}
}

type ReclassifyReport struct {
	Scanned  int64 `json:"scanned"`
	Updated  int64 `json:"updated"`
	AfterID  int64 `json:"after_id"`
	Complete bool  `json:"complete"`
	DryRun   bool  `json:"dry_run"`
}

// Reclassify updates the deduplicated UA dictionary. Each row immediately fixes
// every historical event referencing it, without rewriting millions of events.
func (s *Storage) Reclassify(ctx context.Context, batchSize int, afterID int64, dryRun bool, progress func(ReclassifyReport) error) (ReclassifyReport, error) {
	r := ReclassifyReport{AfterID: afterID, DryRun: dryRun}
	if batchSize < 1 || batchSize > 1000 || afterID < 0 {
		return r, fmt.Errorf("invalid batch size (1..1000) or cursor")
	}
	_, table := s.tables()
	for {
		rows := []userAgent{}
		if e := s.Store.DB.WithContext(ctx).Table(table).Where("id > ? AND classification_version < ?", r.AfterID, uaClassificationVersion).Order("id").Limit(batchSize).Find(&rows).Error; e != nil {
			return r, e
		}
		if len(rows) == 0 {
			r.Complete = true
			if progress != nil {
				return r, progress(r)
			}
			return r, nil
		}
		var updated int64
		if !dryRun {
			batchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			e := s.Store.DB.WithContext(batchCtx).Transaction(func(tx *gorm.DB) error {
				if s.Store.Dialect == "postgres" {
					if e := tx.Exec("SET LOCAL lock_timeout = '1s'").Error; e != nil {
						return e
					}
				}
				for _, row := range rows {
					result := tx.Table(table).Where("id = ? AND classification_version < ?", row.ID, uaClassificationVersion).Updates(classificationFields(classifyUA(row.UserAgent)))
					if result.Error != nil {
						return result.Error
					}
					updated += result.RowsAffected
				}
				return nil
			})
			cancel()
			if e != nil {
				return r, e
			}
		}
		r.Scanned += int64(len(rows))
		r.Updated += updated
		r.AfterID = rows[len(rows)-1].ID
		if progress != nil {
			if e := progress(r); e != nil {
				return r, e
			}
		}
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
