package cpaconnection

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
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
	if revision := rawSettingValue(t, dbPath, "cpa_connection_revision_v1"); len(revision) != 32 {
		t.Fatal("offline import did not commit an opaque revision")
	}
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
	beforeRevision := rawSettingValue(t, dbPath, "cpa_connection_revision_v1")

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
	if after := rawSettingValue(t, dbPath, "cpa_connection_revision_v1"); after != beforeRevision {
		t.Fatal("idempotent offline import after store restart rotated revision")
	}
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
	otherProtector, err := security.NewProtector([]byte("abcdef0123456789abcdef0123456789"))
	if err != nil {
		t.Fatalf("create fixture protector: %v", err)
	}
	protectedKey, err := otherProtector.ProtectString("protected-history")
	if err != nil {
		t.Fatalf("protect fixture key: %v", err)
	}
	managerConfigJSON := `{"cpaConnection":{"cpaBaseUrl":"http://cpa.local:8317","managementKey":"` + protectedKey + `"}}`
	createSQLiteDatabase(t, dbPath,
		`create table settings (key text primary key, value text not null, updated_at_ms integer not null)`,
		`insert into settings (key, value, updated_at_ms) values ('bootstrap_state_v1', '{"connectionStorageMigrationVersion":2}', 1)`,
		`insert into settings (key, value, updated_at_ms) values ('manager_config_v1', '`+managerConfigJSON+`', 1)`,
	)

	var stdout, stderr bytes.Buffer
	err = Run(context.Background(), []string{
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

func TestRunRejectsInvalidPostV2CPAConnectionBeforeCreatingDataKey(t *testing.T) {
	tests := []struct {
		name          string
		settingKey    string
		managementKey string
	}{
		{name: "setup malformed envelope", settingKey: "setup", managementKey: "enc:v1:broken"},
		{name: "manager config malformed envelope", settingKey: "manager_config_v1", managementKey: "enc:v1:broken"},
		{name: "setup plaintext", settingKey: "setup", managementKey: "plain-old-key"},
		{name: "manager config plaintext", settingKey: "manager_config_v1", managementKey: "plain-old-key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConnectionEnvironment(t)
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "usage.sqlite")
			dataKeyPath := filepath.Join(dir, "missing-data.key")
			managementKeyPath := writeManagementKeyFile(t, dir, testCPAManagementKey)
			var rawConnection string
			if tt.settingKey == "setup" {
				rawConnection = `{"cpaBaseUrl":"http://cpa.local:8317","managementKey":"` + tt.managementKey + `"}`
			} else {
				rawConnection = `{"cpaConnection":{"cpaBaseUrl":"http://cpa.local:8317","managementKey":"` + tt.managementKey + `"}}`
			}
			createSQLiteDatabase(t, dbPath,
				`create table settings (key text primary key, value text not null, updated_at_ms integer not null)`,
				`insert into settings (key, value, updated_at_ms) values ('bootstrap_state_v1', '{"connectionStorageMigrationVersion":2}', 1)`,
				"insert into settings (key, value, updated_at_ms) values ('"+tt.settingKey+"', '"+rawConnection+"', 1)",
			)

			var stdout, stderr bytes.Buffer
			err := Run(context.Background(), []string{
				"--db-path", dbPath,
				"--data-key-path", dataKeyPath,
				"--cpa-base-url", testCPABaseURL,
				"--management-key-file", managementKeyPath,
			}, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), "corrupted persisted CPA connection") {
				t.Fatalf("error = %v, want corrupted persisted CPA connection", err)
			}
			if _, statErr := os.Stat(dataKeyPath); !os.IsNotExist(statErr) {
				t.Fatalf("invalid post-v2 connection unexpectedly created data key: %v", statErr)
			}
		})
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
	requireRawSettingEncrypted(t, dbPath, "manager_config_v1")
	stored, _ := loadProtectedConnections(t, dbPath, dataKeyPath)
	if stored.CPAConnection.ManagementKey != testCPAManagementKey {
		t.Fatalf("conflicting import changed manager connection: %#v", stored.CPAConnection)
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
	beforeRevision := rawSettingValue(t, dbPath, "cpa_connection_revision_v1")
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
	if got := rawSettingValue(t, dbPath, "cpa_connection_revision_v1"); got != beforeRevision {
		t.Fatal("revision changed despite offline import rollback")
	}
}

func TestRunRejectsEncryptedHistoryWhenDataKeyIsMissing(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	runStoreCommand(t, dbPath, dataKeyPath, testCPABaseURL, testCPAManagementKey)
	writeRawSettingValue(t, dbPath, "bootstrap_state_v1", `{"connectionStorageMigrationVersion":2}`)
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
	writeRawSettingValue(t, dbPath, "bootstrap_state_v1", `{"connectionStorageMigrationVersion":2}`)
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

func writeRawSettingValue(t testing.TB, dbPath string, key string, value string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`insert into settings (key, value, updated_at_ms) values (?, ?, 1)
		on conflict(key) do update set value = excluded.value`, key, value); err != nil {
		t.Fatalf("write raw setting %s: %v", key, err)
	}
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
	requireRawSettingEncrypted(t, dbPath, "setup")
	if got := rawSettingValue(t, dbPath, "setup"); strings.Contains(got, "different-key") {
		t.Fatalf("rejected import retained plaintext setup key: %s", got)
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
	requireRawSettingEncrypted(t, dbPath, "setup")
	if got := rawSettingValue(t, dbPath, "setup"); strings.Contains(got, "different-key") {
		t.Fatalf("rejected import retained plaintext setup key: %s", got)
	}
}

// seedConflictingPartialManagerAndSetup writes the historical conflict state
// for repair tests: a partial manager row that contradicts a complete legacy
// setup row, plus an unrelated settings row and collector choices that must
// survive any repair.
func seedConflictingPartialManagerAndSetup(t *testing.T, dbPath string, managerURL string, setupURL string, setupKey string) {
	t.Helper()
	legacy, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacy.SaveManagerConfig(context.Background(), store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{CPABaseURL: managerURL},
		Collector:     store.ManagerCollectorConfig{Queue: "manager-queue", PopSide: "right"},
	}); err != nil {
		_ = legacy.Close()
		t.Fatalf("save partial manager config: %v", err)
	}
	if err := legacy.SaveSetup(context.Background(), store.Setup{
		CPAUpstreamURL: setupURL,
		ManagementKey:  setupKey,
		Queue:          "legacy-queue",
	}); err != nil {
		_ = legacy.Close()
		t.Fatalf("save conflicting setup: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}
	execSQLiteStatements(t, dbPath,
		`insert into settings (key, value, updated_at_ms) values ('unrelated_setting', 'keep-me', 1)`)
}

func TestRunRepairConflictCanonicalizesConflictingManagerAndSetup(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	seedConflictingPartialManagerAndSetup(t, dbPath,
		"http://cpa-a.local:8317", "http://cpa-b.local:8317", "setup-key-b")

	managementKeyPath := writeManagementKeyFile(t, dir, "repair-key-c")
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", "http://cpa-c.local:8317",
		"--management-key-file", managementKeyPath,
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "legacy setup CPA connection conflicts") {
		t.Fatalf("normal import err=%v", err)
	}
	if !strings.Contains(err.Error(), "--repair-conflict") {
		t.Fatalf("conflict error lacks repair guidance: %v", err)
	}
	if strings.Contains(err.Error(), "setup-key-b") || strings.Contains(stderr.String(), "setup-key-b") {
		t.Fatalf("conflict error leaked stored key: %v %s", err, stderr.String())
	}
	// The rejected import must not rewrite authority: both rows keep their
	// historical values (only re-encrypted in place).
	stored, storedSetup := loadProtectedConnections(t, dbPath, dataKeyPath)
	if stored.CPAConnection.CPABaseURL != "http://cpa-a.local:8317" || stored.CPAConnection.ManagementKey != "" {
		t.Fatalf("rejected import changed manager connection: %#v", stored.CPAConnection)
	}
	if storedSetup.CPAUpstreamURL != "http://cpa-b.local:8317" || storedSetup.ManagementKey != "setup-key-b" {
		t.Fatalf("rejected import changed legacy setup: %#v", storedSetup)
	}

	stderr.Reset()
	stdout.Reset()
	if err := Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", "http://cpa-c.local:8317",
		"--management-key-file", managementKeyPath,
		"--repair-conflict",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("repair import: %v stderr=%s", err, stderr.String())
	}
	if strings.Contains(stdout.String(), "repair-key-c") || strings.Contains(stderr.String(), "repair-key-c") {
		t.Fatalf("repair output leaked key: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	cfg, setup := loadProtectedConnections(t, dbPath, dataKeyPath)
	if cfg.CPAConnection.CPABaseURL != "http://cpa-c.local:8317" || cfg.CPAConnection.ManagementKey != "repair-key-c" {
		t.Fatalf("repaired manager config = %#v", cfg.CPAConnection)
	}
	if setup.CPAUpstreamURL != "http://cpa-c.local:8317" || setup.ManagementKey != "repair-key-c" {
		t.Fatalf("repaired setup mirror = %#v", setup)
	}
	if cfg.Collector.Queue != "manager-queue" || cfg.Collector.PopSide != "right" {
		t.Fatalf("repair lost collector settings: %#v", cfg.Collector)
	}
	if setup.Queue != "manager-queue" {
		t.Fatalf("repair setup mirror lost collector settings: %#v", setup)
	}
	for _, key := range []string{"manager_config_v1", "setup"} {
		if raw := rawSettingValue(t, dbPath, key); strings.Contains(raw, "repair-key-c") || !strings.Contains(raw, "enc:v1:") {
			t.Fatalf("%s not encrypted after repair: %s", key, raw)
		}
	}
	if got := rawSettingValue(t, dbPath, "unrelated_setting"); got != "keep-me" {
		t.Fatalf("unrelated setting changed by repair: %s", got)
	}
}

func TestRunRepairConflictCanonicalizesConflictingManagerKey(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	legacy, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacy.SaveManagerConfig(context.Background(), store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{ManagementKey: "manager-key-a"},
	}); err != nil {
		_ = legacy.Close()
		t.Fatalf("save partial manager config: %v", err)
	}
	if err := legacy.SaveSetup(context.Background(), store.Setup{
		CPAUpstreamURL: "http://cpa-b.local:8317",
		ManagementKey:  "setup-key-b",
	}); err != nil {
		_ = legacy.Close()
		t.Fatalf("save conflicting setup: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	managementKeyPath := writeManagementKeyFile(t, dir, "repair-key-c")
	var stdout, stderr bytes.Buffer
	err = Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", "http://cpa-c.local:8317",
		"--management-key-file", managementKeyPath,
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "conflicts with the present manager_config_v1 key") {
		t.Fatalf("normal import err=%v", err)
	}
	if !strings.Contains(err.Error(), "--repair-conflict") {
		t.Fatalf("conflict error lacks repair guidance: %v", err)
	}

	stderr.Reset()
	stdout.Reset()
	if err := Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", "http://cpa-c.local:8317",
		"--management-key-file", managementKeyPath,
		"--repair-conflict",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("repair import: %v stderr=%s", err, stderr.String())
	}
	cfg, setup := loadProtectedConnections(t, dbPath, dataKeyPath)
	if cfg.CPAConnection.CPABaseURL != "http://cpa-c.local:8317" || cfg.CPAConnection.ManagementKey != "repair-key-c" {
		t.Fatalf("repaired manager config = %#v", cfg.CPAConnection)
	}
	if setup.CPAUpstreamURL != "http://cpa-c.local:8317" || setup.ManagementKey != "repair-key-c" {
		t.Fatalf("repaired setup mirror = %#v", setup)
	}
}

func TestRunRepairConflictCanonicalizesPartialRowsWithoutAuthority(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	legacy, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacy.SaveManagerConfig(context.Background(), store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{CPABaseURL: "http://cpa-a.local:8317"},
	}); err != nil {
		_ = legacy.Close()
		t.Fatalf("save partial manager config: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}
	execSQLiteStatements(t, dbPath,
		`insert into settings (key, value, updated_at_ms) values ('setup', '{"cpaBaseUrl":"http://cpa-b.local:8317"}', 1)`)

	managementKeyPath := writeManagementKeyFile(t, dir, "repair-key-c")
	var stdout, stderr bytes.Buffer
	err = Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", "http://cpa-c.local:8317",
		"--management-key-file", managementKeyPath,
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "partial CPA connection whose URL conflicts") {
		t.Fatalf("normal import err=%v", err)
	}

	stderr.Reset()
	stdout.Reset()
	if err := Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", "http://cpa-c.local:8317",
		"--management-key-file", managementKeyPath,
		"--repair-conflict",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("repair import: %v stderr=%s", err, stderr.String())
	}
	cfg, setup := loadProtectedConnections(t, dbPath, dataKeyPath)
	if cfg.CPAConnection.CPABaseURL != "http://cpa-c.local:8317" || cfg.CPAConnection.ManagementKey != "repair-key-c" {
		t.Fatalf("repaired manager config = %#v", cfg.CPAConnection)
	}
	if setup.CPAUpstreamURL != "http://cpa-c.local:8317" || setup.ManagementKey != "repair-key-c" {
		t.Fatalf("repaired setup mirror = %#v", setup)
	}
}

func TestRunRepairConflictDoesNotRebindCompleteManagerConnection(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	runStoreCommand(t, dbPath, dataKeyPath, testCPABaseURL, testCPAManagementKey)
	// A stale legacy setup conflicting with the complete stored manager row
	// must not turn the healthy authority into repairable state.
	st, err := openProtectedStoreForTest(t, dbPath, dataKeyPath)
	if err != nil {
		t.Fatalf("open protected store: %v", err)
	}
	if err := st.SaveSetup(context.Background(), store.Setup{
		CPAUpstreamURL: "http://stale-cpa.local:8317",
		ManagementKey:  "stale-key",
	}); err != nil {
		t.Fatalf("save stale setup: %v", err)
	}
	_ = st.Close()

	managementKeyPath := writeManagementKeyFile(t, dir, "rebind-key")
	var stdout, stderr bytes.Buffer
	err = Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", "http://rebound-cpa.local:8317",
		"--management-key-file", managementKeyPath,
		"--repair-conflict",
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "manager_config_v1 CPA connection conflicts") {
		t.Fatalf("repair rebinding err=%v", err)
	}
	if !strings.Contains(err.Error(), "complete and consistent") {
		t.Fatalf("repair rebinding error lacks scope note: %v", err)
	}
	stored, _ := loadProtectedConnections(t, dbPath, dataKeyPath)
	if stored.CPAConnection.CPABaseURL != testCPABaseURL || stored.CPAConnection.ManagementKey != testCPAManagementKey {
		t.Fatalf("repair rebinding changed manager connection: %#v", stored.CPAConnection)
	}

	// Explicitly repairing with the matching connection still canonicalizes
	// the stale setup mirror, exactly like the normal import path.
	stderr.Reset()
	stdout.Reset()
	if err := Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", testCPABaseURL,
		"--management-key-file", managementKeyPath,
		"--repair-conflict",
	}, &stdout, &stderr); err == nil {
		t.Fatal("repair accepted a mismatched key for a complete connection")
	}
	managementKeyPath = writeManagementKeyFile(t, dir, testCPAManagementKey)
	if err := Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", testCPABaseURL,
		"--management-key-file", managementKeyPath,
		"--repair-conflict",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("matching repair import: %v stderr=%s", err, stderr.String())
	}
	_, setup := loadProtectedConnections(t, dbPath, dataKeyPath)
	if setup.CPAUpstreamURL != testCPABaseURL || setup.ManagementKey != testCPAManagementKey {
		t.Fatalf("canonical setup mirror = %#v", setup)
	}
}

