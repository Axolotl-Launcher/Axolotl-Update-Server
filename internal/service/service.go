package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/config"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/domain"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/store"
	"github.com/Masterminds/semver/v3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type cacheEntry struct {
	expires    time.Time
	manifest   *domain.Manifest
	generation uint64
	used       uint64
}
type Service struct {
	Store             *store.Store
	Config            config.Config
	Now               func() time.Time
	FetchSignature    func(context.Context, string, int64, string) (string, error)
	BeforeCommit      func(string) error // Fault-injection seam; nil in production.
	RemoveAll         func(string) error
	mu                sync.Mutex
	busy              map[string]bool
	commitMu          sync.Mutex
	cacheMu           sync.Mutex
	cache             map[string]cacheEntry
	generation, clock uint64
}

func New(s *store.Store, c config.Config) *Service {
	v := &Service{Store: s, Config: c, Now: func() time.Time { return time.Now().UTC() }, RemoveAll: os.RemoveAll, busy: map[string]bool{}, cache: map[string]cacheEntry{}}
	v.FetchSignature = v.fetchSignature
	return v
}
func (s *Service) Lock(version string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[version] {
		return nil, domain.Err("version_busy", "Another operation is modifying this version; retry later.", 409)
	}
	s.busy[version] = true
	return func() { s.mu.Lock(); delete(s.busy, version); s.mu.Unlock() }, nil
}
func (s *Service) invalidate() { s.cacheMu.Lock(); s.generation++; clear(s.cache); s.cacheMu.Unlock() }
func (s *Service) commit(tx *gorm.DB, action string) error {
	if s.BeforeCommit != nil {
		if e := s.BeforeCommit(action); e != nil {
			return e
		}
	}
	return tx.Commit().Error
}

