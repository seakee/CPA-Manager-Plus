// Package pluginquota keeps CPAMP's quota view of CPA plugin providers current.
//
// CPA exposes plugin quota generically: every plugin that implements quota.*
// publishes a provider on /v0/management/quota/providers, binds its credentials
// through the quota_provider reported on /v0/management/auth-files, and answers
// per-credential readings on /v0/management/plugins/:id/quota. This service
// consumes that contract, maps each reading into the quota snapshot model and
// asks CPA for an upstream refresh only when a caller explicitly requests one.
//
// Nothing here is provider specific: the provider catalogue, the credential
// binding and the item keys all come from CPA at runtime.
package pluginquota

import (
	"context"
	"errors"

	"log"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	pluginquotaclient "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/pluginquota"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	quotasnapshotsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/quotasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	// DefaultCatalogueTTL bounds how long a provider catalogue answer is reused
	// so a panel refresh does not re-list providers for every credential.
	DefaultCatalogueTTL   = 60 * time.Second
	defaultRequestTimeout = 30 * time.Second
)

var (
	ErrNotConfigured     = errors.New("usage service is not configured")
	ErrRefreshActive     = errors.New("plugin quota refresh is already running")
	ErrCredentialMissing = errors.New("plugin quota credential not found")
	ErrNoQuotaProvider   = errors.New("credential has no plugin quota provider")
)

// Item is one labelled reading as the panel receives it. Value keeps the
// plugin's own JSON shape, so the panel can render numbers, booleans and
// strings without the server inventing a unit.
type Item struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Value    any    `json:"value"`
	Unit     string `json:"unit,omitempty"`
	Format   string `json:"format,omitempty"`
	Currency string `json:"currency,omitempty"`
}

// CredentialQuota is the quota one credential's plugin reported.
type CredentialQuota struct {
	AuthIndex     string `json:"auth_index"`
	AuthFileName  string `json:"auth_file_name"`
	Provider      string `json:"provider"`
	PluginID      string `json:"plugin_id"`
	DisplayName   string `json:"display_name"`
	SupportsReset bool   `json:"supports_reset"`
	ObservedAtMS  int64  `json:"observed_at_ms"`
	Items         []Item `json:"items"`
}

// ProviderState is one catalogue entry plus what the last refresh achieved for
// the credentials it serves.
type ProviderState struct {
	pluginquotaclient.Provider
	CredentialCount int `json:"credential_count"`
	FailedCount     int `json:"failed_count"`
}

// CredentialState is one credential the last refresh attempted.
type CredentialState struct {
	AuthFileName string `json:"auth_file_name"`
	AuthIndex    string `json:"auth_index"`
	Provider     string `json:"provider"`
	ItemCount    int    `json:"item_count"`
	ObservedAtMS int64  `json:"observed_at_ms"`
	Error        string `json:"error,omitempty"`
}

// RefreshResult summarizes one catalogue and credential refresh pass.
type RefreshResult struct {
	ObservedAtMS int64             `json:"observed_at_ms"`
	Providers    []ProviderState   `json:"providers"`
	Credentials  []CredentialState `json:"credentials"`
}

func (r RefreshResult) FailedCount() int {
	failed := 0
	for _, credential := range r.Credentials {
		if credential.Error != "" {
			failed++
		}
	}
	return failed
}

type Service struct {
	managerConfigService *managerconfig.Service
	quotaSnapshots       *quotasnapshotsvc.Service
	authFiles            *cpaauthfiles.Client
	client               *pluginquotaclient.Client
	now                  func() time.Time
	catalogueTTL         time.Duration

	mu               sync.Mutex
	refreshing       bool
	catalogue        []pluginquotaclient.Provider
	catalogueAtMS    int64
	lastRefresh      RefreshResult
	lastRefreshError string
}

type ServiceOptions struct {
	CatalogueTTL time.Duration
	Now          func() time.Time
}

