package derivedmaintenance

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func Run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	opts, err := parseArgs(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	databaseOptions, err := resolveDatabaseOptions(opts.DBPath)
	if err != nil {
		return err
	}
	if err := validateDatabaseFile(databaseOptions.Path); err != nil {
		return err
	}
	databaseLock, err := processlock.Acquire(databaseOptions.Path)
	if err != nil {
		return fmt.Errorf("acquire offline cleanup lock; stop Manager Server and retry: %w", err)
	}
	defer func() { _ = databaseLock.Close() }()
	databaseOptions.Path = databaseLock.DatabasePath()
	if err := validateManagerDB(ctx, databaseOptions.Path); err != nil {
		return err
	}
	databaseOptions.MaxOpenConns = 1
	databaseOptions.MaxIdleConns = 1
	db, err := sqliterepo.OpenUnmigratedWithOptions(databaseOptions)
	if err != nil {
		return fmt.Errorf("open sqlite %s: %w", databaseOptions.Path, err)
	}
	defer db.Close()
	result, err := sqliterepo.CleanupDerivedOffline(ctx, db)
	if err != nil {
		return fmt.Errorf("cleanup derived data: %w", err)
	}
	if result.CompletedJobs == 0 && result.ProcessedRows == 0 && result.PreparedIndexes == 0 {
		_, _ = fmt.Fprintln(stdout, "No pending derived cleanup jobs.")
		return nil
	}
	_, _ = fmt.Fprintf(stdout, "Derived cleanup completed: jobs=%d processed_rows=%d prepared_indexes=%d\n", result.CompletedJobs, result.ProcessedRows, result.PreparedIndexes)
	return nil
}

type options struct {
	DBPath string
}

func parseArgs(args []string, stderr io.Writer) (options, error) {
	var opts options
	fs := flag.NewFlagSet("cleanup-derived", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.DBPath, "db-path", "", "SQLite database path; defaults to Manager Server config")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: cpa-manager-plus cleanup-derived [--db-path PATH]")
		_, _ = fmt.Fprintln(stderr, "Stop Manager Server before running this offline command.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return opts, nil
}

func resolveDatabaseOptions(override string) (sqliterepo.Options, error) {
	if strings.TrimSpace(override) != "" {
		return sqliterepo.Options{Path: strings.TrimSpace(override)}, nil
	}
	cfg, err := config.LoadWithoutCreatingDefault()
	if err != nil {
		return sqliterepo.Options{}, fmt.Errorf("load config: %w", err)
	}
	if strings.TrimSpace(cfg.DBPath) == "" {
		return sqliterepo.Options{}, errors.New("SQLite database path is empty; pass --db-path")
	}
	return sqliterepo.Options{
		Path:                cfg.DBPath,
		DataSourceName:      cfg.DBURL,
		ExpectedJournalMode: cfg.DBJournalMode,
		ExpectedSynchronous: cfg.DBSynchronous,
		ExpectedBusyTimeout: cfg.DBBusyTimeout,
	}, nil
}

func validateDatabaseFile(dbPath string) error {
	info, err := os.Stat(dbPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("SQLite database not found at %s; pass --db-path", dbPath)
		}
		return fmt.Errorf("stat sqlite %s: %w", dbPath, err)
	}
	if info.IsDir() || info.Size() == 0 {
		return fmt.Errorf("SQLite database at %s is not a non-empty file", dbPath)
	}
	return nil
}

func validateManagerDB(ctx context.Context, dbPath string) error {
	db, err := sqliterepo.OpenUnmigratedWithOptions(sqliterepo.Options{
		Path:         dbPath,
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	if err != nil {
		return fmt.Errorf("open sqlite %s for validation: %w", dbPath, err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRowContext(ctx, `select count(*) from sqlite_schema
		where type = 'table' and name in ('settings', 'usage_events')`).Scan(&count); err != nil {
		return fmt.Errorf("validate sqlite %s: %w", dbPath, err)
	}
	if count != 2 {
		return fmt.Errorf("SQLite database at %s does not look like a CPA Manager Plus Manager Server database", dbPath)
	}
	return nil
}
