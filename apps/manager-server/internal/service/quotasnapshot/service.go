package quotasnapshot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/codexquota"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	quotasnapshotrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/quotasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	maxWriteEntries      = 400
	maxQueryAccounts     = 200
	maxModelIDs          = 200
	maxSnapshotsPerQuery = 2000
	// Codex fixed-window reset timestamps can move by a few seconds between
	// responses. Keep that transport jitter inside one canonical provider cycle.
	derivedFixedBoundaryJitterMS = int64(60 * 1000)
)

var (
	validProviders      = stringSet("codex", "claude", "antigravity", "kimi", "xai")
	validModes          = stringSet("fixed", "calendar", "rolling", "non_window", "unknown")
	validScopes         = stringSet("all", "family", "models", "product", "feature")
	validSources        = stringSet("api_query", "response_header", "response_body", "inspection")
	validAccuracy       = stringSet("exact", "derived", "estimated", "unknown")
	validInventoryModes = stringSet("complete", "partial", "delta")
	validRelationships  = stringSet("concurrent_subwindow")
	codexPersonalPlans  = stringSet("free", "plus", "pro", "go")
)

type Service struct {
	store *store.Store
	now   func() time.Time
}

func New(st *store.Store) *Service {
	return &Service{store: st, now: time.Now}
}

type AccountTarget struct {
	AccountSnapshot       string `json:"account_snapshot,omitempty"`
	AuthLabelSnapshot     string `json:"auth_label_snapshot,omitempty"`
	AuthFileSnapshot      string `json:"auth_file_snapshot,omitempty"`
	AuthProviderSnapshot  string `json:"auth_provider_snapshot,omitempty"`
	AuthAccountIDSnapshot string `json:"auth_account_id_snapshot,omitempty"`
	AuthProjectIDSnapshot string `json:"auth_project_id_snapshot,omitempty"`
	AuthIndex             string `json:"auth_index,omitempty"`
	Source                string `json:"source,omitempty"`
}

type ResetCredit struct {
	ID          string `json:"id"`
	ExpiresAtMS int64  `json:"expires_at_ms"`
}

type WindowInput struct {
	ProviderWindowID      string        `json:"provider_window_id"`
	ProviderWindowAliases []string      `json:"provider_window_aliases,omitempty"`
	WindowKind            string        `json:"window_kind"`
	WindowMode            string        `json:"window_mode"`
	ModelScopeKind        string        `json:"model_scope_kind"`
	ModelScopeKey         string        `json:"model_scope_key,omitempty"`
	ModelIDs              []string      `json:"model_ids,omitempty"`
	Source                string        `json:"source"`
	SourceObservationID   string        `json:"source_observation_id,omitempty"`
	ObservedAtMS          int64         `json:"observed_at_ms"`
	BoundaryAccuracy      string        `json:"boundary_accuracy"`
	CycleStartMS          *int64        `json:"cycle_start_ms,omitempty"`
	CycleEndMS            *int64        `json:"cycle_end_ms,omitempty"`
	DurationSeconds       *int64        `json:"duration_seconds,omitempty"`
	UsedPercent           *float64      `json:"used_percent,omitempty"`
	RemainingPercent      *float64      `json:"remaining_percent,omitempty"`
	UsedValue             *float64      `json:"used_value,omitempty"`
	LimitValue            *float64      `json:"limit_value,omitempty"`
	QuotaUnit             string        `json:"quota_unit,omitempty"`
	ResetCreditsAvailable *int64        `json:"reset_credits_available,omitempty"`
	ResetCredits          []ResetCredit `json:"reset_credits,omitempty"`
	PlanType              string        `json:"plan_type,omitempty"`
	RelationshipKind      string        `json:"relationship_kind,omitempty"`
	ContainerWindowID     string        `json:"container_provider_window_id,omitempty"`
}

type ObservationInput struct {
	Source              string `json:"source"`
	SourceObservationID string `json:"source_observation_id,omitempty"`
	ObservedAtMS        int64  `json:"observed_at_ms,omitempty"`
	InventoryScopeKey   string `json:"inventory_scope_key"`
	InventoryMode       string `json:"inventory_mode"`
}

type RemovedWindowInput struct {
	ProviderWindowID string   `json:"provider_window_id"`
	WindowKind       string   `json:"window_kind,omitempty"`
	ModelScopeKind   string   `json:"model_scope_kind,omitempty"`
	ModelScopeKey    string   `json:"model_scope_key,omitempty"`
	ModelIDs         []string `json:"model_ids,omitempty"`
}

type WriteEntry struct {
	RowKey         string               `json:"row_key,omitempty"`
	Provider       string               `json:"provider"`
	Account        AccountTarget        `json:"account"`
	Observation    *ObservationInput    `json:"observation,omitempty"`
	Windows        []WindowInput        `json:"windows"`
	RemovedWindows []RemovedWindowInput `json:"removed_windows,omitempty"`
}

type WriteRequest struct {
	Entries []WriteEntry `json:"entries"`
}

type WriteItem struct {
	RowKey        string `json:"row_key,omitempty"`
	AccountKey    string `json:"account_key"`
	Provider      string `json:"provider"`
	InsertedCount int    `json:"inserted_count"`
}

type WriteResponse struct {
	ObservedAtMS int64       `json:"observed_at_ms"`
	Items        []WriteItem `json:"items"`
}

type QueryAccount struct {
	RowKey   string        `json:"row_key"`
	Provider string        `json:"provider"`
	Account  AccountTarget `json:"account"`
}

type QueryRequest struct {
	Accounts        []QueryAccount `json:"accounts"`
	NowMS           int64          `json:"now_ms,omitempty"`
	IncludeInactive bool           `json:"include_inactive,omitempty"`
}

type FieldSource struct {
	Source       string `json:"source"`
	ObservedAtMS int64  `json:"observed_at_ms"`
}

type Window struct {
	ProviderWindowID      string                 `json:"provider_window_id"`
	ProviderWindowAliases []string               `json:"provider_window_aliases,omitempty"`
	WindowKind            string                 `json:"window_kind"`
	WindowMode            string                 `json:"window_mode"`
	ModelScopeKind        string                 `json:"model_scope_kind"`
	ModelScopeKey         string                 `json:"model_scope_key,omitempty"`
	ModelIDs              []string               `json:"model_ids,omitempty"`
	Source                string                 `json:"source"`
	SourceObservationID   string                 `json:"source_observation_id,omitempty"`
	ObservedAtMS          int64                  `json:"observed_at_ms"`
	BoundaryAccuracy      string                 `json:"boundary_accuracy"`
	CycleStartMS          *int64                 `json:"cycle_start_ms,omitempty"`
	CycleEndMS            *int64                 `json:"cycle_end_ms,omitempty"`
	DurationSeconds       *int64                 `json:"duration_seconds,omitempty"`
	UsedPercent           *float64               `json:"used_percent,omitempty"`
	RemainingPercent      *float64               `json:"remaining_percent,omitempty"`
	UsedValue             *float64               `json:"used_value,omitempty"`
	LimitValue            *float64               `json:"limit_value,omitempty"`
	QuotaUnit             string                 `json:"quota_unit,omitempty"`
	ResetCreditsAvailable *int64                 `json:"reset_credits_available,omitempty"`
	ResetCredits          []ResetCredit          `json:"reset_credits,omitempty"`
	PlanType              string                 `json:"plan_type,omitempty"`
	Stale                 bool                   `json:"stale"`
	FieldSources          map[string]FieldSource `json:"field_sources,omitempty"`
	LogicalWindowID       int64                  `json:"logical_window_id,omitempty"`
	ActivationGeneration  int                    `json:"activation_generation,omitempty"`
	Availability          string                 `json:"availability,omitempty"`
	RelationshipKind      string                 `json:"relationship_kind,omitempty"`
	ContainerWindowID     string                 `json:"container_provider_window_id,omitempty"`
	FirstSeenAtMS         int64                  `json:"first_seen_at_ms,omitempty"`
	LastSeenAtMS          int64                  `json:"last_seen_at_ms,omitempty"`
	MissingSinceMS        *int64                 `json:"missing_since_ms,omitempty"`
	DeactivatedAtMS       *int64                 `json:"deactivated_at_ms,omitempty"`
	CurrentCycle          *Cycle                 `json:"current_cycle,omitempty"`
	PreviousCycle         *Cycle                 `json:"previous_cycle,omitempty"`
	ScopeFingerprint      string                 `json:"-"`
}

type Cycle struct {
	ID               int64  `json:"id"`
	ActivationID     int64  `json:"activation_id"`
	State            string `json:"state"`
	ScheduledStartMS *int64 `json:"scheduled_start_ms,omitempty"`
	ScheduledEndMS   *int64 `json:"scheduled_end_ms,omitempty"`
	ActualStartMS    int64  `json:"actual_start_ms"`
	ActualEndMS      *int64 `json:"actual_end_ms,omitempty"`
	DurationSeconds  *int64 `json:"duration_seconds,omitempty"`
	BoundaryAccuracy string `json:"boundary_accuracy"`
	EndReason        string `json:"end_reason,omitempty"`
	ParentCycleID    *int64 `json:"parent_cycle_id,omitempty"`
	ForecastEligible bool   `json:"forecast_eligible"`
}

