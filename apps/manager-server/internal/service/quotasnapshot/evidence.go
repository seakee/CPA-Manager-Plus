package quotasnapshot

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/codexquota"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	codexFiveHourSeconds = int64(5 * 60 * 60)
	codexWeekSeconds     = int64(7 * 24 * 60 * 60)
	rollingDaySeconds    = int64(24 * 60 * 60)
	maxObservationIDLen  = 256
)

// WriteUsageEvents persists allowlisted provider quota evidence only after the
// caller has successfully stored the corresponding usage events. Unsupported
// providers and events without a canonical credential identity are ignored.
func (s *Service) WriteUsageEvents(ctx context.Context, events []usage.Event) error {
	entries := make([]WriteEntry, 0, len(events))
	for _, event := range events {
		entry, ok := quotaSnapshotEntryFromUsageEvent(event)
		if ok {
			entries = append(entries, entry)
		}
	}
	return s.writeEvidenceEntries(ctx, entries)
}

// WriteCodexInspectionResult persists standardized quota windows from a
// successfully stored inspection result. Human-readable reset labels are never
// parsed here; only the normalized reset timestamp carried by the result is
// trusted as a fixed-window boundary.
func (s *Service) WriteCodexInspectionResult(ctx context.Context, result model.CodexInspectionResult) error {
	if normalizeProvider(result.Provider) != "codex" {
		return nil
	}
	inventoryMode := "partial"
	if codexInspectionInventoryComplete(result) {
		inventoryMode = "complete"
	}
	observedAtMS := result.CreatedAtMS
	if observedAtMS <= 0 {
		observedAtMS = s.now().UnixMilli()
	}
	account := AccountTarget{
		AuthFileSnapshot:     result.FileName,
		AuthProviderSnapshot: "codex",
		AuthIndex:            result.AuthIndex,
		AccountSnapshot:      result.AccountSnapshot,
		Source:               result.FileName,
	}
	if _, ok := usageidentity.AccountKey(account.identityFields("codex")); !ok {
		return nil
	}
	windows := make([]WindowInput, 0, len(result.QuotaWindows))
	for _, window := range result.QuotaWindows {
		if strings.TrimSpace(window.ID) == "" {
			continue
		}
		duration := roundedPositiveInt64(window.LimitWindowSeconds)
		windowKind := quotaWindowKind(duration)
		providerWindowID := codexquota.CanonicalProviderWindowID(window.ID, windowKind)
		inferredScope := codexquota.InferScopeFromProviderWindowID(providerWindowID)
		modelScope := inferredScope
		if window.ModelScope != nil {
			providedScope := codexquota.NormalizeScope(codexquota.ModelScope{
				Kind:     window.ModelScope.Kind,
				Key:      window.ModelScope.Key,
				Models:   window.ModelScope.Models,
				Complete: window.ModelScope.Complete,
			})
			// Older inspection clients persisted every window as `all`. Stable
			// Codex provider-window identities are stronger evidence for known
			// scoped limits, so do not let that legacy value restore account-wide
			// attribution for Spark or another additional feature.
			if providedScope.Kind != "all" || inferredScope.Kind == "all" {
				modelScope = providedScope
			}
		}
		mode := "unknown"
		accuracy := normalizeInspectionAccuracy(window.ResetAccuracy)
		var cycleStartMS, cycleEndMS *int64
		if duration != nil && window.ResetAtMS > 0 && accuracy != "unknown" {
			mode = "fixed"
			end := window.ResetAtMS
			start := end - *duration*1000
			if start > 0 {
				cycleStartMS = &start
				cycleEndMS = &end
			} else {
				mode = "unknown"
				accuracy = "unknown"
			}
		} else {
			accuracy = "unknown"
		}
		usedPercent := validPercent(window.UsedPercent)
		windows = append(windows, WindowInput{
			ProviderWindowID:      providerWindowID,
			ProviderWindowAliases: append([]string(nil), window.ProviderWindowAliases...),
			WindowKind:            windowKind,
			WindowMode:            mode,
			ModelScopeKind:        modelScope.Kind,
			ModelScopeKey:         modelScope.Key,
			ModelIDs:              append([]string(nil), modelScope.Models...),
			Source:                "inspection",
			SourceObservationID:   inspectionObservationID(result),
			ObservedAtMS:          observedAtMS,
			BoundaryAccuracy:      accuracy,
			CycleStartMS:          cycleStartMS,
			CycleEndMS:            cycleEndMS,
			DurationSeconds:       duration,
			UsedPercent:           usedPercent,
			RemainingPercent:      remainingPercent(usedPercent),
			PlanType:              result.PlanType,
		})
	}
	if len(windows) == 0 && inventoryMode != "complete" {
		return nil
	}
	applyCodexWindowRelationships(windows)
	return s.writeEvidenceEntries(ctx, []WriteEntry{{
		Provider: "codex",
		Account:  account,
		Observation: &ObservationInput{
			Source: "inspection", SourceObservationID: inspectionObservationID(result),
			ObservedAtMS: observedAtMS, InventoryScopeKey: "codex:rate-limits",
			InventoryMode: inventoryMode,
		},
		Windows: windows,
	}})
}

