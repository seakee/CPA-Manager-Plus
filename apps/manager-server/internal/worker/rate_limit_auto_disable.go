package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	quotaAutoDisableQueueSize     = 256
	quotaAutoDisableDefaultTick   = 15 * time.Second
	quotaAutoDisableActionTimeout = 30 * time.Second
	quotaCooldownDueLimit         = 100
	xaiFreeUsageCooldown          = 24 * time.Hour
	quotaReasonCodexUsageLimit    = "codex_usage_limit_reached"
	quotaReasonXAIFreeUsage       = "xai_free_usage_exhausted"
	quotaWindowRolling24H         = "rolling_24h"
	quotaWindowUnknown            = "unknown"
)

// RateLimitAutoDisableWorker reacts to request-monitoring events in near real time.
// It handles strict provider quota signals with a known recovery time: Codex 429
// usage_limit_reached responses and xAI free-usage-exhausted responses. Disables
// are persisted with CPAMP ownership, so recovery never relies solely on in-memory
// timers and never re-enables pre-existing/manual disables.
type RateLimitAutoDisableWorker struct {
	store               *store.Store
	client              *http.Client
	authFileMutations   *cpaauthfiles.MutationCoordinator
	compensationTimeout time.Duration

	jobs chan quotaAutoDisableCandidate

	operationMu         sync.Mutex
	mu                  sync.RWMutex
	baseURL             string
	managementKey       string
	enableCheckInterval time.Duration
}

type quotaAutoDisableCandidate struct {
	BaseURL         string
	ManagementKey   string
	FileName        string
	AuthIndex       string
	DisplayAccount  string
	AccountSnapshot string
	AccountID       string
	Provider        string
	ReasonCode      string
	WindowKind      string
	ResetAt         time.Time
	EventHash       string
	Reason          string
	Owner           string
	EvidenceJSON    string
}

// quota_cooldowns predates the Codex Workspace/member identity split and has
// no dedicated Workspace column. Keep the identity evidence in its existing
// JSON payload so recovery can fail closed without changing the schema.
type codexCooldownIdentityEvidence struct {
	Workspace string `json:"workspace"`
	Member    string `json:"member"`
}

type authFile = cpaauthfiles.File

func NewRateLimitAutoDisableWorker(st *store.Store, initial ...collectorpkg.RuntimeConfig) *RateLimitAutoDisableWorker {
	return NewRateLimitAutoDisableWorkerWithMutationCoordinator(st, nil, initial...)
}

func NewRateLimitAutoDisableWorkerWithMutationCoordinator(
	st *store.Store,
	coordinator *cpaauthfiles.MutationCoordinator,
	initial ...collectorpkg.RuntimeConfig,
) *RateLimitAutoDisableWorker {
	if coordinator == nil {
		coordinator = cpaauthfiles.NewMutationCoordinator()
	}
	w := &RateLimitAutoDisableWorker{
		store:               st,
		client:              &http.Client{Timeout: quotaAutoDisableActionTimeout},
		authFileMutations:   coordinator,
		compensationTimeout: authFileMutationCompensationTimeout,
		jobs:                make(chan quotaAutoDisableCandidate, quotaAutoDisableQueueSize),
		enableCheckInterval: quotaAutoDisableDefaultTick,
	}
	if len(initial) > 0 {
		w.setRuntimeConfig(initial[0].CPAUpstreamURL, initial[0].ManagementKey)
	}
	return w
}

func (w *RateLimitAutoDisableWorker) Start(ctx context.Context) {
	go w.run(ctx)
}

func (w *RateLimitAutoDisableWorker) UpdateRuntimeConfig(ctx context.Context, cfg collectorpkg.RuntimeConfig) {
	if w == nil {
		return
	}
	baseURL := strings.TrimSpace(cfg.CPAUpstreamURL)
	managementKey := strings.TrimSpace(cfg.ManagementKey)
	if baseURL == "" || managementKey == "" {
		return
	}
	if w.setRuntimeConfig(baseURL, managementKey) {
		log.Printf("[quota-auto-disable] runtime config synced baseURL=%q managementKeySet=%t", baseURL, managementKey != "")
	}
	w.enableDue(ctx, time.Now())
}

// HandleUsageEvents is called by the request-monitoring collector after raw CPA
// usage events are normalized and enriched with auth-file snapshots. It does not
// poll historical events; it only reacts to newly observed request failures.
func (w *RateLimitAutoDisableWorker) HandleUsageEvents(ctx context.Context, cfg collectorpkg.RuntimeConfig, events []usage.Event) {
	if w == nil {
		return
	}
	baseURL := strings.TrimSpace(cfg.CPAUpstreamURL)
	managementKey := strings.TrimSpace(cfg.ManagementKey)
	if baseURL == "" || managementKey == "" {
		return
	}
	if w.setRuntimeConfig(baseURL, managementKey) {
		log.Printf("[quota-auto-disable] runtime config synced baseURL=%q managementKeySet=%t", baseURL, managementKey != "")
	}
	if len(events) == 0 {
		return
	}
	now := time.Now()
	for _, event := range events {
		candidate, ok := quotaAutoDisableCandidateFromEvent(event, baseURL, managementKey, now)
		if !ok {
			continue
		}
		select {
		case w.jobs <- candidate:
		case <-ctx.Done():
			return
		default:
			log.Printf("[quota-auto-disable] job queue full, dropped auth file %q event=%q", candidate.FileName, candidate.EventHash)
		}
	}
}

func (w *RateLimitAutoDisableWorker) run(ctx context.Context) {
	interval := w.enableCheckInterval
	if interval <= 0 {
		interval = quotaAutoDisableDefaultTick
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	w.enableDue(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case candidate := <-w.jobs:
			w.handleCandidate(ctx, candidate)
		case <-ticker.C:
			w.enableDue(ctx, time.Now())
		}
	}
}

func (w *RateLimitAutoDisableWorker) setRuntimeConfig(baseURL string, managementKey string) bool {
	baseURL = strings.TrimSpace(baseURL)
	managementKey = strings.TrimSpace(managementKey)
	w.mu.Lock()
	defer w.mu.Unlock()
	changed := w.baseURL != baseURL || w.managementKey != managementKey
	w.baseURL = baseURL
	w.managementKey = managementKey
	return changed
}

func (w *RateLimitAutoDisableWorker) runtimeConfig() (string, string) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.baseURL, w.managementKey
}

func (w *RateLimitAutoDisableWorker) handleCandidate(ctx context.Context, candidate quotaAutoDisableCandidate) {
	if w == nil {
		return
	}
	w.operationMu.Lock()
	defer w.operationMu.Unlock()
	w.handleCandidateLocked(ctx, candidate)
}

