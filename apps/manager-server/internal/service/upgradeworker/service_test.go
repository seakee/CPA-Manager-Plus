package upgradeworker_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradewizard"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgradeworker"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	testInheritedAdminKey = "cpamp_LegacyStandardAdmin0123456789abcdef"
	testExternalMgmtKey   = "external-v8-mgmt-secret"
	testPublicOrigin      = "http://manager.test:8380"
)

func newLegacySourceAndCopiedTarget(t *testing.T, withParityGap bool) (sourceRoot, targetRoot, bundleDir string) {
	t.Helper()
	rawSource := t.TempDir()
	sourceRoot, err := filepath.EvalSymlinks(rawSource)
	if err != nil {
		t.Fatalf("resolve source root: %v", err)
	}
	rawTarget := t.TempDir()
	targetRoot, err = filepath.EvalSymlinks(rawTarget)
	if err != nil {
		t.Fatalf("resolve target root: %v", err)
	}
	rawBundle := t.TempDir()
	bundleDir, err = filepath.EvalSymlinks(rawBundle)
	if err != nil {
		t.Fatalf("resolve bundle root: %v", err)
	}
	bundleDir = filepath.Join(bundleDir, "bundle")

	dbPath := filepath.Join(sourceRoot, "usage.sqlite")
	db, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("open source db: %v", err)
	}
	cred, err := security.NewAdminCredential(testInheritedAdminKey, "legacy")
	if err != nil {
		_ = db.Close()
		t.Fatalf("create admin credential: %v", err)
	}
	if err := setting.New(db).SaveAdminCredential(context.Background(), cred); err != nil {
		_ = db.Close()
		t.Fatalf("save admin credential: %v", err)
	}
	eventTS := int64(1772323200000) // 2026-03-01T00:00:00Z
	if _, err := db.Exec(`insert into usage_events (event_hash, timestamp_ms, timestamp, model, requested_model, created_at_ms)
		values ('legacy-event-1', ?, '2026-03-01T00:00:00Z', 'gpt-test', 'gpt-test', ?)`, eventTS, eventTS); err != nil {
		_ = db.Close()
		t.Fatalf("seed usage event: %v", err)
	}
	if withParityGap {
		// Seed a pre-v1.14.6 deleted-raw daily summary day so Preflight and
		// Reconcile observe a deterministic historical parity gap on 2026-02-15.
		gapDay := int64(20499) // 2026-02-15 UTC epoch day
		gapTS := gapDay * 86400 * 1000
		if _, err := db.Exec(`insert into usage_archive_deleted_coverage_daily (utc_day, deleted_event_count, min_timestamp_ms, max_timestamp_ms)
			values (?, 3, ?, ?)`, gapDay, gapTS, gapTS+1000); err != nil {
			_ = db.Close()
			t.Fatalf("seed parity gap day: %v", err)
		}
	}

	conn, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		t.Fatalf("open source conn: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), `pragma foreign_keys = off`); err != nil {
		_ = conn.Close()
		_ = db.Close()
		t.Fatalf("disable foreign keys: %v", err)
	}
	for _, pattern := range []string{`gateway\_%`, `fresh\_%`, `upgrade\_%`, `usage\_coverage\_incomplete\_days`} {
		rows, err := conn.QueryContext(context.Background(),
			`select name from sqlite_master where type = 'table' and name like ? escape '\'`, pattern)
		if err != nil {
			_ = conn.Close()
			_ = db.Close()
			t.Fatalf("query v2 tables: %v", err)
		}
		var names []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				_ = rows.Close()
				_ = conn.Close()
				_ = db.Close()
				t.Fatalf("scan table: %v", err)
			}
			names = append(names, name)
		}
		_ = rows.Close()
		for _, name := range names {
			if _, err := conn.ExecContext(context.Background(), `drop table `+name); err != nil {
				_ = conn.Close()
				_ = db.Close()
				t.Fatalf("drop table %s: %v", name, err)
			}
		}
	}
	_ = conn.Close()
	if err := db.Close(); err != nil {
		t.Fatalf("close source db: %v", err)
	}

	if _, _, err := security.LoadOrCreateDataKey("", filepath.Join(sourceRoot, "data.key")); err != nil {
		t.Fatalf("create source data key: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sourceRoot, "usage-archives", "2026-03"), 0o755); err != nil {
		t.Fatalf("mkdir source archives: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "usage-archives", "2026-03", "segment-000001.jsonl"), []byte(`{"events":1}`+"\n"), 0o600); err != nil {
		t.Fatalf("write source archive: %v", err)
	}
	if err := os.WriteFile(dbPath+".manager.lock", nil, 0o600); err != nil {
		t.Fatalf("write source lock: %v", err)
	}

	sourceLock, err := processlock.AcquireExistingReadOnly(upgradeservice.SourceDatabasePath(sourceRoot))
	if err != nil {
		t.Fatalf("acquire source lock: %v", err)
	}
	defer func() { _ = sourceLock.Close() }()

	if _, err := upgradeservice.CopyIn(context.Background(), upgradeservice.Options{
		LegacyRoot: sourceRoot,
		TargetRoot: targetRoot,
		BundleDir:  bundleDir,
		Lock:       sourceLock,
	}); err != nil {
		t.Fatalf("copy in target: %v", err)
	}
	return sourceRoot, targetRoot, bundleDir
}

func newShortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "uw-*")
	if err != nil {
		t.Fatalf("mkdir short socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "w.sock")
}

func serveWorkerOverUnixSocket(t *testing.T, ctx context.Context, socketPath string, handler http.Handler) {
	t.Helper()
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	srv := &http.Server{Handler: handler}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = listener.Close()
		<-done
	})
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
}

