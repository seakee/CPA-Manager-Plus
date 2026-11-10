package setting

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func revisionFixture(t *testing.T) (*sql.DB, Repository, *security.Protector, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "revision.sqlite")
	db, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	protector, err := security.NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return db, New(db, protector), protector, dbPath
}

func revisionPair(key string) (model.ManagerConfig, model.Setup) {
	setup := model.Setup{CPAUpstreamURL: "https://cpa.test", ManagementKey: key}
	var cfg model.ManagerConfig
	cfg.CPAConnection.CPABaseURL, cfg.CPAConnection.ManagementKey = setup.CPAUpstreamURL, key
	return cfg, setup
}

func requireRevision(t *testing.T, r Repository) string {
	t.Helper()
	revision, found, err := r.LoadPersistedCPAConnectionRevision(t.Context())
	if err != nil || !found || len(revision) != 32 {
		t.Fatalf("revision=%q found=%v err=%v", revision, found, err)
	}
	return revision
}

func TestPersistedCPAConnectionRevisionRollback(t *testing.T) {
	for _, stage := range []string{"setup", "revision insert", "revision update", "revision delete", "commit"} {
		t.Run(stage, func(t *testing.T) {
			db, r, _, _ := revisionFixture(t)
			cfg, setup := revisionPair("old-key")
			if err := r.SaveManagerConfigAndSetup(t.Context(), cfg, setup); err != nil {
				t.Fatal(err)
			}
			// Snapshot all settings including ciphertext and timestamps, so a failed
			// revision write/commit cannot hide a partially published source write.
			snapshot := func() map[string]string {
				t.Helper()
				rows, err := db.Query("select key, value, updated_at_ms from settings")
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				result := map[string]string{}
				for rows.Next() {
					var key, value string
					var updated int64
					if err := rows.Scan(&key, &value, &updated); err != nil {
						t.Fatal(err)
					}
					result[key] = fmt.Sprintf("%s/%d", value, updated)
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				return result
			}
			exec := func(query string) {
				t.Helper()
				if _, err := db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "revision insert" {
				exec("delete from settings where key = 'cpa_connection_revision_v1'")
			}
			before := snapshot()
			if stage == "commit" {
				// A deferred foreign-key violation reaches SQLite's actual COMMIT
				// failure path, after both source rows and revision were written.
				exec("create table revision_parent(id integer primary key)")
				exec("create table revision_child(id integer references revision_parent(id) deferrable initially deferred)")
				exec(`create trigger fail_commit after update on settings
					when new.key = 'cpa_connection_revision_v1' begin insert into revision_child values(1); end`)
			} else {
				event, key := "insert", "cpa_connection_revision_v1"
				if stage == "setup" {
					key = "setup"
				} else if stage == "revision update" {
					event = "update"
				} else if stage == "revision delete" {
					event = "delete"
				}
				row := "new"
				if event == "delete" {
					row = "old"
				}
				exec(fmt.Sprintf(`create trigger fail_write before %s on settings
					when %s.key = '%s' begin select raise(abort, 'injected failure'); end`, event, row, key))
			}
			cfg, setup = revisionPair("new-key")
			var err error
			if stage == "revision delete" {
				err = r.SaveManagerConfig(t.Context(), cfg) // mismatched setup invalidates revision
			} else {
				err = r.SaveManagerConfigAndSetup(t.Context(), cfg, setup)
			}
			if err == nil {
				t.Fatal("injected SQLite failure was accepted")
			}
			wantError := "injected failure"
			if stage == "commit" {
				wantError = "FOREIGN KEY constraint failed"
			}
			if !strings.Contains(err.Error(), wantError) {
				t.Fatalf("did not reach %s failure: %v", stage, err)
			}
			if !reflect.DeepEqual(before, snapshot()) {
				t.Fatal("failed transaction changed persisted settings or revision")
			}
		})
	}
}

func TestPersistedCPAConnectionRevisionConcurrentWritersAndRestart(t *testing.T) {
	db, r, protector, dbPath := revisionFixture(t)
	cfg, setup := revisionPair("initial-key")
	if err := r.SaveManagerConfigAndSetup(t.Context(), cfg, setup); err != nil {
		t.Fatal(err)
	}
	initial := requireRevision(t, r)
	otherDB, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()
	other := New(otherDB, protector)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			writer := r
			if i%2 == 0 {
				writer = other
			}
			cfg, setup := revisionPair(fmt.Sprintf("key-%d", i))
			if err := writer.SaveManagerConfigAndSetup(t.Context(), cfg, setup); err != nil {
				t.Errorf("concurrent writer: %v", err)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for range 32 {
			if revision, found, err := other.LoadPersistedCPAConnectionRevision(t.Context()); err != nil || !found || revision == "" {
				t.Errorf("concurrent snapshot: found=%v err=%v", found, err)
			}
		}
	}()
	close(start)
	wg.Wait()
	final := requireRevision(t, r)
	if final == initial {
		t.Fatal("concurrent connection replacements retained old revision")
	}
	cfg, _, err = r.LoadManagerConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	setup, _, err = r.LoadSetup(t.Context())
	if err != nil || cfg.CPAConnection.ManagementKey != setup.ManagementKey {
		t.Fatalf("incoherent final pair: %v", err)
	}
	if err := r.SaveManagerConfigAndSetup(t.Context(), cfg, setup); err != nil {
		t.Fatal(err)
	}
	if requireRevision(t, r) != final {
		t.Fatal("no-op after concurrent writes rotated revision")
	}
	if err := otherDB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if requireRevision(t, New(reopened, protector)) != final {
		t.Fatal("closing and reopening SQLite rotated revision")
	}
}

func TestPersistedCPAConnectionRevisionUnknownHistory(t *testing.T) {
	for _, mutation := range []string{
		"delete from settings where key = 'manager_config_v1'",
		"delete from settings where key = 'setup'",
		"delete from settings where key = 'cpa_connection_revision_v1'",
		"update settings set value = '{' where key = 'manager_config_v1'",
		"update settings set value = 'null' where key = 'setup'",
		"update settings set value = 'invalid' where key = 'cpa_connection_revision_v1'",
	} {
		t.Run(mutation, func(t *testing.T) {
			db, r, _, _ := revisionFixture(t)
			cfg, setup := revisionPair("key")
			if err := r.SaveManagerConfigAndSetup(t.Context(), cfg, setup); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if revision, found, err := r.LoadPersistedCPAConnectionRevision(t.Context()); err != nil || found || revision != "" {
				t.Fatalf("unknown history qualified: found=%v err=%v", found, err)
			}
		})
	}
	db, r, _, _ := revisionFixture(t)
	cfg, setup := revisionPair("key")
	if err := r.SaveManagerConfigAndSetup(t.Context(), cfg, setup); err != nil {
		t.Fatal(err)
	}
	wrongProtector, err := security.NewProtector([]byte("abcdef0123456789abcdef0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := New(db, wrongProtector).LoadPersistedCPAConnectionRevision(t.Context()); err != nil || found {
		t.Fatalf("unreadable encrypted connection qualified: found=%v err=%v", found, err)
	}
}

func TestPersistedCPAConnectionRevisionLifecycle(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "revision.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := New(db)
	ctx := t.Context()
	load := func() string {
		t.Helper()
		v, found, err := r.LoadPersistedCPAConnectionRevision(ctx)
		if err != nil || !found || v == "" {
			t.Fatalf("revision: %q found=%v err=%v", v, found, err)
		}
		return v
	}
	if _, found, err := r.LoadPersistedCPAConnectionRevision(ctx); err != nil || found {
		t.Fatalf("legacy revision found=%v err=%v", found, err)
	}
	setup := model.Setup{CPAUpstreamURL: "https://cpa.example.test", ManagementKey: "secret-one"}
	cfg := model.ManagerConfig{}
	cfg.CPAConnection.CPABaseURL = setup.CPAUpstreamURL
	cfg.CPAConnection.ManagementKey = setup.ManagementKey
	if err := r.SaveManagerConfigAndSetup(ctx, cfg, setup); err != nil {
		t.Fatal(err)
	}
	first := load()
	if err := r.SaveManagerConfigAndSetup(ctx, cfg, setup); err != nil {
		t.Fatal(err)
	}
	if v := load(); v != first {
		t.Fatalf("no-op revision rotated")
	}
	cfg.Collector.Queue = "unrelated"
	if err := r.SaveManagerConfigAndSetup(ctx, cfg, setup); err != nil {
		t.Fatal(err)
	}
	if v := load(); v != first {
		t.Fatalf("collector edit rotated revision")
	}
	setup.ManagementKey = "secret-two"
	cfg.CPAConnection.ManagementKey = setup.ManagementKey
	if err := r.SaveManagerConfigAndSetup(ctx, cfg, setup); err != nil {
		t.Fatal(err)
	}
	second := load()
	if second == first {
		t.Fatalf("same-URL key rotation retained revision")
	}
	// Key bytes (including surrounding spaces) must not be normalized away
	// when deciding whether an old private revision can be reused.
	setup.ManagementKey = " secret-two "
	cfg.CPAConnection.ManagementKey = setup.ManagementKey
	if err := r.SaveManagerConfigAndSetup(ctx, cfg, setup); err != nil {
		t.Fatal(err)
	}
	spaced := load()
	if spaced == second {
		t.Fatalf("different key bytes reused the old revision")
	}
	setup.ManagementKey = "secret-two"
	cfg.CPAConnection.ManagementKey = setup.ManagementKey
	if err := r.SaveManagerConfigAndSetup(ctx, cfg, setup); err != nil {
		t.Fatal(err)
	}
	second = load()
	if second == spaced {
		t.Fatalf("key bytes restored without changing revision")
	}
	reopened := New(db)
	if v, found, err := reopened.LoadPersistedCPAConnectionRevision(ctx); err != nil || !found || v != second {
		t.Fatalf("repository restart: revision=%q found=%v err=%v", v, found, err)
	}
	setup.CPAUpstreamURL = "https://replacement.example.test"
	cfg.CPAConnection.CPABaseURL = setup.CPAUpstreamURL
	if err := r.SaveManagerConfigAndSetup(ctx, cfg, setup); err != nil {
		t.Fatal(err)
	}
	if v := load(); v == second {
		t.Fatalf("URL replacement did not rotate")
	}
	if err := r.SaveSetup(ctx, model.Setup{CPAUpstreamURL: setup.CPAUpstreamURL, ManagementKey: "conflict"}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := r.LoadPersistedCPAConnectionRevision(ctx); err != nil || found {
		t.Fatalf("conflicted setup must be unqualified: found=%v err=%v", found, err)
	}
	if err := r.SaveManagerConfigAndSetup(ctx, cfg, setup); err != nil {
		t.Fatal(err)
	}
	if v := load(); v == second {
		t.Fatalf("repaired pair did not get revision")
	}
	if err := r.SaveManagerConfig(ctx, model.ManagerConfig{}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := r.LoadPersistedCPAConnectionRevision(ctx); err != nil || found {
		t.Fatalf("partial manager config must not be qualified: found=%v err=%v", found, err)
	}
}

func TestPersistedCPAConnectionRevisionLegacyNormalizeDoesNotMint(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "legacy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	protector, err := security.NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	r := New(db, protector)
	ctx := t.Context()
	setup := model.Setup{CPAUpstreamURL: "https://cpa.test", ManagementKey: "key"}
	cfg := model.ManagerConfig{}
	cfg.CPAConnection.CPABaseURL, cfg.CPAConnection.ManagementKey = setup.CPAUpstreamURL, setup.ManagementKey
	if err := r.NormalizeLegacyConnectionStorage(ctx, cfg, true, setup, true); err != nil {
		t.Fatal(err)
	}
	if _, found, err := r.LoadPersistedCPAConnectionRevision(ctx); err != nil || found {
		t.Fatalf("legacy normalization minted revision: found=%v err=%v", found, err)
	}
	if err := r.SaveManagerConfigAndSetup(ctx, cfg, setup); err != nil {
		t.Fatal(err)
	}
	rev, found, err := r.LoadPersistedCPAConnectionRevision(ctx)
	if err != nil || !found {
		t.Fatalf("authorized write did not mint: %q %v %v", rev, found, err)
	}
	if err := r.NormalizeLegacyConnectionStorage(ctx, cfg, true, setup, true); err != nil {
		t.Fatal(err)
	}
	after, found, err := r.LoadPersistedCPAConnectionRevision(ctx)
	if err != nil || !found || after != rev {
		t.Fatalf("no-op normalization changed revision")
	}
	cfg, setup = revisionPair("replacement-key")
	if err := r.NormalizeLegacyConnectionStorage(ctx, cfg, true, setup, true); err != nil {
		t.Fatal(err)
	}
	if _, found, err := r.LoadPersistedCPAConnectionRevision(ctx); err != nil || found {
		t.Fatalf("legacy replacement retained qualification: found=%v err=%v", found, err)
	}
}

func TestPatchManagerCollectorEnabledHandlesNullJSONShapes(t *testing.T) {
	db, r, _, _ := revisionFixture(t)
	defer db.Close()
	ctx := t.Context()

	if _, err := db.Exec(`insert into settings(key, value, updated_at_ms) values('manager_config_v1', 'null', 1)`); err != nil {
		t.Fatal(err)
	}
	if patched, err := r.PatchManagerCollectorEnabled(ctx, false); err == nil || patched {
		t.Fatalf("null document patched=%v err=%v, want error", patched, err)
	}

	if _, err := db.Exec(`update settings set value = '{"collector":null,"futureKnob":{"mode":"latent"}}' where key = 'manager_config_v1'`); err != nil {
		t.Fatal(err)
	}
	patched, err := r.PatchManagerCollectorEnabled(ctx, false)
	if err != nil || !patched {
		t.Fatalf("null collector patched=%v err=%v, want patched", patched, err)
	}
	var raw string
	if err := db.QueryRow(`select value from settings where key = 'manager_config_v1'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"enabled":false`) || !strings.Contains(raw, `"futureKnob":{"mode":"latent"}`) {
		t.Fatalf("patched raw = %s", raw)
	}
}
