package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/analytics"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/config"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/httpapi"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/service"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/store"
)

func Run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, e := fmt.Fprintln(out, "Usage: axolotl-server serve | db {migrate|status|import-legacy|verify} | manifests rebuild | analytics maintain [--dry-run]\nConfigure DATABASE_URL and DIST_ROOT through environment variables or .env.")
		return e
	}
	c, e := config.Load()
	if e != nil {
		return e
	}
	s, e := store.Open(c.DatabaseURL)
	if e != nil {
		return fmt.Errorf("open database: %w", e)
	}
	defer s.Close()
	if args[0] == "serve" {
		return serve(ctx, s, c)
	}
	if args[0] == "analytics" && len(args) >= 2 && args[1] == "maintain" {
		flags := flag.NewFlagSet("analytics maintain", flag.ContinueOnError)
		flags.SetOutput(out)
		dryRun := flags.Bool("dry-run", false, "show maintenance without writing")
		if e = flags.Parse(args[2:]); e != nil {
			return e
		}
		if flags.NArg() != 0 {
			return errors.New("unexpected maintenance arguments")
		}
		if e = s.CheckSchema(ctx); e != nil {
			return e
		}
		report, e := (&analytics.Storage{Store: s}).Maintain(ctx, time.Now().UTC(), *dryRun)
		if e != nil {
			return e
		}
		return json.NewEncoder(out).Encode(report)
	}
	if args[0] == "manifests" && len(args) == 2 && args[1] == "rebuild" {
		if e = s.CheckSchema(ctx); e != nil {
			return e
		}
		if e = service.New(s, c).Rebuild(ctx); e != nil {
			return e
		}
		_, e = fmt.Fprintln(out, "Manifests rebuilt.")
		return e
	}
	if args[0] != "db" || len(args) < 2 {
		return errors.New("unknown command; use --help")
	}
	if args[1] == "migrate" {
		if e = s.Migrate(ctx); e != nil {
			return e
		}
		_, e = fmt.Fprintln(out, "Database migrations applied.")
		return e
	}
	if args[1] == "status" {
		status, e := s.Status(ctx)
		if e != nil {
			return e
		}
		return json.NewEncoder(out).Encode(status)
	}
	flags := flag.NewFlagSet(args[1], flag.ContinueOnError)
	flags.SetOutput(out)
	source := flags.String("source", "", "legacy SQLite database path")
	dryRun := flags.Bool("dry-run", false, "validate without writing")
	if e = flags.Parse(args[2:]); e != nil {
		return e
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if e = s.CheckSchema(ctx); e != nil {
		return e
	}
	switch args[1] {
	case "import-legacy":
		if *source == "" {
			return errors.New("--source is required")
		}
		snap, report, e := store.ReadLegacy(ctx, *source)
		if e != nil {
			return e
		}
		if e = s.Import(ctx, snap, *dryRun); e != nil {
			return e
		}
		return json.NewEncoder(out).Encode(report)
	case "verify":
		var want *store.Snapshot
		if *source != "" {
			snap, _, e := store.ReadLegacy(ctx, *source)
			if e != nil {
				return e
			}
			want = &snap
		}
		if e = s.Verify(ctx, want); e != nil {
			return e
		}
		if e = service.New(s, c).VerifyManifests(ctx); e != nil {
			return e
		}
		_, e = fmt.Fprintln(out, "Database and manifest verification passed.")
		return e
	default:
		return errors.New("unknown database command; use --help")
	}
}

func serve(ctx context.Context, s *store.Store, c config.Config) error {
	if c.Env == "production" {
		if e := s.CheckSchema(ctx); e != nil {
			return e
		}
	} else {
		if e := s.Migrate(ctx); e != nil {
			return e
		}
	}
	var observer httpapi.Observer
	var collector *analytics.Collector
	background, cancelBackground := context.WithCancel(context.Background())
	maintenanceDone := make(chan struct{})
	defer func() { cancelBackground(); <-maintenanceDone }()
	if c.AnalyticsEnabled {
		storage := &analytics.Storage{Store: s}
		if _, e := storage.Maintain(ctx, time.Now().UTC(), false); e != nil {
			slog.Warn("initial analytics maintenance failed; background task will retry", "error", e)
		}
		collector = analytics.NewCollector(storage, c.AnalyticsQueueSize, c.AnalyticsBatchSize, c.AnalyticsFlushInterval)
		observer = collector.Observe
		defer func() {
			shutdown, cancel := context.WithTimeout(context.Background(), c.AnalyticsShutdownTimeout)
			defer cancel()
			if e := collector.Close(shutdown); e != nil {
				slog.Warn("analytics shutdown incomplete", "error", e)
			}
			slog.Info("analytics final counters", "stats", collector.Stats())
		}()
		go func() {
			defer close(maintenanceDone)
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-background.Done():
					return
				case <-ticker.C:
					ctx, cancel := context.WithTimeout(background, 30*time.Second)
					_, e := storage.Maintain(ctx, time.Now().UTC(), false)
					cancel()
					if e != nil {
						slog.Warn("analytics maintenance failed", "error", e)
					}
				}
			}
		}()
	} else {
		close(maintenanceDone)
	}
	api := httpapi.New(service.New(s, c), observer)
	server := &http.Server{Addr: net.JoinHostPort(c.Host, c.Port), Handler: api.Router, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	slog.Info("server starting", "address", server.Addr, "database", s.Dialect)
	select {
	case e := <-errCh:
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if e := server.Shutdown(shutdown); e != nil {
		_ = server.Close()
		return e
	}
	return nil
}