type QueryItem struct {
	RowKey     string   `json:"row_key"`
	AccountKey string   `json:"account_key"`
	Provider   string   `json:"provider"`
	Windows    []Window `json:"windows"`
}

type QueryResponse struct {
	GeneratedAtMS int64       `json:"generated_at_ms"`
	Items         []QueryItem `json:"items"`
}

func (s *Service) Write(ctx context.Context, req WriteRequest) (WriteResponse, error) {
	if len(req.Entries) == 0 {
		return WriteResponse{}, errors.New("entries are required")
	}
	if len(req.Entries) > maxWriteEntries {
		return WriteResponse{}, fmt.Errorf("entries must be less than or equal to %d", maxWriteEntries)
	}
	nowMS := s.now().UnixMilli()
	writes := make([]model.AccountQuotaObservationWrite, 0, len(req.Entries))
	totalMutations := 0
	items := make([]WriteItem, 0, len(req.Entries))
	for _, entry := range req.Entries {
		entryMutations := len(entry.Windows) + len(entry.RemovedWindows)
		if entryMutations > maxWriteEntries {
			return WriteResponse{}, fmt.Errorf("window mutations must be less than or equal to %d", maxWriteEntries)
		}
		provider := normalizeProvider(entry.Provider)
		if !validProviders[provider] {
			return WriteResponse{}, fmt.Errorf("unsupported provider %q", entry.Provider)
		}
		accountKey, ok := usageidentity.AccountKey(entry.Account.identityFields(provider))
		if !ok {
			return WriteResponse{}, errors.New("account identity is required")
		}
		observation, err := normalizeObservationInput(accountKey, provider, entry, nowMS)
		if err != nil {
			return WriteResponse{}, err
		}
		if len(entry.Windows) == 0 && observation.InventoryMode == "partial" && len(entry.RemovedWindows) == 0 {
			return WriteResponse{}, errors.New("windows are required for partial observations")
		}
		write := model.AccountQuotaObservationWrite{Observation: observation, ResponseIndex: len(writes)}
		for windowIndex, input := range entry.Windows {
			snapshot, err := normalizeWindowInput(accountKey, provider, input, nowMS)
			if err != nil {
				return WriteResponse{}, err
			}
			if snapshot.Source != observation.Source {
				return WriteResponse{}, fmt.Errorf("windows[%d].source must match observation source", windowIndex)
			}
			if snapshot.ObservedAtMS != observation.ObservedAtMS {
				return WriteResponse{}, fmt.Errorf("windows[%d].observed_at_ms must match observation observed_at_ms", windowIndex)
			}
			if snapshot.SourceObservationID == "" {
				snapshot.SourceObservationID = observation.SourceObservationID
			} else if snapshot.SourceObservationID != observation.SourceObservationID {
				return WriteResponse{}, fmt.Errorf("windows[%d].source_observation_id must match observation source_observation_id", windowIndex)
			}
			snapshot.InventoryScopeKey = observation.InventoryScopeKey
			write.Snapshots = append(write.Snapshots, snapshot)
		}
		for _, input := range entry.RemovedWindows {
			removed, err := normalizeRemovedWindow(provider, input)
			if err != nil {
				return WriteResponse{}, err
			}
			write.Removed = append(write.Removed, removed)
		}
		if err := validateWindowMutations(observation, write.Snapshots, write.Removed); err != nil {
			return WriteResponse{}, err
		}
		write.Observation.WindowCount = len(write.Snapshots)
		writes = append(writes, write)
		totalMutations += len(write.Snapshots) + len(write.Removed)
		items = append(items, WriteItem{
			RowKey: entry.RowKey, AccountKey: accountKey, Provider: provider,
		})
	}
	if totalMutations > maxWriteEntries {
		return WriteResponse{}, fmt.Errorf("window mutations must be less than or equal to %d", maxWriteEntries)
	}
	if err := s.stabilizeNewDerivedFixedBoundaries(ctx, writes); err != nil {
		return WriteResponse{}, err
	}
	for writeIndex := range writes {
		for snapshotIndex := range writes[writeIndex].Snapshots {
			snapshot := &writes[writeIndex].Snapshots[snapshotIndex]
			snapshot.ContentHash = snapshotContentHash(*snapshot)
		}
		writes[writeIndex].Observation.ObservationHash = observationHash(writes[writeIndex])
	}
	if err := s.store.QuotaSnapshots.InsertObservationWrites(ctx, writes); err != nil {
		return WriteResponse{}, err
	}
	for _, write := range writes {
		if write.ResponseIndex >= 0 && write.ResponseIndex < len(items) {
			items[write.ResponseIndex].InsertedCount = write.InsertedSnapshotCount
		}
	}
	return WriteResponse{ObservedAtMS: nowMS, Items: items}, nil
}

func normalizeObservationInput(accountKey, provider string, entry WriteEntry, nowMS int64) (model.AccountQuotaObservation, error) {
	input := entry.Observation
	if input == nil {
		input = &ObservationInput{
			InventoryScopeKey: quotasnapshotrepo.InventoryScopeKey(provider),
			InventoryMode:     "partial",
		}
		if len(entry.Windows) > 0 {
			input.Source = entry.Windows[0].Source
			input.SourceObservationID = entry.Windows[0].SourceObservationID
			input.ObservedAtMS = entry.Windows[0].ObservedAtMS
		}
	}
	source := strings.ToLower(strings.TrimSpace(input.Source))
	if !validSources[source] {
		return model.AccountQuotaObservation{}, fmt.Errorf("unsupported observation source %q", input.Source)
	}
	mode := strings.ToLower(strings.TrimSpace(input.InventoryMode))
	if !validInventoryModes[mode] {
		return model.AccountQuotaObservation{}, fmt.Errorf("unsupported inventory_mode %q", input.InventoryMode)
	}
	scopeKey := strings.TrimSpace(input.InventoryScopeKey)
	if scopeKey == "" {
		return model.AccountQuotaObservation{}, errors.New("inventory_scope_key is required")
	}
	observedAtMS := input.ObservedAtMS
	if observedAtMS <= 0 {
		observedAtMS = nowMS
	}
	if observedAtMS > observationFutureLimitMS(nowMS) {
		return model.AccountQuotaObservation{}, errors.New("observation observed_at_ms is too far in the future")
	}
	sourceObservationID, err := normalizeObservationID(input.SourceObservationID)
	if err != nil {
		return model.AccountQuotaObservation{}, err
	}
	return model.AccountQuotaObservation{
		AccountKey: accountKey, Provider: provider, Source: source,
		SourceObservationID: sourceObservationID,
		InventoryScopeKey:   scopeKey, InventoryMode: mode,
		ObservedAtMS: observedAtMS, CreatedAtMS: nowMS,
	}, nil
}

func normalizeRemovedWindow(provider string, input RemovedWindowInput) (model.AccountQuotaWindowRemoval, error) {
	providerWindowID := strings.TrimSpace(input.ProviderWindowID)
	if providerWindowID == "" {
		return model.AccountQuotaWindowRemoval{}, errors.New("removed provider_window_id is required")
	}
	windowKind := strings.TrimSpace(input.WindowKind)
	if provider == "codex" {
		if strings.EqualFold(providerWindowID, "secondary") && windowKind == "" {
			return model.AccountQuotaWindowRemoval{}, errors.New("removed window_kind is required for Codex secondary windows")
		}
		providerWindowID = codexquota.CanonicalProviderWindowID(providerWindowID, windowKind)
	}
	scopeKind := strings.ToLower(strings.TrimSpace(input.ModelScopeKind))
	if scopeKind == "" {
		scopeKind = "all"
	}
	if !validScopes[scopeKind] {
		return model.AccountQuotaWindowRemoval{}, fmt.Errorf("unsupported removed model_scope_kind %q", input.ModelScopeKind)
	}
	modelIDs, err := normalizeStringList(input.ModelIDs, maxModelIDs)
	if err != nil {
		return model.AccountQuotaWindowRemoval{}, err
	}
	if scopeKind == "models" && len(modelIDs) == 0 {
		return model.AccountQuotaWindowRemoval{}, errors.New("removed model_ids are required for models scope")
	}
	modelIDsJSON := marshalAllowlist(modelIDs)
	scopeKey := strings.TrimSpace(input.ModelScopeKey)
	if provider == "codex" {
		scope := codexquota.ResolveProviderWindowScope(
			providerWindowID,
			windowKind,
			codexquota.ScopeFromFields(scopeKind, scopeKey, modelIDs),
		)
		providerWindowID = codexquota.CanonicalProviderWindowIDForScope(providerWindowID, windowKind, scope)
		scopeKind, scopeKey, modelIDs = scope.Kind, scope.Key, scope.Models
		modelIDsJSON = marshalAllowlist(modelIDs)
	}
	return model.AccountQuotaWindowRemoval{
		ProviderWindowID: providerWindowID,
		ModelScopeKind:   scopeKind,
		ModelScopeKey:    scopeKey,
		ModelIDsJSON:     modelIDsJSON,
		ScopeFingerprint: quotasnapshotrepo.ScopeFingerprint(scopeKind, scopeKey, modelIDs),
	}, nil
}

