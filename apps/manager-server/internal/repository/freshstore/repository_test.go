package freshstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func TestMigratedFreshSchemaInventory(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "manager.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = ValidatePristine(t.Context(), db); err != nil {
		t.Fatal("current Migrate seeds are not recognized", err)
	}
}

func TestPristineInventoryRejectsRetainedProductAndNonInitialMetadata(t *testing.T) {
	for name, statement := range map[string]string{
		"archived-identity-ledger": `insert into usage_event_identity_ledger(event_hash,raw_event_id,timestamp_ms,bucket_ms,first_seen_at_ms,updated_at_ms) values('archived',null,1,1,1,1)`,
		"codex-inspection":         `insert into codex_inspection_runs(trigger_type,status,started_at_ms,settings_json,created_at_ms,updated_at_ms) values('manual','completed',1,'{}',1,1)`,
		"quota-observation":        `insert into account_quota_observations(observation_hash,account_key,provider,source,inventory_scope_key,inventory_mode,observed_at_ms,created_at_ms) values('observed','account','codex','quota','scope','full',1,1)`,
		"future-product-table":     `create table future_product_state(value text); insert into future_product_state values('retained source')`,
		"deleted-source-sequence":  `insert into codex_inspection_runs(trigger_type,status,started_at_ms,settings_json,created_at_ms,updated_at_ms) values('manual','completed',1,'{}',1,1); delete from codex_inspection_runs`,
		"migration-progress":       `update usage_data_migrations set processed_rows=1 where name='usage_cache_accounting_v1'`,
		"rollup-history":           `update usage_monitoring_rollup_state set target_event_id=1 where rollup_name='projection_v1'`,
		"fts-source-payload":       `insert into usage_monitoring_event_search_v1_data(id,block) values(12345,x'012345')`,
		"schema-seed-missing":      `delete from gateway_source_binding_revision`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "manager.sqlite")
			db, err := sqlite.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			if err = ValidatePristine(t.Context(), db); !errors.Is(err, ErrNotFresh) {
				t.Fatal("source state admitted", err)
			}
			id := bootstrap.FreshIdentity{AdmissionID: "a", InstallationID: "i", Generation: "g", OperationID: "fresh-original", DatabasePath: path}
			if _, err = New(db).Create(t.Context(), id, nil); !errors.Is(err, ErrNotFresh) {
				t.Fatal("source promoted to Fresh", err)
			}
			var count int
			if err = db.QueryRow("select count(*) from settings where key in (?,?)", OperationKey, AuthorityKey).Scan(&count); err != nil || count != 0 {
				t.Fatal("negative control wrote Fresh authority", err)
			}
		})
	}
}

func TestAtomicCredentialAndOwnershipFence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := New(db)
	id := bootstrap.FreshIdentity{AdmissionID: "admission", InstallationID: "install", Generation: "gen", DatabasePath: path, OperationID: "fresh-operation"}
	record, err := st.Create(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Create(t.Context(), id, nil); err == nil {
		t.Fatal("created twice")
	}
	key := "cpamp_AtomicCredential0123456789!"
	cred, err := security.NewAdminCredential(key, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error {
		r.Admin = &cred
		r.Operation.AdminInitialized = true
		r.Authority.ClaimClosed = true
		return errors.New("cancel")
	})
	if err == nil {
		t.Fatal("cancel ignored")
	}
	record, err = st.Load(t.Context())
	if err != nil || record.Admin != nil || record.Operation.AdminInitialized {
		t.Fatal("partial credential commit", err)
	}
	record, err = st.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error {
		r.Admin = &cred
		r.Operation.AdminInitialized = true
		r.Authority.ClaimClosed = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := security.NewAdminCredential("cpamp_ReplacementCredential0123456789!", "fresh")
	for _, kind := range []string{"identity", "revision", "credential", "reopen", "admin-state"} {
		input := id
		rev := record.Operation.Revision
		if kind == "identity" {
			input.Generation = "foreign"
		}
		if kind == "revision" {
			rev--
		}
		_, err = st.Update(t.Context(), input, rev, func(r *Record) error {
			switch kind {
			case "credential":
				r.Admin = &other
			case "reopen":
				r.Authority.ClaimClosed = false
			case "admin-state":
				r.Operation.AdminInitialized = false
			}
			return nil
		})
		if err == nil {
			t.Fatal("fence bypass", kind)
		}
	}
	final, err := st.Load(t.Context())
	if err != nil || !security.VerifyAdminKey(*final.Admin, key) || final.Operation.Revision != record.Operation.Revision {
		t.Fatal("failed update changed record", err)
	}
}

func TestConnectionAndStrategyAreAtomicEncryptedAndNeverOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := New(db)
	id := bootstrap.FreshIdentity{AdmissionID: "a", InstallationID: "i", Generation: "g", DatabasePath: path, OperationID: "fresh-operation"}
	admin, err := security.NewAdminCredential("Cpamp_Strategy_Admin_Secret_839216", "host")
	if err != nil {
		t.Fatal(err)
	}
	record, err := st.Create(t.Context(), id, &admin)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error {
		r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: "http://cpa:8317", ManagementKey: "plaintext"}
		return nil
	})
	if err == nil {
		t.Fatal("plaintext accepted")
	}
	protector, err := security.NewProtector([]byte("task test data key"))
	if err != nil {
		t.Fatal(err)
	}
	secret, err := protector.ProtectString("ManagementSecret")
	if err != nil {
		t.Fatal(err)
	}
	connection := &model.ManagerCPAConnectionConfig{CPABaseURL: "http://cpa:8317", ManagementKey: secret}
	targetHash := sha256.Sum256([]byte(connection.CPABaseURL))
	checkpoint := bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.ConnectExternal, Stage: "committed", TargetIdentity: "external:sha256:" + hex.EncodeToString(targetHash[:]), TargetGeneration: 1}
	_, err = st.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error {
		r.Connection = connection
		r.Operation.Strategy = checkpoint
		return errors.New("cancel")
	})
	if err == nil {
		t.Fatal("cancel ignored")
	}
	var count int
	if err = db.QueryRow("select count(*) from settings where key='manager_config_v1'").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial connection write", err)
	}
	record, err = st.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error { r.Connection = connection; r.Operation.Strategy = checkpoint; return nil })
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := st.Load(t.Context())
	if err != nil || loaded.Connection == nil || *loaded.Connection != *connection || loaded.Operation.Strategy.Stage != "committed" {
		t.Fatal("strategy+connection not atomic", err)
	}
	_, err = st.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error { r.Connection.ManagementKey = "new secret"; return nil })
	if err == nil {
		t.Fatal("overwrite accepted")
	}
}

