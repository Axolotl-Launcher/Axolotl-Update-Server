package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/domain"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
	"gorm.io/gorm"
)

type AdminMeta struct{ Operator, Reason, RequestID, IP string }

// Cleanup only touches a deleted version's quarantine and never deletes a live release.
func (s *Service) Cleanup(ctx context.Context, version string, meta AdminMeta) (map[string]any, error) {
	if e := validateAdmin(meta.Operator, meta.Reason); e != nil {
		return nil, e
	}
	unlock, e := s.Lock(version)
	if e != nil {
		return nil, e
	}
	defer unlock()
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	var count int64
	if e = s.Store.DB.WithContext(ctx).Model(&model.Version{}).Where("version = ?", version).Count(&count).Error; e != nil {
		return nil, e
	}
	if count > 0 {
		return nil, domain.Err("version_exists", "Version exists; cleanup cannot delete a live release.", 409)
	}
	path, e := s.quarantine(version)
	if e != nil {
		return nil, e
	}
	if _, e = os.Lstat(path); e != nil && !os.IsNotExist(e) {
		return nil, e
	}
	if e == nil {
		if e = s.RemoveAll(path); e != nil {
			return nil, domain.Err("version_cleanup_failed", "Could not remove remaining version files; retry cleanup.", 500)
		}
	}
	return map[string]any{"status": "deleted", "version": version, "idempotent": true, "files_cleanup_pending": false}, nil
}

func (m AdminMeta) log(v model.Version, action string) model.AuditLog {
	return model.AuditLog{Operator: m.Operator, Action: action, Channel: v.Channel, Version: v.Version, Reason: m.Reason, RequestID: m.RequestID, IPAddress: m.IP}
}

func (s *Service) ChangeState(ctx context.Context, version string, restore bool, meta AdminMeta) (map[string]any, error) {
	if e := validateAdmin(meta.Operator, meta.Reason); e != nil {
		return nil, e
	}
	unlock, e := s.Lock(version)
	if e != nil {
		return nil, e
	}
	defer unlock()
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	tx := s.Store.DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	defer tx.Rollback()
	var v model.Version
	if e = tx.Preload("Artifacts").Where("version = ?", version).First(&v).Error; errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, domain.Err("version_not_found", "Version does not exist.", 404)
	} else if e != nil {
		return nil, e
	}
	action := "revoke"
	if restore {
		for _, a := range v.Artifacts {
			if !domain.ArtifactValid(v, a) || (a.Kind == "updater" && !domain.Signed(a)) {
				return nil, domain.Err("artifact_validation_failed", "Cannot restore with incomplete artifacts.")
			}
		}
		v.Status = "published"
		v.RevokedAt = nil
		v.RevokeReason = nil
		action = "restore"
	} else {
		v.Status = "revoked"
		v.RevokedAt = model.Ptr(s.Now())
		v.RevokeReason = model.Ptr(meta.Reason)
	}
	if e = tx.Omit("Artifacts").Save(&v).Error; e != nil {
		return nil, e
	}
	if e = recomputePointers(tx, s.Now()); e != nil {
		return nil, e
	}
	audit := meta.log(v, action)
	if e = tx.Create(&audit).Error; e != nil {
		return nil, e
	}
	if e = s.commit(tx, action); e != nil {
		return nil, e
	}
	s.invalidate()
	return map[string]any{"status": v.Status, "version": version}, nil
}

func (s *Service) ForceUpdate(ctx context.Context, version string, enabled bool, meta AdminMeta) (map[string]any, error) {
	if e := validateAdmin(meta.Operator, meta.Reason); e != nil {
		return nil, e
	}
	unlock, e := s.Lock(version)
	if e != nil {
		return nil, e
	}
	defer unlock()
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	tx := s.Store.DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	defer tx.Rollback()
	var v model.Version
	if e = tx.Where("version = ?", version).First(&v).Error; errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, domain.Err("version_not_found", "Version does not exist.", 404)
	} else if e != nil {
		return nil, e
	}
	if v.Status != "published" && v.Status != "revoked" {
		return nil, domain.Err("invalid_version_state", "Only published or revoked versions can change force_update.", 409)
	}
	path, e := s.manifestPath(version)
	if e != nil {
		return nil, e
	}
	original, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return nil, domain.Err("manifest_unavailable", "The local version manifest is unavailable.", 409)
	} else if e != nil {
		return nil, e
	}
	result := map[string]any{"version": version, "status": v.Status, "force_update": enabled, "idempotent": v.ForceUpdate == enabled}
	if v.ForceUpdate == enabled {
		return result, nil
	}
	var manifest map[string]any
	if e = json.Unmarshal(original, &manifest); e != nil || manifest == nil || manifest["version"] != version {
		return nil, domain.Err("invalid_manifest", "The local version manifest is invalid.", 409)
	}
	manifest["force_update"] = enabled
	data, e := json.MarshalIndent(manifest, "", "  ")
	if e != nil {
		return nil, e
	}
	v.ForceUpdate = enabled
	if e = tx.Save(&v).Error; e != nil {
		return nil, e
	}
	action := "force_update_disabled"
	if enabled {
		action = "force_update_enabled"
	}
	audit := meta.log(v, action)
	if e = tx.Create(&audit).Error; e != nil {
		return nil, e
	}
	if e = atomicWrite(path, data); e != nil {
		return nil, domain.Err("force_update_failed", "Could not update the version force_update setting.", 500)
	}
	if e = s.commit(tx, action); e != nil {
		return nil, s.logRestore(path, original, true, domain.Err("force_update_failed", "Could not update the version force_update setting.", 500))
	}
	s.invalidate()
	return result, nil
}