func validateWindowMutations(
	observation model.AccountQuotaObservation,
	snapshots []model.AccountQuotaSnapshot,
	removed []model.AccountQuotaWindowRemoval,
) error {
	if len(removed) > 0 && observation.InventoryMode != "delta" {
		return errors.New("removed_windows require delta inventory_mode")
	}
	reported := make(map[string]int, len(snapshots))
	for index, snapshot := range snapshots {
		identity := snapshot.ProviderWindowID + "\x00" + snapshot.ScopeFingerprint
		if previous, ok := reported[identity]; ok {
			return fmt.Errorf("windows[%d] duplicates windows[%d]", index, previous)
		}
		reported[identity] = index
	}
	removedByIdentity := make(map[string]int, len(removed))
	for index, item := range removed {
		identity := item.ProviderWindowID + "\x00" + item.ScopeFingerprint
		if previous, ok := removedByIdentity[identity]; ok {
			return fmt.Errorf("removed_windows[%d] duplicates removed_windows[%d]", index, previous)
		}
		removedByIdentity[identity] = index
		if reportedIndex, ok := reported[identity]; ok {
			return fmt.Errorf("removed_windows[%d] conflicts with windows[%d]", index, reportedIndex)
		}
	}
	return nil
}

type snapshotAccountProviderKey struct {
	accountKey string
	provider   string
}

type snapshotWriteLocation struct {
	writeIndex    int
	snapshotIndex int
}

func (s *Service) stabilizeNewDerivedFixedBoundaries(
	ctx context.Context,
	writes []model.AccountQuotaObservationWrite,
) error {
	grouped := make(map[snapshotAccountProviderKey][]snapshotWriteLocation)
	for writeIndex := range writes {
		for snapshotIndex := range writes[writeIndex].Snapshots {
			snapshot := writes[writeIndex].Snapshots[snapshotIndex]
			if !isDerivedResponseHeaderFixedBoundary(snapshot) {
				continue
			}
			key := snapshotAccountProviderKey{
				accountKey: snapshot.AccountKey,
				provider:   snapshot.Provider,
			}
			grouped[key] = append(grouped[key], snapshotWriteLocation{
				writeIndex:    writeIndex,
				snapshotIndex: snapshotIndex,
			})
		}
	}
	for key, locations := range grouped {
		candidates, err := s.store.QuotaSnapshots.ListCandidates(
			ctx,
			key.accountKey,
			key.provider,
			maxSnapshotsPerQuery,
		)
		if err != nil {
			return err
		}
		sort.SliceStable(locations, func(i, j int) bool {
			leftLocation := locations[i]
			rightLocation := locations[j]
			left := writes[leftLocation.writeIndex].Snapshots[leftLocation.snapshotIndex].ObservedAtMS
			right := writes[rightLocation.writeIndex].Snapshots[rightLocation.snapshotIndex].ObservedAtMS
			if left != right {
				return left < right
			}
			if leftLocation.writeIndex != rightLocation.writeIndex {
				return leftLocation.writeIndex < rightLocation.writeIndex
			}
			return leftLocation.snapshotIndex < rightLocation.snapshotIndex
		})
		for _, location := range locations {
			snapshot := &writes[location.writeIndex].Snapshots[location.snapshotIndex]
			if boundary, ok := selectStableHeaderBoundary(*snapshot, candidates); ok {
				applySnapshotBoundary(snapshot, boundary)
			}
			candidates = append(candidates, *snapshot)
		}
	}
	return nil
}

func (s *Service) Query(ctx context.Context, req QueryRequest) (QueryResponse, error) {
	if len(req.Accounts) == 0 {
		return QueryResponse{}, errors.New("accounts are required")
	}
	if len(req.Accounts) > maxQueryAccounts {
		return QueryResponse{}, fmt.Errorf("accounts must be less than or equal to %d", maxQueryAccounts)
	}
	nowMS := req.NowMS
	if nowMS <= 0 {
		nowMS = s.now().UnixMilli()
	}
	items := make([]QueryItem, 0, len(req.Accounts))
	for _, account := range req.Accounts {
		if strings.TrimSpace(account.RowKey) == "" {
			return QueryResponse{}, errors.New("row_key is required")
		}
		provider := normalizeProvider(account.Provider)
		if !validProviders[provider] {
			return QueryResponse{}, fmt.Errorf("unsupported provider %q", account.Provider)
		}
		accountKey, ok := usageidentity.AccountKey(account.Account.identityFields(provider))
		if !ok {
			return QueryResponse{}, errors.New("account identity is required")
		}
		candidates, err := s.store.QuotaSnapshots.ListCandidates(ctx, accountKey, provider, maxSnapshotsPerQuery)
		if err != nil {
			return QueryResponse{}, err
		}
		states, err := s.store.QuotaSnapshots.ListWindowStates(ctx, accountKey, provider)
		if err != nil {
			return QueryResponse{}, err
		}
		if legacyAccountKey, bridgeEligible := legacyCodexWorkspaceQuotaKey(account.Account, provider); bridgeEligible {
			legacyStates, legacyErr := s.store.QuotaSnapshots.ListLegacyCodexWorkspaceWindowStates(ctx, legacyAccountKey, provider)
			if legacyErr != nil {
				return QueryResponse{}, legacyErr
			}
			states, legacyErr = bridgeLegacyCodexWorkspacePreviousCycles(ctx, s.store.QuotaSnapshots, states, legacyStates)
			if legacyErr != nil {
				return QueryResponse{}, legacyErr
			}
		}
		stateByID := make(map[int64]model.AccountQuotaWindowState, len(states))
		for _, state := range states {
			stateByID[state.ID] = state
		}
		explicitScopedCandidates := make(map[string]struct{})
		for _, candidate := range candidates {
			state, hasState := stateByID[candidate.LogicalWindowID]
			if hasState && state.Availability == "inactive" {
				continue
			}
			scope := codexquota.ScopeFromFields(
				candidate.ModelScopeKind,
				candidate.ModelScopeKey,
				unmarshalStringList(candidate.ModelIDsJSON),
			)
			if provider == "codex" && scope.Kind != "all" {
				explicitScopedCandidates[codexQueryScopeKey(candidate.ProviderWindowID, candidate.WindowKind, scope)] = struct{}{}
			}
		}
		normalizedCandidates := candidates[:0]
		for index := range candidates {
			state, hasState := stateByID[candidates[index].LogicalWindowID]
			if hasState && state.Availability == "inactive" {
				normalizedCandidates = append(normalizedCandidates, candidates[index])
				continue
			}
			wasLegacyScoped := normalizeCodexSnapshotForQuery(&candidates[index])
			if wasLegacyScoped {
				scope := codexquota.ScopeFromFields(
					candidates[index].ModelScopeKind,
					candidates[index].ModelScopeKey,
					unmarshalStringList(candidates[index].ModelIDsJSON),
				)
				if _, exists := explicitScopedCandidates[codexQueryScopeKey(candidates[index].ProviderWindowID, candidates[index].WindowKind, scope)]; exists {
					continue
				}
			}
			normalizedCandidates = append(normalizedCandidates, candidates[index])
		}
		candidates = normalizedCandidates
		explicitScopedStates := make(map[string]struct{})
		for _, state := range states {
			if state.Availability == "inactive" {
				continue
			}
			scope := codexquota.ScopeFromFields(state.ModelScopeKind, state.ModelScopeKey, unmarshalStringList(state.ModelIDsJSON))
			if provider == "codex" && scope.Kind != "all" {
				explicitScopedStates[codexQueryScopeKey(state.ProviderWindowID, state.WindowKind, scope)] = struct{}{}
			}
		}
		normalizedStates := states[:0]
		for index := range states {
			if states[index].Availability == "inactive" {
				normalizedStates = append(normalizedStates, states[index])
				continue
			}
			wasLegacyScoped := normalizeCodexWindowStateForQuery(&states[index])
			if wasLegacyScoped {
				scope := codexquota.ScopeFromFields(states[index].ModelScopeKind, states[index].ModelScopeKey, unmarshalStringList(states[index].ModelIDsJSON))
				if _, exists := explicitScopedStates[codexQueryScopeKey(states[index].ProviderWindowID, states[index].WindowKind, scope)]; exists {
					continue
				}
			}
			normalizedStates = append(normalizedStates, states[index])
		}
		states = normalizedStates
		items = append(items, QueryItem{
			RowKey: account.RowKey, AccountKey: accountKey, Provider: provider,
			Windows: mergeLifecycleWindows(selectWindows(candidates, states, nowMS), states, nowMS, req.IncludeInactive),
		})
	}
	return QueryResponse{GeneratedAtMS: nowMS, Items: items}, nil
}

type legacyCodexPreviousCycleBridgeCandidate struct {
	stateIndex int
	cycleIDs   [3]int64
	previous   model.AccountQuotaCycle
}

