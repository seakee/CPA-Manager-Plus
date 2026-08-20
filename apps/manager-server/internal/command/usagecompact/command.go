package usagecompact

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

type compactUsageFunc func(context.Context, string) (sqlite.CompactResult, error)

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return runWithCompactor(ctx, args, stdout, stderr, sqlite.CompactUsage)
}

func runWithCompactor(ctx context.Context, args []string, stdout, stderr io.Writer, compact compactUsageFunc) (runErr error) {
	if compact == nil {
		return errors.New("compact-usage implementation is not configured")
	}
	flags := flag.NewFlagSet("compact-usage", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbPath := flags.String("db-path", "", "path to the existing CPA Manager Plus usage.sqlite database")
	flags.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: cpa-manager-plus compact-usage [--db-path PATH]")
		_, _ = fmt.Fprintln(stderr, "Stop Manager Server before running this offline command.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("compact-usage does not accept positional arguments")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	path := strings.TrimSpace(*dbPath)
	if path == "" {
		cfg, err := config.LoadWithoutCreatingDefault()
		if err != nil {
			return fmt.Errorf("load config without creating defaults: %w", err)
		}
		path = cfg.DBPath
	}
	path, err := sqlite.ResolveMaintenancePath(path)
	if err != nil {
		return err
	}
	databaseLock, err := processlock.Acquire(path)
	if err != nil {
		if errors.Is(err, processlock.ErrLocked) {
			return fmt.Errorf("compact-usage requires exclusive database ownership; stop Manager Server and retry: %w", err)
		}
		return fmt.Errorf("acquire offline compact-usage database lock: %w", err)
	}
	lockClosed := false
	defer func() {
		if lockClosed {
			return
		}
		if err := databaseLock.Close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("release offline compact-usage database lock: %w", err))
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}

	result, err := compact(ctx, databaseLock.DatabasePath())
	if err != nil {
		return err
	}
	if err := databaseLock.Close(); err != nil {
		lockClosed = true
		return fmt.Errorf("release offline compact-usage database lock: %w", err)
	}
	lockClosed = true
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("write compact-usage result: %w", err)
	}
	return nil
}

func IsHelp(err error) bool {
	return errors.Is(err, flag.ErrHelp)
}