func (w *RateLimitAutoDisableWorker) handleCandidateLocked(ctx context.Context, candidate quotaAutoDisableCandidate) {
	if w.store == nil || w.store.QuotaCooldowns == nil {
		log.Printf("[quota-auto-disable] store unavailable, skip auth file %q", candidate.FileName)
		return
	}
	if candidate.FileName == "" || candidate.BaseURL == "" || candidate.ManagementKey == "" {
		return
	}
	now := time.Now()
	if !candidate.ResetAt.After(now) {
		log.Printf("[quota-auto-disable] quota event for auth file %q has non-future reset time %s, skip auto disable", candidate.FileName, candidate.ResetAt.Format(time.RFC3339))
		return
	}
	if w.authFileMutations == nil {
		log.Printf("[quota-auto-disable] mutation coordinator unavailable, skip auth file %q", candidate.FileName)
		return
	}
	releaseMutation, err := w.authFileMutations.Acquire(ctx, candidate.FileName)
	if err != nil {
		log.Printf("[quota-auto-disable] failed to coordinate auth file %q mutation: %v", candidate.FileName, err)
		return
	}
	defer releaseMutation()

	provider := normalizeQuotaProvider(candidate.Provider)
	accountSnapshot := quotaActionAccountSnapshot(candidate.FileName, candidate.AccountSnapshot)
	accountID := strings.TrimSpace(candidate.AccountID)
	if provider == "codex" {
		// quota_cooldowns has no Workspace column. Without auth_index there is
		// no safe place to persist the credential-level fallback, so do not
		// disable or later recover a same-name Codex credential by email alone.
		if strings.TrimSpace(candidate.AuthIndex) == "" {
			log.Printf("[quota-auto-disable] Codex auth file %q has no auth index; skip quota mutation", candidate.FileName)
			return
		}
		if accountID != "" {
			workspace, workspaceOK := usageidentity.NormalizeCodexWorkspaceSnapshot(accountID)
			if !workspaceOK {
				log.Printf("[quota-auto-disable] Codex auth file %q has invalid workspace evidence; skip quota mutation", candidate.FileName)
				return
			}
			accountID = workspace
		}
		if accountSnapshot != "" {
			member, memberOK := usageidentity.NormalizeCodexMemberSnapshot(accountSnapshot)
			if memberOK {
				accountSnapshot = member
			} else {
				accountSnapshot = ""
			}
		}
	}
	target, ok, err := w.currentAuthFileTarget(ctx, candidate.BaseURL, candidate.ManagementKey, cpaauthfiles.Identity{
		AuthFileName:      candidate.FileName,
		AuthIndex:         candidate.AuthIndex,
		Provider:          provider,
		AccountSnapshot:   accountSnapshot,
		AccountIDSnapshot: accountID,
	})
	if err != nil {
		log.Printf("[quota-auto-disable] failed to verify auth file %q before disable: %v", candidate.FileName, err)
		return
	}
	if !ok {
		log.Printf("[quota-auto-disable] auth file %q authIndex=%q not found/currently mismatched, skip auto disable", candidate.FileName, candidate.AuthIndex)
		return
	}
	current := target.File
	resolvedAuthIndex := firstNonEmpty(candidate.AuthIndex, current.AuthIndex)
	resolvedAccountSnapshot := firstNonEmpty(
		accountSnapshot,
		quotaActionAccountSnapshot(current.Name, current.AccountSnapshot),
	)
	resolvedProvider := normalizeQuotaProvider(firstNonEmpty(provider, current.Provider))
	resolvedAccountID := firstNonEmpty(accountID, current.AccountID)
	if resolvedProvider == "codex" {
		if strings.TrimSpace(resolvedAccountID) != "" {
			workspace, workspaceOK := usageidentity.NormalizeCodexWorkspaceSnapshot(resolvedAccountID)
			if !workspaceOK {
				log.Printf("[quota-auto-disable] auth file %q has invalid Codex workspace evidence; skip quota ownership", candidate.FileName)
				return
			}
			resolvedAccountID = workspace
		}
		if strings.TrimSpace(resolvedAccountSnapshot) != "" {
			member, memberOK := usageidentity.NormalizeCodexMemberSnapshot(resolvedAccountSnapshot)
			if memberOK {
				resolvedAccountSnapshot = member
			} else {
				// Account snapshots can be display labels or filenames. They are
				// optional quota diagnostics once the credential locator is known.
				resolvedAccountSnapshot = ""
			}
		}
	}
	if resolvedProvider == "codex" && resolvedAuthIndex == "" {
		// quota_cooldowns has no Workspace column. Without the credential locator,
		// the same member email in two Workspaces would collide in the legacy
		// active-identity index even though the JSON evidence differs.
		log.Printf("[quota-auto-disable] auth file %q has no stable Codex auth index; skip quota ownership", candidate.FileName)
		return
	}
	if resolvedAuthIndex == "" && !hasQuotaFallbackIdentity(resolvedProvider, resolvedAccountSnapshot) {
		log.Printf("[quota-auto-disable] auth file %q has no stable auth index or provider/account snapshot identity; skip auto disable/recovery ownership", candidate.FileName)
		return
	}
	preDisabled := current.Disabled
	if preDisabled {
		if w.extendExistingCooldown(ctx, candidate, current) {
			return
		}
		log.Printf("[quota-auto-disable] auth file %q was already disabled without CPAMP ownership; skip auto disable/recovery", candidate.FileName)
		return
	}
	observedEnabledAtMS := time.Now().UnixMilli()

	log.Printf("[quota-auto-disable] quota limit reached for auth file %q account=%q provider=%q resetAt=%s, disabling", candidate.FileName, candidate.DisplayAccount, candidate.Provider, candidate.ResetAt.Format(time.RFC3339))
	if err := w.patchAuthFileTarget(ctx, candidate.BaseURL, candidate.ManagementKey, target, true); err != nil {
		log.Printf("[quota-auto-disable] failed to disable auth file %q: %v", candidate.FileName, err)
		return
	}
	disabledAtMS := time.Now().UnixMilli()
	if disabledAtMS < observedEnabledAtMS {
		disabledAtMS = observedEnabledAtMS
	}

	owner := firstNonEmpty(candidate.Owner, model.QuotaCooldownOwnerUsage429)
	cooldownEvidenceJSON := candidate.EvidenceJSON
	persisted, err := w.store.UpsertQuotaCooldown(ctx, store.QuotaCooldownUpsert{
		AuthFileName:        candidate.FileName,
		AuthIndex:           resolvedAuthIndex,
		AccountSnapshot:     resolvedAccountSnapshot,
		Provider:            resolvedProvider,
		ReasonCode:          candidate.ReasonCode,
		WindowKind:          candidate.WindowKind,
		EvidenceJSON:        cooldownEvidenceJSON,
		RecoverAtMS:         candidate.ResetAt.UnixMilli(),
		Owner:               owner,
		EventHash:           candidate.EventHash,
		PreDisabledState:    preDisabled,
		ObservedEnabledAtMS: observedEnabledAtMS,
		DisabledAtMS:        disabledAtMS,
	})
	if err != nil {
		log.Printf("[quota-auto-disable] disabled auth file %q but failed to persist cooldown ownership: %v", candidate.FileName, err)
		rollbackCtx, cancelRollback := detachedAuthFileMutationContext(ctx, w.compensationTimeout)
		defer cancelRollback()
		rollbackTarget, rollbackErr := cpaauthfiles.New(w.client, quotaAutoDisableActionTimeout).ResolveVerifiedStatusMutationTarget(
			rollbackCtx,
			candidate.BaseURL,
			candidate.ManagementKey,
			cpaauthfiles.Identity{
				AuthFileName:      target.File.Name,
				AuthIndex:         target.File.AuthIndex,
				Provider:          target.File.Provider,
				AccountSnapshot:   target.File.AccountSnapshot,
				AccountIDSnapshot: target.File.AccountID,
			},
		)
		if rollbackErr != nil {
			rollbackErr = fmt.Errorf("revalidate rollback target: %w", rollbackErr)
		} else {
			rollbackErr = w.patchAuthFileTarget(rollbackCtx, candidate.BaseURL, candidate.ManagementKey, rollbackTarget, false)
		}
		if rollbackErr != nil {
			log.Printf("[quota-auto-disable] failed to roll back auth file %q after cooldown persistence error: %v", candidate.FileName, rollbackErr)
		}
		return
	}
	log.Printf("[quota-auto-disable] disabled auth file %q; persisted CPAMP-owned auto-enable at %s", candidate.FileName, time.UnixMilli(persisted.RecoverAtMS).Format(time.RFC3339))
}