func startExternalV8Server(t *testing.T, blockProbe <-chan struct{}, probeEntered chan<- struct{}) *httptest.Server {
	t.Helper()
	var notifyOnce sync.Once
	const v8ConfigJSON = `{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"/auth"},"access":{"api-keys":["client-key-1"]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-test"}]}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+testExternalMgmtKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if blockProbe != nil && r.URL.Path == "/v0/management/config" {
			if probeEntered != nil {
				notifyOnce.Do(func() { close(probeEntered) })
			}
			select {
			case <-blockProbe:
			case <-r.Context().Done():
				return
			}
		}
		switch r.URL.Path {
		case "/v0/management/config":
			w.Header().Set("X-CPA-Version", "v8.0.55")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case "/v8/management/config":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(v8ConfigJSON))
		case "/v8/management/config/access/api-keys":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`["client-key-1"]`))
		case "/v8/management/config.yaml":
			w.Header().Set("Content-Type", "application/yaml")
			_, _ = w.Write([]byte("port: 8317\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func waitUntilIdle(t *testing.T, svc *upgradeworker.Service) upgradeworker.Execution {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		state := svc.Execution()
		if !state.Running {
			return state
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("worker did not finish job before deadline; last execution = %+v", svc.Execution())
	return upgradeworker.Execution{}
}

func TestInspectRequiredEnforcesAuthoritativeCompletionAndRefusesFreshOrIncomplete(t *testing.T) {
	ctx := context.Background()

	// 1. Fresh/empty directory must return an error, never required=false.
	emptyRoot := t.TempDir()
	if required, err := upgradeworker.InspectRequired(ctx, emptyRoot); err == nil || !required && err == nil {
		t.Fatalf("empty root: required=%v err=%v, want error", required, err)
	}

	// 2. Valid copied-in unfinished target requires the worker.
	_, targetRoot, _ := newLegacySourceAndCopiedTarget(t, false)
	required, err := upgradeworker.InspectRequired(ctx, targetRoot)
	if err != nil || !required {
		t.Fatalf("copied-in target: required=%v err=%v, want true, nil", required, err)
	}

	// 3. A raw stage='complete' row without authoritative verification/routing
	// must fail closed with an error rather than reporting required=false.
	db, err := sqliterepo.Open(upgradeservice.TargetDatabasePath(targetRoot))
	if err != nil {
		t.Fatalf("open target db: %v", err)
	}
	if _, err := db.Exec(`update upgrade_operations set stage = ?`, string(model.UpgradeStageComplete)); err != nil {
		_ = db.Close()
		t.Fatalf("force stage complete: %v", err)
	}
	_ = db.Close()

	if required, err := upgradeworker.InspectRequired(ctx, targetRoot); err == nil {
		t.Fatalf("unrouted/unverified complete row returned required=%v, nil; want error", required)
	}
}

func TestWorkerEndToEndExternalFlowActivationAndChildRestartRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, true)
	externalCPA := startExternalV8Server(t, nil, nil)

	cfg := config.Config{
		DBPath:      upgradeservice.TargetDatabasePath(targetRoot),
		DataKeyPath: filepath.Join(targetRoot, "data.key"),
	}

	var restartCalled atomic.Int32
	restartSeenResponse := make(chan bool, 1)
	var clientReceivedActivate atomic.Bool

	buildWorker := func(workerCtx context.Context) *upgradeworker.Service {
		t.Helper()
		svc, err := upgradeworker.New(workerCtx, upgradeworker.Options{
			Config:          cfg,
			TargetRoot:      targetRoot,
			SourceRoot:      sourceRoot,
			BundleDir:       bundleDir,
			PublicOrigin:    testPublicOrigin,
			RepairOwnership: func(string) error { return nil },
			RestartParent: func() error {
				restartCalled.Add(1)
				go func() {
					time.Sleep(50 * time.Millisecond)
					restartSeenResponse <- clientReceivedActivate.Load()
				}()
				return nil
			},
		})
		if err != nil {
			t.Fatalf("upgradeworker.New: %v", err)
		}
		svc.SetHandler(&upgradewizard.Handler{
			LegacyRoot:           func() string { return sourceRoot },
			Database:             func() string { return cfg.DBPath },
			DataKey:              func() string { return cfg.DataKeyPath },
			PublicOrigin:         testPublicOrigin,
			Authenticate:         svc.Authenticate,
			Execution:            svc.Execution,
			ReconciliationDays:   svc.ReconciliationDays,
			ReconciliationState:  svc.ReconciliationState,
			PreserveManagerStore: true,
		})
		return svc
	}

	worker := buildWorker(ctx)
	socketPath := newShortSocketPath(t)
	serveWorkerOverUnixSocket(t, ctx, socketPath, worker)
	client := upgradeworker.NewClient(socketPath, testPublicOrigin)
	publicWizard := &upgradewizard.Handler{
		Worker:               client,
		PublicOrigin:         testPublicOrigin,
		Database:             func() string { return cfg.DBPath },
		DataKey:              func() string { return cfg.DataKeyPath },
		PreserveManagerStore: true,
	}

	doWizard := func(method, suffix, auth, origin, body string) *httptest.ResponseRecorder {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1"+suffix, reader)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		publicWizard.ServeHTTP(rec, req)
		return rec
	}

	decodeErrCode := func(raw []byte) string {
		t.Helper()
		var doc struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("decode error JSON %q: %v", string(raw), err)
		}
		return doc.Error.Code
	}

	// Gate order: unauthenticated and unmatching prefixed credentials get 401
	// with error.code == "admin_unauthorized" before origin, path, or method.
	unauthRec := doWizard(http.MethodPost, "/installation/upgrade/unknown", "", "https://other.example", "")
	if unauthRec.Code != http.StatusUnauthorized || decodeErrCode(unauthRec.Body.Bytes()) != "admin_unauthorized" {
		t.Fatalf("unauthenticated unknown path = %d (%s), want 401 admin_unauthorized", unauthRec.Code, unauthRec.Body.String())
	}
	for _, wrongKey := range []string{"cpamp_wrong_key_0123456789", "cmp_admin_wrong_key_0123456789"} {
		wrongRec := doWizard(http.MethodGet, "/installation/upgrade/status", "Bearer "+wrongKey, testPublicOrigin, "")
		if wrongRec.Code != http.StatusUnauthorized || decodeErrCode(wrongRec.Body.Bytes()) != "admin_unauthorized" {
			t.Fatalf("wrong prefixed key %q = %d (%s), want 401 admin_unauthorized", wrongKey, wrongRec.Code, wrongRec.Body.String())
		}
	}
	if rec := doWizard(http.MethodGet, "/installation/upgrade/status", "Bearer "+testInheritedAdminKey, "https://other.example", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want 403", rec.Code)
	}
	if rec := doWizard(http.MethodPost, "/installation/upgrade/verify", "Bearer "+testInheritedAdminKey, testPublicOrigin, `{}`); rec.Code != http.StatusConflict || decodeErrCode(rec.Body.Bytes()) != "upgrade_execution_required" {
		t.Fatalf("direct synchronous verify in worker mode = %d (%s), want 409 upgrade_execution_required", rec.Code, rec.Body.String())
	}

	// Entry authentication uses cpamp.upgrade-auth.v1 and survives even if target
	// WAL becomes unreadable to unprivileged callers (in-memory digest).
	entryReq := httptest.NewRequest(http.MethodPost, testPublicOrigin+upgradeworker.EntryPath, nil)
	entryReq.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
	entryReq.Header.Set("Origin", testPublicOrigin)
	entryRec := httptest.NewRecorder()
	client.ServeHTTP(entryRec, entryReq)
	if entryRec.Code != http.StatusOK {
		t.Fatalf("entry authenticate = %d, body = %s", entryRec.Code, entryRec.Body.String())
	}

	// Initial status at committed copy_in projects nextAction="reconcile" and
	// pre-observed parity gap day 2026-02-15.
	statusRec := doWizard(http.MethodGet, "/installation/upgrade/status", "Bearer "+testInheritedAdminKey, testPublicOrigin, "")
	if statusRec.Code != http.StatusOK {
		t.Fatalf("initial status = %d, body = %s", statusRec.Code, statusRec.Body.String())
	}
	var statusDoc struct {
		Stage          string `json:"stage"`
		NextAction     string `json:"nextAction"`
		Reconciliation *struct {
			MismatchDays []string `json:"mismatchDays"`
		} `json:"reconciliation"`
		Execution *upgradeworker.Execution `json:"execution"`
	}
	if err := json.Unmarshal(statusRec.Body.Bytes(), &statusDoc); err != nil {
		t.Fatalf("decode initial status: %v", err)
	}
	if statusDoc.Stage != "copy_in" || statusDoc.NextAction != "reconcile" {
		t.Fatalf("stage=%q nextAction=%q, want copy_in / reconcile", statusDoc.Stage, statusDoc.NextAction)
	}
	if statusDoc.Execution == nil || !statusDoc.Execution.Available {
		t.Fatalf("execution = %+v, want available=true", statusDoc.Execution)
	}

	// Unacknowledged reconcile fails with typed code and keeps ReconciliationDays observable.
	rec := doWizard(http.MethodPost, "/installation/upgrade/execute", "Bearer "+testInheritedAdminKey, testPublicOrigin, `{"action":"reconcile"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("reconcile unacknowledged = %d, body = %s", rec.Code, rec.Body.String())
	}
	execState := waitUntilIdle(t, worker)
	if execState.Action != "reconcile" || execState.Code == "" {
		t.Fatalf("after unacknowledged reconcile: %+v", execState)
	}
	if days := worker.ReconciliationDays(); len(days) != 1 || days[0] != "2026-02-15" {
		t.Fatalf("ReconciliationDays = %v, want [2026-02-15]", days)
	}

	// Acknowledged reconcile advances stage to reconcile and clears transient days.
	rec = doWizard(http.MethodPost, "/installation/upgrade/execute", "Bearer "+testInheritedAdminKey, testPublicOrigin,
		`{"action":"reconcile","acknowledgeDays":["2026-02-15"],"acknowledgeParityGaps":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("reconcile acknowledged = %d, body = %s", rec.Code, rec.Body.String())
	}
	execState = waitUntilIdle(t, worker)
	if execState.Code != "" || execState.Action != "reconcile" {
		t.Fatalf("after acknowledged reconcile: %+v", execState)
	}
	if days := worker.ReconciliationDays(); len(days) != 0 {
		t.Fatalf("ReconciliationDays after success = %v, want empty", days)
	}

	// External connect with explicit override proves v8 CPA and syncs Manager config.
	extBody, _ := json.Marshal(map[string]any{
		"action":        "external-connect",
		"baseUrl":       externalCPA.URL,
		"managementKey": testExternalMgmtKey,
	})
	rec = doWizard(http.MethodPost, "/installation/upgrade/execute", "Bearer "+testInheritedAdminKey, testPublicOrigin, string(extBody))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("external-connect = %d, body = %s", rec.Code, rec.Body.String())
	}
	execState = waitUntilIdle(t, worker)
	if execState.Code != "" || execState.Action != "external-connect" {
		t.Fatalf("after external-connect: %+v", execState)
	}

	// Configure External management prefix via /routing.
	rec = doWizard(http.MethodPost, "/installation/upgrade/routing", "Bearer "+testInheritedAdminKey, testPublicOrigin, `{"controlBasePath":"/cpamp-admin"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("routing = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Activate before Verify must fail closed with 409.
	rec = doWizard(http.MethodPost, "/installation/upgrade/execute", "Bearer "+testInheritedAdminKey, testPublicOrigin, `{"action":"activate"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("activate before verify = %d, want 409", rec.Code)
	}

	// Verify through the one upgrade.Verify engine reaches authoritative completion.
	rec = doWizard(http.MethodPost, "/installation/upgrade/execute", "Bearer "+testInheritedAdminKey, testPublicOrigin, `{"action":"verify"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("verify = %d, body = %s", rec.Code, rec.Body.String())
	}
	execState = waitUntilIdle(t, worker)
	if execState.Code != "" || execState.Action != "verify" {
		t.Fatalf("after verify: %+v", execState)
	}

	// InspectRequired now reports required=false (authoritative complete).
	required, err := upgradeworker.InspectRequired(ctx, targetRoot)
	if err != nil || required {
		t.Fatalf("InspectRequired after verify: required=%v err=%v, want false, nil", required, err)
	}

	// Simulate upgrade-worker child restart after Verify committed complete
	// (while Manager is still on bootstrap Upgrade-only topology).
	restartedCtx, cancelRestarted := context.WithCancel(context.Background())
	defer cancelRestarted()
	restartedWorker := buildWorker(restartedCtx)
	restartedSocket := newShortSocketPath(t)
	serveWorkerOverUnixSocket(t, restartedCtx, restartedSocket, restartedWorker)
	publicWizard.Worker = upgradeworker.NewClient(restartedSocket, testPublicOrigin)

	statusRec = doWizard(http.MethodGet, "/installation/upgrade/status", "Bearer "+testInheritedAdminKey, testPublicOrigin, "")
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status after child restart = %d, body = %s", statusRec.Code, statusRec.Body.String())
	}
	if err := json.Unmarshal(statusRec.Body.Bytes(), &statusDoc); err != nil {
		t.Fatalf("decode status after restart: %v", err)
	}
	if statusDoc.Stage != "complete" || statusDoc.Execution == nil || !statusDoc.Execution.Available || statusDoc.Execution.Action != "" {
		t.Fatalf("status after child restart: stage=%q execution=%+v", statusDoc.Stage, statusDoc.Execution)
	}

	// Activate on the restarted worker commits HTTP 200 before signaling parent.
	rec = doWizard(http.MethodPost, "/installation/upgrade/execute", "Bearer "+testInheritedAdminKey, testPublicOrigin, `{"action":"activate"}`)
	clientReceivedActivate.Store(true)
	if rec.Code != http.StatusOK {
		t.Fatalf("activate = %d, body = %s", rec.Code, rec.Body.String())
	}
	var activateDoc upgradeworker.Execution
	if err := json.Unmarshal(rec.Body.Bytes(), &activateDoc); err != nil {
		t.Fatalf("decode activate response: %v", err)
	}
	if activateDoc.Action != "activate" || !activateDoc.Available {
		t.Fatalf("activate response = %+v", activateDoc)
	}
	select {
	case saw := <-restartSeenResponse:
		if !saw {
			t.Fatal("parent restart fired before client received HTTP 200 response")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for RestartParent signal")
	}
	if restartCalled.Load() != 1 {
		t.Fatalf("RestartParent calls = %d, want 1", restartCalled.Load())
	}
	// Explicit operator retry of activate re-invokes RestartParent rather than
	// permanently returning a no-op 200 OK.
	retryRec := doWizard(http.MethodPost, "/installation/upgrade/execute", "Bearer "+testInheritedAdminKey, testPublicOrigin, `{"action":"activate"}`)
	if retryRec.Code != http.StatusOK {
		t.Fatalf("explicit activate retry = %d, body = %s", retryRec.Code, retryRec.Body.String())
	}
	select {
	case <-restartSeenResponse:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for explicit retry RestartParent signal")
	}
	if restartCalled.Load() != 2 {
		t.Fatalf("RestartParent calls after explicit retry = %d, want 2", restartCalled.Load())
	}
}

func TestWorkerSerializesConcurrentMutationsAndStreamsArtifactUploadsAboveJSONLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, false)

	// Advance to reconcile first so external-connect and artifact upload are active.
	sourceLock, err := processlock.AcquireExistingReadOnly(upgradeservice.SourceDatabasePath(sourceRoot))
	if err != nil {
		t.Fatalf("source lock: %v", err)
	}
	targetLock, err := processlock.Acquire(upgradeservice.TargetDatabasePath(targetRoot))
	if err != nil {
		_ = sourceLock.Close()
		t.Fatalf("target lock: %v", err)
	}
	if _, err := upgradeservice.Reconcile(ctx, upgradeservice.Options{
		LegacyRoot: sourceRoot,
		TargetRoot: targetRoot,
		BundleDir:  bundleDir,
		Lock:       sourceLock,
		TargetLock: targetLock,
	}); err != nil {
		_ = targetLock.Close()
		_ = sourceLock.Close()
		t.Fatalf("reconcile: %v", err)
	}
	_ = targetLock.Close()
	_ = sourceLock.Close()

	// Real Runtime Supervisor HTTP server verifying Bearer runtime token and
	// receiving a 96 KiB (> 32 KiB JSON limit) binary upload.
	const supervisorSecret = "runtime-supervisor-secret-token"
	var uploadedBytes atomic.Int64
	artifactID := "sha256:" + strings.Repeat("a", 64)
	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+supervisorSecret {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthorized"}`))
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runtime/artifact":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schemaVersion":1,"pinned":{"version":"v8.0.55","artifactId":"` + artifactID + `"},"active":null,"required":true,"operation":null}`))
		case r.Method == http.MethodPut && r.URL.Path == "/v1/runtime/artifact/upload":
			n, copyErr := io.Copy(io.Discard, r.Body)
			if copyErr != nil {
				http.Error(w, copyErr.Error(), http.StatusBadRequest)
				return
			}
			uploadedBytes.Store(n)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"operation":{"id":"op-upload-1","source":"upload","phase":"succeeded","receivedBytes":98304,"totalBytes":98304,"code":""}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(supervisor.Close)

	tokenFile := filepath.Join(t.TempDir(), "runtime-secret")
	if err := os.WriteFile(tokenFile, []byte(supervisorSecret+"\n"), 0o600); err != nil {
		t.Fatalf("write runtime token: %v", err)
	}
	tokenSource := runtimeservice.NewFileRuntimeTokenSource(tokenFile)
	artifactClient := runtimeservice.NewArtifactClientWithTokenSource(supervisor.URL, tokenSource)

	blockProbe := make(chan struct{})
	probeEntered := make(chan struct{})
	externalCPA := startExternalV8Server(t, blockProbe, probeEntered)

	cfg := config.Config{
		DBPath:           upgradeservice.TargetDatabasePath(targetRoot),
		DataKeyPath:      filepath.Join(targetRoot, "data.key"),
		RuntimeURL:       supervisor.URL,
		RuntimeTokenFile: tokenFile,
	}
	worker, err := upgradeworker.New(ctx, upgradeworker.Options{
		Config:          cfg,
		TargetRoot:      targetRoot,
		SourceRoot:      sourceRoot,
		BundleDir:       bundleDir,
		PublicOrigin:    testPublicOrigin,
		RepairOwnership: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("upgradeworker.New: %v", err)
	}
	worker.SetHandler(&upgradewizard.Handler{
		LegacyRoot:           func() string { return sourceRoot },
		Database:             func() string { return cfg.DBPath },
		DataKey:              func() string { return cfg.DataKeyPath },
		PublicOrigin:         testPublicOrigin,
		Authenticate:         worker.Authenticate,
		Execution:            worker.Execution,
		ReconciliationDays:   worker.ReconciliationDays,
		ReconciliationState:  worker.ReconciliationState,
		Artifact:             artifactClient,
		PreserveManagerStore: true,
	})

	socketPath := newShortSocketPath(t)
	serveWorkerOverUnixSocket(t, ctx, socketPath, worker)
	client := upgradeworker.NewClient(socketPath, testPublicOrigin)
	publicWizard := &upgradewizard.Handler{
		Worker:               client,
		PublicOrigin:         testPublicOrigin,
		Database:             func() string { return cfg.DBPath },
		DataKey:              func() string { return cfg.DataKeyPath },
		PreserveManagerStore: true,
	}

	// Start external-connect job and deterministically pause inside CPA probe.
	extBody, _ := json.Marshal(map[string]any{
		"action":        "external-connect",
		"baseUrl":       externalCPA.URL,
		"managementKey": testExternalMgmtKey,
	})
	startReq := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/execute", bytes.NewReader(extBody))
	startReq.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
	startReq.Header.Set("Origin", testPublicOrigin)
	startReq.Header.Set("Content-Type", "application/json")
	startRec := httptest.NewRecorder()
	publicWizard.ServeHTTP(startRec, startReq)
	if startRec.Code != http.StatusAccepted {
		t.Fatalf("start external-connect = %d, body = %s", startRec.Code, startRec.Body.String())
	}
	select {
	case <-probeEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for external-connect probe to start")
	}

	// Unauthenticated request while busy still returns 401, never 409 busy.
	unauthBusyReq := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/routing", strings.NewReader(`{"controlBasePath":"/cpamp-admin"}`))
	unauthBusyReq.Header.Set("Origin", testPublicOrigin)
	unauthBusyRec := httptest.NewRecorder()
	publicWizard.ServeHTTP(unauthBusyRec, unauthBusyReq)
	if unauthBusyRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated routing while busy = %d, want 401", unauthBusyRec.Code)
	}

	// Authenticated concurrent execute, routing, and artifact upload return 409 busy.
	for _, tc := range []struct {
		method      string
		suffix      string
		contentType string
		body        []byte
	}{
		{http.MethodPost, "/installation/upgrade/execute", "application/json", []byte(`{"action":"verify"}`)},
		{http.MethodPost, "/installation/upgrade/routing", "application/json", []byte(`{"controlBasePath":"/cpamp-admin"}`)},
		{http.MethodPut, "/installation/upgrade/runtime/artifact/upload", "application/gzip", bytes.Repeat([]byte("a"), 1024)},
	} {
		req := httptest.NewRequest(tc.method, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1"+tc.suffix, bytes.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		req.Header.Set("Content-Type", tc.contentType)
		rec := httptest.NewRecorder()
		publicWizard.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s %s while busy = %d, want 409; body = %s", tc.method, tc.suffix, rec.Code, rec.Body.String())
		}
	}

	// GET /status stays responsive (200) while the mutation is running.
	statusReq := httptest.NewRequest(http.MethodGet, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/status", nil)
	statusReq.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
	statusReq.Header.Set("Origin", testPublicOrigin)
	statusRec := httptest.NewRecorder()
	publicWizard.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("GET /status while busy = %d, want 200", statusRec.Code)
	}

	// Release external-connect and wait for idle.
	close(blockProbe)
	waitUntilIdle(t, worker)

	// Execute JSON above 32 KiB remains strictly rejected with 400.
	oversizedJSON := `{"action":"reconcile","acknowledgeDays":["` + strings.Repeat("2026-01-01", 4000) + `"]}`
	bigExecReq := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/execute", strings.NewReader(oversizedJSON))
	bigExecReq.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
	bigExecReq.Header.Set("Origin", testPublicOrigin)
	bigExecReq.Header.Set("Content-Type", "application/json")
	bigExecRec := httptest.NewRecorder()
	publicWizard.ServeHTTP(bigExecRec, bigExecReq)
	if bigExecRec.Code != http.StatusBadRequest {
		t.Fatalf("oversized execute JSON = %d, want 400", bigExecRec.Code)
	}

	// Binary artifact upload above 32 KiB (96 KiB) streams cleanly through
	// Client -> Unix Socket -> Service -> upgradewizard -> ArtifactClient.
	archive := bytes.Repeat([]byte{0x1f, 0x8b, 0x08, 0x00}, 24*1024) // 96 KiB
	uploadReq := httptest.NewRequest(http.MethodPut, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/runtime/artifact/upload", bytes.NewReader(archive))
	uploadReq.ContentLength = int64(len(archive))
	uploadReq.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
	uploadReq.Header.Set("Origin", testPublicOrigin)
	uploadReq.Header.Set("Content-Type", "application/gzip")
	uploadRec := httptest.NewRecorder()
	publicWizard.ServeHTTP(uploadRec, uploadReq)
	if uploadRec.Code != http.StatusOK {
		t.Fatalf("96 KiB artifact upload = %d, body = %s", uploadRec.Code, uploadRec.Body.String())
	}
	if uploadedBytes.Load() != int64(len(archive)) {
		t.Fatalf("uploadedBytes = %d, want %d", uploadedBytes.Load(), len(archive))
	}
}

func TestWorkerAcceptsGeneratedCPAMPAndLegacyCMPAdminKeysAndRejectsMismatchedPrefixedKeys(t *testing.T) {
	generatedCPAMPKey, err := security.GenerateAdminKey()
	if err != nil {
		t.Fatalf("GenerateAdminKey: %v", err)
	}
	for _, validKey := range []string{
		generatedCPAMPKey,
		"cmp_admin_1xFullDockerInheritedSecret987654",
	} {
		t.Run(validKey[:9], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, false)
			dbPath := upgradeservice.TargetDatabasePath(targetRoot)
			db, err := sqliterepo.Open(dbPath)
			if err != nil {
				t.Fatalf("open target db: %v", err)
			}
			cred, err := security.NewAdminCredential(validKey, "legacy")
			if err != nil {
				_ = db.Close()
				t.Fatalf("NewAdminCredential: %v", err)
			}
			if err := setting.New(db).SaveAdminCredential(ctx, cred); err != nil {
				_ = db.Close()
				t.Fatalf("SaveAdminCredential: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("close target db: %v", err)
			}

			cfg := config.Config{DBPath: dbPath, DataKeyPath: filepath.Join(targetRoot, "data.key")}
			worker, err := upgradeworker.New(ctx, upgradeworker.Options{
				Config:          cfg,
				TargetRoot:      targetRoot,
				SourceRoot:      sourceRoot,
				BundleDir:       bundleDir,
				PublicOrigin:    testPublicOrigin,
				RepairOwnership: func(string) error { return nil },
			})
			if err != nil {
				t.Fatalf("upgradeworker.New: %v", err)
			}
			worker.SetHandler(&upgradewizard.Handler{
				LegacyRoot:           func() string { return sourceRoot },
				Database:             func() string { return cfg.DBPath },
				DataKey:              func() string { return cfg.DataKeyPath },
				PublicOrigin:         testPublicOrigin,
				Authenticate:         worker.Authenticate,
				Execution:            worker.Execution,
				PreserveManagerStore: true,
			})
			socketPath := newShortSocketPath(t)
			serveWorkerOverUnixSocket(t, ctx, socketPath, worker)
			client := upgradeworker.NewClient(socketPath, testPublicOrigin)
			publicWizard := &upgradewizard.Handler{
				Worker:               client,
				PublicOrigin:         testPublicOrigin,
				Database:             func() string { return cfg.DBPath },
				DataKey:              func() string { return cfg.DataKeyPath },
				PreserveManagerStore: true,
			}

			// Matching prefixed key succeeds on both EntryPath and /status.
			entryReq := httptest.NewRequest(http.MethodPost, testPublicOrigin+upgradeworker.EntryPath, nil)
			entryReq.Header.Set("Authorization", "Bearer "+validKey)
			entryReq.Header.Set("Origin", testPublicOrigin)
			entryRec := httptest.NewRecorder()
			client.ServeHTTP(entryRec, entryReq)
			if entryRec.Code != http.StatusOK {
				t.Fatalf("entry authenticate with %q = %d: %s", validKey[:9], entryRec.Code, entryRec.Body.String())
			}

			statusReq := httptest.NewRequest(http.MethodGet, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/status", nil)
			statusReq.Header.Set("Authorization", "Bearer "+validKey)
			statusReq.Header.Set("Origin", testPublicOrigin)
			statusRec := httptest.NewRecorder()
			publicWizard.ServeHTTP(statusRec, statusReq)
			if statusRec.Code != http.StatusOK {
				t.Fatalf("status with %q = %d: %s", validKey[:9], statusRec.Code, statusRec.Body.String())
			}

			// Non-matching same-prefix keys still fail closed with 401 admin_unauthorized.
			for _, badKey := range []string{"cpamp_differentSecret0000", "cmp_admin_differentSecret0000"} {
				badEntryReq := httptest.NewRequest(http.MethodPost, testPublicOrigin+upgradeworker.EntryPath, nil)
				badEntryReq.Header.Set("Authorization", "Bearer "+badKey)
				badEntryReq.Header.Set("Origin", testPublicOrigin)
				badEntryRec := httptest.NewRecorder()
				client.ServeHTTP(badEntryRec, badEntryReq)
				if badEntryRec.Code != http.StatusUnauthorized {
					t.Fatalf("entry with mismatched %q = %d, want 401", badKey, badEntryRec.Code)
				}

				badStatusReq := httptest.NewRequest(http.MethodGet, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/status", nil)
				badStatusReq.Header.Set("Authorization", "Bearer "+badKey)
				badStatusReq.Header.Set("Origin", testPublicOrigin)
				badStatusRec := httptest.NewRecorder()
				publicWizard.ServeHTTP(badStatusRec, badStatusReq)
				var badErrDoc struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal(badStatusRec.Body.Bytes(), &badErrDoc); err != nil {
					t.Fatalf("decode 401 JSON: %v (%s)", err, badStatusRec.Body.String())
				}
				if badStatusRec.Code != http.StatusUnauthorized || badErrDoc.Error.Code != "admin_unauthorized" {
					t.Fatalf("status with mismatched %q = %d (%+v), want 401 admin_unauthorized", badKey, badStatusRec.Code, badErrDoc)
				}
			}
		})
	}
}

func TestWorkerExternalConnectIdempotencySealedBindingFallbackAndLegacyCollectorSettings(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, false)
	dbPath := upgradeservice.TargetDatabasePath(targetRoot)
	dataKeyPath := filepath.Join(targetRoot, "data.key")

	sourceLock, err := processlock.AcquireExistingReadOnly(upgradeservice.SourceDatabasePath(sourceRoot))
	if err != nil {
		t.Fatalf("source lock: %v", err)
	}
	targetLock, err := processlock.Acquire(dbPath)
	if err != nil {
		_ = sourceLock.Close()
		t.Fatalf("target lock: %v", err)
	}
	if _, err := upgradeservice.Reconcile(ctx, upgradeservice.Options{
		LegacyRoot: sourceRoot,
		TargetRoot: targetRoot,
		BundleDir:  bundleDir,
		Lock:       sourceLock,
		TargetLock: targetLock,
	}); err != nil {
		_ = targetLock.Close()
		_ = sourceLock.Close()
		t.Fatalf("reconcile: %v", err)
	}
	_ = targetLock.Close()
	_ = sourceLock.Close()

	// External v8 CPA without X-CPA-Version header (relies on CPAVersion option).
	const v8ConfigJSON = `{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"/auth"},"access":{"api-keys":["client-key-1"]}}`
	headerlessCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-test"}]}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+testExternalMgmtKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v0/management/config":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case "/v8/management/config":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(v8ConfigJSON))
		case "/v8/management/config/access/api-keys":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`["client-key-1"]`))
		case "/v8/management/config.yaml":
			w.Header().Set("Content-Type", "application/yaml")
			_, _ = w.Write([]byte("port: 8317\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(headerlessCPA.Close)

	// Seed a valid historical 1.x setup-only row (with older management key and
	// custom collector Queue/PopSide, without manager_config_v1).
	keyBytes, err := security.LoadExistingDataKey("", dataKeyPath)
	if err != nil {
		t.Fatalf("load data key: %v", err)
	}
	protector, err := security.NewProtector(keyBytes)
	if err != nil {
		t.Fatalf("new protector: %v", err)
	}
	st, err := store.Open(dbPath, protector)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.SaveSetup(ctx, model.Setup{
		CPAUpstreamURL: headerlessCPA.URL,
		ManagementKey:  "stale-1x-mgmt-key",
		Queue:          "legacy:collector:queue",
		PopSide:        "left",
	}); err != nil {
		_ = st.Close()
		t.Fatalf("save legacy setup: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	cfg := config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath}
	worker, err := upgradeworker.New(ctx, upgradeworker.Options{
		Config:          cfg,
		TargetRoot:      targetRoot,
		SourceRoot:      sourceRoot,
		BundleDir:       bundleDir,
		CPAVersion:      "v8.0.55",
		PublicOrigin:    testPublicOrigin,
		RepairOwnership: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("upgradeworker.New: %v", err)
	}
	worker.SetHandler(&upgradewizard.Handler{
		LegacyRoot:           func() string { return sourceRoot },
		Database:             func() string { return cfg.DBPath },
		DataKey:              func() string { return cfg.DataKeyPath },
		PublicOrigin:         testPublicOrigin,
		Authenticate:         worker.Authenticate,
		Execution:            worker.Execution,
		PreserveManagerStore: true,
	})
	socketPath := newShortSocketPath(t)
	serveWorkerOverUnixSocket(t, ctx, socketPath, worker)
	publicWizard := &upgradewizard.Handler{
		Worker:               upgradeworker.NewClient(socketPath, testPublicOrigin),
		PublicOrigin:         testPublicOrigin,
		Database:             func() string { return cfg.DBPath },
		DataKey:              func() string { return cfg.DataKeyPath },
		PreserveManagerStore: true,
	}

	doExec := func(body string) upgradeworker.Execution {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/execute", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		publicWizard.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("execute %s = %d: %s", body, rec.Code, rec.Body.String())
		}
		return waitUntilIdle(t, worker)
	}

	// 1. Initial explicit external-connect updates the connection and preserves
	// legacy setup-only collector Queue/PopSide in manager_config_v1.
	extBody, _ := json.Marshal(map[string]any{
		"action":        "external-connect",
		"baseUrl":       headerlessCPA.URL,
		"managementKey": testExternalMgmtKey,
	})
	if state := doExec(string(extBody)); state.Code != "" {
		t.Fatalf("first explicit external-connect failed: %+v", state)
	}

	verifyStore, err := store.Open(dbPath, protector)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	savedCfg, found, err := verifyStore.LoadManagerConfig(ctx)
	_ = verifyStore.Close()
	if err != nil || !found {
		t.Fatalf("LoadManagerConfig = (found=%v, err=%v)", found, err)
	}
	if savedCfg.Collector.Queue != "legacy:collector:queue" || savedCfg.Collector.PopSide != "left" {
		t.Fatalf("Collector = %+v, want Queue=legacy:collector:queue PopSide=left", savedCfg.Collector)
	}
	if savedCfg.CPAConnection.CPABaseURL != headerlessCPA.URL || savedCfg.CPAConnection.ManagementKey != testExternalMgmtKey {
		t.Fatalf("CPAConnection = %+v", savedCfg.CPAConnection)
	}

	// 2. Repeating the identical explicit external-connect against headerless CPA
	// takes the idempotent path (reprove=false) instead of failing with external_target_moved.
	if state := doExec(string(extBody)); state.Code != "" {
		t.Fatalf("idempotent explicit external-connect failed: %+v", state)
	}

	// 3. Remove persisted settings connection authority rows so the sealed
	// ExternalBinding is the sole authority, then run external-connect without override.
	rawDB, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	if _, err := rawDB.ExecContext(ctx, `delete from settings where key in ('manager_config_v1', 'setup', 'cpa_connection_revision_v1')`); err != nil {
		_ = rawDB.Close()
		t.Fatalf("clear settings connection rows: %v", err)
	}
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close sqlite db: %v", err)
	}
	if state := doExec(`{"action":"external-connect"}`); state.Code != "" {
		t.Fatalf("sealed-binding external-connect failed: %+v", state)
	}

	restoredStore, err := store.Open(dbPath, protector)
	if err != nil {
		t.Fatalf("reopen restored store: %v", err)
	}
	restoredCfg, restoredFound, err := restoredStore.LoadManagerConfig(ctx)
	_ = restoredStore.Close()
	if err != nil || !restoredFound || restoredCfg.CPAConnection.CPABaseURL != headerlessCPA.URL || restoredCfg.CPAConnection.ManagementKey != testExternalMgmtKey {
		t.Fatalf("restored CPAConnection = (found=%v, err=%v, cfg=%+v)", restoredFound, err, restoredCfg.CPAConnection)
	}
}

func TestWorkerReconcileRetryAfterTargetDriftWithholdsDaysAndTransitionsOnAcknowledgment(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, false)
	dbPath := upgradeservice.TargetDatabasePath(targetRoot)
	dataKeyPath := filepath.Join(targetRoot, "data.key")
	cfg := config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath}

	worker, err := upgradeworker.New(ctx, upgradeworker.Options{
		Config:          cfg,
		TargetRoot:      targetRoot,
		SourceRoot:      sourceRoot,
		BundleDir:       bundleDir,
		CPAVersion:      "v8.0.55",
		PublicOrigin:    testPublicOrigin,
		RepairOwnership: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("upgradeworker.New: %v", err)
	}
	worker.SetHandler(&upgradewizard.Handler{
		LegacyRoot:           func() string { return sourceRoot },
		Database:             func() string { return cfg.DBPath },
		DataKey:              func() string { return cfg.DataKeyPath },
		PublicOrigin:         testPublicOrigin,
		Authenticate:         worker.Authenticate,
		Execution:            worker.Execution,
		ReconciliationDays:   worker.ReconciliationDays,
		ReconciliationState:  worker.ReconciliationState,
		PreserveManagerStore: true,
	})
	socketPath := newShortSocketPath(t)
	serveWorkerOverUnixSocket(t, ctx, socketPath, worker)
	publicWizard := &upgradewizard.Handler{
		Worker:               upgradeworker.NewClient(socketPath, testPublicOrigin),
		PublicOrigin:         testPublicOrigin,
		Database:             func() string { return cfg.DBPath },
		DataKey:              func() string { return cfg.DataKeyPath },
		PreserveManagerStore: true,
	}

	type statusSnapshot struct {
		Stage          string `json:"stage"`
		NextAction     string `json:"nextAction"`
		Reconciliation struct {
			MismatchDays          []string `json:"mismatchDays"`
			MismatchDaysTotal     int      `json:"mismatchDaysTotal"`
			MismatchDaysTruncated bool     `json:"mismatchDaysTruncated"`
		} `json:"reconciliation"`
		Execution *upgradeworker.Execution `json:"execution"`
	}
	readStatus := func() statusSnapshot {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/status", nil)
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		rec := httptest.NewRecorder()
		publicWizard.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /status = %d: %s", rec.Code, rec.Body.String())
		}
		var doc statusSnapshot
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatalf("decode status JSON: %v (%s)", err, rec.Body.String())
		}
		return doc
	}
	doExec := func(body string) upgradeworker.Execution {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/execute", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		publicWizard.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("execute %s = %d: %s", body, rec.Code, rec.Body.String())
		}
		return waitUntilIdle(t, worker)
	}

	// 1. Initial clean reconcile succeeds with zero mismatch days and advances nextAction to cpa_import.
	if state := doExec(`{"action":"reconcile"}`); state.Code != "" {
		t.Fatalf("clean reconcile failed: %+v", state)
	}
	cleanStatus := readStatus()
	if cleanStatus.Stage != "reconcile" || cleanStatus.NextAction != "cpa_import" || len(cleanStatus.Reconciliation.MismatchDays) != 0 {
		t.Fatalf("clean status = %+v, want stage=reconcile nextAction=cpa_import mismatchDays=[]", cleanStatus)
	}

	// 2. Introduce target consumer usage_events token drift on 2026-03-01 without touching 1.x source.
	targetDB, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("open target db: %v", err)
	}
	if _, err := targetDB.ExecContext(ctx, `update usage_events set total_tokens = total_tokens + 1 where event_hash = 'legacy-event-1'`); err != nil {
		_ = targetDB.Close()
		t.Fatalf("drift target usage_events: %v", err)
	}
	if err := targetDB.Close(); err != nil {
		t.Fatalf("close target db: %v", err)
	}

	// 3. Re-running reconcile without acknowledgment withholds 2026-03-01 and projects nextAction=reconcile.
	driftState := doExec(`{"action":"reconcile"}`)
	if driftState.Action != "reconcile" || driftState.Code != string(upgradeservice.UpgradeErrorReconcileMismatch) {
		t.Fatalf("drift reconcile state = %+v, want upgrade_reconcile_mismatch", driftState)
	}
	withheldStatus := readStatus()
	if withheldStatus.Stage != "reconcile" ||
		withheldStatus.NextAction != "reconcile" ||
		len(withheldStatus.Reconciliation.MismatchDays) != 1 ||
		withheldStatus.Reconciliation.MismatchDays[0] != "2026-03-01" ||
		withheldStatus.Reconciliation.MismatchDaysTotal != 1 ||
		withheldStatus.Reconciliation.MismatchDaysTruncated {
		t.Fatalf("withheld status = %+v, want stage=reconcile nextAction=reconcile mismatchDays=[2026-03-01]", withheldStatus)
	}

	// 4. Forward actions cannot rely on the prior stage='reconcile' row to skip unacknowledged drift.
	skipState := doExec(`{"action":"external-connect","baseUrl":"http://127.0.0.1:9","managementKey":"unused-secret"}`)
	if skipState.Code != string(upgradeservice.UpgradeErrorReconcileMismatch) {
		t.Fatalf("forward action while days unacknowledged = %+v, want upgrade_reconcile_mismatch", skipState)
	}

	// 5. Per-day acknowledgment records incomplete coverage day and transitions nextAction back to cpa_import.
	ackState := doExec(`{"action":"reconcile","acknowledgeDays":["2026-03-01"],"acknowledgeParityGaps":true}`)
	if ackState.Code != "" {
		t.Fatalf("acknowledged retry reconcile failed: %+v", ackState)
	}
	ackedStatus := readStatus()
	if ackedStatus.Stage != "reconcile" ||
		ackedStatus.NextAction != "cpa_import" ||
		len(ackedStatus.Reconciliation.MismatchDays) != 1 ||
		ackedStatus.Reconciliation.MismatchDays[0] != "2026-03-01" {
		t.Fatalf("acknowledged status = %+v, want stage=reconcile nextAction=cpa_import mismatchDays=[2026-03-01]", ackedStatus)
	}

	checkDB, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen target db: %v", err)
	}
	var markedCount int
	if err := checkDB.QueryRowContext(ctx, `select count(*) from usage_coverage_incomplete_days where day = '2026-03-01'`).Scan(&markedCount); err != nil {
		_ = checkDB.Close()
		t.Fatalf("query usage_coverage_incomplete_days: %v", err)
	}
	_ = checkDB.Close()
	if markedCount != 1 {
		t.Fatalf("usage_coverage_incomplete_days count for 2026-03-01 = %d, want 1", markedCount)
	}
}

func TestWorkerPersistsAndRestoresPendingReconcileDaysAcrossRestartAndClearsAfterSuccess(t *testing.T) {
	sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, false)
	dbPath := upgradeservice.TargetDatabasePath(targetRoot)
	dataKeyPath := filepath.Join(targetRoot, "data.key")
	cfg := config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath}

	type statusSnapshot struct {
		Stage          string `json:"stage"`
		NextAction     string `json:"nextAction"`
		Reconciliation struct {
			MismatchDays          []string `json:"mismatchDays"`
			MismatchDaysTotal     int      `json:"mismatchDaysTotal"`
			MismatchDaysTruncated bool     `json:"mismatchDaysTruncated"`
		} `json:"reconciliation"`
		Execution *upgradeworker.Execution `json:"execution"`
	}

	startStack := func(t *testing.T) (context.CancelFunc, *upgradeworker.Service, *upgradewizard.Handler) {
		t.Helper()
		workerCtx, workerCancel := context.WithCancel(context.Background())
		worker, err := upgradeworker.New(workerCtx, upgradeworker.Options{
			Config:          cfg,
			TargetRoot:      targetRoot,
			SourceRoot:      sourceRoot,
			BundleDir:       bundleDir,
			CPAVersion:      "v8.0.55",
			PublicOrigin:    testPublicOrigin,
			RepairOwnership: func(string) error { return nil },
		})
		if err != nil {
			workerCancel()
			t.Fatalf("upgradeworker.New: %v", err)
		}
		worker.SetHandler(&upgradewizard.Handler{
			LegacyRoot:           func() string { return sourceRoot },
			Database:             func() string { return cfg.DBPath },
			DataKey:              func() string { return cfg.DataKeyPath },
			PublicOrigin:         testPublicOrigin,
			Authenticate:         worker.Authenticate,
			Execution:            worker.Execution,
			ReconciliationDays:   worker.ReconciliationDays,
			ReconciliationState:  worker.ReconciliationState,
			PreserveManagerStore: true,
		})
		socketPath := newShortSocketPath(t)
		serveWorkerOverUnixSocket(t, workerCtx, socketPath, worker)
		wizard := &upgradewizard.Handler{
			Worker:               upgradeworker.NewClient(socketPath, testPublicOrigin),
			PublicOrigin:         testPublicOrigin,
			Database:             func() string { return cfg.DBPath },
			DataKey:              func() string { return cfg.DataKeyPath },
			PreserveManagerStore: true,
		}
		return workerCancel, worker, wizard
	}

	readStatusFrom := func(handler http.Handler) statusSnapshot {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/status", nil)
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /status = %d: %s", rec.Code, rec.Body.String())
		}
		var doc statusSnapshot
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatalf("decode status JSON: %v (%s)", err, rec.Body.String())
		}
		return doc
	}

	doExecOn := func(worker *upgradeworker.Service, handler http.Handler, body string) upgradeworker.Execution {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/execute", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("execute %s = %d: %s", body, rec.Code, rec.Body.String())
		}
		return waitUntilIdle(t, worker)
	}

	cancelFirst, worker1, wizard1 := startStack(t)
	defer func() {
		if cancelFirst != nil {
			cancelFirst()
		}
	}()

	if state := doExecOn(worker1, wizard1, `{"action":"reconcile"}`); state.Code != "" {
		t.Fatalf("initial clean reconcile failed: %+v", state)
	}

	targetDB, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("open target db: %v", err)
	}
	if _, err := targetDB.Exec(`update usage_events set total_tokens = total_tokens + 1 where event_hash = 'legacy-event-1'`); err != nil {
		_ = targetDB.Close()
		t.Fatalf("drift target usage_events: %v", err)
	}
	_ = targetDB.Close()

	driftState := doExecOn(worker1, wizard1, `{"action":"reconcile"}`)
	if driftState.Code != string(upgradeservice.UpgradeErrorReconcileMismatch) {
		t.Fatalf("drift reconcile = %+v, want upgrade_reconcile_mismatch", driftState)
	}
	persistedOp, found, err := upgradeservice.ReadRecordFromSnapshot(context.Background(), dbPath, "")
	if err != nil || !found {
		t.Fatalf("ReadRecordFromSnapshot = %t, %v", found, err)
	}
	if !reflect.DeepEqual(persistedOp.PendingReconcileDays, []string{"2026-03-01"}) {
		t.Fatalf("persisted PendingReconcileDays = %v, want [2026-03-01]", persistedOp.PendingReconcileDays)
	}

	// Stop worker1 and verify both degraded DB-backed wizard projection (without RAM cache)
	// and a freshly started worker2 restore pending dates and block downstream actions.
	cancelFirst()
	cancelFirst = nil

	offlineWizard := &upgradewizard.Handler{
		Worker:               upgradeworker.NewClient(filepath.Join(t.TempDir(), "missing.sock"), testPublicOrigin),
		PublicOrigin:         testPublicOrigin,
		Database:             func() string { return cfg.DBPath },
		DataKey:              func() string { return cfg.DataKeyPath },
		PreserveManagerStore: true,
	}
	degradedStatus := readStatusFrom(offlineWizard)
	if degradedStatus.NextAction != "reconcile" || !reflect.DeepEqual(degradedStatus.Reconciliation.MismatchDays, []string{"2026-03-01"}) {
		t.Fatalf("degraded status without cache = %+v, want nextAction=reconcile mismatchDays=[2026-03-01]", degradedStatus)
	}

	cancelSecond, worker2, wizard2 := startStack(t)
	defer cancelSecond()

	restoredStatus := readStatusFrom(wizard2)
	if restoredStatus.NextAction != "reconcile" || !reflect.DeepEqual(restoredStatus.Reconciliation.MismatchDays, []string{"2026-03-01"}) {
		t.Fatalf("restored status after worker restart = %+v, want nextAction=reconcile mismatchDays=[2026-03-01]", restoredStatus)
	}

	for _, blockedBody := range []string{
		`{"action":"cpa-import","pluginsEnabled":false}`,
		`{"action":"external-connect","baseUrl":"http://127.0.0.1:9","managementKey":"unused-secret"}`,
		`{"action":"verify"}`,
	} {
		state := doExecOn(worker2, wizard2, blockedBody)
		if state.Code != string(upgradeservice.UpgradeErrorReconcileMismatch) {
			t.Fatalf("restarted worker %s = %+v, want upgrade_reconcile_mismatch", blockedBody, state)
		}
	}

	ackState := doExecOn(worker2, wizard2, `{"action":"reconcile","acknowledgeDays":["2026-03-01"],"acknowledgeParityGaps":true}`)
	if ackState.Code != "" {
		t.Fatalf("acknowledged reconcile after restart failed: %+v", ackState)
	}
	clearedOp, found, err := upgradeservice.ReadRecordFromSnapshot(context.Background(), dbPath, "")
	if err != nil || !found || len(clearedOp.PendingReconcileDays) != 0 {
		t.Fatalf("PendingReconcileDays after acknowledged reconcile = %+v (found=%t, err=%v), want empty", clearedOp.PendingReconcileDays, found, err)
	}
	finalStatus := readStatusFrom(wizard2)
	if finalStatus.Stage != "reconcile" || finalStatus.NextAction != "cpa_import" {
		t.Fatalf("final status after clear = %+v, want stage=reconcile nextAction=cpa_import", finalStatus)
	}
}

func TestWorkerEnforcesSharedStageAndDurableReconciliationGatesBeforeExternalMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, false)
	dbPath := upgradeservice.TargetDatabasePath(targetRoot)
	dataKeyPath := filepath.Join(targetRoot, "data.key")
	cfg := config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath}

	var externalProbes atomic.Int64
	extServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		externalProbes.Add(1)
		w.Header().Set("X-CPA-Version", "v8.0.55")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"/auth"},"access":{"api-keys":["k1"]}}`))
	}))
	defer extServer.Close()

	worker, err := upgradeworker.New(ctx, upgradeworker.Options{
		Config:          cfg,
		TargetRoot:      targetRoot,
		SourceRoot:      sourceRoot,
		BundleDir:       bundleDir,
		CPAVersion:      "v8.0.55",
		PublicOrigin:    testPublicOrigin,
		RepairOwnership: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("upgradeworker.New: %v", err)
	}
	worker.SetHandler(&upgradewizard.Handler{
		LegacyRoot:           func() string { return sourceRoot },
		Database:             func() string { return cfg.DBPath },
		DataKey:              func() string { return cfg.DataKeyPath },
		PublicOrigin:         testPublicOrigin,
		Authenticate:         worker.Authenticate,
		Execution:            worker.Execution,
		ReconciliationDays:   worker.ReconciliationDays,
		ReconciliationState:  worker.ReconciliationState,
		PreserveManagerStore: true,
	})
	socketPath := newShortSocketPath(t)
	serveWorkerOverUnixSocket(t, ctx, socketPath, worker)
	publicWizard := &upgradewizard.Handler{
		Worker:               upgradeworker.NewClient(socketPath, testPublicOrigin),
		PublicOrigin:         testPublicOrigin,
		Database:             func() string { return cfg.DBPath },
		DataKey:              func() string { return cfg.DataKeyPath },
		PreserveManagerStore: true,
	}

	doExec := func(body string) upgradeworker.Execution {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/execute", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		publicWizard.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("execute %s = %d: %s", body, rec.Code, rec.Body.String())
		}
		return waitUntilIdle(t, worker)
	}

	beforeOp, _, err := upgradeservice.ReadRecordFromSnapshot(ctx, dbPath, "")
	if err != nil {
		t.Fatalf("ReadRecordFromSnapshot before: %v", err)
	}

	extBody := fmt.Sprintf(`{"action":"external-connect","baseUrl":%q,"managementKey":%q}`, extServer.URL, testExternalMgmtKey)
	if state := doExec(extBody); state.Code != string(upgradeservice.UpgradeErrorExternalNotApplicable) {
		t.Fatalf("external-connect at copy_in = %+v, want upgrade_external_not_applicable", state)
	}
	if got := externalProbes.Load(); got != 0 {
		t.Fatalf("external-connect at copy_in probed CPA %d times, want 0", got)
	}
	if state := doExec(`{"action":"cpa-import","pluginsEnabled":false}`); state.Code != string(upgradeservice.UpgradeErrorCPAStageConflict) {
		t.Fatalf("cpa-import at copy_in = %+v, want upgrade_cpa_stage_conflict", state)
	}
	if state := doExec(`{"action":"verify"}`); state.Code != string(upgradeservice.UpgradeErrorVerifyNotApplicable) {
		t.Fatalf("verify at copy_in = %+v, want upgrade_verify_not_applicable", state)
	}

	afterCopyInOp, _, err := upgradeservice.ReadRecordFromSnapshot(ctx, dbPath, "")
	if err != nil {
		t.Fatalf("ReadRecordFromSnapshot after copy_in refusals: %v", err)
	}
	if afterCopyInOp.Revision != beforeOp.Revision || afterCopyInOp.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("copy_in refusals mutated operation: %+v -> %+v", beforeOp, afterCopyInOp)
	}

	// Even if stage is set to reconcile without durable reconciliation evidence, external-connect refuses without mutation.
	db, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(`update upgrade_operations set stage = 'reconcile', last_forward_stage = 'reconcile', reconciliation = '{}' where id = 1`); err != nil {
		_ = db.Close()
		t.Fatalf("tamper stage: %v", err)
	}
	var settingsBefore int
	if err := db.QueryRow(`select count(*) from settings where key in ('manager_config_v1', 'setup', 'cpa_connection_revision_v1')`).Scan(&settingsBefore); err != nil {
		_ = db.Close()
		t.Fatalf("count settings before: %v", err)
	}
	_ = db.Close()

	if state := doExec(extBody); state.Code != string(upgradeservice.UpgradeErrorExternalNotApplicable) {
		t.Fatalf("external-connect without durable reconciliation = %+v, want upgrade_external_not_applicable", state)
	}
	if got := externalProbes.Load(); got != 0 {
		t.Fatalf("external-connect without durable reconciliation probed CPA %d times, want 0", got)
	}
	db, err = sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	var settingsAfter int
	if err := db.QueryRow(`select count(*) from settings where key in ('manager_config_v1', 'setup', 'cpa_connection_revision_v1')`).Scan(&settingsAfter); err != nil {
		_ = db.Close()
		t.Fatalf("count settings after: %v", err)
	}
	_ = db.Close()
	if settingsAfter != settingsBefore {
		t.Fatalf("settings mutated without durable reconciliation: %d -> %d", settingsBefore, settingsAfter)
	}

	// Real reconcile succeeds, then cpa_import stage closes reconcile and blocks external-connect.
	if state := doExec(`{"action":"reconcile"}`); state.Code != "" {
		t.Fatalf("real reconcile failed: %+v", state)
	}
	db, err = sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen db for cpa_import stage: %v", err)
	}
	if _, err := db.Exec(`update upgrade_operations set stage = 'cpa_import', last_forward_stage = 'cpa_import' where id = 1`); err != nil {
		_ = db.Close()
		t.Fatalf("set cpa_import stage: %v", err)
	}
	_ = db.Close()

	if state := doExec(`{"action":"reconcile"}`); state.Code != string(upgradeservice.UpgradeErrorReconcileStageClosed) {
		t.Fatalf("reconcile at cpa_import = %+v, want upgrade_reconcile_stage_closed", state)
	}
	if state := doExec(extBody); state.Code != string(upgradeservice.UpgradeErrorExternalNotApplicable) {
		t.Fatalf("external-connect at cpa_import = %+v, want upgrade_external_not_applicable", state)
	}
	if got := externalProbes.Load(); got != 0 {
		t.Fatalf("external-connect at cpa_import probed CPA %d times, want 0", got)
	}
}