func New(
	managerConfigService *managerconfig.Service,
	quotaSnapshots *quotasnapshotsvc.Service,
	options ServiceOptions,
	clients ...*pluginquotaclient.Client,
) *Service {
	client := pluginquotaclient.New(nil, defaultRequestTimeout)
	if len(clients) > 0 && clients[0] != nil {
		client = clients[0]
	}
	catalogueTTL := options.CatalogueTTL
	if catalogueTTL <= 0 {
		catalogueTTL = DefaultCatalogueTTL
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		managerConfigService: managerConfigService,
		quotaSnapshots:       quotaSnapshots,
		authFiles:            cpaauthfiles.New(nil, defaultRequestTimeout),
		client:               client,
		now:                  now,
		catalogueTTL:         catalogueTTL,
	}
}

// Configured reports whether the CPA connection is available.
func (s *Service) Configured(ctx context.Context) bool {
	_, configured, err := s.resolveSetup(ctx)
	return err == nil && configured
}

// Providers returns the plugin quota provider catalogue CPA currently serves.
// The catalogue is also pushed into the snapshot service, which is what allows
// a plugin provider to be written and queried without becoming a built-in.
func (s *Service) Providers(ctx context.Context) ([]pluginquotaclient.Provider, error) {
	setup, configured, err := s.resolveSetup(ctx)
	if err != nil {
		return nil, err
	}
	if !configured {
		return nil, ErrNotConfigured
	}
	return s.catalogueFor(ctx, setup, true)
}

// CredentialQuota fetches, persists and returns the quota one credential's
// plugin reports. The credential is located by CPA's auth index, which is the
// same credential locator the panel shows on the auth file card.
func (s *Service) CredentialQuota(ctx context.Context, authIndex string) (CredentialQuota, error) {
	index := strings.TrimSpace(authIndex)
	if index == "" {
		return CredentialQuota{}, pluginquotaclient.ErrAuthIndexRequired
	}
	setup, configured, err := s.resolveSetup(ctx)
	if err != nil {
		return CredentialQuota{}, err
	}
	if !configured {
		return CredentialQuota{}, ErrNotConfigured
	}
	providers, err := s.catalogueFor(ctx, setup, false)
	if err != nil {
		return CredentialQuota{}, err
	}
	file, found, err := s.authFiles.Find(ctx, setup.CPAUpstreamURL, setup.ManagementKey, "", index)
	if err != nil {
		return CredentialQuota{}, err
	}
	if !found {
		return CredentialQuota{}, ErrCredentialMissing
	}
	provider, ok := matchProvider(file, providers)
	if !ok {
		return CredentialQuota{}, ErrNoQuotaProvider
	}
	items, err := s.client.CredentialQuota(
		ctx, setup.CPAUpstreamURL, setup.ManagementKey, provider.PluginLocator(), index,
	)
	if err != nil {
		return CredentialQuota{}, err
	}
	observedAtMS := s.now().UnixMilli()
	s.persist(ctx, provider, file, items, observedAtMS)
	return buildCredentialQuota(provider, file, items, observedAtMS), nil
}

