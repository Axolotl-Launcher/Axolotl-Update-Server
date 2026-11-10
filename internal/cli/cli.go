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
		_, e := fmt.Fprintln(out, "Usage: axolotl-server serve | db {migrate|status|import-legacy|verify} | manifests rebuild | dist {sync|prune} | analytics {maintain [--dry-run]|reclassify [--batch-size 100 --after-id N --dry-run]|relocate [--start RFC3339 --end RFC3339 --batch-size 500 --pause-ms 100 --after RFC3339 --after-id N --dry-run]}\nConfigure DATABASE_URL and DIST_ROOT through environment variables or .env.")
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
	if args[0] == "analytics" && len(args) >= 2 && args[1] == "relocate" {
		return relocate(ctx, args[2:], out, s, c)
	}
	if args[0] == "analytics" && len(args) >= 2 && args[1] == "reclassify" {
		flags := flag.NewFlagSet("analytics reclassify", flag.ContinueOnError)
		flags.SetOutput(out)
		batch := flags.Int("batch-size", 100, "UA dictionary rows per short transaction")
		after := flags.Int64("after-id", 0, "resume after last committed UA id")
		dryRun := flags.Bool("dry-run", false, "report without changing rows")
		if e = flags.Parse(args[2:]); e != nil {
			return e
		}
		if flags.NArg() != 0 {
			return errors.New("unexpected reclassify arguments")
		}
		if e = s.CheckSchema(ctx); e != nil {
			return e
		}
		encoder := json.NewEncoder(out)
		r, e := (&analytics.Storage{Store: s}).Reclassify(ctx, *batch, *after, *dryRun, func(r analytics.ReclassifyReport) error { return encoder.Encode(r) })
		if e != nil {
			_ = encoder.Encode(r)
		}
		return e
	}
	if args[0] == "dist" && len(args) == 2 {
		if e = s.CheckSchema(ctx); e != nil {
			return e
		}
		svc := service.New(s, c)
		switch args[1] {
		case "sync":
			versions, e := svc.SyncDist(ctx)
			if e != nil {
				return e
			}
			return json.NewEncoder(out).Encode(map[string]any{"synced_versions": versions})
		case "prune":
			files, e := svc.PruneDist(ctx)
			if e != nil {
				return e
			}
			return json.NewEncoder(out).Encode(map[string]any{"removed_files": files})
		default:
			return errors.New("unknown dist command; use dist sync or dist prune")
		}
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

func relocate(ctx context.Context, args []string, out io.Writer, s *store.Store, c config.Config) error {
	flags := flag.NewFlagSet("analytics relocate", flag.ContinueOnError)
	flags.SetOutput(out)
	startRaw := flags.String("start", "", "start of time window, RFC3339; default 14 days ago")
	endRaw := flags.String("end", "", "exclusive end, RFC3339; default command start time")
	afterRaw := flags.String("after", "", "resume from last committed occurred_at, RFC3339")
	afterID := flags.Int64("after-id", 0, "resume from last committed row id")
	batch := flags.Int("batch-size", 500, "rows per short transaction (1..1000)")
	pause := flags.Int("pause-ms", 100, "pause between batches")
	dryRun := flags.Bool("dry-run", false, "resolve and report without updating rows")
	if e := flags.Parse(args); e != nil {
		return e
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected relocate arguments")
	}
	now := time.Now().UTC()
	o := analytics.RelocateOptions{Start: now.Add(-14 * 24 * time.Hour), End: now, BatchSize: *batch, Pause: time.Duration(*pause) * time.Millisecond, AfterID: *afterID, DryRun: *dryRun}
	for raw, dst := range map[*string]*time.Time{startRaw: &o.Start, endRaw: &o.End, afterRaw: &o.After} {
		if *raw != "" {
			parsed, e := time.Parse(time.RFC3339Nano, *raw)
			if e != nil {
				return fmt.Errorf("relocate timestamps must be RFC3339: %w", e)
			}
			*dst = parsed.UTC()
		}
	}
	if o.Start.Before(now.Add(-14 * 24 * time.Hour)) {
		o.Start = now.Add(-14 * 24 * time.Hour)
	}
	if o.End.After(now) {
		return errors.New("relocate end cannot be in the future")
	}
	// A resumed cursor may have fallen outside the moving retention window.
	if !o.After.IsZero() && o.After.Before(o.Start) {
		o.After = time.Time{}
		o.AfterID = 0
	}
	if e := s.CheckSchema(ctx); e != nil {
		return e
	}
	geo, e := analytics.OpenGeoIPWithOptions(c.GeoIPCityDB, c.GeoIPASNDB, c.GeoIPQQWryDB, c.GeoIPAPIURL)
	if e != nil {
		return e
	}
	defer geo.Close()
	geo.BindRateStore(s)
	storage := &analytics.Storage{Store: s, Geo: geo}
	encoder := json.NewEncoder(out)
	r, e := storage.Relocate(ctx, o, func(r analytics.RelocateReport) error { return encoder.Encode(r) })
	if e != nil {
		_ = encoder.Encode(r)
		return fmt.Errorf("relocate stopped; resume using last reported start/end/after/after_id: %w", e)
	}
	return nil
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
		geo, e := analytics.OpenGeoIPWithOptions(c.GeoIPCityDB, c.GeoIPASNDB, c.GeoIPQQWryDB, c.GeoIPAPIURL)
		if e != nil {
			return e
		}
		defer geo.Close()
		geo.BindRateStore(s)
		storage := &analytics.Storage{Store: s, Geo: geo}
		maintenanceDelay := time.Hour
		initialContext, initialCancel := context.WithTimeout(ctx, 30*time.Second)
		_, initialError := storage.Maintain(initialContext, time.Now().UTC(), false)
		initialCancel()
		if initialError != nil {
			slog.Warn("initial analytics maintenance failed; background task will retry", "error", initialError)
			maintenanceDelay = time.Minute
		}
		collector = analytics.NewCollector(storage, c.AnalyticsQueueSize, c.AnalyticsBatchSize, c.AnalyticsFlushInterval)
		observer = collector.Observe
		defer func() {
			cancelBackground()
			<-maintenanceDone
			shutdown, cancel := context.WithTimeout(context.Background(), c.AnalyticsShutdownTimeout)
			defer cancel()
			if e := collector.Close(shutdown); e != nil {
				slog.Warn("analytics shutdown incomplete", "error", e)
			}
		}()
		go func() {
			defer close(maintenanceDone)
			ticker := time.NewTimer(maintenanceDelay)
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
						ticker.Reset(time.Minute)
					} else {
						ticker.Reset(time.Hour)
					}
				}
			}
		}()
	} else {
		close(maintenanceDone)
	}
	svc := service.New(s, c)
	if _, e := svc.PruneDist(ctx); e != nil {
		return fmt.Errorf("distribution retention: %w", e)
	}
	api := httpapi.New(svc, observer)
	api.Collector = collector
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