func codexInspectionInventoryComplete(result model.CodexInspectionResult) bool {
	return result.StatusCode != nil &&
		*result.StatusCode >= 200 &&
		*result.StatusCode < 300 &&
		strings.TrimSpace(result.Error) == "" &&
		strings.TrimSpace(result.ErrorKind) == "" &&
		(result.QuotaInventoryObserved || strings.TrimSpace(result.QuotaWindowsJSON) == "[]")
}

func (s *Service) writeEvidenceEntries(ctx context.Context, entries []WriteEntry) error {
	if len(entries) == 0 {
		return nil
	}
	batch := make([]WriteEntry, 0, maxWriteEntries)
	mutationsInBatch := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if _, err := s.Write(ctx, WriteRequest{Entries: batch}); err != nil {
			return err
		}
		batch = make([]WriteEntry, 0, maxWriteEntries)
		mutationsInBatch = 0
		return nil
	}
	for _, entry := range entries {
		completeEmptyObservation := entry.Observation != nil &&
			entry.Observation.InventoryMode == "complete" &&
			len(entry.RemovedWindows) == 0
		if len(entry.Windows) == 0 && !completeEmptyObservation && len(entry.RemovedWindows) == 0 {
			continue
		}
		entryMutations := len(entry.Windows) + len(entry.RemovedWindows)
		if len(batch) == maxWriteEntries || mutationsInBatch+entryMutations > maxWriteEntries {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, entry)
		mutationsInBatch += entryMutations
	}
	return flush()
}

func quotaSnapshotEntryFromUsageEvent(event usage.Event) (WriteEntry, bool) {
	provider := normalizeProvider(firstNonEmpty(event.AuthProviderSnapshot, event.Provider))
	account := AccountTarget{
		AccountSnapshot:       event.AccountSnapshot,
		AuthLabelSnapshot:     event.AuthLabelSnapshot,
		AuthFileSnapshot:      event.AuthFileSnapshot,
		AuthProviderSnapshot:  firstNonEmpty(event.AuthProviderSnapshot, provider),
		AuthProjectIDSnapshot: event.AuthProjectIDSnapshot,
		AuthIndex:             event.AuthIndex,
		Source:                event.Source,
	}
	if _, ok := usageidentity.AccountKey(account.identityFields(provider)); !ok {
		return WriteEntry{}, false
	}
	observationID := eventObservationID(event)
	switch provider {
	case "codex":
		if event.ResponseMetadata == nil || event.ResponseMetadata.Quota == nil {
			return WriteEntry{}, false
		}
		quota := event.ResponseMetadata.Quota
		quotaScope := codexquota.ResolveUsageScope(
			event.Model,
			event.AnalyticsModel,
			event.RequestedModel,
			event.ResolvedModel,
		)
		windows := make([]WindowInput, 0, 2)
		for index, item := range []*usage.HeaderQuotaWindow{quota.Primary, quota.Secondary} {
			if window, ok := codexHeaderWindowInput(item, index, event.TimestampMS, quota.PlanType, observationID, quotaScope); ok {
				windows = append(windows, window)
			}
		}
		applyCodexWindowRelationships(windows)
		if len(windows) == 0 {
			return WriteEntry{}, false
		}
		return WriteEntry{
			Provider: provider, Account: account,
			Observation: &ObservationInput{
				Source: "response_header", SourceObservationID: observationID,
				ObservedAtMS: event.TimestampMS, InventoryScopeKey: "codex:rate-limits",
				InventoryMode: "partial",
			},
			Windows: windows,
		}, true
	case "xai":
		if event.ResponseMetadata == nil {
			return WriteEntry{}, false
		}
		providerUsage := usage.NormalizeProviderUsageMetadata(event.ResponseMetadata.ProviderUsage)
		if providerUsage == nil || providerUsage.Provider != "xai" ||
			providerUsage.Kind != usage.ProviderUsageKindIncludedFree ||
			providerUsage.WindowKind != usage.ProviderUsageWindowRolling24H ||
			providerUsage.Source != usage.ProviderUsageSourceBody {
			return WriteEntry{}, false
		}
		observedAtMS := providerUsage.ObservedAtMS
		if observedAtMS <= 0 {
			observedAtMS = event.TimestampMS
		}
		duration := rollingDaySeconds
		window := WindowInput{
			ProviderWindowID:    "included-free-rolling-24h",
			WindowKind:          usage.ProviderUsageWindowRolling24H,
			WindowMode:          "rolling",
			ModelScopeKind:      "all",
			Source:              "response_body",
			SourceObservationID: observationID,
			ObservedAtMS:        observedAtMS,
			BoundaryAccuracy:    "estimated",
			DurationSeconds:     &duration,
			QuotaUnit:           providerUsage.Unit,
		}
		if modelID := strings.TrimSpace(providerUsage.Model); modelID != "" {
			window.ModelScopeKind = "models"
			window.ModelScopeKey = strings.ToLower(modelID)
			window.ModelIDs = []string{modelID}
		}
		window.UsedValue = int64AsFloat(providerUsage.Actual)
		window.LimitValue = int64AsFloat(providerUsage.Limit)
		window.UsedPercent = ratioPercent(providerUsage.Actual, providerUsage.Limit)
		window.RemainingPercent = ratioPercent(providerUsage.Remaining, providerUsage.Limit)
		if providerUsage.RecoverAtMS > 0 {
			window.CycleEndMS = int64Pointer(providerUsage.RecoverAtMS)
		}
		return WriteEntry{
			Provider: provider, Account: account,
			Observation: &ObservationInput{
				Source: "response_body", SourceObservationID: observationID,
				ObservedAtMS: observedAtMS, InventoryScopeKey: "xai:included-free",
				InventoryMode: "partial",
			},
			Windows: []WindowInput{window},
		}, true
	default:
		return WriteEntry{}, false
	}
}