// Refresh reads every credential that belongs to a plugin quota provider and
// persists what each plugin reports. It never triggers an upstream refresh, so
// a periodic pass stays cheap and cannot hammer a provider.
func (s *Service) Refresh(ctx context.Context) (RefreshResult, error) {
	setup, configured, err := s.resolveSetup(ctx)
	if err != nil {
		return RefreshResult{}, err
	}
	if !configured {
		return RefreshResult{}, ErrNotConfigured
	}
	if !s.beginRefresh() {
		return RefreshResult{}, ErrRefreshActive
	}
	defer s.endRefresh()

	providers, err := s.catalogueFor(ctx, setup, true)
	if err != nil {
		return RefreshResult{}, err
	}
	result := RefreshResult{ObservedAtMS: s.now().UnixMilli()}
	// Track provider rows by index: appending to the slice can reallocate it,
	// which would invalidate pointers taken before the pass.
	providerIndex := make(map[string]int, len(providers))
	for _, provider := range providers {
		providerIndex[provider.Provider] = len(result.Providers)
		result.Providers = append(result.Providers, ProviderState{Provider: provider})
	}
	files, err := s.authFiles.Fetch(ctx, setup.CPAUpstreamURL, setup.ManagementKey)
	if err != nil {
		return RefreshResult{}, err
	}
	for _, file := range files {
		provider, ok := matchProvider(file, providers)
		if !ok {
			continue
		}
		index := strings.TrimSpace(file.AuthIndex)
		if index == "" {
			continue
		}
		state := CredentialState{
			AuthFileName: file.Name,
			AuthIndex:    index,
			Provider:     provider.Provider,
		}
		items, fetchErr := s.client.CredentialQuota(
			ctx, setup.CPAUpstreamURL, setup.ManagementKey, provider.PluginLocator(), index,
		)
		if fetchErr != nil {
			state.Error = fetchErr.Error()
		} else {
			state.ItemCount = len(items)
			state.ObservedAtMS = s.now().UnixMilli()
			s.persist(ctx, provider, file, items, state.ObservedAtMS)
		}
		result.Credentials = append(result.Credentials, state)
		if index, ok := providerIndex[provider.Provider]; ok {
			result.Providers[index].CredentialCount++
			if state.Error != "" {
				result.Providers[index].FailedCount++
			}
		}
		if ctx.Err() != nil {
			break
		}
	}
	s.mu.Lock()
	s.lastRefresh = result
	s.lastRefreshError = ""
	s.mu.Unlock()
	return result, nil
}

// RefreshUpstream asks CPA to refresh every plugin's cached quota and then
// reads the credentials again. It is the explicit refresh action the panel
// triggers, and the only path that forces an upstream provider round trip.
func (s *Service) RefreshUpstream(ctx context.Context, authIndex string) (RefreshResult, error) {
	setup, configured, err := s.resolveSetup(ctx)
	if err != nil {
		return RefreshResult{}, err
	}
	if !configured {
		return RefreshResult{}, ErrNotConfigured
	}
	if err := s.client.FetchAll(ctx, setup.CPAUpstreamURL, setup.ManagementKey, authIndex); err != nil {
		return RefreshResult{}, err
	}
	s.invalidateCatalogue()
	return s.Refresh(ctx)
}

// Snapshot returns the last refresh outcome without performing any work.
func (s *Service) Snapshot() RefreshResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRefresh
}

func (s *Service) resolveSetup(ctx context.Context) (store.Setup, bool, error) {
	if s.managerConfigService == nil {
		return store.Setup{}, false, nil
	}
	managerCfg, _, ok, err := s.managerConfigService.ResolveManagerConfigWithSource(ctx)
	if err != nil {
		return store.Setup{}, false, err
	}
	if !ok || strings.TrimSpace(managerCfg.CPAConnection.CPABaseURL) == "" ||
		strings.TrimSpace(managerCfg.CPAConnection.ManagementKey) == "" {
		return store.Setup{}, false, nil
	}
	return managerconfig.SetupFromManagerConfig(managerCfg), true, nil
}

// catalogueFor returns the provider catalogue and keeps the snapshot service's
// accepted provider set in sync with it.
func (s *Service) catalogueFor(ctx context.Context, setup store.Setup, force bool) ([]pluginquotaclient.Provider, error) {
	s.mu.Lock()
	catalogue := s.catalogue
	atMS := s.catalogueAtMS
	s.mu.Unlock()
	if !force && len(catalogue) > 0 {
		if age := s.now().UnixMilli() - atMS; age >= 0 && age < s.catalogueTTL.Milliseconds() {
			s.registerProviders(catalogue)
			return catalogue, nil
		}
	}
	providers, err := s.client.Providers(ctx, setup.CPAUpstreamURL, setup.ManagementKey)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.catalogue = providers
	s.catalogueAtMS = s.now().UnixMilli()
	s.mu.Unlock()
	s.registerProviders(providers)
	return providers, nil
}

func (s *Service) invalidateCatalogue() {
	s.mu.Lock()
	s.catalogueAtMS = 0
	s.mu.Unlock()
}

func (s *Service) registerProviders(providers []pluginquotaclient.Provider) {
	if s.quotaSnapshots == nil {
		return
	}
	s.quotaSnapshots.SetPluginQuotaProviders(pluginquotaclient.ProviderNames(providers))
}

