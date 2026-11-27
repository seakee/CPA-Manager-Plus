package worker

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	automationsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/automation"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCollectorWorkerDoesNotStartWhenMonitoringDisabled(t *testing.T) {
	cfg := workerTestConfig(t)
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	enabled := false
	err = db.SaveManagerConfig(context.Background(), store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    "http://cpa.local:8317",
			ManagementKey: "management-key",
		},
		Collector: store.ManagerCollectorConfig{
			Enabled:        &enabled,
			CollectorMode:  "http",
			Queue:          "usage",
			PopSide:        "right",
			BatchSize:      100,
			PollIntervalMS: 500,
		},
	})
	if err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	manager := collectorpkg.NewManager(cfg, db)
	collectorService := collectorservice.New(manager)
	NewCollectorWorker(cfg, db, collectorService).Start(context.Background())

	if status := collectorService.Status(); status.Collector != "stopped" {
		t.Fatalf("collector status = %#v", status)
	}
}

func TestCollectorWorkerStartsFromEnvironmentConfig(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/usage-queue" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)

	cfg := workerTestConfig(t)
	cfg.CPAUpstreamURL = upstream.URL
	cfg.ManagementKey = "management-key"
	cfg.CollectorMode = "http"
	cfg.PollInterval = time.Hour
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	manager := collectorpkg.NewManager(cfg, db)
	collectorService := collectorservice.New(manager)
	t.Cleanup(func() {
		_ = collectorService.Stop(context.Background())
	})

	NewCollectorWorker(cfg, db, collectorService).Start(context.Background())
	status := collectorService.Status()
	if status.Collector != "starting" && status.Collector != "running" {
		t.Fatalf("collector status = %#v", status)
	}
	if status.Upstream != upstream.URL || status.Mode != "http" || status.Queue != "usage" {
		t.Fatalf("collector status = %#v", status)
	}
}

func TestCollectorServiceRestartAndStop(t *testing.T) {
	cfg := workerTestConfig(t)
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	manager := collectorpkg.NewManager(cfg, db)
	collectorService := collectorservice.New(manager)
	managerCfg := store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    "http://cpa.local:8317",
			ManagementKey: "management-key",
		},
		Collector: store.ManagerCollectorConfig{
			CollectorMode:  "http",
			Queue:          "usage",
			PopSide:        "right",
			BatchSize:      100,
			PollIntervalMS: int(time.Hour / time.Millisecond),
		},
	}

	if err := collectorService.Start(context.Background(), managerCfg); err != nil {
		t.Fatalf("start collector: %v", err)
	}
	if status := collectorService.Status(); status.Collector != "starting" {
		t.Fatalf("collector status after start = %#v", status)
	}
	managerCfg.Collector.PollIntervalMS = int((2 * time.Hour) / time.Millisecond)
	if err := collectorService.Restart(context.Background(), managerCfg); err != nil {
		t.Fatalf("restart collector: %v", err)
	}
	if status := collectorService.Status(); status.Collector != "starting" {
		t.Fatalf("collector status after restart = %#v", status)
	}
	if err := collectorService.Stop(context.Background()); err != nil {
		t.Fatalf("stop collector: %v", err)
	}
	if status := collectorService.Status(); status.Collector != "stopped" {
		t.Fatalf("collector status after stop = %#v", status)
	}
}

func workerTestConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		DBPath:        filepath.Join(t.TempDir(), "usage.sqlite"),
		CollectorMode: "auto",
		Queue:         "usage",
		PopSide:       "right",
		BatchSize:     100,
		PollInterval:  time.Hour,
	}
}

type collectorRuntimeConfigCapture struct {
	updates chan collectorpkg.RuntimeConfig
}

func (c collectorRuntimeConfigCapture) HandleUsageEvents(context.Context, collectorpkg.RuntimeConfig, []usage.Event) {
}

func (c collectorRuntimeConfigCapture) UpdateRuntimeConfig(_ context.Context, cfg collectorpkg.RuntimeConfig) {
	select {
	case c.updates <- cfg:
	default:
	}
}

