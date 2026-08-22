package cpaconnection

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	_ "modernc.org/sqlite"
)

const (
	testCPABaseURL       = "http://cpa.local:8317"
	testCPAManagementKey = "cpa-management-key"
)

func TestRunStoresFreshConnectionEncryptedWithoutLeakingSecret(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data", "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data", "data.key")
	managementKeyPath := filepath.Join(dir, "cpa-management-key")
	if err := os.WriteFile(managementKeyPath, []byte(testCPAManagementKey+"\n"), 0o600); err != nil {
		t.Fatalf("write management key: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", testCPABaseURL + "/",
		"--management-key-file", managementKeyPath,
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("store CPA connection: %v stderr=%s", err, stderr.String())
	}
	if strings.Contains(stdout.String(), testCPAManagementKey) || strings.Contains(stderr.String(), testCPAManagementKey) {
		t.Fatalf("command output leaked CPA management key: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "stored in encrypted") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if info, err := os.Stat(dataKeyPath); err != nil {
		t.Fatalf("stat data key: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("data key mode = %o", info.Mode().Perm())
	}

	requireRawSettingEncrypted(t, dbPath, "manager_config_v1")
	requireRawSettingEncrypted(t, dbPath, "setup")
	cfg, setup := loadProtectedConnections(t, dbPath, dataKeyPath)
	if cfg.CPAConnection.CPABaseURL != testCPABaseURL || cfg.CPAConnection.ManagementKey != testCPAManagementKey {
		t.Fatalf("stored manager config = %#v", cfg.CPAConnection)
	}
	if setup.CPAUpstreamURL != testCPABaseURL || setup.ManagementKey != testCPAManagementKey {
		t.Fatalf("stored setup = %#v", setup)
	}
}

func TestRunIsIdempotentAndPreservesExistingManagerSettings(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	runStoreCommand(t, dbPath, dataKeyPath, testCPABaseURL, testCPAManagementKey)

	dataKey, _, err := security.LoadOrCreateDataKey("", dataKeyPath)
	if err != nil {
		t.Fatalf("load data key: %v", err)
	}
	protector, err := security.NewProtector(dataKey)
	if err != nil {
		t.Fatalf("create protector: %v", err)
	}
	st, err := store.Open(dbPath, protector)
	if err != nil {
		t.Fatalf("open protected store: %v", err)
	}
	cfg, ok, err := st.LoadManagerConfig(context.Background())
	if err != nil || !ok {
		_ = st.Close()
		t.Fatalf("load manager config ok=%v err=%v", ok, err)
	}
	cfg.Collector.BatchSize = 321
	cfg.Collector.QueryLimit = 65432
	if err := st.SaveManagerConfig(context.Background(), cfg); err != nil {
		_ = st.Close()
		t.Fatalf("save customized manager config: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close customized store: %v", err)
	}

	runStoreCommand(t, dbPath, dataKeyPath, testCPABaseURL+"/", testCPAManagementKey)
	stored, _ := loadProtectedConnections(t, dbPath, dataKeyPath)
	if stored.Collector.BatchSize != 321 || stored.Collector.QueryLimit != 65432 {
		t.Fatalf("customized manager settings were overwritten: %#v", stored.Collector)
	}
	requireRawSettingEncrypted(t, dbPath, "manager_config_v1")
}

func TestRunMigratesSetupOnlyPlaintextHistory(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	legacy, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacy.SaveSetup(context.Background(), store.Setup{
		CPAUpstreamURL: testCPABaseURL,
		ManagementKey:  testCPAManagementKey,
		Queue:          "legacy-queue",
		PopSide:        "left",
	}); err != nil {
		_ = legacy.Close()
		t.Fatalf("save legacy setup: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}
	if raw := rawSettingValue(t, dbPath, "setup"); !strings.Contains(raw, testCPAManagementKey) {
		t.Fatalf("legacy fixture was not plaintext: %s", raw)
	}

	runStoreCommand(t, dbPath, dataKeyPath, testCPABaseURL, testCPAManagementKey)
	requireRawSettingEncrypted(t, dbPath, "setup")
	requireRawSettingEncrypted(t, dbPath, "manager_config_v1")
	managerCfg, setup := loadProtectedConnections(t, dbPath, dataKeyPath)
	if managerCfg.Collector.Queue != "legacy-queue" || managerCfg.Collector.PopSide != "left" {
		t.Fatalf("legacy collector settings were not migrated: %#v", managerCfg.Collector)
	}
	if setup.Queue != "legacy-queue" || setup.PopSide != "left" {
		t.Fatalf("legacy setup settings changed: %#v", setup)
	}
}

func TestRunAcceptsSettingsOnlyPartialSchema(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	createSQLiteDatabase(t, dbPath,
		`create table settings (key text primary key, value text not null, updated_at_ms integer not null)`,
	)

	runStoreCommand(t, dbPath, dataKeyPath, testCPABaseURL, testCPAManagementKey)
	requireRawSettingEncrypted(t, dbPath, "manager_config_v1")
	if !sqliteTableExists(t, dbPath, "usage_events") {
		t.Fatal("normal store migration did not create usage_events")
	}
}

func TestRunAcceptsPartialSchemaWhenUsageEventsMarkerRemains(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	legacy, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}
	execSQLiteStatements(t, dbPath, `drop table settings`)
	if sqliteTableExists(t, dbPath, "settings") || !sqliteTableExists(t, dbPath, "usage_events") {
		t.Fatal("partial schema fixture does not have the expected core marker")
	}

	runStoreCommand(t, dbPath, dataKeyPath, testCPABaseURL, testCPAManagementKey)
	requireRawSettingEncrypted(t, dbPath, "manager_config_v1")
}

func TestRunRejectsUnrelatedSQLiteDatabase(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "third-party.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	managementKeyPath := writeManagementKeyFile(t, dir, testCPAManagementKey)
	createSQLiteDatabase(t, dbPath,
		`create table customers (id integer primary key, name text not null)`,
	)

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", testCPABaseURL,
		"--management-key-file", managementKeyPath,
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "does not look like") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(dataKeyPath); !os.IsNotExist(statErr) {
		t.Fatalf("unrelated database unexpectedly created a data key: %v", statErr)
	}
}

func TestRunRejectsInlineManagementKey(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", testCPABaseURL,
		"--management-key", testCPAManagementKey,
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(stderr.String(), testCPAManagementKey) || strings.Contains(stdout.String(), testCPAManagementKey) {
		t.Fatalf("inline key appeared in output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
		t.Fatalf("inline-key invocation unexpectedly created database: %v", statErr)
	}
}

func TestRunRejectsEncryptedSettingsOnlyHistoryWhenDataKeyIsMissing(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "missing-data.key")
	managementKeyPath := writeManagementKeyFile(t, dir, testCPAManagementKey)
	createSQLiteDatabase(t, dbPath,
		`create table settings (key text primary key, value text not null, updated_at_ms integer not null)`,
		`insert into settings (key, value, updated_at_ms) values ('manager_config_v1', 'enc:v1:protected-history', 1)`,
	)

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", testCPABaseURL,
		"--management-key-file", managementKeyPath,
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "data key is missing") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(dataKeyPath); !os.IsNotExist(statErr) {
		t.Fatalf("missing data key was unexpectedly recreated: %v", statErr)
	}
}

func TestRunImportsMatchingLegacyEnvironmentConnection(t *testing.T) {
	clearConnectionEnvironment(t)
	t.Setenv("CPA_UPSTREAM_URL", testCPABaseURL+"/")
	t.Setenv("CPA_MANAGEMENT_KEY", testCPAManagementKey)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")

	runStoreCommand(t, dbPath, dataKeyPath, testCPABaseURL, testCPAManagementKey)
	cfg, _ := loadProtectedConnections(t, dbPath, dataKeyPath)
	if cfg.CPAConnection.CPABaseURL != testCPABaseURL || cfg.CPAConnection.ManagementKey != testCPAManagementKey {
		t.Fatalf("stored env connection = %#v", cfg.CPAConnection)
	}
}

func TestRunRejectsConflictingLegacyEnvironmentConnection(t *testing.T) {
	clearConnectionEnvironment(t)
	t.Setenv("CPA_UPSTREAM_URL", "http://other-cpa.local:8317")
	t.Setenv("CPA_MANAGEMENT_KEY", "other-key")
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	managementKeyPath := writeManagementKeyFile(t, dir, testCPAManagementKey)

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", testCPABaseURL,
		"--management-key-file", managementKeyPath,
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "environment CPA connection conflicts") {
		t.Fatalf("error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
		t.Fatalf("conflicting env import unexpectedly created a database: %v", statErr)
	}
	if _, statErr := os.Stat(dataKeyPath); !os.IsNotExist(statErr) {
		t.Fatalf("conflicting env import unexpectedly created a data key: %v", statErr)
	}
}

func TestRunRejectsConflictingExistingConnectionWithoutOverwritingIt(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	runStoreCommand(t, dbPath, dataKeyPath, testCPABaseURL, testCPAManagementKey)
	before := rawSettingValue(t, dbPath, "manager_config_v1")
	managementKeyPath := writeManagementKeyFile(t, dir, "different-key")

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", testCPABaseURL,
		"--management-key-file", managementKeyPath,
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("error = %v", err)
	}
	if stdout.Len() != 0 || strings.Contains(stderr.String(), "different-key") {
		t.Fatalf("unexpected output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if after := rawSettingValue(t, dbPath, "manager_config_v1"); after != before {
		t.Fatal("conflicting import overwrote manager_config_v1")
	}
}

func TestRunRollsBackManagerConfigWhenLegacySetupWriteFails(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	runStoreCommand(t, dbPath, dataKeyPath, testCPABaseURL, testCPAManagementKey)
	beforeManagerConfig := rawSettingValue(t, dbPath, "manager_config_v1")
	beforeSetup := rawSettingValue(t, dbPath, "setup")
	execSQLiteStatements(t, dbPath,
		`create trigger block_setup_insert before insert on settings
		 when new.key = 'setup' begin select raise(abort, 'setup write blocked'); end`,
		`create trigger block_setup_update before update on settings
		 when new.key = 'setup' begin select raise(abort, 'setup write blocked'); end`,
	)

	managementKeyPath := writeManagementKeyFile(t, dir, testCPAManagementKey)
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", testCPABaseURL,
		"--management-key-file", managementKeyPath,
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "save encrypted manager_config_v1 and legacy setup") {
		t.Fatalf("error = %v", err)
	}
	if got := rawSettingValue(t, dbPath, "manager_config_v1"); got != beforeManagerConfig {
		t.Fatal("manager_config_v1 changed despite transaction rollback")
	}
	if got := rawSettingValue(t, dbPath, "setup"); got != beforeSetup {
		t.Fatal("setup changed despite transaction rollback")
	}
}

func TestRunRejectsEncryptedHistoryWhenDataKeyIsMissing(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	runStoreCommand(t, dbPath, dataKeyPath, testCPABaseURL, testCPAManagementKey)
	managementKeyPath := writeManagementKeyFile(t, dir, testCPAManagementKey)
	if err := os.Remove(dataKeyPath); err != nil {
		t.Fatalf("remove data key: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", testCPABaseURL,
		"--management-key-file", managementKeyPath,
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "data key is missing") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(dataKeyPath); !os.IsNotExist(statErr) {
		t.Fatalf("missing data key was unexpectedly recreated: %v", statErr)
	}
}

func TestRunRejectsWrongDataKeyWithoutOverwritingEncryptedHistory(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	runStoreCommand(t, dbPath, dataKeyPath, testCPABaseURL, testCPAManagementKey)
	before := rawSettingValue(t, dbPath, "manager_config_v1")
	otherKeyPath := filepath.Join(dir, "other-data.key")
	if _, _, err := security.LoadOrCreateDataKey("", otherKeyPath); err != nil {
		t.Fatalf("create other data key: %v", err)
	}
	otherKey, err := os.ReadFile(otherKeyPath)
	if err != nil {
		t.Fatalf("read other data key: %v", err)
	}
	if err := os.WriteFile(dataKeyPath, otherKey, 0o600); err != nil {
		t.Fatalf("replace data key: %v", err)
	}
	managementKeyPath := writeManagementKeyFile(t, dir, testCPAManagementKey)

	var stdout, stderr bytes.Buffer
	err = Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", testCPABaseURL,
		"--management-key-file", managementKeyPath,
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "invalid data key") {
		t.Fatalf("error = %v", err)
	}
	if after := rawSettingValue(t, dbPath, "manager_config_v1"); after != before {
		t.Fatal("wrong-key import overwrote manager_config_v1")
	}
}

func runStoreCommand(t testing.TB, dbPath string, dataKeyPath string, baseURL string, managementKey string) {
	t.Helper()
	managementKeyPath := writeManagementKeyFile(t, filepath.Dir(dbPath), managementKey)
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", baseURL,
		"--management-key-file", managementKeyPath,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("store CPA connection: %v stderr=%s", err, stderr.String())
	}
	if strings.Contains(stdout.String(), managementKey) || strings.Contains(stderr.String(), managementKey) {
		t.Fatalf("command output leaked CPA management key: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func writeManagementKeyFile(t testing.TB, dir string, managementKey string) string {
	t.Helper()
	path := filepath.Join(dir, "cpa-management-key")
	if err := os.WriteFile(path, []byte(managementKey+"\n"), 0o600); err != nil {
		t.Fatalf("write management key: %v", err)
	}
	return path
}

func clearConnectionEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("CPA_UPSTREAM_URL", "")
	t.Setenv("CPA_MANAGEMENT_KEY", "")
	t.Setenv("CPA_MANAGEMENT_KEY_FILE", filepath.Join(t.TempDir(), "missing-management-key"))
	t.Setenv("CPA_MANAGER_DATA_KEY", "")
	t.Setenv("CPA_MANAGER_DATA_KEY_FILE", filepath.Join(t.TempDir(), "missing-data-key"))
	t.Setenv("CPA_MANAGER_CONFIG", filepath.Join(t.TempDir(), "missing-config.json"))
}

func loadProtectedConnections(t testing.TB, dbPath string, dataKeyPath string) (store.ManagerConfig, store.Setup) {
	t.Helper()
	dataKey, _, err := security.LoadOrCreateDataKey("", dataKeyPath)
	if err != nil {
		t.Fatalf("load data key: %v", err)
	}
	protector, err := security.NewProtector(dataKey)
	if err != nil {
		t.Fatalf("create protector: %v", err)
	}
	st, err := store.Open(dbPath, protector)
	if err != nil {
		t.Fatalf("open protected store: %v", err)
	}
	defer st.Close()
	cfg, ok, err := st.LoadManagerConfig(context.Background())
	if err != nil || !ok {
		t.Fatalf("load manager config ok=%v err=%v", ok, err)
	}
	setup, ok, err := st.LoadSetup(context.Background())
	if err != nil || !ok {
		t.Fatalf("load setup ok=%v err=%v", ok, err)
	}
	return cfg, setup
}

func requireRawSettingEncrypted(t testing.TB, dbPath string, key string) {
	t.Helper()
	raw := rawSettingValue(t, dbPath, key)
	if strings.Contains(raw, testCPAManagementKey) || !strings.Contains(raw, "enc:v1:") {
		t.Fatalf("%s was not encrypted: %s", key, raw)
	}
}

func rawSettingValue(t testing.TB, dbPath string, key string) string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	defer db.Close()
	var raw string
	if err := db.QueryRow(`select value from settings where key = ?`, key).Scan(&raw); err != nil {
		t.Fatalf("load raw setting %s: %v", key, err)
	}
	return raw
}

