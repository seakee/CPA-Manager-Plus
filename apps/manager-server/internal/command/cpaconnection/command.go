package cpaconnection

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	_ "modernc.org/sqlite"
)

func Run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	opts, err := parseArgs(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	baseURL := cpa.NormalizeBaseURL(opts.CPABaseURL)
	if baseURL == "" {
		return errors.New("--cpa-base-url is required")
	}
	managementKey, err := resolveManagementKey(opts)
	if err != nil {
		return err
	}

	cfg, err := config.LoadWithoutCreatingDefault()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := validateConnection("environment", cfg.CPAUpstreamURL, cfg.ManagementKey, connection{
		BaseURL:       baseURL,
		ManagementKey: managementKey,
	}); err != nil {
		return err
	}
	dbPath := strings.TrimSpace(opts.DBPath)
	if dbPath == "" {
		dbPath = strings.TrimSpace(cfg.DBPath)
	}
	if dbPath == "" {
		return errors.New("SQLite database path is empty; pass --db-path")
	}
	dataKeyPath := strings.TrimSpace(opts.DataKeyPath)
	if dataKeyPath == "" {
		dataKeyPath = strings.TrimSpace(cfg.DataKeyPath)
	}

	databaseLock, err := processlock.Acquire(dbPath)
	if err != nil {
		return fmt.Errorf("acquire CPA connection import lock; stop Manager Server and retry: %w", err)
	}
	defer func() { _ = databaseLock.Close() }()
	dbPath = databaseLock.DatabasePath()

	inspection, err := inspectExistingDatabase(ctx, dbPath)
	if err != nil {
		return err
	}
	if inspection.ProtectedConnection && strings.TrimSpace(cfg.DataKey) == "" {
		if dataKeyPath == "" {
			return errors.New("encrypted CPA connection exists but no data key was configured")
		}
		if _, err := os.Stat(dataKeyPath); err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("encrypted CPA connection exists but data key is missing at %s", dataKeyPath)
			}
			return fmt.Errorf("stat data key %s: %w", dataKeyPath, err)
		}
	}

	dataKey, _, err := security.LoadOrCreateDataKey(cfg.DataKey, dataKeyPath)
	if err != nil {
		return fmt.Errorf("load data key: %w", err)
	}
	protector, err := security.NewProtector(dataKey)
	if err != nil {
		return fmt.Errorf("initialize secret protector: %w", err)
	}
	st, err := store.Open(dbPath, protector)
	if err != nil {
		return fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}
	defer st.Close()

	if err := storeConnection(ctx, cfg, st, baseURL, managementKey); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "CPA connection stored in encrypted Manager Server configuration.")
	return nil
}

type options struct {
	CPABaseURL        string
	ManagementKeyFile string
	DBPath            string
	DataKeyPath       string
}

