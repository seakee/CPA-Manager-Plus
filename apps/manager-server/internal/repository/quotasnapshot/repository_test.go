package quotasnapshot_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	quotasnapshot "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/quotasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestListCandidatesKeepsLatestEvidenceForEveryActiveWindow(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const (
		windowCount = 251
		historySize = 8
	)
	scopeFingerprint := quotasnapshot.ScopeFingerprint("all", "", nil)
	writes := make([]model.AccountQuotaObservationWrite, 0, windowCount*historySize)
	for windowIndex := 0; windowIndex < windowCount; windowIndex++ {
		providerWindowID := fmt.Sprintf("window-%03d", windowIndex)
		for historyIndex := 0; historyIndex < historySize; historyIndex++ {
			observedAtMS := int64(windowIndex*100 + historyIndex + 1)
			observationHash := fmt.Sprintf("observation-%03d-%d", windowIndex, historyIndex)
			writes = append(writes, model.AccountQuotaObservationWrite{
				Observation: model.AccountQuotaObservation{
					ObservationHash:     observationHash,
					AccountKey:          "account-1",
					Provider:            "antigravity",
					Source:              "api_query",
					SourceObservationID: observationHash,
					InventoryScopeKey:   "antigravity:quota-windows",
					InventoryMode:       "partial",
					ObservedAtMS:        observedAtMS,
					WindowCount:         1,
					CreatedAtMS:         observedAtMS,
				},
				Snapshots: []model.AccountQuotaSnapshot{{
					AccountKey:          "account-1",
					Provider:            "antigravity",
					ProviderWindowID:    providerWindowID,
					WindowKind:          "model_quota",
					WindowMode:          "unknown",
					ModelScopeKind:      "all",
					ScopeFingerprint:    scopeFingerprint,
					ContentHash:         fmt.Sprintf("content-%03d-%d", windowIndex, historyIndex),
					InventoryScopeKey:   "antigravity:quota-windows",
					Source:              "api_query",
					SourceObservationID: observationHash,
					ObservedAtMS:        observedAtMS,
					BoundaryAccuracy:    "unknown",
					CreatedAtMS:         observedAtMS,
				}},
			})
		}
	}
	if err := st.QuotaSnapshots.InsertObservationWrites(context.Background(), writes); err != nil {
		t.Fatalf("insert quota evidence: %v", err)
	}

	candidates, err := st.QuotaSnapshots.ListCandidates(
		context.Background(),
		"account-1",
		"antigravity",
		2000,
	)
	if err != nil {
		t.Fatalf("list quota candidates: %v", err)
	}
	windows := make(map[string]struct{}, windowCount)
	for _, candidate := range candidates {
		windows[candidate.ProviderWindowID] = struct{}{}
	}
	if len(windows) != windowCount {
		t.Fatalf("candidate windows = %d, want %d", len(windows), windowCount)
	}
}