func (w *RateLimitAutoDisableWorker) extendExistingCooldown(ctx context.Context, candidate quotaAutoDisableCandidate, current authFile) bool {
	active, err := w.store.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		log.Printf("[quota-auto-disable] failed to check active cooldowns for auth file %q: %v", candidate.FileName, err)
		return false
	}
	owner := firstNonEmpty(candidate.Owner, model.QuotaCooldownOwnerUsage429)
	currentIndex := firstNonEmpty(current.AuthIndex, candidate.AuthIndex)
	currentProvider := normalizeQuotaProvider(firstNonEmpty(current.Provider, candidate.Provider))
	currentSnapshot := firstNonEmpty(
		quotaActionAccountSnapshot(candidate.FileName, candidate.AccountSnapshot),
		quotaActionAccountSnapshot(current.Name, current.AccountSnapshot),
	)
	currentAccountID := firstNonEmpty(candidate.AccountID, current.AccountID)
	if currentProvider == "codex" && currentIndex == "" {
		// Codex mutation ownership requires a credential locator. Workspace and
		// member evidence are not a substitute for file+authIndex.
		return false
	}
	if currentProvider == "codex" {
		if strings.TrimSpace(currentAccountID) != "" {
			workspace, workspaceOK := usageidentity.NormalizeCodexWorkspaceSnapshot(currentAccountID)
			if !workspaceOK {
				log.Printf("[quota-auto-disable] Codex cooldown for auth file %q has invalid workspace evidence", candidate.FileName)
				return false
			}
			currentAccountID = workspace
		}
		if strings.TrimSpace(currentSnapshot) != "" {
			member, memberOK := usageidentity.NormalizeCodexMemberSnapshot(currentSnapshot)
			if memberOK {
				currentSnapshot = member
			} else {
				currentSnapshot = ""
			}
		}
	}
	var existing store.QuotaCooldown
	for _, item := range active {
		if item.AuthFileName == candidate.FileName && item.Owner == owner && quotaCooldownMatchesIdentity(item, currentIndex, currentProvider, currentAccountID, currentSnapshot) {
			existing = item
			break
		}
	}
	if existing.ID == 0 {
		return false
	}
	if existing.AuthIndex != "" && currentIndex != existing.AuthIndex {
		log.Printf("[quota-auto-disable] active cooldown auth index mismatch for auth file %q: stored=%q current=%q", candidate.FileName, existing.AuthIndex, currentIndex)
		return false
	}
	existingSnapshot := quotaActionAccountSnapshot(existing.AuthFileName, existing.AccountSnapshot)
	if existing.AuthIndex == "" {
		existingProvider := normalizeQuotaProvider(existing.Provider)
		if existingProvider != "" && currentProvider != "" && existingProvider != currentProvider {
			log.Printf("[quota-auto-disable] active cooldown provider mismatch for auth file %q: stored=%q current=%q", candidate.FileName, existing.Provider, current.Provider)
			return false
		}
		if existingSnapshot == "" || currentSnapshot == "" {
			log.Printf("[quota-auto-disable] active cooldown for auth file %q has no stable fallback identity", candidate.FileName)
			return false
		}
		if existingSnapshot != currentSnapshot {
			log.Printf("[quota-auto-disable] active cooldown account snapshot mismatch for auth file %q: stored=%q current=%q", candidate.FileName, existingSnapshot, currentSnapshot)
			return false
		}
	}
	candidateRecoverAtMS := candidate.ResetAt.UnixMilli()
	candidateWins := candidateRecoverAtMS >= existing.RecoverAtMS
	finalRecoverAtMS := existing.RecoverAtMS
	primaryEvidence := existing.EvidenceJSON
	supplementalEvidence := candidate.EvidenceJSON
	reasonCode := firstNonEmpty(existing.ReasonCode, candidate.ReasonCode)
	windowKind := firstNonEmpty(existing.WindowKind, candidate.WindowKind)
	eventHash := firstNonEmpty(existing.EventHash, candidate.EventHash)
	evidenceJSON := firstNonEmpty(existing.EvidenceJSON, candidate.EvidenceJSON)
	if candidateWins {
		finalRecoverAtMS = candidateRecoverAtMS
		primaryEvidence = candidate.EvidenceJSON
		supplementalEvidence = existing.EvidenceJSON
		reasonCode = firstNonEmpty(candidate.ReasonCode, existing.ReasonCode)
		windowKind = firstNonEmpty(candidate.WindowKind, existing.WindowKind)
		eventHash = firstNonEmpty(candidate.EventHash, existing.EventHash)
		evidenceJSON = firstNonEmpty(candidate.EvidenceJSON, existing.EvidenceJSON)
	}
	if owner == model.QuotaCooldownOwnerXAIFreeUsage {
		evidenceJSON = mergeXAIProviderUsageEvidence(primaryEvidence, supplementalEvidence, finalRecoverAtMS)
	}
	_, err = w.store.UpsertQuotaCooldown(ctx, store.QuotaCooldownUpsert{
		AuthFileName:     candidate.FileName,
		AuthIndex:        firstNonEmpty(candidate.AuthIndex, existing.AuthIndex, current.AuthIndex),
		AccountSnapshot:  firstNonEmpty(currentSnapshot, existingSnapshot),
		Provider:         firstNonEmpty(currentProvider, normalizeQuotaProvider(existing.Provider)),
		ReasonCode:       reasonCode,
		WindowKind:       windowKind,
		EvidenceJSON:     evidenceJSON,
		RecoverAtMS:      finalRecoverAtMS,
		Owner:            owner,
		EventHash:        eventHash,
		PreDisabledState: false,
		DisabledAtMS:     existing.DisabledAtMS,
	})
	if err != nil {
		log.Printf("[quota-auto-disable] failed to extend active cooldown for auth file %q: %v", candidate.FileName, err)
		return false
	}
	log.Printf("[quota-auto-disable] updated CPAMP-owned auth file %q auto-enable time to %s", candidate.FileName, time.UnixMilli(finalRecoverAtMS).Format(time.RFC3339))
	return true
}

func mergeXAIProviderUsageEvidence(primaryJSON string, supplementalJSON string, recoverAtMS int64) string {
	primary, primaryOK := decodeXAIProviderUsageEvidence(primaryJSON)
	supplemental, supplementalOK := decodeXAIProviderUsageEvidence(supplementalJSON)
	if !primaryOK {
		if !supplementalOK {
			return ""
		}
		primary = supplemental
		supplementalOK = false
	}
	evidenceRecoverAtMS := primary.RecoverAtMS
	if supplementalOK {
		fillMissingXAIProviderUsageEvidence(&primary, supplemental)
		if evidenceRecoverAtMS == 0 && recoverAtMS > 0 && supplemental.RecoverAtMS == recoverAtMS {
			// The winning evidence omitted recovery, but the supplemental event
			// describes the same final schedule, so its source remains valid.
			primary.RecoverAtEstimated = supplemental.RecoverAtEstimated
			evidenceRecoverAtMS = supplemental.RecoverAtMS
		}
	}
	if recoverAtMS > 0 {
		primary.RecoverAtMS = recoverAtMS
		if evidenceRecoverAtMS != recoverAtMS {
			// The evidence that owns the final cooldown did not carry this recovery
			// time. Keep the schedule, but do not present a supplemental event's
			// reported/estimated source as if it belonged to the winning event.
			primary.RecoverAtEstimated = true
		}
	}
	normalized := usage.NormalizeProviderUsageMetadata(&primary)
	if normalized == nil {
		return ""
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	return string(raw)
}

func decodeXAIProviderUsageEvidence(raw string) (usage.ProviderUsageMetadata, bool) {
	var evidence usage.ProviderUsageMetadata
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &evidence); err != nil {
		return usage.ProviderUsageMetadata{}, false
	}
	normalized := usage.NormalizeProviderUsageMetadata(&evidence)
	if normalized == nil || normalized.Provider != "xai" || normalized.Code != usage.ProviderUsageCodeXAIFree {
		return usage.ProviderUsageMetadata{}, false
	}
	return *normalized, true
}