func applyCodexWindowRelationships(windows []WindowInput) {
	type familyWindows struct {
		fiveHourIndex int
		hasFiveHour   bool
		weeklyID      string
		monthlyID     string
	}
	families := make(map[string]familyWindows)
	for index := range windows {
		family, role, ok := codexWindowFamilyRole(windows[index].ProviderWindowID)
		if !ok {
			continue
		}
		item := families[family]
		switch role {
		case "five-hour":
			item.fiveHourIndex = index
			item.hasFiveHour = true
		case "weekly":
			item.weeklyID = windows[index].ProviderWindowID
		case "monthly":
			item.monthlyID = windows[index].ProviderWindowID
		}
		families[family] = item
	}
	for _, item := range families {
		if !item.hasFiveHour {
			continue
		}
		containerID := item.weeklyID
		if containerID == "" {
			containerID = item.monthlyID
		}
		if containerID == "" {
			continue
		}
		windows[item.fiveHourIndex].RelationshipKind = "concurrent_subwindow"
		windows[item.fiveHourIndex].ContainerWindowID = containerID
	}
}

func codexWindowFamilyRole(providerWindowID string) (string, string, bool) {
	id := strings.TrimSpace(providerWindowID)
	switch id {
	case "five-hour", "weekly", "monthly":
		return "main", id, true
	case "code-review-five-hour":
		return "code-review", "five-hour", true
	case "code-review-weekly":
		return "code-review", "weekly", true
	case "code-review-monthly":
		return "code-review", "monthly", true
	}
	for _, role := range []string{"five-hour", "weekly", "monthly"} {
		marker := "-" + role + "-"
		position := strings.LastIndex(id, marker)
		if position <= 0 {
			continue
		}
		index := id[position+len(marker):]
		if index == "" {
			continue
		}
		if _, err := strconv.Atoi(index); err != nil {
			continue
		}
		return id[:position] + "\x00" + index, role, true
	}
	return "", "", false
}

func codexHeaderWindowInput(
	window *usage.HeaderQuotaWindow,
	index int,
	observedAtMS int64,
	planType string,
	observationID string,
	quotaScope codexquota.UsageScopeResolution,
) (WindowInput, bool) {
	if window == nil {
		return WindowInput{}, false
	}
	duration := roundedMinutesAsSeconds(window.WindowMinutes)
	mode := "unknown"
	accuracy := "unknown"
	var cycleStartMS, cycleEndMS *int64
	if window.ResetAtMS > 0 {
		cycleEndMS = int64Pointer(window.ResetAtMS)
	}
	if duration != nil && window.ResetAtMS > 0 {
		mode = "fixed"
		accuracy = "exact"
		if window.ResetAfterSeconds != nil && *window.ResetAfterSeconds > 0 {
			accuracy = "derived"
		}
		end := window.ResetAtMS
		start := end - *duration*1000
		if start > 0 {
			cycleStartMS = &start
			cycleEndMS = &end
		} else {
			mode = "unknown"
			accuracy = "unknown"
		}
	}
	usedPercent := validPercent(window.UsedPercent)
	if duration == nil && cycleEndMS == nil && (usedPercent == nil || *usedPercent == 0) {
		return WindowInput{}, false
	}
	providerWindowID := codexHeaderWindowID(duration, index, planType, quotaScope.ProviderWindowPrefix)
	return WindowInput{
		ProviderWindowID:      providerWindowID,
		ProviderWindowAliases: codexquota.ProviderWindowAliases(providerWindowID, quotaScope.Scope),
		WindowKind:            quotaWindowKind(duration),
		WindowMode:            mode,
		ModelScopeKind:        quotaScope.Scope.Kind,
		ModelScopeKey:         quotaScope.Scope.Key,
		ModelIDs:              append([]string(nil), quotaScope.Scope.Models...),
		Source:                "response_header",
		SourceObservationID:   observationID,
		ObservedAtMS:          observedAtMS,
		BoundaryAccuracy:      accuracy,
		CycleStartMS:          cycleStartMS,
		CycleEndMS:            cycleEndMS,
		DurationSeconds:       duration,
		UsedPercent:           usedPercent,
		RemainingPercent:      remainingPercent(usedPercent),
		PlanType:              planType,
	}, true
}