func legacyCodexWorkspaceQuotaKey(account AccountTarget, provider string) (string, bool) {
	if normalizeProvider(provider) != "codex" || strings.Trim(account.AuthAccountIDSnapshot, " ") == "" {
		return "", false
	}
	fields := account.identityFields(provider)
	workspace, workspaceOK := usageidentity.ResolveCodexWorkspace(fields)
	_, memberOK := usageidentity.NormalizeCodexMemberSnapshot(fields.AccountSnapshot)
	if !workspaceOK || !memberOK {
		return "", false
	}
	return usageidentity.LegacyCodexWorkspaceAccountKey(provider, workspace)
}

func bridgeLegacyCodexWorkspacePreviousCycles(
	ctx context.Context,
	repository quotasnapshotrepo.Repository,
	states, legacyStates []model.AccountQuotaWindowState,
) ([]model.AccountQuotaWindowState, error) {
	if len(states) == 0 || len(legacyStates) == 0 {
		return states, nil
	}
	legacyByWindow := make(map[string][]model.AccountQuotaWindowState, len(legacyStates))
	for _, legacyState := range legacyStates {
		normalized := legacyState
		normalizeCodexWindowStateForQuery(&normalized)
		key := legacyCodexQuotaBridgeWindowKey(normalized)
		legacyByWindow[key] = append(legacyByWindow[key], normalized)
	}

	candidates := make([]legacyCodexPreviousCycleBridgeCandidate, 0)
	cycleIDs := make([]int64, 0)
	for stateIndex := range states {
		if states[stateIndex].PreviousCycle != nil {
			continue
		}
		current := states[stateIndex]
		normalizeCodexWindowStateForQuery(&current)
		matches := make([]model.AccountQuotaWindowState, 0, 1)
		for _, legacyState := range legacyByWindow[legacyCodexQuotaBridgeWindowKey(current)] {
			if legacyCodexQuotaBridgeCyclesMatch(current, legacyState) {
				matches = append(matches, legacyState)
			}
		}
		if len(matches) != 1 {
			continue
		}
		legacyState := matches[0]
		candidate := legacyCodexPreviousCycleBridgeCandidate{
			stateIndex: stateIndex,
			cycleIDs: [3]int64{
				current.CurrentCycle.ID,
				legacyState.CurrentCycle.ID,
				legacyState.PreviousCycle.ID,
			},
			previous: *legacyState.PreviousCycle,
		}
		candidates = append(candidates, candidate)
		cycleIDs = append(cycleIDs, candidate.cycleIDs[:]...)
	}
	if len(candidates) == 0 {
		return states, nil
	}
	evidenceByCycle, err := repository.ListCyclePlanEvidence(ctx, cycleIDs)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if !legacyCodexQuotaBridgeHasPersonalPlan(evidenceByCycle, candidate.cycleIDs) {
			continue
		}
		previous := candidate.previous
		states[candidate.stateIndex].PreviousCycle = &previous
	}
	return states, nil
}

func legacyCodexQuotaBridgeWindowKey(state model.AccountQuotaWindowState) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(state.Provider)),
		state.ProviderWindowID,
		state.WindowKind,
		state.WindowMode,
		state.ModelScopeKind,
		state.ModelScopeKey,
		state.ModelIDsJSON,
		state.ScopeFingerprint,
		state.InventoryScopeKey,
		state.RelationshipKind,
		state.ContainerProviderWindowID,
	}, "\x00")
}

func legacyCodexQuotaBridgeCyclesMatch(
	current, legacy model.AccountQuotaWindowState,
) bool {
	if current.Availability != "active" || legacy.Availability != "active" ||
		(current.WindowMode != "fixed" && current.WindowMode != "calendar") ||
		current.CurrentCycle == nil || legacy.CurrentCycle == nil || legacy.PreviousCycle == nil {
		return false
	}
	currentCycle := current.CurrentCycle
	legacyCurrent := legacy.CurrentCycle
	legacyPrevious := legacy.PreviousCycle
	if currentCycle.State != "active" || legacyCurrent.State != "active" ||
		currentCycle.ActualEndMS != nil || legacyCurrent.ActualEndMS != nil ||
		!reliableQuotaCycleBoundary(currentCycle) ||
		!reliableQuotaCycleBoundary(legacyCurrent) ||
		!reliableQuotaCycleBoundary(legacyPrevious) ||
		currentCycle.ActualStartMS != legacyCurrent.ActualStartMS ||
		!equalOptionalInt64(currentCycle.ScheduledStartMS, legacyCurrent.ScheduledStartMS) ||
		!equalOptionalInt64(currentCycle.ScheduledEndMS, legacyCurrent.ScheduledEndMS) ||
		!equalOptionalInt64(currentCycle.DurationSeconds, legacyCurrent.DurationSeconds) ||
		currentCycle.DurationSeconds == nil || *currentCycle.DurationSeconds <= 0 {
		return false
	}
	if legacyPrevious.State != "closed" || legacyPrevious.ActualEndMS == nil ||
		legacyPrevious.ActualStartMS >= *legacyPrevious.ActualEndMS ||
		*legacyPrevious.ActualEndMS > legacyCurrent.ActualStartMS {
		return false
	}
	switch legacyPrevious.EndReason {
	case "scheduled", "provider_reset", "early_reset":
		return true
	default:
		return false
	}
}

func reliableQuotaCycleBoundary(cycle *model.AccountQuotaCycle) bool {
	return cycle != nil && (cycle.BoundaryAccuracy == "exact" || cycle.BoundaryAccuracy == "derived")
}

func equalOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func legacyCodexQuotaBridgeHasPersonalPlan(
	evidenceByCycle map[int64]quotasnapshotrepo.CyclePlanEvidence,
	cycleIDs [3]int64,
) bool {
	planType := ""
	for _, cycleID := range cycleIDs {
		evidence, ok := evidenceByCycle[cycleID]
		if !ok || evidence.SnapshotCount == 0 || evidence.MissingPlanCount != 0 || len(evidence.PlanTypes) != 1 {
			return false
		}
		candidate := evidence.PlanTypes[0]
		if _, personal := codexPersonalPlans[candidate]; !personal {
			return false
		}
		if planType != "" && candidate != planType {
			return false
		}
		planType = candidate
	}
	return planType != ""
}

// normalizeCodexSnapshotForQuery is the read-side compatibility shield for
// rows written before scoped Codex fields were available. It runs before
// grouping so an unattached legacy `all` Spark row cannot sit beside the new
// model-scoped row while deferred lifecycle migration is still pending.
func normalizeCodexSnapshotForQuery(snapshot *model.AccountQuotaSnapshot) bool {
	if snapshot == nil || normalizeProvider(snapshot.Provider) != "codex" {
		return false
	}
	modelIDs := unmarshalStringList(snapshot.ModelIDsJSON)
	providedScope := codexquota.ScopeFromFields(snapshot.ModelScopeKind, snapshot.ModelScopeKey, modelIDs)
	scope := codexquota.ResolveProviderWindowScope(
		snapshot.ProviderWindowID,
		snapshot.WindowKind,
		codexquota.ScopeFromFields(snapshot.ModelScopeKind, snapshot.ModelScopeKey, modelIDs),
	)
	snapshot.ProviderWindowID = codexquota.CanonicalProviderWindowIDForScope(
		snapshot.ProviderWindowID,
		snapshot.WindowKind,
		scope,
	)
	snapshot.ModelScopeKind = scope.Kind
	snapshot.ModelScopeKey = scope.Key
	snapshot.ModelIDsJSON = marshalAllowlist(scope.Models)
	snapshot.ScopeFingerprint = quotasnapshotrepo.ScopeFingerprint(scope.Kind, scope.Key, scope.Models)
	if snapshot.ContainerWindowID != "" {
		snapshot.ContainerWindowID = codexquota.CanonicalProviderWindowID(snapshot.ContainerWindowID)
	}
	return providedScope.Kind == "all" && scope.Kind != "all"
}

func normalizeCodexWindowStateForQuery(state *model.AccountQuotaWindowState) bool {
	if state == nil || normalizeProvider(state.Provider) != "codex" {
		return false
	}
	if state.Availability == "inactive" {
		return false
	}
	modelIDs := unmarshalStringList(state.ModelIDsJSON)
	providedScope := codexquota.ScopeFromFields(state.ModelScopeKind, state.ModelScopeKey, modelIDs)
	scope := codexquota.ResolveProviderWindowScope(
		state.ProviderWindowID,
		state.WindowKind,
		codexquota.ScopeFromFields(state.ModelScopeKind, state.ModelScopeKey, modelIDs),
	)
	state.ProviderWindowID = codexquota.CanonicalProviderWindowIDForScope(
		state.ProviderWindowID,
		state.WindowKind,
		scope,
	)
	state.ModelScopeKind = scope.Kind
	state.ModelScopeKey = scope.Key
	state.ModelIDsJSON = marshalAllowlist(scope.Models)
	state.ScopeFingerprint = quotasnapshotrepo.ScopeFingerprint(scope.Kind, scope.Key, scope.Models)
	if state.ContainerProviderWindowID != "" {
		state.ContainerProviderWindowID = codexquota.CanonicalProviderWindowID(state.ContainerProviderWindowID)
	}
	return providedScope.Kind == "all" && scope.Kind != "all"
}