func TestCollectorWorkerUsesCompletedManagedConnectionMirror(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	path := filepath.Join(t.TempDir(), "manager.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fresh := freshstore.New(db)
	id := bootstrap.FreshIdentity{AdmissionID: "admission", InstallationID: "installation", Generation: "generation", OperationID: "operation", DatabasePath: path}
	admin, err := security.NewAdminCredential("Cpamp_Collector_Mirror_Admin_839216", "host")
	if err != nil {
		t.Fatal(err)
	}
	record, err := fresh.Create(ctx, id, &admin)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector([]byte("collector mirror data key"))
	if err != nil {
		t.Fatal(err)
	}
	const baseURL = "http://cpamp-runtime:8317"
	const managementKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	ciphertext, err := protector.ProtectString(managementKey)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, Stage: "committed", TargetIdentity: "runtime", TargetGeneration: 7, TargetArtifactID: "sha256:" + strings.Repeat("a", 64), DestinationConnectionIdentity: freshstrategy.ManagedDestinationIdentity(baseURL)}
	connection := &model.ManagerCPAConnectionConfig{CPABaseURL: baseURL, ManagementKey: ciphertext}
	pending, err := fresh.Update(ctx, id, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = checkpoint
		r.Connection = connection
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fresh.Update(ctx, id, pending.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing.OperationID = id.OperationID
		r.Operation.Routing.Revision = 1
		r.Operation.Routing.Target = "/console"
		r.Operation.Routing.InferenceTarget = "/access-api"
		r.Operation.Routing.Stage = bootstrap.PrefixCommitted
		r.Operation.Routing.Committed = true
		r.Operation.Routing.InferenceCommitted = true
		r.Operation.ProductComplete = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	managerStore := store.New(db, protector)
	managerCfg, ok, err := managerStore.LoadManagerConfig(ctx)
	if err != nil || !ok {
		t.Fatalf("manager config mirror unavailable: ok=%v err=%v", ok, err)
	}
	if managerCfg.CPAConnection.CPABaseURL != baseURL || managerCfg.CPAConnection.ManagementKey != managementKey {
		t.Fatalf("manager config mirror = %#v", managerCfg.CPAConnection)
	}
	enabled := true
	managerCfg.Collector.Enabled = &enabled
	if err := managerStore.SaveManagerConfigAndSetup(ctx, managerCfg, model.Setup{CPAUpstreamURL: baseURL, ManagementKey: managementKey}); err != nil {
		t.Fatal(err)
	}
	base := config.Config{CollectorMode: "poll", Queue: "queue", PopSide: "left", PollInterval: time.Second, BatchSize: 1}
	manager := collectorpkg.NewManager(base, managerStore)
	capture := collectorRuntimeConfigCapture{updates: make(chan collectorpkg.RuntimeConfig, 1)}
	manager.SetUsageEventHandler(capture)
	worker := NewCollectorWorker(base, managerStore, collectorservice.New(manager))
	worker.Start(ctx)
	select {
	case got := <-capture.updates:
		if got.CPAUpstreamURL != baseURL || got.ManagementKey != managementKey {
			t.Fatalf("collector received base=%q key=%q", got.CPAUpstreamURL, got.ManagementKey)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("collector did not receive the completed managed connection")
	}
	worker.Stop(context.Background())
}

func TestCollectorWorkerDoesNotStartSetupFallbackWhenManagerConfigDisablesCollector(t *testing.T) {
	cfg := workerTestConfig(t)
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	enabled := false
	if err := db.SaveManagerConfigAndSetup(
		context.Background(),
		store.ManagerConfig{
			Collector: store.ManagerCollectorConfig{Enabled: &enabled},
		},
		model.Setup{
			CPAUpstreamURL: "http://cpa.local:8317",
			ManagementKey:  "management-key",
		},
	); err != nil {
		t.Fatalf("save disabled manager config and setup: %v", err)
	}

	manager := collectorpkg.NewManager(cfg, db)
	collectorService := collectorservice.New(manager)
	NewCollectorWorker(cfg, db, collectorService).Start(context.Background())

	if status := collectorService.Status(); status.Collector != "stopped" {
		t.Fatalf("collector status = %#v, want stopped", status)
	}
}

func TestCollectorWorkerGatesUpgradeExternalStoreOnPersistentRejectionAndTargetFence(t *testing.T) {
	var outboundRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outboundRequests.Add(1)
		if r.URL.Path == "/v0/management/usage-queue" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	keyBytes, _, err := security.LoadOrCreateDataKey("", dataKeyPath)
	if err != nil {
		t.Fatalf("LoadOrCreateDataKey: %v", err)
	}
	protector, err := security.NewProtector(keyBytes)
	if err != nil {
		t.Fatalf("NewProtector: %v", err)
	}
	db, err := store.Open(dbPath, protector)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	canonicalURL, err := identity.CanonicalExternalURL(upstream.URL)
	if err != nil {
		t.Fatalf("CanonicalExternalURL: %v", err)
	}
	const mgmtKey = "upgrade-external-worker-mgmt-key"
	enabled := true
	if err := db.SaveManagerConfigAndSetup(
		ctx,
		store.ManagerConfig{
			CPAConnection: store.ManagerCPAConnectionConfig{CPABaseURL: canonicalURL, ManagementKey: mgmtKey},
			Collector:     store.ManagerCollectorConfig{Enabled: &enabled, CollectorMode: "http", PollIntervalMS: 20},
		},
		model.Setup{CPAUpstreamURL: canonicalURL, ManagementKey: mgmtKey},
	); err != nil {
		t.Fatalf("SaveManagerConfigAndSetup: %v", err)
	}

	repo := upgradeoperation.New(db.SQLDB())
	fpr := "sha256:" + strings.Repeat("a", 64)
	manifest := "sha256:" + strings.Repeat("b", 64)
	if err := repo.Put(ctx, model.UpgradeOperation{
		Stage:                   model.UpgradeStageExternalConnect,
		LastForwardStage:        model.UpgradeStageExternalConnect,
		DomainsDone:             []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainIdentity, model.UpgradeDomainCPAConfig},
		SourceKind:              legacypreflight.SourceKindUpgradedLegacy,
		SourceVersion:           "v1.8.0",
		SourceRoot:              filepath.Join(dir, "legacy"),
		SourceFingerprintBefore: fpr,
		SourceFingerprintAfter:  fpr,
		BackupManifestSHA256:    manifest,
		Reconciliation:          json.RawMessage(`{"ok":true}`),
		ControlBasePath:         "/upg-console",
		RoutingCommitted:        true,
		CreatedAtMS:             1000,
		UpdatedAtMS:             1000,
	}); err != nil {
		t.Fatalf("seed upgrade op: %v", err)
	}
	sealedKey, err := protector.ProtectString(mgmtKey)
	if err != nil {
		t.Fatalf("ProtectString: %v", err)
	}
	extID := freshkeyapply.ExternalIdentity(canonicalURL)
	tx, err := db.SQLDB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := upgradeoperation.RecordExternalBindingTx(ctx, tx, upgradeoperation.ExternalBinding{
		BaseURL:       canonicalURL,
		Identity:      extID,
		Generation:    extID,
		ManagementKey: sealedKey,
		ObservedAtMS:  2000,
	}); err != nil {
		_ = tx.Rollback()
		t.Fatalf("RecordExternalBindingTx: %v", err)
	}
	if _, err := upgradeoperation.RecordCompletionTx(ctx, tx, model.UpgradeVerification{
		TargetMode:          model.UpgradeTargetExternal,
		SourceFingerprint:   fpr,
		Domains:             []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainIdentity, model.UpgradeDomainCPAConfig},
		BindingIdentity:     extID,
		BindingGeneration:   extID,
		BindingObservedAtMS: 2000,
		ObservedAtMS:        2001,
	}); err != nil {
		_ = tx.Rollback()
		t.Fatalf("RecordCompletionTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Persist a refusal for (extID, credDigest) and verify CollectorWorker.Start stays stopped with 0 requests.
	credDigest := workerEnvelopeDigest(sealedKey)
	refusalJSON, _ := json.Marshal(map[string]any{
		"identity":   extID,
		"credential": credDigest,
		"atMs":       time.Now().UnixMilli(),
	})
	if _, err := db.SQLDB().ExecContext(ctx,
		`insert into settings (key, value, updated_at_ms) values (?, ?, ?)`,
		workerExternalRejectionKey, string(refusalJSON), time.Now().UnixMilli(),
	); err != nil {
		t.Fatalf("insert refusal row: %v", err)
	}

	cfg := config.Config{
		DBPath:        dbPath,
		DataKeyPath:   dataKeyPath,
		CollectorMode: "http",
		PollInterval:  20 * time.Millisecond,
	}
	manager := collectorpkg.NewManager(cfg, db)
	collectorSvc := collectorservice.New(manager)
	t.Cleanup(func() { _ = manager.StopAndWait(context.Background()) })
	cw := NewCollectorWorker(cfg, db, collectorSvc)
	cw.Start(ctx)
	time.Sleep(60 * time.Millisecond)
	if status := collectorSvc.Status(); status.Collector != "stopped" {
		t.Fatalf("collector status with persisted refusal = %#v, want stopped", status)
	}
	if got := outboundRequests.Load(); got != 0 {
		t.Fatalf("outbound CPA requests with persisted refusal = %d, want 0", got)
	}

	// Clear the refusal row; CollectorWorker.Start now starts polling.
	if _, err := db.SQLDB().ExecContext(ctx, `delete from settings where key = ?`, workerExternalRejectionKey); err != nil {
		t.Fatalf("delete refusal row: %v", err)
	}
	cw.Start(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for collectorSvc.Status().Collector != "running" {
		if time.Now().After(deadline) {
			t.Fatalf("collector did not reach running after clearing refusal: %#v", collectorSvc.Status())
		}
		time.Sleep(15 * time.Millisecond)
	}
	if outboundRequests.Load() == 0 {
		t.Fatal("expected outbound CPA queue requests after clearing refusal")
	}
}

func TestCollectorWorkerPreservesTLSSkipVerifyAndSetupOnlyPatchedOverridesAcrossRestart(t *testing.T) {
	var (
		pollCount atomic.Int32
		lastCmd   atomic.Value
		lastQueue atomic.Value
		lastCount atomic.Value
	)
	certHelper := httptest.NewTLSServer(nil)
	tlsCerts := certHelper.TLS.Certificates
	certHelper.Close()

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: tlsCerts})
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	readRespArgs := func(r *bufio.Reader) ([]string, error) {
		header, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		header = strings.TrimSuffix(strings.TrimSuffix(header, "\n"), "\r")
		if !strings.HasPrefix(header, "*") {
			return nil, fmt.Errorf("unexpected RESP header %q", header)
		}
		n, err := strconv.Atoi(header[1:])
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, n)
		for range n {
			szLine, err := r.ReadString('\n')
			if err != nil {
				return nil, err
			}
			szLine = strings.TrimSuffix(strings.TrimSuffix(szLine, "\n"), "\r")
			sz, err := strconv.Atoi(strings.TrimPrefix(szLine, "$"))
			if err != nil {
				return nil, err
			}
			buf := make([]byte, sz+2)
			if _, err := io.ReadFull(r, buf); err != nil {
				return nil, err
			}
			out = append(out, string(buf[:sz]))
		}
		return out, nil
	}

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				tlsConn := c.(*tls.Conn)
				_ = tlsConn.SetDeadline(time.Now().Add(5 * time.Second))
				if hsErr := tlsConn.Handshake(); hsErr != nil {
					return
				}
				_ = tlsConn.SetDeadline(time.Time{})
				r := bufio.NewReader(tlsConn)
				for {
					args, readErr := readRespArgs(r)
					if readErr != nil || len(args) == 0 {
						return
					}
					switch strings.ToUpper(args[0]) {
					case "AUTH":
						_, _ = io.WriteString(tlsConn, "+OK\r\n")
					case "LPOP", "RPOP":
						lastCmd.Store(strings.ToUpper(args[0]))
						if len(args) > 1 {
							lastQueue.Store(args[1])
						}
						if len(args) > 2 {
							lastCount.Store(args[2])
						}
						pollCount.Add(1)
						_, _ = io.WriteString(tlsConn, "*-1\r\n")
					default:
						_, _ = io.WriteString(tlsConn, "-ERR unsupported\r\n")
					}
				}
			}(conn)
		}
	}()

	cfg := workerTestConfig(t)
	cfg.CollectorMode = "auto"
	cfg.Queue = "env_default_queue"
	cfg.PopSide = "right"
	cfg.BatchSize = 100
	cfg.PollInterval = time.Hour
	cfg.TLSSkipVerify = true

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	upstreamURL := "https://" + ln.Addr().String()
	// Seed a legacy setup-only DB (no manager_config_v1 row initially).
	if err := db.SaveSetup(t.Context(), store.Setup{
		CPAUpstreamURL: upstreamURL,
		ManagementKey:  "setup-secret",
		Queue:          "legacy_setup_queue",
		PopSide:        "right",
	}); err != nil {
		t.Fatalf("SaveSetup: %v", err)
	}

	// Apply PatchCollector on the setup-only DB without touching TLSSkipVerify.
	cfgSvc := managerconfig.New(cfg, db, nil)
	cfgSvc.UseExternalCollectorLifecycle()
	cfgSvc.SetCPAUsageProbeAllowed(func(context.Context) bool { return false })

	mode := "resp"
	queue := "patched_setup_queue"
	side := "left"
	batch := 37
	pollMS := 25
	queryLimit := 4321
	if _, err := cfgSvc.PatchCollector(t.Context(), store.ManagerCollectorPatch{
		CollectorMode:  &mode,
		Queue:          &queue,
		PopSide:        &side,
		BatchSize:      &batch,
		PollIntervalMS: &pollMS,
		QueryLimit:     &queryLimit,
	}); err != nil {
		t.Fatalf("PatchCollector on setup-only DB: %v", err)
	}

	// Simulate process restart: CollectorWorker.Start must merge persisted collector
	// overrides over setup AND preserve process cfg.TLSSkipVerify=true.
	mgr := collectorpkg.NewManager(cfg, db)
	collectorSvc := collectorservice.New(mgr)
	t.Cleanup(func() { _ = mgr.StopAndWait(context.Background()) })
	cw := NewCollectorWorker(cfg, db, collectorSvc)
	cw.Start(t.Context())

	deadline := time.Now().Add(3 * time.Second)
	for pollCount.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("collector did not poll self-signed TLS RESP upstream (status=%#v)", collectorSvc.Status())
		}
		time.Sleep(15 * time.Millisecond)
	}
	if status := collectorSvc.Status(); status.Mode != "resp" || status.Queue != queue {
		t.Fatalf("collector status = %#v, want mode=resp queue=%q", status, queue)
	}
	if gotCmd, _ := lastCmd.Load().(string); gotCmd != "LPOP" {
		t.Fatalf("polled cmd = %q, want LPOP", gotCmd)
	}
	if gotQ, _ := lastQueue.Load().(string); gotQ != queue {
		t.Fatalf("polled queue = %q, want %q", gotQ, queue)
	}
	if gotCount, _ := lastCount.Load().(string); gotCount != "37" {
		t.Fatalf("polled batch count = %q, want 37", gotCount)
	}
	_ = mgr.StopAndWait(context.Background())

	// Explicit TLSSkipVerify=false persisted via PatchCollector must override cfg.TLSSkipVerify=true.
	explicitFalse := false
	if _, err := cfgSvc.PatchCollector(t.Context(), store.ManagerCollectorPatch{
		TLSSkipVerify: &explicitFalse,
	}); err != nil {
		t.Fatalf("PatchCollector(TLSSkipVerify=false): %v", err)
	}
	pollCount.Store(0)
	mgrStrict := collectorpkg.NewManager(cfg, db)
	collectorSvcStrict := collectorservice.New(mgrStrict)
	t.Cleanup(func() { _ = mgrStrict.StopAndWait(context.Background()) })
	NewCollectorWorker(cfg, db, collectorSvcStrict).Start(t.Context())

	errDeadline := time.Now().Add(3 * time.Second)
	for collectorSvcStrict.Status().LastError == "" {
		if time.Now().After(errDeadline) {
			t.Fatalf("expected TLS verification failure when TLSSkipVerify=false explicitly persisted; status=%#v", collectorSvcStrict.Status())
		}
		time.Sleep(15 * time.Millisecond)
	}
	if got := pollCount.Load(); got != 0 {
		t.Fatalf("strict TLS collector made %d authenticated polls to self-signed upstream, want 0", got)
	}
}