func TestWorkerLiveAdminResetRotationAndUnreadableWALReturns503(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, false)
	dbPath := upgradeservice.TargetDatabasePath(targetRoot)
	dataKeyPath := filepath.Join(targetRoot, "data.key")
	cfg := config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath}

	worker, err := upgradeworker.New(ctx, upgradeworker.Options{
		Config:          cfg,
		TargetRoot:      targetRoot,
		SourceRoot:      sourceRoot,
		BundleDir:       bundleDir,
		PublicOrigin:    testPublicOrigin,
		RepairOwnership: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("upgradeworker.New: %v", err)
	}
	worker.SetHandler(&upgradewizard.Handler{
		LegacyRoot:                  func() string { return sourceRoot },
		Database:                    func() string { return cfg.DBPath },
		DataKey:                     func() string { return cfg.DataKeyPath },
		PublicOrigin:                testPublicOrigin,
		Authenticate:                worker.Authenticate,
		Execution:                   worker.Execution,
		ReconciliationDays:          worker.ReconciliationDays,
		ReconciliationHasParityGaps: worker.ReconciliationHasParityGaps,
		ReconciliationState:         worker.ReconciliationState,
		PreserveManagerStore:        true,
	})
	socketPath := newShortSocketPath(t)
	serveWorkerOverUnixSocket(t, ctx, socketPath, worker)
	client := upgradeworker.NewClient(socketPath, testPublicOrigin)
	publicWizard := &upgradewizard.Handler{
		Worker:               client,
		PublicOrigin:         testPublicOrigin,
		Database:             func() string { return cfg.DBPath },
		DataKey:              func() string { return cfg.DataKeyPath },
		PreserveManagerStore: true,
	}

	doEntry := func(key string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, testPublicOrigin+upgradeworker.EntryPath, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Origin", testPublicOrigin)
		rec := httptest.NewRecorder()
		client.ServeHTTP(rec, req)
		return rec
	}
	doWizard := func(method, suffix, key, body string) *httptest.ResponseRecorder {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1"+suffix, reader)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Origin", testPublicOrigin)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		publicWizard.ServeHTTP(rec, req)
		return rec
	}

	if rec := doEntry(testInheritedAdminKey); rec.Code != http.StatusOK {
		t.Fatalf("initial entry = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := doWizard(http.MethodGet, "/installation/upgrade/status", testInheritedAdminKey, ""); rec.Code != http.StatusOK {
		t.Fatalf("initial status = %d: %s", rec.Code, rec.Body.String())
	}

	// Simulate offline admin-reset rotating the admin credential in usage.sqlite while worker is live.
	rotatedKey, err := security.GenerateAdminKey()
	if err != nil {
		t.Fatalf("GenerateAdminKey: %v", err)
	}
	rotatedCred, err := security.NewAdminCredential(rotatedKey, "admin-reset")
	if err != nil {
		t.Fatalf("NewAdminCredential: %v", err)
	}
	db, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("open target db for rotation: %v", err)
	}
	if err := setting.New(db).SaveAdminCredential(ctx, rotatedCred); err != nil {
		_ = db.Close()
		t.Fatalf("save rotated admin credential: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close target db after rotation: %v", err)
	}

	// Old key is immediately rejected with 401; rotated key immediately succeeds with 200.
	if rec := doEntry(testInheritedAdminKey); rec.Code != http.StatusUnauthorized {
		t.Fatalf("entry with old key after rotation = %d, want 401 (%s)", rec.Code, rec.Body.String())
	}
	if rec := doWizard(http.MethodGet, "/installation/upgrade/status", testInheritedAdminKey, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status with old key after rotation = %d, want 401 (%s)", rec.Code, rec.Body.String())
	}
	if rec := doEntry(rotatedKey); rec.Code != http.StatusOK {
		t.Fatalf("entry with rotated key = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if rec := doWizard(http.MethodGet, "/installation/upgrade/status", rotatedKey, ""); rec.Code != http.StatusOK {
		t.Fatalf("status with rotated key = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	// Unreadable/unusable WAL during Authenticate returns 503 unavailable (never 401).
	walPath := dbPath + "-wal"
	t.Cleanup(func() {
		_ = os.Chmod(walPath, 0o600)
		_ = os.Remove(walPath)
	})
	if os.Geteuid() != 0 {
		if err := os.WriteFile(walPath, []byte("locked-wal"), 0o000); err != nil {
			t.Fatalf("write unreadable wal: %v", err)
		}
		if rec := doEntry(rotatedKey); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "source_unavailable") {
			t.Fatalf("entry with unreadable WAL = %d (%s), want 503 source_unavailable", rec.Code, rec.Body.String())
		}
		if rec := doWizard(http.MethodGet, "/installation/upgrade/status", rotatedKey, ""); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), upgradeworker.CodeUnavailable) {
			t.Fatalf("status with unreadable WAL = %d (%s), want 503 %s", rec.Code, rec.Body.String(), upgradeworker.CodeUnavailable)
		}
		_ = os.Chmod(walPath, 0o600)
		_ = os.Remove(walPath)
	}
	if err := os.WriteFile(walPath, []byte("invalid-wal-header"), 0o600); err != nil {
		t.Fatalf("write corrupt wal: %v", err)
	}
	if rec := doEntry(rotatedKey); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "source_unavailable") {
		t.Fatalf("entry with corrupt WAL = %d (%s), want 503 source_unavailable", rec.Code, rec.Body.String())
	}
	if rec := doWizard(http.MethodGet, "/installation/upgrade/status", rotatedKey, ""); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), upgradeworker.CodeUnavailable) {
		t.Fatalf("status with corrupt WAL = %d (%s), want 503 %s", rec.Code, rec.Body.String(), upgradeworker.CodeUnavailable)
	}
	if rec := doWizard(http.MethodPost, "/installation/upgrade/execute", rotatedKey, `{"action":"reconcile"}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("execute with corrupt WAL = %d (%s), want 503", rec.Code, rec.Body.String())
	}
	if err := os.Remove(walPath); err != nil {
		t.Fatalf("remove corrupt wal: %v", err)
	}

	if rec := doWizard(http.MethodGet, "/installation/upgrade/status", rotatedKey, ""); rec.Code != http.StatusOK {
		t.Fatalf("status after WAL restored = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

func TestWorkerReconcileTruncated400PendingDaysSucceedsWithAcknowledgeAllMismatchesAndProjectsParityGaps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Build a 1.x source with 400 UTC days of usage_events and no parity gaps.
	rawSource, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve source root: %v", err)
	}
	rawTarget, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve target root: %v", err)
	}
	rawBundle, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve bundle root: %v", err)
	}
	bundleDir := filepath.Join(rawBundle, "bundle")
	sourceDBPath := filepath.Join(rawSource, "usage.sqlite")

	srcDB, err := sqliterepo.Open(sourceDBPath)
	if err != nil {
		t.Fatalf("open source db: %v", err)
	}
	cred, err := security.NewAdminCredential(testInheritedAdminKey, "legacy")
	if err != nil {
		_ = srcDB.Close()
		t.Fatalf("create admin credential: %v", err)
	}
	if err := setting.New(srcDB).SaveAdminCredential(ctx, cred); err != nil {
		_ = srcDB.Close()
		t.Fatalf("save admin credential: %v", err)
	}
	tx, err := srcDB.BeginTx(ctx, nil)
	if err != nil {
		_ = srcDB.Close()
		t.Fatalf("begin seed tx: %v", err)
	}
	baseDay := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 400 {
		day := baseDay.AddDate(0, 0, i)
		tsMS := day.UnixMilli()
		tsStr := day.Format(time.RFC3339)
		if _, err := tx.ExecContext(ctx, `insert into usage_events (event_hash, timestamp_ms, timestamp, model, requested_model, total_tokens, created_at_ms)
			values (?, ?, ?, 'gpt-test', 'gpt-test', 10, ?)`,
			fmt.Sprintf("legacy-bulk-%03d", i), tsMS, tsStr, tsMS); err != nil {
			_ = tx.Rollback()
			_ = srcDB.Close()
			t.Fatalf("insert usage_event %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		_ = srcDB.Close()
		t.Fatalf("commit seed tx: %v", err)
	}
	conn, err := srcDB.Conn(ctx)
	if err != nil {
		_ = srcDB.Close()
		t.Fatalf("open source conn: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `pragma foreign_keys = off`); err != nil {
		_ = conn.Close()
		_ = srcDB.Close()
		t.Fatalf("disable foreign keys: %v", err)
	}
	for _, pattern := range []string{`gateway\_%`, `fresh\_%`, `upgrade\_%`, `usage\_coverage\_incomplete\_days`} {
		rows, err := conn.QueryContext(ctx,
			`select name from sqlite_master where type = 'table' and name like ? escape '\'`, pattern)
		if err != nil {
			_ = conn.Close()
			_ = srcDB.Close()
			t.Fatalf("query v2 tables: %v", err)
		}
		var names []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				_ = rows.Close()
				_ = conn.Close()
				_ = srcDB.Close()
				t.Fatalf("scan table: %v", err)
			}
			names = append(names, name)
		}
		_ = rows.Close()
		for _, name := range names {
			if _, err := conn.ExecContext(ctx, `drop table `+name); err != nil {
				_ = conn.Close()
				_ = srcDB.Close()
				t.Fatalf("drop table %s: %v", name, err)
			}
		}
	}
	_ = conn.Close()
	if err := srcDB.Close(); err != nil {
		t.Fatalf("close source db: %v", err)
	}
	if _, _, err := security.LoadOrCreateDataKey("", filepath.Join(rawSource, "data.key")); err != nil {
		t.Fatalf("create source data key: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(rawSource, "usage-archives"), 0o755); err != nil {
		t.Fatalf("mkdir source archives: %v", err)
	}
	if err := os.WriteFile(sourceDBPath+".manager.lock", nil, 0o600); err != nil {
		t.Fatalf("write source lock: %v", err)
	}
	sourceLock, err := processlock.AcquireExistingReadOnly(sourceDBPath)
	if err != nil {
		t.Fatalf("acquire source lock: %v", err)
	}
	if _, err := upgradeservice.CopyIn(ctx, upgradeservice.Options{
		LegacyRoot: rawSource,
		TargetRoot: rawTarget,
		BundleDir:  bundleDir,
		Lock:       sourceLock,
	}); err != nil {
		_ = sourceLock.Close()
		t.Fatalf("copy in 400-day target: %v", err)
	}
	_ = sourceLock.Close()

	// Introduce drift on all 400 days in target usage_events.
	targetDBPath := upgradeservice.TargetDatabasePath(rawTarget)
	targetDB, err := sqliterepo.Open(targetDBPath)
	if err != nil {
		t.Fatalf("open target db: %v", err)
	}
	if _, err := targetDB.ExecContext(ctx, `update usage_events set total_tokens = total_tokens + 1`); err != nil {
		_ = targetDB.Close()
		t.Fatalf("drift 400 usage_events rows: %v", err)
	}
	if err := targetDB.Close(); err != nil {
		t.Fatalf("close target db: %v", err)
	}

	cfg := config.Config{DBPath: targetDBPath, DataKeyPath: filepath.Join(rawTarget, "data.key")}
	worker, err := upgradeworker.New(ctx, upgradeworker.Options{
		Config:          cfg,
		TargetRoot:      rawTarget,
		SourceRoot:      rawSource,
		BundleDir:       bundleDir,
		PublicOrigin:    testPublicOrigin,
		RepairOwnership: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("upgradeworker.New: %v", err)
	}
	worker.SetHandler(&upgradewizard.Handler{
		LegacyRoot:                  func() string { return rawSource },
		Database:                    func() string { return cfg.DBPath },
		DataKey:                     func() string { return cfg.DataKeyPath },
		PublicOrigin:                testPublicOrigin,
		Authenticate:                worker.Authenticate,
		Execution:                   worker.Execution,
		ReconciliationDays:          worker.ReconciliationDays,
		ReconciliationHasParityGaps: worker.ReconciliationHasParityGaps,
		ReconciliationState:         worker.ReconciliationState,
		PreserveManagerStore:        true,
	})
	socketPath := newShortSocketPath(t)
	serveWorkerOverUnixSocket(t, ctx, socketPath, worker)
	publicWizard := &upgradewizard.Handler{
		Worker:               upgradeworker.NewClient(socketPath, testPublicOrigin),
		PublicOrigin:         testPublicOrigin,
		Database:             func() string { return cfg.DBPath },
		DataKey:              func() string { return cfg.DataKeyPath },
		PreserveManagerStore: true,
	}

	doExec := func(body string) upgradeworker.Execution {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/execute", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		publicWizard.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("execute %s = %d: %s", body, rec.Code, rec.Body.String())
		}
		return waitUntilIdle(t, worker)
	}

	type statusView struct {
		Stage          string `json:"stage"`
		NextAction     string `json:"nextAction"`
		Reconciliation struct {
			MismatchDays          []string `json:"mismatchDays"`
			MismatchDaysTotal     int      `json:"mismatchDaysTotal"`
			MismatchDaysTruncated bool     `json:"mismatchDaysTruncated"`
			HasParityGaps         bool     `json:"hasParityGaps"`
		} `json:"reconciliation"`
	}
	readStatus := func() statusView {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/status", nil)
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		rec := httptest.NewRecorder()
		publicWizard.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /status = %d: %s", rec.Code, rec.Body.String())
		}
		var doc statusView
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		return doc
	}

	// 1. Unacknowledged reconcile records all 400 pending days and status truncates window to 366 with hasParityGaps=false.
	if state := doExec(`{"action":"reconcile"}`); state.Code != string(upgradeservice.UpgradeErrorReconcileMismatch) {
		t.Fatalf("unacknowledged 400-day reconcile = %+v, want upgrade_reconcile_mismatch", state)
	}
	truncStatus := readStatus()
	if len(truncStatus.Reconciliation.MismatchDays) != 366 ||
		truncStatus.Reconciliation.MismatchDaysTotal != 400 ||
		!truncStatus.Reconciliation.MismatchDaysTruncated ||
		truncStatus.Reconciliation.HasParityGaps {
		t.Fatalf("truncated status = %+v, want 366/400 truncated=true hasParityGaps=false", truncStatus.Reconciliation)
	}

	// 2. AcknowledgeAllMismatches unions persisted PendingReconcileDays so Reconcile succeeds without requiring acknowledgeParityGaps.
	if state := doExec(`{"action":"reconcile","acknowledgeAllMismatches":true}`); state.Code != "" {
		t.Fatalf("acknowledgeAllMismatches reconcile failed: %+v", state)
	}
	doneStatus := readStatus()
	if doneStatus.Stage != "reconcile" || doneStatus.NextAction != "cpa_import" {
		t.Fatalf("status after acknowledgeAllMismatches = %+v, want stage=reconcile nextAction=cpa_import", doneStatus)
	}

	verifyDB, err := sqliterepo.Open(targetDBPath)
	if err != nil {
		t.Fatalf("reopen target db: %v", err)
	}
	var incompleteCount int
	if err := verifyDB.QueryRowContext(ctx, `select count(*) from usage_coverage_incomplete_days`).Scan(&incompleteCount); err != nil {
		_ = verifyDB.Close()
		t.Fatalf("count incomplete days: %v", err)
	}
	_ = verifyDB.Close()
	if incompleteCount != 400 {
		t.Fatalf("marked incomplete days = %d, want 400", incompleteCount)
	}

	// 3. Parity-gap source exposes hasParityGaps=true in worker status projection.
	gapSource, gapTarget, gapBundle := newLegacySourceAndCopiedTarget(t, true)
	gapCfg := config.Config{
		DBPath:      upgradeservice.TargetDatabasePath(gapTarget),
		DataKeyPath: filepath.Join(gapTarget, "data.key"),
	}
	gapWorker, err := upgradeworker.New(ctx, upgradeworker.Options{
		Config:          gapCfg,
		TargetRoot:      gapTarget,
		SourceRoot:      gapSource,
		BundleDir:       gapBundle,
		PublicOrigin:    testPublicOrigin,
		RepairOwnership: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("upgradeworker.New (parity gap): %v", err)
	}
	gapWizard := &upgradewizard.Handler{
		LegacyRoot:                  func() string { return gapSource },
		Database:                    func() string { return gapCfg.DBPath },
		DataKey:                     func() string { return gapCfg.DataKeyPath },
		PublicOrigin:                testPublicOrigin,
		Authenticate:                gapWorker.Authenticate,
		Execution:                   gapWorker.Execution,
		ReconciliationDays:          gapWorker.ReconciliationDays,
		ReconciliationHasParityGaps: gapWorker.ReconciliationHasParityGaps,
		ReconciliationState:         gapWorker.ReconciliationState,
		PreserveManagerStore:        true,
	}
	gapWorker.SetHandler(gapWizard)
	gapReq := httptest.NewRequest(http.MethodGet, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/status", nil)
	gapReq.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
	gapReq.Header.Set("Origin", testPublicOrigin)
	gapRec := httptest.NewRecorder()
	gapWorker.ServeHTTP(gapRec, gapReq)
	if gapRec.Code != http.StatusOK {
		t.Fatalf("gap worker status = %d: %s", gapRec.Code, gapRec.Body.String())
	}
	var gapDoc statusView
	if err := json.Unmarshal(gapRec.Body.Bytes(), &gapDoc); err != nil {
		t.Fatalf("decode gap status: %v", err)
	}
	if !gapDoc.Reconciliation.HasParityGaps || len(gapDoc.Reconciliation.MismatchDays) != 1 {
		t.Fatalf("gap status reconciliation = %+v, want hasParityGaps=true mismatchDays=[2026-02-15]", gapDoc.Reconciliation)
	}
}

func TestWorkerExternalConnectRefusesStaleSettingsWriteAfterConcurrentReproveOrStageTransition(t *testing.T) {
	const (
		initialMgmtKey  = "external-v8-initial-key"
		reprovedMgmtKey = "external-v8-reproved-key"
	)
	const v8ConfigJSON = `{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"/auth"},"access":{"api-keys":["client-key-1"]}}`
	dualKeyCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-test"}]}`))
			return
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+initialMgmtKey && auth != "Bearer "+reprovedMgmtKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v0/management/config":
			w.Header().Set("X-CPA-Version", "v8.0.55")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case "/v8/management/config":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(v8ConfigJSON))
		case "/v8/management/config/access/api-keys":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`["client-key-1"]`))
		case "/v8/management/config.yaml":
			w.Header().Set("Content-Type", "application/yaml")
			_, _ = w.Write([]byte("port: 8317\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(dualKeyCPA.Close)

	t.Run("concurrent_cli_reprove_preserves_newer_settings", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, false)
		dbPath := upgradeservice.TargetDatabasePath(targetRoot)
		dataKeyPath := filepath.Join(targetRoot, "data.key")

		sourceLock, err := processlock.AcquireExistingReadOnly(upgradeservice.SourceDatabasePath(sourceRoot))
		if err != nil {
			t.Fatalf("source lock: %v", err)
		}
		targetLock, err := processlock.Acquire(dbPath)
		if err != nil {
			_ = sourceLock.Close()
			t.Fatalf("target lock: %v", err)
		}
		if _, err := upgradeservice.Reconcile(ctx, upgradeservice.Options{
			LegacyRoot: sourceRoot,
			TargetRoot: targetRoot,
			BundleDir:  bundleDir,
			Lock:       sourceLock,
			TargetLock: targetLock,
		}); err != nil {
			_ = targetLock.Close()
			_ = sourceLock.Close()
			t.Fatalf("reconcile: %v", err)
		}
		_ = targetLock.Close()
		_ = sourceLock.Close()

		keyBytes, err := security.LoadExistingDataKey("", dataKeyPath)
		if err != nil {
			t.Fatalf("load data key: %v", err)
		}
		protector, err := security.NewProtector(keyBytes)
		if err != nil {
			t.Fatalf("new protector: %v", err)
		}

		cfg := config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath}
		worker, err := upgradeworker.New(ctx, upgradeworker.Options{
			Config:          cfg,
			TargetRoot:      targetRoot,
			SourceRoot:      sourceRoot,
			BundleDir:       bundleDir,
			PublicOrigin:    testPublicOrigin,
			RepairOwnership: func(string) error { return nil },
			BeforeExternalSettingsSync: func() {
				if _, err := upgradeservice.ExternalConnect(ctx, upgradeservice.ExternalConnectOptions{
					TargetRoot:    targetRoot,
					BaseURL:       dualKeyCPA.URL,
					ManagementKey: reprovedMgmtKey,
					Reprove:       true,
				}); err != nil {
					t.Errorf("concurrent CLI reprove: %v", err)
					return
				}
				st, err := store.Open(dbPath, protector)
				if err != nil {
					t.Errorf("open store in reprove seam: %v", err)
					return
				}
				defer func() { _ = st.Close() }()
				_ = st.SaveManagerConfigAndSetup(ctx, model.ManagerConfig{
					CPAConnection: model.ManagerCPAConnectionConfig{
						CPABaseURL:    dualKeyCPA.URL,
						ManagementKey: reprovedMgmtKey,
					},
				}, model.Setup{
					CPAUpstreamURL: dualKeyCPA.URL,
					ManagementKey:  reprovedMgmtKey,
				})
			},
		})
		if err != nil {
			t.Fatalf("upgradeworker.New: %v", err)
		}

		body, _ := json.Marshal(map[string]any{
			"action":        "external-connect",
			"baseUrl":       dualKeyCPA.URL,
			"managementKey": initialMgmtKey,
		})
		req := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/execute", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		worker.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("execute external-connect = %d: %s", rec.Code, rec.Body.String())
		}
		state := waitUntilIdle(t, worker)
		if state.Code != "upgrade_external_connect_conflict" {
			t.Fatalf("stale external settings sync state = %+v, want upgrade_external_connect_conflict", state)
		}

		st, err := store.Open(dbPath, protector)
		if err != nil {
			t.Fatalf("reopen store: %v", err)
		}
		savedCfg, found, err := st.LoadManagerConfig(ctx)
		_ = st.Close()
		if err != nil || !found || savedCfg.CPAConnection.ManagementKey != reprovedMgmtKey {
			t.Fatalf("LoadManagerConfig after concurrent reprove = (found=%v, err=%v, key=%q), want %q",
				found, err, savedCfg.CPAConnection.ManagementKey, reprovedMgmtKey)
		}
	})

	t.Run("concurrent_stage_advance_refuses_settings_overwrite", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, false)
		dbPath := upgradeservice.TargetDatabasePath(targetRoot)
		dataKeyPath := filepath.Join(targetRoot, "data.key")

		sourceLock, err := processlock.AcquireExistingReadOnly(upgradeservice.SourceDatabasePath(sourceRoot))
		if err != nil {
			t.Fatalf("source lock: %v", err)
		}
		targetLock, err := processlock.Acquire(dbPath)
		if err != nil {
			_ = sourceLock.Close()
			t.Fatalf("target lock: %v", err)
		}
		if _, err := upgradeservice.Reconcile(ctx, upgradeservice.Options{
			LegacyRoot: sourceRoot,
			TargetRoot: targetRoot,
			BundleDir:  bundleDir,
			Lock:       sourceLock,
			TargetLock: targetLock,
		}); err != nil {
			_ = targetLock.Close()
			_ = sourceLock.Close()
			t.Fatalf("reconcile: %v", err)
		}
		_ = targetLock.Close()
		_ = sourceLock.Close()

		cfg := config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath}
		worker, err := upgradeworker.New(ctx, upgradeworker.Options{
			Config:          cfg,
			TargetRoot:      targetRoot,
			SourceRoot:      sourceRoot,
			BundleDir:       bundleDir,
			PublicOrigin:    testPublicOrigin,
			RepairOwnership: func(string) error { return nil },
			BeforeExternalSettingsSync: func() {
				db, err := sqliterepo.Open(dbPath)
				if err != nil {
					t.Errorf("open db in stage seam: %v", err)
					return
				}
				defer func() { _ = db.Close() }()
				_, _ = db.ExecContext(ctx, `update upgrade_operations set stage = 'verify', last_forward_stage = 'verify' where id = 1`)
			},
		})
		if err != nil {
			t.Fatalf("upgradeworker.New: %v", err)
		}

		body, _ := json.Marshal(map[string]any{
			"action":        "external-connect",
			"baseUrl":       dualKeyCPA.URL,
			"managementKey": initialMgmtKey,
		})
		req := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/execute", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		worker.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("execute external-connect = %d: %s", rec.Code, rec.Body.String())
		}
		state := waitUntilIdle(t, worker)
		if state.Code != string(upgradeservice.UpgradeErrorExternalNotApplicable) {
			t.Fatalf("stage-advanced external settings sync = %+v, want %s", state, upgradeservice.UpgradeErrorExternalNotApplicable)
		}
	})
}

