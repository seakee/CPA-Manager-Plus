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
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
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

	if err := storeConnection(ctx, cfg, st, baseURL, managementKey, opts.RepairConflict); err != nil {
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
	RepairConflict    bool
}

func parseArgs(args []string, stderr io.Writer) (options, error) {
	var opts options
	fs := flag.NewFlagSet("store-cpa-connection", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.CPABaseURL, "cpa-base-url", "", "CPA Management API base URL")
	fs.StringVar(&opts.ManagementKeyFile, "management-key-file", "", "file containing the CPA Management Key")
	fs.StringVar(&opts.DBPath, "db-path", "", "SQLite database path; defaults to Manager Server config")
	fs.StringVar(&opts.DataKeyPath, "data-key-path", "", "data.key path; defaults to Manager Server config")
	fs.BoolVar(&opts.RepairConflict, "repair-conflict", false, "explicitly canonicalize persisted CPA connection state the resolver cannot trust (rows conflicting with each other, or authority-less partial rows conflicting with the request), using the requested connection")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: cpa-manager-plus store-cpa-connection --cpa-base-url URL --management-key-file PATH [--db-path PATH] [--data-key-path PATH] [--repair-conflict]")
		_, _ = fmt.Fprintln(stderr, "Stop Manager Server before running this offline command.")
		_, _ = fmt.Fprintln(stderr, "--repair-conflict only repairs persisted state the resolver cannot trust; a complete stored connection still requires matching input.")
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

	storageInspection, err := sqliterepo.InspectPersistedCPAConnectionStorage(ctx, dbPath)
	if err != nil {
		return databaseInspection{}, err
	}
	inspection.ProtectedConnection = storageInspection.HasEncryptedConnection
	return inspection, nil
}

// storeConnection imports the requested connection into the encrypted
// manager_config_v1 row and its legacy setup mirror in one transaction.
// Without --repair-conflict, persisted authority rules reject only state the
// shared resolver cannot trust. A complete manager_config_v1 row is authoritative
// over stale setup data; a complete setup can fill a compatible partial manager
// row. With an explicit repair, only state the resolver judged conflicting (rows
// contradicting each other, or partial rows without an authority that contradict
// the request) may be canonicalized; a complete and consistent stored connection
// still requires matching input.
func storeConnection(ctx context.Context, cfg config.Config, st *store.Store, baseURL string, managementKey string, repairConflict bool) error {
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
		managerCfg.Collector.TLSSkipVerify = false
		managerCfg.Collector.TLSSkipVerifyExplicit = false
	}
	resolution, resolveErr := managerconfig.ResolveLegacyConnectionAuthority(
		managerCfg,
		managerOK,
		setup,
		setupOK,
	)
	validateErr := error(nil)
	if resolveErr == nil {
		validateErr = resolution.ValidateRequestedLegacyConnection(input)
	}
	// Repairable persisted state is exactly what the shared resolver cannot
	// trust: rows that contradict each other, or authority-less partial rows
	// that contradict the request. A complete, consistent authority (manager
	// or setup) is never rebound through repair, with or without the flag.
	repairableConflict := resolveErr != nil ||
		(resolution.Authority == managerconfig.LegacyConnectionAuthorityNone && validateErr != nil)
	if repairableConflict && !repairConflict {
		if normalizeErr := st.NormalizeLegacyConnectionStorage(ctx, managerCfg, managerOK, setup, setupOK); normalizeErr != nil {
			return fmt.Errorf("normalize legacy CPA connection storage: %w", normalizeErr)
		}
		conflictErr := validateErr
		if resolveErr != nil {
			conflictErr = resolveErr
		}
		return fmt.Errorf("%w%s", conflictErr, managerconfig.LegacyConnectionConflictRepairHint)
	}
	if !repairableConflict && validateErr != nil {
		// The persisted authority is healthy and simply differs from the
		// request; repair does not apply to this state.
		if normalizeErr := st.NormalizeLegacyConnectionStorage(ctx, managerCfg, managerOK, setup, setupOK); normalizeErr != nil {
			return fmt.Errorf("normalize legacy CPA connection storage: %w", normalizeErr)
		}
		return fmt.Errorf("%w%s", validateErr, managerconfig.LegacyConnectionCompleteAuthorityNote)
	}
	if repairableConflict {
		// Canonicalizing conflicting history: normalize first so that even a
		// failed canonical write leaves the historical rows encrypted at
		// rest, exactly like the non-repair rejection path.
		if normalizeErr := st.NormalizeLegacyConnectionStorage(ctx, managerCfg, managerOK, setup, setupOK); normalizeErr != nil {
			return fmt.Errorf("normalize legacy CPA connection storage: %w", normalizeErr)
		}
	}
	if !managerOK {
		// A missing manager row starts from defaults, so a persisted legacy
		// setup's collector choices are the only historical values available.
		managerCfg.Collector.Queue = managerconfig.ValueOr(setup.Queue, managerCfg.Collector.Queue)
		managerCfg.Collector.PopSide = managerconfig.NormalizePopSide(setup.PopSide, managerCfg.Collector.PopSide)
	} else {
		managerconfig.MergeLegacyCollectorSettings(&managerCfg, setup, setupOK)
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

type connection = managerconfig.LegacyConnection

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

func connectionsEqual(left connection, right connection) bool {
	return managerconfig.LegacyConnectionsEqual(left, right)
}
