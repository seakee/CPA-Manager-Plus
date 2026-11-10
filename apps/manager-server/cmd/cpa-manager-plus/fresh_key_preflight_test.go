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
}