func TestBackgroundWorkersWaitDrainsCleanlyOnCancel(t *testing.T) {
	cfg := workerTestConfig(t)
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	accountRollup := NewAccountHistoryRollupWorker(db)
	pricingRollup := NewUsagePricingRollupWorker(db)
	hourlyAggregate := NewUsageHourlyAggregateWorker(db)
	dashboardRollup := NewDashboardHourlyRollupWorker(db)
	cacheMigration := NewUsageCacheAccountingMigrationWorker(db, nil)
	quotaMigration := NewLegacyQuotaSnapshotMigrationWorker(db)
	rateLimit := NewRateLimitAutoDisableWorker(db)
	accountAction := NewAccountActionCandidateWorker(db, false)
	automation := NewAutomationRuntime(automationsvc.New(config.Config{}, db), rateLimit, accountAction)

	accountRollup.Start(ctx)
	pricingRollup.Start(ctx)
	hourlyAggregate.Start(ctx)
	dashboardRollup.Start(ctx)
	cacheMigration.Start(ctx)
	quotaMigration.Start(ctx)
	automation.Start(ctx)

	cancel()

	accountRollup.Wait()
	pricingRollup.Wait()
	hourlyAggregate.Wait()
	dashboardRollup.Wait()
	cacheMigration.Wait()
	quotaMigration.Wait()
	automation.Wait()
}