func TestMalformedStrategyCannotBeWrittenOrLoaded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := New(db)
	id := bootstrap.FreshIdentity{AdmissionID: "a", InstallationID: "i", Generation: "g", DatabasePath: path, OperationID: "fresh-operation"}
	admin, err := security.NewAdminCredential("Cpamp_Strategy_Admin_Secret_839216", "host")
	if err != nil {
		t.Fatal(err)
	}
	record, err := st.Create(t.Context(), id, &admin)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error {
		r.Operation.Strategy.Stage = "committed"
		return nil
	})
	if !errors.Is(err, ErrNotFresh) {
		t.Fatal("stage-only commit was saved", err)
	}
	loaded, err := st.Load(t.Context())
	if err != nil || loaded.Operation.Revision != record.Operation.Revision || loaded.Operation.Strategy.Stage != "" {
		t.Fatal("rejected update changed checkpoint", err)
	}
	// Corruption written outside the repository is also rejected on read.
	record.Operation.Strategy.Stage = "committed"
	data, err := json.Marshal(record.Operation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("update settings set value=? where key=?", string(data), OperationKey); err != nil {
		t.Fatal(err)
	}
	if _, err = st.Load(t.Context()); !errors.Is(err, ErrNotFresh) {
		t.Fatal("malformed durable checkpoint was loaded", err)
	}
}

func TestExternalCommittedRequiresMatchingDurableConnection(t *testing.T) {
	for _, kind := range []string{"missing", "different-target", "matching"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "manager.sqlite")
			db, err := sqlite.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			st := New(db)
			id := bootstrap.FreshIdentity{AdmissionID: "a", InstallationID: "i", Generation: "g", DatabasePath: path, OperationID: "fresh-operation"}
			admin, err := security.NewAdminCredential("Cpamp_Strategy_Admin_Secret_839216", "host")
			if err != nil {
				t.Fatal(err)
			}
			record, err := st.Create(t.Context(), id, &admin)
			if err != nil {
				t.Fatal(err)
			}
			protector, err := security.NewProtector([]byte("isolated test key"))
			if err != nil {
				t.Fatal(err)
			}
			secret, err := protector.ProtectString("source management key")
			if err != nil {
				t.Fatal(err)
			}
			baseURL := "http://cpa:8317"
			digest := sha256.Sum256([]byte(baseURL))
			checkpoint := bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.ConnectExternal, Stage: "committed", TargetIdentity: "external:sha256:" + hex.EncodeToString(digest[:]), TargetGeneration: 1}
			var connection *model.ManagerCPAConnectionConfig
			if kind != "missing" {
				connection = &model.ManagerCPAConnectionConfig{CPABaseURL: baseURL, ManagementKey: secret}
				if kind == "different-target" {
					connection.CPABaseURL = "http://other:8317"
				}
			}
			_, err = st.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error {
				r.Operation.Strategy = checkpoint
				r.Connection = connection
				return nil
			})
			if kind == "matching" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if !errors.Is(err, ErrNotFresh) {
					t.Fatal("inconsistent External commit accepted", err)
				}
				loaded, err := st.Load(t.Context())
				if err != nil || loaded.Operation.Revision != record.Operation.Revision || loaded.Connection != nil || loaded.Operation.Strategy.Stage != "" {
					t.Fatal("rejected update changed durable authority", err)
				}
				// Simulate corruption outside the repository, then test a real reopen.
				record.Operation.Strategy = checkpoint
				data, err := json.Marshal(record.Operation)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec("update settings set value=? where key=?", string(data), OperationKey); err != nil {
					t.Fatal(err)
				}
				if connection != nil {
					disabled := false
					data, err = json.Marshal(model.ManagerConfig{CPAConnection: *connection, Collector: model.ManagerCollectorConfig{Enabled: &disabled}})
					if err != nil {
						t.Fatal(err)
					}
					if _, err = db.Exec("insert into settings(key,value,updated_at_ms) values('manager_config_v1',?,1)", string(data)); err != nil {
						t.Fatal(err)
					}
				}
				if _, err = st.Load(t.Context()); !errors.Is(err, ErrNotFresh) {
					t.Fatal("corrupt External record loaded", err)
				}
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = sqlite.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := New(db).Load(t.Context())
			if kind == "matching" {
				if err != nil || loaded.Connection == nil || *loaded.Connection != *connection || !reflect.DeepEqual(loaded.Operation.Strategy, checkpoint) {
					t.Fatal("matching External record did not survive reopen", err)
				}
			} else if !errors.Is(err, ErrNotFresh) {
				t.Fatal("corrupt External record survived reopen", err)
			}
		})
	}
}