func fillMissingXAIProviderUsageEvidence(target *usage.ProviderUsageMetadata, source usage.ProviderUsageMetadata) {
	if target == nil {
		return
	}
	if target.Kind == "" {
		target.Kind = source.Kind
	}
	if target.State == "" {
		target.State = source.State
	}
	if target.Model == "" {
		target.Model = source.Model
	}
	if target.Unit == "" {
		target.Unit = source.Unit
	}
	if target.Actual == nil {
		target.Actual = source.Actual
	}
	if target.Limit == nil {
		target.Limit = source.Limit
	}
	if target.Remaining == nil {
		target.Remaining = source.Remaining
	}
	if target.Overage == nil {
		target.Overage = source.Overage
	}
	if target.WindowKind == "" {
		target.WindowKind = source.WindowKind
	}
	if target.ObservedAtMS == 0 {
		target.ObservedAtMS = source.ObservedAtMS
	}
	if target.Source == "" {
		target.Source = source.Source
	}
}

func (w *RateLimitAutoDisableWorker) enableDue(ctx context.Context, now time.Time) {
	if w == nil {
		return
	}
	w.operationMu.Lock()
	defer w.operationMu.Unlock()
	w.enableDueLocked(ctx, now)
}

func (w *RateLimitAutoDisableWorker) enableDueLocked(ctx context.Context, now time.Time) {
	if w.store == nil || w.store.QuotaCooldowns == nil {
		return
	}
	baseURL, managementKey := w.runtimeConfig()
	if baseURL == "" || managementKey == "" {
		return
	}
	due, err := w.store.ListDueQuotaCooldowns(ctx, now.UnixMilli(), quotaCooldownDueLimit)
	if err != nil {
		log.Printf("[quota-auto-disable] failed to list due quota cooldowns: %v", err)
		return
	}
	for _, item := range due {
		w.recoverCooldown(ctx, baseURL, managementKey, item, now)
	}
}

func (w *RateLimitAutoDisableWorker) recoverCooldown(ctx context.Context, baseURL string, managementKey string, item store.QuotaCooldown, now time.Time) {
	if item.Owner != model.QuotaCooldownOwnerUsage429 && item.Owner != model.QuotaCooldownOwnerXAIFreeUsage {
		reason := "unknown owner"
		_ = w.store.MarkQuotaCooldownSkipped(ctx, item.ID, reason)
		log.Printf("[quota-auto-disable] skip cooldown recovery id=%d authFile=%q reason=%s owner=%q", item.ID, item.AuthFileName, reason, item.Owner)
		return
	}
	if item.PreDisabledState {
		reason := "pre-disabled before CPAMP action"
		_ = w.store.MarkQuotaCooldownSkipped(ctx, item.ID, reason)
		log.Printf("[quota-auto-disable] skip cooldown recovery id=%d authFile=%q reason=%s", item.ID, item.AuthFileName, reason)
		return
	}
	authIndex := strings.TrimSpace(item.AuthIndex)
	accountSnapshot := quotaActionAccountSnapshot(item.AuthFileName, item.AccountSnapshot)
	provider := normalizeQuotaProvider(item.Provider)
	accountID := ""
	if provider == "codex" {
		if strings.TrimSpace(accountSnapshot) != "" {
			member, memberOK := usageidentity.NormalizeCodexMemberSnapshot(accountSnapshot)
			if memberOK {
				accountSnapshot = member
			} else {
				accountSnapshot = ""
			}
		}
		var member string
		var identityPresent, identityOK bool
		accountID, member, identityPresent, identityOK = codexCooldownIdentity(item)
		if !identityOK {
			reason := "Codex cooldown has conflicting workspace/member evidence"
			_ = w.store.MarkQuotaCooldownSkipped(ctx, item.ID, reason)
			log.Printf("[quota-auto-disable] skip cooldown recovery id=%d authFile=%q reason=%s", item.ID, item.AuthFileName, reason)
			return
		}
		if identityPresent && accountSnapshot == "" {
			accountSnapshot = member
		}
	}
	if provider == "codex" && authIndex == "" {
		reason := "Codex cooldown has no auth index; credential locator is not recoverable"
		_ = w.store.MarkQuotaCooldownSkipped(ctx, item.ID, reason)
		log.Printf("[quota-auto-disable] skip cooldown recovery id=%d authFile=%q reason=%s", item.ID, item.AuthFileName, reason)
		return
	}
	if authIndex == "" && !hasQuotaFallbackIdentity(provider, accountSnapshot) {
		reason := "cooldown identity has no stable auth index or provider/account snapshot identity"
		_ = w.store.MarkQuotaCooldownSkipped(ctx, item.ID, reason)
		log.Printf("[quota-auto-disable] skip cooldown recovery id=%d authFile=%q reason=%s", item.ID, item.AuthFileName, reason)
		return
	}
	if w.authFileMutations == nil {
		_ = w.store.RecordQuotaCooldownFailure(ctx, item.ID, cpaauthfiles.ErrMutationCoordinatorUnavailable.Error())
		return
	}
	releaseMutation, err := w.authFileMutations.Acquire(ctx, item.AuthFileName)
	if err != nil {
		_ = w.store.RecordQuotaCooldownFailure(ctx, item.ID, err.Error())
		log.Printf("[quota-auto-disable] failed to coordinate auth file %q recovery: %v", item.AuthFileName, err)
		return
	}
	defer releaseMutation()
	target, ok, err := w.currentAuthFileTarget(ctx, baseURL, managementKey, cpaauthfiles.Identity{
		AuthFileName:      item.AuthFileName,
		AuthIndex:         authIndex,
		Provider:          provider,
		AccountSnapshot:   accountSnapshot,
		AccountIDSnapshot: accountID,
	})
	if err != nil {
		if errors.Is(err, cpaauthfiles.ErrIdentityMismatch) {
			reason := "auth file identity changed before cooldown recovery"
			_ = w.store.MarkQuotaCooldownSkipped(ctx, item.ID, reason)
			log.Printf("[quota-auto-disable] skip cooldown recovery id=%d authFile=%q reason=%s: %v", item.ID, item.AuthFileName, reason, err)
			return
		}
		_ = w.store.RecordQuotaCooldownFailure(ctx, item.ID, err.Error())
		log.Printf("[quota-auto-disable] failed to verify auth file %q before recovery: %v", item.AuthFileName, err)
		return
	}
	if !ok {
		_ = w.store.MarkQuotaCooldownSkipped(ctx, item.ID, "auth file missing or auth index mismatch")
		log.Printf("[quota-auto-disable] auth file %q authIndex=%q missing/mismatched, skip auto-enable", item.AuthFileName, item.AuthIndex)
		return
	}
	if !target.File.Disabled {
		if err := w.store.MarkQuotaCooldownRecovered(ctx, item.ID, now.UnixMilli()); err != nil {
			_ = w.store.RecordQuotaCooldownFailure(ctx, item.ID, fmt.Sprintf("mark already-enabled cooldown recovered: %v", err))
			log.Printf("[quota-auto-disable] auth file %q is enabled but failed to mark cooldown recovered: %v", item.AuthFileName, err)
			return
		}
		log.Printf("[quota-auto-disable] auth file %q already enabled; marked cooldown recovered", item.AuthFileName)
		return
	}

	log.Printf("[quota-auto-disable] reset time reached for auth file %q account=%q, enabling", item.AuthFileName, item.AccountSnapshot)
	if err := w.patchAuthFileTarget(ctx, baseURL, managementKey, target, false); err != nil {
		_ = w.store.RecordQuotaCooldownFailure(ctx, item.ID, err.Error())
		log.Printf("[quota-auto-disable] failed to enable auth file %q: %v", item.AuthFileName, err)
		return
	}
	if err := w.store.MarkQuotaCooldownRecovered(ctx, item.ID, now.UnixMilli()); err != nil {
		rollbackCtx, cancelRollback := detachedAuthFileMutationContext(ctx, w.compensationTimeout)
		defer cancelRollback()
		rollbackTarget, rollbackErr := cpaauthfiles.New(w.client, quotaAutoDisableActionTimeout).ResolveVerifiedStatusMutationTarget(
			rollbackCtx,
			baseURL,
			managementKey,
			cpaauthfiles.Identity{
				AuthFileName:      target.File.Name,
				AuthIndex:         target.File.AuthIndex,
				Provider:          target.File.Provider,
				AccountSnapshot:   target.File.AccountSnapshot,
				AccountIDSnapshot: target.File.AccountID,
			},
		)
		if rollbackErr != nil {
			rollbackErr = fmt.Errorf("revalidate rollback target: %w", rollbackErr)
		} else {
			rollbackErr = w.patchAuthFileTarget(rollbackCtx, baseURL, managementKey, rollbackTarget, true)
		}
		reason := fmt.Sprintf("cooldown recovery marker persistence failed: %v", err)
		if rollbackErr != nil {
			reason += fmt.Sprintf("; rollback disable failed: %v", rollbackErr)
		}
		_ = w.store.RecordQuotaCooldownFailure(rollbackCtx, item.ID, reason)
		log.Printf("[quota-auto-disable] enabled auth file %q but failed to mark cooldown recovered; rollbackErr=%v: %v", item.AuthFileName, rollbackErr, err)
		return
	}
	log.Printf("[quota-auto-disable] enabled auth file %q after quota cooldown", item.AuthFileName)
}

