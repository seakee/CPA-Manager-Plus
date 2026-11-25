package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	freshstrategy "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
)

const (
	bridgeTestWorkspaceSecret = "cpamp_sk_0123456789abcdef0123456789abcdef0123456789abcdef0123456789ab1111"
	bridgeTestInitialSecret   = "sk-0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWX2222"
	bridgeTestAdoptedSecret   = "custom-adopted-gateway-client-key-9999"
	bridgeTestCommentSecret   = "sk-commented-out-legacy-secret-do-not-expose-8888"
	bridgeTestProviderSecret  = "AIzaSyProviderUpstreamGeminiKeyKeepIntact"
)

type bridgeKeysUpstream struct {
	server          *httptest.Server
	managementKey   string
	writeHits       atomic.Int32
	malformedBodies atomic.Bool
}

func newBridgeKeysUpstream(t *testing.T, managementKey string) *bridgeKeysUpstream {
	t.Helper()
	u := &bridgeKeysUpstream{managementKey: managementKey}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+u.managementKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			u.writeHits.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		w.Header().Set("X-CPA-Version", "v8.0.55")
		if u.malformedBodies.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{broken-payload`))
			return
		}
		switch r.URL.Path {
		case "/v0/management/api-keys":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"api-keys": []string{
					bridgeTestWorkspaceSecret,
					bridgeTestInitialSecret,
					bridgeTestAdoptedSecret,
				},
			})
		case "/v0/management/config":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"port":     8317,
				"api-keys": []string{bridgeTestWorkspaceSecret, bridgeTestInitialSecret},
				"apiKeys":  []string{bridgeTestAdoptedSecret},
				"access": map[string]any{
					"api-keys": []string{bridgeTestWorkspaceSecret},
					"mode":     "strict",
				},
				"gemini-api-key": []map[string]any{
					{"api-key": bridgeTestProviderSecret},
				},
				"openai-compatibility": []map[string]any{
					{"name": "openrouter", "base-url": "https://openrouter.ai/api/v1"},
				},
			})
		case "/v0/management/config.yaml":
			w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
			_, _ = w.Write([]byte("" +
				"# comment with old key " + bridgeTestCommentSecret + "\n" +
				"port: 8317\n" +
				"api-keys:\n" +
				"  - " + bridgeTestWorkspaceSecret + "\n" +
				"  - " + bridgeTestInitialSecret + "\n" +
				"access:\n" +
				"  api-keys:\n" +
				"    - " + bridgeTestAdoptedSecret + "\n" +
				"  mode: strict\n" +
				"gemini-api-key:\n" +
				"  - api-key: " + bridgeTestProviderSecret + "\n"))
		case "/v8/management/config":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"config-version": 8,
				"access":         map[string]any{"api-keys": []string{bridgeTestWorkspaceSecret}},
			})
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(u.server.Close)
	return u
}

func assertBridgeContainmentAndSanitization(
	t *testing.T,
	modeLabel string,
	wantWriteCode string,
	upstream *bridgeKeysUpstream,
	doReq func(method, resource, body string) (int, []byte),
) {
	t.Helper()

	// 1. All client-key write routes are refused with 409 and zero upstream write requests.
	for _, writeCase := range []struct {
		method   string
		resource string
		body     string
	}{
		{http.MethodPut, "cpamp/v0/management/api-keys", `["` + bridgeTestWorkspaceSecret + `"]`},
		{http.MethodPatch, "cpamp/v0/management/api-keys", `{"old":"a","new":"b"}`},
		{http.MethodDelete, "cpamp/v0/management/api-keys", `{"value":"a"}`},
		{http.MethodPut, "cpamp/v0/management/api-key-aliases", `{"hash":"` + strings.Repeat("a", 64) + `","alias":"Bypass"}`},
		{http.MethodDelete, "cpamp/v0/management/api-key-aliases/" + strings.Repeat("a", 64), ""},
		{http.MethodPut, "cpamp/v0/management/config.yaml", "port: 8317\n"},
		{http.MethodPut, "cpamp/v0/management/config", `{"port":8317}`},
	} {
		beforeWrites := upstream.writeHits.Load()
		status, raw := doReq(writeCase.method, writeCase.resource, writeCase.body)
		if status != http.StatusConflict {
			t.Fatalf("[%s] %s %s status = %d, want 409: %s", modeLabel, writeCase.method, writeCase.resource, status, string(raw))
		}
		var errBody struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(raw, &errBody); err != nil {
			t.Fatalf("[%s] %s %s invalid JSON error %q: %v", modeLabel, writeCase.method, writeCase.resource, string(raw), err)
		}
		if errBody.Code != wantWriteCode {
			t.Fatalf("[%s] %s %s code = %q, want %q", modeLabel, writeCase.method, writeCase.resource, errBody.Code, wantWriteCode)
		}
		if delta := upstream.writeHits.Load() - beforeWrites; delta != 0 {
			t.Fatalf("[%s] %s %s forwarded %d writes to CPA", modeLabel, writeCase.method, writeCase.resource, delta)
		}
	}

	assertNoPlaintextClientSecrets := func(endpoint string, raw []byte) {
		t.Helper()
		text := string(raw)
		for _, secret := range []string{
			bridgeTestWorkspaceSecret,
			bridgeTestInitialSecret,
			bridgeTestAdoptedSecret,
			bridgeTestCommentSecret,
		} {
			if strings.Contains(text, secret) {
				t.Fatalf("[%s] %s leaked client secret %q in payload: %s", modeLabel, endpoint, secret, text)
			}
		}
	}

	// 2. GET cpamp/v0/management/api-keys returns masked previews with preserved length and zero plaintext.
	statusKeys, rawKeys := doReq(http.MethodGet, "cpamp/v0/management/api-keys", "")
	if statusKeys != http.StatusOK {
		t.Fatalf("[%s] GET api-keys status = %d: %s", modeLabel, statusKeys, string(rawKeys))
	}
	assertNoPlaintextClientSecrets("GET cpamp/v0/management/api-keys", rawKeys)
	var keysPayload struct {
		APIKeys []string `json:"api-keys"`
	}
	if err := json.Unmarshal(rawKeys, &keysPayload); err != nil {
		t.Fatalf("[%s] decode api-keys JSON: %v (%s)", modeLabel, err, string(rawKeys))
	}
	wantMasked := []string{"cpamp_sk_…1111", "sk-…2222", "…9999"}
	if len(keysPayload.APIKeys) != len(wantMasked) {
		t.Fatalf("[%s] api-keys count = %d, want %d: %#v", modeLabel, len(keysPayload.APIKeys), len(wantMasked), keysPayload.APIKeys)
	}
	for i, want := range wantMasked {
		if keysPayload.APIKeys[i] != want {
			t.Fatalf("[%s] api-keys[%d] = %q, want %q", modeLabel, i, keysPayload.APIKeys[i], want)
		}
	}

	// 3. GET cpamp/v0/management/config strips client api-keys/apiKeys/access.api-keys while preserving provider keys.
	statusCfg, rawCfg := doReq(http.MethodGet, "cpamp/v0/management/config", "")
	if statusCfg != http.StatusOK {
		t.Fatalf("[%s] GET config status = %d: %s", modeLabel, statusCfg, string(rawCfg))
	}
	assertNoPlaintextClientSecrets("GET cpamp/v0/management/config", rawCfg)
	if !strings.Contains(string(rawCfg), bridgeTestProviderSecret) {
		t.Fatalf("[%s] GET config dropped upstream provider gemini-api-key: %s", modeLabel, string(rawCfg))
	}
	var cfgDoc map[string]any
	if err := json.Unmarshal(rawCfg, &cfgDoc); err != nil {
		t.Fatalf("[%s] decode config JSON: %v", modeLabel, err)
	}
	if topKeys, ok := cfgDoc["api-keys"].([]any); !ok || len(topKeys) != 0 {
		t.Fatalf("[%s] config[\"api-keys\"] = %#v, want empty slice", modeLabel, cfgDoc["api-keys"])
	}
	if camelKeys, ok := cfgDoc["apiKeys"].([]any); !ok || len(camelKeys) != 0 {
		t.Fatalf("[%s] config[\"apiKeys\"] = %#v, want empty slice", modeLabel, cfgDoc["apiKeys"])
	}
	accessMap, ok := cfgDoc["access"].(map[string]any)
	if !ok || accessMap["mode"] != "strict" {
		t.Fatalf("[%s] config[\"access\"] = %#v, want preserved access.mode=strict", modeLabel, cfgDoc["access"])
	}
	if accessKeys, ok := accessMap["api-keys"].([]any); !ok || len(accessKeys) != 0 {
		t.Fatalf("[%s] config[\"access\"][\"api-keys\"] = %#v, want empty slice", modeLabel, accessMap["api-keys"])
	}

	// 4. GET cpamp/v0/management/config.yaml strips client keys and comments while keeping provider config.
	statusYAML, rawYAML := doReq(http.MethodGet, "cpamp/v0/management/config.yaml", "")
	if statusYAML != http.StatusOK {
		t.Fatalf("[%s] GET config.yaml status = %d: %s", modeLabel, statusYAML, string(rawYAML))
	}
	assertNoPlaintextClientSecrets("GET cpamp/v0/management/config.yaml", rawYAML)
	if !strings.Contains(string(rawYAML), bridgeTestProviderSecret) {
		t.Fatalf("[%s] GET config.yaml dropped upstream provider gemini-api-key: %s", modeLabel, string(rawYAML))
	}
	var parsedYAML map[string]any
	if err := yaml.Unmarshal(rawYAML, &parsedYAML); err != nil {
		t.Fatalf("[%s] decode sanitized config.yaml: %v (%s)", modeLabel, err, string(rawYAML))
	}
	if yamlKeys, ok := parsedYAML["api-keys"].([]any); !ok || len(yamlKeys) != 0 {
		t.Fatalf("[%s] config.yaml api-keys = %#v, want empty slice", modeLabel, parsedYAML["api-keys"])
	}
	yamlAccess, ok := parsedYAML["access"].(map[string]any)
	if !ok || yamlAccess["mode"] != "strict" {
		t.Fatalf("[%s] config.yaml access = %#v", modeLabel, parsedYAML["access"])
	}
	if yamlAccessKeys, ok := yamlAccess["api-keys"].([]any); !ok || len(yamlAccessKeys) != 0 {
		t.Fatalf("[%s] config.yaml access.api-keys = %#v, want empty slice", modeLabel, yamlAccess["api-keys"])
	}

	// 5. Malformed upstream payloads fail closed with 502 and never echo raw bytes.
	upstream.malformedBodies.Store(true)
	defer upstream.malformedBodies.Store(false)
	for _, readResource := range []string{
		"cpamp/v0/management/api-keys",
		"cpamp/v0/management/config",
		"cpamp/v0/management/config.yaml",
	} {
		statusBad, rawBad := doReq(http.MethodGet, readResource, "")
		if statusBad != http.StatusBadGateway {
			t.Fatalf("[%s] malformed %s status = %d, want 502: %s", modeLabel, readResource, statusBad, string(rawBad))
		}
		if strings.Contains(string(rawBad), "broken-payload") {
			t.Fatalf("[%s] malformed %s echoed unsanitized upstream bytes: %s", modeLabel, readResource, string(rawBad))
		}
	}
}

func TestFreshCPAMPBridgeContainmentAcrossManagedAdoptAndExternal(t *testing.T) {
	const mgmtKey = externalAuthorityManagement

	for _, strategy := range []bootstrap.CPAStrategy{bootstrap.NewManaged, bootstrap.AdoptExisting} {
		t.Run(string(strategy), func(t *testing.T) {
			upstream := newBridgeKeysUpstream(t, mgmtKey)
			app := newInstallationTestApplication(t)
			protected, err := app.protector.ProtectString(mgmtKey)
			if err != nil {
				t.Fatal(err)
			}
			rec, err := app.store.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			s := validInstallationRuntime()
			if _, err := app.store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(r *freshstore.Record) error {
				r.Operation.Routing.InferenceTarget = "/cpamp-api"
				r.Operation.Routing.InferenceCommitted = true
				checkpoint := bootstrap.FreshStrategyCheckpoint{
					Strategy:                      strategy,
					Stage:                         "committed",
					TargetIdentity:                string(s.Identity),
					TargetGeneration:              uint64(s.Generation),
					TargetArtifactID:              string(s.ActiveGatewayArtifact.ArtifactID),
					DestinationConnectionIdentity: freshstrategy.ManagedDestinationIdentity(upstream.server.URL),
				}
				if strategy == bootstrap.AdoptExisting {
					checkpoint.DestinationConnectionIdentity = freshstrategy.AdoptedDestinationIdentity(upstream.server.URL)
					checkpoint.SourceIdentity = "external:sha256:" + strings.Repeat("d", 64)
					checkpoint.SourceClass = bootstrap.AdoptNetworkExisting
					checkpoint.AdoptionResources = []bootstrap.ResourceRef{
						bootstrap.AdoptionRefClientAPIKeys,
						bootstrap.AdoptionRefCPAAuthFiles,
						bootstrap.AdoptionRefConfigServer,
						bootstrap.AdoptionRefProviderStateProviders,
					}
					checkpoint.AdoptionDecisions = []bootstrap.AdoptionResourceDecision{
						{ResourceRef: bootstrap.AdoptionRefClientAPIKeys, Kind: bootstrap.APIKeys, Scope: bootstrap.ClientAPIKeys, ReadEvidenceRef: "preflight:keys-read", ReplayEvidenceRef: "preflight:keys-replay", VerifyEvidenceRef: "preflight:keys-verify", Qualification: bootstrap.CapabilityQualified, Disposition: bootstrap.Migrate},
						{ResourceRef: bootstrap.AdoptionRefCPAAuthFiles, Kind: bootstrap.Credentials, Scope: bootstrap.CPAAuthFiles, Qualification: bootstrap.CapabilityPending, ManualAction: bootstrap.ReauthenticateAfterAdoption, Disposition: bootstrap.ManualAction},
						{ResourceRef: bootstrap.AdoptionRefConfigServer, Kind: bootstrap.CPAConfiguration, Scope: bootstrap.ManagementAPIFields, Qualification: bootstrap.CapabilityPending, ManualAction: bootstrap.ReconfigureAfterAdoption, Disposition: bootstrap.ManualAction},
						{ResourceRef: bootstrap.AdoptionRefProviderStateProviders, Kind: bootstrap.ProviderRuntimeState, Scope: bootstrap.ProviderState, Qualification: bootstrap.CapabilityPending, ManualAction: bootstrap.ReauthenticateAfterAdoption, Disposition: bootstrap.ManualAction},
					}
					r.AdoptionCiphertext = protected
				}
				r.Operation.Strategy = checkpoint
				r.Connection = &model.ManagerCPAConnectionConfig{
					CPABaseURL:    upstream.server.URL,
					ManagementKey: protected,
				}
				r.Operation.ProductComplete = true
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := setting.New(app.legacyStore.SQLDB(), app.protector).SaveManagerConfig(t.Context(), model.ManagerConfig{
				CPAConnection: model.ManagerCPAConnectionConfig{
					CPABaseURL:    upstream.server.URL,
					ManagementKey: mgmtKey,
				},
			}); err != nil {
				t.Fatal(err)
			}

			assertBridgeContainmentAndSanitization(t, string(strategy), legacyWriteReplaced, upstream, func(method, resource, body string) (int, []byte) {
				resp := gatewayRequest(t, app, method, resource, body, "application/json")
				return resp.Code, resp.Body.Bytes()
			})

			// Verify unified cross-page alias directory over cpamp/v0/management/api-key-aliases.
			keyHashSum := sha256.Sum256([]byte(bridgeTestWorkspaceSecret))
			keyHash := hex.EncodeToString(keyHashSum[:])
			if _, err := app.legacyStore.SQLDB().ExecContext(t.Context(), `
				insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms)
				values ('cccccccccccccccccccccccccccccccc', 1, 'active', 100, 200);
				insert into fresh_client_api_keys (request_id, api_key_id, secret_ciphertext, secret_hash, masked_preview, name, name_revision, origin)
				values ('33333333333333333333333333333333', 'cccccccccccccccccccccccccccccccc', 'cipher-only', ?, 'cpamp_sk_…1111', 'Bridge Directory Key', 2, 'workspace');
			`, keyHash); err != nil {
				t.Fatal(err)
			}
			aliasResp := gatewayRequest(t, app, http.MethodGet, "cpamp/v0/management/api-key-aliases", "", "")
			if aliasResp.Code != http.StatusOK {
				t.Fatalf("GET cpamp/v0/management/api-key-aliases = %d: %s", aliasResp.Code, aliasResp.Body.String())
			}
			if strings.Contains(aliasResp.Body.String(), bridgeTestWorkspaceSecret) || strings.Contains(aliasResp.Body.String(), "cipher-only") {
				t.Fatalf("aliases endpoint leaked secret material: %s", aliasResp.Body.String())
			}
			var aliasPayload struct {
				Items []model.APIKeyAlias `json:"items"`
			}
			if err := json.Unmarshal(aliasResp.Body.Bytes(), &aliasPayload); err != nil {
				t.Fatal(err)
			}
			if len(aliasPayload.Items) != 1 || aliasPayload.Items[0].Alias != "Bridge Directory Key" || aliasPayload.Items[0].MaskedPreview != "cpamp_sk_…1111" {
				t.Fatalf("aliases items = %#v", aliasPayload.Items)
			}
		})
	}

	t.Run("connect_external", func(t *testing.T) {
		upstream := newBridgeKeysUpstream(t, mgmtKey)
		app := newExternalApp(t, upstream.server.URL, mgmtKey)
		assertBridgeContainmentAndSanitization(t, "connect_external", externalLegacyWriteReplaced, upstream, func(method, resource, body string) (int, []byte) {
			resp := gatewayRequest(t, app, method, resource, body, "application/json")
			return resp.Code, resp.Body.Bytes()
		})
	})
}

func TestUpgradeCPAMPBridgeContainmentAcrossManagedAndExternal(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	mgmtKey := strings.Repeat("b", 64)

	t.Run("upgrade_managed", func(t *testing.T) {
		upstream := newBridgeKeysUpstream(t, mgmtKey)
		st, cfg, adminKey := seedCompletedManagedUpgradeStore(t, "/upg-console", "/upg-gw")
		keyBytes, _, err := security.LoadOrCreateDataKey("", cfg.DataKeyPath)
		if err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector(keyBytes)
		if err != nil {
			t.Fatal(err)
		}
		sealedDestKey, err := protector.ProtectString(mgmtKey)
		if err != nil {
			t.Fatal(err)
		}
		qualifiedArtifact, err := freshstrategy.QualifiedUpgradeAdoptionArtifactID()
		if err != nil {
			t.Fatal(err)
		}
		sourceIdentity := "external:sha256:" + strings.Repeat("c", 64)
		pluginsInit := false
		destPayload, _ := json.Marshal(upgradeDestinationRecord{
			BaseURL:           upstream.server.URL,
			URLProved:         true,
			ManagementKey:     sealedDestKey,
			ServerFingerprint: "external:sha256:" + strings.Repeat("b", 64),
			Attempt:           1,
			OperationID:       upgradeManagedDestinationOperationID(sourceIdentity, 1),
			Intent: upgradeDestinationIntent{
				ExpectedRuntimeIdentity:   "rt-managed-bridge",
				ExpectedRuntimeGeneration: 7,
				ExpectedActiveArtifactID:  qualifiedArtifact,
				SourceIdentity:            sourceIdentity,
				SourceClass:               upgradeManagedDestinationClass,
				APIKeys:                   []string{bridgeTestInitialSecret},
				PluginsEnabled:            &pluginsInit,
			},
		})
		if _, err := st.SQLDB().Exec(`insert into settings(key, value, updated_at_ms) values(?, ?, ?)`, upgradeImportDestinationSettingKey, string(destPayload), time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SetEmbeddedRuntimeDesiredLifecycle(context.Background(), model.EmbeddedRuntimeDesiredRunning); err != nil {
			t.Fatal(err)
		}

		rt := &upgradeCanonicalTestRuntime{
			status: model.RuntimeObservedStatus{
				Identity:           "rt-managed-bridge",
				Generation:         19,
				ProtocolVersion:    "v1",
				State:              model.RuntimeStateReady,
				CPAObservedVersion: "v8.0.55",
				Capabilities:       model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStop, model.RuntimeCapabilityRestart},
				ActiveGatewayArtifact: &model.ActiveGatewayArtifact{
					Engine:     "cpa",
					ArtifactID: qualifiedArtifact,
					Version:    "v8.0.55",
				},
			},
		}

		manager := collector.NewManager(cfg, st)
		t.Cleanup(func() { manager.Stop() })
		serverApp := httpapi.New(cfg, st, manager)
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, func(context.Context) bool { return true }, upgradeCanonicalDependencies{
			Protector: protector,
			Runtime:   rt,
		}); err != nil {
			t.Fatalf("attachCompletedUpgradeRouting(Managed): %v", err)
		}
		ts := httptest.NewServer(serverApp.Handler())
		defer ts.Close()

		assertBridgeContainmentAndSanitization(t, "upgrade_managed", legacyWriteReplaced, upstream, func(method, resource, body string) (int, []byte) {
			var reader io.Reader
			if body != "" {
				reader = strings.NewReader(body)
			}
			req, err := http.NewRequest(method, ts.URL+"/upg-console/api/v2/"+resource, reader)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+adminKey)
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			raw, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatal(err)
			}
			return res.StatusCode, raw
		})
	})

	t.Run("upgrade_external", func(t *testing.T) {
		extMgmtKey := strings.Repeat("a", 64)
		upstream := newBridgeKeysUpstream(t, extMgmtKey)
		st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", upstream.server.URL)
		if err := st.SaveManagerConfig(context.Background(), model.ManagerConfig{
			CPAConnection: model.ManagerCPAConnectionConfig{
				CPABaseURL:    upstream.server.URL,
				ManagementKey: extMgmtKey,
			},
		}); err != nil {
			t.Fatal(err)
		}
		serverApp := httpapi.New(cfg, st, nil)
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil); err != nil {
			t.Fatalf("attachCompletedUpgradeRouting(External): %v", err)
		}
		ts := httptest.NewServer(serverApp.Handler())
		defer ts.Close()

		assertBridgeContainmentAndSanitization(t, "upgrade_external", externalLegacyWriteReplaced, upstream, func(method, resource, body string) (int, []byte) {
			var reader io.Reader
			if body != "" {
				reader = strings.NewReader(body)
			}
			req, err := http.NewRequest(method, ts.URL+"/ext-console/api/v2/"+resource, reader)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+adminKey)
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			raw, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatal(err)
			}
			return res.StatusCode, raw
		})
	})
}

func TestSanitizeBridgedConfigYAMLAliasesMergeKeysAndFailClosedWriter(t *testing.T) {
	// 1) YAML anchors (*alias) and merge keys (<<) cannot smuggle client API keys past sanitization.
	yamlWithAnchors := []byte(`
defaults: &defs
  api-keys: &shared_keys
    - &first_key sk-merged-secret-1111
    - &second_key cpk_merged_secret_2222
  port: 8317
<<: *defs
access:
  <<: *defs
  mode: strict
nested-int-map:
  1: *first_key
  2: keep-int-entry
nested-key-map:
  *first_key: mapped-role
  safe-key: keep-role
nested-mixed-map:
  1: keep-mixed-entry
  *second_key: mapped-mixed-role
gemini-api-key:
  - api-key: AIzaSyProviderMustSurvive
`)
	sanitized, ct, err := sanitizeBridgedConfigYAML(yamlWithAnchors)
	if err != nil {
		t.Fatalf("sanitizeBridgedConfigYAML(anchors/merge): %v", err)
	}
	if !strings.HasPrefix(ct, "application/yaml") {
		t.Fatalf("content-type = %q", ct)
	}
	text := string(sanitized)
	for _, leaked := range []string{"sk-merged-secret-1111", "cpk_merged_secret_2222"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("sanitized YAML leaked anchored/merged client secret %q:\n%s", leaked, text)
		}
	}
	for _, preserved := range []string{"AIzaSyProviderMustSurvive", "keep-int-entry", "keep-role", "keep-mixed-entry"} {
		if !strings.Contains(text, preserved) {
			t.Fatalf("sanitized YAML dropped expected non-secret value %q:\n%s", preserved, text)
		}
	}

	// 2) Non-200 206 Partial Content, unexpected Content-Encoding, and secret-bearing 500 error bodies fail closed.
	t.Run("206_partial_content_fails_closed_502", func(t *testing.T) {
		rec := httptest.NewRecorder()
		sw := newBridgeSanitizingWriter(rec, sanitizeBridgedConfigYAML)
		sw.Header().Set("Content-Type", "application/yaml")
		sw.WriteHeader(http.StatusPartialContent)
		_, _ = sw.Write([]byte("api-keys:\n  - sk-partial-leak\n"))
		sw.finish(http.MethodGet)
		if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "sk-partial-leak") {
			t.Fatalf("206 response = %d %q, want 502 without leak", rec.Code, rec.Body.String())
		}
	})
	t.Run("content_encoding_fails_closed_502", func(t *testing.T) {
		rec := httptest.NewRecorder()
		sw := newBridgeSanitizingWriter(rec, sanitizeBridgedConfigYAML)
		sw.Header().Set("Content-Encoding", "br")
		sw.WriteHeader(http.StatusOK)
		_, _ = sw.Write([]byte("compressed-secret-sk-leak"))
		sw.finish(http.MethodGet)
		if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "sk-leak") {
			t.Fatalf("encoded response = %d %q, want 502 without leak", rec.Code, rec.Body.String())
		}
	})
	t.Run("500_upstream_secret_dump_is_scrubbed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		sw := newBridgeSanitizingWriter(rec, sanitizeBridgedConfigYAML)
		sw.WriteHeader(http.StatusInternalServerError)
		_, _ = sw.Write([]byte(`{"error":"failed near sk-raw-upstream-leak"}`))
		sw.finish(http.MethodGet)
		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "sk-raw-upstream-leak") {
			t.Fatalf("500 secret dump = %d %q, want 500 scrubbed", rec.Code, rec.Body.String())
		}
	})
}

func TestSanitizeBridgedConfigJSONPreservesV8ProviderGroupsAndScrubsCustomErrorBody(t *testing.T) {
	v8ConfigJSON := []byte(`{
		"access": {
			"api-keys": ["cpamp_sk_1111111111111111111111111111111111111111111111111111111111111111", "custom-adopted-client-secret-7777"],
			"mode": "strict"
		},
		"api-keys": {
			"claude": [{"api-key": "sk-ant-upstream-provider-must-stay"}],
			"openai-compatibility": [{"name": "upstream-relay", "api-key": "rk-upstream-provider"}]
		}
	}`)
	sanitized, ct, err := sanitizeBridgedConfigJSON(v8ConfigJSON)
	if err != nil {
		t.Fatalf("sanitizeBridgedConfigJSON(v8): %v", err)
	}
	if ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	rawOut := string(sanitized)
	for _, leaked := range []string{"cpamp_sk_1111", "custom-adopted-client-secret-7777"} {
		if strings.Contains(rawOut, leaked) {
			t.Fatalf("sanitized v8 JSON leaked client secret %q: %s", leaked, rawOut)
		}
	}
	if !strings.Contains(rawOut, "sk-ant-upstream-provider-must-stay") || !strings.Contains(rawOut, "openai-compatibility") {
		t.Fatalf("sanitized v8 JSON wiped top-level provider-group api-keys map: %s", rawOut)
	}

	// Scalar api-keys / access.api-keys must fail closed in both JSON and YAML.
	if _, _, err := sanitizeBridgedConfigJSON([]byte(`{"api-keys": 123}`)); err == nil {
		t.Fatal("sanitizeBridgedConfigJSON accepted scalar api-keys")
	}
	if _, _, err := sanitizeBridgedConfigYAML([]byte("api-keys: 123\n")); err == nil {
		t.Fatal("sanitizeBridgedConfigYAML accepted scalar api-keys")
	}
	if _, _, err := sanitizeBridgedConfigYAML([]byte("access:\n  api-keys: not-a-list\n")); err == nil {
		t.Fatal("sanitizeBridgedConfigYAML accepted scalar access.api-keys")
	}
	if _, _, err := sanitizeBridgedConfigYAML([]byte("api-keys:\n  - 123\n")); err == nil {
		t.Fatal("sanitizeBridgedConfigYAML accepted non-string api-keys item")
	}

	// Custom adopted secret in upstream 500 JSON detail/error must be scrubbed to generic error, while Manager-local refusal codes pass through.
	recLeak := httptest.NewRecorder()
	swLeak := newBridgeSanitizingWriter(recLeak, sanitizeBridgedConfigJSON)
	swLeak.WriteHeader(http.StatusInternalServerError)
	_, _ = swLeak.Write([]byte(`{"detail":"custom-adopted-gateway-client-key-9999"}`))
	swLeak.finish(http.MethodGet)
	if recLeak.Code != http.StatusInternalServerError || strings.Contains(recLeak.Body.String(), "custom-adopted-gateway-client-key-9999") {
		t.Fatalf("custom adopted secret leaked in 500 body: %d %q", recLeak.Code, recLeak.Body.String())
	}

	recLocal := httptest.NewRecorder()
	swLocal := newBridgeSanitizingWriter(recLocal, sanitizeBridgedConfigJSON)
	swLocal.WriteHeader(http.StatusConflict)
	_, _ = swLocal.Write([]byte(`{"code":"external_management_key_rejected","detail":"` + externalRebindForbiddenDetail + `"}`))
	swLocal.finish(http.MethodGet)
	if recLocal.Code != http.StatusConflict || !strings.Contains(recLocal.Body.String(), externalRebindForbiddenDetail) {
		t.Fatalf("Manager-local forbidden detail unexpectedly scrubbed: %d %q", recLocal.Code, recLocal.Body.String())
	}
}