func codexQueryScopeKey(providerWindowID, windowKind string, scope codexquota.ModelScope) string {
	providerWindowID = codexquota.CanonicalProviderWindowIDForScope(providerWindowID, windowKind, scope)
	return providerWindowID + "\x00" + quotasnapshotrepo.ScopeFingerprint(scope.Kind, scope.Key, scope.Models)
}

func normalizeWindowInput(accountKey, provider string, input WindowInput, nowMS int64) (model.AccountQuotaSnapshot, error) {
	providerWindowID := strings.TrimSpace(input.ProviderWindowID)
	if providerWindowID == "" {
		return model.AccountQuotaSnapshot{}, errors.New("provider_window_id is required")
	}
	mode := strings.ToLower(strings.TrimSpace(input.WindowMode))
	if !validModes[mode] {
		return model.AccountQuotaSnapshot{}, fmt.Errorf("unsupported window_mode %q", input.WindowMode)
	}
	scopeKind := strings.ToLower(strings.TrimSpace(input.ModelScopeKind))
	if !validScopes[scopeKind] {
		return model.AccountQuotaSnapshot{}, fmt.Errorf("unsupported model_scope_kind %q", input.ModelScopeKind)
	}
	source := strings.ToLower(strings.TrimSpace(input.Source))
	if !validSources[source] {
		return model.AccountQuotaSnapshot{}, fmt.Errorf("unsupported source %q", input.Source)
	}
	accuracy := strings.ToLower(strings.TrimSpace(input.BoundaryAccuracy))
	if !validAccuracy[accuracy] {
		return model.AccountQuotaSnapshot{}, fmt.Errorf("unsupported boundary_accuracy %q", input.BoundaryAccuracy)
	}
	observedAtMS := input.ObservedAtMS
	if observedAtMS <= 0 {
		observedAtMS = nowMS
	}
	if observedAtMS > observationFutureLimitMS(nowMS) {
		return model.AccountQuotaSnapshot{}, errors.New("observed_at_ms is too far in the future")
	}
	cycleStart, cycleEnd, duration, err := normalizeBoundaries(mode, accuracy, input.CycleStartMS, input.CycleEndMS, input.DurationSeconds)
	if err != nil {
		return model.AccountQuotaSnapshot{}, err
	}
	if mode == "rolling" && duration != nil {
		if _, ok := rollingWindowExpiryMS(observedAtMS, *duration); !ok {
			return model.AccountQuotaSnapshot{}, errors.New("rolling window expiry is too large")
		}
	}
	for name, value := range map[string]*float64{
		"used_percent": input.UsedPercent, "remaining_percent": input.RemainingPercent,
	} {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > 100) {
			return model.AccountQuotaSnapshot{}, fmt.Errorf("%s must be between 0 and 100", name)
		}
	}
	for name, value := range map[string]*float64{"used_value": input.UsedValue, "limit_value": input.LimitValue} {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0) {
			return model.AccountQuotaSnapshot{}, fmt.Errorf("%s must be greater than or equal to 0", name)
		}
	}
	if input.ResetCreditsAvailable != nil && *input.ResetCreditsAvailable < 0 {
		return model.AccountQuotaSnapshot{}, errors.New("reset_credits_available must be greater than or equal to 0")
	}
	modelIDs, err := normalizeStringList(input.ModelIDs, maxModelIDs)
	if err != nil {
		return model.AccountQuotaSnapshot{}, err
	}
	if scopeKind == "models" && len(modelIDs) == 0 {
		return model.AccountQuotaSnapshot{}, errors.New("model_ids are required for models scope")
	}
	scopeKey := strings.TrimSpace(input.ModelScopeKey)
	if provider == "codex" {
		scope := codexquota.ResolveProviderWindowScope(
			providerWindowID,
			input.WindowKind,
			codexquota.ScopeFromFields(scopeKind, scopeKey, modelIDs),
		)
		providerWindowID = codexquota.CanonicalProviderWindowIDForScope(providerWindowID, input.WindowKind, scope)
		scopeKind, scopeKey, modelIDs = scope.Kind, scope.Key, scope.Models
	}
	modelIDsJSON := marshalAllowlist(modelIDs)
	providerWindowAliases, err := normalizeStringList(input.ProviderWindowAliases, 32)
	if err != nil {
		return model.AccountQuotaSnapshot{}, fmt.Errorf("provider_window_aliases: %w", err)
	}
	if provider == "codex" {
		filtered := providerWindowAliases[:0]
		for _, alias := range providerWindowAliases {
			rawAlias := strings.ToLower(strings.TrimSpace(alias))
			canonicalAlias := codexquota.CanonicalProviderWindowIDForScope(alias, input.WindowKind, codexquota.ScopeFromFields(scopeKind, scopeKey, modelIDs))
			for _, candidate := range []string{rawAlias, canonicalAlias} {
				if candidate != "" && candidate != providerWindowID {
					filtered = append(filtered, candidate)
				}
			}
		}
		providerWindowAliases, err = normalizeStringList(filtered, 32)
		if err != nil {
			return model.AccountQuotaSnapshot{}, fmt.Errorf("provider_window_aliases: %w", err)
		}
	}
	scopeFingerprint := quotasnapshotrepo.ScopeFingerprint(scopeKind, scopeKey, modelIDs)
	resetCredits, err := normalizeResetCredits(input.ResetCredits)
	if err != nil {
		return model.AccountQuotaSnapshot{}, err
	}
	relationshipKind := strings.ToLower(strings.TrimSpace(input.RelationshipKind))
	containerWindowID := strings.TrimSpace(input.ContainerWindowID)
	if provider == "codex" && containerWindowID != "" {
		containerWindowID = codexquota.CanonicalProviderWindowID(containerWindowID)
	}
	if relationshipKind == "" && containerWindowID != "" {
		return model.AccountQuotaSnapshot{}, errors.New("relationship_kind is required with container_provider_window_id")
	}
	if relationshipKind != "" {
		if !validRelationships[relationshipKind] {
			return model.AccountQuotaSnapshot{}, fmt.Errorf("unsupported relationship_kind %q", input.RelationshipKind)
		}
		if containerWindowID == "" {
			return model.AccountQuotaSnapshot{}, errors.New("container_provider_window_id is required with relationship_kind")
		}
		if containerWindowID == providerWindowID {
			return model.AccountQuotaSnapshot{}, errors.New("container_provider_window_id must differ from provider_window_id")
		}
	}
	sourceObservationID, err := normalizeObservationID(input.SourceObservationID)
	if err != nil {
		return model.AccountQuotaSnapshot{}, err
	}
	return model.AccountQuotaSnapshot{
		AccountKey: accountKey, Provider: provider, ProviderWindowID: providerWindowID,
		ProviderWindowAliases: providerWindowAliases,
		WindowKind:            strings.TrimSpace(input.WindowKind), WindowMode: mode,
		ModelScopeKind: scopeKind, ModelScopeKey: scopeKey,
		ModelIDsJSON: modelIDsJSON, ScopeFingerprint: scopeFingerprint, Source: source,
		SourceObservationID: sourceObservationID, ObservedAtMS: observedAtMS,
		BoundaryAccuracy: accuracy, CycleStartMS: cycleStart, CycleEndMS: cycleEnd,
		DurationSeconds: duration, UsedPercent: input.UsedPercent,
		RemainingPercent: input.RemainingPercent, UsedValue: input.UsedValue,
		LimitValue: input.LimitValue, QuotaUnit: strings.TrimSpace(input.QuotaUnit),
		ResetCreditsAvailable: input.ResetCreditsAvailable,
		ResetCreditsJSON:      marshalAllowlist(resetCredits), PlanType: strings.TrimSpace(input.PlanType),
		RelationshipKind:  relationshipKind,
		ContainerWindowID: containerWindowID,
		CreatedAtMS:       nowMS,
	}, nil
}

func normalizeBoundaries(mode, accuracy string, start, end, duration *int64) (*int64, *int64, *int64, error) {
	if start != nil && *start <= 0 {
		return nil, nil, nil, errors.New("cycle_start_ms must be greater than 0")
	}
	if end != nil && *end <= 0 {
		return nil, nil, nil, errors.New("cycle_end_ms must be greater than 0")
	}
	var durationMS int64
	if duration != nil {
		if *duration <= 0 {
			return nil, nil, nil, errors.New("duration_seconds must be greater than 0")
		}
		if *duration > math.MaxInt64/1000 {
			return nil, nil, nil, errors.New("duration_seconds is too large")
		}
		durationMS = *duration * 1000
	}
	if start != nil && end != nil && *start >= *end {
		return nil, nil, nil, errors.New("cycle_start_ms must be less than cycle_end_ms")
	}
	if mode == "rolling" && duration == nil {
		return nil, nil, nil, errors.New("duration_seconds is required for rolling windows")
	}
	if (mode == "fixed" || mode == "calendar") && (accuracy == "exact" || accuracy == "derived") {
		if end == nil || (start == nil && duration == nil) {
			return nil, nil, nil, errors.New("reliable fixed/calendar windows require cycle_end_ms and cycle_start_ms or duration_seconds")
		}
	}
	if start != nil && end != nil {
		difference := *end - *start
		if duration != nil && difference != durationMS {
			return nil, nil, nil, errors.New("cycle boundaries must match duration_seconds")
		}
		if duration == nil {
			if difference%1000 != 0 {
				return nil, nil, nil, errors.New("cycle boundary difference must be a whole number of seconds")
			}
			derivedDuration := difference / 1000
			if derivedDuration <= 0 {
				return nil, nil, nil, errors.New("duration_seconds must be greater than 0")
			}
			duration = &derivedDuration
		}
		return start, end, duration, nil
	}
	if start == nil && end != nil && duration != nil {
		if *end <= durationMS {
			return nil, nil, nil, errors.New("derived cycle_start_ms must be greater than 0")
		}
		value := *end - durationMS
		start = &value
	}
	if start != nil && end == nil && duration != nil {
		if *start > math.MaxInt64-durationMS {
			return nil, nil, nil, errors.New("derived cycle_end_ms is too large")
		}
		value := *start + durationMS
		end = &value
	}
	return start, end, duration, nil
}

