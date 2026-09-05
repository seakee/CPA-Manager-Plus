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