func TestWorkerRestartPreservesSourceRevisionAndDetectsWALOnlyMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, false)
	cfg := config.Config{
		DBPath:      filepath.Join(targetRoot, "usage.sqlite"),
		DataKeyPath: filepath.Join(targetRoot, "data.key"),
	}

	// InspectRequired verifies data.key directly and fails closed if data.key is missing.
	savedKeyBytes, err := os.ReadFile(cfg.DataKeyPath)
	if err != nil {
		t.Fatalf("read target data.key: %v", err)
	}
	if err := os.Remove(cfg.DataKeyPath); err != nil {
		t.Fatalf("remove target data.key: %v", err)
	}
	if required, err := upgradeworker.InspectRequired(ctx, targetRoot); err == nil || required {
		t.Fatalf("InspectRequired with missing data.key = (%v, %v), want (false, err)", required, err)
	}
	if err := os.WriteFile(cfg.DataKeyPath, savedKeyBytes, 0o600); err != nil {
		t.Fatalf("restore target data.key: %v", err)
	}
	if required, err := upgradeworker.InspectRequired(ctx, targetRoot); err != nil || !required {
		t.Fatalf("InspectRequired with valid data.key = (%v, %v), want (true, nil)", required, err)
	}

	newWorker := func() *upgradeworker.Service {
		t.Helper()
		w, err := upgradeworker.New(ctx, upgradeworker.Options{
			Config:          cfg,
			TargetRoot:      targetRoot,
			SourceRoot:      sourceRoot,
			BundleDir:       bundleDir,
			PublicOrigin:    testPublicOrigin,
			RepairOwnership: func(string) error { return nil },
		})
		if err != nil {
			t.Fatalf("upgradeworker.New: %v", err)
		}
		return w
	}

	doEntry := func(h http.Handler, priorRev string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, testPublicOrigin+upgradeworker.EntryPath, nil)
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		if priorRev != "" {
			req.Header.Set("X-CPAMP-Upgrade-Source", priorRev)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			return rec.Code, ""
		}
		var doc struct {
			SourceRevision string `json:"source_revision"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatalf("decode entry response: %v", err)
		}
		return rec.Code, doc.SourceRevision
	}

	// (a) Restarting upgrade-worker produces the exact same deterministic source_revision without 409.
	worker1 := newWorker()
	code1, rev1 := doEntry(worker1, "")
	if code1 != http.StatusOK || len(rev1) != 64 {
		t.Fatalf("initial sign-in = (%d, %q), want (200, 64-hex)", code1, rev1)
	}
	worker1.Wait()

	worker2 := newWorker()
	defer worker2.Wait()
	codeRestart, revRestart := doEntry(worker2, rev1)
	if codeRestart != http.StatusOK || revRestart != rev1 {
		t.Fatalf("renewal across worker restart = (%d, %q), want (200, %q)", codeRestart, revRestart, rev1)
	}

	// Symlinked -journal on 1.x source fails closed in observeSourceRevision (503 on renewal).
	sourceDBPath := filepath.Join(sourceRoot, "usage.sqlite")
	journalPath := sourceDBPath + "-journal"
	if err := os.Symlink(sourceDBPath, journalPath); err != nil {
		t.Fatalf("symlink -journal: %v", err)
	}
	if symCode, _ := doEntry(worker2, rev1); symCode != http.StatusServiceUnavailable {
		t.Fatalf("renewal with symlinked -journal = %d, want 503", symCode)
	}
	_ = os.Remove(journalPath)

	// (b) WAL-only write on an unstopped 1.x source (leaving base usage.sqlite untouched) changes source_revision -> 409.
	beforeStat, err := os.Stat(sourceDBPath)
	if err != nil {
		t.Fatalf("stat source db before WAL write: %v", err)
	}
	beforeDBBytes, err := os.ReadFile(sourceDBPath)
	if err != nil {
		t.Fatalf("read source db before WAL write: %v", err)
	}
	liveDB, err := sql.Open("sqlite", sourceDBPath)
	if err != nil {
		t.Fatalf("open source db for WAL-only write: %v", err)
	}
	defer func() { _ = liveDB.Close() }()
	if _, err := liveDB.ExecContext(ctx, `PRAGMA wal_autocheckpoint = 0; INSERT INTO settings (key, value, updated_at_ms) VALUES ('live_wal_mutation', '1', 1);`); err != nil {
		t.Fatalf("exec WAL-only write: %v", err)
	}
	afterStat, err := os.Stat(sourceDBPath)
	if err != nil {
		t.Fatalf("stat source db after WAL write: %v", err)
	}
	afterDBBytes, err := os.ReadFile(sourceDBPath)
	if err != nil {
		t.Fatalf("read source db after WAL write: %v", err)
	}
	if afterStat.Size() != beforeStat.Size() || !bytes.Equal(afterDBBytes[:100], beforeDBBytes[:100]) {
		t.Fatalf("expected base usage.sqlite size/header unchanged during WAL-only write")
	}
	if walInfo, walErr := os.Stat(sourceDBPath + "-wal"); walErr != nil || walInfo.Size() == 0 {
		t.Fatalf("expected non-empty usage.sqlite-wal after WAL-only write, got (%v, %v)", walInfo, walErr)
	}

	if walCode, _ := doEntry(worker2, rev1); walCode != http.StatusConflict {
		t.Fatalf("renewal after WAL-only write = %d, want 409", walCode)
	}
	codeFresh, revAfterWAL := doEntry(worker2, "")
	if codeFresh != http.StatusOK || revAfterWAL == "" || revAfterWAL == rev1 {
		t.Fatalf("fresh sign-in after WAL-only write = (%d, %q), want 200 with revision != %q", codeFresh, revAfterWAL, rev1)
	}
}

func TestClientUnavailableFallbackBridgesToLiveWorkerRenewal(t *testing.T) {
	doEntry := func(t *testing.T, h http.Handler, priorRev string) (*httptest.ResponseRecorder, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, testPublicOrigin+upgradeworker.EntryPath, nil)
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		if priorRev != "" {
			req.Header.Set("X-CPAMP-Upgrade-Source", priorRev)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			return rec, ""
		}
		var doc struct {
			SourceRevision string `json:"source_revision"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatalf("decode entry response: %v", err)
		}
		return rec, doc.SourceRevision
	}

	t.Run("unchanged_source_upgrades_fallback_revision_to_live_worker_revision", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, false)
		dbPath := filepath.Join(targetRoot, "usage.sqlite")
		dataKeyPath := filepath.Join(targetRoot, "data.key")
		socketPath := newShortSocketPath(t)

		verifier := upgradeworker.NewPreparedCredentialVerifier(dbPath)
		client := upgradeworker.NewClient(socketPath, testPublicOrigin)
		client.FallbackAuthenticateAndRevision = verifier.AuthenticateAndRevision

		// Sign in while worker socket is not yet listening (serveEntryUnavailable).
		recFallback, fallbackRev := doEntry(t, client, "")
		if recFallback.Code != http.StatusOK || len(fallbackRev) != 64 {
			t.Fatalf("fallback sign-in = (%d, %q): %s", recFallback.Code, fallbackRev, recFallback.Body.String())
		}

		// Bring up live worker on the same socket and renew with fallbackRev -> 200 OK with live revision.
		worker, err := upgradeworker.New(ctx, upgradeworker.Options{
			Config:          config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath},
			TargetRoot:      targetRoot,
			SourceRoot:      sourceRoot,
			BundleDir:       bundleDir,
			PublicOrigin:    testPublicOrigin,
			RepairOwnership: func(string) error { return nil },
		})
		if err != nil {
			t.Fatalf("upgradeworker.New: %v", err)
		}
		defer worker.Wait()
		serveWorkerOverUnixSocket(t, ctx, socketPath, worker)

		recBridged, liveRev := doEntry(t, client, fallbackRev)
		if recBridged.Code != http.StatusOK || len(liveRev) != 64 || liveRev == fallbackRev {
			t.Fatalf("bridged renewal = (%d, %q), want 200 with deterministic live revision != fallback %q: %s",
				recBridged.Code, liveRev, fallbackRev, recBridged.Body.String())
		}
		// Subsequent renewals use liveRev directly (and cached fallbackRev also continues to pass while unchanged).
		if recNext, nextRev := doEntry(t, client, liveRev); recNext.Code != http.StatusOK || nextRev != liveRev {
			t.Fatalf("subsequent live renewal = (%d, %q), want (200, %q)", recNext.Code, nextRev, liveRev)
		}
		if recCachedBridge, cachedRev := doEntry(t, client, fallbackRev); recCachedBridge.Code != http.StatusOK || cachedRev != liveRev {
			t.Fatalf("cached bridge renewal = (%d, %q), want (200, %q)", recCachedBridge.Code, cachedRev, liveRev)
		}
	})

	t.Run("modified_source_since_copyin_returns_409_on_live_worker_bridge", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, false)
		dbPath := filepath.Join(targetRoot, "usage.sqlite")
		dataKeyPath := filepath.Join(targetRoot, "data.key")
		socketPath := newShortSocketPath(t)

		verifier := upgradeworker.NewPreparedCredentialVerifier(dbPath)
		client := upgradeworker.NewClient(socketPath, testPublicOrigin)
		client.FallbackAuthenticateAndRevision = verifier.AuthenticateAndRevision

		recFallback, fallbackRev := doEntry(t, client, "")
		if recFallback.Code != http.StatusOK || len(fallbackRev) != 64 {
			t.Fatalf("fallback sign-in = (%d, %q): %s", recFallback.Code, fallbackRev, recFallback.Body.String())
		}

		// Mutate the 1.x source after CopyIn before renewing against the live worker.
		sourceDB, err := sqliterepo.Open(filepath.Join(sourceRoot, "usage.sqlite"))
		if err != nil {
			t.Fatalf("open 1.x source db: %v", err)
		}
		if _, err := sourceDB.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at_ms) VALUES ('mutated_after_copyin', 'yes', 1)`); err != nil {
			_ = sourceDB.Close()
			t.Fatalf("mutate 1.x source db: %v", err)
		}
		_ = sourceDB.Close()

		worker, err := upgradeworker.New(ctx, upgradeworker.Options{
			Config:          config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath},
			TargetRoot:      targetRoot,
			SourceRoot:      sourceRoot,
			BundleDir:       bundleDir,
			PublicOrigin:    testPublicOrigin,
			RepairOwnership: func(string) error { return nil },
		})
		if err != nil {
			t.Fatalf("upgradeworker.New: %v", err)
		}
		defer worker.Wait()
		serveWorkerOverUnixSocket(t, ctx, socketPath, worker)

		recRejected, _ := doEntry(t, client, fallbackRev)
		if recRejected.Code != http.StatusConflict {
			t.Fatalf("bridged renewal after 1.x source mutation = %d: %s, want 409", recRejected.Code, recRejected.Body.String())
		}
	})
}

func TestWorkerReconcileClearsDaysOnNonMismatchError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, true)
	dbPath := filepath.Join(targetRoot, "usage.sqlite")
	dataKeyPath := filepath.Join(targetRoot, "data.key")

	// Corrupt target completeness by deleting copied usage_events rows so Reconcile
	// fails with UpgradeErrorReconcileTargetIncomplete (not ErrReconcileMismatch).
	targetDB, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("open target db: %v", err)
	}
	if _, err := targetDB.ExecContext(ctx, `DELETE FROM usage_events`); err != nil {
		_ = targetDB.Close()
		t.Fatalf("delete target usage_events: %v", err)
	}
	_ = targetDB.Close()

	worker, err := upgradeworker.New(ctx, upgradeworker.Options{
		Config:          config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath},
		TargetRoot:      targetRoot,
		SourceRoot:      sourceRoot,
		BundleDir:       bundleDir,
		PublicOrigin:    testPublicOrigin,
		RepairOwnership: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("upgradeworker.New: %v", err)
	}
	defer worker.Wait()

	if days := worker.ReconciliationDays(); len(days) == 0 || !worker.ReconciliationHasParityGaps() {
		t.Fatalf("expected initial parity-gap days before reconcile failure, got days=%v gaps=%v", days, worker.ReconciliationHasParityGaps())
	}

	body, _ := json.Marshal(map[string]any{"action": "reconcile"})
	req := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/execute", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
	req.Header.Set("Origin", testPublicOrigin)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	worker.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("execute reconcile = %d: %s", rec.Code, rec.Body.String())
	}

	state := waitUntilIdle(t, worker)
	if state.Code != string(upgradeservice.UpgradeErrorReconcileTargetIncomplete) {
		t.Fatalf("reconcile state = %+v, want %s", state, upgradeservice.UpgradeErrorReconcileTargetIncomplete)
	}
	if days := worker.ReconciliationDays(); len(days) != 0 {
		t.Fatalf("ReconciliationDays after non-mismatch error = %v, want empty", days)
	}
	if worker.ReconciliationHasParityGaps() {
		t.Fatal("ReconciliationHasParityGaps after non-mismatch error = true, want false")
	}
}

func TestWorkerOperationRevisionParityAndAbsentSourceBehavior(t *testing.T) {
	type entryDoc struct {
		ContractVersion   string `json:"contract_version"`
		Authenticated     bool   `json:"authenticated"`
		SourceRevision    string `json:"source_revision"`
		OperationRevision string `json:"operation_revision"`
		Status            string `json:"status"`
		Error             struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	doEntry := func(t *testing.T, h http.Handler, priorRev string) (int, entryDoc) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, testPublicOrigin+upgradeworker.EntryPath, nil)
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		if priorRev != "" {
			req.Header.Set("X-CPAMP-Upgrade-Source", priorRev)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var doc entryDoc
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatalf("decode entry response (%d %q): %v", rec.Code, rec.Body.String(), err)
		}
		return rec.Code, doc
	}

	t.Run("unmounted_source_bridges_offline_fallback_and_live_worker_with_operation_revision_parity", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		_, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, false)
		unmountedSource := filepath.Join(t.TempDir(), "unmounted-source")
		if err := os.MkdirAll(unmountedSource, 0o755); err != nil {
			t.Fatalf("mkdir unmounted source: %v", err)
		}
		dbPath := upgradeservice.TargetDatabasePath(targetRoot)
		dataKeyPath := filepath.Join(targetRoot, "data.key")
		op, found, err := upgradeservice.ReadRecordFromSnapshot(ctx, dbPath, "")
		if err != nil || !found {
			t.Fatalf("read prepared operation: found=%v err=%v", found, err)
		}
		recordedFP := strings.ToLower(strings.TrimSpace(op.SourceFingerprintAfter))
		expectedOpRev := upgradeworker.OperationRevision(op)
		if len(recordedFP) != 64 || len(expectedOpRev) != 64 {
			t.Fatalf("invalid prepared operation digests: source=%q operation=%q", recordedFP, expectedOpRev)
		}

		socketPath := newShortSocketPath(t)
		verifier := upgradeworker.NewPreparedCredentialVerifier(dbPath)
		client := upgradeworker.NewClient(socketPath, testPublicOrigin)
		client.FallbackAuthenticateIdentity = verifier.AuthenticateIdentity

		codeOffline, docOffline := doEntry(t, client, "")
		if codeOffline != http.StatusOK || docOffline.Status != "observation_unavailable" ||
			docOffline.SourceRevision != recordedFP || docOffline.OperationRevision != expectedOpRev {
			t.Fatalf("offline fallback = (%d, %+v), want 200 observation_unavailable source=%q operation=%q",
				codeOffline, docOffline, recordedFP, expectedOpRev)
		}

		worker, err := upgradeworker.New(ctx, upgradeworker.Options{
			Config:          config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath},
			TargetRoot:      targetRoot,
			SourceRoot:      unmountedSource,
			BundleDir:       bundleDir,
			PublicOrigin:    testPublicOrigin,
			RepairOwnership: func(string) error { return nil },
		})
		if err != nil {
			t.Fatalf("upgradeworker.New(unmounted source): %v", err)
		}
		defer worker.Wait()
		serveWorkerOverUnixSocket(t, ctx, socketPath, worker)

		// Bridging from offline fallback to live worker with unmounted source preserves recordedFP and operation_revision.
		codeBridged, docBridged := doEntry(t, client, docOffline.SourceRevision)
		if codeBridged != http.StatusOK || docBridged.Status != "observation_unavailable" ||
			docBridged.SourceRevision != recordedFP || docBridged.OperationRevision != expectedOpRev {
			t.Fatalf("unmounted live bridge = (%d, %+v), want 200 observation_unavailable source=%q operation=%q",
				codeBridged, docBridged, recordedFP, expectedOpRev)
		}

		codeFresh, docFresh := doEntry(t, client, "")
		if codeFresh != http.StatusOK || docFresh.Status != "observation_unavailable" ||
			docFresh.SourceRevision != recordedFP || docFresh.OperationRevision != expectedOpRev {
			t.Fatalf("unmounted live initial sign-in = (%d, %+v), want 200 observation_unavailable source=%q operation=%q",
				codeFresh, docFresh, recordedFP, expectedOpRev)
		}

		codeMismatch, docMismatch := doEntry(t, client, strings.Repeat("0", 64))
		if codeMismatch != http.StatusServiceUnavailable || docMismatch.Error.Code != "source_unavailable" {
			t.Fatalf("unmounted mismatched renewal = (%d, %+v), want 503 source_unavailable", codeMismatch, docMismatch)
		}
	})

	t.Run("live_observation_followed_by_vanished_source_fails_closed_for_both_signin_and_renewal", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, false)
		dbPath := upgradeservice.TargetDatabasePath(targetRoot)
		dataKeyPath := filepath.Join(targetRoot, "data.key")
		op, found, err := upgradeservice.ReadRecordFromSnapshot(ctx, dbPath, "")
		if err != nil || !found {
			t.Fatalf("read prepared operation: found=%v err=%v", found, err)
		}
		expectedOpRev := upgradeworker.OperationRevision(op)

		socketPath := newShortSocketPath(t)
		verifier := upgradeworker.NewPreparedCredentialVerifier(dbPath)
		client := upgradeworker.NewClient(socketPath, testPublicOrigin)
		client.FallbackAuthenticateIdentity = verifier.AuthenticateIdentity

		worker, err := upgradeworker.New(ctx, upgradeworker.Options{
			Config:          config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath},
			TargetRoot:      targetRoot,
			SourceRoot:      sourceRoot,
			BundleDir:       bundleDir,
			PublicOrigin:    testPublicOrigin,
			RepairOwnership: func(string) error { return nil },
		})
		if err != nil {
			t.Fatalf("upgradeworker.New: %v", err)
		}
		defer worker.Wait()
		serveWorkerOverUnixSocket(t, ctx, socketPath, worker)

		codeLive, docLive := doEntry(t, client, "")
		if codeLive != http.StatusOK || docLive.Status != "unsupported_upgrade" ||
			len(docLive.SourceRevision) != 64 || docLive.OperationRevision != expectedOpRev {
			t.Fatalf("live initial sign-in = (%d, %+v), want 200 unsupported_upgrade operation=%q",
				codeLive, docLive, expectedOpRev)
		}

		// Remove source database and data.key so obsState transitions from OK to sourceObservationAbsent.
		sourceDBPath := upgradeservice.SourceDatabasePath(sourceRoot)
		if err := os.Remove(sourceDBPath); err != nil {
			t.Fatalf("remove source db: %v", err)
		}
		if err := os.Remove(filepath.Join(sourceRoot, "data.key")); err != nil {
			t.Fatalf("remove source data.key: %v", err)
		}

		codeRenewVanished, docRenewVanished := doEntry(t, client, docLive.SourceRevision)
		if codeRenewVanished != http.StatusServiceUnavailable || docRenewVanished.Error.Code != "source_unavailable" {
			t.Fatalf("renewal after live source vanished = (%d, %+v), want 503 source_unavailable", codeRenewVanished, docRenewVanished)
		}
		codeInitVanished, docInitVanished := doEntry(t, client, "")
		if codeInitVanished != http.StatusServiceUnavailable || docInitVanished.Error.Code != "source_unavailable" {
			t.Fatalf("initial sign-in after live source vanished = (%d, %+v), want 503 source_unavailable", codeInitVanished, docInitVanished)
		}
	})
}

func TestWorkerSourceObservationDetectsSHMSymlinkWALFrameMutationAndArchiveMutation(t *testing.T) {
	doEntry := func(t *testing.T, h http.Handler, priorRev string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, testPublicOrigin+upgradeworker.EntryPath, nil)
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		if priorRev != "" {
			req.Header.Set("X-CPAMP-Upgrade-Source", priorRev)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			return rec.Code, ""
		}
		var doc struct {
			SourceRevision string `json:"source_revision"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatalf("decode entry response: %v", err)
		}
		return rec.Code, doc.SourceRevision
	}

	t.Run("symlinked_shm_and_inplace_wal_frame_mutation_past_byte_31", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sourceRoot, _, _ := newLegacySourceAndCopiedTarget(t, false)
		sourceDBPath := upgradeservice.SourceDatabasePath(sourceRoot)

		// Seed a valid SQLite WAL with a trailing old-salt frame so CopyIn records the WAL-backed source.
		walDB, err := sql.Open("sqlite", sourceDBPath)
		if err != nil {
			t.Fatalf("open source db for WAL seed: %v", err)
		}
		for _, stmt := range []string{
			`pragma journal_mode = wal`,
			`pragma wal_autocheckpoint = 0`,
			`insert into settings (key, value, updated_at_ms) values ('wal_seed', 'v1', 1)`,
		} {
			if _, err := walDB.ExecContext(ctx, stmt); err != nil {
				_ = walDB.Close()
				t.Fatalf("exec %q: %v", stmt, err)
			}
		}
		walBytes, err := os.ReadFile(sourceDBPath + "-wal")
		if err != nil || len(walBytes) < 64 {
			_ = walDB.Close()
			t.Fatalf("read seeded wal: len=%d err=%v", len(walBytes), err)
		}
		if err := walDB.Close(); err != nil {
			t.Fatalf("close source db after WAL seed: %v", err)
		}
		_ = os.Remove(sourceDBPath + "-shm")

		pageSize := binary.BigEndian.Uint32(walBytes[8:12])
		oldFrame := make([]byte, 24+int(pageSize))
		binary.BigEndian.PutUint32(oldFrame[0:4], 1)
		binary.BigEndian.PutUint32(oldFrame[8:12], binary.BigEndian.Uint32(walBytes[16:20])-1)
		binary.BigEndian.PutUint32(oldFrame[12:16], binary.BigEndian.Uint32(walBytes[20:24])^0x12345678)
		recycledWAL := append(append([]byte(nil), walBytes...), oldFrame...)
		if err := os.WriteFile(sourceDBPath+"-wal", recycledWAL, 0o600); err != nil {
			t.Fatalf("write recycled wal: %v", err)
		}

		// Re-run CopyIn into a fresh targetRoot/bundleDir so recordedFP reflects the WAL-backed source.
		walTargetRoot, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("resolve wal target root: %v", err)
		}
		walBundleRoot, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("resolve wal bundle root: %v", err)
		}
		walBundleDir := filepath.Join(walBundleRoot, "bundle")
		sourceLock, err := processlock.AcquireExistingReadOnly(sourceDBPath)
		if err != nil {
			t.Fatalf("acquire source lock: %v", err)
		}
		copyOut, err := upgradeservice.CopyIn(ctx, upgradeservice.Options{
			LegacyRoot: sourceRoot,
			TargetRoot: walTargetRoot,
			BundleDir:  walBundleDir,
			Lock:       sourceLock,
		})
		_ = sourceLock.Close()
		if err != nil {
			t.Fatalf("CopyIn WAL source: %v", err)
		}
		recordedFP := strings.ToLower(strings.TrimSpace(copyOut.SourceFingerprintAfter))

		worker, err := upgradeworker.New(ctx, upgradeworker.Options{
			Config:          config.Config{DBPath: upgradeservice.TargetDatabasePath(walTargetRoot), DataKeyPath: filepath.Join(walTargetRoot, "data.key")},
			TargetRoot:      walTargetRoot,
			SourceRoot:      sourceRoot,
			BundleDir:       walBundleDir,
			PublicOrigin:    testPublicOrigin,
			RepairOwnership: func(string) error { return nil },
		})
		if err != nil {
			t.Fatalf("upgradeworker.New: %v", err)
		}
		defer worker.Wait()
		codeInit, rev1 := doEntry(t, worker, "")
		if codeInit != http.StatusOK || len(rev1) != 64 {
			t.Fatalf("initial sign-in = (%d, %q), want 200 64-hex", codeInit, rev1)
		}
		if codeBridge, bridgedRev := doEntry(t, worker, recordedFP); codeBridge != http.StatusOK || bridgedRev != rev1 {
			t.Fatalf("initial bridge = (%d, %q), want (200, %q)", codeBridge, bridgedRev, rev1)
		}

		// 1. Symlinked -shm must fail closed with 503 source_unavailable.
		shmPath := sourceDBPath + "-shm"
		if err := os.Symlink(sourceDBPath, shmPath); err != nil {
			t.Fatalf("symlink -shm: %v", err)
		}
		if code, _ := doEntry(t, worker, rev1); code != http.StatusServiceUnavailable {
			t.Fatalf("renewal with symlinked -shm = %d, want 503", code)
		}
		if code, _ := doEntry(t, worker, ""); code != http.StatusServiceUnavailable {
			t.Fatalf("sign-in with symlinked -shm = %d, want 503", code)
		}
		if code, _ := doEntry(t, worker, recordedFP); code != http.StatusServiceUnavailable {
			t.Fatalf("bridge renewal with symlinked -shm = %d, want 503", code)
		}
		if err := os.Remove(shmPath); err != nil {
			t.Fatalf("remove symlinked -shm: %v", err)
		}
		if code, revRestored := doEntry(t, worker, rev1); code != http.StatusOK || revRestored != rev1 {
			t.Fatalf("renewal after removing symlinked -shm = (%d, %q), want (200, %q)", code, revRestored, rev1)
		}

		// 2. Mutate one byte past byte 31 in -wal while preserving exact file Size and ModTime.
		walPath := sourceDBPath + "-wal"
		walBefore, err := os.Stat(walPath)
		if err != nil {
			t.Fatalf("stat wal before mutation: %v", err)
		}
		mutOffset := walBefore.Size() - 1
		if mutOffset <= 32 {
			t.Fatalf("expected wal size > 32, got %d", walBefore.Size())
		}
		wf, err := os.OpenFile(walPath, os.O_RDWR, 0)
		if err != nil {
			t.Fatalf("open wal for in-place mutation: %v", err)
		}
		var one [1]byte
		if _, err := wf.ReadAt(one[:], mutOffset); err != nil {
			_ = wf.Close()
			t.Fatalf("read wal byte: %v", err)
		}
		one[0] ^= 0xff
		if _, err := wf.WriteAt(one[:], mutOffset); err != nil {
			_ = wf.Close()
			t.Fatalf("write wal byte: %v", err)
		}
		if err := wf.Close(); err != nil {
			t.Fatalf("close mutated wal: %v", err)
		}
		if err := os.Chtimes(walPath, walBefore.ModTime(), walBefore.ModTime()); err != nil {
			t.Fatalf("restore wal mtime: %v", err)
		}
		walAfter, err := os.Stat(walPath)
		if err != nil {
			t.Fatalf("stat wal after mutation: %v", err)
		}
		if walAfter.Size() != walBefore.Size() || !walAfter.ModTime().Equal(walBefore.ModTime()) {
			t.Fatalf("wal size/mtime changed: before=(%d,%v) after=(%d,%v)", walBefore.Size(), walBefore.ModTime(), walAfter.Size(), walAfter.ModTime())
		}

		if code, _ := doEntry(t, worker, rev1); code != http.StatusConflict {
			t.Fatalf("renewal after in-place WAL frame mutation = %d, want 409", code)
		}
		if code, _ := doEntry(t, worker, recordedFP); code != http.StatusConflict {
			t.Fatalf("bridge renewal after in-place WAL frame mutation = %d, want 409", code)
		}
	})

	t.Run("usage_archives_mutation_invalidates_verified_fingerprint_cache", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, false)
		dbPath := upgradeservice.TargetDatabasePath(targetRoot)
		dataKeyPath := filepath.Join(targetRoot, "data.key")
		op, found, err := upgradeservice.ReadRecordFromSnapshot(ctx, dbPath, "")
		if err != nil || !found {
			t.Fatalf("read prepared operation: found=%v err=%v", found, err)
		}
		recordedFP := strings.ToLower(strings.TrimSpace(op.SourceFingerprintAfter))

		worker, err := upgradeworker.New(ctx, upgradeworker.Options{
			Config:          config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath},
			TargetRoot:      targetRoot,
			SourceRoot:      sourceRoot,
			BundleDir:       bundleDir,
			PublicOrigin:    testPublicOrigin,
			RepairOwnership: func(string) error { return nil },
		})
		if err != nil {
			t.Fatalf("upgradeworker.New: %v", err)
		}
		defer worker.Wait()

		// Warm verifiedFingerprintRevision cache via fallback->live bridge.
		code1, liveRev := doEntry(t, worker, recordedFP)
		if code1 != http.StatusOK || len(liveRev) != 64 {
			t.Fatalf("first bridge = (%d, %q), want 200", code1, liveRev)
		}
		if code2, cachedRev := doEntry(t, worker, recordedFP); code2 != http.StatusOK || cachedRev != liveRev {
			t.Fatalf("cached bridge = (%d, %q), want (200, %q)", code2, cachedRev, liveRev)
		}

		// Rewrite archive segment with identical byte size and restored ModTime.
		archivePath := filepath.Join(sourceRoot, "usage-archives", "2026-03", "segment-000001.jsonl")
		archiveBefore, err := os.Stat(archivePath)
		if err != nil {
			t.Fatalf("stat archive: %v", err)
		}
		replacement := []byte(`{"events":9}` + "\n")
		if int64(len(replacement)) != archiveBefore.Size() {
			t.Fatalf("replacement size = %d, want %d", len(replacement), archiveBefore.Size())
		}
		if err := os.WriteFile(archivePath, replacement, 0o600); err != nil {
			t.Fatalf("rewrite archive: %v", err)
		}
		if err := os.Chtimes(archivePath, archiveBefore.ModTime(), archiveBefore.ModTime()); err != nil {
			t.Fatalf("restore archive mtime: %v", err)
		}

		if codeAfter, _ := doEntry(t, worker, recordedFP); codeAfter != http.StatusConflict {
			t.Fatalf("bridge renewal after usage-archives mutation = %d, want 409", codeAfter)
		}
	})
}

