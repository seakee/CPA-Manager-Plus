package main

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
)

// adoptedClientKeys are the source CPA's client keys of this fixture. They carry
// none of this Manager's canonical shapes, exactly like a real adopted CPA.
var adoptedClientKeys = []string{"source-client-two", "source-client-one"}

// adoptionFixtureWithKeys stages the full adoption with the source presenting
// these client keys and runs the migration to a settled run.
func adoptionFixtureWithKeys(t *testing.T, clientKeys ...string) *adoptMigrationFixture {
	t.Helper()
	fixture := newAdoptMigrationFixtureWithClientKeys(t, clientKeys...)
	if response := fixture.strategy(t, fixture.record); response.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", response.Code, response.Body.String())
	}
	fixture.wait()
	if summary := fixture.reload(t).AdoptionSummary; summary == nil || !summary.Finished {
		t.Fatalf("the migration did not record a finished run: %+v", summary)
	}
	return fixture
}

// Completion is where an adopted destination's client keys become the Key
// workspace's records and the install journal is claimed, so the credentials the
// console listed as migrated are the credentials it can now revoke or replace.
func TestAdoptCompletionRegistersTheDestinationKeys(t *testing.T) {
	fixture := adoptionFixtureWithKeys(t, adoptedClientKeys...)
	ctx := t.Context()

	if listed, err := fixture.app.keys.List(ctx); err != nil || len(listed) != 0 {
		t.Fatalf("the installation listed keys before completion: %#v %v", listed, err)
	}
	if response := fixture.complete(t); response.Code != http.StatusOK {
		t.Fatalf("completion: %d %s", response.Code, response.Body.String())
	}
	keys, err := fixture.app.keys.List(ctx)
	if err != nil || len(keys) != len(adoptedClientKeys) {
		t.Fatalf("workspace list = %#v %v", keys, err)
	}
	for index, key := range keys {
		if key.Origin != freshkeys.OriginAdopted || key.Lifecycle != "active" {
			t.Fatalf("key %d is not an adopted record: %#v", index, key)
		}
		if key.MaskedPreview != "…"+adoptedClientKeys[index][len(adoptedClientKeys[index])-4:] {
			t.Fatalf("key %d preview = %q", index, key.MaskedPreview)
		}
		if key.NameRevision != 1 || !slices.Contains([]string{"not_applied", "effect_unknown"}, key.RuntimeState) {
			t.Fatalf("key %d is not a fresh installation record: %#v", index, key)
		}
	}
	journal, err := fixture.app.keyRepo.InitialInstall(ctx)
	if err != nil || journal == nil {
		t.Fatalf("install journal: %#v %v", journal, err)
	}
	if journal.KeyID != string(keys[0].ID) || journal.RunID != freshkeyapply.ManagedRunID(string(fixture.record.Operation.Identity.OperationID)) ||
		journal.Generation != "3" {
		t.Fatalf("journal is not bound to this installation: %#v", journal)
	}

	// The console's own list is the surface the walkthrough found empty: it now
	// answers with the destination's credentials and never with their plaintext.
	response := gatewayRequest(t, fixture.app, http.MethodGet, "api-keys", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("api-keys after completion: %d %s", response.Code, response.Body.String())
	}
	var listed struct {
		Keys []struct {
			ID            string `json:"id"`
			Origin        string `json:"origin"`
			MaskedPreview string `json:"maskedPreview"`
			Lifecycle     string `json:"lifecycle"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil || len(listed.Keys) != len(keys) {
		t.Fatalf("api-keys body: %s", response.Body.String())
	}
	for index, key := range listed.Keys {
		if key.ID != string(keys[index].ID) || key.Origin != freshkeys.OriginAdopted || key.Lifecycle != "active" ||
			key.MaskedPreview != keys[index].MaskedPreview {
			t.Fatalf("api-keys entry %d = %#v", index, key)
		}
		if strings.Contains(response.Body.String(), adoptedClientKeys[index]) {
			t.Fatalf("api-keys disclosed %q", adoptedClientKeys[index])
		}
	}

	// A replay of an already-complete installation is idempotent: it answers 200
	// and registers nothing a second time.
	completed := fixture.reload(t)
	if replay := fixture.complete(t); replay.Code != http.StatusOK {
		t.Fatalf("completion replay: %d %s", replay.Code, replay.Body.String())
	}
	if after, err := fixture.app.keys.List(ctx); err != nil || len(after) != len(keys) {
		t.Fatalf("completion replay changed the key list: %#v %v", after, err)
	}
	if again, err := fixture.app.keyRepo.InitialInstall(ctx); err != nil || again == nil || *again != *journal {
		t.Fatalf("completion replay rewrote the journal: %#v %v", again, err)
	}
	if fixture.reload(t).Operation.Revision != completed.Operation.Revision {
		t.Fatal("completion replay changed the installation record")
	}
}

// A journal another installation already owns is a conflict, not an outage: the
// completion refuses with its own code, keeps the installation incomplete, and
// leaves the registered keys visible instead of rolling them back silently.
func TestAdoptCompletionRefusesAForeignInstallJournalAsAConflict(t *testing.T) {
	fixture := adoptionFixtureWithKeys(t, adoptedClientKeys...)
	ctx := t.Context()
	if err := fixture.app.keyRepo.ClaimInitialInstall(ctx, freshkeys.InitialInstall{
		KeyID: strings.Repeat("a", 32), RunID: "managed:another-install", Generation: "3", AttemptedAtMS: 1,
	}); err != nil {
		t.Fatal(err)
	}
	response := fixture.complete(t)
	if response.Code != http.StatusConflict {
		t.Fatalf("foreign journal completion: %d %s", response.Code, response.Body.String())
	}
	var failure struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(response.Body.Bytes(), &failure) != nil || failure.Code != "adopt_installation_conflict" {
		t.Fatalf("foreign journal body: %s", response.Body.String())
	}
	if fixture.reload(t).Operation.ProductComplete {
		t.Fatal("a refused registration completed the installation")
	}
	if listed, err := fixture.app.keys.List(ctx); err != nil || len(listed) != len(adoptedClientKeys) {
		t.Fatalf("refused registration left %#v %v", listed, err)
	}
}

// The gateway configuration page and the plugin switch read the same
// install-derived authority the key journal does. Before completion an adopted
// installation has none — the reported defect — and afterwards it has exactly the
// one a New Managed install derives.
func TestAdoptCompletionGrantsTheGatewayConfigurationAuthority(t *testing.T) {
	fixture := adoptionFixtureWithKeys(t, adoptedClientKeys...)
	destination := fixture.destination.URL()
	fixture.app.gatewayConfig = freshconfig.New(fixture.app.store, fixture.app.keyApply.ConfigTarget,
		fixture.app.keyApply.TargetsFunc(freshkeyapply.SurfaceGatewayConfig))
	fixture.app.gatewayConfig.EnableManaged(destination)

	// The pre-completion state of the defect: the page reads the switch as having
	// no authority at all, which is the switch the walkthrough did not render.
	before := pluginRuntimeCapability(t, fixture.app)
	if before.Mode != "unavailable" || before.Reason != "config_authority_unavailable" || before.Writable {
		t.Fatalf("pre-completion capability = %#v", before)
	}
	if response := fixture.complete(t); response.Code != http.StatusOK {
		t.Fatalf("completion: %d %s", response.Code, response.Body.String())
	}
	if managementKey, err := fixture.app.keyApply.ConfigTarget(t.Context(), 3); err != nil || len(managementKey) != 64 {
		t.Fatalf("config authority after completion: %v", err)
	}
	after := pluginRuntimeCapability(t, fixture.app)
	if after.Mode == "unavailable" || after.Reason != "" || !after.Readable || !after.Writable {
		t.Fatalf("post-completion capability = %#v", after)
	}
	if response := gatewayRequest(t, fixture.app, http.MethodGet, "gateway/config", "", ""); response.Code != http.StatusOK {
		t.Fatalf("gateway config after completion: %d %s", response.Code, response.Body.String())
	}
}

// pluginRuntimeCapability reads the plugin switch's own capability report.
func pluginRuntimeCapability(t *testing.T, app *freshApplication) struct {
	Mode     string `json:"mode"`
	Readable bool   `json:"readable"`
	Writable bool   `json:"writable"`
	Reason   string `json:"reason"`
} {
	t.Helper()
	response := gatewayRequest(t, app, http.MethodGet, "plugins/runtime", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("plugin runtime: %d %s", response.Code, response.Body.String())
	}
	var reply struct {
		Capability struct {
			Mode     string `json:"mode"`
			Readable bool   `json:"readable"`
			Writable bool   `json:"writable"`
			Reason   string `json:"reason"`
		} `json:"capability"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil {
		t.Fatalf("plugin runtime body: %s", response.Body.String())
	}
	return struct {
		Mode     string `json:"mode"`
		Readable bool   `json:"readable"`
		Writable bool   `json:"writable"`
		Reason   string `json:"reason"`
	}{reply.Capability.Mode, reply.Capability.Readable, reply.Capability.Writable, reply.Capability.Reason}
}