func TestRunRepairConflictRollsBackWhenSetupWriteFails(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	seedConflictingPartialManagerAndSetup(t, dbPath,
		"http://cpa-a.local:8317", "http://cpa-b.local:8317", "setup-key-b")
	// The repair normalizes before its canonical write, so the gate allows the
	// first setup write (at-rest normalization) and aborts the second one
	// (the canonical replacement) to exercise the save-failure rollback.
	execSQLiteStatements(t, dbPath,
		`create table setup_write_gate (n integer not null)`,
		`insert into setup_write_gate (n) values (0)`,
		`create trigger block_second_setup_write before insert on settings
		 when new.key = 'setup' begin
		   update setup_write_gate set n = n + 1;
		   select case when (select n from setup_write_gate) >= 2
		     then raise(abort, 'setup write blocked') end;
		 end`,
	)

	managementKeyPath := writeManagementKeyFile(t, dir, "repair-key-c")
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", "http://cpa-c.local:8317",
		"--management-key-file", managementKeyPath,
		"--repair-conflict",
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "save encrypted manager_config_v1 and legacy setup") {
		t.Fatalf("failing repair err=%v", err)
	}
	// The normalization before the canonical write committed, so the setup row
	// (the only row holding a key) is encrypted at rest despite the failed
	// repair...
	if raw := rawSettingValue(t, dbPath, "setup"); strings.Contains(raw, "setup-key-b") || !strings.Contains(raw, "enc:v1:") {
		t.Fatalf("setup was not normalized before the failed repair: %s", raw)
	}
	// ...the key-less partial manager row simply keeps its original value and
	// never receives the attempted canonical key.
	if raw := rawSettingValue(t, dbPath, "manager_config_v1"); strings.Contains(raw, "repair-key-c") || strings.Contains(raw, "http://cpa-c.local:8317") {
		t.Fatalf("manager_config_v1 leaked the failed canonical write: %s", raw)
	}
	// ...while the canonical transaction rolls back completely: the values
	// keep their historical sides instead of a half-repaired state.
	stored, storedSetup := loadProtectedConnections(t, dbPath, dataKeyPath)
	if stored.CPAConnection.CPABaseURL != "http://cpa-a.local:8317" || stored.CPAConnection.ManagementKey != "" {
		t.Fatalf("failed repair changed manager connection: %#v", stored.CPAConnection)
	}
	if storedSetup.CPAUpstreamURL != "http://cpa-b.local:8317" || storedSetup.ManagementKey != "setup-key-b" {
		t.Fatalf("failed repair changed legacy setup: %#v", storedSetup)
	}
	if got := rawSettingValue(t, dbPath, "unrelated_setting"); got != "keep-me" {
		t.Fatalf("unrelated setting changed by failed repair: %s", got)
	}
}