func TestWorkerFallbackBridgeReturns503WhenSourceLockHeldExclusively(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, false)
	dbPath := upgradeservice.TargetDatabasePath(targetRoot)
	dataKeyPath := filepath.Join(targetRoot, "data.key")
	op, found, err := upgradeservice.ReadRecordFromSnapshot(ctx, dbPath, "")
	if err != nil || !found {
		t.Fatalf("read prepared operation: found=%v err=%v", found, err)
	}
	recordedFP := strings.ToLower(strings.TrimSpace(op.SourceFingerprintAfter))

	worker, err := upgradeworker.New(ctx, upgradeworker.Options{
		Config:          config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath},
		TargetRoot:      targetRoot,
		SourceRoot:      sourceRoot,
		BundleDir:       bundleDir,
		PublicOrigin:    testPublicOrigin,
		RepairOwnership: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("upgradeworker.New: %v", err)
	}
	defer worker.Wait()

	// Hold exclusive source lock while renewing from fallback revision -> must return 503 (never 409).
	heldLock, err := processlock.AcquireExisting(upgradeservice.SourceDatabasePath(sourceRoot))
	if err != nil {
		t.Fatalf("acquire exclusive source lock: %v", err)
	}

	doBridge := func() *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, testPublicOrigin+upgradeworker.EntryPath, nil)
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		req.Header.Set("X-CPAMP-Upgrade-Source", recordedFP)
		rec := httptest.NewRecorder()
		worker.ServeHTTP(rec, req)
		return rec
	}

	lockedRec := doBridge()
	if lockedRec.Code != http.StatusServiceUnavailable || !strings.Contains(lockedRec.Body.String(), `"source_unavailable"`) {
		_ = heldLock.Close()
		t.Fatalf("bridge renewal under exclusive source lock = %d (%s), want 503 source_unavailable", lockedRec.Code, lockedRec.Body.String())
	}

	if err := heldLock.Close(); err != nil {
		t.Fatalf("release exclusive source lock: %v", err)
	}

	unlockedRec := doBridge()
	if unlockedRec.Code != http.StatusOK {
		t.Fatalf("bridge renewal after releasing source lock = %d (%s), want 200", unlockedRec.Code, unlockedRec.Body.String())
	}
}

