package pluginquota

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	pluginquotaclient "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/pluginquota"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	managerconfigsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	quotasnapshotsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/quotasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

type pluginQuotaCPAMock struct {
	server *httptest.Server

	mu           sync.Mutex
	fetchBodies  []string
	quotaPaths   []string
	authFileHits int
}

func newPluginQuotaCPAMock(t *testing.T) *pluginQuotaCPAMock {
	t.Helper()
	mock := &pluginQuotaCPAMock{}
	mock.server = httptest.NewServer(http.HandlerFunc(mock.serveHTTP))
	t.Cleanup(mock.server.Close)
	return mock
}

func (m *pluginQuotaCPAMock) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v0/management/quota/providers":
		_, _ = w.Write([]byte(`{"providers":[
			{"plugin_id":"first-plugin","provider":"first-plugin","display_name":"First","supported_providers":["first-plugin"],"supports_reset":false},
			{"plugin_id":"second-plugin","provider":"second-plugin","display_name":"Second","supported_providers":["second-plugin","shared-plugin"]}
		]}`))
	case r.URL.Path == "/v0/management/auth-files":
		m.mu.Lock()
		m.authFileHits++
		m.mu.Unlock()
		_, _ = w.Write([]byte(`{"files":[
			{"name":"first-1.json","auth_index":"index-first-1","provider":"first-plugin","quota_provider":"first-plugin","account":"account-1","label":"First one"},
			{"name":"second-1.json","auth_index":"index-second-1","provider":"second-plugin","quota_provider":"second-plugin","account":"account-2"},
			{"name":"shared-1.json","auth_index":"index-shared-1","provider":"shared-plugin","account":"account-3"},
			{"name":"codex-1.json","auth_index":"index-codex-1","provider":"codex","account":"account-4"},
			{"name":"unknown-1.json","auth_index":"index-unknown-1","provider":"first-plugin","quota_provider":"retired-plugin"}
		]}`))
	case strings.HasPrefix(r.URL.Path, "/v0/management/plugins/"):
		m.mu.Lock()
		m.quotaPaths = append(m.quotaPaths, r.URL.Path+"?"+r.URL.RawQuery)
		m.mu.Unlock()
		switch r.URL.Path {
		case "/v0/management/plugins/first-plugin/quota":
			if r.URL.Query().Get("auth_index") != "index-first-1" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"summary":[
				{"key":"credit_remaining","label":"剩余额度","value":1739.5,"unit":"credit","format":"number"},
				{"key":"daily_checkin","label":"今日可签到","value":0,"format":"boolean"}
			]}`))
		case "/v0/management/plugins/second-plugin/quota":
			_, _ = w.Write([]byte(`{"summary":[{"key":"balance","label":"Balance","value":5,"unit":"USD","format":"currency","currency":"USD"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	case r.URL.Path == "/v0/management/quota/fetch":
		body := map[string]string{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		encoded, _ := json.Marshal(body)
		m.mu.Lock()
		m.fetchBodies = append(m.fetchBodies, string(encoded))
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (m *pluginQuotaCPAMock) observed() (paths []string, fetches []string, authFileHits int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.quotaPaths...), append([]string(nil), m.fetchBodies...), m.authFileHits
}

func newPluginQuotaTestService(t *testing.T, cpaURL string) (*Service, *quotasnapshotsvc.Service) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	testutil.EnsureAdminCredential(t, st)
	t.Cleanup(func() { _ = st.Close() })

	cfg := config.Config{
		DBPath:        filepath.Join(t.TempDir(), "usage.sqlite"),
		Queue:         "usage",
		PopSide:       "right",
		BatchSize:     100,
		QueryLimit:    50000,
		CORSOrigins:   []string{"*"},
		CollectorMode: "auto",
	}
	manager := collectorpkg.NewManager(cfg, st)
	managerConfigService := managerconfigsvc.New(cfg, st, collector.New(manager))
	if cpaURL != "" {
		if err := st.SaveManagerConfigAndSetup(context.Background(), store.ManagerConfig{
			CPAConnection: store.ManagerCPAConnectionConfig{
				CPABaseURL:    cpaURL,
				ManagementKey: "management-key",
			},
		}, store.Setup{
			CPAUpstreamURL: cpaURL,
			ManagementKey:  "management-key",
		}); err != nil {
			t.Fatalf("save manager config: %v", err)
		}
	}
	quotaSnapshots := quotasnapshotsvc.New(st)
	service := New(
		managerConfigService,
		quotaSnapshots,
		ServiceOptions{},
		pluginquotaclient.New(nil),
	)
	return service, quotaSnapshots
}

func TestCredentialQuotaResolvesProviderFromCatalogAndPersistsItems(t *testing.T) {
	mock := newPluginQuotaCPAMock(t)
	service, quotaSnapshots := newPluginQuotaTestService(t, mock.server.URL)

	quota, err := service.CredentialQuota(context.Background(), "index-first-1")
	if err != nil {
		t.Fatalf("credential quota: %v", err)
	}
	if quota.Provider != "first-plugin" || quota.DisplayName != "First" {
		t.Fatalf("quota provider = %#v", quota)
	}
	if len(quota.Items) != 2 || quota.Items[0].Key != "credit_remaining" {
		t.Fatalf("quota items = %#v", quota.Items)
	}
	if value, ok := quota.Items[0].Value.(float64); !ok || value != 1739.5 {
		t.Fatalf("item value = %#v", quota.Items[0].Value)
	}
	if quota.Items[1].Format != "boolean" {
		t.Fatalf("item format = %#v", quota.Items[1])
	}

	// The same read must leave durable evidence the panel's snapshot tab and
	// the monitoring surfaces read back.
	query, err := quotaSnapshots.Query(context.Background(), quotasnapshotsvc.QueryRequest{
		Accounts: []quotasnapshotsvc.QueryAccount{{
			RowKey: "row-1", Provider: "first-plugin",
			Account: quotasnapshotsvc.AccountTarget{
				AuthFileSnapshot:     "first-1.json",
				AuthProviderSnapshot: "first-plugin",
				AuthIndex:            "index-first-1",
				AccountSnapshot:      "account-1",
			},
		}},
	})
	if err != nil {
		t.Fatalf("query snapshots: %v", err)
	}
	if len(query.Items) != 1 || len(query.Items[0].Windows) != 2 {
		t.Fatalf("persisted windows = %#v", query.Items)
	}
	windows := map[string]quotasnapshotsvc.Window{}
	for _, window := range query.Items[0].Windows {
		windows[window.ProviderWindowID] = window
	}
	remaining, ok := windows["credit_remaining"]
	if !ok {
		t.Fatalf("credit_remaining window missing: %#v", windows)
	}
	if remaining.QuotaUnit != "credit" || remaining.WindowMode != "non_window" {
		t.Fatalf("credit_remaining window = %#v", remaining)
	}
	if remaining.LimitValue == nil || *remaining.LimitValue != 1739.5 {
		t.Fatalf("credit_remaining value = %#v", remaining.LimitValue)
	}
}

func TestCredentialQuotaFallsBackToSupportedProviders(t *testing.T) {
	mock := newPluginQuotaCPAMock(t)
	service, _ := newPluginQuotaTestService(t, mock.server.URL)

	quota, err := service.CredentialQuota(context.Background(), "index-shared-1")
	if err != nil {
		t.Fatalf("credential quota: %v", err)
	}
	if quota.Provider != "second-plugin" {
		t.Fatalf("supported provider binding = %#v", quota)
	}
}

func TestCredentialQuotaRejectsCredentialsWithoutPluginQuota(t *testing.T) {
	mock := newPluginQuotaCPAMock(t)
	service, _ := newPluginQuotaTestService(t, mock.server.URL)

	if _, err := service.CredentialQuota(context.Background(), "index-codex-1"); !errors.Is(err, ErrNoQuotaProvider) {
		t.Fatalf("built-in credential error = %v", err)
	}
	if _, err := service.CredentialQuota(context.Background(), "index-unknown-1"); !errors.Is(err, ErrNoQuotaProvider) {
		t.Fatalf("retired quota provider error = %v", err)
	}
	if _, err := service.CredentialQuota(context.Background(), "index-missing"); !errors.Is(err, ErrCredentialMissing) {
		t.Fatalf("missing credential error = %v", err)
	}
	if _, err := service.CredentialQuota(context.Background(), "  "); !errors.Is(err, pluginquotaclient.ErrAuthIndexRequired) {
		t.Fatalf("missing auth index error = %v", err)
	}
}

func TestProvidersRegistersCatalogueForSnapshotWrites(t *testing.T) {
	mock := newPluginQuotaCPAMock(t)
	service, quotaSnapshots := newPluginQuotaTestService(t, mock.server.URL)

	providers, err := service.Providers(context.Background())
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	if len(providers) != 2 {
		t.Fatalf("providers = %#v", providers)
	}
	registered := quotaSnapshots.PluginQuotaProviders()
	if len(registered) != 2 || registered[0] != "first-plugin" || registered[1] != "second-plugin" {
		t.Fatalf("registered providers = %#v", registered)
	}
}

func TestRefreshCoversPluginCredentialsOnly(t *testing.T) {
	mock := newPluginQuotaCPAMock(t)
	service, _ := newPluginQuotaTestService(t, mock.server.URL)

	result, err := service.Refresh(context.Background())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(result.Credentials) != 3 {
		t.Fatalf("refreshed credentials = %#v", result.Credentials)
	}
	for _, credential := range result.Credentials {
		if credential.AuthFileName == "codex-1.json" || credential.AuthFileName == "unknown-1.json" {
			t.Fatalf("refresh included a non plugin credential: %#v", credential)
		}
		if credential.Error != "" {
			t.Fatalf("refresh failed for %s: %s", credential.AuthFileName, credential.Error)
		}
	}
	if result.FailedCount() != 0 {
		t.Fatalf("failed count = %d", result.FailedCount())
	}
	counts := map[string]int{}
	for _, provider := range result.Providers {
		counts[provider.Provider.Provider] = provider.CredentialCount
	}
	if counts["first-plugin"] != 1 || counts["second-plugin"] != 2 {
		t.Fatalf("provider credential counts = %#v", counts)
	}
	if snapshot := service.Snapshot(); len(snapshot.Credentials) != 3 {
		t.Fatalf("stored snapshot = %#v", snapshot)
	}

	paths, fetches, _ := mock.observed()
	if len(paths) != 3 || len(fetches) != 0 {
		t.Fatalf("refresh paths = %#v fetches = %#v", paths, fetches)
	}
}

func TestRefreshUpstreamTriggersCPAQuotaFetch(t *testing.T) {
	mock := newPluginQuotaCPAMock(t)
	service, _ := newPluginQuotaTestService(t, mock.server.URL)

	if _, err := service.RefreshUpstream(context.Background(), ""); err != nil {
		t.Fatalf("refresh upstream: %v", err)
	}
	_, fetches, _ := mock.observed()
	if len(fetches) != 1 || fetches[0] != "{}" {
		t.Fatalf("quota fetch bodies = %#v", fetches)
	}

	if _, err := service.RefreshUpstream(context.Background(), "index-first-1"); err != nil {
		t.Fatalf("refresh upstream for one credential: %v", err)
	}
	_, fetches, _ = mock.observed()
	if len(fetches) != 2 || fetches[1] != `{"auth_index":"index-first-1"}` {
		t.Fatalf("quota fetch bodies = %#v", fetches)
	}
}

func TestServiceRequiresCPAConnection(t *testing.T) {
	service, _ := newPluginQuotaTestService(t, "")
	if service.Configured(context.Background()) {
		t.Fatal("service reported a configured CPA connection")
	}
	if _, err := service.Providers(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("providers error = %v", err)
	}
	if _, err := service.CredentialQuota(context.Background(), "index-1"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("credential quota error = %v", err)
	}
	if _, err := service.Refresh(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("refresh error = %v", err)
	}
}

func TestCatalogueIsReusedWithinTTL(t *testing.T) {
	mock := newPluginQuotaCPAMock(t)
	service, _ := newPluginQuotaTestService(t, mock.server.URL)

	if _, err := service.Providers(context.Background()); err != nil {
		t.Fatalf("providers: %v", err)
	}
	if _, err := service.CredentialQuota(context.Background(), "index-first-1"); err != nil {
		t.Fatalf("credential quota: %v", err)
	}
	// A second read within the TTL must not re-list providers.
	if _, err := service.Providers(context.Background()); err != nil {
		t.Fatalf("providers: %v", err)
	}
	_, _, authFileHits := mock.observed()
	if authFileHits == 0 {
		t.Fatal("credential lookup did not read CPA auth files")
	}
}