func TestCollectorWorkerStartCompleteManagerConfigMergesLegacySetup(t *testing.T) {
	var (
		pollCount atomic.Int32
		lastCmd   atomic.Value
		lastQueue atomic.Value
	)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, rErr := r.ReadString('\n')
					if rErr != nil {
						return
					}
					if !strings.HasPrefix(line, "*") {
						continue
					}
					n, _ := strconv.Atoi(strings.TrimSpace(line[1:]))
					args := make([]string, 0, n)
					for range n {
						szLine, err := r.ReadString('\n')
						if err != nil {
							return
						}
						sz, _ := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(szLine), "$"))
						buf := make([]byte, sz+2)
						if _, err := io.ReadFull(r, buf); err != nil {
							return
						}
						args = append(args, string(buf[:sz]))
					}
					if len(args) == 0 {
						continue
					}
					cmd := strings.ToUpper(args[0])
					if cmd == "AUTH" {
						_, _ = io.WriteString(c, "+OK\r\n")
					} else if cmd == "LPOP" || cmd == "RPOP" {
						lastCmd.Store(cmd)
						if len(args) > 1 {
							lastQueue.Store(args[1])
						}
						pollCount.Add(1)
						_, _ = io.WriteString(c, "*-1\r\n")
					} else {
						_, _ = io.WriteString(c, "-ERR unsupported\r\n")
					}
				}
			}(conn)
		}
	}()

	cfg := workerTestConfig(t)
	cfg.CollectorMode = "resp"
	cfg.PollInterval = time.Hour

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	upstreamURL := "http://" + ln.Addr().String()
	if err := db.SaveSetup(t.Context(), store.Setup{
		CPAUpstreamURL: upstreamURL,
		ManagementKey:  "setup-secret",
		Queue:          "merged_setup_queue",
		PopSide:        "left",
	}); err != nil {
		t.Fatalf("SaveSetup: %v", err)
	}

	enabled := true
	if err := db.SaveManagerConfig(t.Context(), store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    upstreamURL,
			ManagementKey: "setup-secret",
		},
		Collector: store.ManagerCollectorConfig{
			Enabled:        &enabled,
			CollectorMode:  "resp",
			BatchSize:      10,
			PollIntervalMS: 20,
			// Queue and PopSide intentionally empty so CollectorWorker must merge Setup values.
		},
	}); err != nil {
		t.Fatalf("SaveManagerConfig: %v", err)
	}

	mgr := collectorpkg.NewManager(cfg, db)
	collectorSvc := collectorservice.New(mgr)
	t.Cleanup(func() { _ = mgr.StopAndWait(context.Background()) })

	NewCollectorWorker(cfg, db, collectorSvc).Start(t.Context())

	deadline := time.Now().Add(3 * time.Second)
	for pollCount.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("collector did not poll merged queue (status=%#v)", collectorSvc.Status())
		}
		time.Sleep(15 * time.Millisecond)
	}
	if gotQ, _ := lastQueue.Load().(string); gotQ != "merged_setup_queue" {
		t.Fatalf("polled queue = %q, want merged_setup_queue", gotQ)
	}
	if gotCmd, _ := lastCmd.Load().(string); gotCmd != "LPOP" {
		t.Fatalf("polled cmd = %q, want LPOP", gotCmd)
	}
}