func (s *Service) persist(
	ctx context.Context,
	provider pluginquotaclient.Provider,
	file cpaauthfiles.File,
	items []pluginquotaclient.Item,
	observedAtMS int64,
) {
	if s.quotaSnapshots == nil {
		return
	}
	result := model.PluginQuotaResult{
		PluginID:        provider.PluginID,
		Provider:        provider.Provider,
		DisplayName:     provider.DisplayName,
		SupportsReset:   provider.SupportsReset,
		AuthFileName:    file.Name,
		AuthLabel:       rawString(file, "label"),
		AuthIndex:       file.AuthIndex,
		AccountSnapshot: file.AccountSnapshot,
		ObservedAtMS:    observedAtMS,
		Items:           make([]model.PluginQuotaItem, 0, len(items)),
	}
	for _, item := range items {
		modelItem := model.PluginQuotaItem{
			Key:      item.Key,
			Label:    item.Label,
			Value:    item.Value.Number,
			ValueRaw: item.Value.Text,
			Unit:     item.Unit,
			Format:   item.Format,
			Currency: item.Currency,
		}
		result.Items = append(result.Items, modelItem)
	}
	if err := s.quotaSnapshots.WritePluginQuotaResult(ctx, result); err != nil {
		log.Printf(
			"[plugin-quota] persist quota for auth file %q (provider %s): %v",
			file.Name,
			provider.Provider,
			err,
		)
	}
}

func (s *Service) beginRefresh() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refreshing {
		return false
	}
	s.refreshing = true
	return true
}

func (s *Service) endRefresh() {
	s.mu.Lock()
	s.refreshing = false
	s.mu.Unlock()
}

// matchProvider resolves which quota provider serves one credential. CPA's own
// quota_provider binding wins, because it is the authoritative answer when a
// credential provider is served by more than one quota provider.
func matchProvider(
	file cpaauthfiles.File,
	providers []pluginquotaclient.Provider,
) (pluginquotaclient.Provider, bool) {
	byName := make(map[string]pluginquotaclient.Provider, len(providers))
	for _, provider := range providers {
		byName[normalizeCredentialProvider(provider.Provider)] = provider
	}
	if declared := normalizeCredentialProvider(rawString(file, "quota_provider")); declared != "" {
		if provider, ok := byName[declared]; ok {
			return provider, true
		}
		return pluginquotaclient.Provider{}, false
	}
	byCredential := pluginquotaclient.SupportedCredentialProviders(providers)
	provider, ok := byCredential[normalizeCredentialProvider(file.Provider)]
	if !ok {
		return pluginquotaclient.Provider{}, false
	}
	for _, candidate := range providers {
		if candidate.Provider == provider {
			return candidate, true
		}
	}
	return pluginquotaclient.Provider{}, false
}

func buildCredentialQuota(
	provider pluginquotaclient.Provider,
	file cpaauthfiles.File,
	items []pluginquotaclient.Item,
	observedAtMS int64,
) CredentialQuota {
	result := CredentialQuota{
		AuthIndex:     strings.TrimSpace(file.AuthIndex),
		AuthFileName:  file.Name,
		Provider:      provider.Provider,
		PluginID:      provider.PluginID,
		DisplayName:   provider.DisplayName,
		SupportsReset: provider.SupportsReset,
		ObservedAtMS:  observedAtMS,
		Items:         make([]Item, 0, len(items)),
	}
	for _, item := range items {
		value := any(nil)
		switch {
		case item.Value.Number != nil:
			value = *item.Value.Number
		case item.Value.Text != "":
			value = item.Value.Text
		}
		result.Items = append(result.Items, Item{
			Key:      item.Key,
			Label:    item.Label,
			Value:    value,
			Unit:     item.Unit,
			Format:   item.Format,
			Currency: item.Currency,
		})
	}
	return result
}

func rawString(file cpaauthfiles.File, key string) string {
	if file.Raw == nil {
		return ""
	}
	value, ok := file.Raw[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func normalizeCredentialProvider(value string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", "-"))
}
