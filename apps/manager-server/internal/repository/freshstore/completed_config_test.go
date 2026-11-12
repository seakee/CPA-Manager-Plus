package freshstore

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func TestConfigCompatibilityRequiresCompletedFreshOperation(t *testing.T) {
	for _, complete := range []bool{false, true} {
		for _, enabled := range []*bool{nil, configTestBool(false), configTestBool(true)} {
			name := "pending"
			if complete {
				name = "complete"
			}
			collector := "default"
			if enabled != nil {
				collector = "disabled"
				if *enabled {
					collector = "enabled"
				}
			}
			t.Run(name+"/"+collector, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "manager.sqlite")
				db, err := sqlite.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				st := New(db)
				id := bootstrap.FreshIdentity{AdmissionID: "admission", InstallationID: "installation", Generation: "generation", OperationID: "operation", DatabasePath: path}
				admin, err := security.NewAdminCredential("Cpamp_Config_Admin_Secret_839216", "host")
				if err != nil {
					t.Fatal(err)
				}
				record, err := st.Create(t.Context(), id, &admin)
				if err != nil {
					t.Fatal(err)
				}
				if complete {
					record, err = st.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error {
						r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: id.OperationID, Revision: 1, Target: "/console", Stage: bootstrap.PrefixCommitted, Committed: true}
						r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, Stage: "committed", TargetIdentity: "runtime", TargetGeneration: 1, TargetArtifactID: "sha256:" + strings.Repeat("a", 64)}
						r.Operation.ProductComplete = true
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				protector, err := security.NewProtector([]byte("config compatibility test data key"))
				if err != nil {
					t.Fatal(err)
				}
				settings := setting.New(db, protector)
				cfg := model.ManagerConfig{CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: "http://cpa:8317", ManagementKey: "original-management-secret"}, Collector: model.ManagerCollectorConfig{Enabled: enabled}}
				// Exercise the same persistence contract as the existing Config
				// service, including its encrypted setup compatibility row.
				if err = settings.SaveManagerConfigAndSetup(t.Context(), cfg, model.Setup{CPAUpstreamURL: cfg.CPAConnection.CPABaseURL, ManagementKey: cfg.CPAConnection.ManagementKey}); err != nil {
					t.Fatal(err)
				}
				loaded, err := st.Load(t.Context())
				if !complete {
					if !errors.Is(err, ErrNotFresh) {
						t.Fatalf("pending Config state admitted: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal("completed Config state became unreadable", err)
				}
				if loaded.Operation.Identity != id || loaded.Operation.Revision != record.Operation.Revision || !loaded.Operation.ProductComplete || loaded.Connection == nil {
					t.Fatal("Config save changed Fresh ownership")
				}
				secret, err := protector.UnprotectString(loaded.Connection.ManagementKey)
				if err != nil || secret != cfg.CPAConnection.ManagementKey {
					t.Fatal("Config secret was not preserved encrypted", err)
				}
				if err = db.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := sqlite.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				if _, err = New(reopened).Load(t.Context()); err != nil {
					t.Fatal("completed checkpoint did not reopen", err)
				}
			})
		}
	}
}

