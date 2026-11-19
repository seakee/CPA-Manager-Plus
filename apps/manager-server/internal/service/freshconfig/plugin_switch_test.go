package freshconfig

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

func pluginDocument(enabled any) map[string]any {
	return map[string]any{"plugins": map[string]any{"enabled": enabled, "dir": "/srv/cpamp/plugins"}}
}

func pluginBaseline(t *testing.T, service *Service) string {
	t.Helper()
	view, err := service.ReadPlugins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range view.Groups {
		if group.ID == cpa.PluginConfigGroupID {
			return group.Baseline
		}
	}
	t.Fatalf("plugin group missing from the plugin scope: %+v", view.Groups)
	return ""
}

func pluginRequest(t *testing.T, service *Service, requestID string, value json.RawMessage) GroupRequest {
	t.Helper()
	return GroupRequest{
		RequestID: requestID,
		Expected:  Expected{Generation: "7", Baseline: pluginBaseline(t, service)},
		Edits:     []Edit{{Path: "plugins.enabled", Action: "set", Value: value}},
	}
}

// TestPluginSwitchWritesThroughTheSharedJournalAndReadsBack proves the switch is
// one journaled group operation on the same CAS journal as the gateway page, and
// that the page never projects it.
func TestPluginSwitchWritesThroughTheSharedJournalAndReadsBack(t *testing.T) {
	wire := &fakeWire{document: pluginDocument(false)}
	service, store := managedService(t, wire)

	view, err := service.ReadPlugins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.Capability.Mode != ModeManaged || !view.Capability.Readable || !view.Capability.Writable {
		t.Fatalf("plugin capability %+v", view.Capability)
	}
	if len(view.Fields) != 1 || view.Fields[0].Path != "plugins.enabled" || view.Fields[0].Presence != "present" {
		t.Fatalf("plugin projection %+v", view.Fields)
	}
	if value, ok := view.Fields[0].Value.(bool); !ok || value {
		t.Fatalf("plugin value %+v", view.Fields[0].Value)
	}
	if len(view.Families) != 0 {
		t.Fatalf("plugin scope projected rule families: %+v", view.Families)
	}

	operation, err := service.SavePluginGroup(context.Background(), cpa.PluginConfigGroupID, pluginRequest(t, service, strings.Repeat("a1", 16), json.RawMessage("true")))
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != "persisted" || !operation.HTTP200 || operation.Target != "group" || operation.Prefix != "plugins" {
		t.Fatalf("operation %+v", operation)
	}
	if len(wire.patches) != 1 || wire.patches[0].path != "plugins" || wire.patches[0].body != `{"enabled":true}` {
		t.Fatalf("patches %+v", wire.patches)
	}
	if len(wire.deletes) != 0 {
		t.Fatalf("deletes %v", wire.deletes)
	}
	if len(store.record.ConfigGroups) != 1 || len(store.record.ConfigGroups[cpa.PluginConfigGroupID].Operations) != 1 {
		t.Fatalf("journal %+v", store.record.ConfigGroups)
	}

	// Another writer observes the switch as persisted, and the effective value is
	// never claimed: CPA applies configuration asynchronously.
	view, err = service.ReadPlugins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	group := view.Groups[0]
	if group.ID != cpa.PluginConfigGroupID || group.Persisted != "persisted" || group.Effective != "unknown" {
		t.Fatalf("group view %+v", group)
	}
	if value, ok := view.Fields[0].Value.(bool); !ok || !value {
		t.Fatalf("read-back value %+v", view.Fields[0].Value)
	}

	// The gateway page is unchanged: the switch is a scope of its own.
	page, err := service.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Groups) != 21 || len(page.Fields) != 88 || len(page.Families) != 6 {
		t.Fatalf("gateway projection sizes %d %d %d", len(page.Groups), len(page.Fields), len(page.Families))
	}
	for _, entry := range page.Groups {
		if entry.ID == cpa.PluginConfigGroupID {
			t.Fatal("the plugin group leaked onto the gateway page")
		}
	}
	for _, field := range page.Fields {
		if strings.HasPrefix(field.Path, "plugins.") {
			t.Fatalf("the plugin leaf leaked onto the gateway page: %s", field.Path)
		}
	}
}

