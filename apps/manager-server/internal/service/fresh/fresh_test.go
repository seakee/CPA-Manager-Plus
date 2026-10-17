package fresh

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

func pathsFor(dir string) sqlite.InspectionPaths {
	p := legacypaths(filepath.Join(dir, "manager.sqlite"))
	return p
}
func legacypaths(db string) sqlite.InspectionPaths {
	return sqlite.InspectionPaths{Database: db, DataKey: filepath.Join(filepath.Dir(db), "data.key"), Config: filepath.Join(filepath.Dir(db), "manager.yaml"), Archives: filepath.Join(filepath.Dir(db), "usage-archives")}
}
func openTest(t *testing.T) (*Opened, *Service) {
	t.Helper()
	o, err := Open(t.Context(), pathsFor(t.TempDir()), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Close() })
	return o, New(freshstore.New(o.DB))
}
func handoff(t *testing.T, h Handoff) ExchangeRequest {
	t.Helper()
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(h.Fragment, "#handoff="))
	if err != nil {
		t.Fatal(err)
	}
	var req ExchangeRequest
	if err = json.Unmarshal(data, &req); err != nil {
		t.Fatal(err)
	}
	return req
}
func sessionFor(t *testing.T, s *Service) (Handoff, SessionResult) {
	t.Helper()
	h, err := s.Issue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Exchange(t.Context(), handoff(t, h))
	if err != nil {
		t.Fatal(err)
	}
	return h, r
}

func TestCheckpointURIKeepsWindowsDriveInLocalPath(t *testing.T) {
	for _, path := range []string{"C:/Manager data/#fresh?/manager.sqlite", "/tmp/Manager data/#fresh?/manager.sqlite"} {
		u, err := url.Parse(checkpointDataSourceName(path))
		if err != nil {
			t.Fatal(err)
		}
		if u.Scheme != "file" || u.Host != "" || strings.TrimPrefix(u.Path, "/") != strings.TrimPrefix(path, "/") || u.Fragment != "" {
			t.Fatalf("checkpoint URI changed local database path: %q -> %s", path, u)
		}
		if u.Query().Get("mode") != "ro" || !reflect.DeepEqual(u.Query()["_pragma"], []string{"query_only(1)", "busy_timeout(2000)"}) {
			t.Fatalf("checkpoint URI lost read-only constraints: %s", u)
		}
	}
}

func TestFileDataKeyFenceFailurePreventsSQLiteAndCanRetry(t *testing.T) {
	p := pathsFor(t.TempDir())
	_, err := Open(t.Context(), p, OpenOptions{Hook: func(stage string) error {
		if stage == "data-key-loaded" {
			// Deterministic OpenFile failure on all platforms, regardless of uid
			// or permission semantics: the successfully loaded key is now absent.
			return os.Remove(p.DataKey)
		}
		return nil
	}})
	if err == nil {
		t.Fatal("missing file-backed key bypassed durability fence")
	}
	for _, name := range []string{p.Database, p.Database + "-wal", p.Database + "-shm"} {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatal("SQLite created before key durability", name, err)
		}
	}
	receiptBytes, err := os.ReadFile(p.Database + ".manager.lock")
	if err != nil {
		t.Fatal(err)
	}
	var receipt processlock.FreshReceipt
	if err := json.Unmarshal(receiptBytes, &receipt); err != nil {
		t.Fatal(err)
	}
	retry, err := Open(t.Context(), p, OpenOptions{})
	if err != nil {
		t.Fatal("retry after fence failure", err)
	}
	defer retry.Close()
	if retry.Record.Operation.Identity != receipt.Identity {
		t.Fatal("retry replaced Fresh admission")
	}
}

func TestExplicitRawDataKeyDoesNotRequireFileFence(t *testing.T) {
	for _, raw := range []string{"explicit raw key", "  explicit raw key  "} {
		t.Run(raw, func(t *testing.T) {
			p := pathsFor(t.TempDir())
			o, err := Open(t.Context(), p, OpenOptions{DataKey: raw})
			if err != nil {
				t.Fatal(err)
			}
			identity := o.Record.Operation.Identity
			if _, err := os.Stat(p.DataKey); !os.IsNotExist(err) {
				t.Fatal("raw key materialized a key file", err)
			}
			if err := o.Close(); err != nil {
				t.Fatal(err)
			}
			o, err = Open(t.Context(), p, OpenOptions{DataKey: raw})
			if err != nil {
				t.Fatal("raw key reopen", err)
			}
			defer o.Close()
			if o.Record.Operation.Identity != identity {
				t.Fatal("raw key reopen changed identity")
			}
		})
	}
}

