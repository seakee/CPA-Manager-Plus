package main

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// The preflight is the question the drawer asks before it offers the submit. It
// answers with the same verdict and the same code vocabulary the prepare would,
// and it answers without writing to the destination. Ordinary creation now
// applies the key to CPA, so the install is never pristine by the time the page
// asks: every no-mutation assertion compares the destination's actual write
// count before and after the preflight.
func TestKeyOperationPreflightNamesEveryLimitBeforeSubmit(t *testing.T) {
	preflight := func(t *testing.T, harness *managedInstallHarness, target, keeper, mode string) (bool, string) {
		t.Helper()
		resource := "api-keys/" + target + "/revocation/" + mode
		if keeper != "" {
			resource = "api-keys/" + target + "/revocation/" + keeper + "/" + mode
		}
		response := harness.call(t, http.MethodGet, resource, "")
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

	// revokeThroughCPA performs the real keeperless revocation an operator
	// submits, so the next preflight reads the destination CPA's actual list
	// instead of an assumption about what creation left behind.
	revokeThroughCPA := func(t *testing.T, harness *managedInstallHarness, target, requestID string) {
		t.Helper()
		prepare := harness.call(t, http.MethodPost, "api-keys/"+target+"/revocation",
			`{"requestId":"`+requestID+`","mode":"revoke"}`)
		if prepare.Code != http.StatusOK {
			t.Fatalf("revoke prepare = %d %s", prepare.Code, prepare.Body.String())
		}
		cont := harness.call(t, http.MethodPost, "api-keys/operations/"+requestID+"/continue", `{}`)
		if cont.Code != http.StatusOK {
			t.Fatalf("revoke continue = %d %s", cont.Code, cont.Body.String())
		}
	}

	t.Run("accepted selection is ready", func(t *testing.T) {
		harness := newManagedInstallHarness(t)
		keeper := harness.createKey(t, strings.Repeat("6", 32))
		// Creation is a real installation: the keeper is effective and CPA now
		// holds both client keys, so this accepted replace is already multi-key.
		if keeper.Key.RuntimeState != "effective_observed" {
			t.Fatalf("created keeper state = %q", keeper.Key.RuntimeState)
		}
		if got := harness.destination.installed(); len(got) != 2 ||
			got[0] != harness.installKey.Secret || got[1] != keeper.CreatedSecret {
			t.Fatalf("destination keys after creation = %v", got)
		}
		writesBefore := harness.destination.writes
		if ready, reason := preflight(t, harness, string(harness.installKey.ID), string(keeper.Key.ID), "replace"); !ready {
			t.Fatalf("accepted selection = %v %q", ready, reason)
		}
		if harness.destination.writes != writesBefore {
			t.Fatalf("preflight wrote the destination (%d -> %d writes)", writesBefore, harness.destination.writes)
		}
	})

	t.Run("the last effective key is named", func(t *testing.T) {
		harness := newManagedInstallHarness(t)
		keeper := harness.createKey(t, strings.Repeat("7", 32))
		// Revoke the install key for real, leaving exactly one client key in
		// CPA: that lone key is the boundary the preflight must name.
		revokeThroughCPA(t, harness, string(harness.installKey.ID), strings.Repeat("4", 32))
		if got := harness.destination.installed(); len(got) != 1 || got[0] != keeper.CreatedSecret {
			t.Fatalf("destination keys after the real revocation = %v", got)
		}
		writesBefore := harness.destination.writes
		ready, reason := preflight(t, harness, string(keeper.Key.ID), "", "revoke")
		if ready || reason != "last_effective_key" {
			t.Fatalf("last key = %v %q", ready, reason)
		}
		if harness.destination.writes != writesBefore {
			t.Fatalf("preflight wrote the destination (%d -> %d writes)", writesBefore, harness.destination.writes)
		}
		// The prediction and the submit are the same verdict, so a page that
		// ignored the preflight still learns the boundary without an attempt.
		blocked := harness.call(t, http.MethodPost, "api-keys/"+string(keeper.Key.ID)+"/revocation",
			`{"requestId":"`+strings.Repeat("d", 32)+`","mode":"revoke"}`)
		if blocked.Code != http.StatusConflict || errorCode(t, blocked) != "last_effective_key" {
			t.Fatalf("submit = %d %s", blocked.Code, blocked.Body.String())
		}
		if harness.destination.writes != writesBefore {
			t.Fatalf("the refused revoke wrote %d times (%d -> %d)", harness.destination.writes-writesBefore, writesBefore, harness.destination.writes)
		}
		if got := harness.destination.installed(); len(got) != 1 || got[0] != keeper.CreatedSecret {
			t.Fatalf("destination keys after the refused revoke = %v", got)
		}
	})

	t.Run("a key staged but never installed is refused", func(t *testing.T) {
		harness := newManagedInstallHarness(t)
		keeper := harness.createKey(t, strings.Repeat("9", 32))
		// Staging is not installing: the underlying service mints the record
		// without touching CPA, so the destination never carries this key.
		staged, created, err := harness.app.keys.Create(t.Context(), strings.Repeat("8", 32))
		if err != nil || !created || staged.CreatedSecret == "" {
			t.Fatalf("stage key = %v (created=%t secret=%t)", err, created, staged.CreatedSecret != "")
		}
		got := harness.destination.installed()
		if len(got) != 2 || slices.Contains(got, staged.CreatedSecret) {
			t.Fatalf("staged key reached the destination = %v", got)
		}
		writesBefore := harness.destination.writes
		ready, reason := preflight(t, harness, string(staged.Key.ID), string(keeper.Key.ID), "revoke")
		if ready || reason != "key_operation_source_conflict" {
			t.Fatalf("uninstalled staged key = %v %q", ready, reason)
		}
		if harness.destination.writes != writesBefore {
			t.Fatalf("preflight wrote the destination (%d -> %d writes)", writesBefore, harness.destination.writes)
		}
	})

	t.Run("a destination key the manager never staged is named", func(t *testing.T) {
		harness := newManagedInstallHarness(t)
		keeper := harness.createKey(t, strings.Repeat("a", 32))
		harness.destination.seed("unknown-client-key")
		writesBefore := harness.destination.writes
		ready, reason := preflight(t, harness, string(harness.installKey.ID), string(keeper.Key.ID), "revoke")
		if ready || reason != "key_operation_source_conflict" {
			t.Fatalf("unknown destination key = %v %q", ready, reason)
		}
		if harness.destination.writes != writesBefore {
			t.Fatalf("preflight wrote the destination (%d -> %d writes)", writesBefore, harness.destination.writes)
		}
	})
}