func codexHeaderWindowID(duration *int64, index int, planType, scopePrefix string) string {
	role := ""
	if duration != nil {
		switch *duration {
		case codexFiveHourSeconds:
			role = "five-hour"
		case codexWeekSeconds:
			role = "weekly"
		default:
			if *duration >= 28*24*60*60 && *duration <= 31*24*60*60 {
				role = "monthly"
			} else {
				id := fmt.Sprintf("window-%s-%d", formatDuration(*duration), index)
				if scopePrefix != "" {
					return fmt.Sprintf("%s-0-%s", scopePrefix, id)
				}
				return id
			}
		}
	} else if index == 0 {
		role = "five-hour"
	} else if strings.EqualFold(strings.TrimSpace(planType), "team") {
		role = "monthly"
	} else {
		role = "weekly"
	}
	if scopePrefix != "" {
		return fmt.Sprintf("%s-%s-0", scopePrefix, role)
	}
	return role
}

func quotaWindowKind(duration *int64) string {
	if duration == nil {
		return "unknown"
	}
	switch *duration {
	case codexFiveHourSeconds:
		return "five_hour"
	case rollingDaySeconds:
		return "daily"
	case codexWeekSeconds:
		return "weekly"
	default:
		if *duration >= 28*24*60*60 && *duration <= 31*24*60*60 {
			return "monthly"
		}
		return "unknown"
	}
}

func formatDuration(seconds int64) string {
	if seconds%(24*60*60) == 0 {
		return strconv.FormatInt(seconds/(24*60*60), 10) + "d"
	}
	if seconds%(60*60) == 0 {
		return strconv.FormatInt(seconds/(60*60), 10) + "h"
	}
	return strconv.FormatInt(seconds, 10) + "s"
}

func roundedMinutesAsSeconds(value *float64) *int64 {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value <= 0 {
		return nil
	}
	seconds := int64(math.Round(*value * 60))
	if seconds <= 0 {
		return nil
	}
	return &seconds
}

func roundedPositiveInt64(value *float64) *int64 {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value <= 0 {
		return nil
	}
	rounded := int64(math.Round(*value))
	if rounded <= 0 {
		return nil
	}
	return &rounded
}

func validPercent(value *float64) *float64 {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > 100 {
		return nil
	}
	copy := *value
	return &copy
}

func remainingPercent(used *float64) *float64 {
	if used == nil {
		return nil
	}
	value := math.Max(0, math.Min(100, 100-*used))
	return &value
}

func ratioPercent(value, limit *int64) *float64 {
	if value == nil || limit == nil || *value < 0 || *limit <= 0 {
		return nil
	}
	percent := math.Max(0, math.Min(100, float64(*value)/float64(*limit)*100))
	return &percent
}

func int64AsFloat(value *int64) *float64 {
	if value == nil || *value < 0 {
		return nil
	}
	converted := float64(*value)
	return &converted
}

func normalizeInspectionAccuracy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "exact":
		return "exact"
	case "derived":
		return "derived"
	case "estimated":
		return "derived"
	default:
		return "unknown"
	}
}

func eventObservationID(event usage.Event) string {
	value := strings.TrimSpace(event.RequestID)
	if value == "" && event.ResponseMetadata != nil && event.ResponseMetadata.Trace != nil {
		value = strings.TrimSpace(event.ResponseMetadata.Trace.PrimaryTraceID)
	}
	return truncateObservationID(value)
}

func inspectionObservationID(result model.CodexInspectionResult) string {
	if result.ID > 0 {
		return fmt.Sprintf("run:%d:result:%d", result.RunID, result.ID)
	}
	return fmt.Sprintf("run:%d:%s", result.RunID, truncateObservationID(result.AccountKey))
}

func truncateObservationID(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) <= maxObservationIDLen {
		return trimmed
	}
	return trimmed[:maxObservationIDLen]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func int64Pointer(value int64) *int64 {
	return &value
}