func parseArgs(args []string, stderr io.Writer) (options, error) {
	var opts options
	fs := flag.NewFlagSet("store-cpa-connection", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.CPABaseURL, "cpa-base-url", "", "CPA Management API base URL")
	fs.StringVar(&opts.ManagementKeyFile, "management-key-file", "", "file containing the CPA Management Key")
	fs.StringVar(&opts.DBPath, "db-path", "", "SQLite database path; defaults to Manager Server config")
	fs.StringVar(&opts.DataKeyPath, "data-key-path", "", "data.key path; defaults to Manager Server config")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: cpa-manager-plus store-cpa-connection --cpa-base-url URL --management-key-file PATH [--db-path PATH] [--data-key-path PATH]")
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

func resolveManagementKey(opts options) (string, error) {
	if strings.TrimSpace(opts.ManagementKeyFile) == "" {
		return "", errors.New("--management-key-file is required; pass the CPA Management Key through a file")
	}
	data, err := os.ReadFile(opts.ManagementKeyFile)
	if err != nil {
		return "", fmt.Errorf("read CPA management key file %s: %w", opts.ManagementKeyFile, err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", errors.New("CPA management key file is empty")
	}
	return key, nil
}

type databaseInspection struct {
	ProtectedConnection bool
}

func inspectExistingDatabase(ctx context.Context, dbPath string) (databaseInspection, error) {
	info, err := os.Stat(dbPath)
	if err != nil {
		if os.IsNotExist(err) {
			return databaseInspection{}, nil
		}
		return databaseInspection{}, fmt.Errorf("stat sqlite %s: %w", dbPath, err)
	}
	if info.IsDir() {
		return databaseInspection{}, fmt.Errorf("SQLite database path is a directory: %s", dbPath)
	}
	if info.Size() == 0 {
		return databaseInspection{}, nil
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return databaseInspection{}, fmt.Errorf("open sqlite %s for validation: %w", dbPath, err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `select name from sqlite_schema
		where type = 'table' and name in ('settings', 'usage_events')`)
	if err != nil {
		return databaseInspection{}, fmt.Errorf("validate sqlite %s: %w", dbPath, err)
	}
	hasSettings := false
	hasUsageEvents := false
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return databaseInspection{}, fmt.Errorf("validate sqlite %s: %w", dbPath, err)
		}
		switch name {
		case "settings":
			hasSettings = true
		case "usage_events":
			hasUsageEvents = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return databaseInspection{}, fmt.Errorf("validate sqlite %s: %w", dbPath, err)
	}
	if err := rows.Close(); err != nil {
		return databaseInspection{}, fmt.Errorf("validate sqlite %s: %w", dbPath, err)
	}
	if !hasSettings && !hasUsageEvents {
		return databaseInspection{}, fmt.Errorf("SQLite database at %s does not look like a CPA Manager Plus Manager Server database", dbPath)
	}

	inspection := databaseInspection{}
	if !hasSettings {
		return inspection, nil
	}

	rows, err = db.QueryContext(ctx, `select value from settings where key in ('setup', 'manager_config_v1')`)
	if err != nil {
		return databaseInspection{}, fmt.Errorf("inspect encrypted CPA connection in %s: %w", dbPath, err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return databaseInspection{}, err
		}
		if strings.Contains(raw, "enc:v1:") {
			inspection.ProtectedConnection = true
		}
	}
	if err := rows.Err(); err != nil {
		return databaseInspection{}, err
	}
	return inspection, nil
}

func storeConnection(ctx context.Context, cfg config.Config, st *store.Store, baseURL string, managementKey string) error {
	input := connection{BaseURL: baseURL, ManagementKey: managementKey}
	if err := validateConnection("environment", cfg.CPAUpstreamURL, cfg.ManagementKey, input); err != nil {
		return err
	}

	managerCfg, managerOK, err := st.LoadManagerConfig(ctx)
	if err != nil {
		return fmt.Errorf("load manager_config_v1: %w", err)
	}
	setup, setupOK, err := st.LoadSetup(ctx)
	if err != nil {
		return fmt.Errorf("load legacy setup: %w", err)
	}
	if !managerOK {
		managerCfg = managerconfig.New(cfg, st, nil).DefaultManagerConfig()
	}
	setupUsable := setupOK && managerconfig.SetupConnectionComplete(setup)

	// Authority rule, shared with the bootstrap migration: a complete
	// manager_config_v1 is authoritative and a stale or partial legacy setup
	// row is rewritten from it instead of failing the import; when the manager
	// config is incomplete, a complete legacy setup is the authority; a
	// conflicting authoritative connection is never silently overwritten.
	if managerOK && managerconfig.ConnectionComplete(managerCfg) {
		if err := validateAuthorityConnection(
			"manager_config_v1",
			managerCfg.CPAConnection.CPABaseURL,
			managerCfg.CPAConnection.ManagementKey,
			input,
		); err != nil {
			return err
		}
		managerconfig.MergeLegacyCollectorSettings(&managerCfg, setup, setupUsable)
	} else {
		if managerOK {
			if err := validateAuthorityConnection(
				"manager_config_v1",
				managerCfg.CPAConnection.CPABaseURL,
				managerCfg.CPAConnection.ManagementKey,
				input,
			); err != nil {
				return err
			}
		}
		if setupOK {
			if err := validateAuthorityConnection(
				"legacy setup",
				setup.CPAUpstreamURL,
				setup.ManagementKey,
				input,
			); err != nil {
				return err
			}
			if setupUsable {
				managerCfg.Collector.Queue = managerconfig.ValueOr(setup.Queue, managerCfg.Collector.Queue)
				managerCfg.Collector.PopSide = managerconfig.NormalizePopSide(setup.PopSide, managerCfg.Collector.PopSide)
			}
		}
	}

	managerCfg.CPAConnection.CPABaseURL = input.BaseURL
	managerCfg.CPAConnection.ManagementKey = input.ManagementKey

	nextSetup := managerconfig.SetupFromManagerConfig(managerCfg)
	if err := st.SaveManagerConfigAndSetup(ctx, managerCfg, nextSetup); err != nil {
		return fmt.Errorf("save encrypted manager_config_v1 and legacy setup: %w", err)
	}

	verified, ok, err := st.LoadManagerConfig(ctx)
	if err != nil {
		return fmt.Errorf("verify encrypted manager_config_v1: %w", err)
	}
	if !ok || !connectionsEqual(input, connection{
		BaseURL:       verified.CPAConnection.CPABaseURL,
		ManagementKey: verified.CPAConnection.ManagementKey,
	}) {
		return errors.New("verify encrypted manager_config_v1: stored CPA connection does not match input")
	}
	return nil
}

type connection struct {
	BaseURL       string
	ManagementKey string
}

// validateConnection guards an unrepairable connection source (the resolved
// environment): any partial state is refused outright.
func validateConnection(source string, rawBaseURL string, rawManagementKey string, input connection) error {
	existing := connection{
		BaseURL:       cpa.NormalizeBaseURL(rawBaseURL),
		ManagementKey: strings.TrimSpace(rawManagementKey),
	}
	if existing.BaseURL == "" && existing.ManagementKey == "" {
		return nil
	}
	if existing.BaseURL == "" || existing.ManagementKey == "" {
		return fmt.Errorf("%s contains a partial CPA connection; refusing to overwrite it", source)
	}
	if !connectionsEqual(existing, input) {
		return fmt.Errorf("%s CPA connection conflicts with the requested connection", source)
	}
	return nil
}

// validateAuthorityConnection guards a persisted connection source that the
// import may repair. A complete connection must equal the input; a partial
// connection is tolerated only when its present side matches the input, so
// the import completes the connection instead of rebinding it.
func validateAuthorityConnection(source string, rawBaseURL string, rawManagementKey string, input connection) error {
	existing := connection{
		BaseURL:       cpa.NormalizeBaseURL(rawBaseURL),
		ManagementKey: strings.TrimSpace(rawManagementKey),
	}
	switch {
	case existing.BaseURL == "" && existing.ManagementKey == "":
		return nil
	case existing.BaseURL != "" && existing.ManagementKey != "":
		if !connectionsEqual(existing, input) {
			return fmt.Errorf("%s CPA connection conflicts with the requested connection", source)
		}
		return nil
	case existing.BaseURL != "":
		if existing.BaseURL != input.BaseURL {
			return fmt.Errorf("%s contains a partial CPA connection whose URL conflicts with the requested connection", source)
		}
		return nil
	default:
		if !security.EqualHMAC(existing.ManagementKey, input.ManagementKey) {
			return fmt.Errorf("%s contains a partial CPA connection whose key conflicts with the requested connection", source)
		}
		return nil
	}
}

func connectionsEqual(left connection, right connection) bool {
	return cpa.NormalizeBaseURL(left.BaseURL) == cpa.NormalizeBaseURL(right.BaseURL) &&
		security.EqualHMAC(strings.TrimSpace(left.ManagementKey), strings.TrimSpace(right.ManagementKey))
}