func TestWorkerRetainsParityGapsWithPersistedPendingDaysAcrossNonMismatchError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, true)
	dbPath := filepath.Join(targetRoot, "usage.sqlite")
	dataKeyPath := filepath.Join(targetRoot, "data.key")

	worker, err := upgradeworker.New(ctx, upgradeworker.Options{
		Config:          config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath},
		TargetRoot:      targetRoot,
		SourceRoot:      sourceRoot,
		BundleDir:       bundleDir,
		PublicOrigin:    testPublicOrigin,
		RepairOwnership: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("upgradeworker.New: %v", err)
	}
	defer worker.Wait()

	runReconcile := func() upgradeworker.Execution {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"action": "reconcile"})
		req := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/execute", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		worker.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("execute reconcile = %d: %s", rec.Code, rec.Body.String())
		}
		return waitUntilIdle(t, worker)
	}

	// 1. Unacknowledged reconcile persists PendingReconcileDays and sets parity gaps flag.
	firstState := runReconcile()
	if firstState.Code != string(upgradeservice.UpgradeErrorReconcileMismatch) {
		t.Fatalf("first reconcile state = %+v, want %s", firstState, upgradeservice.UpgradeErrorReconcileMismatch)
	}
	if days := worker.ReconciliationDays(); !reflect.DeepEqual(days, []string{"2026-02-15"}) || !worker.ReconciliationHasParityGaps() {
		t.Fatalf("after mismatch reconcile: days=%v gaps=%v, want [2026-02-15] true", days, worker.ReconciliationHasParityGaps())
	}

	// 2. Subsequent non-mismatch reconcile failure that leaves PendingReconcileDays persisted
	// (e.g., unreadable backup manifest before Reconcile mutates SQLite) keeps days and hasParityGaps=true.
	manifestPath := filepath.Join(bundleDir, "manifest.json")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read backup manifest: %v", err)
	}
	if err := os.Remove(manifestPath); err != nil {
		t.Fatalf("remove backup manifest: %v", err)
	}

	secondState := runReconcile()
	if secondState.Code == "" || secondState.Code == string(upgradeservice.UpgradeErrorReconcileMismatch) {
		t.Fatalf("second reconcile state = %+v, want non-mismatch backup failure", secondState)
	}
	if days := worker.ReconciliationDays(); !reflect.DeepEqual(days, []string{"2026-02-15"}) {
		t.Fatalf("ReconciliationDays after non-mismatch failure with persisted days = %v, want [2026-02-15]", days)
	}
	if !worker.ReconciliationHasParityGaps() {
		t.Fatal("ReconciliationHasParityGaps after non-mismatch failure with persisted days = false, want true")
	}

	if err := os.WriteFile(manifestPath, manifestBytes, 0o600); err != nil {
		t.Fatalf("restore backup manifest: %v", err)
	}
}

func TestWorkerExternalConnectCorruptBindingKeyCanonicalCodeAndExplicitOverride(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, false)
	externalCPA := startExternalV8Server(t, nil, nil)
	dbPath := filepath.Join(targetRoot, "usage.sqlite")
	dataKeyPath := filepath.Join(targetRoot, "data.key")
	cfg := config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath}

	worker, err := upgradeworker.New(ctx, upgradeworker.Options{
		Config:          cfg,
		TargetRoot:      targetRoot,
		SourceRoot:      sourceRoot,
		BundleDir:       bundleDir,
		PublicOrigin:    testPublicOrigin,
		RepairOwnership: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("upgradeworker.New: %v", err)
	}
	defer worker.Wait()
	worker.SetHandler(&upgradewizard.Handler{
		LegacyRoot:           func() string { return sourceRoot },
		Database:             func() string { return cfg.DBPath },
		DataKey:              func() string { return cfg.DataKeyPath },
		PublicOrigin:         testPublicOrigin,
		Authenticate:         worker.Authenticate,
		Execution:            worker.Execution,
		ReconciliationDays:   worker.ReconciliationDays,
		ReconciliationState:  worker.ReconciliationState,
		PreserveManagerStore: true,
	})

	doExec := func(payload map[string]any) upgradeworker.Execution {
		t.Helper()
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/execute", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		worker.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("execute %v = %d: %s", payload, rec.Code, rec.Body.String())
		}
		return waitUntilIdle(t, worker)
	}

	if state := doExec(map[string]any{"action": "reconcile"}); state.Code != "" {
		t.Fatalf("reconcile failed: %+v", state)
	}
	if state := doExec(map[string]any{
		"action":        "external-connect",
		"baseUrl":       externalCPA.URL,
		"managementKey": testExternalMgmtKey,
	}); state.Code != "" {
		t.Fatalf("initial external-connect failed: %+v", state)
	}

	// Seal a valid envelope under a foreign 32-byte key so UnprotectString fails.
	foreignKey := bytes.Repeat([]byte{0x42}, 32)
	foreignProtector, err := security.NewProtector(foreignKey)
	if err != nil {
		t.Fatalf("foreign protector: %v", err)
	}
	corruptEnvelope, err := foreignProtector.ProtectString("unreadable-secret")
	if err != nil {
		t.Fatalf("protect foreign envelope: %v", err)
	}
	rawDB, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	if _, err := rawDB.ExecContext(ctx, `UPDATE upgrade_external_connect SET management_key = ? WHERE id = 1`, corruptEnvelope); err != nil {
		_ = rawDB.Close()
		t.Fatalf("corrupt binding management_key: %v", err)
	}
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close sqlite db: %v", err)
	}

	// 1. Implicit reuse (without baseUrl) fails with canonical external_management_key_rejected.
	reuseState := doExec(map[string]any{"action": "external-connect"})
	if reuseState.Code != string(upgradeservice.UpgradeErrorExternalManagementRejected) {
		t.Fatalf("reuse with undecryptable binding key = %+v, want %s", reuseState, upgradeservice.UpgradeErrorExternalManagementRejected)
	}

	statusReq := httptest.NewRequest(http.MethodGet, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/status", nil)
	statusReq.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
	statusReq.Header.Set("Origin", testPublicOrigin)
	statusRec := httptest.NewRecorder()
	worker.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK || !strings.Contains(statusRec.Body.String(), `"external_management_key_rejected"`) {
		t.Fatalf("status after decrypt failure = %d (%s), want canonical external_management_key_rejected", statusRec.Code, statusRec.Body.String())
	}

	// 2. Explicit override with baseUrl + valid managementKey bypasses the broken stored envelope,
	// triggers reprove=true on the same URL, and heals the sealed management_key in place.
	overrideState := doExec(map[string]any{
		"action":        "external-connect",
		"baseUrl":       externalCPA.URL,
		"managementKey": testExternalMgmtKey,
	})
	if overrideState.Code != "" {
		t.Fatalf("explicit override after decrypt failure = %+v, want clean success", overrideState)
	}

	// 3. Subsequent implicit reuse succeeds again using the healed sealed key.
	healedReuse := doExec(map[string]any{"action": "external-connect"})
	if healedReuse.Code != "" {
		t.Fatalf("implicit reuse after healing = %+v, want clean success", healedReuse)
	}
}

func TestWorkerReconciliationDaysMonotonicityGuardAgainstStaleSnapshot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, true)
	dbPath := filepath.Join(targetRoot, "usage.sqlite")
	dataKeyPath := filepath.Join(targetRoot, "data.key")

	worker, err := upgradeworker.New(ctx, upgradeworker.Options{
		Config:          config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath},
		TargetRoot:      targetRoot,
		SourceRoot:      sourceRoot,
		BundleDir:       bundleDir,
		PublicOrigin:    testPublicOrigin,
		RepairOwnership: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("upgradeworker.New: %v", err)
	}
	defer worker.Wait()

	doReconcile := func(ack bool) upgradeworker.Execution {
		t.Helper()
		payload := map[string]any{"action": "reconcile"}
		if ack {
			payload["acknowledgeDays"] = []string{"2026-02-15"}
			payload["acknowledgeParityGaps"] = true
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/execute", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		worker.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("execute reconcile = %d: %s", rec.Code, rec.Body.String())
		}
		return waitUntilIdle(t, worker)
	}

	if st := doReconcile(false); st.Code != string(upgradeservice.UpgradeErrorReconcileMismatch) {
		t.Fatalf("unacknowledged reconcile = %+v, want mismatch", st)
	}
	if st := doReconcile(true); st.Code != "" {
		t.Fatalf("acknowledged reconcile = %+v, want clean success", st)
	}
	if days := worker.ReconciliationDays(); len(days) != 0 {
		t.Fatalf("ReconciliationDays after acknowledged reconcile = %v, want empty", days)
	}

	rawDB, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	var clearedRev int64
	if err := rawDB.QueryRowContext(ctx, `SELECT revision FROM upgrade_operations WHERE id = 1`).Scan(&clearedRev); err != nil {
		_ = rawDB.Close()
		t.Fatalf("query revision: %v", err)
	}
	if clearedRev < 2 {
		_ = rawDB.Close()
		t.Fatalf("cleared revision = %d, want >= 2", clearedRev)
	}
	// Simulate an out-of-order stale snapshot carrying an older revision < clearedRev with non-empty days.
	if _, err := rawDB.ExecContext(ctx, `UPDATE upgrade_operations SET revision = ?, pending_reconcile_days = '["2026-02-15"]' WHERE id = 1`, clearedRev-1); err != nil {
		_ = rawDB.Close()
		t.Fatalf("downgrade revision for stale snapshot simulation: %v", err)
	}
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close sqlite db: %v", err)
	}

	if days := worker.ReconciliationDays(); len(days) != 0 {
		t.Fatalf("ReconciliationDays from stale lower revision = %v, want empty", days)
	}

	// A snapshot at >= clearedRev updates ReconciliationDays normally.
	rawDB2, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen sqlite db: %v", err)
	}
	if _, err := rawDB2.ExecContext(ctx, `UPDATE upgrade_operations SET revision = ?, pending_reconcile_days = '["2026-02-16"]' WHERE id = 1`, clearedRev+1); err != nil {
		_ = rawDB2.Close()
		t.Fatalf("advance revision for fresh snapshot: %v", err)
	}
	if err := rawDB2.Close(); err != nil {
		t.Fatalf("close sqlite db: %v", err)
	}

	if days := worker.ReconciliationDays(); !reflect.DeepEqual(days, []string{"2026-02-16"}) {
		t.Fatalf("ReconciliationDays at newer revision = %v, want [2026-02-16]", days)
	}
}

func TestWorkerReconcilePreservesParityGapsAcrossTransientLockAtCopyIn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, true)
	dbPath := filepath.Join(targetRoot, "usage.sqlite")
	dataKeyPath := filepath.Join(targetRoot, "data.key")

	worker, err := upgradeworker.New(ctx, upgradeworker.Options{
		Config:          config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath},
		TargetRoot:      targetRoot,
		SourceRoot:      sourceRoot,
		BundleDir:       bundleDir,
		PublicOrigin:    testPublicOrigin,
		RepairOwnership: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("upgradeworker.New: %v", err)
	}
	defer worker.Wait()

	if days := worker.ReconciliationDays(); !reflect.DeepEqual(days, []string{"2026-02-15"}) || !worker.ReconciliationHasParityGaps() {
		t.Fatalf("initial parity-gap state = (%v, %v), want ([2026-02-15], true)", days, worker.ReconciliationHasParityGaps())
	}

	heldTargetLock, err := processlock.Acquire(dbPath)
	if err != nil {
		t.Fatalf("acquire target lock: %v", err)
	}

	body, _ := json.Marshal(map[string]any{"action": "reconcile"})
	req := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/execute", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
	req.Header.Set("Origin", testPublicOrigin)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	worker.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		_ = heldTargetLock.Close()
		t.Fatalf("execute reconcile = %d: %s", rec.Code, rec.Body.String())
	}
	lockedState := waitUntilIdle(t, worker)
	if err := heldTargetLock.Close(); err != nil {
		t.Fatalf("release target lock: %v", err)
	}
	if lockedState.Code != string(upgradeservice.UpgradeErrorTargetLocked) && lockedState.Code != string(upgradeservice.UpgradeErrorStoreLocked) {
		t.Fatalf("reconcile under held target lock = %+v, want target/store locked", lockedState)
	}
	if days := worker.ReconciliationDays(); !reflect.DeepEqual(days, []string{"2026-02-15"}) {
		t.Fatalf("ReconciliationDays after transient lock failure at copy_in = %v, want [2026-02-15]", days)
	}
	if !worker.ReconciliationHasParityGaps() {
		t.Fatal("ReconciliationHasParityGaps after transient lock failure at copy_in = false, want true")
	}
}

func startLocalhostExternalV8Server(t *testing.T, activeKey *atomic.Value) (*httptest.Server, int) {
	t.Helper()
	addrs, err := net.DefaultResolver.LookupIPAddr(context.Background(), "localhost")
	if err != nil || len(addrs) == 0 {
		t.Skipf("localhost did not resolve: %v", err)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(addrs[0].IP.String(), "0"))
	if err != nil {
		t.Fatalf("listen on localhost address %s: %v", addrs[0].IP.String(), err)
	}
	const v8ConfigJSON = `{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"/auth"},"access":{"api-keys":["client-key-1"]}}`
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-test"}]}`))
			return
		}
		expectedKey, _ := activeKey.Load().(string)
		if expectedKey == "" || r.Header.Get("Authorization") != "Bearer "+expectedKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v0/management/config":
			w.Header().Set("X-CPA-Version", "v8.0.55")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case "/v8/management/config":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(v8ConfigJSON))
		case "/v8/management/config/access/api-keys":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`["client-key-1"]`))
		case "/v8/management/config.yaml":
			w.Header().Set("Content-Type", "application/yaml")
			_, _ = w.Write([]byte("port: 8317\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("expected *net.TCPAddr, got %T", ln.Addr())
	}
	return srv, tcpAddr.Port
}

func TestExternalURLCanonicalizationHelpers(t *testing.T) {
	got, err := upgradeworker.CanonicalExternalBaseURL("HTTP://CPA.Example:80/")
	if err != nil || got != "http://cpa.example" {
		t.Fatalf("CanonicalExternalBaseURL(HTTP://CPA.Example:80/) = (%q, %v), want (http://cpa.example, nil)", got, err)
	}
	gotHTTPS, err := upgradeworker.CanonicalExternalBaseURL("HTTPS://CPA.Example:443/")
	if err != nil || gotHTTPS != "https://cpa.example" {
		t.Fatalf("CanonicalExternalBaseURL(HTTPS://CPA.Example:443/) = (%q, %v), want (https://cpa.example, nil)", gotHTTPS, err)
	}
	gotV0Mgmt, err := upgradeworker.CanonicalExternalBaseURL("HTTP://CPA.Example:80/v0/management/")
	if err != nil || gotV0Mgmt != "http://cpa.example/v0/management" {
		t.Fatalf("CanonicalExternalBaseURL(HTTP://CPA.Example:80/v0/management/) = (%q, %v), want (http://cpa.example/v0/management, nil)", gotV0Mgmt, err)
	}
	gotV0, err := upgradeworker.CanonicalExternalBaseURL("HTTPS://CPA.Example:443/v0/")
	if err != nil || gotV0 != "https://cpa.example/v0" {
		t.Fatalf("CanonicalExternalBaseURL(HTTPS://CPA.Example:443/v0/) = (%q, %v), want (https://cpa.example/v0, nil)", gotV0, err)
	}
	gotIPv6, err := upgradeworker.CanonicalExternalBaseURL("HTTPS://[2001:DB8::1]:443/v0/management/")
	if err != nil || gotIPv6 != "https://[2001:db8::1]/v0/management" {
		t.Fatalf("CanonicalExternalBaseURL(HTTPS://[2001:DB8::1]:443/v0/management/) = (%q, %v), want (https://[2001:db8::1]/v0/management, nil)", gotIPv6, err)
	}
	for _, schemeless := range []string{"127.0.0.1:8317", "127.0.0.1:8317/v0/management", "CPA.Example:80/"} {
		if gotSchemeless, schemelessErr := upgradeworker.CanonicalExternalBaseURL(schemeless); schemelessErr == nil || gotSchemeless != "" {
			t.Fatalf("CanonicalExternalBaseURL(%q) = (%q, %v), want empty error", schemeless, gotSchemeless, schemelessErr)
		}
	}

	if !upgradeworker.SameExternalBaseURL("http://LOCALHOST:80/", "http://localhost") {
		t.Fatal("expected http://LOCALHOST:80/ and http://localhost to match canonically")
	}
	if !upgradeworker.SameExternalBaseURL("https://CPA.Example:443", "HTTPS://cpa.example/") {
		t.Fatal("expected https://CPA.Example:443 and HTTPS://cpa.example/ to match canonically")
	}
	if !upgradeworker.SameExternalBaseURL("HTTP://[::1]:80/v0/management/", "http://[::1]/v0/management") {
		t.Fatal("expected canonical IPv6 /v0/management URLs to match")
	}
	if !upgradeworker.SameExternalBaseURL("http://127.0.0.1:8317/v0/management", "HTTP://127.0.0.1:8317/v0/management/") {
		t.Fatal("expected explicit /v0/management URLs to match canonically")
	}
	if upgradeworker.SameExternalBaseURL("http://127.0.0.1:8317", "http://127.0.0.1:8318") {
		t.Fatal("expected distinct ports not to match")
	}
	if upgradeworker.SameExternalBaseURL("http://127.0.0.1:8317", "http://127.0.0.1:8317/v0/management") {
		t.Fatal("expected distinct paths (/ vs /v0/management) not to match")
	}
	if upgradeworker.SameExternalBaseURL("http://127.0.0.1:8317", "http://127.0.0.1:8317/v0") {
		t.Fatal("expected distinct paths (/ vs /v0) not to match")
	}
	if upgradeworker.SameExternalBaseURL("http://127.0.0.1:8317/v0", "http://127.0.0.1:8317/v0/management") {
		t.Fatal("expected /v0 and /v0/management not to match")
	}
	if upgradeworker.SameExternalBaseURL("http://[::1]/v0/management", "http://[::1]") {
		t.Fatal("expected IPv6 /v0/management not to match IPv6 root")
	}
	if upgradeworker.SameExternalBaseURL("127.0.0.1:8317", "http://127.0.0.1:8317/") ||
		upgradeworker.SameExternalBaseURL("127.0.0.1:8317", "127.0.0.1:8317") {
		t.Fatal("expected schemeless URLs to be rejected by SameExternalBaseURL")
	}
	if upgradeworker.SameExternalBaseURL("127.0.0.1:8317/v0/management", "HTTP://127.0.0.1:8317/v0/management/") {
		t.Fatal("expected schemeless /v0/management URL to be rejected by SameExternalBaseURL")
	}
	if upgradeworker.SameExternalBaseURL("CPA.Example:80/", "HTTP://cpa.example") {
		t.Fatal("expected schemeless host to be rejected by SameExternalBaseURL")
	}
	if upgradeworker.SameExternalBaseURL("http://127.0.0.1:8317?foo=1", "http://127.0.0.1:8317") {
		t.Fatal("expected query URL not to match bare base URL")
	}
	if upgradeworker.SameExternalBaseURL("http://127.0.0.1:8317?foo=1", "http://127.0.0.1:8317?foo=1") {
		t.Fatal("expected query URLs to be rejected by SameExternalBaseURL")
	}
	if upgradeworker.SameExternalBaseURL("http://127.0.0.1:8317#frag", "http://127.0.0.1:8317") ||
		upgradeworker.SameExternalBaseURL("http://127.0.0.1:8317#frag", "http://127.0.0.1:8317#frag") {
		t.Fatal("expected fragment URLs to be rejected by SameExternalBaseURL")
	}
}

func TestWorkerExternalConnectCanonicalizesURLAcrossSyncAndKeyRotation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var activeKey atomic.Value
	activeKey.Store(testExternalMgmtKey)
	_, port := startLocalhostExternalV8Server(t, &activeKey)
	nonCanonicalURL := fmt.Sprintf("HTTP://LOCALHOST:%d/", port)
	canonicalURL := fmt.Sprintf("http://localhost:%d", port)

	sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, false)
	dbPath := filepath.Join(targetRoot, "usage.sqlite")
	dataKeyPath := filepath.Join(targetRoot, "data.key")
	cfg := config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath}

	worker, err := upgradeworker.New(ctx, upgradeworker.Options{
		Config:          cfg,
		TargetRoot:      targetRoot,
		SourceRoot:      sourceRoot,
		BundleDir:       bundleDir,
		PublicOrigin:    testPublicOrigin,
		RepairOwnership: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("upgradeworker.New: %v", err)
	}
	defer worker.Wait()

	doExec := func(payload map[string]any) upgradeworker.Execution {
		t.Helper()
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/execute", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		worker.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("execute %v = %d: %s", payload, rec.Code, rec.Body.String())
		}
		return waitUntilIdle(t, worker)
	}

	if st := doExec(map[string]any{"action": "reconcile"}); st.Code != "" {
		t.Fatalf("reconcile failed: %+v", st)
	}

	// 1. First-time explicit external-connect with mixed-case URL + trailing slash syncs cleanly.
	if st := doExec(map[string]any{
		"action":        "external-connect",
		"baseUrl":       nonCanonicalURL,
		"managementKey": testExternalMgmtKey,
	}); st.Code != "" {
		t.Fatalf("first external-connect with %q failed: %+v", nonCanonicalURL, st)
	}

	keyBytes, err := security.LoadExistingDataKey("", dataKeyPath)
	if err != nil {
		t.Fatalf("load data key: %v", err)
	}
	protector, err := security.NewProtector(keyBytes)
	if err != nil {
		t.Fatalf("new protector: %v", err)
	}
	stStore, err := store.Open(dbPath, protector)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	managerCfg, ok, err := stStore.LoadManagerConfig(ctx)
	if err != nil || !ok {
		_ = stStore.Close()
		t.Fatalf("LoadManagerConfig = (%+v, %v, %v)", managerCfg, ok, err)
	}
	if managerCfg.CPAConnection.CPABaseURL != canonicalURL || managerCfg.CPAConnection.ManagementKey != testExternalMgmtKey {
		_ = stStore.Close()
		t.Fatalf("stored CPAConnection = %+v, want baseURL=%q key=%q", managerCfg.CPAConnection, canonicalURL, testExternalMgmtKey)
	}
	setupCfg, setupOK, err := stStore.LoadSetup(ctx)
	if err != nil || !setupOK || setupCfg.CPAUpstreamURL != canonicalURL {
		_ = stStore.Close()
		t.Fatalf("stored Setup = (%+v, %v, %v), want CPAUpstreamURL=%q", setupCfg, setupOK, err, canonicalURL)
	}
	_ = stStore.Close()

	// 2. Key rotation supplying a canonically-equivalent spelling sets reprove=true and rotates cleanly.
	rotatedKey := "rotated-external-v8-mgmt-secret"
	activeKey.Store(rotatedKey)
	if st := doExec(map[string]any{
		"action":        "external-connect",
		"baseUrl":       nonCanonicalURL,
		"managementKey": rotatedKey,
	}); st.Code != "" {
		t.Fatalf("key rotation with canonically-equivalent URL %q failed: %+v", nonCanonicalURL, st)
	}

	stStore2, err := store.Open(dbPath, protector)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	managerCfg2, ok2, err2 := stStore2.LoadManagerConfig(ctx)
	_ = stStore2.Close()
	if err2 != nil || !ok2 || managerCfg2.CPAConnection.CPABaseURL != canonicalURL || managerCfg2.CPAConnection.ManagementKey != rotatedKey {
		t.Fatalf("rotated CPAConnection = (%+v, %v, %v), want baseURL=%q key=%q", managerCfg2.CPAConnection, ok2, err2, canonicalURL, rotatedKey)
	}

	// 3. Corrupt binding ciphertext overridden with canonically-equivalent URL heals cleanly.
	foreignKey := bytes.Repeat([]byte{0x55}, 32)
	foreignProtector, err := security.NewProtector(foreignKey)
	if err != nil {
		t.Fatalf("foreign protector: %v", err)
	}
	corruptEnvelope, err := foreignProtector.ProtectString("unreadable-secret")
	if err != nil {
		t.Fatalf("protect foreign envelope: %v", err)
	}
	rawDB, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	if _, err := rawDB.ExecContext(ctx, `UPDATE upgrade_external_connect SET management_key = ? WHERE id = 1`, corruptEnvelope); err != nil {
		_ = rawDB.Close()
		t.Fatalf("corrupt binding management_key: %v", err)
	}
	_ = rawDB.Close()

	if st := doExec(map[string]any{
		"action":        "external-connect",
		"baseUrl":       nonCanonicalURL,
		"managementKey": rotatedKey,
	}); st.Code != "" {
		t.Fatalf("corrupt ciphertext override with canonically-equivalent URL failed: %+v", st)
	}
	if st := doExec(map[string]any{"action": "external-connect"}); st.Code != "" {
		t.Fatalf("implicit external-connect after canonical healing failed: %+v", st)
	}
}

