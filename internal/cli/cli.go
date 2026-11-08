package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/config"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/store"
)

func Run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, e := fmt.Fprintln(out, "Usage: axolotl-server db {migrate|status|import-legacy|verify}\nConfigure DATABASE_URL and DIST_ROOT through environment variables or .env.")
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
		_, e = fmt.Fprintln(out, "Database verification passed.")
		return e
	default:
		return errors.New("unknown database command; use --help")
	}
}