func (s *Service) Delete(ctx context.Context, version string, meta AdminMeta) (map[string]any, error) {
	if e := validateAdmin(meta.Operator, meta.Reason); e != nil {
		return nil, e
	}
	unlock, e := s.Lock(version)
	if e != nil {
		return nil, e
	}
	defer unlock()
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	quarantine, e := s.quarantine(version)
	if e != nil {
		return nil, e
	}
	tx := s.Store.DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	defer tx.Rollback()
	var v model.Version
	if e = tx.Preload("Artifacts").Where("version = ?", version).First(&v).Error; errors.Is(e, gorm.ErrRecordNotFound) {
		if _, err := os.Lstat(quarantine); os.IsNotExist(err) {
			return nil, domain.Err("version_not_found", "Version does not exist.", 404)
		} else if err != nil {
			return nil, err
		}
		if e = s.RemoveAll(quarantine); e != nil {
			return nil, domain.Err("version_cleanup_failed", "Could not remove remaining version files; retry deletion.", 500)
		}
		return map[string]any{"status": "deleted", "version": version, "idempotent": true, "files_cleanup_pending": false}, nil
	} else if e != nil {
		return nil, e
	}
	if _, e = os.Lstat(quarantine); e == nil {
		return nil, domain.Err("version_cleanup_pending", "An earlier deletion still has files pending cleanup.", 409)
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	legacyID := ""
	if v.ReleaseTag != "" && v.ReleaseID != "" {
		legacyID = "github-" + v.ReleaseTag + "-" + v.ReleaseID
	}
	query := tx.Where("version = ?", version)
	if legacyID != "" {
		query = tx.Where("version = ? OR (version IS NULL AND event_id = ?)", version, legacyID)
	}
	result := query.Delete(&model.WebhookEvent{})
	if result.Error != nil {
		return nil, result.Error
	}
	deletedEvents := result.RowsAffected
	if e = tx.Delete(&v).Error; e != nil {
		return nil, e
	}
	if e = recomputePointers(tx, s.Now()); e != nil {
		return nil, e
	}
	audit := meta.log(v, "delete_version")
	if e = tx.Create(&audit).Error; e != nil {
		return nil, e
	}
	path, e := s.manifestPath(version)
	if e != nil {
		return nil, e
	}
	directory := filepath.Dir(path)
	moved := false
	if _, e = os.Stat(directory); e == nil {
		if e = os.MkdirAll(filepath.Dir(quarantine), 0755); e != nil {
			return nil, e
		}
		if e = os.Rename(directory, quarantine); e != nil {
			return nil, domain.Err("version_delete_failed", "Could not quarantine version files.", 500)
		}
		moved = true
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if e = s.commit(tx, "delete_version"); e != nil {
		if moved {
			if restore := os.Rename(quarantine, directory); restore != nil {
				return nil, domain.Err("version_restore_failed", "Deletion failed and version files require recovery from quarantine.", 500)
			}
		}
		return nil, domain.Err("version_delete_failed", "Could not delete the version.", 500)
	}
	s.invalidate()
	pending := false
	if moved {
		pending = s.RemoveAll(quarantine) != nil
	}
	pointers := map[string]*string{}
	var ps []model.ChannelPointer
	if e = s.Store.DB.Find(&ps).Error; e != nil {
		return nil, e
	}
	for _, p := range ps {
		pointers[p.Channel] = p.CurrentVersion
	}
	return map[string]any{"status": "deleted", "version": version, "idempotent": false, "deleted_artifacts": len(v.Artifacts), "deleted_webhook_events": deletedEvents, "channel_pointers": pointers, "files_cleanup_pending": pending}, nil
}