// TestPluginSwitchRejectsAStaleBaselineAndWritesNothing proves the CAS check the
// gateway page uses applies to the switch: a baseline that moved is a conflict.
func TestPluginSwitchRejectsAStaleBaselineAndWritesNothing(t *testing.T) {
	wire := &fakeWire{document: pluginDocument(false)}
	service, store := managedService(t, wire)
	request := pluginRequest(t, service, strings.Repeat("b2", 16), json.RawMessage("true"))
	// Another writer flips the switch between the read and the save.
	setDocumentValue(wire.document, []string{"plugins", "enabled"}, true)
	if _, err := service.SavePluginGroup(context.Background(), cpa.PluginConfigGroupID, request); !errors.Is(err, cpa.ErrSourceConflict) {
		t.Fatalf("stale baseline accepted: %v", err)
	}
	if len(wire.patches) != 0 {
		t.Fatalf("a conflicted switch save reached CPA: %+v", wire.patches)
	}
	if len(store.record.ConfigGroups) != 0 {
		t.Fatal("a conflicted switch save was journaled")
	}
}

// TestPluginSwitchUnknownResultIsReconciledByReadBackOnly proves a lost reply is
// settled by reading the document: the write is never resent, and the group stays
// locked until it is settled.
func TestPluginSwitchUnknownResultIsReconciledByReadBackOnly(t *testing.T) {
	wire := &fakeWire{document: pluginDocument(false)}
	service, store := managedService(t, wire)
	request := pluginRequest(t, service, strings.Repeat("c3", 16), json.RawMessage("true"))

	// The switch lands in CPA but the reply is lost.
	wire.patchErr = cpa.ErrApplyUnknown
	if _, err := service.SavePluginGroup(context.Background(), cpa.PluginConfigGroupID, request); !errors.Is(err, cpa.ErrApplyUnknown) {
		t.Fatalf("lost reply not unknown: %v", err)
	}
	wire.patchErr = nil
	setDocumentValue(wire.document, []string{"plugins", "enabled"}, true)

	// An unresolved original blocks the switch: the page must settle it first.
	blockedView, err := service.ReadPlugins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if blockedView.Groups[0].Writable || blockedView.Groups[0].Reason != ReasonUnknownOriginal {
		t.Fatalf("unresolved switch still writable: %+v", blockedView.Groups[0])
	}
	if _, err = service.SavePluginGroup(context.Background(), cpa.PluginConfigGroupID, GroupRequest{
		RequestID: strings.Repeat("d4", 16),
		Expected:  Expected{Generation: "7", Baseline: blockedView.Groups[0].Baseline},
		Edits:     []Edit{{Path: "plugins.enabled", Action: "set", Value: json.RawMessage("false")}},
	}); !errors.Is(err, cpa.ErrApplyUnknown) {
		t.Fatalf("unresolved original did not block the switch: %v", err)
	}

	patches := len(wire.patches)
	reconciled, err := service.PluginOperation(context.Background(), request.RequestID, true)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.State != "persisted" || reconciled.HTTP200 {
		t.Fatalf("read-back resolved to %+v", reconciled)
	}
	if len(wire.patches) != patches {
		t.Fatalf("reconciliation replayed the write: %d -> %d", patches, len(wire.patches))
	}
	if len(store.record.ConfigGroups[cpa.PluginConfigGroupID].Operations) != 1 {
		t.Fatal("reconciliation invented an operation")
	}
	settled, err := service.ReadPlugins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !settled.Groups[0].Writable || settled.Groups[0].Persisted != "persisted" || settled.Groups[0].Effective != "unknown" {
		t.Fatalf("switch stayed locked after reconcile: %+v", settled.Groups[0])
	}
}