func TestPendingFreshRejectsCollectorConfigWithoutSetup(t *testing.T) {
	for _, enabled := range []*bool{nil, configTestBool(true)} {
		path := filepath.Join(t.TempDir(), "manager.sqlite")
		db, err := sqlite.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		st := New(db)
		id := bootstrap.FreshIdentity{AdmissionID: "admission", InstallationID: "installation", Generation: "generation", OperationID: "operation", DatabasePath: path}
		if _, err = st.Create(t.Context(), id, nil); err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector([]byte("pending config test data key"))
		if err != nil {
			t.Fatal(err)
		}
		cfg := model.ManagerConfig{CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: "http://cpa:8317", ManagementKey: "management-secret"}, Collector: model.ManagerCollectorConfig{Enabled: enabled}}
		if err = setting.New(db, protector).SaveManagerConfig(t.Context(), cfg); err != nil {
			t.Fatal(err)
		}
		if _, err = st.Load(t.Context()); !errors.Is(err, ErrNotFresh) {
			t.Fatalf("pending collector state admitted: %v", err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPendingFreshRejectsLegacySettings(t *testing.T) {
	for _, key := range []string{"setup", "bootstrap_state_v1", "runtime_desired_v1"} {
		t.Run(key, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "manager.sqlite")
			db, err := sqlite.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			st := New(db)
			id := bootstrap.FreshIdentity{AdmissionID: "admission", InstallationID: "installation", Generation: "generation", OperationID: "operation", DatabasePath: path}
			if _, err = st.Create(t.Context(), id, nil); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec("insert into settings(key,value,updated_at_ms) values(?, '{}', 1)", key); err != nil {
				t.Fatal(err)
			}
			if _, err = st.Load(t.Context()); !errors.Is(err, ErrNotFresh) {
				t.Fatalf("pending legacy setting %s admitted: %v", key, err)
			}
		})
	}
}

func configTestBool(value bool) *bool { return &value }

func TestCompletedExternalAllowsExistingConfigConnectionEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	protector, err := security.NewProtector([]byte("external config test data key"))
	if err != nil {
		t.Fatal(err)
	}
	st := New(db)
	id := bootstrap.FreshIdentity{AdmissionID: "a", InstallationID: "i", Generation: "g", DatabasePath: path, OperationID: "external-config"}
	admin, err := security.NewAdminCredential("Cpamp_Config_Admin_Secret_839216", "host")
	if err != nil {
		t.Fatal(err)
	}
	record, err := st.Create(t.Context(), id, &admin)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := protector.ProtectString("original-secret")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("http://original-cpa:8317"))
	record, err = st.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: id.OperationID, Revision: 1, Target: "/console", Stage: bootstrap.PrefixCommitted, Committed: true}
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.ConnectExternal, TargetIdentity: "external:sha256:" + hex.EncodeToString(digest[:]), TargetGeneration: 1, Stage: "committed"}
		r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: "http://original-cpa:8317", ManagementKey: secret}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err = st.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error { r.Operation.ProductComplete = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	cfg := model.ManagerConfig{CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: "http://replacement-cpa:8317", ManagementKey: "replacement-secret"}, Collector: model.ManagerCollectorConfig{Enabled: configTestBool(false)}}
	if err = setting.New(db, protector).SaveManagerConfigAndSetup(t.Context(), cfg, model.Setup{CPAUpstreamURL: cfg.CPAConnection.CPABaseURL, ManagementKey: cfg.CPAConnection.ManagementKey}); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.Load(t.Context())
	if err != nil || !reflect.DeepEqual(loaded.Operation, record.Operation) || loaded.Connection == nil || loaded.Connection.CPABaseURL != cfg.CPAConnection.CPABaseURL {
		t.Fatal("post-install Config edit invalidated Fresh authority", err)
	}
}