func TestCollectorWorkerStartCompleteManagerConfigPreservesBaseFallbackWhenSetupBlank(t *testing.T) {
	cfg := workerTestConfig(t)
	cfg.Queue = "env_fallback_queue"
	cfg.PopSide = "left"
	cfg.PollInterval = time.Second
	cfg.BatchSize = 10

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	upstreamURL := "http://127.0.0.1:18317"
	if err := db.SaveSetup(t.Context(), store.Setup{
		CPAUpstreamURL: upstreamURL,
		ManagementKey:  "setup-secret",
		Queue:          "   ",
		PopSide:        "invalid",
	}); err != nil {
		t.Fatalf("SaveSetup: %v", err)
	}
	enabled := true
	if err := db.SaveManagerConfig(t.Context(), store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    upstreamURL,
			ManagementKey: "setup-secret",
		},
		Collector: store.ManagerCollectorConfig{
			Enabled:       &enabled,
			CollectorMode: "http",
		},
	}); err != nil {
		t.Fatalf("SaveManagerConfig: %v", err)
	}

	mgr := collectorpkg.NewManager(cfg, db)
	collectorSvc := collectorservice.New(mgr)
	t.Cleanup(func() { _ = mgr.StopAndWait(context.Background()) })

	NewCollectorWorker(cfg, db, collectorSvc).Start(t.Context())
	active, ok := collectorSvc.ActiveRuntimeConfig()
	if !ok {
		t.Fatal("expected collector runtime to be active")
	}
	if active.Queue != "env_fallback_queue" || active.PopSide != "left" {
		t.Fatalf("active runtime = (queue=%q, popSide=%q), want (env_fallback_queue, left)", active.Queue, active.PopSide)
	}
}