// TestPluginSwitchIsRefusedOutsideTheManagedAuthority proves every profile that
// does not own the Supervisor destination fails closed: no read, no write, no
// journal entry.
func TestPluginSwitchIsRefusedOutsideTheManagedAuthority(t *testing.T) {
	wire := &fakeWire{document: pluginDocument(false)}
	private, _, _, _ := privateFixture(t, false)

	cases := []struct {
		name    string
		service *Service
		wire    *fakeWire
		mode    string
	}{
		{"external", externalServiceForPluginTest(t, wire), wire, ModeExternal},
		{"private", private, nil, ModePrivate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view, err := tc.service.ReadPlugins(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if view.Capability.Mode != tc.mode || view.Capability.Readable || view.Capability.Writable {
				t.Fatalf("capability %+v", view.Capability)
			}
			if view.Capability.Reason != ReasonPluginManagedOnly {
				t.Fatalf("reason %q", view.Capability.Reason)
			}
			// The refusal still projects the plugin scope's own table: a closed
			// profile is never answered with the gateway page's groups.
			if len(view.Groups) != 1 || view.Groups[0].ID != cpa.PluginConfigGroupID {
				t.Fatalf("closed projection %+v", view.Groups)
			}
			if len(view.Fields) != 1 || view.Fields[0].Path != "plugins.enabled" {
				t.Fatalf("closed projection fields %+v", view.Fields)
			}
			_, err = tc.service.SavePluginGroup(context.Background(), cpa.PluginConfigGroupID, GroupRequest{
				RequestID: strings.Repeat("e5", 16),
				Expected:  Expected{Generation: "7", Baseline: strings.Repeat("a", 64)},
				Edits:     []Edit{{Path: "plugins.enabled", Action: "set", Value: json.RawMessage("true")}},
			})
			if !errors.Is(err, ErrPluginAuthority) {
				t.Fatalf("save err = %v, want the closed refusal", err)
			}
			if tc.wire != nil && (len(tc.wire.patches) != 0 || tc.wire.reads != 0) {
				t.Fatalf("a refused profile reached CPA: reads=%d patches=%d", tc.wire.reads, len(tc.wire.patches))
			}
		})
	}
}

// externalServiceForPluginTest builds the External profile the gateway page
// already serves, so the refusal is about the scope and not a broken install.
func externalServiceForPluginTest(t *testing.T, wire *fakeWire) *Service {
	t.Helper()
	store := &memoryStore{record: managedRecord()}
	store.record.Operation.Strategy.Strategy = bootstrap.ConnectExternal
	store.record.Operation.Strategy.Stage = "committed"
	store.record.Connection = &model.ManagerCPAConnectionConfig{
		CPABaseURL: "https://external.test", ManagementKey: "cpamp_protected_envelope",
	}
	service := New(store, nil, nil)
	service.wire = wire
	service.transport = nil
	service.EnableExternal(func(context.Context, uint64) (string, string, error) {
		return store.record.Connection.CPABaseURL, "management-test-only", nil
	})
	// The profile question is asked of the one discriminator, so this fixture
	// answers it exactly as the committed Connect External checkpoint does.
	service.targets = func(op bootstrap.FreshOperation) bool {
		return op.Strategy.Strategy == bootstrap.ConnectExternal && op.Strategy.Stage == "committed"
	}
	service.externalWire = func(string) configWire { return wire }
	return service
}

// TestPluginRouteRefusesExternalBeforeAskingTheAuthority proves the managed-only
// scope is decided before the External authority is resolved: an installation that
// does not own the plugin root neither contacts the user's CPA nor consumes its
// management credential to say so.
func TestPluginRouteRefusesExternalBeforeAskingTheAuthority(t *testing.T) {
	wire := &fakeWire{document: pluginDocument(false)}
	store := &memoryStore{record: managedRecord()}
	store.record.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
		Strategy: bootstrap.ConnectExternal, Stage: "committed",
		TargetIdentity: "external:sha256:" + strings.Repeat("b", 64), TargetGeneration: 1,
	}
	store.record.Connection = &model.ManagerCPAConnectionConfig{
		CPABaseURL: "https://external.test", ManagementKey: "cpamp_protected_envelope",
	}
	service := New(store, nil, nil)
	service.wire = wire
	service.transport = nil
	asked := 0
	service.EnableExternal(func(context.Context, uint64) (string, string, error) {
		asked++
		return "https://external.test", "management-test-only", nil
	})
	service.targets = externalTargets()
	service.externalWire = func(string) configWire { return wire }
	view, err := service.ReadPlugins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.Capability.Reason != ReasonPluginManagedOnly {
		t.Fatalf("reason = %q", view.Capability.Reason)
	}
	if asked != 0 {
		t.Fatalf("the plugin route resolved the External authority %d times", asked)
	}
	if wire.reads != 0 {
		t.Fatalf("the plugin route read the user's CPA %d times", wire.reads)
	}
}