func quotaAutoDisableCandidateFromEvent(event usage.Event, baseURL string, managementKey string, now time.Time) (quotaAutoDisableCandidate, bool) {
	if resetAt, ok := xaiFreeUsageResetTimeFromEvent(event, now); ok {
		fileName := strings.TrimSpace(event.AuthFileSnapshot)
		if fileName == "" {
			log.Printf("[quota-auto-disable] xAI free-usage event %q has no auth file snapshot, skip auto disable", event.EventHash)
			return quotaAutoDisableCandidate{}, false
		}
		return quotaAutoDisableCandidate{
			BaseURL:         baseURL,
			ManagementKey:   managementKey,
			FileName:        fileName,
			AuthIndex:       strings.TrimSpace(event.AuthIndex),
			DisplayAccount:  firstNonEmpty(event.AccountSnapshot, event.AuthLabelSnapshot, event.Source, fileName),
			AccountSnapshot: quotaActionAccountSnapshot(fileName, event.AccountSnapshot),
			Provider:        "xai",
			ReasonCode:      quotaReasonXAIFreeUsage,
			WindowKind:      quotaWindowRolling24H,
			ResetAt:         resetAt,
			EventHash:       event.EventHash,
			Reason:          event.FailSummary,
			Owner:           model.QuotaCooldownOwnerXAIFreeUsage,
			EvidenceJSON:    xaiProviderUsageEvidenceJSON(event, resetAt, now),
		}, true
	}
	resetAt, ok := codexUsageLimitResetTimeFromEvent(event, now)
	if !ok {
		return quotaAutoDisableCandidate{}, false
	}
	fileName := strings.TrimSpace(event.AuthFileSnapshot)
	if fileName == "" {
		log.Printf("[quota-auto-disable] Codex usage-limit event %q has no auth file snapshot, skip auto disable", event.EventHash)
		return quotaAutoDisableCandidate{}, false
	}
	return quotaAutoDisableCandidate{
		BaseURL:         baseURL,
		ManagementKey:   managementKey,
		FileName:        fileName,
		AuthIndex:       strings.TrimSpace(event.AuthIndex),
		DisplayAccount:  firstNonEmpty(event.AccountSnapshot, event.AuthLabelSnapshot, event.Source, fileName),
		AccountSnapshot: quotaActionAccountSnapshot(fileName, event.AccountSnapshot),
		AccountID:       strings.TrimSpace(event.AuthAccountIDSnapshot),
		Provider:        "codex",
		ReasonCode:      quotaReasonCodexUsageLimit,
		WindowKind:      codexQuotaWindowKindFromEvent(event),
		ResetAt:         resetAt,
		EventHash:       event.EventHash,
		Reason:          event.FailSummary,
		Owner:           model.QuotaCooldownOwnerUsage429,
	}, true
}

func xaiFreeUsageResetTimeFromEvent(event usage.Event, now time.Time) (time.Time, bool) {
	if !event.Failed || (event.FailStatusCode != http.StatusPaymentRequired && event.FailStatusCode != http.StatusTooManyRequests) {
		return time.Time{}, false
	}
	if !isXAIFreeUsageProvider(event) {
		return time.Time{}, false
	}
	observedAt := xaiFreeUsageObservedAt(event, now)
	texts := []string{event.FailBody, event.RawJSON, event.FailSummary}
	if providerUsage := xaiProviderUsageFromEvent(event, now); providerUsage != nil && strings.EqualFold(providerUsage.Code, usage.ProviderUsageCodeXAIFree) {
		// Free-usage recovery is quota-window based. Transport Retry-After only
		// describes short request backoff and must not drive credential cooldown.
		if providerUsage.RecoverAtMS > 0 && !providerUsage.RecoverAtEstimated {
			resetAt := time.UnixMilli(providerUsage.RecoverAtMS)
			return resetAt, resetAt.After(now)
		}
		if resetAt, ok := xaiFreeUsageResetTimeFromTexts(texts, observedAt); ok {
			return resetAt, resetAt.After(now)
		}
		if providerUsage.RecoverAtMS > 0 {
			resetAt := time.UnixMilli(providerUsage.RecoverAtMS)
			return resetAt, resetAt.After(now)
		}
		resetAt := observedAt.Add(xaiFreeUsageCooldown)
		return resetAt, resetAt.After(now)
	}
	matched := false
	for _, text := range texts {
		forEachJSONValue(text, func(decoded any) bool {
			if xaiFreeUsageCode(decoded) {
				matched = true
				return true
			}
			return false
		})
		if matched {
			break
		}
	}
	if matched {
		if resetAt, ok := xaiFreeUsageResetTimeFromTexts(texts, observedAt); ok {
			return resetAt, resetAt.After(now)
		}
		resetAt := observedAt.Add(xaiFreeUsageCooldown)
		return resetAt, resetAt.After(now)
	}
	return time.Time{}, false
}

