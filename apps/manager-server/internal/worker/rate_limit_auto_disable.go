package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
)

const (
	quotaAutoDisableQueueSize     = 256
	quotaAutoDisableDefaultTick   = 15 * time.Second
	quotaAutoDisableActionTimeout = 15 * time.Second
	quotaCooldownDueLimit         = 100
)

// RateLimitAutoDisableWorker reacts to request-monitoring events in near real time.
// It only handles Codex 429 usage_limit_reached responses that include an explicit
// reset time. Disables are persisted with CPAMP ownership, so recovery never relies
// solely on in-memory timers and never re-enables pre-existing/manual disables.
type RateLimitAutoDisableWorker struct {
	store  *store.Store
	client *http.Client

	jobs chan quotaAutoDisableCandidate

	mu                  sync.RWMutex
	baseURL             string
	managementKey       string
	enableCheckInterval time.Duration
}

type quotaAutoDisableCandidate struct {
	BaseURL        string
	ManagementKey  string
	FileName       string
	AuthIndex      string
	DisplayAccount string
	Provider       string
	ResetAt        time.Time
	EventHash      string
	Reason         string
}

type authFile = cpaauthfiles.File

func NewRateLimitAutoDisableWorker(st *store.Store, initial ...collectorpkg.RuntimeConfig) *RateLimitAutoDisableWorker {
	w := &RateLimitAutoDisableWorker{
		store:               st,
		client:              &http.Client{Timeout: quotaAutoDisableActionTimeout},
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
	if w == nil || w.store == nil || w.store.QuotaCooldowns == nil {
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

	current, ok, err := w.currentAuthFile(ctx, candidate.BaseURL, candidate.ManagementKey, candidate.FileName, candidate.AuthIndex)
	if err != nil {
		log.Printf("[quota-auto-disable] failed to verify auth file %q before disable: %v", candidate.FileName, err)
		return
	}
	if !ok {
		log.Printf("[quota-auto-disable] auth file %q authIndex=%q not found/currently mismatched, skip auto disable", candidate.FileName, candidate.AuthIndex)
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

	log.Printf("[quota-auto-disable] Codex usage limit reached for auth file %q account=%q provider=%q resetAt=%s, disabling", candidate.FileName, candidate.DisplayAccount, candidate.Provider, candidate.ResetAt.Format(time.RFC3339))
	if err := w.patchAuthFile(ctx, candidate.BaseURL, candidate.ManagementKey, candidate.FileName, true); err != nil {
		log.Printf("[quota-auto-disable] failed to disable auth file %q: %v", candidate.FileName, err)
		return
	}

	_, err = w.store.UpsertQuotaCooldown(ctx, store.QuotaCooldownUpsert{
		AuthFileName:     candidate.FileName,
		AuthIndex:        firstNonEmpty(candidate.AuthIndex, current.AuthIndex),
		AccountSnapshot:  candidate.DisplayAccount,
		Provider:         strings.ToLower(strings.TrimSpace(candidate.Provider)),
		RecoverAtMS:      candidate.ResetAt.UnixMilli(),
		Owner:            model.QuotaCooldownOwnerUsage429,
		EventHash:        candidate.EventHash,
		PreDisabledState: preDisabled,
		DisabledAtMS:     now.UnixMilli(),
	})
	if err != nil {
		log.Printf("[quota-auto-disable] disabled auth file %q but failed to persist cooldown ownership: %v", candidate.FileName, err)
		if rollbackErr := w.patchAuthFile(ctx, candidate.BaseURL, candidate.ManagementKey, candidate.FileName, false); rollbackErr != nil {
			log.Printf("[quota-auto-disable] failed to roll back auth file %q after cooldown persistence error: %v", candidate.FileName, rollbackErr)
		}
		return
	}
	log.Printf("[quota-auto-disable] disabled auth file %q; persisted CPAMP-owned auto-enable at %s", candidate.FileName, candidate.ResetAt.Format(time.RFC3339))
}

func (w *RateLimitAutoDisableWorker) extendExistingCooldown(ctx context.Context, candidate quotaAutoDisableCandidate, current authFile) bool {
	active, err := w.store.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		log.Printf("[quota-auto-disable] failed to check active cooldowns for auth file %q: %v", candidate.FileName, err)
		return false
	}
	var existing store.QuotaCooldown
	for _, item := range active {
		if item.AuthFileName == candidate.FileName && item.Owner == model.QuotaCooldownOwnerUsage429 {
			existing = item
			break
		}
	}
	if existing.ID == 0 {
		return false
	}
	currentIndex := current.AuthIndex
	if existing.AuthIndex != "" && currentIndex != existing.AuthIndex {
		log.Printf("[quota-auto-disable] active cooldown auth index mismatch for auth file %q: stored=%q current=%q", candidate.FileName, existing.AuthIndex, currentIndex)
		return false
	}
	_, err = w.store.UpsertQuotaCooldown(ctx, store.QuotaCooldownUpsert{
		AuthFileName:     candidate.FileName,
		AuthIndex:        firstNonEmpty(candidate.AuthIndex, existing.AuthIndex, current.AuthIndex),
		AccountSnapshot:  firstNonEmpty(candidate.DisplayAccount, existing.AccountSnapshot),
		Provider:         strings.ToLower(strings.TrimSpace(firstNonEmpty(candidate.Provider, existing.Provider))),
		RecoverAtMS:      candidate.ResetAt.UnixMilli(),
		Owner:            model.QuotaCooldownOwnerUsage429,
		EventHash:        candidate.EventHash,
		PreDisabledState: false,
		DisabledAtMS:     existing.DisabledAtMS,
	})
	if err != nil {
		log.Printf("[quota-auto-disable] failed to extend active cooldown for auth file %q: %v", candidate.FileName, err)
		return false
	}
	log.Printf("[quota-auto-disable] extended CPAMP-owned auth file %q auto-enable time to %s", candidate.FileName, candidate.ResetAt.Format(time.RFC3339))
	return true
}

func (w *RateLimitAutoDisableWorker) enableDue(ctx context.Context, now time.Time) {
	if w == nil || w.store == nil || w.store.QuotaCooldowns == nil {
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
	if item.Owner != model.QuotaCooldownOwnerUsage429 {
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
	current, ok, err := w.currentAuthFile(ctx, baseURL, managementKey, item.AuthFileName, item.AuthIndex)
	if err != nil {
		_ = w.store.RecordQuotaCooldownFailure(ctx, item.ID, err.Error())
		log.Printf("[quota-auto-disable] failed to verify auth file %q before recovery: %v", item.AuthFileName, err)
		return
	}
	if !ok {
		_ = w.store.MarkQuotaCooldownSkipped(ctx, item.ID, "auth file missing or auth index mismatch")
		log.Printf("[quota-auto-disable] auth file %q authIndex=%q missing/mismatched, skip auto-enable", item.AuthFileName, item.AuthIndex)
		return
	}
	if !current.Disabled {
		_ = w.store.MarkQuotaCooldownRecovered(ctx, item.ID, now.UnixMilli())
		log.Printf("[quota-auto-disable] auth file %q already enabled; marked cooldown recovered", item.AuthFileName)
		return
	}

	log.Printf("[quota-auto-disable] reset time reached for auth file %q account=%q, enabling", item.AuthFileName, item.AccountSnapshot)
	if err := w.patchAuthFile(ctx, baseURL, managementKey, item.AuthFileName, false); err != nil {
		_ = w.store.RecordQuotaCooldownFailure(ctx, item.ID, err.Error())
		log.Printf("[quota-auto-disable] failed to enable auth file %q: %v", item.AuthFileName, err)
		return
	}
	if err := w.store.MarkQuotaCooldownRecovered(ctx, item.ID, now.UnixMilli()); err != nil {
		log.Printf("[quota-auto-disable] enabled auth file %q but failed to mark cooldown recovered: %v", item.AuthFileName, err)
		return
	}
	log.Printf("[quota-auto-disable] enabled auth file %q after Codex usage-limit reset", item.AuthFileName)
}

func quotaAutoDisableCandidateFromEvent(event usage.Event, baseURL string, managementKey string, now time.Time) (quotaAutoDisableCandidate, bool) {
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
		BaseURL:        baseURL,
		ManagementKey:  managementKey,
		FileName:       fileName,
		AuthIndex:      strings.TrimSpace(event.AuthIndex),
		DisplayAccount: firstNonEmpty(event.AccountSnapshot, event.AuthLabelSnapshot, event.Source, fileName),
		Provider:       "codex",
		ResetAt:        resetAt,
		EventHash:      event.EventHash,
		Reason:         event.FailSummary,
	}, true
}

func codexUsageLimitResetTimeFromEvent(event usage.Event, now time.Time) (time.Time, bool) {
	if !event.Failed || event.FailStatusCode != http.StatusTooManyRequests {
		return time.Time{}, false
	}
	provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(event.Provider, event.AuthProviderSnapshot)))
	if provider != "codex" {
		return time.Time{}, false
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

func (w *RateLimitAutoDisableWorker) currentAuthFile(ctx context.Context, baseURL string, managementKey string, fileName string, authIndex string) (authFile, bool, error) {
	files, err := cpaauthfiles.New(w.client, quotaAutoDisableActionTimeout).Fetch(ctx, baseURL, managementKey)
	if err != nil {
		return authFile{}, false, err
	}
	file, ok := cpaauthfiles.Find(files, fileName, authIndex)
	return file, ok, nil
}

func (w *RateLimitAutoDisableWorker) disableAuthFile(ctx context.Context, baseURL string, managementKey string, fileName string) error {
	return w.patchAuthFile(ctx, baseURL, managementKey, fileName, true)
}

func (w *RateLimitAutoDisableWorker) enableAuthFile(ctx context.Context, baseURL string, managementKey string, fileName string) error {
	return w.patchAuthFile(ctx, baseURL, managementKey, fileName, false)
}

func (w *RateLimitAutoDisableWorker) patchAuthFile(ctx context.Context, baseURL string, managementKey string, fileName string, disabled bool) error {
	return cpaauthfiles.New(w.client, quotaAutoDisableActionTimeout).PatchDisabled(ctx, baseURL, managementKey, fileName, disabled)
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