// TestPluginRequestValidationOwnsOnlyTheSwitch covers the closed request contract:
// one known leaf, a boolean shape and no gateway leaf or group.
func TestPluginRequestValidationOwnsOnlyTheSwitch(t *testing.T) {
	valid := GroupRequest{
		RequestID: strings.Repeat("f6", 16),
		Expected:  Expected{Generation: "7", Baseline: strings.Repeat("a", 64)},
		Edits:     []Edit{{Path: "plugins.enabled", Action: "set", Value: json.RawMessage("true")}},
	}
	if err := ValidatePluginGroupRequest(cpa.PluginConfigGroupID, valid); err != nil {
		t.Fatalf("valid switch refused: %v", err)
	}
	cases := []struct {
		name   string
		group  string
		mutate func(*GroupRequest)
	}{
		{name: "gateway leaf", group: cpa.PluginConfigGroupID, mutate: func(q *GroupRequest) {
			q.Edits = []Edit{{Path: "routing.strategy", Action: "set", Value: json.RawMessage(`"round-robin"`)}}
		}},
		{name: "gateway group", group: "routing"},
		{name: "gateway leaf on the plugin path", group: cpa.PluginConfigGroupID, mutate: func(q *GroupRequest) {
			q.Edits = []Edit{{Path: "plugins.dir", Action: "set", Value: json.RawMessage(`"/tmp"`)}}
		}},
		{name: "wrong shape", group: cpa.PluginConfigGroupID, mutate: func(q *GroupRequest) {
			q.Edits = []Edit{{Path: "plugins.enabled", Action: "set", Value: json.RawMessage(`"true"`)}}
		}},
		{name: "null value", group: cpa.PluginConfigGroupID, mutate: func(q *GroupRequest) {
			q.Edits = []Edit{{Path: "plugins.enabled", Action: "set", Value: json.RawMessage("null")}}
		}},
		{name: "duplicate leaf", group: cpa.PluginConfigGroupID, mutate: func(q *GroupRequest) {
			q.Edits = append(q.Edits, q.Edits[0])
		}},
		{name: "empty edits", group: cpa.PluginConfigGroupID, mutate: func(q *GroupRequest) { q.Edits = nil }},
		{name: "bad request id", group: cpa.PluginConfigGroupID, mutate: func(q *GroupRequest) { q.RequestID = "short" }},
		{name: "missing baseline", group: cpa.PluginConfigGroupID, mutate: func(q *GroupRequest) { q.Expected.Baseline = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := valid
			candidate.Edits = append([]Edit(nil), valid.Edits...)
			if tc.mutate != nil {
				tc.mutate(&candidate)
			}
			if err := ValidatePluginGroupRequest(tc.group, candidate); err == nil {
				t.Fatal("refused request accepted")
			}
		})
	}
	// The gateway page's own validator never accepts the plugin group.
	if err := ValidateGroupRequest(cpa.PluginConfigGroupID, valid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("gateway validator accepted the plugin group: %v", err)
	}
	// Nor does the plugin scope accept a gateway group.
	if err := ValidatePluginGroupRequest("routing", valid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("plugin validator accepted a gateway group: %v", err)
	}
}

// A transient proof failure on a managed install must not be reported as "this
// install does not own the plugin root". The operator would be told their
// install is the wrong shape and the switch would disappear, when the only fact
// is that the proof could not be taken right now.
func TestPluginSwitchReportsTransientManagedProofFailureAsUnavailable(t *testing.T) {
	wire := &fakeWire{document: pluginDocument(true)}
	service, _ := managedService(t, wire)
	// A managed install whose committed target no longer satisfies the pin: the
	// profile is still managed, but the proof fails.
	service.targets = func(bootstrap.FreshOperation) bool { return false }

	view, err := service.ReadPlugins(context.Background())
	if err != nil {
		if !errors.Is(err, cpa.ErrProofUnavailable) {
			t.Fatalf("read err = %v, want ErrProofUnavailable", err)
		}
	}
	// The proof could not be taken, so the profile is unavailable; what must not
	// happen is the managed-only refusal, which asserts a fact about the
	// install's shape that is not true here.
	if view.Capability.Reason == ReasonPluginManagedOnly {
		t.Fatalf("a transient managed proof failure was reported as %q", view.Capability.Reason)
	}
	if view.Capability.Writable {
		t.Fatal("a switch without proof was reported writable")
	}
}