func TestListCurrentAmbiguousCandidatesUsesLatestCompletePerInventoryScope(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const (
		accountKey = "account-ambiguous"
		provider   = "codex"
	)
	scopeFingerprint := quotasnapshot.ScopeFingerprint("feature", "future_feature", nil)
	ambiguousSnapshots := func(scope, hash string) model.AccountQuotaObservationWrite {
		return model.AccountQuotaObservationWrite{
			Observation: model.AccountQuotaObservation{
				ObservationHash:     hash,
				AccountKey:          accountKey,
				Provider:            provider,
				Source:              "inspection",
				SourceObservationID: hash,
				InventoryScopeKey:   scope,
				InventoryMode:       "complete",
				ObservedAtMS:        100,
				WindowCount:         2,
				CreatedAtMS:         100,
			},
			Snapshots: []model.AccountQuotaSnapshot{
				{
					AccountKey: accountKey, Provider: provider,
					ProviderWindowID: "cpamp:ambiguous:future-feature-weekly-0", WindowKind: "weekly", WindowMode: "fixed",
					ScopeDisplayName: "Same Quota", ModelScopeKind: "feature", ModelScopeKey: "future_feature",
					ScopeFingerprint: scopeFingerprint, ContentHash: hash + "-0", InventoryScopeKey: scope,
					Source: "inspection", SourceObservationID: hash, ObservedAtMS: 100, BoundaryAccuracy: "unknown", CreatedAtMS: 100,
				},
				{
					AccountKey: accountKey, Provider: provider,
					ProviderWindowID: "cpamp:ambiguous:future-feature-weekly-1", WindowKind: "weekly", WindowMode: "fixed",
					ScopeDisplayName: "Same Quota", ModelScopeKind: "feature", ModelScopeKey: "future_feature",
					ScopeFingerprint: scopeFingerprint, ContentHash: hash + "-1", InventoryScopeKey: scope,
					Source: "inspection", SourceObservationID: hash, ObservedAtMS: 100, BoundaryAccuracy: "unknown", CreatedAtMS: 100,
				},
			},
		}
	}
	if err := st.QuotaSnapshots.InsertObservationWrites(context.Background(), []model.AccountQuotaObservationWrite{
		ambiguousSnapshots("scope-a", "scope-a-complete-1"),
		{
			Observation: model.AccountQuotaObservation{
				ObservationHash: "scope-b-complete-1", AccountKey: accountKey, Provider: provider,
				Source: "api_query", SourceObservationID: "scope-b-complete-1", InventoryScopeKey: "scope-b",
				InventoryMode: "complete", ObservedAtMS: 100, CreatedAtMS: 100,
			},
		},
	}); err != nil {
		t.Fatalf("insert initial scoped observations: %v", err)
	}

	legacyCandidates, err := st.QuotaSnapshots.ListCandidates(context.Background(), accountKey, provider, 100)
	if err != nil {
		t.Fatalf("list ordinary candidates: %v", err)
	}
	if len(legacyCandidates) != 0 {
		t.Fatalf("ambiguous snapshots entered ordinary candidates: %#v", legacyCandidates)
	}
	current, err := st.QuotaSnapshots.ListCurrentAmbiguousCandidates(context.Background(), accountKey, provider)
	if err != nil {
		t.Fatalf("list current ambiguous candidates: %v", err)
	}
	if len(current) != 2 {
		t.Fatalf("initial current ambiguous candidates = %#v", current)
	}
	for _, candidate := range current {
		if candidate.InventoryScopeKey != "scope-a" {
			t.Fatalf("current ambiguous candidate crossed inventory scope: %#v", candidate)
		}
	}

	if err := st.QuotaSnapshots.InsertObservationWrites(context.Background(), []model.AccountQuotaObservationWrite{
		{
			Observation: model.AccountQuotaObservation{
				ObservationHash: "scope-a-partial", AccountKey: accountKey, Provider: provider,
				Source: "api_query", SourceObservationID: "scope-a-partial", InventoryScopeKey: "scope-a",
				InventoryMode: "partial", ObservedAtMS: 200, CreatedAtMS: 200,
			},
		},
	}); err != nil {
		t.Fatalf("insert partial observation: %v", err)
	}
	current, err = st.QuotaSnapshots.ListCurrentAmbiguousCandidates(context.Background(), accountKey, provider)
	if err != nil {
		t.Fatalf("list after partial observation: %v", err)
	}
	if len(current) != 2 {
		t.Fatalf("partial observation evicted current ambiguous candidates: %#v", current)
	}

	if err := st.QuotaSnapshots.InsertObservationWrites(context.Background(), []model.AccountQuotaObservationWrite{
		{
			Observation: model.AccountQuotaObservation{
				ObservationHash: "scope-a-empty-complete", AccountKey: accountKey, Provider: provider,
				Source: "inspection", SourceObservationID: "scope-a-empty-complete", InventoryScopeKey: "scope-a",
				InventoryMode: "complete", ObservedAtMS: 300, CreatedAtMS: 300,
			},
		},
	}); err != nil {
		t.Fatalf("insert empty complete observation: %v", err)
	}
	current, err = st.QuotaSnapshots.ListCurrentAmbiguousCandidates(context.Background(), accountKey, provider)
	if err != nil {
		t.Fatalf("list after empty complete observation: %v", err)
	}
	if len(current) != 0 {
		t.Fatalf("empty complete inventory retained old ambiguous candidates: %#v", current)
	}
}
