package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The preflight is the question the drawer asks before it offers the submit. It
// answers with the same verdict and the same code vocabulary the prepare would,
// and it never journals or writes anything.
func TestKeyOperationPreflightNamesEveryLimitBeforeSubmit(t *testing.T) {
	preflight := func(t *testing.T, harness *managedInstallHarness, target, keeper, mode string) (bool, string) {
		t.Helper()
		response := harness.call(t, http.MethodGet,
			"api-keys/"+target+"/revocation/"+keeper+"/"+mode, "")
		if response.Code != http.StatusOK {
			t.Fatalf("preflight = %d %s", response.Code, response.Body.String())
		}
		var reply struct {
			Ready  bool   `json:"ready"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil {
			t.Fatalf("preflight body = %s (%v)", response.Body.String(), err)
		}
		return reply.Ready, reply.Reason
	}

	t.Run("accepted selection is ready", func(t *testing.T) {
		harness := newManagedInstallHarness(t)
		keeper := harness.createKey(t, strings.Repeat("6", 32))
		if ready, reason := preflight(t, harness, string(harness.installKey.ID), string(keeper.Key.ID), "replace"); !ready {
			t.Fatalf("accepted selection = %v %q", ready, reason)
		}
		if harness.destination.writes != 0 {
			t.Fatalf("preflight wrote the destination (%d writes)", harness.destination.writes)
		}
	})

	t.Run("the last effective key is named", func(t *testing.T) {
		harness := newManagedInstallHarness(t)
		keeper := harness.createKey(t, strings.Repeat("7", 32))
		ready, reason := preflight(t, harness, string(harness.installKey.ID), string(keeper.Key.ID), "revoke")
		if ready || reason != "last_effective_key" {
			t.Fatalf("last key = %v %q", ready, reason)
		}
	})

	t.Run("the installation fence is named", func(t *testing.T) {
		harness := newManagedInstallHarness(t)
		target := harness.createKey(t, strings.Repeat("8", 32))
		keeper := harness.createKey(t, strings.Repeat("9", 32))
		ready, reason := preflight(t, harness, string(target.Key.ID), string(keeper.Key.ID), "revoke")
		if ready || reason != "key_operation_conflict" {
			t.Fatalf("installation fence = %v %q", ready, reason)
		}
	})

	t.Run("a destination key the manager never staged is named", func(t *testing.T) {
		harness := newManagedInstallHarness(t)
		keeper := harness.createKey(t, strings.Repeat("a", 32))
		harness.destination.seed("unknown-client-key")
		ready, reason := preflight(t, harness, string(harness.installKey.ID), string(keeper.Key.ID), "revoke")
		if ready || reason != "key_operation_source_conflict" {
			t.Fatalf("unknown destination key = %v %q", ready, reason)
		}
	})

	// A whole-list write can only state a list the destination holds. The managed
	// posture takes two keys, so replacing one of two would have to send three:
	// the write itself would be refused before it left the Manager, after the
	// attempt had already been journaled. The preflight must therefore answer the
	// same conflict the prepare does, and nothing may reach the destination.
	t.Run("a list past the destination's bound is named", func(t *testing.T) {
		harness := newManagedInstallHarness(t)
		spare := harness.createKey(t, strings.Repeat("b", 32))
		harness.destination.seed(harness.installKey.Secret, spare.CreatedSecret)
		next := harness.createKey(t, strings.Repeat("c", 32))
		ready, reason := preflight(t, harness, string(harness.installKey.ID), string(next.Key.ID), "replace")
		if ready || reason != "key_operation_source_conflict" {
			t.Fatalf("bound = %v %q", ready, reason)
		}
		if harness.destination.writes != 0 {
			t.Fatalf("preflight wrote the destination (%d writes)", harness.destination.writes)
		}
		// The prediction and the submit are the same verdict, so a page that
		// ignored the preflight still learns the limit without an attempt.
		blocked := harness.call(t, http.MethodPost, "api-keys/"+string(harness.installKey.ID)+"/revocation",
			`{"requestId":"`+strings.Repeat("d", 32)+`","keeperId":"`+string(next.Key.ID)+`","mode":"replace"}`)
		if blocked.Code != http.StatusConflict || errorCode(t, blocked) != "key_operation_source_conflict" {
			t.Fatalf("submit = %d %s", blocked.Code, blocked.Body.String())
		}
		if harness.destination.writes != 0 {
			t.Fatalf("the refused replace wrote %d times", harness.destination.writes)
		}
		if got := harness.destination.installed(); len(got) != 2 {
			t.Fatalf("destination keys after the refused replace = %v", got)
		}
	})
}