func TestCollectorWorkerStartSerializesWithConcurrentStopAndDisable(t *testing.T) {
	t.Run("concurrent_stop_during_start_never_leaves_collector_running", func(t *testing.T) {
		cpaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"usage":{}}`))
		}))
		defer cpaSrv.Close()

		for iter := range 8 {
			cfg := workerTestConfig(t)
			cfg.PollInterval = 25 * time.Millisecond
			db, err := store.Open(cfg.DBPath)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			enabled := true
			if err := db.SaveManagerConfig(t.Context(), store.ManagerConfig{
				CPAConnection: store.ManagerCPAConnectionConfig{
					CPABaseURL:    cpaSrv.URL,
					ManagementKey: "secret",
				},
				Collector: store.ManagerCollectorConfig{
					Enabled:        &enabled,
					CollectorMode:  "http",
					PollIntervalMS: 25,
				},
			}); err != nil {
				_ = db.Close()
				t.Fatalf("SaveManagerConfig: %v", err)
			}

			mgr := collectorpkg.NewManager(cfg, db)
			collectorSvc := collectorservice.New(mgr)
			worker := NewCollectorWorker(cfg, db, collectorSvc)

			enteredGate := make(chan struct{})
			releaseGate := make(chan struct{})
			worker.SetRuntimeGate(func(ctx context.Context, _ collectorpkg.RuntimeConfig) bool {
				close(enteredGate)
				<-releaseGate
				return true
			})

			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				worker.Start(context.Background())
			}()
			<-enteredGate
			go func() {
				defer wg.Done()
				worker.Stop(context.Background())
			}()
			// Give Stop a moment to mark stopped and wait on lifecycle lock before releasing the gate.
			time.Sleep(5 * time.Millisecond)
			close(releaseGate)
			wg.Wait()

			if status := collectorSvc.Status().Collector; status != "stopped" {
				_ = mgr.StopAndWait(context.Background())
				_ = db.Close()
				t.Fatalf("iter %d: collector status after concurrent Start+Stop = %q, want stopped", iter, status)
			}
			if mgr.StartCount() != 0 {
				_ = mgr.StopAndWait(context.Background())
				_ = db.Close()
				t.Fatalf("iter %d: StartCount after Stop arrived during Start = %d, want 0", iter, mgr.StartCount())
			}
			_ = db.Close()
		}
	})

	t.Run("shared_lifecycle_locker_prevents_stale_start_after_disable", func(t *testing.T) {
		cpaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"usage":{}}`))
		}))
		defer cpaSrv.Close()

		cfg := workerTestConfig(t)
		cfg.PollInterval = 25 * time.Millisecond
		db, err := store.Open(cfg.DBPath)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })

		enabled := true
		if err := db.SaveManagerConfig(t.Context(), store.ManagerConfig{
			CPAConnection: store.ManagerCPAConnectionConfig{
				CPABaseURL:    cpaSrv.URL,
				ManagementKey: "secret",
			},
			Collector: store.ManagerCollectorConfig{
				Enabled:        &enabled,
				CollectorMode:  "http",
				PollIntervalMS: 25,
			},
		}); err != nil {
			t.Fatalf("SaveManagerConfig: %v", err)
		}

		mgr := collectorpkg.NewManager(cfg, db)
		collectorSvc := collectorservice.New(mgr)
		t.Cleanup(func() { _ = mgr.StopAndWait(context.Background()) })

		var sharedMu sync.Mutex
		worker := NewCollectorWorker(cfg, db, collectorSvc)
		worker.SetLifecycleLocker(&sharedMu)

		// Hold sharedMu while Start is launched, flip enabled=false + Stop under sharedMu, then unlock.
		sharedMu.Lock()
		startDone := make(chan struct{})
		go func() {
			defer close(startDone)
			worker.Start(context.Background())
		}()
		time.Sleep(10 * time.Millisecond)
		if _, err := db.PatchManagerCollectorEnabled(context.Background(), false); err != nil {
			sharedMu.Unlock()
			t.Fatalf("PatchManagerCollectorEnabled(false): %v", err)
		}
		_ = collectorSvc.Stop(context.Background())
		sharedMu.Unlock()

		<-startDone
		if mgr.StartCount() != 0 || collectorSvc.Status().Collector != "stopped" {
			t.Fatalf("collector started from stale enabled snapshot: starts=%d status=%+v", mgr.StartCount(), collectorSvc.Status())
		}
	})
}