func TestExistingExternalConnectionRejectsWrongDataKey(t *testing.T) {
	p := pathsFor(t.TempDir())
	o, err := Open(t.Context(), p, OpenOptions{AdminKey: "cpamp_AbcDef0123456789ManualKey!"})
	if err != nil {
		t.Fatal(err)
	}
	originalDataKey, err := os.ReadFile(p.DataKey)
	if err != nil {
		t.Fatal(err)
	}
	const baseURL = "http://cpa:8317"
	const managementKey = "private-management-secret"
	protected, err := o.Protector.ProtectString(managementKey)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(baseURL))
	st := freshstore.New(o.DB)
	_, err = st.Update(t.Context(), o.Record.Operation.Identity, o.Record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.ConnectExternal, Stage: "committed",
			TargetIdentity: "external:sha256:" + hex.EncodeToString(digest[:]), TargetGeneration: 1,
		}
		r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: baseURL, ManagementKey: protected}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = o.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p.DataKey, []byte("0123456789abcdef0123456789abcdef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reopened, reopenErr := Open(t.Context(), p, OpenOptions{}); reopenErr == nil {
		_ = reopened.Close()
		t.Fatal("wrong data key reopened a persisted External credential")
	}
	if err = os.WriteFile(p.DataKey, originalDataKey, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), p, OpenOptions{})
	if err != nil {
		t.Fatal("original data key no longer reopens Fresh state", err)
	}
	defer reopened.Close()
	if reopened.Record.Connection == nil {
		t.Fatal("restored Fresh state lost External connection")
	}
	got, err := reopened.Protector.UnprotectString(reopened.Record.Connection.ManagementKey)
	if err != nil || got != managementKey {
		t.Fatal("restored data key cannot decrypt External credential", err)
	}
}

func TestClaimAdminContinuationAndRestart(t *testing.T) {
	o, s := openTest(t)
	ctx := t.Context()
	if s.Discover(ctx).Status != bootstrap.PublicFreshClaimRequired {
		t.Fatal("fresh discovery")
	}
	h, session := sessionFor(t, s)
	if _, err := s.Exchange(ctx, handoff(t, h)); err == nil {
		t.Fatal("claim exchanged twice")
	}
	key, err := s.GenerateAdmin(ctx, session.Token)
	if err != nil {
		t.Fatal(err)
	}
	req := AdminCommitRequest{Identity: h.Operation.Identity, CommitID: "commit-a", Key: key}
	op, err := s.CommitAdmin(ctx, session.Token, req)
	if err != nil || !op.AdminInitialized {
		t.Fatal(op, err)
	}
	retry, err := s.CommitAdmin(ctx, session.Token, req)
	if err != nil || retry.Revision != op.Revision {
		t.Fatal("lost result changed admin", err)
	}
	conflict := req
	conflict.Key = "cpamp_AbcDef0123456789ManualKey!"
	if _, err = s.CommitAdmin(ctx, session.Token, conflict); err == nil {
		t.Fatal("same commit id accepted a different Admin key")
	}
	current, err := freshstore.New(o.DB).Load(ctx)
	if err != nil || current.Admin == nil || !security.VerifyAdminKey(*current.Admin, key) || security.VerifyAdminKey(*current.Admin, conflict.Key) {
		t.Fatal("conflicting lost-response replay changed canonical Admin", err)
	}
	if _, err = s.GenerateAdmin(ctx, session.Token); err == nil {
		t.Fatal("admin generated after commit")
	}
	if s.Discover(ctx).Status != bootstrap.PublicUnavailable {
		t.Fatal("initialized leaked admin auth entry")
	}
	if _, err = s.Authorize(ctx, key); err == nil {
		t.Fatal("Admin Key accepted as session")
	}
	if _, err = s.Authorize(ctx, session.Token); err != nil {
		t.Fatal("continuation lost", err)
	}
	identity := o.Record.Operation.Identity
	path := identity.DatabasePath
	if err = o.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, legacypaths(path), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s = New(freshstore.New(reopened.DB))
	if _, err = s.Authorize(ctx, session.Token); err != nil {
		t.Fatal("session not restart durable", err)
	}
	resume, err := s.Issue(ctx)
	if err != nil || !resume.Resume || resume.Operation.Identity != identity {
		t.Fatal("resume", err)
	}
	if _, err = s.Authorize(ctx, session.Token); err == nil {
		t.Fatal("resume did not revoke old session")
	}
	token := handoff(t, resume).Token
	if _, err = s.Authorize(ctx, token); err != nil {
		t.Fatal("resume token", err)
	}
	if _, err = s.Exchange(ctx, handoff(t, resume)); err == nil {
		t.Fatal("resume reopened claim")
	}
	if _, err = s.CommitAdmin(ctx, token, AdminCommitRequest{Identity: identity, CommitID: "replace", Key: key}); err == nil {
		t.Fatal("resume reset admin")
	}
	record, err := freshstore.New(reopened.DB).Load(ctx)
	if err != nil || !security.VerifyAdminKey(*record.Admin, key) {
		t.Fatal("canonical credential", err)
	}
	var checkpoint, authority string
	if err = reopened.DB.QueryRow("select value from settings where key=?", freshstore.OperationKey).Scan(&checkpoint); err != nil {
		t.Fatal(err)
	}
	if err = reopened.DB.QueryRow("select value from settings where key=?", freshstore.AuthorityKey).Scan(&authority); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{key, session.Token, token, handoff(t, h).Token} {
		if strings.Contains(checkpoint, secret) || strings.Contains(authority, secret) {
			t.Fatal("secret persisted plaintext")
		}
	}
}