func xaiFreeUsageObservedAt(event usage.Event, fallback time.Time) time.Time {
	if event.TimestampMS > 0 {
		return time.UnixMilli(event.TimestampMS)
	}
	return fallback
}

func xaiProviderUsageFromEvent(event usage.Event, now time.Time) *usage.ProviderUsageMetadata {
	metadata := event.ResponseMetadata
	if metadata == nil && event.ResponseMetadataJSON != "" {
		metadata = usage.ResponseHeaderMetadataFromJSON(event.ResponseMetadataJSON)
	}
	if metadata != nil && metadata.ProviderUsage != nil {
		return metadata.ProviderUsage
	}
	base := xaiFreeUsageObservedAt(event, now)
	if event.RawJSON != "" {
		if parsed := usage.ParseResponseHeaderMetadataFromRawJSON(event.RawJSON, base); parsed != nil && parsed.ProviderUsage != nil {
			return parsed.ProviderUsage
		}
	}
	record := map[string]any{
		"provider":               firstNonEmpty(event.Provider, event.AuthProviderSnapshot),
		"auth_provider_snapshot": event.AuthProviderSnapshot,
		"executor_type":          event.ExecutorType,
		"fail": map[string]any{
			"status_code": event.FailStatusCode,
			"body":        event.FailBody,
		},
	}
	return usage.ProviderUsageMetadataFromRecord(record, base)
}

func xaiProviderUsageEvidenceJSON(event usage.Event, resetAt time.Time, now time.Time) string {
	providerUsage := xaiProviderUsageFromEvent(event, now)
	if providerUsage == nil {
		providerUsage = &usage.ProviderUsageMetadata{
			Provider:     "xai",
			Kind:         usage.ProviderUsageKindIncludedFree,
			State:        usage.ProviderUsageStateExhausted,
			Code:         usage.ProviderUsageCodeXAIFree,
			Unit:         "tokens",
			WindowKind:   usage.ProviderUsageWindowRolling24H,
			ObservedAtMS: xaiFreeUsageObservedAt(event, now).UnixMilli(),
		}
		if model := strings.TrimSpace(event.Model); model != "" && model != "-" {
			providerUsage.Model = model
		}
	}
	evidence := *providerUsage
	if !resetAt.IsZero() {
		evidence.RecoverAtMS = resetAt.UnixMilli()
		switch {
		case xaiFreeUsageHasExplicitReset(event, now):
			evidence.RecoverAtEstimated = false
		case providerUsage.RecoverAtMS == evidence.RecoverAtMS:
			// Preserve recovery provenance carried by structured metadata when the
			// raw response body is no longer available on an imported event.
			evidence.RecoverAtEstimated = providerUsage.RecoverAtEstimated
		default:
			evidence.RecoverAtEstimated = true
		}
	}
	normalized := usage.NormalizeProviderUsageMetadata(&evidence)
	if normalized == nil {
		return ""
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	return string(raw)
}

func xaiFreeUsageHasExplicitReset(event usage.Event, now time.Time) bool {
	base := xaiFreeUsageObservedAt(event, now)
	_, ok := xaiFreeUsageResetTimeFromTexts([]string{event.FailBody, event.RawJSON, event.FailSummary}, base)
	return ok
}

func xaiFreeUsageResetTimeFromJSONText(text string, base time.Time) (time.Time, bool) {
	if resetAt, ok := xaiResetTimeFromJSONText(text, base, xaiAbsoluteResetKeys, false); ok {
		return resetAt, true
	}
	return xaiResetTimeFromJSONText(text, base, xaiRelativeResetKeys, true)
}

var (
	xaiAbsoluteResetKeys = []string{
		"reset_at", "resetAt", "resets_at", "resetsAt",
		"period_end", "periodEnd", "billing_period_end", "billingPeriodEnd",
	}
	xaiRelativeResetKeys = []string{"reset_after_seconds", "resetAfterSeconds"}
)

func xaiFreeUsageResetTimeFromTexts(texts []string, base time.Time) (time.Time, bool) {
	for _, candidate := range []struct {
		keys     []string
		relative bool
	}{
		{keys: xaiAbsoluteResetKeys},
		{keys: xaiRelativeResetKeys, relative: true},
	} {
		for _, text := range texts {
			if resetAt, ok := xaiResetTimeFromJSONText(text, base, candidate.keys, candidate.relative); ok {
				return resetAt, true
			}
		}
	}
	return time.Time{}, false
}

func xaiResetTimeFromJSONText(text string, base time.Time, keys []string, relative bool) (time.Time, bool) {
	var resetAt time.Time
	found := false
	forEachJSONValue(text, func(decoded any) bool {
		if at, ok := xaiResetTimeByKeys(decoded, base, keys, relative); ok {
			resetAt = at
			found = true
			return true
		}
		return false
	})
	return resetAt, found && resetAt.After(base)
}

func xaiResetTimeByKeys(value any, base time.Time, keys []string, relative bool) (time.Time, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range keys {
			if raw, ok := typed[key]; ok {
				if resetAt, ok := parseResetValue(raw, base, relative); ok {
					return resetAt, true
				}
			}
		}
		childKeys := make([]string, 0, len(typed))
		for key := range typed {
			childKeys = append(childKeys, key)
		}
		sort.Strings(childKeys)
		for _, key := range childKeys {
			if isResponseHeaderContainer(key) {
				continue
			}
			if resetAt, ok := xaiResetTimeByKeys(typed[key], base, keys, relative); ok {
				return resetAt, true
			}
		}
	case []any:
		for _, child := range typed {
			if resetAt, ok := xaiResetTimeByKeys(child, base, keys, relative); ok {
				return resetAt, true
			}
		}
	}
	return time.Time{}, false
}

func isResponseHeaderContainer(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.NewReplacer("-", "_", " ", "_").Replace(normalized)
	return normalized == "headers" || normalized == "response_headers" || normalized == "responseheaders"
}

func xaiFreeUsageCode(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(typed["code"])), usage.ProviderUsageCodeXAIFree) {
			return true
		}
		for _, child := range typed {
			if xaiFreeUsageCode(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if xaiFreeUsageCode(child) {
				return true
			}
		}
	}
	return false
}

// isXAIFreeUsageProvider requires a native xAI identity. Bare "grok" alone can
// name openai-compatible proxies and must not trigger free-usage cooldown.
func isXAIFreeUsageProvider(event usage.Event) bool {
	return usage.IsNativeXAIProvider(event.Provider, event.AuthProviderSnapshot, event.ExecutorType)
}

func codexUsageLimitResetTimeFromEvent(event usage.Event, now time.Time) (time.Time, bool) {
	if !event.Failed || event.FailStatusCode != http.StatusTooManyRequests {
		return time.Time{}, false
	}
	provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(event.Provider, event.AuthProviderSnapshot)))
	if provider != "codex" {
		return time.Time{}, false
	}
	if resetAt, ok := codexUsageLimitResetTimeFromHeaders(event, now); ok {
		return resetAt, true
	}
	for _, text := range []string{event.FailBody, event.RawJSON, event.FailSummary} {
		var resetAt time.Time
		found := false
		forEachJSONValue(text, func(decoded any) bool {
			if at, ok := usageLimitResetFromJSON(decoded, now); ok {
				resetAt = at
				found = true
				return true
			}
			return false
		})
		if found {
			return resetAt, true
		}
	}
	return time.Time{}, false
}