func TestParseWorkerManagedDestinationAcceptsRetiredAdoptionArtifactAndRejectsUnqualified(t *testing.T) {
	protector, err := security.NewProtector([]byte("worker-managed-dest-test-key-32b"))
	if err != nil {
		t.Fatalf("NewProtector: %v", err)
	}
	sealed, err := protector.ProtectString("valid-v8-mgmt-secret-key")
	if err != nil {
		t.Fatalf("ProtectString: %v", err)
	}
	sourceIdentity := "external:sha256:" + strings.Repeat("1", 64)
	serverFingerprint := "external:sha256:" + strings.Repeat("2", 64)
	opID := upgradeManagedDestinationOperationIDForWorkerTest(sourceIdentity)

	buildDoc := func(artifact model.RuntimeArtifactID) string {
		raw, err := json.Marshal(map[string]any{
			"baseUrl":           "http://127.0.0.1:8317",
			"urlProved":         true,
			"serverFingerprint": serverFingerprint,
			"managementKey":     sealed,
			"intent": map[string]any{
				"sourceIdentity":            sourceIdentity,
				"sourceClass":               "legacy_managed",
				"expectedRuntimeIdentity":   "rt-worker-1",
				"expectedRuntimeGeneration": 1,
				"expectedActiveArtifactId":  string(artifact),
				"apiKeys":                   []string{"sk-client-1"},
				"pluginsEnabled":            false,
			},
			"operationId": opID,
			"attempt":     1,
		})
		if err != nil {
			t.Fatalf("marshal doc: %v", err)
		}
		return string(raw)
	}

	unqualified := model.RuntimeArtifactID("sha256:" + strings.Repeat("9", 64))
	if _, _, _, _, ok := parseWorkerManagedDestination(buildDoc(unqualified), protector); ok {
		t.Fatal("parseWorkerManagedDestination accepted unqualified artifact, want false")
	}

	retired := model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64))
	freshstrategy.WithRetiredUpgradeAdoptionArtifactsForTesting([]model.RuntimeArtifactID{retired}, func() {
		baseURL, _, secret, _, ok := parseWorkerManagedDestination(buildDoc(retired), protector)
		if !ok || baseURL != "http://127.0.0.1:8317" || secret != "valid-v8-mgmt-secret-key" {
			t.Fatalf("parseWorkerManagedDestination(retired) = (%q, %q, %v), want valid destination", baseURL, secret, ok)
		}
	})
}

func upgradeManagedDestinationOperationIDForWorkerTest(sourceIdentity string) string {
	return workerManagedDestinationOperationID(sourceIdentity, 1)
}
