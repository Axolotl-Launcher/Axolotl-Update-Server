package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/domain"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
	"gorm.io/gorm"
)

type Snapshot struct {
	Versions  []model.Version        `json:"versions"`
	Artifacts []model.Artifact       `json:"artifacts"`
	Pointers  []model.ChannelPointer `json:"pointers"`
	Events    []model.WebhookEvent   `json:"events"`
	Audits    []model.AuditLog       `json:"audits"`
}
type ImportReport struct {
	Revision                            string `json:"revision"`
	Versions, Artifacts, Events, Audits int
	Warnings                            []string `json:"warnings"`
}

func ReadLegacy(ctx context.Context, path string) (Snapshot, ImportReport, error) {
	var snap Snapshot
	report := ImportReport{Warnings: []string{}}
	abs, e := filepath.Abs(path)
	if e != nil {
		return snap, report, e
	}
	uriPath := filepath.ToSlash(abs)
	if filepath.VolumeName(abs) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	db, e := sql.Open("sqlite3", u.String()+"?mode=ro")
	if e != nil {
		return snap, report, e
	}
	defer db.Close()
	tx, e := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return snap, report, e
	}
	defer tx.Rollback()
	_ = tx.QueryRowContext(ctx, "SELECT version_num FROM alembic_version").Scan(&report.Revision)
	if report.Revision != "" {
		parts := strings.SplitN(report.Revision, "_", 2)
		n, err := strconv.Atoi(parts[0])
		if err != nil || n < 1 || n > 9 {
			return snap, report, fmt.Errorf("unsupported legacy revision %q", report.Revision)
		}
	}
	read := func(table string) ([]map[string]any, error) {
		rows, err := tx.QueryContext(ctx, "SELECT * FROM "+table)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		columns, err := rows.Columns()
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for rows.Next() {
			values := make([]any, len(columns))
			ptrs := make([]any, len(columns))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err = rows.Scan(ptrs...); err != nil {
				return nil, err
			}
			row := map[string]any{}
			for i, k := range columns {
				row[k] = values[i]
				if strings.HasSuffix(k, "_at") && values[i] != nil && timestamp(values[i]) == nil {
					return nil, fmt.Errorf("%s row has invalid %s", table, k)
				}
			}
			required := []string{}
			switch table {
			case "version":
				required = []string{"created_at", "updated_at"}
			case "artifact":
				required = []string{"created_at", "updated_at", "uploaded_at"}
			case "webhook_event":
				required = []string{"received_at"}
			case "audit_log":
				required = []string{"created_at"}
			case "channel_pointer":
				required = []string{"updated_at"}
			}
			for _, k := range required {
				if row[k] == nil {
					return nil, fmt.Errorf("%s row missing %s", table, k)
				}
			}
			out = append(out, row)
		}
		return out, rows.Err()
	}
	versions, e := read("version")
	if e != nil {
		return snap, report, e
	}
	for _, r := range versions {
		v := model.Version{ID: integer(r["id"]), Version: str(r["version"]), Channel: str(r["channel"]), Status: str(r["status"]), Notes: str(r["notes"]), ReleaseTag: str(r["release_tag"]), ReleaseID: str(r["release_id"]), PublishedAt: timestamp(r["published_at"]), MinimumVersion: nullable(r["minimum_version"]), ForceUpdate: boolean(r["force_update"]), CreatedAt: requiredTime(r["created_at"]), UpdatedAt: requiredTime(r["updated_at"]), RevokedAt: timestamp(r["revoked_at"]), RevokeReason: nullable(r["revoke_reason"])}
		if _, err := domain.ParseVersion(v.Version); err != nil {
			return snap, report, fmt.Errorf("version row %d: %w", v.ID, err)
		}
		if !domain.ChannelValid(v.Channel) {
			return snap, report, fmt.Errorf("version row %d has invalid channel", v.ID)
		}
		for _, k := range []string{"created_at", "updated_at", "published_at", "revoked_at"} {
			if r[k] != nil && timestamp(r[k]) == nil {
				return snap, report, fmt.Errorf("version row %d invalid timestamp %s", v.ID, k)
			}
		}
		snap.Versions = append(snap.Versions, v)
	}
	artifacts, e := read("artifact")
	if e != nil {
		return snap, report, e
	}
	for _, r := range artifacts {
		a := model.Artifact{ID: integer(r["id"]), VersionID: integer(r["version_id"]), Platform: str(r["platform"]), Architecture: str(r["architecture"]), Filename: str(r["filename"]), RelativePath: str(r["relative_path"]), Size: integer(r["size"]), SHA256: strings.ToLower(str(r["sha256"])), Signature: nullable(r["signature"]), SignatureFilename: nullable(r["signature_filename"]), ContentType: str(r["content_type"]), Kind: str(r["kind"]), Variant: str(r["variant"]), DisplayName: str(r["display_name"]), SortOrder: int(integer(r["sort_order"])), IsPublic: true, UploadedAt: requiredTime(r["uploaded_at"]), CreatedAt: requiredTime(r["created_at"]), UpdatedAt: requiredTime(r["updated_at"])}
		if _, ok := r["is_public"]; ok {
			a.IsPublic = boolean(r["is_public"])
		}
		if a.Kind == "" {
			a.Kind = "updater"
		}
		if v := str(r["target_platforms"]); v != "" && v != "null" {
			if err := json.Unmarshal([]byte(v), &a.TargetPlatforms); err != nil {
				return snap, report, fmt.Errorf("artifact row %d invalid target_platforms", a.ID)
			}
		}
		for _, k := range []string{"created_at", "updated_at", "uploaded_at"} {
			if r[k] == nil || timestamp(r[k]) == nil {
				return snap, report, fmt.Errorf("artifact row %d invalid timestamp %s", a.ID, k)
			}
		}
		snap.Artifacts = append(snap.Artifacts, a)
	}
	byID := map[int64]int{}
	for i, v := range snap.Versions {
		byID[v.ID] = i
	}
	for _, a := range snap.Artifacts {
		i, ok := byID[a.VersionID]
		if !ok {
			return snap, report, fmt.Errorf("artifact row %d has missing version", a.ID)
		}
		if !domain.ArtifactValid(snap.Versions[i], a) {
			return snap, report, fmt.Errorf("artifact row %d has invalid metadata", a.ID)
		}
		snap.Versions[i].Artifacts = append(snap.Versions[i].Artifacts, a)
	}
	for i := range snap.Versions {
		domain.Normalize(&snap.Versions[i])
		for _, a := range snap.Versions[i].Artifacts {
			for j := range snap.Artifacts {
				if snap.Artifacts[j].ID == a.ID {
					snap.Artifacts[j].TargetPlatforms = a.TargetPlatforms
				}
			}
		}
	}
	pointers, e := read("channel_pointer")
	if e != nil {
		return snap, report, e
	}
	for _, r := range pointers {
		snap.Pointers = append(snap.Pointers, model.ChannelPointer{Channel: str(r["channel"]), CurrentVersion: nullable(r["current_version"]), UpdatedAt: requiredTime(r["updated_at"])})
	}
	events, e := read("webhook_event")
	if e != nil {
		return snap, report, e
	}
	for _, r := range events {
		ev := model.WebhookEvent{EventID: str(r["event_id"]), Version: nullable(r["version"]), EventType: str(r["event_type"]), PayloadHash: str(r["payload_hash"]), ReceivedAt: requiredTime(r["received_at"]), ProcessedAt: timestamp(r["processed_at"]), Status: str(r["status"]), ErrorMessage: nullable(r["error_message"])}
		if ev.EventType == "" {
			ev.EventType = "release"
		}
		if ev.Version == nil {
			for _, v := range snap.Versions {
				if v.ReleaseTag != "" && v.ReleaseID != "" && ev.EventID == "github-"+v.ReleaseTag+"-"+v.ReleaseID {
					ev.Version = model.Ptr(v.Version)
				}
			}
		}
		snap.Events = append(snap.Events, ev)
	}
	audits, e := read("audit_log")
	if e != nil {
		return snap, report, e
	}
	for _, r := range audits {
		snap.Audits = append(snap.Audits, model.AuditLog{ID: integer(r["id"]), Operator: str(r["operator"]), Action: str(r["action"]), Channel: str(r["channel"]), Version: str(r["version"]), Reason: str(r["reason"]), RequestID: str(r["request_id"]), IPAddress: str(r["ip_address"]), CreatedAt: requiredTime(r["created_at"])})
	}
	for i := range snap.Versions {
		snap.Versions[i].Artifacts = nil
	}
	report.Versions = len(snap.Versions)
	report.Artifacts = len(snap.Artifacts)
	report.Events = len(snap.Events)
	report.Audits = len(snap.Audits)
	report.Warnings = append(report.Warnings, "Local package files and legacy usage statistics are not imported; run dist sync for retained local packages or configure DIST_SERVER.")
	if err := tx.Commit(); err != nil {
		return snap, report, err
	}
	return snap, report, nil
}