func selectWindows(
	candidates []model.AccountQuotaSnapshot,
	states []model.AccountQuotaWindowState,
	nowMS int64,
) []Window {
	groups := make(map[string][]model.AccountQuotaSnapshot)
	for _, candidate := range candidates {
		if candidate.ScopeFingerprint == "" {
			candidate.ScopeFingerprint = quotasnapshotrepo.ScopeFingerprint(
				candidate.ModelScopeKind, candidate.ModelScopeKey, unmarshalStringList(candidate.ModelIDsJSON),
			)
		}
		key := candidate.ProviderWindowID + "\x00" + candidate.ScopeFingerprint
		groups[key] = append(groups[key], candidate)
	}
	lifecycleLastSeen := make(map[string]int64, len(states))
	for _, state := range states {
		normalizeCodexWindowStateForQuery(&state)
		key := state.ProviderWindowID + "\x00" + state.ScopeFingerprint
		if previous, ok := lifecycleLastSeen[key]; !ok || state.LastSeenAtMS > previous {
			lifecycleLastSeen[key] = state.LastSeenAtMS
		}
	}
	result := make([]Window, 0, len(groups))
	for key, group := range groups {
		lastSeenAtMS, hasLifecycleState := lifecycleLastSeen[key]
		selectionWatermarkMS := lastSeenAtMS
		if hasLifecycleState {
			for _, candidate := range group {
				if candidate.ObservationID == 0 && candidate.ObservedAtMS > selectionWatermarkMS {
					selectionWatermarkMS = candidate.ObservedAtMS
				}
			}
		}
		sort.SliceStable(group, func(i, j int) bool {
			leftCurrent := hasLifecycleState && group[i].ObservedAtMS == selectionWatermarkMS
			rightCurrent := hasLifecycleState && group[j].ObservedAtMS == selectionWatermarkMS
			if leftCurrent != rightCurrent {
				return leftCurrent
			}
			leftRank := candidateRank(group[i], nowMS)
			rightRank := candidateRank(group[j], nowMS)
			if leftRank != rightRank {
				return leftRank > rightRank
			}
			if group[i].ObservedAtMS != group[j].ObservedAtMS {
				return group[i].ObservedAtMS > group[j].ObservedAtMS
			}
			return group[i].ID > group[j].ID
		})
		selected := group[0]
		if isZeroOnlyHeaderPlaceholder(selected) {
			continue
		}
		boundarySource := selected
		if boundary, ok := selectStableHeaderBoundary(selected, group); ok {
			applySnapshotBoundary(&selected, boundary)
			boundarySource = boundary
		}
		window := snapshotWindow(selected, isStale(selected, nowMS))
		window.FieldSources = map[string]FieldSource{
			"quota": {Source: selected.Source, ObservedAtMS: selected.ObservedAtMS},
		}
		if boundaryComplete(selected) {
			window.FieldSources["boundary"] = FieldSource{
				Source: boundarySource.Source, ObservedAtMS: boundarySource.ObservedAtMS,
			}
		}
		if selected.Provider == "codex" {
			if window.Stale {
				window.ResetCreditsAvailable = nil
				window.ResetCredits = nil
			} else {
				if selected.ResetCreditsAvailable != nil {
					window.FieldSources["reset_credits_available"] = FieldSource{
						Source: selected.Source, ObservedAtMS: selected.ObservedAtMS,
					}
				}
				if selected.ResetCreditsJSON != "" {
					window.FieldSources["reset_credits"] = FieldSource{
						Source: selected.Source, ObservedAtMS: selected.ObservedAtMS,
					}
				}
			}
			if selected.PlanType != "" {
				window.FieldSources["plan_type"] = FieldSource{
					Source: selected.Source, ObservedAtMS: selected.ObservedAtMS,
				}
			}
			for _, candidate := range group {
				if isStale(candidate, nowMS) {
					continue
				}
				if window.ResetCreditsAvailable == nil && candidate.ResetCreditsAvailable != nil {
					window.ResetCreditsAvailable = candidate.ResetCreditsAvailable
					window.FieldSources["reset_credits_available"] = FieldSource{Source: candidate.Source, ObservedAtMS: candidate.ObservedAtMS}
				}
				if len(window.ResetCredits) == 0 && candidate.ResetCreditsJSON != "" {
					window.ResetCredits = unmarshalResetCredits(candidate.ResetCreditsJSON)
					window.FieldSources["reset_credits"] = FieldSource{Source: candidate.Source, ObservedAtMS: candidate.ObservedAtMS}
				}
				if window.PlanType == "" && candidate.PlanType != "" {
					window.PlanType = candidate.PlanType
					window.FieldSources["plan_type"] = FieldSource{Source: candidate.Source, ObservedAtMS: candidate.ObservedAtMS}
				}
			}
			countSource, hasCountSource := window.FieldSources["reset_credits_available"]
			creditsSource, hasCreditsSource := window.FieldSources["reset_credits"]
			if window.ResetCreditsAvailable != nil && *window.ResetCreditsAvailable == 0 &&
				hasCountSource && (!hasCreditsSource || countSource.ObservedAtMS >= creditsSource.ObservedAtMS) {
				window.ResetCredits = nil
				delete(window.FieldSources, "reset_credits")
			}
		}
		result = append(result, window)
	}
	sort.SliceStable(result, func(i, j int) bool {
		left, right := windowSortRank(result[i]), windowSortRank(result[j])
		if left != right {
			return left < right
		}
		return result[i].ProviderWindowID < result[j].ProviderWindowID
	})
	return result
}

func mergeLifecycleWindows(
	windows []Window,
	states []model.AccountQuotaWindowState,
	nowMS int64,
	includeInactive bool,
) []Window {
	statesByKey := make(map[string]model.AccountQuotaWindowState, len(states))
	for _, state := range states {
		normalizeCodexWindowStateForQuery(&state)
		key := state.ProviderWindowID + "\x00" + state.ScopeFingerprint
		// ListWindowStates is ordered newest-first. Keep the first state when
		// legacy and scoped rows normalize to one logical identity.
		if _, exists := statesByKey[key]; !exists {
			statesByKey[key] = state
		}
	}
	result := make([]Window, 0, len(windows))
	for _, window := range windows {
		state, ok := statesByKey[window.ProviderWindowID+"\x00"+window.ScopeFingerprint]
		if !ok {
			if window.Availability == "" {
				window.Availability = "unknown"
			}
			result = append(result, window)
			continue
		}
		if state.Availability == "inactive" && !includeInactive {
			continue
		}
		window.LogicalWindowID = state.ID
		window.ActivationGeneration = state.Generation
		window.Availability = state.Availability
		window.WindowKind = state.WindowKind
		window.WindowMode = state.WindowMode
		window.RelationshipKind = state.RelationshipKind
		window.ContainerWindowID = state.ContainerProviderWindowID
		window.FirstSeenAtMS = state.FirstSeenAtMS
		window.LastSeenAtMS = state.LastSeenAtMS
		window.MissingSinceMS = state.MissingSinceMS
		window.DeactivatedAtMS = state.DeactivatedAtMS
		window.CurrentCycle = quotaCycleResponse(state.CurrentCycle, true)
		window.PreviousCycle = quotaCycleResponse(state.PreviousCycle, false)
		if state.CurrentCycle != nil {
			start := state.CurrentCycle.ActualStartMS
			window.CycleStartMS = &start
			window.CycleEndMS = copyInt64Pointer(state.CurrentCycle.ScheduledEndMS)
			window.DurationSeconds = copyInt64Pointer(state.CurrentCycle.DurationSeconds)
			window.BoundaryAccuracy = state.CurrentCycle.BoundaryAccuracy
			window.Stale = state.CurrentCycle.ScheduledEndMS != nil && *state.CurrentCycle.ScheduledEndMS <= nowMS
		}
		if state.Availability == "pending_absent" || state.Availability == "inactive" {
			window.Stale = true
		}
		result = append(result, window)
	}
	return result
}