func codexUsageLimitResetTimeFromHeaders(event usage.Event, now time.Time) (time.Time, bool) {
	metadata := event.ResponseMetadata
	if metadata == nil && event.ResponseMetadataJSON != "" {
		metadata = usage.ResponseHeaderMetadataFromJSON(event.ResponseMetadataJSON)
	}
	if metadata == nil {
		return time.Time{}, false
	}
	resetAtMS := int64(0)
	if !codexUsageLimitSignalFromHeaders(event, metadata) {
		return time.Time{}, false
	}
	if metadata.Quota != nil {
		resetAtMS = codexQuotaReachedResetAtMS(metadata.Quota)
	}
	if resetAtMS <= 0 && metadata.Errors != nil {
		resetAtMS = metadata.Errors.RetryAfterRecoverAtMS
	}
	if resetAtMS <= 0 {
		return time.Time{}, false
	}
	resetAt := time.UnixMilli(resetAtMS)
	return resetAt, resetAt.After(now)
}

func codexUsageLimitSignalFromHeaders(event usage.Event, metadata *usage.ResponseHeaderMetadata) bool {
	if metadata == nil {
		return false
	}
	if metadata.Quota != nil && strings.TrimSpace(metadata.Quota.RateLimitReachedType) != "" {
		return true
	}
	if metadata.Quota != nil && codexQuotaHasFullWindow(metadata.Quota) {
		return true
	}
	values := []string{event.HeaderErrorKind, event.HeaderErrorCode}
	if metadata.Errors != nil {
		values = append(
			values,
			metadata.Errors.Kind,
			metadata.Errors.Code,
			metadata.Errors.AuthorizationError,
			metadata.Errors.IDEErrorCode,
			metadata.Errors.IDERootErrorCode,
		)
	}
	for _, value := range values {
		if isCodexUsageLimitSignalText(value) {
			return true
		}
	}
	return false
}

func isCodexUsageLimitSignalText(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	return strings.Contains(normalized, "usage_limit_reached")
}

func codexQuotaReachedResetAtMS(quota *usage.HeaderQuotaMetadata) int64 {
	if quota == nil {
		return 0
	}
	switch strings.ToLower(strings.TrimSpace(quota.RateLimitReachedType)) {
	case "primary":
		return quotaWindowResetAtMS(quota.Primary)
	case "secondary":
		return quotaWindowResetAtMS(quota.Secondary)
	}
	if strings.TrimSpace(quota.ReachedWindowKind) != "" && quota.RecoverAtMS > 0 {
		return quota.RecoverAtMS
	}
	return codexQuotaFullWindowResetAtMS(quota)
}

func codexQuotaWindowKindFromEvent(event usage.Event) string {
	metadata := event.ResponseMetadata
	if metadata == nil && event.ResponseMetadataJSON != "" {
		metadata = usage.ResponseHeaderMetadataFromJSON(event.ResponseMetadataJSON)
	}
	if metadata == nil || metadata.Quota == nil {
		return quotaWindowUnknown
	}
	quota := metadata.Quota
	if kind := strings.TrimSpace(quota.ReachedWindowKind); kind != "" {
		return kind
	}
	switch strings.ToLower(strings.TrimSpace(quota.RateLimitReachedType)) {
	case "primary":
		return quotaWindowKind(quota.Primary)
	case "secondary":
		return quotaWindowKind(quota.Secondary)
	}
	return quotaWindowUnknown
}

func quotaWindowKind(window *usage.HeaderQuotaWindow) string {
	if window == nil || window.WindowMinutes == nil {
		return quotaWindowUnknown
	}
	minutes := *window.WindowMinutes
	switch {
	case minutes >= 299 && minutes <= 301:
		return "five_hour"
	case minutes >= 10_079 && minutes <= 10_081:
		return "weekly"
	case minutes >= 40_319 && minutes <= 44_641:
		return "monthly"
	default:
		return quotaWindowUnknown
	}
}

func codexQuotaHasFullWindow(quota *usage.HeaderQuotaMetadata) bool {
	if quota == nil {
		return false
	}
	return quotaWindowUsedAtLimit(quota.Primary) || quotaWindowUsedAtLimit(quota.Secondary)
}

func codexQuotaFullWindowResetAtMS(quota *usage.HeaderQuotaMetadata) int64 {
	if quota == nil {
		return 0
	}
	resetAtMS := int64(0)
	for _, window := range []*usage.HeaderQuotaWindow{quota.Primary, quota.Secondary} {
		if !quotaWindowUsedAtLimit(window) {
			continue
		}
		if reset := quotaWindowResetAtMS(window); reset > resetAtMS {
			resetAtMS = reset
		}
	}
	return resetAtMS
}

func quotaWindowUsedAtLimit(window *usage.HeaderQuotaWindow) bool {
	return window != nil && window.UsedPercent != nil && *window.UsedPercent >= 100
}

func quotaWindowResetAtMS(window *usage.HeaderQuotaWindow) int64 {
	if window == nil {
		return 0
	}
	return window.ResetAtMS
}

// forEachJSONValue decodes every JSON value found in text, calling fn for each.
// It handles concatenated JSON values (e.g. body + headers) and text with
// non-JSON prefixes (HTML, plain text) by scanning for embedded JSON objects.
func forEachJSONValue(text string, fn func(any) bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if tryDecodeAllJSON(text, fn) {
		return
	}
	for i := 0; i < len(text); i++ {
		if text[i] == '{' || text[i] == '[' {
			if tryDecodeAllJSON(text[i:], fn) {
				return
			}
		}
	}
}

func tryDecodeAllJSON(text string, fn func(any) bool) bool {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	for {
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return false
		}
		if fn(decoded) {
			return true
		}
	}
}

func usageLimitResetFromJSON(value any, now time.Time) (time.Time, bool) {
	switch typed := value.(type) {
	case map[string]any:
		if isUsageLimitMap(typed) {
			if resetAt, ok := explicitCodexResetTime(typed, now); ok {
				return resetAt, true
			}
		}
		if rawError, ok := typed["error"]; ok {
			if errorMap, ok := rawError.(map[string]any); ok && isUsageLimitMap(errorMap) {
				if resetAt, ok := explicitCodexResetTime(errorMap, now); ok {
					return resetAt, true
				}
				if resetAt, ok := explicitCodexResetTime(typed, now); ok {
					return resetAt, true
				}
			}
		}
		for _, child := range typed {
			if resetAt, ok := usageLimitResetFromJSON(child, now); ok {
				return resetAt, true
			}
		}
	case []any:
		for _, child := range typed {
			if resetAt, ok := usageLimitResetFromJSON(child, now); ok {
				return resetAt, true
			}
		}
	}
	return time.Time{}, false
}

func isUsageLimitMap(value map[string]any) bool {
	return strings.EqualFold(strings.TrimSpace(fmt.Sprint(value["type"])), "usage_limit_reached")
}

func explicitCodexResetTime(value map[string]any, now time.Time) (time.Time, bool) {
	for _, key := range []string{"resets_at", "resetsAt"} {
		if raw, ok := value[key]; ok {
			return parseResetValue(raw, now, false)
		}
	}
	for _, key := range []string{"resets_in_seconds", "resetsInSeconds"} {
		if raw, ok := value[key]; ok {
			return parseResetValue(raw, now, true)
		}
	}
	return time.Time{}, false
}

