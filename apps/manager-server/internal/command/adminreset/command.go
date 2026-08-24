package adminreset

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
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func Run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	opts, err := parseArgs(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	adminKey, err := resolveAdminKey(opts)
	if err != nil {
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
		return fmt.Errorf("acquire admin reset lock; stop Manager Server and retry: %w", err)
	}
	defer func() { _ = databaseLock.Close() }()
	databaseOptions.Path = databaseLock.DatabasePath()
	if err := ensureManagerDB(ctx, databaseOptions.Path); err != nil {
		return err
	}

	st, err := store.OpenWithOptions(databaseOptions)
	if err != nil {
		return fmt.Errorf("open sqlite %s: %w", databaseOptions.Path, err)
	}
	defer st.Close()

	result, err := adminauthsvc.ResetAdminKey(ctx, st, adminKey)
	if err != nil {
		return fmt.Errorf("reset admin key: %w", err)
	}

	_, _ = fmt.Fprintln(stdout, "CPA Manager Plus admin key reset.")
	if result.Generated {
		_, _ = fmt.Fprintf(stdout, "New admin key: %s\n", result.AdminKey)
		_, _ = fmt.Fprintln(stdout, "Save this value now. It will not be shown again.")
	} else {
		_, _ = fmt.Fprintln(stdout, "Use the provided admin key to log in after restarting Manager Server.")
	}
	return nil
}

type options struct {
	AdminKey     string
	AdminKeyFile string
	DBPath       string
}

func parseArgs(args []string, stderr io.Writer) (options, error) {
	var opts options
	fs := flag.NewFlagSet("reset-admin-key", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.AdminKey, "admin-key", "", "new admin key; omitted to generate a random key")
	fs.StringVar(&opts.AdminKeyFile, "admin-key-file", "", "file containing the new admin key")
	fs.StringVar(&opts.DBPath, "db-path", "", "SQLite database path; defaults to Manager Server config")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: cpa-manager-plus reset-admin-key [--db-path PATH] [--admin-key VALUE | --admin-key-file PATH]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if strings.TrimSpace(opts.AdminKey) != "" && strings.TrimSpace(opts.AdminKeyFile) != "" {
		return options{}, errors.New("--admin-key and --admin-key-file cannot be used together")
	}
	return opts, nil
}

func resolveAdminKey(opts options) (string, error) {
	if strings.TrimSpace(opts.AdminKeyFile) == "" {
		return strings.TrimSpace(opts.AdminKey), nil
	}
	data, err := os.ReadFile(opts.AdminKeyFile)
	if err != nil {
		return "", fmt.Errorf("read admin key file %s: %w", opts.AdminKeyFile, err)
	}
	adminKey := strings.TrimSpace(string(data))
	if adminKey == "" {
		return "", errors.New("admin key file is empty")
	}
	return adminKey, nil
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
			return fmt.Errorf("SQLite database not found at %s; pass --db-path or run the command from the configured Manager Server environment", dbPath)
		}
		return fmt.Errorf("stat sqlite %s: %w", dbPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("SQLite database path is a directory: %s", dbPath)
	}
	if info.Size() == 0 {
		return fmt.Errorf("SQLite database at %s is empty; verify --db-path points to the Manager Server data file", dbPath)
	}
	return nil
}

func ensureManagerDB(ctx context.Context, dbPath string) error {
	if err := validateDatabaseFile(dbPath); err != nil {
		return err
	}
	db, err := sqliterepo.OpenUnmigratedWithOptions(sqliterepo.Options{
		Path:         dbPath,
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	if err != nil {
		return fmt.Errorf("open sqlite %s for validation: %w", dbPath, err)
	}
	defer db.Close()

	rows, err := db.QueryContext(
		ctx,
		`select name from sqlite_schema where type = 'table' and name in ('settings', 'usage_events')`,
	)
	if err != nil {
		return fmt.Errorf("validate sqlite %s: %w", dbPath, err)
	}
	defer rows.Close()

	found := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("validate sqlite %s: %w", dbPath, err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("validate sqlite %s: %w", dbPath, err)
	}
	if !found["settings"] || !found["usage_events"] {
		return fmt.Errorf("SQLite database at %s does not look like a CPA Manager Plus Manager Server database", dbPath)
	}
	return nil
}