func quotaCycleResponse(cycle *model.AccountQuotaCycle, current bool) *Cycle {
	if cycle == nil {
		return nil
	}
	reliableBoundary := cycle.BoundaryAccuracy == "exact" || cycle.BoundaryAccuracy == "derived"
	forecastEligible := reliableBoundary &&
		((current && cycle.State == "active" && cycle.ScheduledEndMS != nil) ||
			(!current && cycle.EndReason == "scheduled"))
	return &Cycle{
		ID: cycle.ID, ActivationID: cycle.ActivationID, State: cycle.State,
		ScheduledStartMS: copyInt64Pointer(cycle.ScheduledStartMS),
		ScheduledEndMS:   copyInt64Pointer(cycle.ScheduledEndMS),
		ActualStartMS:    cycle.ActualStartMS, ActualEndMS: copyInt64Pointer(cycle.ActualEndMS),
		DurationSeconds:  copyInt64Pointer(cycle.DurationSeconds),
		BoundaryAccuracy: cycle.BoundaryAccuracy, EndReason: cycle.EndReason,
		ParentCycleID:    copyInt64Pointer(cycle.ParentCycleID),
		ForecastEligible: forecastEligible,
	}
}

func isDerivedResponseHeaderFixedBoundary(snapshot model.AccountQuotaSnapshot) bool {
	return snapshot.Source == "response_header" &&
		snapshot.WindowMode == "fixed" &&
		snapshot.BoundaryAccuracy == "derived" &&
		boundaryComplete(snapshot)
}

func isReliableResponseHeaderFixedBoundary(snapshot model.AccountQuotaSnapshot) bool {
	return snapshot.Source == "response_header" &&
		snapshot.WindowMode == "fixed" &&
		(snapshot.BoundaryAccuracy == "exact" || snapshot.BoundaryAccuracy == "derived") &&
		boundaryComplete(snapshot)
}

func selectStableHeaderBoundary(selected model.AccountQuotaSnapshot, candidates []model.AccountQuotaSnapshot) (model.AccountQuotaSnapshot, bool) {
	if !isDerivedResponseHeaderFixedBoundary(selected) {
		return model.AccountQuotaSnapshot{}, false
	}
	bestIndex := -1
	for index := range candidates {
		candidate := candidates[index]
		if !sameHeaderWindowCycle(selected, candidate) {
			continue
		}
		if bestIndex < 0 || stableBoundaryLess(candidate, candidates[bestIndex]) {
			bestIndex = index
		}
	}
	if bestIndex < 0 {
		return model.AccountQuotaSnapshot{}, false
	}
	return candidates[bestIndex], true
}

func sameHeaderWindowCycle(left, right model.AccountQuotaSnapshot) bool {
	if !isReliableResponseHeaderFixedBoundary(right) ||
		left.ProviderWindowID != right.ProviderWindowID ||
		left.ScopeFingerprint != right.ScopeFingerprint ||
		left.InventoryScopeKey != right.InventoryScopeKey ||
		left.DurationSeconds == nil || right.DurationSeconds == nil ||
		*left.DurationSeconds != *right.DurationSeconds ||
		left.CycleEndMS == nil || right.CycleEndMS == nil {
		return false
	}
	delta := *left.CycleEndMS - *right.CycleEndMS
	if delta < 0 {
		delta = -delta
	}
	return delta <= derivedFixedBoundaryJitterMS
}

func stableBoundaryLess(left, right model.AccountQuotaSnapshot) bool {
	leftRank := boundaryAccuracyRank(left.BoundaryAccuracy)
	rightRank := boundaryAccuracyRank(right.BoundaryAccuracy)
	if leftRank != rightRank {
		return leftRank > rightRank
	}
	if left.BoundaryAccuracy == "exact" {
		if left.ObservedAtMS != right.ObservedAtMS {
			return left.ObservedAtMS > right.ObservedAtMS
		}
		return left.ID > right.ID
	}
	if left.CycleStartMS != nil && right.CycleStartMS != nil && *left.CycleStartMS != *right.CycleStartMS {
		return *left.CycleStartMS < *right.CycleStartMS
	}
	if left.ObservedAtMS != right.ObservedAtMS {
		return left.ObservedAtMS < right.ObservedAtMS
	}
	return left.ID < right.ID
}

func boundaryAccuracyRank(accuracy string) int {
	if accuracy == "exact" {
		return 2
	}
	if accuracy == "derived" {
		return 1
	}
	return 0
}

func applySnapshotBoundary(snapshot *model.AccountQuotaSnapshot, boundary model.AccountQuotaSnapshot) {
	snapshot.CycleStartMS = copyInt64Pointer(boundary.CycleStartMS)
	snapshot.CycleEndMS = copyInt64Pointer(boundary.CycleEndMS)
	snapshot.DurationSeconds = copyInt64Pointer(boundary.DurationSeconds)
	snapshot.BoundaryAccuracy = boundary.BoundaryAccuracy
}

func copyInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func isZeroOnlyHeaderPlaceholder(snapshot model.AccountQuotaSnapshot) bool {
	providerWindowID := snapshot.ProviderWindowID
	if snapshot.Provider == "codex" {
		providerWindowID = codexquota.CanonicalProviderWindowID(providerWindowID, snapshot.WindowKind)
	}
	if snapshot.Source != "response_header" ||
		snapshot.WindowMode != "unknown" ||
		(providerWindowID != "five-hour" && providerWindowID != "weekly" && providerWindowID != "monthly") ||
		snapshot.CycleStartMS != nil || snapshot.CycleEndMS != nil || snapshot.DurationSeconds != nil ||
		snapshot.UsedValue != nil || snapshot.LimitValue != nil || strings.TrimSpace(snapshot.QuotaUnit) != "" ||
		snapshot.UsedPercent == nil || *snapshot.UsedPercent != 0 {
		return false
	}
	return snapshot.RemainingPercent == nil || *snapshot.RemainingPercent == 100
}

func candidateRank(snapshot model.AccountQuotaSnapshot, nowMS int64) int {
	rank := 0
	if !isStale(snapshot, nowMS) {
		rank += 4
	}
	if boundaryComplete(snapshot) {
		rank += 2
	}
	if snapshot.BoundaryAccuracy == "exact" || snapshot.BoundaryAccuracy == "derived" {
		rank++
	}
	return rank
}

func boundaryComplete(snapshot model.AccountQuotaSnapshot) bool {
	switch snapshot.WindowMode {
	case "fixed", "calendar":
		return snapshot.CycleStartMS != nil && snapshot.CycleEndMS != nil && snapshot.DurationSeconds != nil
	case "rolling":
		return snapshot.DurationSeconds != nil
	default:
		return true
	}
}

func isStale(snapshot model.AccountQuotaSnapshot, nowMS int64) bool {
	if snapshot.CycleEndMS != nil && (snapshot.WindowMode == "fixed" || snapshot.WindowMode == "calendar") {
		return *snapshot.CycleEndMS <= nowMS
	}
	if snapshot.WindowMode == "rolling" && snapshot.DurationSeconds != nil {
		expiresAtMS, ok := rollingWindowExpiryMS(snapshot.ObservedAtMS, *snapshot.DurationSeconds)
		if !ok {
			return true
		}
		return expiresAtMS < nowMS
	}
	return false
}

func snapshotWindow(snapshot model.AccountQuotaSnapshot, stale bool) Window {
	modelScopeKind := snapshot.ModelScopeKind
	modelScopeKey := snapshot.ModelScopeKey
	modelIDs := unmarshalStringList(snapshot.ModelIDsJSON)
	if snapshot.Provider == "codex" && strings.EqualFold(strings.TrimSpace(modelScopeKind), "all") &&
		codexquota.IsMainProviderWindowID(snapshot.ProviderWindowID) {
		mainScope := codexquota.MainScope()
		modelScopeKind = mainScope.Kind
		modelScopeKey = mainScope.Key
		modelIDs = nil
	}
	providerWindowAliases := []string(nil)
	if snapshot.Provider == "codex" {
		for _, alias := range codexquota.ProviderWindowAliases(
			snapshot.ProviderWindowID,
			codexquota.ModelScope{Kind: modelScopeKind, Key: modelScopeKey, Models: modelIDs, Complete: true},
		) {
			if alias != snapshot.ProviderWindowID {
				providerWindowAliases = append(providerWindowAliases, alias)
			}
		}
	}
	return Window{
		ProviderWindowID:      snapshot.ProviderWindowID,
		ProviderWindowAliases: providerWindowAliases,
		WindowKind:            snapshot.WindowKind,
		WindowMode:            snapshot.WindowMode,
		ModelScopeKind:        modelScopeKind,
		ModelScopeKey:         modelScopeKey,
		ModelIDs:              modelIDs,
		Source:                snapshot.Source, SourceObservationID: snapshot.SourceObservationID,
		ObservedAtMS: snapshot.ObservedAtMS, BoundaryAccuracy: snapshot.BoundaryAccuracy,
		CycleStartMS: snapshot.CycleStartMS, CycleEndMS: snapshot.CycleEndMS,
		DurationSeconds: snapshot.DurationSeconds, UsedPercent: snapshot.UsedPercent,
		RemainingPercent: snapshot.RemainingPercent, UsedValue: snapshot.UsedValue,
		LimitValue: snapshot.LimitValue, QuotaUnit: snapshot.QuotaUnit,
		ResetCreditsAvailable: snapshot.ResetCreditsAvailable,
		ResetCredits:          unmarshalResetCredits(snapshot.ResetCreditsJSON), PlanType: snapshot.PlanType,
		Stale: stale, ScopeFingerprint: snapshot.ScopeFingerprint,
	}
}

