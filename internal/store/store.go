package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

//go:embed migrations/*/*.sql
var migrations embed.FS

type Store struct {
	DB         *gorm.DB
	SQL        *sql.DB
	Dialect    string
	migrations *goose.Provider
}

func Open(dsn string) (*Store, error) {
	var dialector gorm.Dialector
	dialect := "postgres"
	if strings.HasPrefix(dsn, "sqlite:") {
		dialect = "sqlite3"
		path := strings.TrimPrefix(dsn, "sqlite://")
		if path == dsn {
			path = strings.TrimPrefix(dsn, "sqlite:")
		}
		if path != "" && path != ":memory:" && !strings.HasPrefix(path, "file:") {
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return nil, err
			}
		}
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		dialector = sqlite.Open(path + sep + "_foreign_keys=on&_busy_timeout=5000&_journal_mode=WAL")
	} else if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		dialector = postgres.Open(dsn)
	} else {
		return nil, fmt.Errorf("DATABASE_URL must use postgres://, postgresql:// or sqlite://")
	}
	db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), NowFunc: func() time.Time { return time.Now().UTC() }})
	if err != nil {
		return nil, err
	}
	sq, err := db.DB()
	if err != nil {
		return nil, err
	}
	if dialect == "sqlite3" {
		sq.SetMaxOpenConns(1)
	} else {
		sq.SetMaxOpenConns(12)
		sq.SetMaxIdleConns(4)
		sq.SetConnMaxLifetime(30 * time.Minute)
	}
	s := &Store{DB: db, SQL: sq, Dialect: dialect}
	return s, nil
}

func (s *Store) provider() (*goose.Provider, error) {
	if s.migrations != nil {
		return s.migrations, nil
	}
	sub, err := subFS(s.Dialect)
	if err != nil {
		return nil, err
	}
	dialect := goose.DialectPostgres
	if s.Dialect == "sqlite3" {
		dialect = goose.DialectSQLite3
	}
	p, err := goose.NewProvider(dialect, s.SQL, sub)
	if err != nil {
		return nil, err
	}
	s.migrations = p
	return p, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	p, e := s.provider()
	if e != nil {
		return e
	}
	_, e = p.Up(ctx)
	return e
}
func (s *Store) Status(ctx context.Context) ([]*goose.MigrationStatus, error) {
	p, e := s.provider()
	if e != nil {
		return nil, e
	}
	return p.Status(ctx)
}
func (s *Store) CheckSchema(ctx context.Context) error {
	p, e := s.provider()
	if e != nil {
		return e
	}
	pending, e := p.HasPending(ctx)
	if e != nil {
		return e
	}
	if pending {
		return fmt.Errorf("database schema is behind; run axolotl-server db migrate")
	}
	return nil
}
func (s *Store) Close() error { return s.SQL.Close() }
