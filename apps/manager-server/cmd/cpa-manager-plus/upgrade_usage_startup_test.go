package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	usageadapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identityusage"
	identityprojectionapp "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityprojection"
	usageapp "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	settingrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/worker"
)

const upgradeUsageStartupTestAdminKey = "cpamp_Startup_Migration_Test_7391_Strong_Key"

type mockUpgradeUsageCPA struct {
	mu           sync.Mutex
	management   string
	usageEnabled bool
	failQueue    bool
	rejectAuth   bool
	failConfig   bool
	keys         []string
	queue        []string
	mgmtRequests atomic.Int32
	server       *httptest.Server
}

func newMockUpgradeUsageCPA(t *testing.T, managementKey string, initialKeys ...string) *mockUpgradeUsageCPA {
	t.Helper()
	m := &mockUpgradeUsageCPA{
		management:   managementKey,
		usageEnabled: true,
		keys:         slices.Clone(initialKeys),
	}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			m.mu.Lock()
			accepted := token != "" && r.Header.Get("Authorization") == "Bearer "+token && slices.Contains(m.keys, token)
			m.mu.Unlock()
			if !accepted {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
			return
		}

		if strings.HasPrefix(r.URL.Path, "/v8/") || strings.HasPrefix(r.URL.Path, "/v0/management/") {
			m.mgmtRequests.Add(1)
			m.mu.Lock()
			expectedAuth := "Bearer " + m.management
			reject := m.rejectAuth
			m.mu.Unlock()
			if reject || r.Header.Get("Authorization") != expectedAuth {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid management key"}`))
				return
			}
		}

		w.Header().Set("X-CPA-Version", "v8.0.13")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/config":
			m.mu.Lock()
			failCfg := m.failConfig
			enabled := m.usageEnabled
			m.mu.Unlock()
			if failCfg {
				http.Error(w, "config failed", http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"usage-statistics-enabled":            enabled,
				"redis-usage-queue-retention-seconds": 60,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/usage-statistics-enabled":
			m.mu.Lock()
			enabled := m.usageEnabled
			m.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"usage-statistics-enabled": enabled})
		case r.Method == http.MethodPut && r.URL.Path == "/v0/management/usage-statistics-enabled":
			var body struct {
				Value *bool `json:"value"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Value == nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			m.mu.Lock()
			m.usageEnabled = *body.Value
			m.mu.Unlock()
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/usage-queue":
			m.mu.Lock()
			failQ := m.failQueue
			if failQ {
				m.mu.Unlock()
				http.Error(w, "queue unavailable", http.StatusInternalServerError)
				return
			}
			items := slices.Clone(m.queue)
			m.queue = nil
			m.mu.Unlock()
			if len(items) == 0 {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			raw := make([]json.RawMessage, 0, len(items))
			for _, item := range items {
				raw = append(raw, json.RawMessage(item))
			}
			_ = json.NewEncoder(w).Encode(raw)
		case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config":
			_, _ = w.Write(m.v8Document())
		case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config.yaml":
			var doc map[string]any
			if err := json.Unmarshal(m.v8Document(), &doc); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			encoded, err := yaml.Marshal(doc)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(encoded)
		case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config/access/api-keys":
			m.mu.Lock()
			keys := slices.Clone(m.keys)
			m.mu.Unlock()
			if keys == nil {
				keys = []string{}
			}
			_ = json.NewEncoder(w).Encode(keys)
		case r.Method == http.MethodPut && r.URL.Path == "/v8/management/config/access/api-keys":
			var keys []string
			if json.NewDecoder(r.Body).Decode(&keys) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			m.mu.Lock()
			m.keys = slices.Clone(keys)
			m.mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(m.server.Close)
	return m
}

func (m *mockUpgradeUsageCPA) v8Document() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]any, 0, len(m.keys))
	for _, key := range m.keys {
		keys = append(keys, key)
	}
	doc := map[string]any{
		"config-version": 8,
		"server":         map[string]any{"host": "0.0.0.0", "port": 8317},
		"management": map[string]any{
			"allow-remote":              true,
			"secret-key":                "$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY12",
			"disable-control-panel":     true,
			"disable-auto-update-panel": true,
		},
		"access":        map[string]any{"api-keys": keys},
		"oauth":         map[string]any{"auth-dir": "/runtime/supervisor/destination/auth"},
		"plugins":       map[string]any{"enabled": false, "dir": "/runtime/supervisor/plugins"},
		"observability": map[string]any{"logs": map[string]any{"logging-to-file": false}, "usage": map[string]any{"usage-statistics-enabled": m.usageEnabled}},
	}
	body, _ := json.Marshal(doc)
	return body
}

func (m *mockUpgradeUsageCPA) enqueueEvent(requestID, clientSecret string, timestampMS int64, inputTokens, outputTokens int64) {
	payload, _ := json.Marshal(map[string]any{
		"request_id":    requestID,
		"timestamp":     strconv.FormatInt(timestampMS, 10),
		"model":         "gpt-4",
		"api_key":       clientSecret,
		"input_tokens":  inputTokens,
		"output_tokens": outputTokens,
		"total_tokens":  inputTokens + outputTokens,
	})
	m.mu.Lock()
	m.queue = append(m.queue, string(payload))
	m.mu.Unlock()
}

type mockUpgradeRuntimeSupervisor struct {
	mu       sync.Mutex
	token    string
	identity string
	gen      uint64
	state    string
	artifact string
	server   *httptest.Server
}

func newMockUpgradeRuntimeSupervisor(t *testing.T, token, runtimeIdentity string, gen uint64, state string) *mockUpgradeRuntimeSupervisor {
	t.Helper()
	artifact, err := freshstrategy.QualifiedUpgradeAdoptionArtifactID()
	if err != nil {
		t.Fatalf("QualifiedUpgradeAdoptionArtifactID: %v", err)
	}
	s := &mockUpgradeRuntimeSupervisor{
		token:    token,
		identity: runtimeIdentity,
		gen:      gen,
		state:    state,
		artifact: string(artifact),
	}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		expected := "Bearer " + s.token
		identityVal := s.identity
		genVal := s.gen
		stateVal := s.state
		artifactVal := s.artifact
		s.mu.Unlock()
		if r.Header.Get("Authorization") != expected {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1/runtime/status" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"protocolVersion":    "v1",
			"runtimeIdentity":    identityVal,
			"runtimeGeneration":  genVal,
			"state":              stateVal,
			"cpaObservedVersion": "v8.0.13",
			"activeGatewayArtifact": map[string]any{
				"engine":     "cpa",
				"artifactId": artifactVal,
				"version":    "v8.0.13",
			},
			"capabilities": []string{"active_gateway_artifact"},
		})
	}))
	t.Cleanup(s.server.Close)
	return s
}

func upgradeStartupProcessEnv(dataDir, dbPath string, extra map[string]string) []string {
	base := managerServerTestEnvironment(dataDir, dbPath)
	merged := make(map[string]string, len(extra)+4)
	for k, v := range extra {
		merged[k] = v
	}
	if _, ok := merged["USAGE_POLL_INTERVAL_MS"]; !ok {
		merged["USAGE_POLL_INTERVAL_MS"] = "50"
	}
	if _, ok := merged["CPAMP_RUNTIME_JOURNAL_PATH"]; !ok {
		merged["CPAMP_RUNTIME_JOURNAL_PATH"] = "/runtime/supervisor/operations.sqlite"
	}
	if _, ok := merged["CPAMP_RUNTIME_DATA_DIR"]; !ok {
		merged["CPAMP_RUNTIME_DATA_DIR"] = ""
	}
	out := make([]string, 0, len(base)+len(merged))
	for _, entry := range base {
		k, _, _ := strings.Cut(entry, "=")
		if _, override := merged[k]; override {
			continue
		}
		out = append(out, entry)
	}
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	return out
}

func seedUpgradeUsageStartupStore(
	t *testing.T,
	targetMode string,
	cpaURL string,
	mgmtSecret string,
	collectorEnabled *bool,
	runtimeIdentity string,
	runtimeGeneration uint64,
	controlBasePath string,
) (dataDir string, dbPath string, dataKeyPath string, protector *security.Protector, adminKey string) {
	t.Helper()
	dataDir = t.TempDir()
	dbPath = filepath.Join(dataDir, "usage.sqlite")
	dataKeyPath = filepath.Join(dataDir, "data.key")
	keyBytes, _, err := security.LoadOrCreateDataKey("", dataKeyPath)
	if err != nil {
		t.Fatalf("LoadOrCreateDataKey: %v", err)
	}
	protector, err = security.NewProtector(keyBytes)
	if err != nil {
		t.Fatalf("NewProtector: %v", err)
	}
	sqlDB, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("sqliterepo.Open: %v", err)
	}
	if err := sqliterepo.Migrate(sqlDB); err != nil {
		_ = sqlDB.Close()
		t.Fatalf("sqliterepo.Migrate: %v", err)
	}
	st := store.New(sqlDB, protector)
	ctx := context.Background()

	adminKey = upgradeUsageStartupTestAdminKey
	cred, err := security.NewAdminCredential(adminKey, "upgrade")
	if err != nil {
		_ = sqlDB.Close()
		t.Fatalf("NewAdminCredential: %v", err)
	}
	if err := settingrepo.New(sqlDB).SaveAdminCredential(ctx, cred); err != nil {
		_ = sqlDB.Close()
		t.Fatalf("SaveAdminCredential: %v", err)
	}

	canonicalURL, err := identity.CanonicalExternalURL(cpaURL)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatalf("CanonicalExternalURL(%q): %v", cpaURL, err)
	}
	if err := st.SaveManagerConfigAndSetup(
		ctx,
		model.ManagerConfig{
			CPAConnection: model.ManagerCPAConnectionConfig{
				CPABaseURL:    canonicalURL,
				ManagementKey: mgmtSecret,
			},
			Collector: model.ManagerCollectorConfig{
				Enabled:        collectorEnabled,
				CollectorMode:  "http",
				PollIntervalMS: 50,
			},
		},
		model.Setup{
			CPAUpstreamURL: canonicalURL,
			ManagementKey:  mgmtSecret,
		},
	); err != nil {
		_ = sqlDB.Close()
		t.Fatalf("SaveManagerConfigAndSetup: %v", err)
	}

	repo := upgradeoperation.New(sqlDB)
	fpr := "sha256:" + strings.Repeat("a", 64)
	manifest := "sha256:" + strings.Repeat("b", 64)
	if targetMode == model.UpgradeTargetManaged {
		if err := repo.Put(ctx, model.UpgradeOperation{
			Stage:                   model.UpgradeStageCPAImport,
			LastForwardStage:        model.UpgradeStageCPAImport,
			DomainsDone:             []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig},
			SourceKind:              legacypreflight.SourceKindUpgradedLegacy,
			SourceVersion:           "v1.8.0",
			SourceRoot:              filepath.Join(dataDir, "legacy"),
			SourceFingerprintBefore: fpr,
			SourceFingerprintAfter:  fpr,
			BackupManifestSHA256:    manifest,
			Reconciliation:          json.RawMessage(`{"ok":true}`),
			ControlBasePath:         controlBasePath,
			RoutingCommitted:        true,
			CreatedAtMS:             1000,
			UpdatedAtMS:             1000,
		}); err != nil {
			_ = sqlDB.Close()
			t.Fatalf("seed managed upgrade operation: %v", err)
		}
		sealedMgmt, err := protector.ProtectString(mgmtSecret)
		if err != nil {
			_ = sqlDB.Close()
			t.Fatalf("ProtectString: %v", err)
		}
		qualifiedArtifact, err := freshstrategy.QualifiedUpgradeAdoptionArtifactID()
		if err != nil {
			_ = sqlDB.Close()
			t.Fatalf("QualifiedUpgradeAdoptionArtifactID: %v", err)
		}
		sourceIdentity := "external:sha256:" + strings.Repeat("c", 64)
		opSum := sha256.Sum256([]byte(sourceIdentity))
		destPayload, err := json.Marshal(map[string]any{
			"baseUrl":           canonicalURL,
			"urlProved":         true,
			"serverFingerprint": freshkeyapply.ExternalServerFingerprint("v8.0.13", strings.Repeat("d", 64)),
			"managementKey":     sealedMgmt,
			"intent": map[string]any{
				"sourceIdentity":            sourceIdentity,
				"sourceClass":               "legacy_managed",
				"expectedRuntimeIdentity":   runtimeIdentity,
				"expectedRuntimeGeneration": runtimeGeneration,
				"expectedActiveArtifactId":  string(qualifiedArtifact),
				"apiKeys":                   []string{"legacy-migrated-client-key-1"},
				"pluginsEnabled":            true,
			},
			"operationId": "upgrade-cpa-import:" + hex.EncodeToString(opSum[:])[:32],
			"attempt":     1,
			"updatedAtMs": 1,
		})
		if err != nil {
			_ = sqlDB.Close()
			t.Fatalf("marshal managed destination: %v", err)
		}
		for _, key := range []string{upgradeImportDestinationSettingKey, upgradeDestinationSettingKey} {
			if _, err := sqlDB.ExecContext(ctx, `insert into settings (key, value, updated_at_ms) values (?, ?, 1)`, key, string(destPayload)); err != nil {
				_ = sqlDB.Close()
				t.Fatalf("insert setting %s: %v", key, err)
			}
		}
		tx, err := sqlDB.BeginTx(ctx, nil)
		if err != nil {
			_ = sqlDB.Close()
			t.Fatalf("BeginTx: %v", err)
		}
		if _, err := upgradeoperation.RecordCompletionTx(ctx, tx, model.UpgradeVerification{
			TargetMode:        model.UpgradeTargetManaged,
			SourceFingerprint: fpr,
			Domains:           []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig},
			ObservedAtMS:      2000,
		}); err != nil {
			_ = tx.Rollback()
			_ = sqlDB.Close()
			t.Fatalf("RecordCompletionTx managed: %v", err)
		}
		if err := tx.Commit(); err != nil {
			_ = sqlDB.Close()
			t.Fatalf("Commit managed: %v", err)
		}
	} else {
		if err := repo.Put(ctx, model.UpgradeOperation{
			Stage:                   model.UpgradeStageExternalConnect,
			LastForwardStage:        model.UpgradeStageExternalConnect,
			DomainsDone:             []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainIdentity, model.UpgradeDomainCPAConfig},
			SourceKind:              legacypreflight.SourceKindUpgradedLegacy,
			SourceVersion:           "v1.8.0",
			SourceRoot:              filepath.Join(dataDir, "legacy"),
			SourceFingerprintBefore: fpr,
			SourceFingerprintAfter:  fpr,
			BackupManifestSHA256:    manifest,
			Reconciliation:          json.RawMessage(`{"ok":true}`),
			ControlBasePath:         controlBasePath,
			RoutingCommitted:        true,
			CreatedAtMS:             1000,
			UpdatedAtMS:             1000,
		}); err != nil {
			_ = sqlDB.Close()
			t.Fatalf("seed external upgrade operation: %v", err)
		}
		sealedMgmt, err := protector.ProtectString(mgmtSecret)
		if err != nil {
			_ = sqlDB.Close()
			t.Fatalf("ProtectString external: %v", err)
		}
		extIdentity := freshkeyapply.ExternalIdentity(canonicalURL)
		tx, err := sqlDB.BeginTx(ctx, nil)
		if err != nil {
			_ = sqlDB.Close()
			t.Fatalf("BeginTx external: %v", err)
		}
		if err := upgradeoperation.RecordExternalBindingTx(ctx, tx, upgradeoperation.ExternalBinding{
			BaseURL:       canonicalURL,
			Identity:      extIdentity,
			Generation:    extIdentity,
			ManagementKey: sealedMgmt,
			ObservedAtMS:  2000,
		}); err != nil {
			_ = tx.Rollback()
			_ = sqlDB.Close()
			t.Fatalf("RecordExternalBindingTx: %v", err)
		}
		if _, err := upgradeoperation.RecordCompletionTx(ctx, tx, model.UpgradeVerification{
			TargetMode:          model.UpgradeTargetExternal,
			SourceFingerprint:   fpr,
			Domains:             []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainIdentity, model.UpgradeDomainCPAConfig},
			BindingIdentity:     extIdentity,
			BindingGeneration:   extIdentity,
			BindingObservedAtMS: 2000,
			ObservedAtMS:        2001,
		}); err != nil {
			_ = tx.Rollback()
			_ = sqlDB.Close()
			t.Fatalf("RecordCompletionTx external: %v", err)
		}
		if err := tx.Commit(); err != nil {
			_ = sqlDB.Close()
			t.Fatalf("Commit external: %v", err)
		}
	}

	_, _ = sqlDB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close seeded db: %v", err)
	}
	return dataDir, dbPath, dataKeyPath, protector, adminKey
}

func upgradeHTTPRequestJSON(t *testing.T, method, url, bearer, body string, out any) int {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("NewRequest %s %s: %v", method, url, err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, url, err)
	}
	if out != nil && res.StatusCode >= 200 && res.StatusCode < 300 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode %s %s (%s): %v", method, url, string(raw), err)
		}
	}
	return res.StatusCode
}

func waitForUpgradeServerCollectorRunning(t *testing.T, addr, controlBasePath, adminKey string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	statusURL := "http://" + addr + controlBasePath + "/api/v2/cpamp/status"
	var lastStatus int
	var lastBody string
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, statusURL, nil)
		if err != nil {
			t.Fatalf("NewRequest status: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+adminKey)
		res, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
		if err == nil {
			raw, _ := io.ReadAll(res.Body)
			_ = res.Body.Close()
			lastStatus = res.StatusCode
			lastBody = string(raw)
			if res.StatusCode == http.StatusOK {
				var parsed struct {
					Collector struct {
						Collector string `json:"collector"`
					} `json:"collector"`
				}
				if json.Unmarshal(raw, &parsed) == nil && parsed.Collector.Collector == "running" {
					return
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("collector did not reach running state via %s (last=%d %s)", statusURL, lastStatus, lastBody)
}

func readCollectionStartedAtFromPath(t *testing.T, dbPath string) int64 {
	t.Helper()
	db := openMigrationObserver(t, dbPath)
	defer func() { _ = db.Close() }()
	startedAt, err := usageadapter.New(db).CollectionStartedAtMS(context.Background())
	if err != nil {
		t.Fatalf("CollectionStartedAtMS: %v", err)
	}
	return startedAt
}

func assertCreatedUpgradeKeyCollectsUsageViaHTTP(
	t *testing.T,
	cpa *mockUpgradeUsageCPA,
	addr string,
	controlBasePath string,
	adminKey string,
	requestID string,
	eventID string,
) {
	t.Helper()
	var created struct {
		Key struct {
			ID           string `json:"id"`
			RuntimeState string `json:"runtimeState"`
		} `json:"key"`
		CreatedSecret string `json:"createdSecret"`
	}
	createURL := "http://" + addr + controlBasePath + "/api/v2/api-keys"
	if code := upgradeHTTPRequestJSON(t, http.MethodPost, createURL, adminKey, `{"requestId":"`+requestID+`"}`, &created); code != http.StatusCreated {
		t.Fatalf("POST %s = %d", createURL, code)
	}
	if created.Key.ID == "" || created.CreatedSecret == "" || created.Key.RuntimeState != "effective_observed" {
		t.Fatalf("unexpected created key response: %+v", created)
	}

	eventTimeMS := time.Now().UnixMilli()
	cpa.enqueueEvent(eventID, created.CreatedSecret, eventTimeMS, 15, 25)

	summaryURL := "http://" + addr + controlBasePath + "/api/v2/api-keys/usage-summary"
	deadline := time.Now().Add(15 * time.Second)
	var summary usageapp.SummaryResponse
	for time.Now().Before(deadline) {
		for time.Now().UnixMilli() <= eventTimeMS {
			time.Sleep(time.Millisecond)
		}
		if code := upgradeHTTPRequestJSON(t, http.MethodGet, summaryURL, adminKey, "", &summary); code == http.StatusOK {
			for _, item := range summary.Items {
				if item.KeyID == created.Key.ID && item.Calls == 1 {
					if summary.Coverage.State == "unknown" || item.Coverage.State == "unknown" || item.LastUsedAtMS == nil {
						t.Fatalf("usage summary remained unknown after collection start: summary=%+v item=%+v", summary.Coverage, item)
					}
					detailURL := "http://" + addr + controlBasePath + "/api/v2/api-keys/" + created.Key.ID + "/usage"
					var detail usageapp.DetailResponse
					if dCode := upgradeHTTPRequestJSON(t, http.MethodGet, detailURL, adminKey, "", &detail); dCode != http.StatusOK {
						t.Fatalf("GET %s = %d", detailURL, dCode)
					}
					if detail.Coverage.State == "unknown" || detail.Coverage.CollectingSinceMS <= 0 {
						t.Fatalf("detail coverage unexpected: %+v", detail.Coverage)
					}
					if detail.Totals == nil || detail.Totals.Calls != 1 || detail.Totals.TotalTokens != 40 || len(detail.Series) == 0 {
						t.Fatalf("detail totals/series unexpected: %+v", detail)
					}
					return
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("usage summary did not attribute collected event for %s: last=%+v", created.Key.ID, summary)
}

func TestUpgradeManagedStartupProvesCollectionStartAndServesUsageWithoutManualSeeding(t *testing.T) {
	const (
		mgmtSecret      = "managed-upgrade-startup-mgmt-secret-0123456789abcdef0123456789ab"
		runtimeToken    = "runtime-supervisor-startup-token"
		runtimeIdentity = "runtime-managed-upgrade-1"
		controlPath     = "/upg-console"
	)
	cpa := newMockUpgradeUsageCPA(t, mgmtSecret, "legacy-migrated-client-key-1")
	rt := newMockUpgradeRuntimeSupervisor(t, runtimeToken, runtimeIdentity, 4, string(model.RuntimeStateReady))
	enabled := true
	dataDir, dbPath, _, _, adminKey := seedUpgradeUsageStartupStore(
		t,
		model.UpgradeTargetManaged,
		cpa.server.URL,
		mgmtSecret,
		&enabled,
		runtimeIdentity,
		4,
		controlPath,
	)
	tokenFile := filepath.Join(dataDir, "runtime.token")
	if err := os.WriteFile(tokenFile, []byte(runtimeToken), 0o600); err != nil {
		t.Fatalf("write runtime token: %v", err)
	}

	env := upgradeStartupProcessEnv(dataDir, dbPath, map[string]string{
		"CPAMP_RUNTIME_URL":        rt.server.URL,
		"CPAMP_RUNTIME_TOKEN_FILE": tokenFile,
	})
	proc := startManagerServerProcessWithEnvironment(t, env)
	waitForUpgradeServerCollectorRunning(t, proc.addr, controlPath, adminKey)

	firstStartedAt := readCollectionStartedAtFromPath(t, dbPath)
	if firstStartedAt <= 0 {
		proc.stop(t)
		t.Fatalf("fresh_usage_collection_started_at_ms = %d, want > 0 after collector running", firstStartedAt)
	}

	assertCreatedUpgradeKeyCollectsUsageViaHTTP(
		t, cpa, proc.addr, controlPath, adminKey,
		strings.Repeat("a", 32), "upg-managed-startup-ev-1",
	)
	proc.stop(t)

	// Restarting on the same upgraded store must keep the earliest collection start instant.
	time.Sleep(5 * time.Millisecond)
	proc2 := startManagerServerProcessWithEnvironment(t, env)
	defer proc2.stop(t)
	waitForUpgradeServerCollectorRunning(t, proc2.addr, controlPath, adminKey)
	if secondStartedAt := readCollectionStartedAtFromPath(t, dbPath); secondStartedAt != firstStartedAt {
		t.Fatalf("collection start changed across restart: got %d, want %d", secondStartedAt, firstStartedAt)
	}
}

func TestUpgradeExternalStartupProvesCollectionStartAndServesUsageWithoutManualSeeding(t *testing.T) {
	const (
		mgmtSecret  = "external-upgrade-startup-mgmt-secret"
		controlPath = "/upg-console"
	)
	cpa := newMockUpgradeUsageCPA(t, mgmtSecret, "user-external-preexisting-key")
	enabled := true
	dataDir, dbPath, _, _, adminKey := seedUpgradeUsageStartupStore(
		t,
		model.UpgradeTargetExternal,
		cpa.server.URL,
		mgmtSecret,
		&enabled,
		"",
		0,
		controlPath,
	)

	env := upgradeStartupProcessEnv(dataDir, dbPath, nil)
	proc := startManagerServerProcessWithEnvironment(t, env)
	defer proc.stop(t)

	waitForUpgradeServerCollectorRunning(t, proc.addr, controlPath, adminKey)
	if startedAt := readCollectionStartedAtFromPath(t, dbPath); startedAt <= 0 {
		t.Fatalf("fresh_usage_collection_started_at_ms = %d, want > 0 after external collector running", startedAt)
	}

	assertCreatedUpgradeKeyCollectsUsageViaHTTP(
		t, cpa, proc.addr, controlPath, adminKey,
		strings.Repeat("b", 32), "upg-external-startup-ev-1",
	)
}

func TestUpgradeStartupDisabledAtBootThenEnabledLaterRecordsCollectionStart(t *testing.T) {
	const (
		mgmtSecret      = "managed-disabled-then-enabled-secret-0123456789abcdef0123456789a"
		runtimeToken    = "runtime-token-disabled-then-enabled"
		runtimeIdentity = "runtime-managed-upgrade-toggle"
		controlPath     = "/upg-console"
	)
	cpa := newMockUpgradeUsageCPA(t, mgmtSecret, "legacy-migrated-client-key-1")
	rt := newMockUpgradeRuntimeSupervisor(t, runtimeToken, runtimeIdentity, 4, string(model.RuntimeStateReady))
	disabled := false
	_, dbPath, dataKeyPath, _, adminKey := seedUpgradeUsageStartupStore(
		t,
		model.UpgradeTargetManaged,
		cpa.server.URL,
		mgmtSecret,
		&disabled,
		runtimeIdentity,
		4,
		controlPath,
	)

	keyBytes, _, err := security.LoadOrCreateDataKey("", dataKeyPath)
	if err != nil {
		t.Fatalf("LoadOrCreateDataKey: %v", err)
	}
	protector, err := security.NewProtector(keyBytes)
	if err != nil {
		t.Fatalf("NewProtector: %v", err)
	}
	st, err := store.Open(dbPath, protector)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// Seed a canonical identity + binding + an unattributed historical event to confirm
	// usage reads stay unknown (collection_unproven) while collector is disabled.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const preKeyID = "99887766554433221100ffeeddccbb11"
	const preSecret = "legacy-migrated-client-key-1"
	preSum := sha256.Sum256([]byte(preSecret))
	preHash := hex.EncodeToString(preSum[:])
	createdAtMS := time.Now().Add(-time.Hour).UnixMilli()
	if _, err := st.SQLDB().ExecContext(ctx,
		`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms) values (?, 1, 'active', ?, ?)`,
		preKeyID, createdAtMS, createdAtMS); err != nil {
		t.Fatalf("seed key identity: %v", err)
	}
	if _, err := st.SQLDB().ExecContext(ctx,
		`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, observed_runtime_generation, first_seen_at_ms, last_seen_at_ms, retired_at_ms) values (?, ?, ?, 4, ?, ?, null)`,
		preKeyID, runtimeIdentity, preHash, createdAtMS, createdAtMS); err != nil {
		t.Fatalf("seed key binding: %v", err)
	}

	cfg := config.Config{
		DBPath:        dbPath,
		CollectorMode: "http",
		PollInterval:  25 * time.Millisecond,
	}
	manager := collector.NewManager(cfg, st)
	collectorSvc := collectorservice.New(manager)
	collectorWorker := worker.NewCollectorWorker(cfg, st, collectorSvc)
	serverApp := httpapi.New(cfg, st, manager)
	projectionWorker := identityprojectionapp.NewWorker(st.IdentityProjections)
	serverApp.AppContext().UsageService.SetEventsInsertedNotifier(projectionWorker.Wake)
	manager.SetUsageEventHandler(collector.UsageEventHandlerFunc(func(ctx context.Context, _ collector.RuntimeConfig, events []usage.Event) {
		projectionWorker.HandleUsageEvents(ctx, events)
	}))

	rtClient := runtimeservice.NewEmbeddedClient(rt.server.URL, runtimeToken)
	usageSvc := usageapp.New(usageadapter.New(st.SQLDB()), func() usageapp.CollectionPhase {
		return collectionPhaseFor(manager.Status().Collector)
	})
	upgradeKeysHandler, closeUpgradeKeys, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
		DB:             st.SQLDB(),
		Protector:      protector,
		Runtime:        rtClient,
		Usage:          usageSvc,
		WakeProjection: projectionWorker.Wake,
	})
	if err != nil {
		t.Fatalf("newUpgradeAPIKeysHandler: %v", err)
	}
	t.Cleanup(closeUpgradeKeys)
	t.Cleanup(func() {
		stopCtx, cancelStop := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelStop()
		_ = manager.StopAndWait(stopCtx)
	})
	attachUpgradeUsageStartupObserver(manager, upgradeUsageStartupConfig{
		DB:                   st.SQLDB(),
		Store:                st,
		Protector:            protector,
		Runtime:              rtClient,
		ManagerConfigService: serverApp.AppContext().ManagerConfigService,
		Usage:                usageSvc,
		Rejection:            upgradeKeysHandler.rejection,
		CollectorWorker:      collectorWorker,
	})

	projectionWorker.Start(ctx)
	collectorWorker.Start(ctx)

	if status := manager.Status().Collector; status != "stopped" {
		t.Fatalf("collector status when disabled = %q, want stopped", status)
	}
	if startedAt, err := usageadapter.New(st.SQLDB()).CollectionStartedAtMS(ctx); err != nil || startedAt != 0 {
		t.Fatalf("CollectionStartedAtMS when disabled = %d err=%v, want 0", startedAt, err)
	}

	callKeys := func(method, resource string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, controlPath+"/api/v2/"+resource, nil)
		rec := httptest.NewRecorder()
		upgradeKeysHandler.serveHTTP(rec, req, resource)
		return rec
	}

	disabledSummaryRes := callKeys(http.MethodGet, "api-keys/usage-summary")
	if disabledSummaryRes.Code != http.StatusOK {
		t.Fatalf("disabled summary status = %d %s", disabledSummaryRes.Code, disabledSummaryRes.Body.String())
	}
	var disabledSummary usageapp.SummaryResponse
	if err := json.Unmarshal(disabledSummaryRes.Body.Bytes(), &disabledSummary); err != nil {
		t.Fatal(err)
	}
	if disabledSummary.Coverage.State != "unknown" || disabledSummary.Coverage.Reason != "collection_unproven" {
		t.Fatalf("disabled summary coverage = %+v, want unknown/collection_unproven", disabledSummary.Coverage)
	}

	disabledDetailRes := callKeys(http.MethodGet, "api-keys/"+preKeyID+"/usage")
	if disabledDetailRes.Code != http.StatusOK {
		t.Fatalf("disabled detail status = %d %s", disabledDetailRes.Code, disabledDetailRes.Body.String())
	}
	var disabledDetail usageapp.DetailResponse
	if err := json.Unmarshal(disabledDetailRes.Body.Bytes(), &disabledDetail); err != nil {
		t.Fatal(err)
	}
	if disabledDetail.Coverage.State != "unknown" || disabledDetail.Coverage.Reason != "collection_unproven" || disabledDetail.Totals != nil {
		t.Fatalf("disabled detail = %+v, want unknown/collection_unproven and nil totals", disabledDetail)
	}

	// Now enable collection via the existing ManagerConfig HTTP PATCH callback.
	patchReq := httptest.NewRequest(http.MethodPatch, "/usage-service/config", bytes.NewBufferString(`{"enabled":true}`))
	patchReq.Header.Set("Authorization", "Bearer "+adminKey)
	patchReq.Header.Set("Content-Type", "application/json")
	patchRec := httptest.NewRecorder()
	serverApp.Handler().ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("PATCH usage-service/config = %d %s", patchRec.Code, patchRec.Body.String())
	}

	deadline := time.Now().Add(5 * time.Second)
	for manager.Status().Collector != "running" {
		if time.Now().After(deadline) {
			t.Fatalf("collector did not become running after PATCH enable (status=%+v)", manager.Status())
		}
		time.Sleep(15 * time.Millisecond)
	}
	startedAt, err := usageadapter.New(st.SQLDB()).CollectionStartedAtMS(ctx)
	if err != nil || startedAt <= 0 {
		t.Fatalf("CollectionStartedAtMS after enable = %d err=%v, want > 0", startedAt, err)
	}

	eventTimeMS := time.Now().UnixMilli()
	cpa.enqueueEvent("upg-enabled-later-ev-1", preSecret, eventTimeMS, 15, 25)
	for time.Now().Before(deadline) {
		for time.Now().UnixMilli() <= eventTimeMS {
			time.Sleep(time.Millisecond)
		}
		res := callKeys(http.MethodGet, "api-keys/usage-summary")
		var summary usageapp.SummaryResponse
		if res.Code == http.StatusOK && json.Unmarshal(res.Body.Bytes(), &summary) == nil && len(summary.Items) > 0 && summary.Items[0].Calls == 1 {
			if summary.Coverage.State == "unknown" || summary.Items[0].Coverage.State == "unknown" {
				t.Fatalf("summary stayed unknown after enablement: %+v", summary)
			}
			dRes := callKeys(http.MethodGet, "api-keys/"+preKeyID+"/usage")
			var detail usageapp.DetailResponse
			if dRes.Code != http.StatusOK || json.Unmarshal(dRes.Body.Bytes(), &detail) != nil {
				t.Fatalf("detail after enablement = %d %s", dRes.Code, dRes.Body.String())
			}
			if detail.Coverage.State == "unknown" || detail.Totals == nil || detail.Totals.Calls != 1 || detail.Totals.TotalTokens != 40 {
				t.Fatalf("detail after enablement unexpected: %+v", detail)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("usage summary did not attribute collected event after enabling collector")
}

func TestUpgradeStartupPreservesFailedDisabledForeignAndMovedAsUnproven(t *testing.T) {
	type setupHooks struct {
		mode            string
		collectorOn     bool
		cpaFailQueue    bool
		cpaRejectAuth   bool
		cpaUsageOff     bool
		runtimeState    string
		runtimeIdentity string
		foreignEnvURL   string
		foreignCfgURL   string
		moveExternal    bool
		incompleteStage bool
		importDestOnly  bool
		conflictingDest bool
	}

	runCase := func(t *testing.T, hooks setupHooks, postCheck func(t *testing.T, cpa *mockUpgradeUsageCPA, st *store.Store, manager *collector.Manager, usageSvc *usageapp.Service)) {
		t.Helper()
		const (
			mgmtSecret      = "case-upgrade-mgmt-secret"
			runtimeToken    = "case-runtime-token"
			expectedRuntime = "runtime-expected-1"
		)
		cpa := newMockUpgradeUsageCPA(t, mgmtSecret, "client-key-1")
		cpa.mu.Lock()
		cpa.failQueue = hooks.cpaFailQueue
		cpa.rejectAuth = hooks.cpaRejectAuth
		cpa.usageEnabled = !hooks.cpaUsageOff
		cpa.mu.Unlock()

		rtIdentity := expectedRuntime
		if hooks.runtimeIdentity != "" {
			rtIdentity = hooks.runtimeIdentity
		}
		rtState := string(model.RuntimeStateReady)
		if hooks.runtimeState != "" {
			rtState = hooks.runtimeState
		}
		rt := newMockUpgradeRuntimeSupervisor(t, runtimeToken, rtIdentity, 4, rtState)

		enabled := hooks.collectorOn
		_, dbPath, dataKeyPath, _, _ := seedUpgradeUsageStartupStore(
			t,
			hooks.mode,
			cpa.server.URL,
			mgmtSecret,
			&enabled,
			expectedRuntime,
			4,
			"/upg-console",
		)
		keyBytes, _, err := security.LoadOrCreateDataKey("", dataKeyPath)
		if err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector(keyBytes)
		if err != nil {
			t.Fatal(err)
		}
		st, err := store.Open(dbPath, protector)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		ctx := context.Background()

		if hooks.importDestOnly {
			if _, err := st.SQLDB().ExecContext(ctx, `delete from settings where key = ?`, upgradeDestinationSettingKey); err != nil {
				t.Fatal(err)
			}
		}
		if hooks.conflictingDest {
			otherCPA := newMockUpgradeUsageCPA(t, mgmtSecret, "client-key-1")
			sealed, err := protector.ProtectString(mgmtSecret)
			if err != nil {
				t.Fatal(err)
			}
			badPayload, _ := json.Marshal(map[string]any{
				"baseUrl":           otherCPA.server.URL,
				"urlProved":         true,
				"managementKey":     sealed,
				"serverFingerprint": freshkeyapply.ExternalServerFingerprint("v8.0.13", strings.Repeat("f", 64)),
			})
			if _, err := st.SQLDB().ExecContext(ctx, `update settings set value = ? where key = ?`, string(badPayload), upgradeDestinationSettingKey); err != nil {
				t.Fatal(err)
			}
		}
		if hooks.foreignCfgURL != "" {
			if err := st.SaveManagerConfigAndSetup(ctx, model.ManagerConfig{
				CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: hooks.foreignCfgURL, ManagementKey: mgmtSecret},
				Collector:     model.ManagerCollectorConfig{Enabled: &enabled, CollectorMode: "http", PollIntervalMS: 25},
			}, model.Setup{CPAUpstreamURL: hooks.foreignCfgURL, ManagementKey: mgmtSecret}); err != nil {
				t.Fatal(err)
			}
		}
		if hooks.moveExternal {
			if _, err := st.SQLDB().ExecContext(ctx, `update upgrade_external_connect set base_url = ? where id = 1`, "http://127.0.0.1:65432"); err != nil {
				t.Fatal(err)
			}
		}
		if hooks.incompleteStage {
			if _, err := st.SQLDB().ExecContext(ctx, `update upgrade_operations set stage = 'cpa_import' where id = 1`); err != nil {
				t.Fatal(err)
			}
		}

		cfg := config.Config{
			DBPath:         dbPath,
			CollectorMode:  "http",
			PollInterval:   25 * time.Millisecond,
			CPAUpstreamURL: hooks.foreignEnvURL,
		}
		if hooks.foreignEnvURL != "" {
			cfg.ManagementKey = mgmtSecret
		}

		manager := collector.NewManager(cfg, st)
		collectorSvc := collectorservice.New(manager)
		collectorWorker := worker.NewCollectorWorker(cfg, st, collectorSvc)
		serverApp := httpapi.New(cfg, st, manager)
		rtClient := runtimeservice.NewEmbeddedClient(rt.server.URL, runtimeToken)
		usageSvc := usageapp.New(usageadapter.New(st.SQLDB()), func() usageapp.CollectionPhase {
			return collectionPhaseFor(manager.Status().Collector)
		})
		handler, closeHandler, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
			DB:        st.SQLDB(),
			Protector: protector,
			Runtime:   rtClient,
			Usage:     usageSvc,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeHandler)
		t.Cleanup(func() {
			stopCtx, cancelStop := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancelStop()
			_ = manager.StopAndWait(stopCtx)
		})

		// Handler construction alone must never record collection start.
		if startedAt, err := usageadapter.New(st.SQLDB()).CollectionStartedAtMS(ctx); err != nil || startedAt != 0 {
			t.Fatalf("handler construction marked collection start = %d err=%v", startedAt, err)
		}

		attachUpgradeUsageStartupObserver(manager, upgradeUsageStartupConfig{
			DB:                   st.SQLDB(),
			Store:                st,
			Protector:            protector,
			Runtime:              rtClient,
			ManagerConfigService: serverApp.AppContext().ManagerConfigService,
			Usage:                usageSvc,
			Rejection:            handler.rejection,
			CollectorWorker:      collectorWorker,
		})

		runCtx, cancelRun := context.WithCancel(context.Background())
		t.Cleanup(cancelRun)
		collectorWorker.Start(runCtx)
		postCheck(t, cpa, st, manager, usageSvc)
	}

	assertUnproven := func(t *testing.T, st *store.Store, usageSvc *usageapp.Service) {
		t.Helper()
		ctx := context.Background()
		if startedAt, err := usageadapter.New(st.SQLDB()).CollectionStartedAtMS(ctx); err != nil || startedAt != 0 {
			t.Fatalf("CollectionStartedAtMS = %d err=%v, want 0", startedAt, err)
		}
		summary, err := usageSvc.Summary(ctx, []identity.APIKeyID{"99887766554433221100ffeeddccbb99"})
		if err != nil {
			t.Fatalf("Summary: %v", err)
		}
		if summary.Coverage.State != "unknown" || summary.Coverage.Reason != "collection_unproven" {
			t.Fatalf("Summary coverage = %+v, want unknown/collection_unproven", summary.Coverage)
		}
	}

	t.Run("failed_queue_500_stays_unproven_then_marks_on_recovery", func(t *testing.T) {
		runCase(t, setupHooks{mode: model.UpgradeTargetManaged, collectorOn: true, cpaFailQueue: true}, func(t *testing.T, cpa *mockUpgradeUsageCPA, st *store.Store, manager *collector.Manager, usageSvc *usageapp.Service) {
			deadline := time.Now().Add(5 * time.Second)
			for manager.Status().Collector != "error" {
				if time.Now().After(deadline) {
					t.Fatalf("collector did not enter error state: %+v", manager.Status())
				}
				time.Sleep(15 * time.Millisecond)
			}
			assertUnproven(t, st, usageSvc)

			cpa.mu.Lock()
			cpa.failQueue = false
			cpa.mu.Unlock()
			deadline = time.Now().Add(5 * time.Second)
			for manager.Status().Collector != "running" {
				if time.Now().After(deadline) {
					t.Fatalf("collector did not recover to running: %+v", manager.Status())
				}
				time.Sleep(20 * time.Millisecond)
			}
			if startedAt, err := usageadapter.New(st.SQLDB()).CollectionStartedAtMS(context.Background()); err != nil || startedAt <= 0 {
				t.Fatalf("CollectionStartedAtMS after recovery = %d err=%v, want > 0", startedAt, err)
			}
		})
	})

	t.Run("auth_rejection_401_stays_unknown", func(t *testing.T) {
		runCase(t, setupHooks{mode: model.UpgradeTargetExternal, collectorOn: true, cpaRejectAuth: true}, func(t *testing.T, _ *mockUpgradeUsageCPA, st *store.Store, manager *collector.Manager, usageSvc *usageapp.Service) {
			deadline := time.Now().Add(5 * time.Second)
			for manager.Status().Collector != "stopped" {
				if time.Now().After(deadline) {
					t.Fatalf("collector did not stop on 401: %+v", manager.Status())
				}
				time.Sleep(15 * time.Millisecond)
			}
			assertUnproven(t, st, usageSvc)
			binding, found, err := upgradeoperation.New(st.SQLDB()).ExternalBinding(context.Background())
			if err != nil || !found {
				t.Fatalf("ExternalBinding: found=%v err=%v", found, err)
			}
			checkRej := newUpgradeExternalRejection(st.SQLDB())
			defer checkRej.close()
			refused, err := checkRej.check(context.Background(), binding.Identity, envelopeDigest(binding.ManagementKey))
			if err != nil || !refused {
				t.Fatalf("durable external rejection after collector 401: refused=%v err=%v, want true/nil", refused, err)
			}
		})
	})

	t.Run("cpa_usage_statistics_disabled_stays_unknown", func(t *testing.T) {
		runCase(t, setupHooks{mode: model.UpgradeTargetExternal, collectorOn: true, cpaUsageOff: true}, func(t *testing.T, _ *mockUpgradeUsageCPA, st *store.Store, manager *collector.Manager, usageSvc *usageapp.Service) {
			deadline := time.Now().Add(5 * time.Second)
			for manager.Status().Collector != "running" {
				if time.Now().After(deadline) {
					t.Fatalf("collector did not reach running: %+v", manager.Status())
				}
				time.Sleep(15 * time.Millisecond)
			}
			assertUnproven(t, st, usageSvc)
		})
	})

	t.Run("managed_runtime_offline_stays_unknown", func(t *testing.T) {
		runCase(t, setupHooks{mode: model.UpgradeTargetManaged, collectorOn: true, runtimeState: string(model.RuntimeStateOffline)}, func(t *testing.T, _ *mockUpgradeUsageCPA, st *store.Store, manager *collector.Manager, usageSvc *usageapp.Service) {
			deadline := time.Now().Add(5 * time.Second)
			for manager.Status().Collector != "running" {
				if time.Now().After(deadline) {
					t.Fatalf("collector did not reach running: %+v", manager.Status())
				}
				time.Sleep(15 * time.Millisecond)
			}
			assertUnproven(t, st, usageSvc)
		})
	})

	t.Run("managed_foreign_runtime_identity_stays_unknown", func(t *testing.T) {
		runCase(t, setupHooks{mode: model.UpgradeTargetManaged, collectorOn: true, runtimeIdentity: "runtime-foreign-99"}, func(t *testing.T, _ *mockUpgradeUsageCPA, st *store.Store, manager *collector.Manager, usageSvc *usageapp.Service) {
			deadline := time.Now().Add(5 * time.Second)
			for manager.Status().Collector != "running" {
				if time.Now().After(deadline) {
					t.Fatalf("collector did not reach running: %+v", manager.Status())
				}
				time.Sleep(15 * time.Millisecond)
			}
			assertUnproven(t, st, usageSvc)
		})
	})

	t.Run("managed_accepts_import_destination_key_alone", func(t *testing.T) {
		runCase(t, setupHooks{mode: model.UpgradeTargetManaged, collectorOn: true, importDestOnly: true}, func(t *testing.T, _ *mockUpgradeUsageCPA, st *store.Store, manager *collector.Manager, _ *usageapp.Service) {
			deadline := time.Now().Add(5 * time.Second)
			for manager.Status().Collector != "running" {
				if time.Now().After(deadline) {
					t.Fatalf("collector did not reach running: %+v", manager.Status())
				}
				time.Sleep(15 * time.Millisecond)
			}
			if startedAt, err := usageadapter.New(st.SQLDB()).CollectionStartedAtMS(context.Background()); err != nil || startedAt <= 0 {
				t.Fatalf("CollectionStartedAtMS with upgrade_cpa_import_destination_v1 alone = %d err=%v, want > 0", startedAt, err)
			}
		})
	})

	t.Run("managed_conflicting_destination_settings_stays_unknown", func(t *testing.T) {
		runCase(t, setupHooks{mode: model.UpgradeTargetManaged, collectorOn: true, conflictingDest: true}, func(t *testing.T, _ *mockUpgradeUsageCPA, st *store.Store, manager *collector.Manager, usageSvc *usageapp.Service) {
			if status := manager.Status().Collector; status != "stopped" {
				t.Fatalf("collector status on conflicting managed destination = %q, want stopped", status)
			}
			assertUnproven(t, st, usageSvc)
		})
	})

	t.Run("foreign_env_override_stays_unknown", func(t *testing.T) {
		foreignCPA := newMockUpgradeUsageCPA(t, "case-upgrade-mgmt-secret", "client-key-1")
		runCase(t, setupHooks{mode: model.UpgradeTargetExternal, collectorOn: true, foreignEnvURL: foreignCPA.server.URL}, func(t *testing.T, _ *mockUpgradeUsageCPA, st *store.Store, manager *collector.Manager, usageSvc *usageapp.Service) {
			if status := manager.Status().Collector; status != "stopped" {
				t.Fatalf("collector status on foreign env override = %q, want stopped", status)
			}
			assertUnproven(t, st, usageSvc)
		})
	})

	t.Run("foreign_manager_config_target_stays_unknown", func(t *testing.T) {
		foreignCPA := newMockUpgradeUsageCPA(t, "case-upgrade-mgmt-secret", "client-key-1")
		runCase(t, setupHooks{mode: model.UpgradeTargetExternal, collectorOn: true, foreignCfgURL: foreignCPA.server.URL}, func(t *testing.T, _ *mockUpgradeUsageCPA, st *store.Store, manager *collector.Manager, usageSvc *usageapp.Service) {
			if status := manager.Status().Collector; status != "stopped" {
				t.Fatalf("collector status on foreign manager_config target = %q, want stopped", status)
			}
			assertUnproven(t, st, usageSvc)
		})
	})

	t.Run("moved_external_target_stays_unknown", func(t *testing.T) {
		runCase(t, setupHooks{mode: model.UpgradeTargetExternal, collectorOn: true, moveExternal: true}, func(t *testing.T, _ *mockUpgradeUsageCPA, st *store.Store, manager *collector.Manager, usageSvc *usageapp.Service) {
			if status := manager.Status().Collector; status != "stopped" {
				t.Fatalf("collector status on moved external target = %q, want stopped", status)
			}
			assertUnproven(t, st, usageSvc)
		})
	})

	t.Run("incomplete_upgrade_operation_stays_unknown", func(t *testing.T) {
		runCase(t, setupHooks{mode: model.UpgradeTargetManaged, collectorOn: true, incompleteStage: true}, func(t *testing.T, _ *mockUpgradeUsageCPA, st *store.Store, manager *collector.Manager, usageSvc *usageapp.Service) {
			if status := manager.Status().Collector; status != "stopped" {
				t.Fatalf("collector status on incomplete upgrade operation = %q, want stopped", status)
			}
			assertUnproven(t, st, usageSvc)
		})
	})
}

func TestUpgradeStartupProofConvergesAcrossDelayedReadinessAndFencesStaleRuns(t *testing.T) {
	waitForRunning := func(t *testing.T, manager *collector.Manager) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for manager.Status().Collector != "running" {
			if time.Now().After(deadline) {
				t.Fatalf("collector did not reach running: %+v", manager.Status())
			}
			time.Sleep(15 * time.Millisecond)
		}
	}
	waitForProven := func(t *testing.T, st *store.Store, usageSvc *usageapp.Service) {
		t.Helper()
		ctx := context.Background()
		deadline := time.Now().Add(5 * time.Second)
		for {
			startedAt, err := usageadapter.New(st.SQLDB()).CollectionStartedAtMS(ctx)
			if err == nil && startedAt > 0 {
				summary, sumErr := usageSvc.Summary(ctx, []identity.APIKeyID{"99887766554433221100ffeeddccbb99"})
				if sumErr != nil {
					t.Fatalf("Summary after proof: %v", sumErr)
				}
				if summary.Coverage.Reason == "collection_unproven" {
					t.Fatalf("Summary coverage remained collection_unproven after proof: %+v", summary.Coverage)
				}
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("CollectionStartedAtMS did not converge (last=%d err=%v)", startedAt, err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	assertUnproven := func(t *testing.T, st *store.Store, usageSvc *usageapp.Service) {
		t.Helper()
		ctx := context.Background()
		if startedAt, err := usageadapter.New(st.SQLDB()).CollectionStartedAtMS(ctx); err != nil || startedAt != 0 {
			t.Fatalf("CollectionStartedAtMS = %d err=%v, want 0", startedAt, err)
		}
		summary, err := usageSvc.Summary(ctx, []identity.APIKeyID{"99887766554433221100ffeeddccbb99"})
		if err != nil {
			t.Fatalf("Summary: %v", err)
		}
		if summary.Coverage.State != "unknown" || summary.Coverage.Reason != "collection_unproven" {
			t.Fatalf("Summary coverage = %+v, want unknown/collection_unproven", summary.Coverage)
		}
	}

	setupHarness := func(
		t *testing.T,
		mode string,
		rtState string,
		failConfig bool,
		usageEnabled bool,
	) (*mockUpgradeUsageCPA, *mockUpgradeRuntimeSupervisor, *store.Store, *security.Protector, *collector.Manager, *worker.CollectorWorker, *usageapp.Service, upgradeUsageStartupConfig) {
		t.Helper()
		const (
			mgmtSecret      = "converge-upgrade-mgmt-secret"
			runtimeToken    = "converge-runtime-token"
			expectedRuntime = "runtime-converge-1"
		)
		cpa := newMockUpgradeUsageCPA(t, mgmtSecret, "client-key-1")
		cpa.mu.Lock()
		cpa.failConfig = failConfig
		cpa.usageEnabled = usageEnabled
		cpa.mu.Unlock()

		rt := newMockUpgradeRuntimeSupervisor(t, runtimeToken, expectedRuntime, 4, rtState)
		enabled := true
		_, dbPath, dataKeyPath, _, _ := seedUpgradeUsageStartupStore(
			t, mode, cpa.server.URL, mgmtSecret, &enabled, expectedRuntime, 4, "/upg-console",
		)
		keyBytes, _, err := security.LoadOrCreateDataKey("", dataKeyPath)
		if err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector(keyBytes)
		if err != nil {
			t.Fatal(err)
		}
		st, err := store.Open(dbPath, protector)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })

		// Use only canonical upgrade_cpa_import_destination_v1 on Managed to mirror real cpa-import stores.
		if mode == model.UpgradeTargetManaged {
			if _, err := st.SQLDB().Exec(`delete from settings where key = ?`, upgradeDestinationSettingKey); err != nil {
				t.Fatal(err)
			}
		}

		cfg := config.Config{
			DBPath:        dbPath,
			CollectorMode: "http",
			PollInterval:  25 * time.Millisecond,
		}
		manager := collector.NewManager(cfg, st)
		collectorSvc := collectorservice.New(manager)
		collectorWorker := worker.NewCollectorWorker(cfg, st, collectorSvc)
		serverApp := httpapi.New(cfg, st, manager)
		rtClient := runtimeservice.NewEmbeddedClient(rt.server.URL, runtimeToken)
		usageSvc := usageapp.New(usageadapter.New(st.SQLDB()), func() usageapp.CollectionPhase {
			return collectionPhaseFor(manager.Status().Collector)
		})
		rejection := newUpgradeExternalRejection(st.SQLDB())
		t.Cleanup(func() {
			stopCtx, cancelStop := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancelStop()
			_ = manager.StopAndWait(stopCtx)
			rejection.close()
		})
		startupCfg := upgradeUsageStartupConfig{
			DB:                   st.SQLDB(),
			Store:                st,
			Protector:            protector,
			Runtime:              rtClient,
			ManagerConfigService: serverApp.AppContext().ManagerConfigService,
			Usage:                usageSvc,
			Rejection:            rejection,
			CollectorWorker:      collectorWorker,
		}
		return cpa, rt, st, protector, manager, collectorWorker, usageSvc, startupCfg
	}

	t.Run("converges_after_managed_runtime_initially_starting", func(t *testing.T) {
		_, rt, st, _, manager, collectorWorker, usageSvc, startupCfg := setupHarness(
			t, model.UpgradeTargetManaged, string(model.RuntimeStateStarting), false, true,
		)
		attachUpgradeUsageStartupObserver(manager, startupCfg)
		runCtx, cancelRun := context.WithCancel(context.Background())
		defer cancelRun()
		collectorWorker.Start(runCtx)

		waitForRunning(t, manager)
		time.Sleep(60 * time.Millisecond)
		assertUnproven(t, st, usageSvc)

		rt.mu.Lock()
		rt.state = string(model.RuntimeStateReady)
		rt.gen = 99
		rt.mu.Unlock()
		waitForProven(t, st, usageSvc)
	})

	t.Run("converges_after_temporary_config_proof_failure", func(t *testing.T) {
		cpa, _, st, _, manager, collectorWorker, usageSvc, startupCfg := setupHarness(
			t, model.UpgradeTargetExternal, string(model.RuntimeStateReady), true, true,
		)
		attachUpgradeUsageStartupObserver(manager, startupCfg)
		runCtx, cancelRun := context.WithCancel(context.Background())
		defer cancelRun()
		collectorWorker.Start(runCtx)

		waitForRunning(t, manager)
		time.Sleep(60 * time.Millisecond)
		assertUnproven(t, st, usageSvc)

		cpa.mu.Lock()
		cpa.failConfig = false
		cpa.mu.Unlock()
		waitForProven(t, st, usageSvc)
	})

	t.Run("converges_after_later_cpa_usage_enable", func(t *testing.T) {
		cpa, _, st, _, manager, collectorWorker, usageSvc, startupCfg := setupHarness(
			t, model.UpgradeTargetExternal, string(model.RuntimeStateReady), false, false,
		)
		attachUpgradeUsageStartupObserver(manager, startupCfg)
		runCtx, cancelRun := context.WithCancel(context.Background())
		defer cancelRun()
		collectorWorker.Start(runCtx)

		waitForRunning(t, manager)
		time.Sleep(60 * time.Millisecond)
		assertUnproven(t, st, usageSvc)

		cpa.mu.Lock()
		cpa.usageEnabled = true
		cpa.mu.Unlock()
		waitForProven(t, st, usageSvc)
	})

	t.Run("fences_stale_run_and_never_accepts_new_run_from_old_collection_start", func(t *testing.T) {
		cpa, rt, st, _, manager, _, usageSvc, startupCfg := setupHarness(
			t, model.UpgradeTargetManaged, string(model.RuntimeStateReady), false, true,
		)
		ctx := context.Background()
		validRunCfg := collector.RuntimeConfig{
			CPAUpstreamURL: cpa.server.URL,
			ManagementKey:  "converge-upgrade-mgmt-secret",
			CollectorMode:  "http",
		}
		manager.Start(ctx, validRunCfg)
		waitForRunning(t, manager)
		run1 := manager.StartCount()

		// Stale or mismatched runID must be rejected without writing CollectionStartedAtMS.
		if proven, err := proveUpgradeCollectionStart(ctx, manager, run1+99, startupCfg, validRunCfg); err != nil || proven {
			t.Fatalf("stale runID proveUpgradeCollectionStart = (%v, %v), want (false, nil)", proven, err)
		}
		assertUnproven(t, st, usageSvc)

		// Supersede run1 with run2 on a foreign Runtime identity after pre-seeding an older global collection-start.
		if err := usageSvc.MarkCollectionStart(ctx); err != nil {
			t.Fatal(err)
		}
		rt.mu.Lock()
		rt.identity = "runtime-foreign-supersede"
		rt.mu.Unlock()
		manager.Start(ctx, validRunCfg)
		waitForRunning(t, manager)
		run2 := manager.StartCount()

		// Stale run1 callback after run2 started must be rejected.
		if proven, err := proveUpgradeCollectionStart(ctx, manager, run1, startupCfg, validRunCfg); err != nil || proven {
			t.Fatalf("superseded run1 proveUpgradeCollectionStart = (%v, %v), want (false, nil)", proven, err)
		}
		// Active run2 must NOT be accepted solely because CollectionStartedAtMS > 0 already exists.
		if proven, err := proveUpgradeCollectionStart(ctx, manager, run2, startupCfg, validRunCfg); err != nil || proven {
			t.Fatalf("foreign-target run2 proveUpgradeCollectionStart = (%v, %v), want (false, nil)", proven, err)
		}

		// Restoring the expected runtime identity lets run2 prove readiness genuinely.
		rt.mu.Lock()
		rt.identity = "runtime-converge-1"
		rt.mu.Unlock()
		if proven, err := proveUpgradeCollectionStart(ctx, manager, run2, startupCfg, validRunCfg); err != nil || !proven {
			t.Fatalf("restored run2 proveUpgradeCollectionStart = (%v, %v), want (true, nil)", proven, err)
		}
	})

	t.Run("propagates_sql_and_context_errors_truthfully", func(t *testing.T) {
		cpa, rt, st, _, _, _, _, startupCfg := setupHarness(
			t, model.UpgradeTargetManaged, string(model.RuntimeStateStarting), false, true,
		)
		runCfg := collector.RuntimeConfig{
			CPAUpstreamURL: cpa.server.URL,
			ManagementKey:  "converge-upgrade-mgmt-secret",
			CollectorMode:  "http",
		}
		// Starting runtime is unproven, not an error.
		if proven, err := proveUpgradeCollectionStart(context.Background(), nil, 0, startupCfg, runCfg); err != nil || proven {
			t.Fatalf("starting runtime proveUpgradeCollectionStart = (%v, %v), want (false, nil)", proven, err)
		}

		// Cancelled context propagates context.Canceled.
		canceledCtx, cancel := context.WithCancel(context.Background())
		cancel()
		if proven, err := proveUpgradeCollectionStart(canceledCtx, nil, 0, startupCfg, runCfg); proven || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled proveUpgradeCollectionStart = (%v, %v), want (false, context.Canceled)", proven, err)
		}

		// SQL failure propagates non-nil error.
		rt.mu.Lock()
		rt.state = string(model.RuntimeStateReady)
		rt.mu.Unlock()
		if _, err := st.SQLDB().Exec(`alter table settings rename to settings_hidden`); err != nil {
			t.Fatal(err)
		}
		if proven, err := proveUpgradeCollectionStart(context.Background(), nil, 0, startupCfg, runCfg); proven || err == nil {
			t.Fatalf("SQL failure proveUpgradeCollectionStart = (%v, %v), want (false, non-nil err)", proven, err)
		}
	})

	t.Run("rejects_provenance_verdict_and_observed_at_tampering", func(t *testing.T) {
		checkManagedMut := func(name string, mutate func(t *testing.T, st *store.Store, protector *security.Protector, rt *mockUpgradeRuntimeSupervisor)) {
			t.Run(name, func(t *testing.T) {
				cpa, rt, st, protector, _, _, usageSvc, startupCfg := setupHarness(
					t, model.UpgradeTargetManaged, string(model.RuntimeStateReady), false, true,
				)
				mutate(t, st, protector, rt)
				runCfg := collector.RuntimeConfig{
					CPAUpstreamURL: cpa.server.URL,
					ManagementKey:  "converge-upgrade-mgmt-secret",
					CollectorMode:  "http",
				}
				if proven, err := proveUpgradeCollectionStart(context.Background(), nil, 0, startupCfg, runCfg); err != nil || proven {
					t.Fatalf("%s proveUpgradeCollectionStart = (%v, %v), want (false, nil)", name, proven, err)
				}
				assertUnproven(t, st, usageSvc)
			})
		}

		updateDest := func(t *testing.T, st *store.Store, edit func(map[string]any, map[string]any)) {
			t.Helper()
			var raw string
			if err := st.SQLDB().QueryRow(`select value from settings where key = ?`, upgradeImportDestinationSettingKey).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := json.Unmarshal([]byte(raw), &doc); err != nil {
				t.Fatal(err)
			}
			intent, _ := doc["intent"].(map[string]any)
			edit(doc, intent)
			updated, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.SQLDB().Exec(`update settings set value = ? where key = ?`, string(updated), upgradeImportDestinationSettingKey); err != nil {
				t.Fatal(err)
			}
		}

		checkManagedMut("bad_source_class", func(t *testing.T, st *store.Store, _ *security.Protector, _ *mockUpgradeRuntimeSupervisor) {
			updateDest(t, st, func(_ map[string]any, intent map[string]any) {
				intent["sourceClass"] = "fresh_managed"
			})
		})
		checkManagedMut("bad_source_identity", func(t *testing.T, st *store.Store, _ *security.Protector, _ *mockUpgradeRuntimeSupervisor) {
			updateDest(t, st, func(_ map[string]any, intent map[string]any) {
				intent["sourceIdentity"] = "sha256:" + strings.Repeat("c", 64)
			})
		})
		checkManagedMut("mismatched_attempt_operation_id", func(t *testing.T, st *store.Store, _ *security.Protector, _ *mockUpgradeRuntimeSupervisor) {
			updateDest(t, st, func(doc map[string]any, _ map[string]any) {
				doc["attempt"] = 2
			})
		})
		checkManagedMut("zero_expected_generation", func(t *testing.T, st *store.Store, _ *security.Protector, _ *mockUpgradeRuntimeSupervisor) {
			updateDest(t, st, func(_ map[string]any, intent map[string]any) {
				intent["expectedRuntimeGeneration"] = 0
			})
		})
		checkManagedMut("unqualified_expected_artifact", func(t *testing.T, st *store.Store, _ *security.Protector, _ *mockUpgradeRuntimeSupervisor) {
			updateDest(t, st, func(_ map[string]any, intent map[string]any) {
				intent["expectedActiveArtifactId"] = "sha256:" + strings.Repeat("9", 64)
			})
		})
		checkManagedMut("live_runtime_artifact_mismatch", func(_ *testing.T, _ *store.Store, _ *security.Protector, rt *mockUpgradeRuntimeSupervisor) {
			rt.mu.Lock()
			rt.artifact = "sha256:" + strings.Repeat("8", 64)
			rt.mu.Unlock()
		})
		checkManagedMut("verdict_fingerprint_mismatch", func(t *testing.T, st *store.Store, _ *security.Protector, _ *mockUpgradeRuntimeSupervisor) {
			if _, err := st.SQLDB().Exec(`update upgrade_operations set source_fingerprint_after = 'sha256:drifted' where id = 1`); err != nil {
				t.Fatal(err)
			}
		})
		checkManagedMut("conflicting_external_binding_on_managed", func(t *testing.T, st *store.Store, protector *security.Protector, _ *mockUpgradeRuntimeSupervisor) {
			sealed, err := protector.ProtectString("converge-upgrade-mgmt-secret")
			if err != nil {
				t.Fatal(err)
			}
			extID := freshkeyapply.ExternalIdentity("https://conflict.example")
			if _, err := st.SQLDB().Exec(
				`insert into upgrade_external_connect (id, base_url, identity, generation, management_key, observed_at_ms, client_keys) values (1, ?, ?, ?, ?, 1000, 1)`,
				"https://conflict.example", extID, extID, sealed,
			); err != nil {
				t.Fatal(err)
			}
		})

		t.Run("external_observed_at_mismatch_and_refused_gate", func(t *testing.T) {
			cpa, _, st, protector, _, _, usageSvc, startupCfg := setupHarness(
				t, model.UpgradeTargetExternal, string(model.RuntimeStateReady), false, true,
			)
			runCfg := collector.RuntimeConfig{
				CPAUpstreamURL: cpa.server.URL,
				ManagementKey:  "converge-upgrade-mgmt-secret",
				CollectorMode:  "http",
			}
			// Refused External credential must fail closed without recording collection start.
			binding, found, err := upgradeoperation.New(st.SQLDB()).ExternalBinding(context.Background())
			if err != nil || !found {
				t.Fatalf("ExternalBinding: found=%v err=%v", found, err)
			}
			if err := startupCfg.Rejection.record(context.Background(), binding.Identity, envelopeDigest(binding.ManagementKey)); err != nil {
				t.Fatal(err)
			}
			if proven, err := proveUpgradeCollectionStart(context.Background(), nil, 0, startupCfg, runCfg); err != nil || proven {
				t.Fatalf("refused external proveUpgradeCollectionStart = (%v, %v), want (false, nil)", proven, err)
			}
			assertUnproven(t, st, usageSvc)

			// Clear refusal, then drift observed_at_ms away from verification.bindingObservedAtMs.
			if err := startupCfg.Rejection.clear(context.Background()); err != nil {
				t.Fatal(err)
			}
			_ = protector
			if _, err := st.SQLDB().Exec(`update upgrade_external_connect set observed_at_ms = observed_at_ms + 500 where id = 1`); err != nil {
				t.Fatal(err)
			}
			if proven, err := proveUpgradeCollectionStart(context.Background(), nil, 0, startupCfg, runCfg); err != nil || proven {
				t.Fatalf("observed_at_ms mismatch proveUpgradeCollectionStart = (%v, %v), want (false, nil)", proven, err)
			}
			assertUnproven(t, st, usageSvc)
		})
	})
}

func TestUpgradeExternalCollectorRefusalPersistenceStopRestartAndRebind(t *testing.T) {
	t.Run("collector_poll_refusal_persists_blocks_restarts_until_rebound", func(t *testing.T) {
		const (
			initialSecret = "external-initial-mgmt-secret-1111"
			rotatedSecret = "external-rotated-mgmt-secret-2222"
			controlPath   = "/upg-console"
		)
		cpaServer := newMockUpgradeUsageCPA(t, initialSecret, "client-key-1")
		enabled := true
		_, dbPath, dataKeyPath, _, _ := seedUpgradeUsageStartupStore(
			t, model.UpgradeTargetExternal, cpaServer.server.URL, initialSecret, &enabled, "", 0, controlPath,
		)
		keyBytes, _, err := security.LoadOrCreateDataKey("", dataKeyPath)
		if err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector(keyBytes)
		if err != nil {
			t.Fatal(err)
		}
		st, err := store.Open(dbPath, protector)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })

		cfg := config.Config{
			DBPath:        dbPath,
			DataKeyPath:   dataKeyPath,
			CollectorMode: "http",
			PollInterval:  25 * time.Millisecond,
		}
		manager1 := collector.NewManager(cfg, st)
		collectorSvc1 := collectorservice.New(manager1)
		collectorWorker1 := worker.NewCollectorWorker(cfg, st, collectorSvc1)
		serverApp1 := httpapi.New(cfg, st, manager1)
		usageSvc1 := usageapp.New(usageadapter.New(st.SQLDB()), func() usageapp.CollectionPhase {
			return collectionPhaseFor(manager1.Status().Collector)
		})
		handler1, closeHandler1, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
			DB:        st.SQLDB(),
			Protector: protector,
			Usage:     usageSvc1,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeHandler1)
		t.Cleanup(func() { _ = manager1.StopAndWait(context.Background()) })

		attachUpgradeUsageStartupObserver(manager1, upgradeUsageStartupConfig{
			DB:                   st.SQLDB(),
			Store:                st,
			Protector:            protector,
			ManagerConfigService: serverApp1.AppContext().ManagerConfigService,
			Usage:                usageSvc1,
			Rejection:            handler1.rejection,
			CollectorWorker:      collectorWorker1,
		})

		ctx := context.Background()
		collectorWorker1.Start(ctx)
		deadline := time.Now().Add(5 * time.Second)
		for manager1.Status().Collector != "running" {
			if time.Now().After(deadline) {
				t.Fatalf("collector1 did not reach running: %+v", manager1.Status())
			}
			time.Sleep(15 * time.Millisecond)
		}

		// Rotate management credential on External CPA so the next Collector poll receives HTTP 401.
		cpaServer.mu.Lock()
		cpaServer.management = rotatedSecret
		cpaServer.mu.Unlock()
		cpaServer.mgmtRequests.Store(0)

		deadline = time.Now().Add(5 * time.Second)
		for manager1.Status().Collector != "stopped" {
			if time.Now().After(deadline) {
				t.Fatalf("collector1 did not stop after credential rotation: %+v", manager1.Status())
			}
			time.Sleep(15 * time.Millisecond)
		}
		_ = manager1.StopAndWait(ctx)

		requestsAfterStop := cpaServer.mgmtRequests.Load()
		if requestsAfterStop != 1 {
			t.Fatalf("outbound management requests to observe refusal and stop = %d, want 1", requestsAfterStop)
		}

		// Wait until the auth-rejection callback finishes persisting durable refusal.
		deadline = time.Now().Add(3 * time.Second)
		for {
			_, resolveErr := handler1.resolveExternalInstall(ctx)
			if errors.Is(resolveErr, cpa.ErrManagementRejected) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("resolveExternalInstall after collector refusal = %v, want ErrManagementRejected", resolveErr)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if got := cpaServer.mgmtRequests.Load(); got != requestsAfterStop {
			t.Fatalf("resolveExternalInstall contacted CPA after refusal: got %d requests, want %d", got, requestsAfterStop)
		}

		// Simulate Manager restart on the same SQLite store: CollectorWorker.Start must not contact CPA.
		manager2 := collector.NewManager(cfg, st)
		collectorSvc2 := collectorservice.New(manager2)
		collectorWorker2 := worker.NewCollectorWorker(cfg, st, collectorSvc2)
		serverApp2 := httpapi.New(cfg, st, manager2)
		usageSvc2 := usageapp.New(usageadapter.New(st.SQLDB()), func() usageapp.CollectionPhase {
			return collectionPhaseFor(manager2.Status().Collector)
		})
		handler2, closeHandler2, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
			DB:        st.SQLDB(),
			Protector: protector,
			Usage:     usageSvc2,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeHandler2)
		t.Cleanup(func() { _ = manager2.StopAndWait(context.Background()) })

		attachUpgradeUsageStartupObserver(manager2, upgradeUsageStartupConfig{
			DB:                   st.SQLDB(),
			Store:                st,
			Protector:            protector,
			ManagerConfigService: serverApp2.AppContext().ManagerConfigService,
			Usage:                usageSvc2,
			Rejection:            handler2.rejection,
			CollectorWorker:      collectorWorker2,
		})

		cpaServer.mgmtRequests.Store(0)
		collectorWorker2.Start(ctx)
		time.Sleep(80 * time.Millisecond)
		if status := manager2.Status().Collector; status != "stopped" {
			t.Fatalf("collector2 status on restart with refused credential = %q, want stopped", status)
		}
		if got := cpaServer.mgmtRequests.Load(); got != 0 {
			t.Fatalf("outbound CPA requests across restart with refused credential = %d, want 0", got)
		}
	})

	t.Run("workspace_rejection_record_stops_matching_run_without_stopping_rebound_run", func(t *testing.T) {
		const (
			initialSecret = "external-live-stop-secret-1111"
			rotatedSecret = "external-live-stop-secret-2222"
			controlPath   = "/upg-console"
		)
		cpaServer := newMockUpgradeUsageCPA(t, initialSecret, "client-key-1")
		enabled := true
		_, dbPath, dataKeyPath, _, _ := seedUpgradeUsageStartupStore(
			t, model.UpgradeTargetExternal, cpaServer.server.URL, initialSecret, &enabled, "", 0, controlPath,
		)
		keyBytes, _, err := security.LoadOrCreateDataKey("", dataKeyPath)
		if err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector(keyBytes)
		if err != nil {
			t.Fatal(err)
		}
		st, err := store.Open(dbPath, protector)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })

		cfg := config.Config{
			DBPath:        dbPath,
			DataKeyPath:   dataKeyPath,
			CollectorMode: "http",
			PollInterval:  25 * time.Millisecond,
		}
		manager := collector.NewManager(cfg, st)
		collectorSvc := collectorservice.New(manager)
		collectorWorker := worker.NewCollectorWorker(cfg, st, collectorSvc)
		serverApp := httpapi.New(cfg, st, manager)
		usageSvc := usageapp.New(usageadapter.New(st.SQLDB()), func() usageapp.CollectionPhase {
			return collectionPhaseFor(manager.Status().Collector)
		})
		handler, closeHandler, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
			DB:        st.SQLDB(),
			Protector: protector,
			Usage:     usageSvc,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeHandler)
		t.Cleanup(func() { _ = manager.StopAndWait(context.Background()) })

		attachUpgradeUsageStartupObserver(manager, upgradeUsageStartupConfig{
			DB:                   st.SQLDB(),
			Store:                st,
			Protector:            protector,
			ManagerConfigService: serverApp.AppContext().ManagerConfigService,
			Usage:                usageSvc,
			Rejection:            handler.rejection,
			CollectorWorker:      collectorWorker,
		})

		ctx := context.Background()
		collectorWorker.Start(ctx)
		deadline := time.Now().Add(5 * time.Second)
		for manager.Status().Collector != "running" {
			if time.Now().After(deadline) {
				t.Fatalf("collector did not reach running: %+v", manager.Status())
			}
			time.Sleep(15 * time.Millisecond)
		}

		binding1, found, err := upgradeoperation.New(st.SQLDB()).ExternalBinding(ctx)
		if err != nil || !found {
			t.Fatalf("ExternalBinding: found=%v err=%v", found, err)
		}
		oldDigest := envelopeDigest(strings.TrimSpace(binding1.ManagementKey))

		// Recording refusal for the active credential stops the running Collector immediately.
		if err := handler.rejection.record(ctx, binding1.Identity, oldDigest); err != nil {
			t.Fatalf("rejection.record: %v", err)
		}
		if status := manager.Status().Collector; status != "stopped" {
			t.Fatalf("collector status after rejection.record = %q, want stopped", status)
		}
		_ = manager.StopAndWait(ctx)
		cpaServer.mgmtRequests.Store(0)
		time.Sleep(60 * time.Millisecond)
		if got := cpaServer.mgmtRequests.Load(); got != 0 {
			t.Fatalf("outbound CPA requests after active collector stopped on refusal = %d, want 0", got)
		}

		// Simulate rebind to rotatedSecret and start a new Collector run.
		cpaServer.mu.Lock()
		cpaServer.management = rotatedSecret
		cpaServer.mu.Unlock()
		sealedRotated, err := protector.ProtectString(rotatedSecret)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := st.SQLDB().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := upgradeoperation.RebindCompletedExternalConnectionTx(
			ctx, tx, binding1.ObservedAtMS, sealedRotated, "v8.0.13", "supported",
		); err != nil {
			_ = tx.Rollback()
			t.Fatalf("RebindCompletedExternalConnectionTx: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		canonicalURL, err := identity.CanonicalExternalURL(cpaServer.server.URL)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SaveManagerConfigAndSetup(
			ctx,
			model.ManagerConfig{
				CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: canonicalURL, ManagementKey: rotatedSecret},
				Collector:     model.ManagerCollectorConfig{Enabled: &enabled, CollectorMode: "http", PollIntervalMS: 25},
			},
			model.Setup{CPAUpstreamURL: canonicalURL, ManagementKey: rotatedSecret},
		); err != nil {
			t.Fatal(err)
		}
		if err := handler.rejection.clear(ctx); err != nil {
			t.Fatalf("rejection.clear: %v", err)
		}
		newDigest := envelopeDigest(sealedRotated)
		handler.rejection.noteCollectorStart(binding1.Identity, newDigest, canonicalURL, rotatedSecret)
		manager.Start(ctx, collector.RuntimeConfig{
			CPAUpstreamURL: canonicalURL,
			ManagementKey:  rotatedSecret,
			CollectorMode:  "http",
			PollInterval:   25 * time.Millisecond,
		})
		deadline = time.Now().Add(5 * time.Second)
		for manager.Status().Collector != "running" {
			if time.Now().After(deadline) {
				t.Fatalf("rebound collector did not reach running: %+v", manager.Status())
			}
			time.Sleep(15 * time.Millisecond)
		}

		// Late refusal record for oldDigest must NOT stop the rebound run on rotatedSecret.
		if err := handler.rejection.record(ctx, binding1.Identity, oldDigest); err != nil {
			t.Fatalf("late oldDigest rejection.record: %v", err)
		}
		if status := manager.Status().Collector; status != "running" {
			t.Fatalf("rebound collector stopped by late old-credential refusal: status=%q", status)
		}
		if refused, err := handler.rejection.check(ctx, binding1.Identity, newDigest); err != nil || refused {
			t.Fatalf("rebound credential marked refused by old-credential refusal: refused=%v err=%v", refused, err)
		}
	})
}