func windowSortRank(window Window) int64 {
	if window.WindowMode == "non_window" || window.WindowMode == "unknown" {
		return math.MaxInt64
	}
	if window.DurationSeconds == nil {
		return math.MaxInt64 - 1
	}
	return *window.DurationSeconds
}

func (target AccountTarget) identityFields(provider string) usageidentity.Fields {
	providerSnapshot := strings.TrimSpace(target.AuthProviderSnapshot)
	if providerSnapshot == "" {
		providerSnapshot = provider
	}
	return usageidentity.Fields{
		AuthFileSnapshot:      strings.TrimSpace(target.AuthFileSnapshot),
		AuthIndex:             strings.TrimSpace(target.AuthIndex),
		AuthProviderSnapshot:  providerSnapshot,
		AuthAccountIDSnapshot: strings.TrimSpace(target.AuthAccountIDSnapshot),
		AuthProjectIDSnapshot: strings.TrimSpace(target.AuthProjectIDSnapshot),
		AccountSnapshot:       strings.TrimSpace(target.AccountSnapshot),
		AuthLabelSnapshot:     strings.TrimSpace(target.AuthLabelSnapshot),
		Source:                strings.TrimSpace(target.Source),
	}
}

func normalizeProvider(value string) string {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", "-"))
	switch normalized {
	case "x-ai", "grok":
		return "xai"
	default:
		return normalized
	}
}

func normalizeStringList(values []string, limit int) ([]string, error) {
	if len(values) > limit {
		return nil, fmt.Errorf("list must be less than or equal to %d", limit)
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		identity := strings.ToLower(trimmed)
		if _, ok := seen[identity]; ok {
			continue
		}
		seen[identity] = struct{}{}
		result = append(result, trimmed)
	}
	sort.SliceStable(result, func(i, j int) bool {
		left, right := strings.ToLower(result[i]), strings.ToLower(result[j])
		if left != right {
			return left < right
		}
		return result[i] < result[j]
	})
	return result, nil
}

func normalizeObservationID(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) > maxObservationIDLen {
		return "", fmt.Errorf("source_observation_id must be less than or equal to %d bytes", maxObservationIDLen)
	}
	return trimmed, nil
}

func rollingWindowExpiryMS(observedAtMS, durationSeconds int64) (int64, bool) {
	if observedAtMS <= 0 || durationSeconds <= 0 || durationSeconds > math.MaxInt64/1000 {
		return 0, false
	}
	durationMS := durationSeconds * 1000
	if observedAtMS > math.MaxInt64-durationMS {
		return 0, false
	}
	return observedAtMS + durationMS, true
}

func observationFutureLimitMS(nowMS int64) int64 {
	const futureAllowanceMS = int64(5 * 60 * 1000)
	if nowMS > math.MaxInt64-futureAllowanceMS {
		return math.MaxInt64
	}
	return nowMS + futureAllowanceMS
}

func normalizeResetCredits(values []ResetCredit) ([]ResetCredit, error) {
	if len(values) > 100 {
		return nil, errors.New("reset_credits must be less than or equal to 100")
	}
	result := make([]ResetCredit, 0, len(values))
	seen := make(map[string]int, len(values))
	for _, value := range values {
		value.ID = strings.TrimSpace(value.ID)
		if value.ID == "" || value.ExpiresAtMS <= 0 {
			return nil, errors.New("reset credit id and expires_at_ms are required")
		}
		if len(value.ID) > maxObservationIDLen {
			return nil, fmt.Errorf("reset credit id must be less than or equal to %d bytes", maxObservationIDLen)
		}
		identity := strings.ToLower(value.ID)
		if index, ok := seen[identity]; ok {
			if result[index].ExpiresAtMS != value.ExpiresAtMS {
				return nil, fmt.Errorf("reset credit %q has conflicting expiry", value.ID)
			}
			if value.ID < result[index].ID {
				result[index].ID = value.ID
			}
			continue
		}
		seen[identity] = len(result)
		result = append(result, value)
	}
	sort.SliceStable(result, func(i, j int) bool {
		leftID, rightID := strings.ToLower(result[i].ID), strings.ToLower(result[j].ID)
		if leftID != rightID {
			return leftID < rightID
		}
		if result[i].ExpiresAtMS != result[j].ExpiresAtMS {
			return result[i].ExpiresAtMS < result[j].ExpiresAtMS
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

func marshalAllowlist(value any) string {
	data, err := json.Marshal(value)
	if err != nil || string(data) == "[]" || string(data) == "null" {
		return ""
	}
	return string(data)
}

func snapshotContentHash(snapshot model.AccountQuotaSnapshot) string {
	payload, _ := json.Marshal(struct {
		ProviderWindowID      string
		WindowKind            string
		WindowMode            string
		ModelScopeKind        string
		ModelScopeKey         string
		ModelIDsJSON          string
		ScopeFingerprint      string
		InventoryScopeKey     string
		RelationshipKind      string
		ContainerWindowID     string
		Source                string
		SourceObservationID   string
		BoundaryAccuracy      string
		CycleStartMS          *int64
		CycleEndMS            *int64
		DurationSeconds       *int64
		UsedPercent           *float64
		RemainingPercent      *float64
		UsedValue             *float64
		LimitValue            *float64
		QuotaUnit             string
		ResetCreditsAvailable *int64
		ResetCreditsJSON      string
		PlanType              string
	}{
		ProviderWindowID:      snapshot.ProviderWindowID,
		WindowKind:            snapshot.WindowKind,
		WindowMode:            snapshot.WindowMode,
		ModelScopeKind:        snapshot.ModelScopeKind,
		ModelScopeKey:         snapshot.ModelScopeKey,
		ModelIDsJSON:          snapshot.ModelIDsJSON,
		ScopeFingerprint:      snapshot.ScopeFingerprint,
		InventoryScopeKey:     snapshot.InventoryScopeKey,
		RelationshipKind:      snapshot.RelationshipKind,
		ContainerWindowID:     snapshot.ContainerWindowID,
		Source:                snapshot.Source,
		SourceObservationID:   snapshot.SourceObservationID,
		BoundaryAccuracy:      snapshot.BoundaryAccuracy,
		CycleStartMS:          snapshot.CycleStartMS,
		CycleEndMS:            snapshot.CycleEndMS,
		DurationSeconds:       snapshot.DurationSeconds,
		UsedPercent:           snapshot.UsedPercent,
		RemainingPercent:      snapshot.RemainingPercent,
		UsedValue:             snapshot.UsedValue,
		LimitValue:            snapshot.LimitValue,
		QuotaUnit:             snapshot.QuotaUnit,
		ResetCreditsAvailable: snapshot.ResetCreditsAvailable,
		ResetCreditsJSON:      snapshot.ResetCreditsJSON,
		PlanType:              snapshot.PlanType,
	})
	return fmt.Sprintf("%x", sha256.Sum256(payload))
}

func observationHash(write model.AccountQuotaObservationWrite) string {
	contentHashes := make([]string, 0, len(write.Snapshots))
	for _, snapshot := range write.Snapshots {
		contentHashes = append(contentHashes, snapshot.ProviderWindowID+"\x00"+snapshot.ScopeFingerprint+"\x00"+snapshot.ContentHash)
	}
	sort.Strings(contentHashes)
	removed := make([]string, 0, len(write.Removed))
	for _, item := range write.Removed {
		removed = append(removed, item.ProviderWindowID+"\x00"+item.ScopeFingerprint)
	}
	sort.Strings(removed)
	payload, _ := json.Marshal(struct {
		AccountKey          string
		Provider            string
		Source              string
		SourceObservationID string
		InventoryScopeKey   string
		InventoryMode       string
		ObservedAtMS        int64
		ContentHashes       []string
		Removed             []string
	}{
		AccountKey: write.Observation.AccountKey, Provider: write.Observation.Provider,
		Source: write.Observation.Source, SourceObservationID: write.Observation.SourceObservationID,
		InventoryScopeKey: write.Observation.InventoryScopeKey,
		InventoryMode:     write.Observation.InventoryMode, ObservedAtMS: write.Observation.ObservedAtMS,
		ContentHashes: contentHashes, Removed: removed,
	})
	return fmt.Sprintf("%x", sha256.Sum256(payload))
}

func unmarshalStringList(raw string) []string {
	var result []string
	if json.Unmarshal([]byte(raw), &result) != nil {
		return nil
	}
	return result
}

func unmarshalResetCredits(raw string) []ResetCredit {
	var result []ResetCredit
	if json.Unmarshal([]byte(raw), &result) != nil {
		return nil
	}
	return result
}

func stringSet(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}