func TestRunRepairConflictRejectsMissingKeyFileWithoutTouchingState(t *testing.T) {
	clearConnectionEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	seedConflictingPartialManagerAndSetup(t, dbPath,
		"http://cpa-a.local:8317", "http://cpa-b.local:8317", "setup-key-b")
	beforeManagerConfig := rawSettingValue(t, dbPath, "manager_config_v1")
	beforeSetup := rawSettingValue(t, dbPath, "setup")

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", "http://cpa-c.local:8317",
		"--management-key-file", filepath.Join(dir, "missing-management-key"),
		"--repair-conflict",
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "read CPA management key file") {
		t.Fatalf("missing key file err=%v", err)
	}
	if got := rawSettingValue(t, dbPath, "manager_config_v1"); got != beforeManagerConfig {
		t.Fatal("manager_config_v1 changed despite rejected repair")
	}
	if got := rawSettingValue(t, dbPath, "setup"); got != beforeSetup {
		t.Fatal("setup changed despite rejected repair")
	}
}

func TestRunPreservesImplicitTLSSkipVerifyOnSetupOnlyAndSparseRows(t *testing.T) {
	t.Run("setup-only 1.x row with USAGE_RESP_TLS_SKIP_VERIFY=true stays sparse", func(t *testing.T) {
		clearConnectionEnvironment(t)
		t.Setenv("USAGE_RESP_TLS_SKIP_VERIFY", "true")
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
			Queue:          "legacy-q",
			PopSide:        "left",
		}); err != nil {
			_ = legacy.Close()
			t.Fatalf("seed legacy setup: %v", err)
		}
		if err := legacy.Close(); err != nil {
			t.Fatalf("close legacy store: %v", err)
		}

		runStoreCommand(t, dbPath, dataKeyPath, testCPABaseURL, testCPAManagementKey)
		raw := rawSettingValue(t, dbPath, "manager_config_v1")
		if strings.Contains(raw, `"tlsSkipVerify"`) {
			t.Fatalf("store-cpa-connection materialized tlsSkipVerify on setup-only DB: %s", raw)
		}
		stored, _ := loadProtectedConnections(t, dbPath, dataKeyPath)
		if stored.Collector.TLSSkipVerifyExplicit {
			t.Fatalf("stored Collector.TLSSkipVerifyExplicit = true, want false: %#v", stored.Collector)
		}
		rtCfg := collectorservice.RuntimeConfigFromManagerConfigWithFallback(stored, config.Config{TLSSkipVerify: true})
		if !rtCfg.TLSSkipVerify || rtCfg.Queue != "legacy-q" {
			t.Fatalf("RuntimeConfigFromManagerConfigWithFallback = %#v, want TLSSkipVerify=true Queue=legacy-q", rtCfg)
		}
	})

	t.Run("sparse manager_config_v1 without tlsSkipVerify stays sparse across handoff", func(t *testing.T) {
		clearConnectionEnvironment(t)
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "usage.sqlite")
		dataKeyPath := filepath.Join(dir, "data.key")

		st, err := openProtectedStoreForTest(t, dbPath, dataKeyPath)
		if err != nil {
			t.Fatalf("open protected store: %v", err)
		}
		queue := "sparse-queue"
		if _, err := st.PatchManagerCollectorConfig(context.Background(), store.ManagerCollectorPatch{Queue: &queue}); err != nil {
			_ = st.Close()
			t.Fatalf("seed sparse manager_config_v1: %v", err)
		}
		if err := st.Close(); err != nil {
			t.Fatalf("close protected store: %v", err)
		}

		runStoreCommand(t, dbPath, dataKeyPath, testCPABaseURL, testCPAManagementKey)
		raw := rawSettingValue(t, dbPath, "manager_config_v1")
		if strings.Contains(raw, `"tlsSkipVerify"`) {
			t.Fatalf("store-cpa-connection materialized tlsSkipVerify on sparse row: %s", raw)
		}
		reopened, err := openProtectedStoreForTest(t, dbPath, dataKeyPath)
		if err != nil {
			t.Fatalf("reopen protected store: %v", err)
		}
		defer func() { _ = reopened.Close() }()
		resolved, _, _, err := managerconfig.New(config.Config{TLSSkipVerify: true}, reopened, nil).ResolveManagerConfigWithSource(context.Background())
		if err != nil {
			t.Fatalf("ResolveManagerConfigWithSource: %v", err)
		}
		if !resolved.Collector.TLSSkipVerify || resolved.Collector.Queue != "sparse-queue" {
			t.Fatalf("resolved Collector after sparse handoff = %#v, want TLSSkipVerify=true Queue=sparse-queue", resolved.Collector)
		}
	})
}

func openProtectedStoreForTest(t *testing.T, dbPath string, dataKeyPath string) (*store.Store, error) {
	t.Helper()
	dataKey, _, err := security.LoadOrCreateDataKey("", dataKeyPath)
	if err != nil {
		return nil, err
	}
	protector, err := security.NewProtector(dataKey)
	if err != nil {
		return nil, err
	}
	return store.Open(dbPath, protector)
}