func TestWorkerReconciliationStateAtomicAndClearedNeverResurrectsWhenPostRunSnapshotFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	t.Run("cleared_reconcile_advances_revision_even_when_post_run_snapshot_fails", func(t *testing.T) {
		sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, true)
		dbPath := filepath.Join(targetRoot, "usage.sqlite")
		dataKeyPath := filepath.Join(targetRoot, "data.key")

		worker, err := upgradeworker.New(ctx, upgradeworker.Options{
			Config:          config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath},
			TargetRoot:      targetRoot,
			SourceRoot:      sourceRoot,
			BundleDir:       bundleDir,
			PublicOrigin:    testPublicOrigin,
			RepairOwnership: func(string) error { return nil },
		})
		if err != nil {
			t.Fatalf("upgradeworker.New: %v", err)
		}
		defer worker.Wait()

		days, hasGaps := worker.ReconciliationState()
		if !reflect.DeepEqual(days, []string{"2026-02-15"}) || !hasGaps {
			t.Fatalf("initial ReconciliationState = (%v, %v), want ([2026-02-15], true)", days, hasGaps)
		}

		doReconcile := func(ack bool) upgradeworker.Execution {
			t.Helper()
			payload := map[string]any{"action": "reconcile"}
			if ack {
				payload["acknowledgeDays"] = []string{"2026-02-15"}
				payload["acknowledgeParityGaps"] = true
			}
			body, _ := json.Marshal(payload)
			req := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/execute", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
			req.Header.Set("Origin", testPublicOrigin)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			worker.ServeHTTP(rec, req)
			if rec.Code != http.StatusAccepted {
				t.Fatalf("execute reconcile = %d: %s", rec.Code, rec.Body.String())
			}
			return waitUntilIdle(t, worker)
		}

		if st := doReconcile(false); st.Code != string(upgradeservice.UpgradeErrorReconcileMismatch) {
			t.Fatalf("unacknowledged reconcile = %+v, want mismatch", st)
		}
		days, hasGaps = worker.ReconciliationState()
		if !reflect.DeepEqual(days, []string{"2026-02-15"}) || !hasGaps {
			t.Fatalf("after mismatch ReconciliationState = (%v, %v), want ([2026-02-15], true)", days, hasGaps)
		}

		rawDB, err := sqliterepo.Open(dbPath)
		if err != nil {
			t.Fatalf("open sqlite db: %v", err)
		}
		var preClearRev int64
		if err := rawDB.QueryRowContext(ctx, `SELECT revision FROM upgrade_operations WHERE id = 1`).Scan(&preClearRev); err != nil {
			_ = rawDB.Close()
			t.Fatalf("query pre-clear revision: %v", err)
		}
		_ = rawDB.Close()

		var postRunCalls atomic.Int32
		restore := upgradeworker.SetReadReconcilePostRunSnapshotForTest(func(snapCtx context.Context, _, _ string) (model.UpgradeOperation, bool, error) {
			postRunCalls.Add(1)
			if snapCtx.Err() != nil {
				return model.UpgradeOperation{}, false, snapCtx.Err()
			}
			if _, hasDeadline := snapCtx.Deadline(); !hasDeadline {
				return model.UpgradeOperation{}, false, errors.New("expected bounded post-run snapshot deadline")
			}
			return model.UpgradeOperation{}, false, errors.New("injected post-run snapshot read failure")
		})
		defer restore()

		if st := doReconcile(true); st.Code != "" {
			t.Fatalf("acknowledged reconcile = %+v, want clean success", st)
		}
		if postRunCalls.Load() != 1 {
			t.Fatalf("post-run snapshot calls = %d, want 1", postRunCalls.Load())
		}
		days, hasGaps = worker.ReconciliationState()
		if len(days) != 0 || hasGaps {
			t.Fatalf("ReconciliationState after cleared reconcile = (%v, %v), want ([], false)", days, hasGaps)
		}

		// Simulate a stale pre-commit snapshot carrying preClearRev with pending days.
		rawDB2, err := sqliterepo.Open(dbPath)
		if err != nil {
			t.Fatalf("reopen sqlite db: %v", err)
		}
		if _, err := rawDB2.ExecContext(ctx, `UPDATE upgrade_operations SET revision = ?, pending_reconcile_days = '["2026-02-15"]' WHERE id = 1`, preClearRev); err != nil {
			_ = rawDB2.Close()
			t.Fatalf("downgrade revision for stale snapshot simulation: %v", err)
		}
		_ = rawDB2.Close()

		days, hasGaps = worker.ReconciliationState()
		if len(days) != 0 || hasGaps {
			t.Fatalf("ReconciliationState resurrected stale days at preClearRev=%d: (%v, %v)", preClearRev, days, hasGaps)
		}
	})

	t.Run("non_transient_target_incomplete_clears_days_even_when_post_run_snapshot_fails", func(t *testing.T) {
		sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, true)
		dbPath := filepath.Join(targetRoot, "usage.sqlite")
		dataKeyPath := filepath.Join(targetRoot, "data.key")

		worker, err := upgradeworker.New(ctx, upgradeworker.Options{
			Config:          config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath},
			TargetRoot:      targetRoot,
			SourceRoot:      sourceRoot,
			BundleDir:       bundleDir,
			PublicOrigin:    testPublicOrigin,
			RepairOwnership: func(string) error { return nil },
		})
		if err != nil {
			t.Fatalf("upgradeworker.New: %v", err)
		}
		defer worker.Wait()

		doReconcile := func() upgradeworker.Execution {
			t.Helper()
			body, _ := json.Marshal(map[string]any{"action": "reconcile"})
			req := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/execute", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
			req.Header.Set("Origin", testPublicOrigin)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			worker.ServeHTTP(rec, req)
			if rec.Code != http.StatusAccepted {
				t.Fatalf("execute reconcile = %d: %s", rec.Code, rec.Body.String())
			}
			return waitUntilIdle(t, worker)
		}

		if st := doReconcile(); st.Code != string(upgradeservice.UpgradeErrorReconcileMismatch) {
			t.Fatalf("unacknowledged reconcile = %+v, want mismatch", st)
		}

		rawDB, err := sqliterepo.Open(dbPath)
		if err != nil {
			t.Fatalf("open target db: %v", err)
		}
		var preIncompleteRev int64
		if err := rawDB.QueryRowContext(ctx, `SELECT revision FROM upgrade_operations WHERE id = 1`).Scan(&preIncompleteRev); err != nil {
			_ = rawDB.Close()
			t.Fatalf("query revision: %v", err)
		}
		if _, err := rawDB.ExecContext(ctx, `DELETE FROM usage_events`); err != nil {
			_ = rawDB.Close()
			t.Fatalf("delete target usage_events: %v", err)
		}
		_ = rawDB.Close()

		restore := upgradeworker.SetReadReconcilePostRunSnapshotForTest(func(context.Context, string, string) (model.UpgradeOperation, bool, error) {
			return model.UpgradeOperation{}, false, errors.New("injected snapshot unavailable")
		})
		defer restore()

		if st := doReconcile(); st.Code != string(upgradeservice.UpgradeErrorReconcileTargetIncomplete) {
			t.Fatalf("reconcile on incomplete target = %+v, want %s", st, upgradeservice.UpgradeErrorReconcileTargetIncomplete)
		}
		days, hasGaps := worker.ReconciliationState()
		if len(days) != 0 || hasGaps {
			t.Fatalf("ReconciliationState after target incomplete = (%v, %v), want ([], false)", days, hasGaps)
		}

		rawDB2, err := sqliterepo.Open(dbPath)
		if err != nil {
			t.Fatalf("reopen target db: %v", err)
		}
		if _, err := rawDB2.ExecContext(ctx, `UPDATE upgrade_operations SET revision = ?, pending_reconcile_days = '["2026-02-15"]' WHERE id = 1`, preIncompleteRev); err != nil {
			_ = rawDB2.Close()
			t.Fatalf("downgrade revision: %v", err)
		}
		_ = rawDB2.Close()

		days, hasGaps = worker.ReconciliationState()
		if len(days) != 0 || hasGaps {
			t.Fatalf("ReconciliationState resurrected stale days after non-transient refusal: (%v, %v)", days, hasGaps)
		}
	})
}

func TestIsTransientReconcileErrorClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "wrapped_snapshot_failed",
			err:  fmt.Errorf("snapshot read: %w", upgradeservice.ErrSnapshotFailed),
			want: true,
		},
		{
			name: "sqlite_busy_text",
			err:  errors.New("database is locked (SQLITE_BUSY)"),
			want: true,
		},
		{
			name: "sqlite_locked_text",
			err:  errors.New("SQLITE_LOCKED: database table is locked"),
			want: true,
		},
		{
			name: "reconcile_target_incomplete",
			err:  upgradeservice.ErrReconcileTargetIncomplete,
			want: false,
		},
		{
			name: "reconcile_nothing_compared",
			err:  fmt.Errorf("wrapped: %w", upgradeservice.ErrReconcileNothingCompared),
			want: false,
		},
		{
			name: "non_transient_coded_error_mentioning_locked_text",
			err:  upgradeservice.CodedError(upgradeservice.UpgradeErrorReconcileTargetIncomplete, "database is locked", nil),
			want: false,
		},
		{
			name: "nil",
			err:  nil,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := upgradeworker.IsTransientReconcileError(tc.err); got != tc.want {
				t.Fatalf("IsTransientReconcileError(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}

func TestWorkerReconcileRevisionDoesNotDriftOnPreDBFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const seededEventSQL = `insert into usage_events (event_hash, timestamp_ms, timestamp, model, requested_model, created_at_ms)
		values ('legacy-event-1', 1772323200000, '2026-03-01T00:00:00Z', 'gpt-test', 'gpt-test', 1772323200000)`

	execReconcile := func(t *testing.T, worker *upgradeworker.Service) upgradeworker.Execution {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"action": "reconcile"})
		req := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/execute", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		worker.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("execute reconcile = %d: %s", rec.Code, rec.Body.String())
		}
		return waitUntilIdle(t, worker)
	}

	runOutOfBandCLIReconcile := func(t *testing.T, sourceRoot, targetRoot, bundleDir string, ackDays []string, ackParityGaps bool) error {
		t.Helper()
		sourceLock, err := processlock.AcquireExistingReadOnly(upgradeservice.SourceDatabasePath(sourceRoot))
		if err != nil {
			t.Fatalf("acquire source lock: %v", err)
		}
		defer func() { _ = sourceLock.Close() }()
		targetLock, err := processlock.Acquire(upgradeservice.TargetDatabasePath(targetRoot))
		if err != nil {
			t.Fatalf("acquire target lock: %v", err)
		}
		defer func() { _ = targetLock.Close() }()
		_, err = upgradeservice.Reconcile(ctx, upgradeservice.Options{
			LegacyRoot:            sourceRoot,
			TargetRoot:            targetRoot,
			BundleDir:             bundleDir,
			Acknowledged:          ackDays,
			AcknowledgeParityGaps: ackParityGaps,
			Lock:                  sourceLock,
			TargetLock:            targetLock,
		})
		return err
	}

	t.Run("two_pre_db_non_transient_failures_with_snapshot_failure_followed_by_healthy_snapshot", func(t *testing.T) {
		sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, true)
		dbPath := filepath.Join(targetRoot, "usage.sqlite")
		dataKeyPath := filepath.Join(targetRoot, "data.key")

		worker, err := upgradeworker.New(ctx, upgradeworker.Options{
			Config:          config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath},
			TargetRoot:      targetRoot,
			SourceRoot:      sourceRoot,
			BundleDir:       bundleDir,
			PublicOrigin:    testPublicOrigin,
			RepairOwnership: func(string) error { return nil },
		})
		if err != nil {
			t.Fatalf("upgradeworker.New: %v", err)
		}
		defer worker.Wait()

		rawDB, err := sqliterepo.Open(dbPath)
		if err != nil {
			t.Fatalf("open target db: %v", err)
		}
		var initialRev int64
		if err := rawDB.QueryRowContext(ctx, `SELECT revision FROM upgrade_operations WHERE id = 1`).Scan(&initialRev); err != nil {
			_ = rawDB.Close()
			t.Fatalf("query initial revision: %v", err)
		}
		if _, err := rawDB.ExecContext(ctx, `DELETE FROM usage_events`); err != nil {
			_ = rawDB.Close()
			t.Fatalf("delete target usage_events: %v", err)
		}
		_ = rawDB.Close()

		restore := upgradeworker.SetReadReconcilePostRunSnapshotForTest(func(context.Context, string, string) (model.UpgradeOperation, bool, error) {
			return model.UpgradeOperation{}, false, errors.New("injected snapshot unavailable")
		})
		defer restore()

		for i := range 2 {
			if st := execReconcile(t, worker); st.Code != string(upgradeservice.UpgradeErrorReconcileTargetIncomplete) {
				t.Fatalf("pre-DB failure #%d = %+v, want %s", i+1, st, upgradeservice.UpgradeErrorReconcileTargetIncomplete)
			}
		}

		// Neither pre-DB failure advanced SQLite revision, so reconcileDaysRevision must stay at initialRev.
		rawDB2, err := sqliterepo.Open(dbPath)
		if err != nil {
			t.Fatalf("reopen target db: %v", err)
		}
		var revAfterPreDB int64
		if err := rawDB2.QueryRowContext(ctx, `SELECT revision FROM upgrade_operations WHERE id = 1`).Scan(&revAfterPreDB); err != nil {
			_ = rawDB2.Close()
			t.Fatalf("query revision after pre-DB failures: %v", err)
		}
		if revAfterPreDB != initialRev {
			_ = rawDB2.Close()
			t.Fatalf("DB revision drifted on pre-DB failures: got %d, want %d", revAfterPreDB, initialRev)
		}
		if _, err := rawDB2.ExecContext(ctx, seededEventSQL); err != nil {
			_ = rawDB2.Close()
			t.Fatalf("restore target usage_events: %v", err)
		}
		_ = rawDB2.Close()

		restore()

		// Out-of-band CLI reconcile advances SQLite revision by 1 (initialRev+1) with pending_reconcile_days.
		if cliErr := runOutOfBandCLIReconcile(t, sourceRoot, targetRoot, bundleDir, nil, false); !errors.Is(cliErr, upgradeservice.ErrReconcileMismatch) {
			t.Fatalf("CLI unacknowledged reconcile err = %v, want ErrReconcileMismatch", cliErr)
		}
		days, _ := worker.ReconciliationState()
		if !reflect.DeepEqual(days, []string{"2026-02-15"}) {
			t.Fatalf("ReconciliationState after CLI mismatch = %v, want [2026-02-15] (revision must not drift ahead of DB)", days)
		}

		// Out-of-band CLI acknowledge advances SQLite revision to initialRev+2 and clears pending_reconcile_days.
		if cliErr := runOutOfBandCLIReconcile(t, sourceRoot, targetRoot, bundleDir, []string{"2026-02-15"}, true); cliErr != nil {
			t.Fatalf("CLI acknowledged reconcile err = %v", cliErr)
		}
		days, hasGaps := worker.ReconciliationState()
		if len(days) != 0 || hasGaps {
			t.Fatalf("ReconciliationState after CLI clear = (%v, %v), want ([], false)", days, hasGaps)
		}
	})

	t.Run("pre_db_failure_plus_mismatch_with_snapshot_failure_followed_by_out_of_band_cli_reconcile", func(t *testing.T) {
		sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, true)
		dbPath := filepath.Join(targetRoot, "usage.sqlite")
		dataKeyPath := filepath.Join(targetRoot, "data.key")

		worker, err := upgradeworker.New(ctx, upgradeworker.Options{
			Config:          config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath},
			TargetRoot:      targetRoot,
			SourceRoot:      sourceRoot,
			BundleDir:       bundleDir,
			PublicOrigin:    testPublicOrigin,
			RepairOwnership: func(string) error { return nil },
		})
		if err != nil {
			t.Fatalf("upgradeworker.New: %v", err)
		}
		defer worker.Wait()

		rawDB, err := sqliterepo.Open(dbPath)
		if err != nil {
			t.Fatalf("open target db: %v", err)
		}
		if _, err := rawDB.ExecContext(ctx, `DELETE FROM usage_events`); err != nil {
			_ = rawDB.Close()
			t.Fatalf("delete target usage_events: %v", err)
		}
		_ = rawDB.Close()

		restore := upgradeworker.SetReadReconcilePostRunSnapshotForTest(func(context.Context, string, string) (model.UpgradeOperation, bool, error) {
			return model.UpgradeOperation{}, false, errors.New("injected snapshot unavailable")
		})
		defer restore()

		// 1. Pre-DB failure with snapshot failure must not advance reconcileDaysRevision.
		if st := execReconcile(t, worker); st.Code != string(upgradeservice.UpgradeErrorReconcileTargetIncomplete) {
			t.Fatalf("pre-DB failure = %+v, want %s", st, upgradeservice.UpgradeErrorReconcileTargetIncomplete)
		}

		// 2. Restore target usage_events and hit ErrReconcileMismatch while post-run snapshot still fails.
		rawDB2, err := sqliterepo.Open(dbPath)
		if err != nil {
			t.Fatalf("reopen target db: %v", err)
		}
		if _, err := rawDB2.ExecContext(ctx, seededEventSQL); err != nil {
			_ = rawDB2.Close()
			t.Fatalf("restore target usage_events: %v", err)
		}
		_ = rawDB2.Close()

		if st := execReconcile(t, worker); st.Code != string(upgradeservice.UpgradeErrorReconcileMismatch) {
			t.Fatalf("mismatch reconcile = %+v, want %s", st, upgradeservice.UpgradeErrorReconcileMismatch)
		}
		restore()

		// 3. Out-of-band CLI reconcile clears pending_reconcile_days and bumps DB revision by +1.
		if cliErr := runOutOfBandCLIReconcile(t, sourceRoot, targetRoot, bundleDir, []string{"2026-02-15"}, true); cliErr != nil {
			t.Fatalf("CLI acknowledged reconcile err = %v", cliErr)
		}

		days, hasGaps := worker.ReconciliationState()
		if len(days) != 0 || hasGaps {
			t.Fatalf("ReconciliationState after out-of-band CLI clear = (%v, %v), want ([], false)", days, hasGaps)
		}
	})

	t.Run("pre_db_record_incomplete_does_not_poison_revision_across_healthy_snapshot_and_cli_clear", func(t *testing.T) {
		sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, true)
		dbPath := filepath.Join(targetRoot, "usage.sqlite")
		dataKeyPath := filepath.Join(targetRoot, "data.key")

		worker, err := upgradeworker.New(ctx, upgradeworker.Options{
			Config:          config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath},
			TargetRoot:      targetRoot,
			SourceRoot:      sourceRoot,
			BundleDir:       bundleDir,
			PublicOrigin:    testPublicOrigin,
			RepairOwnership: func(string) error { return nil },
		})
		if err != nil {
			t.Fatalf("upgradeworker.New: %v", err)
		}
		defer worker.Wait()

		if st := execReconcile(t, worker); st.Code != string(upgradeservice.UpgradeErrorReconcileMismatch) {
			t.Fatalf("initial mismatch reconcile = %+v, want %s", st, upgradeservice.UpgradeErrorReconcileMismatch)
		}

		rawDB, err := sqliterepo.Open(dbPath)
		if err != nil {
			t.Fatalf("open target db: %v", err)
		}
		var savedFingerprint string
		if err := rawDB.QueryRowContext(ctx, `SELECT source_fingerprint_after FROM upgrade_operations WHERE id = 1`).Scan(&savedFingerprint); err != nil {
			_ = rawDB.Close()
			t.Fatalf("query source_fingerprint_after: %v", err)
		}
		if _, err := rawDB.ExecContext(ctx, `UPDATE upgrade_operations SET source_fingerprint_after = '' WHERE id = 1`); err != nil {
			_ = rawDB.Close()
			t.Fatalf("clear source_fingerprint_after: %v", err)
		}
		_ = rawDB.Close()

		restore := upgradeworker.SetReadReconcilePostRunSnapshotForTest(func(context.Context, string, string) (model.UpgradeOperation, bool, error) {
			return model.UpgradeOperation{}, false, errors.New("injected snapshot unavailable")
		})
		defer restore()

		for i := range 2 {
			if st := execReconcile(t, worker); st.Code != string(upgradeservice.UpgradeErrorReconcileRecordIncomplete) {
				t.Fatalf("record-incomplete pre-DB failure #%d = %+v, want %s", i+1, st, upgradeservice.UpgradeErrorReconcileRecordIncomplete)
			}
		}

		rawDB2, err := sqliterepo.Open(dbPath)
		if err != nil {
			t.Fatalf("reopen target db: %v", err)
		}
		if _, err := rawDB2.ExecContext(ctx, `UPDATE upgrade_operations SET source_fingerprint_after = ? WHERE id = 1`, savedFingerprint); err != nil {
			_ = rawDB2.Close()
			t.Fatalf("restore source_fingerprint_after: %v", err)
		}
		_ = rawDB2.Close()
		restore()

		days, _ := worker.ReconciliationState()
		if !reflect.DeepEqual(days, []string{"2026-02-15"}) {
			t.Fatalf("ReconciliationState after healthy snapshot = %v, want [2026-02-15]", days)
		}

		if cliErr := runOutOfBandCLIReconcile(t, sourceRoot, targetRoot, bundleDir, []string{"2026-02-15"}, true); cliErr != nil {
			t.Fatalf("CLI acknowledged reconcile err = %v", cliErr)
		}
		days, hasGaps := worker.ReconciliationState()
		if len(days) != 0 || hasGaps {
			t.Fatalf("ReconciliationState after CLI clear = (%v, %v), want ([], false)", days, hasGaps)
		}
	})
}