func createSQLiteDatabase(t testing.TB, dbPath string, statements ...string) {
	t.Helper()
	execSQLiteStatements(t, dbPath, statements...)
}

func execSQLiteStatements(t testing.TB, dbPath string, statements ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite fixture: %v", err)
	}
	defer db.Close()
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("execute sqlite fixture statement %q: %v", statement, err)
		}
	}
}

func sqliteTableExists(t testing.TB, dbPath string, table string) bool {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite fixture: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`select count(*) from sqlite_schema where type = 'table' and name = ?`, table).Scan(&count); err != nil {
		t.Fatalf("inspect sqlite fixture table %s: %v", table, err)
	}
	return count == 1
}

func TestRunRebuildsConflictingSetupWhenManagerMatchesInput(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	legacy, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacy.SaveManagerConfig(context.Background(), store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    testCPABaseURL,
			ManagementKey: testCPAManagementKey,
		},
		Collector: store.ManagerCollectorConfig{BatchSize: 321, QueryLimit: 65432},
	}); err != nil {
		_ = legacy.Close()
		t.Fatalf("save manager config: %v", err)
	}
	if err := legacy.SaveSetup(context.Background(), store.Setup{
		CPAUpstreamURL: "http://legacy-cpa.local:8317",
		ManagementKey:  "legacy-key",
	}); err != nil {
		_ = legacy.Close()
		t.Fatalf("save conflicting legacy setup: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	runStoreCommand(t, dbPath, dataKeyPath, testCPABaseURL, testCPAManagementKey)
	managerCfg, setup := loadProtectedConnections(t, dbPath, dataKeyPath)
	if managerCfg.Collector.BatchSize != 321 || managerCfg.Collector.QueryLimit != 65432 {
		t.Fatalf("manager collector settings were overwritten: %#v", managerCfg.Collector)
	}
	if setup.CPAUpstreamURL != testCPABaseURL || setup.ManagementKey != testCPAManagementKey {
		t.Fatalf("legacy setup did not follow manager authority = %#v", setup)
	}
	requireRawSettingEncrypted(t, dbPath, "setup")
	requireRawSettingEncrypted(t, dbPath, "manager_config_v1")
}

func TestRunIgnoresPartialSetupWhenManagerMatchesInput(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	legacy, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacy.SaveManagerConfig(context.Background(), store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    testCPABaseURL,
			ManagementKey: testCPAManagementKey,
		},
	}); err != nil {
		_ = legacy.Close()
		t.Fatalf("save manager config: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}
	execSQLiteStatements(t, dbPath,
		`insert into settings (key, value, updated_at_ms) values ('setup', '{"cpaBaseUrl":"http://stale-cpa.local:8317","queue":"stale-queue"}', 1)`,
	)

	runStoreCommand(t, dbPath, dataKeyPath, testCPABaseURL, testCPAManagementKey)
	managerCfg, setup := loadProtectedConnections(t, dbPath, dataKeyPath)
	if managerCfg.CPAConnection.ManagementKey != testCPAManagementKey {
		t.Fatalf("manager connection = %#v", managerCfg.CPAConnection)
	}
	if setup.CPAUpstreamURL != testCPABaseURL || setup.ManagementKey != testCPAManagementKey {
		t.Fatalf("partial legacy setup was not rebuilt from manager authority = %#v", setup)
	}
	requireRawSettingEncrypted(t, dbPath, "setup")
}

func TestRunRepairsPartialManagerFromMatchingSetup(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	legacy, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacy.SaveManagerConfig(context.Background(), store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{CPABaseURL: testCPABaseURL},
	}); err != nil {
		_ = legacy.Close()
		t.Fatalf("save partial manager config: %v", err)
	}
	if err := legacy.SaveSetup(context.Background(), store.Setup{
		CPAUpstreamURL: testCPABaseURL,
		ManagementKey:  testCPAManagementKey,
		Queue:          "legacy-queue",
		PopSide:        "left",
	}); err != nil {
		_ = legacy.Close()
		t.Fatalf("save matching legacy setup: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	runStoreCommand(t, dbPath, dataKeyPath, testCPABaseURL, testCPAManagementKey)
	managerCfg, setup := loadProtectedConnections(t, dbPath, dataKeyPath)
	if managerCfg.CPAConnection.CPABaseURL != testCPABaseURL || managerCfg.CPAConnection.ManagementKey != testCPAManagementKey {
		t.Fatalf("repaired manager connection = %#v", managerCfg.CPAConnection)
	}
	if managerCfg.Collector.Queue != "legacy-queue" || managerCfg.Collector.PopSide != "left" {
		t.Fatalf("legacy collector settings were not adopted: %#v", managerCfg.Collector)
	}
	if setup.ManagementKey != testCPAManagementKey {
		t.Fatalf("canonical setup = %#v", setup)
	}
}

func TestRunRejectsConflictingSetupWhenManagerPartial(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	legacy, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacy.SaveManagerConfig(context.Background(), store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{CPABaseURL: testCPABaseURL},
	}); err != nil {
		_ = legacy.Close()
		t.Fatalf("save partial manager config: %v", err)
	}
	if err := legacy.SaveSetup(context.Background(), store.Setup{
		CPAUpstreamURL: testCPABaseURL,
		ManagementKey:  "different-key",
	}); err != nil {
		_ = legacy.Close()
		t.Fatalf("save conflicting legacy setup: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	managementKeyPath := writeManagementKeyFile(t, dir, testCPAManagementKey)
	var stdout, stderr bytes.Buffer
	err = Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", testCPABaseURL,
		"--management-key-file", managementKeyPath,
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "legacy setup CPA connection conflicts") {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(err.Error(), "different-key") || strings.Contains(stderr.String(), "different-key") {
		t.Fatalf("conflict error leaked the stored key: %v %s", err, stderr.String())
	}
}

func TestRunRejectsPartialManagerURLConflict(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	legacy, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacy.SaveManagerConfig(context.Background(), store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{CPABaseURL: "http://other-cpa.local:8317"},
	}); err != nil {
		_ = legacy.Close()
		t.Fatalf("save partial manager config: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	managementKeyPath := writeManagementKeyFile(t, dir, testCPAManagementKey)
	var stdout, stderr bytes.Buffer
	err = Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", testCPABaseURL,
		"--management-key-file", managementKeyPath,
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "partial CPA connection whose URL conflicts") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunRejectsSetupOnlyConflictingConnection(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	legacy, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacy.SaveSetup(context.Background(), store.Setup{
		CPAUpstreamURL: testCPABaseURL,
		ManagementKey:  "different-key",
	}); err != nil {
		_ = legacy.Close()
		t.Fatalf("save legacy setup: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	managementKeyPath := writeManagementKeyFile(t, dir, testCPAManagementKey)
	var stdout, stderr bytes.Buffer
	err = Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", testCPABaseURL,
		"--management-key-file", managementKeyPath,
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "legacy setup CPA connection conflicts") {
		t.Fatalf("err=%v", err)
	}
	if got := rawSettingValue(t, dbPath, "setup"); !strings.Contains(got, "different-key") {
		t.Fatalf("rejected import rewrote the legacy setup row: %s", got)
	}
}