func TestClaimFencesExpiryReissueAndRevocation(t *testing.T) {
	_, s := openTest(t)
	ctx := t.Context()
	now := time.Now()
	s.now = func() time.Time { return now }
	first, err := s.Issue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	req := handoff(t, first)
	for _, which := range []string{"installation", "generation", "operation", "claim-generation"} {
		bad := req
		switch which {
		case "installation":
			bad.Identity.InstallationID = "foreign"
		case "generation":
			bad.Identity.Generation = "foreign"
		case "operation":
			bad.Identity.OperationID = "foreign"
		default:
			bad.ClaimGeneration++
		}
		if _, err = s.Exchange(ctx, bad); err == nil {
			t.Fatal(which)
		}
	}
	now = now.Add(bootstrap.FreshClaimLifetime)
	if _, err = s.Exchange(ctx, req); err == nil {
		t.Fatal("expired claim accepted")
	}
	_, session := sessionFor(t, s)
	if _, err = s.Issue(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authorize(ctx, session.Token); err == nil {
		t.Fatal("old generation session accepted")
	}
	_, session = sessionFor(t, s)
	now = now.Add(bootstrap.FreshSessionLifetime)
	if _, err = s.Authorize(ctx, session.Token); err == nil {
		t.Fatal("expired session accepted")
	}
	_, session = sessionFor(t, s)
	if err = s.Revoke(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authorize(ctx, session.Token); err == nil {
		t.Fatal("revoked session")
	}
}

func TestConcurrentClaimAndAdminAtMostOne(t *testing.T) {
	_, s := openTest(t)
	h, err := s.Issue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var wins atomic.Int32
	var token string
	var mu sync.Mutex
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := s.Exchange(context.Background(), handoff(t, h))
			if err == nil {
				wins.Add(1)
				mu.Lock()
				token = result.Token
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("exchange winners=%d", wins.Load())
	}
	wins.Store(0)
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.CommitAdmin(context.Background(), token, AdminCommitRequest{Identity: h.Operation.Identity, CommitID: strings.Repeat("x", i+1), Key: "cpamp_AbcDef0123456789ManualKey!"})
			if err == nil {
				wins.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("admin winners=%d", wins.Load())
	}
}

func TestFreshKillChild(t *testing.T) {
	dir := os.Getenv("CPAMP_FRESH_KILL_DIR")
	if dir == "" {
		return
	}
	stage := os.Getenv("CPAMP_FRESH_KILL_STAGE")
	_, err := Open(t.Context(), pathsFor(dir), OpenOptions{Hook: func(at string) error {
		if at == stage {
			os.Stdout.WriteString("KILL_WINDOW_READY\n")
			select {}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Fatal("did not enter kill window")
}

func TestAdmissionSubprocessKillWindows(t *testing.T) {
	for _, stage := range []string{"pending-receipt", "data-key", "schema", "checkpoint"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestFreshKillChild$", "-test.timeout=30s")
			cmd.Env = append(os.Environ(), "CPAMP_FRESH_KILL_DIR="+dir, "CPAMP_FRESH_KILL_STAGE="+stage)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			ready := make(chan bool, 1)
			go func() {
				buf := make([]byte, 1024)
				n, _ := stdout.Read(buf)
				ready <- strings.Contains(string(buf[:n]), "KILL_WINDOW_READY")
			}()
			select {
			case ok := <-ready:
				if !ok {
					_ = cmd.Process.Kill()
					t.Fatal("child failed before window")
				}
			case <-time.After(20 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatal("window timeout")
			}
			p := pathsFor(dir)
			before, err := os.Stat(p.Database + ".manager.lock")
			if err != nil {
				t.Fatal(err)
			}
			// Observe crash artifacts after the owner exits: Windows locks
			// receipt bytes against other readers while the child is alive.
			// The child is parked at the same durable window on every OS.
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			receiptData, err := os.ReadFile(p.Database + ".manager.lock")
			if err != nil {
				t.Fatal(err)
			}
			var receipt processlock.FreshReceipt
			if err = json.Unmarshal(receiptData, &receipt); err != nil {
				t.Fatal(err)
			}
			if stage == "pending-receipt" || stage == "data-key" {
				if _, e := os.Stat(p.Database); !os.IsNotExist(e) {
					t.Fatal("database created before designated window")
				}
				_, e := os.Stat(p.DataKey)
				if stage == "pending-receipt" && !os.IsNotExist(e) {
					t.Fatal("key created before receipt window")
				}
				if stage == "data-key" && e != nil {
					t.Fatal("key missing at key window", e)
				}
			} else {
				v, e := inspectCheckpoint(t.Context(), receipt.Identity.DatabasePath, &receipt.Identity)
				if e != nil {
					t.Fatal("window checkpoint observation", e)
				}
				if (stage == "checkpoint") != v.Operation.Identity.Valid() {
					t.Fatal("unexpected checkpoint presence at kill window")
				}
			}
			o, err := Open(t.Context(), p, OpenOptions{})
			if err != nil {
				t.Fatal("restart:", err)
			}
			defer o.Close()
			if o.Record.Operation.Identity != receipt.Identity {
				t.Fatal("restart minted identity")
			}
			after, _ := os.Stat(p.Database + ".manager.lock")
			if !os.SameFile(before, after) {
				t.Fatal("lock inode replaced")
			}
			if pending, err := o.Admission.PendingFreshAdmission(); err != nil || pending != nil {
				t.Fatal("pending not finalized", err)
			}
		})
	}
}

func TestAdmissionNegativeControls(t *testing.T) {
	for _, kind := range []string{"malformed", "generation", "installation", "admission", "operation", "path", "ordinary", "unknown-native", "archive", "admin", "runtime", "bootstrap"} {
		t.Run(kind, func(t *testing.T) {
			p := pathsFor(t.TempDir())
			var original bootstrap.FreshIdentity
			if kind == "ordinary" {
				l, err := processlock.Acquire(p.Database)
				if err != nil {
					t.Fatal(err)
				}
				l.Close()
			} else if kind == "unknown-native" {
				db, err := sqlite.Open(p.Database)
				if err != nil {
					t.Fatal(err)
				}
				db.Close()
			} else {
				_, err := Open(t.Context(), p, OpenOptions{Hook: func(stage string) error {
					target := "checkpoint"
					if kind == "archive" || kind == "admin" || kind == "runtime" || kind == "bootstrap" {
						target = "schema"
					}
					if stage == target {
						return errors.New("crash")
					}
					return nil
				}})
				if err == nil {
					t.Fatal("hook missing")
				}
				file := p.Database + ".manager.lock"
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				var receipt processlock.FreshReceipt
				json.Unmarshal(data, &receipt)
				original = receipt.Identity
				switch kind {
				case "malformed":
					data = []byte("garbled")
				case "generation":
					receipt.Identity.Generation = strings.Repeat("a", 32)
				case "installation":
					receipt.Identity.InstallationID = strings.Repeat("b", 32)
				case "admission":
					receipt.Identity.AdmissionID = strings.Repeat("c", 32)
				case "operation":
					receipt.Identity.OperationID = bootstrap.OperationID("fresh-" + strings.Repeat("d", 32))
				case "path":
					receipt.Identity.DatabasePath += ".foreign"
				case "archive":
					if err = os.Mkdir(p.Archives, 0700); err != nil {
						t.Fatal(err)
					}
				case "admin", "runtime", "bootstrap":
					db, e := sqlite.Open(p.Database)
					if e != nil {
						t.Fatal(e)
					}
					key := map[string]string{"admin": "admin_credential_v1", "runtime": "runtime_desired_v1", "bootstrap": "bootstrap_state_v1"}[kind]
					_, e = db.Exec("insert into settings(key,value,updated_at_ms) values(?,'{}',1)", key)
					db.Close()
					if e != nil {
						t.Fatal(e)
					}
				}
				if kind != "malformed" {
					data, _ = json.Marshal(receipt)
				}
				if err = os.WriteFile(file, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if o, err := Open(t.Context(), p, OpenOptions{}); err == nil {
				defer o.Close()
				t.Fatalf("negative accepted %s original=%v", kind, original)
			}
		})
	}
}

func TestHostAdminMaterializesAtomicallyAndCannotOverride(t *testing.T) {
	p := pathsFor(t.TempDir())
	key := "cpamp_HostAuthority0123456789!"
	o, err := Open(t.Context(), p, OpenOptions{AdminKey: key})
	if err != nil {
		t.Fatal(err)
	}
	if !o.Record.Operation.AdminInitialized || o.Record.Admin == nil || !security.VerifyAdminKey(*o.Record.Admin, key) {
		t.Fatal("host not canonical")
	}
	s := New(freshstore.New(o.DB))
	h, err := s.Issue(t.Context())
	if err != nil || !h.Resume {
		t.Fatal("host did not resume")
	}
	o.Close()
	o, err = Open(t.Context(), p, OpenOptions{AdminKey: key})
	if err != nil {
		t.Fatal(err)
	}
	o.Close()
	if o, err = Open(t.Context(), p, OpenOptions{AdminKey: "cpamp_DifferentAuthority0123456789!"}); err == nil {
		o.Close()
		t.Fatal("env override accepted")
	}
}

func TestFreshCheckpointCannotEscapeToHistoricalStartup(t *testing.T) {
	for _, marker := range []string{"retained", "deleted", "empty", "malformed-checkpoint"} {
		t.Run(marker, func(t *testing.T) {
			p := pathsFor(t.TempDir())
			o, err := Open(t.Context(), p, OpenOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if marker == "malformed-checkpoint" {
				if _, err = o.DB.Exec("update settings set value='malformed' where key=?", freshstore.OperationKey); err != nil {
					t.Fatal(err)
				}
			}
			if err = o.Close(); err != nil {
				t.Fatal(err)
			}
			if marker == "deleted" {
				if err = os.Remove(p.Database + ".manager.lock"); err != nil {
					t.Fatal(err)
				}
			}
			if marker == "empty" {
				if err = os.WriteFile(p.Database+".manager.lock", nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if admission, err := legacypreflight.AdmitStartup(t.Context(), p, legacypreflight.Options{}); err == nil {
				admission.Close()
				t.Fatal("Fresh checkpoint entered historical startup")
			}
			if marker != "retained" {
				if reopened, err := Open(t.Context(), p, OpenOptions{}); err == nil {
					reopened.Close()
					t.Fatal("damaged Fresh evidence resumed")
				}
			}
		})
	}
}

func TestPendingSchemaWithRetainedProductRowsNeverBecomesFresh(t *testing.T) {
	for name, statement := range map[string]string{
		"archived-ledger":  `insert into usage_event_identity_ledger(event_hash,raw_event_id,timestamp_ms,bucket_ms,first_seen_at_ms,updated_at_ms) values('archived',null,1,1,1,1)`,
		"codex-inspection": `insert into codex_inspection_runs(trigger_type,status,started_at_ms,settings_json,created_at_ms,updated_at_ms) values('manual','completed',1,'{}',1,1)`,
		"account-quota":    `insert into account_quota_observations(observation_hash,account_key,provider,source,inventory_scope_key,inventory_mode,observed_at_ms,created_at_ms) values('observed','account','codex','quota','scope','full',1,1)`,
		"unknown-product":  `create table unknown_product_state(value text); insert into unknown_product_state values('source')`,
	} {
		t.Run(name, func(t *testing.T) {
			p := pathsFor(t.TempDir())
			_, err := Open(t.Context(), p, OpenOptions{Hook: func(stage string) error {
				if stage == "schema" {
					return errors.New("stop before checkpoint")
				}
				return nil
			}})
			if err == nil {
				t.Fatal("missing window")
			}
			before, err := os.ReadFile(p.Database + ".manager.lock")
			if err != nil {
				t.Fatal(err)
			}
			db, err := sqlite.Open(p.Database)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(statement); err != nil {
				db.Close()
				t.Fatal(err)
			}
			db.Close()
			if o, err := Open(t.Context(), p, OpenOptions{}); err == nil {
				o.Close()
				t.Fatal("retained source became Fresh")
			}
			after, err := os.ReadFile(p.Database + ".manager.lock")
			if err != nil || string(after) != string(before) {
				t.Fatal("negative recovery rewrote original admission", err)
			}
		})
	}
}