func versions(db *gorm.DB) ([]model.Version, error) {
	items := []model.Version{}
	e := db.Preload("Artifacts", func(tx *gorm.DB) *gorm.DB { return tx.Order("id") }).Find(&items).Error
	for i := range items {
		domain.Normalize(&items[i])
	}
	return items, e
}
func (s *Service) Versions(ctx context.Context) ([]model.Version, error) {
	items, e := versions(s.Store.DB.WithContext(ctx))
	if e != nil {
		return nil, e
	}
	sortVersions(items)
	return items, nil
}
func sortVersions(v []model.Version) {
	// The version count is small; insertion sort keeps malformed legacy values last.
	for i := 1; i < len(v); i++ {
		for j := i; j > 0; j-- {
			a, ea := domain.ParseVersion(v[j].Version)
			b, eb := domain.ParseVersion(v[j-1].Version)
			if ea != nil || (eb == nil && !a.GreaterThan(b)) {
				break
			}
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
func (s *Service) Version(ctx context.Context, name string) (model.Version, error) {
	var v model.Version
	e := s.Store.DB.WithContext(ctx).Preload("Artifacts", func(tx *gorm.DB) *gorm.DB { return tx.Order("id") }).Where("version = ?", name).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return v, domain.Err("version_not_found", "Version does not exist.", 404)
	}
	domain.Normalize(&v)
	return v, e
}

func (s *Service) Latest(ctx context.Context, channel, platform, current string) (*domain.Manifest, error) {
	state := "miss"
	if s.Config.LatestCacheTTL <= 0 {
		state = "bypass"
	}
	observeCache(ctx, state)
	key := channel + "\x00" + platform + "\x00" + current
	now := s.Now()
	s.cacheMu.Lock()
	s.clock++
	generation := s.generation
	if entry, ok := s.cache[key]; ok && entry.expires.After(now) && entry.generation == generation {
		observeCache(ctx, "hit")
		entry.used = s.clock
		s.cache[key] = entry
		s.cacheMu.Unlock()
		return entry.manifest, nil
	}
	s.cacheMu.Unlock()
	items, e := s.Versions(ctx)
	if e != nil {
		return nil, e
	}
	var cur *semver.Version
	if current != "" {
		cur, e = domain.ParseVersion(current)
		if e != nil {
			return nil, e
		}
	}
	var found *domain.Manifest
	for _, v := range items {
		if !domain.Eligible(v, channel) || (v.PublishedAt == nil && !v.ForceUpdate) {
			continue
		}
		parsed, _ := domain.ParseVersion(v.Version)
		if cur != nil && !parsed.GreaterThan(cur) {
			continue
		}
		if platform != "" {
			available := false
			for _, a := range v.Artifacts {
				if !domain.Signed(a) || !domain.ArtifactValid(v, a) {
					continue
				}
				for _, p := range a.TargetPlatforms {
					if p == platform {
						available = true
					}
				}
			}
			if !available {
				continue
			}
		}
		m, err := domain.BuildManifest(v, s.Config.PublicBaseURL, platform, now)
		if err != nil {
			return nil, err
		}
		if platform != "" && len(m.Platforms) == 0 {
			continue
		}
		found = &m
		break
	}
	if s.Config.LatestCacheTTL > 0 {
		s.cacheMu.Lock()
		if s.generation == generation {
			s.clock++
			if len(s.cache) >= 256 {
				var oldest string
				var age uint64 = ^uint64(0)
				for k, v := range s.cache {
					if v.used < age {
						oldest = k
						age = v.used
					}
				}
				delete(s.cache, oldest)
			}
			s.cache[key] = cacheEntry{now.Add(s.Config.LatestCacheTTL), found, generation, s.clock}
		}
		s.cacheMu.Unlock()
	}
	return found, nil
}

func recomputePointers(tx *gorm.DB, now time.Time) error {
	items, e := versions(tx)
	if e != nil {
		return e
	}
	sortVersions(items)
	for _, channel := range []string{"release", "beta"} {
		var selected *string
		for _, v := range items {
			if !domain.Eligible(v, channel) || v.PublishedAt == nil {
				continue
			}
			for _, a := range v.Artifacts {
				if domain.Signed(a) && domain.ArtifactValid(v, a) {
					selected = model.Ptr(v.Version)
					break
				}
			}
			if selected != nil {
				break
			}
		}
		p := model.ChannelPointer{Channel: channel, CurrentVersion: selected, UpdatedAt: now}
		if e := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&p).Error; e != nil {
			return e
		}
	}
	return nil
}

func (s *Service) manifestPath(version string) (string, error) {
	if _, e := domain.ParseVersion(version); e != nil {
		return "", e
	}
	if e := safeDir(s.Config.DistRoot); e != nil {
		return "", e
	}
	dir := filepath.Join(s.Config.DistRoot, version)
	if e := safeDir(dir); e != nil {
		return "", e
	}
	path := filepath.Join(dir, "manifest.json")
	if st, e := os.Lstat(path); e == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return "", domain.Err("unsafe_storage_path", "Manifest path is unsafe.", 409)
		}
	} else if !os.IsNotExist(e) {
		return "", e
	}
	return path, nil
}
func safeDir(path string) error {
	st, e := os.Lstat(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return domain.Err("unsafe_storage_path", "Version storage path is unsafe.", 409)
	}
	return nil
}
func (s *Service) ManifestPath(version string) (string, error) { return s.manifestPath(version) }
func atomicWrite(path string, data []byte) error {
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".manifest-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(data); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(name, path)
}
func restoreFile(path string, original []byte, existed bool) error {
	if existed {
		return atomicWrite(path, original)
	}
	e := os.Remove(path)
	if os.IsNotExist(e) {
		return nil
	}
	return e
}
func (s *Service) Rebuild(ctx context.Context) error {
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	items, e := s.Versions(ctx)
	if e != nil {
		return e
	}
	for _, v := range items {
		if v.Status != "published" && v.Status != "revoked" {
			continue
		}
		path, e := s.manifestPath(v.Version)
		if e != nil {
			return e
		}
		m, e := domain.BuildManifest(v, s.Config.PublicBaseURL, "", s.Now())
		if e != nil {
			return e
		}
		if len(m.Platforms) == 0 {
			return fmt.Errorf("version %s has no signed updater", v.Version)
		}
		data, e := json.MarshalIndent(m, "", "  ")
		if e != nil {
			return e
		}
		if e = atomicWrite(path, data); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) VerifyManifests(ctx context.Context) error {
	items, e := s.Versions(ctx)
	if e != nil {
		return e
	}
	for _, v := range items {
		if v.Status != "published" && v.Status != "revoked" {
			continue
		}
		path, e := s.manifestPath(v.Version)
		if e != nil {
			return e
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return fmt.Errorf("manifest %s: %w", v.Version, e)
		}
		var got domain.Manifest
		if e = json.Unmarshal(b, &got); e != nil {
			return e
		}
		want, e := domain.BuildManifest(v, s.Config.PublicBaseURL, "", s.Now())
		if e != nil {
			return e
		}
		gb, _ := json.Marshal(got)
		wb, _ := json.Marshal(want)
		if string(gb) != string(wb) {
			return fmt.Errorf("manifest %s differs from database", v.Version)
		}
	}
	return nil
}
func (s *Service) quarantine(version string) (string, error) {
	sum := sha256.Sum256([]byte(version))
	root := filepath.Join(filepath.Dir(s.Config.DistRoot), "."+filepath.Base(s.Config.DistRoot)+"-deletions")
	if e := safeDir(root); e != nil {
		return "", e
	}
	path := filepath.Join(root, hex.EncodeToString(sum[:]))
	if e := safeDir(path); e != nil {
		return "", e
	}
	return path, nil
}
func (s *Service) EnsureCleanup(version string) error {
	path, e := s.quarantine(version)
	if e != nil {
		return e
	}
	if _, e = os.Lstat(path); e == nil {
		return domain.Err("version_cleanup_pending", "Retry version deletion to finish file cleanup before republishing.", 409)
	} else if !os.IsNotExist(e) {
		return e
	}
	return nil
}
func (s *Service) logRestore(path string, original []byte, exists bool, cause error) error {
	if e := restoreFile(path, original, exists); e != nil {
		slog.Error("manifest restore failed", "path", path, "error", e)
		return domain.Err("version_restore_failed", "Manifest recovery requires operator attention.", 500)
	}
	return cause
}
func validateAdmin(operator, reason string) error {
	if strings.TrimSpace(operator) == "" || len([]rune(operator)) > 128 {
		return domain.Err("invalid_admin_metadata", "operator and reason must be strings; operator must contain 1 to 128 characters.")
	}
	return nil
}