// managerConfigRow reads the stored Manager configuration directly, so a test
// can assert what the installation default actually persisted.
func managerConfigRow(t *testing.T, db *sql.DB) model.ManagerConfig {
	t.Helper()
	var data string
	if err := db.QueryRow("select value from settings where key='manager_config_v1'").Scan(&data); err != nil {
		t.Fatal(err)
	}
	var cfg model.ManagerConfig
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// A new installation has to collect by default. The pending fence requires a
// stopped Collector, so completion is the only transition that can supply the
// enabled default; nothing after it writes one.
func TestCompletionEnablesRequestMonitoring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	st := New(db)
	id := bootstrap.FreshIdentity{AdmissionID: "admission", InstallationID: "installation", Generation: "generation", OperationID: "operation", DatabasePath: path}
	admin, err := security.NewAdminCredential("Cpamp_Collector_Admin_Secret_839216", "host")
	if err != nil {
		t.Fatal(err)
	}
	record, err := st.Create(t.Context(), id, &admin)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector([]byte("completion collector test data key"))
	if err != nil {
		t.Fatal(err)
	}
	secret, err := protector.ProtectString("installed-management-key")
	if err != nil {
		t.Fatal(err)
	}
	connection := &model.ManagerCPAConnectionConfig{CPABaseURL: "http://cpa:8317", ManagementKey: secret}
	record, err = st.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: id.OperationID, Revision: 1, Target: "/console", Stage: bootstrap.PrefixCommitted, Committed: true}
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, Stage: "committed", TargetIdentity: "runtime", TargetGeneration: 1, TargetArtifactID: "sha256:" + strings.Repeat("a", 64)}
		r.Connection = connection
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if pending := managerConfigRow(t, db); pending.Collector.Enabled == nil || *pending.Collector.Enabled {
		t.Fatalf("pending installation must keep the Collector stopped, got %+v", pending.Collector)
	}
	record, err = st.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error {
		r.Operation.ProductComplete = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	completed := managerConfigRow(t, db)
	if completed.Collector.Enabled == nil || !*completed.Collector.Enabled {
		t.Fatalf("completed installation left request monitoring off: %+v", completed.Collector)
	}
	if completed.CPAConnection.CPABaseURL != connection.CPABaseURL || completed.CPAConnection.ManagementKey != secret {
		t.Fatal("completion default rewrote the staged CPA connection")
	}
	loaded, err := st.Load(t.Context())
	if err != nil || loaded.Connection == nil || *loaded.Connection != *connection {
		t.Fatal("completed installation did not keep its connection", err)
	}
	if plain, err := protector.UnprotectString(loaded.Connection.ManagementKey); err != nil || plain != "installed-management-key" {
		t.Fatal("completion default damaged the protected management key", err)
	}
	// A completed installation owns its Collector choice: a later write is the
	// operator's, not an installation default to overrule.
	cfg := model.ManagerConfig{CPAConnection: *connection, Collector: model.ManagerCollectorConfig{Enabled: configTestBool(false)}}
	if err = setting.New(db, protector).SaveManagerConfig(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err = st.Update(t.Context(), id, record.Operation.Revision, func(*Record) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if after := managerConfigRow(t, db); after.Collector.Enabled == nil || *after.Collector.Enabled {
		t.Fatal("a completed installation's Collector choice was overwritten")
	}
}

// Legacy start-only NewManaged installations already have ProductComplete=true
// with Connection=nil when prepareCompletedStartup migrates their destination
// connection. Committing that connection on an already-completed installation
// must enable request monitoring by default while preserving any explicit
// operator choice stored in a connectionless manager_config_v1 row beforehand.
func TestCompletedConnectionMigrationEnablesDefaultMonitoringAndPreservesOperatorChoice(t *testing.T) {
	t.Run("default_unconfigured_enables_monitoring", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "manager.sqlite")
		db, err := sqlite.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		st := New(db)
		id := bootstrap.FreshIdentity{AdmissionID: "admission", InstallationID: "installation", Generation: "generation", OperationID: "operation", DatabasePath: path}
		admin, err := security.NewAdminCredential("Cpamp_Collector_Admin_Secret_839216", "host")
		if err != nil {
			t.Fatal(err)
		}
		record, err := st.Create(t.Context(), id, &admin)
		if err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector([]byte("completion collector test data key"))
		if err != nil {
			t.Fatal(err)
		}
		secret, err := protector.ProtectString("migrated-management-key")
		if err != nil {
			t.Fatal(err)
		}
		// Legacy completed start-only NewManaged store: ProductComplete=true, Connection=nil.
		record, err = st.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: id.OperationID, Revision: 1, Target: "/console", Stage: bootstrap.PrefixCommitted, Committed: true}
			r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, Stage: "committed", TargetIdentity: "runtime", TargetGeneration: 1, TargetArtifactID: "sha256:" + strings.Repeat("a", 64)}
			r.Operation.ProductComplete = true
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		connection := &model.ManagerCPAConnectionConfig{CPABaseURL: "http://127.0.0.1:8317", ManagementKey: secret}
		digest := sha256.Sum256([]byte(connection.CPABaseURL))
		if _, err = st.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error {
			r.Operation.Strategy.DestinationConnectionIdentity = "managed:sha256:" + hex.EncodeToString(digest[:])
			r.Connection = connection
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		migrated := managerConfigRow(t, db)
		if migrated.Collector.Enabled == nil || !*migrated.Collector.Enabled {
			t.Fatalf("completed destination migration left request monitoring off: %+v", migrated.Collector)
		}
		if migrated.CPAConnection != *connection {
			t.Fatalf("completed destination migration connection = %+v, want %+v", migrated.CPAConnection, *connection)
		}
	})

	t.Run("preserves_operator_disabled_switch_and_collector_fields", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "manager.sqlite")
		db, err := sqlite.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		st := New(db)
		id := bootstrap.FreshIdentity{AdmissionID: "admission", InstallationID: "installation", Generation: "generation", OperationID: "operation", DatabasePath: path}
		admin, err := security.NewAdminCredential("Cpamp_Collector_Admin_Secret_839216", "host")
		if err != nil {
			t.Fatal(err)
		}
		record, err := st.Create(t.Context(), id, &admin)
		if err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector([]byte("completion collector test data key"))
		if err != nil {
			t.Fatal(err)
		}
		secret, err := protector.ProtectString("migrated-management-key")
		if err != nil {
			t.Fatal(err)
		}
		record, err = st.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: id.OperationID, Revision: 1, Target: "/console", Stage: bootstrap.PrefixCommitted, Committed: true}
			r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, Stage: "committed", TargetIdentity: "runtime", TargetGeneration: 1, TargetArtifactID: "sha256:" + strings.Repeat("a", 64)}
			r.Operation.ProductComplete = true
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		// Operator explicitly disabled monitoring and tuned pollIntervalMs before destination migration ran.
		settingsRepo := setting.New(db, protector)
		if err := settingsRepo.SaveManagerConfig(t.Context(), model.ManagerConfig{
			Collector: model.ManagerCollectorConfig{Enabled: configTestBool(false), PollIntervalMS: 1500},
		}); err != nil {
			t.Fatal(err)
		}
		loadedBefore, err := st.Load(t.Context())
		if err != nil || loadedBefore.Connection != nil {
			t.Fatalf("connectionless completed config must keep Connection=nil before migration, got %+v err=%v", loadedBefore.Connection, err)
		}
		connection := &model.ManagerCPAConnectionConfig{CPABaseURL: "http://127.0.0.1:8317", ManagementKey: secret}
		digest := sha256.Sum256([]byte(connection.CPABaseURL))
		if _, err = st.Update(t.Context(), id, loadedBefore.Operation.Revision, func(r *Record) error {
			r.Operation.Strategy.DestinationConnectionIdentity = "managed:sha256:" + hex.EncodeToString(digest[:])
			r.Connection = connection
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		migrated := managerConfigRow(t, db)
		if migrated.Collector.Enabled == nil || *migrated.Collector.Enabled {
			t.Fatalf("completed destination migration overwrote operator disabled switch: %+v", migrated.Collector)
		}
		if migrated.Collector.PollIntervalMS != 1500 {
			t.Fatalf("completed destination migration lost existing collector PollIntervalMS=%d, want 1500", migrated.Collector.PollIntervalMS)
		}
		if migrated.CPAConnection != *connection {
			t.Fatalf("completed destination migration connection = %+v, want %+v", migrated.CPAConnection, *connection)
		}
	})
}
