package freshkeyapply

import (
	"errors"
	"slices"
	"strings"
	"testing"

	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

func TestCommittedExternalAddReplayObservesCurrentKeyAfterLaterOperations(t *testing.T) {
	for _, state := range []string{"effective", "retired", "removed"} {
		t.Run(state, func(t *testing.T) {
			h := newExternalHarness(t, "owner-seed")
			firstRequest, secondRequest := strings.Repeat("a", 32), strings.Repeat("b", 32)
			firstID, firstSecret := h.staged(t, firstRequest)
			secondID, _ := h.staged(t, secondRequest)
			if _, err := h.service.ApplyAdd(t.Context(), firstRequest, firstID, 1); err != nil {
				t.Fatal(err)
			}
			if _, err := h.service.ApplyAdd(t.Context(), secondRequest, secondID, 1); err != nil {
				t.Fatal(err)
			}
			switch state {
			case "retired":
				operation, err := h.service.Prepare(t.Context(), strings.Repeat("c", 32), firstID, "", "revoke", 1)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := h.service.ContinueOperation(t.Context(), operation.Operation.ID, 1); err != nil {
					t.Fatal(err)
				}
			case "removed":
				h.cpa.mu.Lock()
				h.cpa.keys = slices.DeleteFunc(h.cpa.keys, func(key string) bool { return key == firstSecret })
				h.cpa.mu.Unlock()
			}
			h.cpa.mu.Lock()
			before := h.cpa.writes
			h.cpa.mu.Unlock()
			result, err := h.service.ApplyAdd(t.Context(), firstRequest, firstID, 1)
			switch state {
			case "effective":
				if err != nil || result.RuntimeState != "effective_observed" {
					t.Fatalf("replay = %s, %v", result.RuntimeState, err)
				}
			case "retired":
				if !errors.Is(err, repo.ErrConflict) {
					t.Fatalf("retired replay = %v, want conflict", err)
				}
			case "removed":
				if !errors.Is(err, cpa.ErrApplyUnknown) || result.RuntimeState == "effective_observed" {
					t.Fatalf("missing replay = %s, %v", result.RuntimeState, err)
				}
			}
			h.cpa.mu.Lock()
			after := h.cpa.writes
			h.cpa.mu.Unlock()
			if after != before {
				t.Fatalf("committed replay resent a mutation: before=%d after=%d", before, after)
			}
		})
	}
}

func TestCommittedManagedAddReplayObservesCurrentKeyAfterLaterOperations(t *testing.T) {
	for _, state := range []string{"effective", "retired", "removed"} {
		t.Run(state, func(t *testing.T) {
			svc, destination, _, initialID := managedFixture(t)
			keys := svc.keys
			if _, err := svc.Apply(t.Context(), initialID, 9); err != nil {
				t.Fatal(err)
			}
			firstRequest, secondRequest := strings.Repeat("a", 32), strings.Repeat("b", 32)
			first, _, err := keys.Create(t.Context(), firstRequest)
			if err != nil {
				t.Fatal(err)
			}
			second, _, err := keys.Create(t.Context(), secondRequest)
			if err != nil {
				t.Fatal(err)
			}
			var firstSecret string
			if err := keys.WithStagedSecret(t.Context(), string(first.Key.ID), func(secret string) error { firstSecret = secret; return nil }); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ApplyAdd(t.Context(), firstRequest, string(first.Key.ID), 9); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ApplyAdd(t.Context(), secondRequest, string(second.Key.ID), 9); err != nil {
				t.Fatal(err)
			}
			switch state {
			case "retired":
				prepared, err := svc.Prepare(t.Context(), strings.Repeat("c", 32), string(first.Key.ID), "", "revoke", 9)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := svc.ContinueOperation(t.Context(), prepared.Operation.ID, 9); err != nil {
					t.Fatal(err)
				}
			case "removed":
				destination.mu.Lock()
				destination.keys = slices.DeleteFunc(destination.keys, func(key string) bool { return key == firstSecret })
				destination.mu.Unlock()
			}
			destination.mu.Lock()
			before := destination.keyWrites
			destination.mu.Unlock()
			result, err := svc.ApplyAdd(t.Context(), firstRequest, string(first.Key.ID), 9)
			switch state {
			case "effective":
				if err != nil || result.RuntimeState != "effective_observed" {
					t.Fatalf("replay = %s, %v", result.RuntimeState, err)
				}
			case "retired":
				if !errors.Is(err, repo.ErrConflict) {
					t.Fatalf("retired replay = %v, want conflict", err)
				}
			case "removed":
				if !errors.Is(err, cpa.ErrApplyUnknown) || result.RuntimeState == "effective_observed" {
					t.Fatalf("missing replay = %s, %v", result.RuntimeState, err)
				}
			}
			destination.mu.Lock()
			after := destination.keyWrites
			destination.mu.Unlock()
			if after != before {
				t.Fatalf("committed replay resent mutation: before=%d after=%d", before, after)
			}
		})
	}
}