func TestWorkerExternalConnectCanonicalizesLegacy1xStoredURLs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const v8ConfigJSON = `{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"/auth"},"access":{"api-keys":["client-key-1"]}}`
	startIPv4ExternalV8Server := func(t *testing.T) (port int) {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen 127.0.0.1:0: %v", err)
		}
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-test"}]}`))
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+testExternalMgmtKey {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			switch r.URL.Path {
			case "/v0/management/config":
				w.Header().Set("X-CPA-Version", "v8.0.55")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			case "/v8/management/config":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(v8ConfigJSON))
			case "/v8/management/config/access/api-keys":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`["client-key-1"]`))
			case "/v8/management/config.yaml":
				w.Header().Set("Content-Type", "application/yaml")
				_, _ = w.Write([]byte("port: 8317\n"))
			default:
				http.NotFound(w, r)
			}
		}))
		srv.Listener = ln
		srv.Start()
		t.Cleanup(srv.Close)
		return ln.Addr().(*net.TCPAddr).Port
	}

	ipv4Port := startIPv4ExternalV8Server(t)
	var localhostKey atomic.Value
	localhostKey.Store(testExternalMgmtKey)
	_, localhostPort := startLocalhostExternalV8Server(t, &localhostKey)

	type legacyCase struct {
		name         string
		rawURL       string
		canonicalURL string
		storage      string // "manager_config_v1" or "setup"
		prebind      bool
		wantSync     bool
	}

	cases := []legacyCase{
		{
			name:         "manager_config_v1_v0_management_initial",
			rawURL:       fmt.Sprintf("http://127.0.0.1:%d/v0/management", ipv4Port),
			canonicalURL: fmt.Sprintf("http://127.0.0.1:%d", ipv4Port),
			storage:      "manager_config_v1",
			wantSync:     true,
		},
		{
			name:         "manager_config_v1_v0_initial",
			rawURL:       fmt.Sprintf("http://127.0.0.1:%d/v0", ipv4Port),
			canonicalURL: fmt.Sprintf("http://127.0.0.1:%d", ipv4Port),
			storage:      "manager_config_v1",
			wantSync:     true,
		},
		{
			name:         "manager_config_v1_schemeless_initial",
			rawURL:       fmt.Sprintf("127.0.0.1:%d", ipv4Port),
			canonicalURL: fmt.Sprintf("http://127.0.0.1:%d", ipv4Port),
			storage:      "manager_config_v1",
			wantSync:     true,
		},
		{
			name:         "manager_config_v1_uppercase_localhost_initial",
			rawURL:       fmt.Sprintf("HTTP://LOCALHOST:%d/", localhostPort),
			canonicalURL: fmt.Sprintf("http://localhost:%d", localhostPort),
			storage:      "manager_config_v1",
			wantSync:     true,
		},
		{
			name:         "setup_only_v0_management_initial",
			rawURL:       fmt.Sprintf("http://127.0.0.1:%d/v0/management", ipv4Port),
			canonicalURL: fmt.Sprintf("http://127.0.0.1:%d", ipv4Port),
			storage:      "setup",
			wantSync:     true,
		},
		{
			name:         "setup_only_v0_initial",
			rawURL:       fmt.Sprintf("http://127.0.0.1:%d/v0", ipv4Port),
			canonicalURL: fmt.Sprintf("http://127.0.0.1:%d", ipv4Port),
			storage:      "setup",
			wantSync:     true,
		},
		{
			name:         "setup_only_schemeless_initial",
			rawURL:       fmt.Sprintf("127.0.0.1:%d", ipv4Port),
			canonicalURL: fmt.Sprintf("http://127.0.0.1:%d", ipv4Port),
			storage:      "setup",
			wantSync:     true,
		},
		{
			name:         "manager_config_v1_v0_management_with_binding",
			rawURL:       fmt.Sprintf("http://127.0.0.1:%d/v0/management", ipv4Port),
			canonicalURL: fmt.Sprintf("http://127.0.0.1:%d", ipv4Port),
			storage:      "manager_config_v1",
			prebind:      true,
			wantSync:     true,
		},
		{
			name:         "setup_only_schemeless_with_binding",
			rawURL:       fmt.Sprintf("127.0.0.1:%d", ipv4Port),
			canonicalURL: fmt.Sprintf("http://127.0.0.1:%d", ipv4Port),
			storage:      "setup",
			prebind:      true,
			wantSync:     true,
		},
		{
			name:         "manager_config_v1_canonical_with_noncanonical_setup_triggers_sync",
			rawURL:       fmt.Sprintf("http://127.0.0.1:%d", ipv4Port),
			canonicalURL: fmt.Sprintf("http://127.0.0.1:%d", ipv4Port),
			storage:      "mixed_setup_v0_management",
			wantSync:     true,
		},
		{
			name:         "manager_config_v1_already_canonical_skips_sync",
			rawURL:       fmt.Sprintf("http://127.0.0.1:%d", ipv4Port),
			canonicalURL: fmt.Sprintf("http://127.0.0.1:%d", ipv4Port),
			storage:      "manager_config_v1",
			wantSync:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, false)
			dbPath := filepath.Join(targetRoot, "usage.sqlite")
			dataKeyPath := filepath.Join(targetRoot, "data.key")

			keyBytes, err := security.LoadExistingDataKey("", dataKeyPath)
			if err != nil {
				t.Fatalf("load data key: %v", err)
			}
			protector, err := security.NewProtector(keyBytes)
			if err != nil {
				t.Fatalf("new protector: %v", err)
			}

			seedConnection := func() {
				stStore, err := store.Open(dbPath, protector)
				if err != nil {
					t.Fatalf("open store: %v", err)
				}
				switch tc.storage {
				case "manager_config_v1":
					cfg := store.ManagerConfig{
						CPAConnection: store.ManagerCPAConnectionConfig{
							CPABaseURL:    tc.rawURL,
							ManagementKey: testExternalMgmtKey,
						},
					}
					if err := stStore.SaveManagerConfig(ctx, cfg); err != nil {
						_ = stStore.Close()
						t.Fatalf("SaveManagerConfig: %v", err)
					}
					if _, err := stStore.SQLDB().ExecContext(ctx, `DELETE FROM settings WHERE key = 'setup'`); err != nil {
						_ = stStore.Close()
						t.Fatalf("delete setup: %v", err)
					}
				case "setup":
					if _, err := stStore.SQLDB().ExecContext(ctx, `DELETE FROM settings WHERE key = 'manager_config_v1'`); err != nil {
						_ = stStore.Close()
						t.Fatalf("delete manager_config_v1: %v", err)
					}
					if err := stStore.SaveSetup(ctx, store.Setup{
						CPAUpstreamURL: tc.rawURL,
						ManagementKey:  testExternalMgmtKey,
					}); err != nil {
						_ = stStore.Close()
						t.Fatalf("SaveSetup: %v", err)
					}
				case "mixed_setup_v0_management":
					if err := stStore.SaveManagerConfig(ctx, store.ManagerConfig{
						CPAConnection: store.ManagerCPAConnectionConfig{
							CPABaseURL:    tc.canonicalURL,
							ManagementKey: testExternalMgmtKey,
						},
					}); err != nil {
						_ = stStore.Close()
						t.Fatalf("SaveManagerConfig: %v", err)
					}
					if err := stStore.SaveSetup(ctx, store.Setup{
						CPAUpstreamURL: tc.canonicalURL + "/v0/management",
						ManagementKey:  testExternalMgmtKey,
						Queue:          "mixed:queue",
						PopSide:        "left",
					}); err != nil {
						_ = stStore.Close()
						t.Fatalf("SaveSetup: %v", err)
					}
				default:
					_ = stStore.Close()
					t.Fatalf("unknown storage %q", tc.storage)
				}
				if err := stStore.Close(); err != nil {
					t.Fatalf("close store: %v", err)
				}
			}

			seedConnection()

			worker, err := upgradeworker.New(ctx, upgradeworker.Options{
				Config:          config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath},
				TargetRoot:      targetRoot,
				SourceRoot:      sourceRoot,
				BundleDir:       bundleDir,
				PublicOrigin:    testPublicOrigin,
				RepairOwnership: func(string) error { return nil },
			})
			if err != nil {
				t.Fatalf("upgradeworker.New: %v", err)
			}
			defer worker.Wait()

			doExec := func(payload map[string]any) upgradeworker.Execution {
				t.Helper()
				body, _ := json.Marshal(payload)
				req := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/execute", bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
				req.Header.Set("Origin", testPublicOrigin)
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				worker.ServeHTTP(rec, req)
				if rec.Code != http.StatusAccepted {
					t.Fatalf("execute %v = %d: %s", payload, rec.Code, rec.Body.String())
				}
				return waitUntilIdle(t, worker)
			}

			if st := doExec(map[string]any{"action": "reconcile"}); st.Code != "" {
				t.Fatalf("reconcile failed: %+v", st)
			}

			if tc.prebind {
				// Pre-bind ExternalConnect directly via the service layer (simulating CLI external-connect
				// or an older worker that left the legacy non-canonical URL in settings).
				if _, err := upgradeservice.ExternalConnect(ctx, upgradeservice.ExternalConnectOptions{
					TargetRoot:    targetRoot,
					BaseURL:       tc.canonicalURL,
					ManagementKey: testExternalMgmtKey,
				}); err != nil {
					t.Fatalf("prebind upgradeservice.ExternalConnect: %v", err)
				}
				seedConnection()
			}

			resolvedBaseURL, resolvedKey, syncStore, reprove, err := worker.ExternalConnectionForTest(ctx, upgradeworker.Request{
				Action: "external-connect",
			})
			if err != nil {
				t.Fatalf("ExternalConnectionForTest: %v", err)
			}
			if resolvedBaseURL != tc.canonicalURL || resolvedKey != testExternalMgmtKey || reprove {
				t.Fatalf("ExternalConnectionForTest = (%q, %q, sync=%t, reprove=%t), want (%q, %q, sync=%t, reprove=false)",
					resolvedBaseURL, resolvedKey, syncStore, reprove, tc.canonicalURL, testExternalMgmtKey, tc.wantSync)
			}
			if syncStore != tc.wantSync {
				t.Fatalf("syncStore = %t, want %t for %q in %s (prebind=%t)", syncStore, tc.wantSync, tc.rawURL, tc.storage, tc.prebind)
			}

			if st := doExec(map[string]any{"action": "external-connect"}); st.Code != "" {
				t.Fatalf("implicit external-connect failed: %+v", st)
			}

			stStore, err := store.Open(dbPath, protector)
			if err != nil {
				t.Fatalf("reopen store: %v", err)
			}
			managerCfg, ok, err := stStore.LoadManagerConfig(ctx)
			if err != nil || !ok {
				_ = stStore.Close()
				t.Fatalf("LoadManagerConfig = (%+v, %v, %v)", managerCfg, ok, err)
			}
			if managerCfg.CPAConnection.CPABaseURL != tc.canonicalURL || managerCfg.CPAConnection.ManagementKey != testExternalMgmtKey {
				_ = stStore.Close()
				t.Fatalf("persisted CPABaseURL = %q, key = %q; want %q, %q",
					managerCfg.CPAConnection.CPABaseURL, managerCfg.CPAConnection.ManagementKey, tc.canonicalURL, testExternalMgmtKey)
			}
			if tc.storage == "mixed_setup_v0_management" {
				if managerCfg.Collector.Queue != "mixed:queue" || managerCfg.Collector.PopSide != "left" {
					_ = stStore.Close()
					t.Fatalf("Collector after mixed_setup_v0_management sync = %+v, want Queue=mixed:queue PopSide=left", managerCfg.Collector)
				}
				setupCfg, setupOK, setupErr := stStore.LoadSetup(ctx)
				if setupErr != nil || !setupOK || setupCfg.CPAUpstreamURL != tc.canonicalURL {
					_ = stStore.Close()
					t.Fatalf("Setup after mixed_setup_v0_management sync = (%+v, %v, %v), want CPAUpstreamURL=%q", setupCfg, setupOK, setupErr, tc.canonicalURL)
				}
			}
			op, opFound, err := upgradeservice.ReadRecordFrom(ctx, stStore.SQLDB())
			if err != nil || !opFound {
				_ = stStore.Close()
				t.Fatalf("ReadRecordFrom = (%+v, %v, %v)", op, opFound, err)
			}
			binding, hasBinding, err := upgradeoperation.New(stStore.SQLDB()).ExternalBinding(ctx)
			if err != nil || !hasBinding {
				_ = stStore.Close()
				t.Fatalf("ExternalBinding = (%+v, %v, %v)", binding, hasBinding, err)
			}
			fencedURL := upgradeservice.FencedExternalBaseURLForStore(ctx, stStore.SQLDB(), nil, op, binding, hasBinding)
			_ = stStore.Close()
			if fencedURL != tc.canonicalURL {
				t.Fatalf("FencedExternalBaseURLForStore(..., nil, ...) = %q, want %q", fencedURL, tc.canonicalURL)
			}
		})
	}
}

func TestWorkerExternalConnectStrictV2ExplicitURLsAndKeyRotationOnV0Management(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sourceRoot, targetRoot, bundleDir := newLegacySourceAndCopiedTarget(t, false)
	dbPath := filepath.Join(targetRoot, "usage.sqlite")
	dataKeyPath := filepath.Join(targetRoot, "data.key")
	cfg := config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath}

	keyBytes, err := security.LoadExistingDataKey("", dataKeyPath)
	if err != nil {
		t.Fatalf("load data key: %v", err)
	}
	protector, err := security.NewProtector(keyBytes)
	if err != nil {
		t.Fatalf("new protector: %v", err)
	}

	worker, err := upgradeworker.New(ctx, upgradeworker.Options{
		Config:          cfg,
		TargetRoot:      targetRoot,
		SourceRoot:      sourceRoot,
		BundleDir:       bundleDir,
		PublicOrigin:    testPublicOrigin,
		RepairOwnership: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("upgradeworker.New: %v", err)
	}
	defer worker.Wait()

	doExec := func(payload map[string]any) upgradeworker.Execution {
		t.Helper()
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, testPublicOrigin+"/.well-known/cpamp-bootstrap/api/v1/installation/upgrade/execute", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testInheritedAdminKey)
		req.Header.Set("Origin", testPublicOrigin)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		worker.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("execute %v = %d: %s", payload, rec.Code, rec.Body.String())
		}
		return waitUntilIdle(t, worker)
	}

	if st := doExec(map[string]any{"action": "reconcile"}); st.Code != "" {
		t.Fatalf("reconcile failed: %+v", st)
	}

	// 1. Explicit schemeless host URLs must be rejected by externalConnection,
	// execute, and syncExternalManagerConnection without prepending http://.
	for _, schemeless := range []string{"127.0.0.1:8317", "127.0.0.1:8317/v0/management", "localhost:8317"} {
		urlCopy := schemeless
		keyCopy := testExternalMgmtKey
		_, _, _, _, connErr := worker.ExternalConnectionForTest(ctx, upgradeworker.Request{
			Action:        "external-connect",
			BaseURL:       &urlCopy,
			ManagementKey: &keyCopy,
		})
		if code, ok := upgradeservice.UpgradeErrorCodeOf(connErr); !ok || code != upgradeservice.UpgradeErrorExternalAuthorityUnavailable {
			t.Fatalf("ExternalConnectionForTest(%q) err = (%v, code=%q/%t), want %s",
				schemeless, connErr, code, ok, upgradeservice.UpgradeErrorExternalAuthorityUnavailable)
		}

		execState := doExec(map[string]any{
			"action":        "external-connect",
			"baseUrl":       schemeless,
			"managementKey": testExternalMgmtKey,
		})
		if execState.Code != string(upgradeservice.UpgradeErrorExternalAuthorityUnavailable) {
			t.Fatalf("execute external-connect(%q) = %+v, want %s",
				schemeless, execState, upgradeservice.UpgradeErrorExternalAuthorityUnavailable)
		}

		syncErr := worker.SyncExternalManagerConnectionForTest(ctx, schemeless, testExternalMgmtKey)
		if code, ok := upgradeservice.UpgradeErrorCodeOf(syncErr); !ok || code != upgradeservice.UpgradeErrorExternalAuthorityUnavailable {
			t.Fatalf("SyncExternalManagerConnectionForTest(%q) err = (%v, code=%q/%t), want %s",
				schemeless, syncErr, code, ok, upgradeservice.UpgradeErrorExternalAuthorityUnavailable)
		}
	}

	// 2. Explicit /v0/management stays distinct from bare root URL (no stripping).
	explicitV0MgmtInput := "HTTP://127.0.0.1:8317/v0/management/"
	wantV0MgmtCanonical := "http://127.0.0.1:8317/v0/management"
	explicitKey := testExternalMgmtKey
	gotURL, gotKey, syncStore, reprove, err := worker.ExternalConnectionForTest(ctx, upgradeworker.Request{
		Action:        "external-connect",
		BaseURL:       &explicitV0MgmtInput,
		ManagementKey: &explicitKey,
	})
	if err != nil {
		t.Fatalf("ExternalConnectionForTest(/v0/management): %v", err)
	}
	if gotURL != wantV0MgmtCanonical || gotKey != testExternalMgmtKey || !syncStore || reprove {
		t.Fatalf("ExternalConnectionForTest(/v0/management) = (%q, %q, sync=%t, reprove=%t), want (%q, %q, sync=true, reprove=false)",
			gotURL, gotKey, syncStore, reprove, wantV0MgmtCanonical, testExternalMgmtKey)
	}

	// 3. With a recorded root binding (no /v0/management), explicit /v0/management
	// stays distinct: externalConnection keeps reprove=false and syncExternalManagerConnection
	// refuses the mismatched target as moved.
	sealedInitialKey, err := protector.ProtectString(testExternalMgmtKey)
	if err != nil {
		t.Fatalf("seal initial key: %v", err)
	}
	rawDB, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	if _, err := rawDB.ExecContext(ctx, `UPDATE upgrade_operations SET stage = 'external_connect', last_forward_stage = 'external_connect' WHERE id = 1`); err != nil {
		_ = rawDB.Close()
		t.Fatalf("set stage external_connect: %v", err)
	}
	if _, err := rawDB.ExecContext(ctx, `INSERT INTO upgrade_external_connect (
		id, base_url, identity, generation, server_fingerprint, cpa_version, support, client_keys, management_key, observed_at_ms
	) VALUES (1, 'http://127.0.0.1:8317', 'id-root', 'id-root', 'fp-root', 'v8.0.55', 'supported', 1, ?, 1770000000000)`, sealedInitialKey); err != nil {
		_ = rawDB.Close()
		t.Fatalf("insert root binding: %v", err)
	}
	_ = rawDB.Close()

	rotatedKey := "rotated-v0-management-mgmt-key"
	_, _, _, reproveAgainstRoot, err := worker.ExternalConnectionForTest(ctx, upgradeworker.Request{
		Action:        "external-connect",
		BaseURL:       &explicitV0MgmtInput,
		ManagementKey: &rotatedKey,
	})
	if err != nil {
		t.Fatalf("ExternalConnectionForTest(/v0/management vs root binding): %v", err)
	}
	if reproveAgainstRoot {
		t.Fatal("expected /v0/management not to match recorded root binding for reprove")
	}
	if syncErr := worker.SyncExternalManagerConnectionForTest(ctx, wantV0MgmtCanonical, testExternalMgmtKey); func() bool {
		code, ok := upgradeservice.UpgradeErrorCodeOf(syncErr)
		return !ok || code != upgradeservice.UpgradeErrorExternalTargetMoved
	}() {
		t.Fatalf("SyncExternalManagerConnectionForTest(/v0/management vs root binding) = %v, want %s",
			syncErr, upgradeservice.UpgradeErrorExternalTargetMoved)
	}
	// Even with a root binding recorded, schemeless sync must fail with authority unavailable, never normalizing to root.
	if syncErr := worker.SyncExternalManagerConnectionForTest(ctx, "127.0.0.1:8317", testExternalMgmtKey); func() bool {
		code, ok := upgradeservice.UpgradeErrorCodeOf(syncErr)
		return !ok || code != upgradeservice.UpgradeErrorExternalAuthorityUnavailable
	}() {
		t.Fatalf("SyncExternalManagerConnectionForTest(schemeless vs root binding) = %v, want %s",
			syncErr, upgradeservice.UpgradeErrorExternalAuthorityUnavailable)
	}

	// 4. Key rotation on /v0/management: when /v0/management is the recorded binding,
	// canonically equivalent /v0/management/ URL sets reprove=true on key change,
	// reprove=false on same key, and syncExternalManagerConnection persists the
	// canonical /v0/management URL while keeping root URL distinct.
	sealedRotatedKey, err := protector.ProtectString(rotatedKey)
	if err != nil {
		t.Fatalf("seal rotated key: %v", err)
	}
	rawDB2, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen sqlite db: %v", err)
	}
	if _, err := rawDB2.ExecContext(ctx, `UPDATE upgrade_external_connect SET base_url = ?, identity = 'id-v0mgmt', generation = 'id-v0mgmt', management_key = ? WHERE id = 1`,
		wantV0MgmtCanonical, sealedInitialKey); err != nil {
		_ = rawDB2.Close()
		t.Fatalf("update binding to /v0/management: %v", err)
	}
	_ = rawDB2.Close()

	sameURL, sameKeyOut, sameSync, sameReprove, err := worker.ExternalConnectionForTest(ctx, upgradeworker.Request{
		Action:        "external-connect",
		BaseURL:       &explicitV0MgmtInput,
		ManagementKey: &explicitKey,
	})
	if err != nil || sameURL != wantV0MgmtCanonical || sameKeyOut != testExternalMgmtKey || !sameSync || sameReprove {
		t.Fatalf("same key on /v0/management = (%q, %q, sync=%t, reprove=%t, err=%v), want (%q, %q, sync=true, reprove=false, nil)",
			sameURL, sameKeyOut, sameSync, sameReprove, err, wantV0MgmtCanonical, testExternalMgmtKey)
	}

	rotURL, rotKeyOut, rotSync, rotReprove, err := worker.ExternalConnectionForTest(ctx, upgradeworker.Request{
		Action:        "external-connect",
		BaseURL:       &explicitV0MgmtInput,
		ManagementKey: &rotatedKey,
	})
	if err != nil || rotURL != wantV0MgmtCanonical || rotKeyOut != rotatedKey || !rotSync || !rotReprove {
		t.Fatalf("key rotation on /v0/management = (%q, %q, sync=%t, reprove=%t, err=%v), want (%q, %q, sync=true, reprove=true, nil)",
			rotURL, rotKeyOut, rotSync, rotReprove, err, wantV0MgmtCanonical, rotatedKey)
	}

	rootInput := "http://127.0.0.1:8317"
	_, _, _, rootVsV0Reprove, err := worker.ExternalConnectionForTest(ctx, upgradeworker.Request{
		Action:        "external-connect",
		BaseURL:       &rootInput,
		ManagementKey: &rotatedKey,
	})
	if err != nil {
		t.Fatalf("ExternalConnectionForTest(root vs /v0/management binding): %v", err)
	}
	if rootVsV0Reprove {
		t.Fatal("expected bare root URL not to trigger reprove against /v0/management binding")
	}

	// Update sealed binding key as ExternalConnect(..., Reprove: true) does, then sync settings.
	rawDB3, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen sqlite db for rotated key: %v", err)
	}
	if _, err := rawDB3.ExecContext(ctx, `UPDATE upgrade_external_connect SET management_key = ? WHERE id = 1`, sealedRotatedKey); err != nil {
		_ = rawDB3.Close()
		t.Fatalf("update sealed rotated key: %v", err)
	}
	_ = rawDB3.Close()

	if err := worker.SyncExternalManagerConnectionForTest(ctx, explicitV0MgmtInput, rotatedKey); err != nil {
		t.Fatalf("SyncExternalManagerConnectionForTest(/v0/management): %v", err)
	}
	stStore, err := store.Open(dbPath, protector)
	if err != nil {
		t.Fatalf("open store after /v0/management sync: %v", err)
	}
	savedCfg, ok, err := stStore.LoadManagerConfig(ctx)
	savedSetup, setupOK, setupErr := stStore.LoadSetup(ctx)
	_ = stStore.Close()
	if err != nil || !ok || savedCfg.CPAConnection.CPABaseURL != wantV0MgmtCanonical || savedCfg.CPAConnection.ManagementKey != rotatedKey {
		t.Fatalf("LoadManagerConfig after /v0/management sync = (%+v, %v, %v), want CPABaseURL=%q key=%q",
			savedCfg.CPAConnection, ok, err, wantV0MgmtCanonical, rotatedKey)
	}
	if setupErr != nil || !setupOK || savedSetup.CPAUpstreamURL != wantV0MgmtCanonical || savedSetup.ManagementKey != rotatedKey {
		t.Fatalf("LoadSetup after /v0/management sync = (%+v, %v, %v), want CPAUpstreamURL=%q key=%q",
			savedSetup, setupOK, setupErr, wantV0MgmtCanonical, rotatedKey)
	}
}