func (s *Store) Import(ctx context.Context, snap Snapshot, dryRun bool) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, table := range []string{"version", "artifact", "channel_pointer", "webhook_event", "audit_log"} {
			var n int64
			if err := tx.Table(table).Count(&n).Error; err != nil {
				return err
			}
			if n > 0 {
				return fmt.Errorf("target business tables must be empty: %s has %d rows", table, n)
			}
		}
		if dryRun {
			return nil
		}
		for _, part := range []any{&snap.Versions, &snap.Artifacts, &snap.Pointers, &snap.Events, &snap.Audits} {
			if reflect.ValueOf(part).Elem().Len() == 0 {
				continue
			}
			if err := tx.Omit("Artifacts").CreateInBatches(part, 100).Error; err != nil {
				return err
			}
		}
		if s.Dialect == "postgres" {
			for _, table := range []string{"version", "artifact", "audit_log"} {
				query := fmt.Sprintf("SELECT setval(pg_get_serial_sequence('%s','id'), COALESCE(MAX(id),1), MAX(id) IS NOT NULL) FROM %s", table, table)
				if err := tx.Exec(query).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s *Store) Snapshot(ctx context.Context) (Snapshot, error) {
	var out Snapshot
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, p := range []struct {
			dst   any
			order string
		}{{&out.Versions, "id"}, {&out.Artifacts, "id"}, {&out.Pointers, "channel"}, {&out.Events, "event_id"}, {&out.Audits, "id"}} {
			if e := tx.Order(p.order).Find(p.dst).Error; e != nil {
				return e
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true})
	return out, err
}

func (s *Store) Verify(ctx context.Context, want *Snapshot) error {
	got, e := s.Snapshot(ctx)
	if e != nil {
		return e
	}
	ids := map[int64]model.Version{}
	for _, v := range got.Versions {
		ids[v.ID] = v
		if _, e := domain.ParseVersion(v.Version); e != nil {
			return e
		}
	}
	for _, a := range got.Artifacts {
		v, ok := ids[a.VersionID]
		if !ok || !domain.ArtifactValid(v, a) {
			return fmt.Errorf("invalid artifact %d", a.ID)
		}
	}
	for _, p := range got.Pointers {
		if p.CurrentVersion != nil {
			found := false
			for _, v := range got.Versions {
				if v.Version == *p.CurrentVersion {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("pointer %s references missing version", p.Channel)
			}
		}
	}
	if s.Dialect == "postgres" {
		for _, table := range []string{"version", "artifact", "audit_log"} {
			var bad bool
			q := fmt.Sprintf("SELECT COALESCE((SELECT MAX(id) FROM %s),0) > (SELECT last_value FROM %s_id_seq)", table, table)
			if e := s.DB.WithContext(ctx).Raw(q).Scan(&bad).Error; e != nil {
				return e
			}
			if bad {
				return fmt.Errorf("sequence behind table %s", table)
			}
		}
	}
	if want != nil {
		canonical := func(x Snapshot) []byte {
			sort.Slice(x.Versions, func(i, j int) bool { return x.Versions[i].ID < x.Versions[j].ID })
			sort.Slice(x.Artifacts, func(i, j int) bool { return x.Artifacts[i].ID < x.Artifacts[j].ID })
			sort.Slice(x.Events, func(i, j int) bool { return x.Events[i].EventID < x.Events[j].EventID })
			sort.Slice(x.Audits, func(i, j int) bool { return x.Audits[i].ID < x.Audits[j].ID })
			sort.Slice(x.Pointers, func(i, j int) bool { return x.Pointers[i].Channel < x.Pointers[j].Channel })
			b, _ := json.Marshal(snapshotValue(reflect.ValueOf(x)))
			return b
		}
		if string(canonical(got)) != string(canonical(*want)) {
			return fmt.Errorf("target data differs from normalized source snapshot")
		}
	}
	return nil
}

// Include IDs and timestamps hidden from HTTP JSON, and normalize timezone and empty slices.
func snapshotValue(v reflect.Value) any {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		return snapshotValue(v.Elem())
	}
	if t, ok := v.Interface().(time.Time); ok {
		return t.UTC().Format(time.RFC3339Nano)
	}
	switch v.Kind() {
	case reflect.Struct:
		out := map[string]any{}
		for i := 0; i < v.NumField(); i++ {
			out[v.Type().Field(i).Name] = snapshotValue(v.Field(i))
		}
		return out
	case reflect.Slice:
		out := []any{}
		for i := 0; i < v.Len(); i++ {
			out = append(out, snapshotValue(v.Index(i)))
		}
		return out
	default:
		return v.Interface()
	}
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return fmt.Sprint(v)
}
func nullable(v any) *string {
	if v == nil {
		return nil
	}
	return model.Ptr(str(v))
}
func integer(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	n, _ := strconv.ParseInt(str(v), 10, 64)
	return n
}
func boolean(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return integer(v) != 0 || str(v) == "true"
}
func timestamp(v any) *time.Time {
	if v == nil || str(v) == "" {
		return nil
	}
	if t, ok := v.(time.Time); ok {
		if t.IsZero() {
			return nil
		}
		u := t.UTC()
		return &u
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999", "2006-01-02"} {
		if t, e := time.Parse(layout, str(v)); e == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}
func requiredTime(v any) time.Time {
	if t := timestamp(v); t != nil {
		return *t
	}
	return time.Time{}
}