func parseResetValue(value any, now time.Time, relative bool) (time.Time, bool) {
	if value == nil {
		return time.Time{}, false
	}
	switch typed := value.(type) {
	case json.Number:
		return parseResetNumberString(typed.String(), now, relative)
	case float64:
		return resetTimeFromNumber(typed, now, relative)
	case int:
		return resetTimeFromNumber(float64(typed), now, relative)
	case int64:
		return resetTimeFromNumber(float64(typed), now, relative)
	case string:
		return parseResetNumberString(strings.TrimSpace(typed), now, relative)
	default:
		return parseResetNumberString(strings.TrimSpace(fmt.Sprint(typed)), now, relative)
	}
}

func parseResetNumberString(text string, now time.Time, relative bool) (time.Time, bool) {
	if text == "" || strings.EqualFold(text, "null") {
		return time.Time{}, false
	}
	if !relative {
		if parsed, ok := parseCommonTime(text); ok {
			return parsed, true
		}
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || value <= 0 {
		return time.Time{}, false
	}
	return resetTimeFromNumber(value, now, relative)
}

func resetTimeFromNumber(value float64, now time.Time, relative bool) (time.Time, bool) {
	if value <= 0 {
		return time.Time{}, false
	}
	if relative {
		return now.Add(time.Duration(value * float64(time.Second))), true
	}
	// Unix milliseconds, e.g. JavaScript timestamps.
	if value > 1_000_000_000_000 {
		return time.UnixMilli(int64(value)), true
	}
	// Unix seconds.
	if value > 1_000_000_000 {
		return time.Unix(int64(value), 0), true
	}
	return time.Time{}, false
}

func parseCommonTime(text string) (time.Time, bool) {
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		time.RFC1123,
		time.RFC1123Z,
		"2006-01-02T15:04:05.000Z07:00",
		"2006-01-02 15:04:05 MST",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func (w *RateLimitAutoDisableWorker) currentAuthFileTarget(ctx context.Context, baseURL string, managementKey string, identity cpaauthfiles.Identity) (cpaauthfiles.StatusMutationTarget, bool, error) {
	target, err := cpaauthfiles.New(w.client, quotaAutoDisableActionTimeout).ResolveVerifiedStatusMutationTarget(ctx, baseURL, managementKey, identity)
	if errors.Is(err, cpaauthfiles.ErrAuthFileNotFound) {
		return cpaauthfiles.StatusMutationTarget{}, false, nil
	}
	if err != nil {
		return cpaauthfiles.StatusMutationTarget{}, false, err
	}
	return target, true, nil
}

func quotaActionAccountSnapshot(fileName string, value string) string {
	snapshot := strings.TrimSpace(value)
	if snapshot == "" || snapshot == strings.TrimSpace(fileName) {
		return ""
	}
	return snapshot
}

func normalizeQuotaProvider(value string) string {
	provider := strings.ToLower(strings.TrimSpace(value))
	provider = strings.ReplaceAll(provider, "_", "-")
	switch provider {
	case "x-ai", "grok":
		return "xai"
	default:
		return provider
	}
}

func hasQuotaFallbackIdentity(provider string, accountSnapshot string) bool {
	return normalizeQuotaProvider(provider) != "" && strings.TrimSpace(accountSnapshot) != ""
}

func quotaCooldownMatchesIdentity(item store.QuotaCooldown, authIndex string, provider string, accountID string, accountSnapshot string) bool {
	itemAuthIndex := strings.TrimSpace(item.AuthIndex)
	authIndex = strings.TrimSpace(authIndex)
	provider = normalizeQuotaProvider(provider)
	itemProvider := normalizeQuotaProvider(item.Provider)
	if provider == "codex" || itemProvider == "codex" {
		if provider != "codex" || itemProvider != "codex" || itemAuthIndex == "" || authIndex == "" || itemAuthIndex != authIndex {
			return false
		}
		storedWorkspace, storedMember, identityPresent, identityOK := codexCooldownIdentity(item)
		if !identityOK {
			return false
		}
		currentWorkspace, workspaceOK := optionalCodexWorkspace(accountID)
		currentMember, memberOK := optionalCodexMember(accountSnapshot)
		if storedMemberFromColumn, ok := optionalCodexMember(item.AccountSnapshot); ok && memberOK && storedMemberFromColumn != currentMember {
			return false
		}
		if identityPresent {
			if workspaceOK && storedWorkspace != currentWorkspace {
				return false
			}
			if memberOK && storedMember != currentMember {
				return false
			}
		}
		return true
	}
	if itemAuthIndex != "" {
		return authIndex != "" && itemAuthIndex == authIndex
	}

	itemSnapshot := quotaActionAccountSnapshot(item.AuthFileName, item.AccountSnapshot)
	if itemSnapshot == "" || accountSnapshot == "" || itemSnapshot != accountSnapshot {
		return false
	}
	return itemProvider != "" && provider != "" && itemProvider == provider
}

func codexCooldownIdentityEvidenceJSON(workspace string, member string) string {
	workspace, workspaceOK := usageidentity.NormalizeCodexWorkspaceSnapshot(workspace)
	member, memberOK := usageidentity.NormalizeCodexMemberSnapshot(member)
	if !workspaceOK || !memberOK {
		return ""
	}
	raw, err := json.Marshal(codexCooldownIdentityEvidence{Workspace: workspace, Member: member})
	if err != nil {
		return ""
	}
	return string(raw)
}

func optionalCodexWorkspace(value string) (string, bool) {
	if strings.TrimSpace(value) == "" {
		return "", false
	}
	return usageidentity.NormalizeCodexWorkspaceSnapshot(value)
}

func optionalCodexMember(value string) (string, bool) {
	if strings.TrimSpace(value) == "" {
		return "", false
	}
	return usageidentity.NormalizeCodexMemberSnapshot(value)
}

// codexCooldownIdentity reads only the optional identity-shaped fields from
// EvidenceJSON. Quota/provider evidence may use the same column and must not
// become an implicit recovery prerequisite.
func codexCooldownIdentity(item store.QuotaCooldown) (workspace string, member string, present bool, ok bool) {
	raw := strings.TrimSpace(item.EvidenceJSON)
	if raw == "" {
		return "", "", false, true
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return "", "", false, true
	}
	workspaceRaw, workspacePresent := fields["workspace"]
	memberRaw, memberPresent := fields["member"]
	if !workspacePresent && !memberPresent {
		return "", "", false, true
	}
	if err := json.Unmarshal(workspaceRaw, &workspace); err != nil {
		return "", "", true, false
	}
	if err := json.Unmarshal(memberRaw, &member); err != nil {
		return "", "", true, false
	}
	workspace, workspaceOK := optionalCodexWorkspace(workspace)
	member, memberOK := optionalCodexMember(member)
	if !workspaceOK || !memberOK {
		return "", "", true, false
	}
	if storedMember, storedMemberOK := optionalCodexMember(item.AccountSnapshot); storedMemberOK && storedMember != member {
		return "", "", true, false
	}
	return workspace, member, true, true
}

func (w *RateLimitAutoDisableWorker) patchAuthFileTarget(ctx context.Context, baseURL string, managementKey string, target cpaauthfiles.StatusMutationTarget, disabled bool) error {
	return cpaauthfiles.New(w.client, quotaAutoDisableActionTimeout).PatchDisabledTarget(ctx, baseURL, managementKey, target, disabled)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// NormalizeBaseURL is exported for legacy tests.
var NormalizeBaseURL = cpa.NormalizeBaseURL
