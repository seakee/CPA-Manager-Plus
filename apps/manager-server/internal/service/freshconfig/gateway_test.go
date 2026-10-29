package freshconfig

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
)

// privateTargets is the same binding for the private H1/direct profile, whose page
// and field journal predate the managed strategy and select the attested target
// alone.
func privateTargets(op bootstrap.FreshOperation) bool {
	return freshkeyapply.New(nil, nil, nil).TargetsFunc(freshkeyapply.SurfaceGatewayConfig)(op)
}

// managedTargets is the profile rule production wires into the page: the key
// authority's own discriminator bound to the gateway surface. A stub here would
// let the page pass its tests against a contract no installation enforces.
func managedTargets(op bootstrap.FreshOperation) bool {
	return freshkeyapply.NewManaged(nil, nil, nil, "http://127.0.0.1:8317", nil).
		TargetsFunc(freshkeyapply.SurfaceGatewayConfig)(op)
}

// memoryStore is the Fresh CAS in miniature: one record, one revision, and the
// same conflict semantics the SQLite store enforces.
type memoryStore struct {
	record freshstore.Record
}

func (m *memoryStore) Load(context.Context) (freshstore.Record, error) {
	return cloneRecord(m.record), nil
}

func (m *memoryStore) Create(context.Context, bootstrap.FreshIdentity, *model.AdminCredential) (freshstore.Record, error) {
	return freshstore.Record{}, errors.New("unsupported")
}

func (m *memoryStore) Update(_ context.Context, id bootstrap.FreshIdentity, revision uint64, fn func(*freshstore.Record) error) (freshstore.Record, error) {
	if m.record.Operation.Identity != id || m.record.Operation.Revision != revision {
		return freshstore.Record{}, freshstore.ErrConflict
	}
	next := cloneRecord(m.record)
	if err := fn(&next); err != nil {
		return freshstore.Record{}, err
	}
	next.Operation.Revision++
	m.record = next
	return cloneRecord(next), nil
}

func cloneRecord(record freshstore.Record) freshstore.Record {
	data, err := json.Marshal(record)
	if err != nil {
		panic(err)
	}
	var cloned freshstore.Record
	if err := json.Unmarshal(data, &cloned); err != nil {
		panic(err)
	}
	if cloned.Config == nil {
		cloned.Config = map[string]*freshstore.FieldJournal{}
	}
	if cloned.ConfigInvalid == nil {
		cloned.ConfigInvalid = map[string]bool{}
	}
	if cloned.ConfigGroups == nil {
		cloned.ConfigGroups = map[string]*freshstore.ConfigGroupJournal{}
	}
	cloned.Operation = record.Operation
	return cloned
}

// fakeWire is CPA's configuration surface with a document in memory.
type fakeWire struct {
	document    map[string]any
	documentErr error
	patchErr    error
	deleteErr   error
	putErr      error
	runtime     bool
	// legacy reads for a pre-cutover field operation
	fieldPresence string
	fieldValue    any
	fieldErr      error

	reads   int
	patches []wireCall
	deletes []string
	puts    []wireCall
}

type wireCall struct {
	path string
	body string
}

func (f *fakeWire) ReadConfigDocument(context.Context, string) (map[string]any, error) {
	f.reads++
	if f.documentErr != nil {
		return nil, f.documentErr
	}
	return f.document, nil
}

func (f *fakeWire) PatchConfigPath(_ context.Context, _, prefix string, payload []byte) error {
	f.patches = append(f.patches, wireCall{prefix, string(payload)})
	if f.patchErr != nil {
		return f.patchErr
	}
	var body map[string]any
	if json.Unmarshal(payload, &body) != nil {
		return cpa.ErrApplyUnknown
	}
	mergeDocument(f.document, strings.Split(prefix, "."), body)
	return nil
}

func (f *fakeWire) PutConfigValue(_ context.Context, _, path string, payload []byte) error {
	f.puts = append(f.puts, wireCall{path, string(payload)})
	if f.putErr != nil {
		return f.putErr
	}
	var value any
	if json.Unmarshal(payload, &value) != nil {
		return cpa.ErrApplyUnknown
	}
	setDocumentValue(f.document, strings.Split(path, "."), value)
	return nil
}

func (f *fakeWire) DeleteConfigPath(_ context.Context, _, path string) error {
	f.deletes = append(f.deletes, path)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	deleteDocumentValue(f.document, strings.Split(path, "."))
	return nil
}

func (f *fakeWire) ReadConfigFieldValue(context.Context, string, string) (string, any, error) {
	return f.fieldPresence, f.fieldValue, f.fieldErr
}

func (f *fakeWire) ReconcileConfigField(context.Context, cpa.ConfigFieldSource, string, string, any) (cpa.ConfigFieldSource, error) {
	return cpa.ConfigFieldSource{}, cpa.ErrApplyUnknown
}

func (f *fakeWire) ConfigFieldSource(context.Context) (cpa.ConfigFieldSource, error) {
	return cpa.ConfigFieldSource{}, nil
}

func (f *fakeWire) HasRuntimeJournal() bool { return f.runtime }

// The private H1/direct profile has its own transport surface. The group-path
// fake refuses it, so a test that reaches it by mistake fails loudly instead of
// silently writing a leaf through the group journal.
func (f *fakeWire) ReadConfigField(context.Context, string, string) (cpa.ConfigFieldSource, string, any, error) {
	return cpa.ConfigFieldSource{}, "unknown", nil, cpa.ErrProofUnavailable
}

func (f *fakeWire) BeginConfigField(context.Context, cpa.ConfigFieldSource, string, string, any) error {
	return cpa.ErrProofUnavailable
}

func (f *fakeWire) PutConfigField(context.Context, cpa.ConfigFieldSource, string, string, string, any) error {
	return cpa.ErrProofUnavailable
}

func (f *fakeWire) WriteH1ConfigField(context.Context, string, cpa.H1Step, string, string, any) error {
	return cpa.ErrProofUnavailable
}

func mergeDocument(document map[string]any, prefix []string, body map[string]any) {
	cursor := document
	for _, part := range prefix {
		next, ok := cursor[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			cursor[part] = next
		}
		cursor = next
	}
	for key, value := range body {
		if nested, ok := value.(map[string]any); ok {
			existing, _ := cursor[key].(map[string]any)
			if existing == nil {
				existing = map[string]any{}
			}
			mergeDocument(existing, []string{}, nested)
			cursor[key] = existing
			continue
		}
		cursor[key] = value
	}
}

func setDocumentValue(document map[string]any, parts []string, value any) {
	cursor := document
	for _, part := range parts[:len(parts)-1] {
		next, ok := cursor[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			cursor[part] = next
		}
		cursor = next
	}
	cursor[parts[len(parts)-1]] = value
}

func deleteDocumentValue(document map[string]any, parts []string) {
	cursor := document
	for _, part := range parts[:len(parts)-1] {
		next, ok := cursor[part].(map[string]any)
		if !ok {
			return
		}
		cursor = next
	}
	delete(cursor, parts[len(parts)-1])
}

func managedRecord() freshstore.Record {
	return freshstore.Record{
		Operation: bootstrap.FreshOperation{
			Revision: 1,
			Identity: bootstrap.FreshIdentity{AdmissionID: "admission", InstallationID: "install-identity-0001", Generation: "gen-1", DatabasePath: "/tmp/manager.sqlite", OperationID: "fresh-operation"},
			Strategy: bootstrap.FreshStrategyCheckpoint{
				Strategy: bootstrap.NewManaged, Stage: "committed", TargetIdentity: "managed-template",
				TargetGeneration: 7, DestinationConnectionIdentity: "managed:sha256:" + strings.Repeat("a", 64),
			},
		},
		Config:        map[string]*freshstore.FieldJournal{},
		ConfigInvalid: map[string]bool{},
		ConfigGroups:  map[string]*freshstore.ConfigGroupJournal{},
	}
}

func managedService(t *testing.T, wire *fakeWire) (*Service, *memoryStore) {
	t.Helper()
	store := &memoryStore{record: managedRecord()}
	service := New(store, func(context.Context, uint64) (string, error) { return "management-test-only", nil }, managedTargets)
	service.wire = wire
	service.transport = nil
	service.managed = true
	service.endpoint = "http://127.0.0.1:8317"
	return service, store
}

func baseline(t *testing.T, service *Service, groupID string) string {
	t.Helper()
	view, err := service.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range view.Groups {
		if group.ID == groupID {
			return group.Baseline
		}
	}
	t.Fatalf("group %s missing", groupID)
	return ""
}

func familyBaselineFor(t *testing.T, service *Service, family string) string {
	t.Helper()
	view, err := service.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range view.Families {
		if item.Family == family {
			return item.Baseline
		}
	}
	t.Fatalf("family %s missing", family)
	return ""
}

func TestReadProjectsOnlyThePageScope(t *testing.T) {
	wire := &fakeWire{document: map[string]any{
		"routing": map[string]any{
			"strategy":         "round-robin",
			"session-affinity": nil,
			"retry":            map[string]any{"request-retry": 3},
		},
		"access": map[string]any{"api-keys": []any{"sk-live-secret"}},
		"requests": map[string]any{"payload": map[string]any{
			"filter": []any{map[string]any{"models": []any{map[string]any{"name": "gpt-*"}}, "params": []any{"metadata.client"}}},
		}},
	}}
	service, _ := managedService(t, wire)
	view, err := service.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.Capability.Mode != ModeManaged || !view.Capability.Writable || !view.Capability.RulesWritable {
		t.Fatalf("capability %+v", view.Capability)
	}
	if len(view.Fields) != 39 || len(view.Families) != 5 || len(view.Groups) != 11 {
		t.Fatalf("projection sizes %d %d %d", len(view.Fields), len(view.Families), len(view.Groups))
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "sk-live-secret") {
		t.Fatal("undefined secret escaped the projection")
	}
	presence := map[string]string{}
	for _, field := range view.Fields {
		presence[field.Path] = field.Presence
	}
	if presence["routing.strategy"] != "present" || presence["routing.session-affinity"] != "null" ||
		presence["routing.retry.request-retry"] != "present" || presence["requests.proxy-url"] != "missing" {
		t.Fatalf("presence %v", presence)
	}
	for _, family := range view.Families {
		switch family.Family {
		case "filter":
			if family.Rules == nil || family.ReadOnly {
				t.Fatalf("filter family %+v", family)
			}
		default:
			if family.Rules != nil || family.ReadOnly {
				t.Fatalf("family %s %+v", family.Family, family)
			}
		}
	}
}

func TestGroupSaveWritesOnlyEditedLeavesAndRestoresDefaults(t *testing.T) {
	wire := &fakeWire{document: map[string]any{
		"routing":  map[string]any{"retry": map[string]any{"request-retry": 3, "max-retry-interval": 30}},
		"requests": map[string]any{"passthrough-headers": false},
	}}
	service, store := managedService(t, wire)
	request := GroupRequest{
		RequestID: strings.Repeat("ab", 16),
		Expected:  Expected{Generation: "7", Baseline: baseline(t, service, "retry")},
		Edits: []Edit{
			{Path: "routing.retry.request-retry", Action: "set", Value: json.RawMessage("5")},
			{Path: "routing.retry.max-retry-interval", Action: "default"},
		},
	}
	operation, err := service.SaveGroup(context.Background(), "retry", request)
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != "persisted" || operation.HTTP200 != true || operation.Target != "group" {
		t.Fatalf("operation %+v", operation)
	}
	if len(wire.patches) != 1 || wire.patches[0].path != "routing.retry" || wire.patches[0].body != `{"request-retry":5}` {
		t.Fatalf("patches %+v", wire.patches)
	}
	if len(wire.deletes) != 1 || wire.deletes[0] != "routing.retry.max-retry-interval" {
		t.Fatalf("deletes %v", wire.deletes)
	}
	// Another group's journal is untouched: one group save is one journal entry.
	if _, ok := store.record.ConfigGroups["requests"]; ok {
		t.Fatal("unrelated group journal written")
	}
	if len(store.record.ConfigGroups["retry"].Operations) != 1 {
		t.Fatal("operation not journaled")
	}
	// A replay of the same request ID is the original operation, never a write.
	replayed, err := service.SaveGroup(context.Background(), "retry", request)
	if err != nil || replayed.RequestID != operation.RequestID || len(wire.patches) != 1 {
		t.Fatalf("replay %+v err=%v patches=%d", replayed, err, len(wire.patches))
	}
}

func TestGroupSaveRejectsAStaleBaselineAndWritesNothing(t *testing.T) {
	wire := &fakeWire{document: map[string]any{"routing": map[string]any{"retry": map[string]any{"request-retry": 3}}}}
	service, store := managedService(t, wire)
	stale := baseline(t, service, "retry")
	// Another writer changes a sibling leaf of the same group.
	setDocumentValue(wire.document, []string{"routing", "retry", "max-retry-interval"}, 30)
	_, err := service.SaveGroup(context.Background(), "retry", GroupRequest{
		RequestID: strings.Repeat("cd", 16),
		Expected:  Expected{Generation: "7", Baseline: stale},
		Edits:     []Edit{{Path: "routing.retry.request-retry", Action: "set", Value: json.RawMessage("5")}},
	})
	if !errors.Is(err, cpa.ErrSourceConflict) {
		t.Fatalf("stale baseline accepted: %v", err)
	}
	if len(wire.patches) != 0 || len(wire.deletes) != 0 {
		t.Fatal("a conflicted save reached CPA")
	}
	if len(store.record.ConfigGroups) != 0 {
		t.Fatal("a conflicted save was journaled")
	}
}

func TestGroupRequestValidationOwnsOnlyShapesAndProductRules(t *testing.T) {
	valid := GroupRequest{
		RequestID: strings.Repeat("ef", 16),
		Expected:  Expected{Generation: "7", Baseline: strings.Repeat("a", 64)},
		Edits:     []Edit{{Path: "routing.retry.request-retry", Action: "set", Value: json.RawMessage("42")}},
	}
	if err := ValidateGroupRequest("retry", valid); err != nil {
		t.Fatalf("unbounded integer refused: %v", err)
	}
	for name, mutate := range map[string]func(*GroupRequest){
		"foreign leaf": func(q *GroupRequest) {
			q.Edits = []Edit{{Path: "routing.strategy", Action: "set", Value: json.RawMessage(`"x"`)}}
		},
		"duplicate leaf": func(q *GroupRequest) { q.Edits = append(q.Edits, q.Edits[0]) },
		"wrong shape": func(q *GroupRequest) {
			q.Edits = []Edit{{Path: "routing.retry.request-retry", Action: "set", Value: json.RawMessage(`"3"`)}}
		},
		"default with value": func(q *GroupRequest) {
			q.Edits = []Edit{{Path: "routing.retry.request-retry", Action: "default", Value: json.RawMessage("3")}}
		},
		"bad baseline":       func(q *GroupRequest) { q.Expected.Baseline = "abc" },
		"missing generation": func(q *GroupRequest) { q.Expected.Generation = "" },
		"bad request id":     func(q *GroupRequest) { q.RequestID = "short" },
		"empty edits":        func(q *GroupRequest) { q.Edits = nil },
		"proxy with password": func(q *GroupRequest) {
			q.Edits = []Edit{{Path: "requests.proxy-url", Action: "set", Value: json.RawMessage(`"http://user:pass@proxy.test:8080"`)}}
		},
	} {
		candidate := valid
		candidate.Edits = append([]Edit(nil), valid.Edits...)
		mutate(&candidate)
		group := "retry"
		if len(candidate.Edits) == 1 && candidate.Edits[0].Path == "requests.proxy-url" {
			group = "request"
		}
		if err := ValidateGroupRequest(group, candidate); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if err := ValidateGroupRequest("nope", valid); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown group accepted")
	}
}

func TestLostReplyIsResolvedByReadBackAndNeverResent(t *testing.T) {
	wire := &fakeWire{document: map[string]any{"requests": map[string]any{"streaming": map[string]any{"keepalive-seconds": 15}}}}
	service, store := managedService(t, wire)
	request := GroupRequest{
		RequestID: strings.Repeat("11", 16),
		Expected:  Expected{Generation: "7", Baseline: baseline(t, service, "streaming")},
		Edits:     []Edit{{Path: "requests.streaming.bootstrap-retries", Action: "set", Value: json.RawMessage("2")}},
	}
	// The write lands but the reply is lost.
	wire.patchErr = cpa.ErrApplyUnknown
	if _, err := service.SaveGroup(context.Background(), "streaming", request); !errors.Is(err, cpa.ErrApplyUnknown) {
		t.Fatalf("lost reply not unknown: %v", err)
	}
	wire.patchErr = nil
	// The same group cannot be saved again while its original is unresolved.
	if _, err := service.SaveGroup(context.Background(), "streaming", GroupRequest{
		RequestID: strings.Repeat("22", 16),
		Expected:  Expected{Generation: "7", Baseline: baseline(t, service, "streaming")},
		Edits:     []Edit{{Path: "requests.streaming.keepalive-seconds", Action: "set", Value: json.RawMessage("20")}},
	}); !errors.Is(err, cpa.ErrApplyUnknown) {
		t.Fatalf("unresolved original did not block the group: %v", err)
	}
	patches := len(wire.patches)
	reconciled, err := service.Operation(context.Background(), request.RequestID, true)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.State != "failed" || reconciled.Code != "write_not_applied" {
		t.Fatalf("absent value on an unacknowledged write resolved to %+v", reconciled)
	}
	if len(wire.patches) != patches {
		t.Fatal("reconciliation replayed the write")
	}
	if len(store.record.ConfigGroups["streaming"].Operations) != 1 {
		t.Fatal("reconciliation invented an operation")
	}
}

func TestAcknowledgedWriteAnotherWriterReplacedIsSuperseded(t *testing.T) {
	// A later writer wins the file between CPA's 200 and the read-back: the
	// original is persisted evidence, and the foreign value is the outcome.
	wire := &fakeWire{document: map[string]any{"requests": map[string]any{"streaming": map[string]any{"keepalive-seconds": 25}}}}
	service, _ := managedService(t, wire)
	service.wire = &noMergeWire{fakeWire: wire}
	request := GroupRequest{
		RequestID: strings.Repeat("44", 16),
		Expected:  Expected{Generation: "7", Baseline: baseline(t, service, "streaming")},
		Edits:     []Edit{{Path: "requests.streaming.keepalive-seconds", Action: "set", Value: json.RawMessage("20")}},
	}
	if _, err := service.SaveGroup(context.Background(), "streaming", request); err != nil {
		t.Fatal(err)
	}
	view, err := service.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range view.Groups {
		if group.ID != "streaming" {
			continue
		}
		if group.Operation == nil || group.Operation.State != "persisted" || group.Operation.Code != "superseded" {
			t.Fatalf("superseded not recorded: %+v", group.Operation)
		}
		if group.Effective != "unknown" {
			t.Fatal("effectiveness claimed")
		}
	}
}

// noMergeWire accepts the PATCH without applying it, exactly like a later writer
// winning the file after CPA answered 200.
type noMergeWire struct{ *fakeWire }

func (w *noMergeWire) PatchConfigPath(context.Context, string, string, []byte) error { return nil }

func TestDefiniteRejectionIsTerminalAndWritableAgain(t *testing.T) {
	wire := &fakeWire{document: map[string]any{"multimedia": map[string]any{"disable-image-generation": false}}}
	service, store := managedService(t, wire)
	wire.patchErr = cpa.ConfigWriteError{Status: 422, Code: "invalid_config", Message: "yaml: unmarshal errors"}
	request := GroupRequest{
		RequestID: strings.Repeat("55", 16),
		Expected:  Expected{Generation: "7", Baseline: baseline(t, service, "media")},
		Edits:     []Edit{{Path: "multimedia.gpt-image-2-base-model", Action: "set", Value: json.RawMessage(`"gpt-5"`)}},
	}
	_, err := service.SaveGroup(context.Background(), "media", request)
	// CPA's own string is journaled as evidence; the page receives the bounded
	// code this Manager owns.
	var refusal CPARejection
	if !errors.As(err, &refusal) || refusal.Code != "invalid_config" {
		t.Fatalf("refusal not reported: %v", err)
	}
	stored := store.record.ConfigGroups["media"].Operations[request.RequestID]
	if stored.State != "failed" || stored.Code != "invalid_config" {
		t.Fatalf("failure not recorded: %+v", stored)
	}
	// The field is writable again: the original is terminal, not unknown.
	view, err := service.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range view.Groups {
		if group.ID == "media" && (!group.Writable || group.Reason != "") {
			t.Fatalf("group stayed locked: %+v", group)
		}
	}
	if len(wire.patches) != 1 {
		t.Fatal("a definite rejection was retried")
	}
}

func TestExternalProfileWritesScalarsAndRefusesRuleFamilies(t *testing.T) {
	wire := &fakeWire{document: map[string]any{"routing": map[string]any{"strategy": "round-robin"}}}
	store := &memoryStore{record: managedRecord()}
	store.record.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
		Strategy: bootstrap.ConnectExternal, Stage: "committed", TargetIdentity: "external:sha256:" + strings.Repeat("b", 64), TargetGeneration: 1,
	}
	store.record.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: "https://external.test", ManagementKey: "cpamp_protected_envelope"}
	service := New(store, nil, nil)
	service.wire = wire
	service.transport = nil
	service.EnableExternal(func(context.Context, uint64) (string, string, error) {
		return "https://external.test", "management-test-only", nil
	})
	// The page asks the one discriminator; an External checkpoint naming this
	// installation's connection is what its own authority answers for.
	service.targets = externalTargets()
	service.externalWire = func(string) configWire { return wire }
	service.externalProbe = supportedExternalVersion
	view, err := service.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.Capability.Mode != ModeExternal || !view.Capability.Writable || view.Capability.RulesWritable {
		t.Fatalf("external capability %+v", view.Capability)
	}
	for _, family := range view.Families {
		if !family.ReadOnly {
			t.Fatalf("external family writable: %+v", family)
		}
	}
	request := GroupRequest{
		RequestID: strings.Repeat("66", 16),
		Expected:  Expected{Generation: "1", Baseline: baseline(t, service, "routing")},
		Edits:     []Edit{{Path: "routing.strategy", Action: "set", Value: json.RawMessage(`"fill-first"`)}},
	}
	if _, err = service.SaveGroup(context.Background(), "routing", request); err != nil {
		t.Fatalf("external scalar refused: %v", err)
	}
	if _, err = service.SaveFamily(context.Background(), "filter", FamilyRequest{
		RequestID: strings.Repeat("77", 16),
		Expected:  Expected{Generation: "1", Baseline: familyBaselineFor(t, service, "filter")},
		Rules:     json.RawMessage(`[{"models":[{"name":"*"}],"params":["a"]}]`),
	}); !errors.Is(err, ErrRulesReadOnly) {
		t.Fatalf("external rule family written: %v", err)
	}
	if len(wire.puts) != 0 {
		t.Fatal("rule family reached CPA")
	}
}

func TestLegacyFieldOperationStaysReconcilableAndIsNeverWritten(t *testing.T) {
	wire := &fakeWire{
		document:      map[string]any{"routing": map[string]any{"retry": map[string]any{"request-retry": 3}}},
		fieldPresence: "present", fieldValue: 3,
	}
	store := &memoryStore{record: managedRecord()}
	requestID := strings.Repeat("99", 16)
	store.record.Config["routing.retry.request-retry"] = &freshstore.FieldJournal{
		Latest: requestID,
		Operations: map[string]freshstore.ConfigOperation{
			requestID: {
				RequestID: requestID, Field: "routing.retry.request-retry", Value: 3,
				SourceID: "install-identity-0001", Generation: "7", Revision: 1, State: "unknown", HTTP200: true,
			},
		},
	}
	service := New(store, func(context.Context, uint64) (string, error) { return "management-test-only", nil }, managedTargets)
	service.wire = wire
	service.transport = nil
	service.managed = true
	operation, err := service.Operation(context.Background(), requestID, true)
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != "persisted" || operation.RequestID != requestID || operation.Group != "retry" {
		t.Fatalf("legacy operation %+v", operation)
	}
	if len(wire.patches) != 0 || len(wire.puts) != 0 || len(wire.deletes) != 0 {
		t.Fatal("legacy reconciliation wrote to CPA")
	}
}

// TestFailureAfterThePatchLandedIsUnknownNotUnchanged proves a save that mixes a
// set and a default is not reported as "nothing was written" when the later
// DELETE is refused. The PATCH is sent first, so by that point the document has
// already moved; the only truthful answer is unknown, resolved by reading back.
func TestFailureAfterThePatchLandedIsUnknownNotUnchanged(t *testing.T) {
	wire := &fakeWire{
		document: map[string]any{
			"routing": map[string]any{"retry": map[string]any{"request-retry": 3, "max-retry-interval": 30}},
		},
		deleteErr: cpa.ConfigWriteError{Status: 422, Code: "invalid_config"},
	}
	service, store := managedService(t, wire)
	request := GroupRequest{
		RequestID: strings.Repeat("ab", 16),
		Expected:  Expected{Generation: "7", Baseline: baseline(t, service, "retry")},
		Edits: []Edit{
			{Path: "routing.retry.request-retry", Action: "set", Value: json.RawMessage("5")},
			{Path: "routing.retry.max-retry-interval", Action: "default"},
		},
	}
	if _, err := service.SaveGroup(context.Background(), "retry", request); !errors.Is(err, cpa.ErrApplyUnknown) {
		t.Fatalf("err = %v, want an unknown result", err)
	}
	// The PATCH went out before the failed DELETE, and it landed.
	if len(wire.patches) != 1 {
		t.Fatalf("patches %+v", wire.patches)
	}
	// The journal must still say unknown, never failed: the document did change.
	journal := store.record.ConfigGroups["retry"]
	operation := journal.Operations[request.RequestID]
	if operation.State != "unknown" || operation.HTTP200 {
		t.Fatalf("operation recorded as %+v", operation)
	}
	// Reading back now proves the PATCH half landed, so the result is not
	// "write_not_applied" either; a second save replays nothing.
	reconciled, err := service.Operation(context.Background(), request.RequestID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire.patches) != 1 || len(wire.deletes) != 1 {
		t.Fatalf("reconcile resent the write: patches=%d deletes=%d", len(wire.patches), len(wire.deletes))
	}
	if reconciled.State != "persisted" || reconciled.Code != "partial" {
		t.Fatalf("partially applied save recorded as %+v", reconciled)
	}
	// The half that landed is visible in the document, so the group is writable
	// again against the new baseline rather than locked on an unknown original.
	view, err := service.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range view.Groups {
		if group.ID != "retry" {
			continue
		}
		if !group.Writable || group.Reason != "" {
			t.Fatalf("group stayed locked after reconcile: %+v", group)
		}
	}
}

// TestDefiniteRejectionOfOneEditLandsOnThatField proves a CPA refusal names the
// edited field even when CPA did not. CPA reports "field" for only a few codes,
// so a one-leaf save would otherwise surface as a group-wide failure with no
// indication of what to fix.
func TestDefiniteRejectionOfOneEditLandsOnThatField(t *testing.T) {
	wire := &fakeWire{
		document: map[string]any{"routing": map[string]any{"retry": map[string]any{"request-retry": 3, "max-retry-interval": 30}}},
		patchErr: cpa.ConfigWriteError{Status: 422, Code: "invalid_config"},
	}
	service, _ := managedService(t, wire)
	request := GroupRequest{
		RequestID: strings.Repeat("ab", 16),
		Expected:  Expected{Generation: "7", Baseline: baseline(t, service, "retry")},
		Edits:     []Edit{{Path: "routing.retry.max-retry-interval", Action: "set", Value: json.RawMessage("45")}},
	}
	_, err := service.SaveGroup(context.Background(), "retry", request)
	var rejection CPARejection
	if !errors.As(err, &rejection) {
		t.Fatalf("err = %v, want a rejection", err)
	}
	if rejection.Field != "routing.retry.max-retry-interval" {
		t.Fatalf("rejection %+v did not name the edited field", rejection)
	}
	// A save that edited two leaves has no single owner for the message.
	wire.patchErr = cpa.ConfigWriteError{Status: 422, Code: "invalid_config"}
	second := GroupRequest{
		RequestID: strings.Repeat("cd", 16),
		Expected:  Expected{Generation: "7", Baseline: baseline(t, service, "retry")},
		Edits: []Edit{
			{Path: "routing.retry.max-retry-interval", Action: "set", Value: json.RawMessage("45")},
			{Path: "routing.retry.request-retry", Action: "set", Value: json.RawMessage("4")},
		},
	}
	_, err = service.SaveGroup(context.Background(), "retry", second)
	if !errors.As(err, &rejection) || rejection.Field != "" {
		t.Fatalf("multi-leaf rejection claimed a field: %+v err=%v", rejection, err)
	}
}

// TestSaveRefusesATargetTheBrowserDidNotRead proves the echoed generation is
// checked, not just stored. Without it a draft built against one CPA is applied
// to whatever target the Manager now writes.
func TestSaveRefusesATargetTheBrowserDidNotRead(t *testing.T) {
	wire := &fakeWire{document: map[string]any{"routing": map[string]any{"retry": map[string]any{"request-retry": 3, "max-retry-interval": 30}}}}
	service, store := managedService(t, wire)
	if _, err := service.SaveGroup(context.Background(), "retry", GroupRequest{
		RequestID: strings.Repeat("ab", 16),
		Expected:  Expected{Generation: "6", Baseline: baseline(t, service, "retry")},
		Edits:     []Edit{{Path: "routing.retry.request-retry", Action: "set", Value: json.RawMessage("5")}},
	}); !errors.Is(err, ErrGenerationMismatch) {
		t.Fatalf("stale generation accepted: %v", err)
	}
	if len(wire.patches) != 0 || len(store.record.ConfigGroups) != 0 {
		t.Fatal("refused save still wrote")
	}
	if _, err := service.SaveFamily(context.Background(), "filter", FamilyRequest{
		RequestID: strings.Repeat("cd", 16),
		Expected:  Expected{Generation: "6", Baseline: familyBaselineFor(t, service, "filter")},
		Rules:     json.RawMessage(`[{"models":[{"name":"*"}],"params":["a"]}]`),
	}); !errors.Is(err, ErrGenerationMismatch) {
		t.Fatalf("stale generation accepted for a family: %v", err)
	}
	if len(wire.puts) != 0 {
		t.Fatal("refused family save reached CPA")
	}
}

// TestExternalProfileRefusesAnAuthorityItCannotResolve proves the page writes only
// what the External authority resolves. When that authority cannot prove the
// committed connection, the page stays closed and never contacts a CPA.
func TestExternalProfileRefusesAnAuthorityItCannotResolve(t *testing.T) {
	wire := &fakeWire{document: map[string]any{"routing": map[string]any{"strategy": "round-robin"}}}
	store := &memoryStore{record: managedRecord()}
	store.record.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
		Strategy: bootstrap.ConnectExternal, Stage: "committed", TargetIdentity: "external:sha256:" + strings.Repeat("b", 64), TargetGeneration: 1,
	}
	store.record.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: "https://committed.test", ManagementKey: "cpamp_protected_envelope"}
	service := New(store, nil, nil)
	service.wire = wire
	service.transport = nil
	service.EnableExternal(func(context.Context, uint64) (string, string, error) {
		return "", "", cpa.ErrProofUnavailable
	})
	service.targets = externalTargets()
	service.externalWire = func(string) configWire { return wire }
	view, err := service.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.Capability.Readable || view.Capability.Writable || view.Capability.Reason != ReasonUnavailable {
		t.Fatalf("unresolved authority treated as writable: %+v", view.Capability)
	}
	if wire.reads != 0 {
		t.Fatal("unowned connection was contacted")
	}
}

// TestExternalProfileReportsARejectedManagementKey proves a 401 from the user's
// CPA is its own refusal on the page. It is distinct from an unavailable
// authority: only rebinding the connection resolves it, so the page must not
// offer a retry.
func TestExternalProfileReportsARejectedManagementKey(t *testing.T) {
	wire := &fakeWire{document: map[string]any{"routing": map[string]any{"strategy": "round-robin"}}}
	store := &memoryStore{record: managedRecord()}
	store.record.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
		Strategy: bootstrap.ConnectExternal, Stage: "committed", TargetIdentity: "external:sha256:" + strings.Repeat("b", 64), TargetGeneration: 1,
	}
	service := New(store, nil, nil)
	service.wire = wire
	service.transport = nil
	service.EnableExternal(func(context.Context, uint64) (string, string, error) {
		return "", "", cpa.ErrManagementRejected
	})
	service.targets = externalTargets()
	service.externalWire = func(string) configWire { return wire }
	view, err := service.Read(context.Background())
	if err != nil {
		t.Fatalf("read = %v", err)
	}
	if view.Capability.Readable || view.Capability.Writable || view.Capability.RulesWritable ||
		view.Capability.Reason != ReasonManagementRejected {
		t.Fatalf("rejected credential treated as another mode: %+v", view.Capability)
	}
	if wire.reads != 0 {
		t.Fatal("a rejected credential still contacted the CPA")
	}
}

// TestUnresolvedLegacyFieldOperationBlocksItsGroup proves the pre-cutover journal
// still gates new saves. Resolving an old field operation reads the current
// document, so letting the group save on top of it would let the reconcile read
// the newer value and record the wrong outcome.
func TestUnresolvedLegacyFieldOperationBlocksItsGroup(t *testing.T) {
	wire := &fakeWire{document: map[string]any{"routing": map[string]any{"retry": map[string]any{"request-retry": 3, "max-retry-interval": 30}}}}
	service, store := managedService(t, wire)
	legacyID := strings.Repeat("cd", 16)
	store.record.Config["routing.retry.request-retry"] = &freshstore.FieldJournal{
		Latest: legacyID,
		Operations: map[string]freshstore.ConfigOperation{
			legacyID: {RequestID: legacyID, Field: "routing.retry.request-retry", Value: 4,
				SourceID: "install-identity-0001", Generation: "7", Revision: 1, State: "unknown"},
		},
	}
	view, err := service.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range view.Groups {
		switch group.ID {
		case "retry":
			if group.Writable || group.Reason != ReasonLegacyUnresolved {
				t.Fatalf("group holding an unresolved legacy operation stayed writable: %+v", group)
			}
		default:
			if group.ID == "request" && !group.Writable {
				t.Fatalf("unrelated group blocked too: %+v", group)
			}
		}
	}
	for _, field := range view.Fields {
		if field.Path == "routing.retry.max-retry-interval" && field.Editable {
			t.Fatal("field of a blocked group stayed editable")
		}
	}
	// The save itself is refused, not merely greyed out in the projection.
	if _, err = service.SaveGroup(context.Background(), "retry", GroupRequest{
		RequestID: strings.Repeat("ab", 16),
		Expected:  Expected{Generation: "7", Baseline: baseline(t, service, "retry")},
		Edits:     []Edit{{Path: "routing.retry.request-retry", Action: "set", Value: json.RawMessage("5")}},
	}); !errors.Is(err, ErrLegacyUnresolved) {
		t.Fatalf("save on a blocked group = %v", err)
	}
	if len(wire.patches) != 0 {
		t.Fatal("blocked save reached CPA")
	}
}

// externalTargets is the real External surface rule, not a stub: the page asks the
// key authority's own discriminator exactly as production wires it.
func externalTargets() func(bootstrap.FreshOperation) bool {
	return freshkeyapply.NewExternal(nil, nil, nil).TargetsFunc(freshkeyapply.SurfaceGatewayConfig)
}

// supportedExternalVersion stands in for a CPA that advertises the v8 contract the
// fake wire serves. The probe itself is tested against its own fake server.
func supportedExternalVersion(context.Context, string, string) (cpa.ExternalVersionReport, error) {
	return cpa.ClassifyExternalVersion("v8.0.13"), nil
}

// TestExternalProfileReportsAMovedTarget proves a connection the checkpoint no
// longer names is its own blocker on the page. The installation exists, so it is
// neither an unavailable authority nor a missing proof: only rebinding the
// connection reaches it again, and nothing here contacts the old address.
func TestExternalProfileReportsAMovedTarget(t *testing.T) {
	wire := &fakeWire{document: map[string]any{"routing": map[string]any{"strategy": "round-robin"}}}
	store := &memoryStore{record: managedRecord()}
	store.record.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
		Strategy: bootstrap.ConnectExternal, Stage: "committed", TargetIdentity: "external:sha256:" + strings.Repeat("b", 64), TargetGeneration: 1,
	}
	store.record.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: "https://committed.test", ManagementKey: "cpamp_protected_envelope"}
	service := New(store, nil, nil)
	service.wire = wire
	service.transport = nil
	service.EnableExternal(func(context.Context, uint64) (string, string, error) {
		return "", "", cpa.ErrExternalTargetMoved
	})
	service.targets = externalTargets()
	service.externalWire = func(string) configWire { return wire }
	view, err := service.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.Capability.Readable || view.Capability.Writable || view.Capability.Reason != ReasonTargetMoved {
		t.Fatalf("moved target treated as another mode: %+v", view.Capability)
	}
	if wire.reads != 0 {
		t.Fatal("a moved target was contacted")
	}
}

// TestExternalWritesAreRefusedForAnUnsupportedVersion proves the CPA version
// bounds the save and nothing else. An External CPA that does not prove the v8
// contract is refused before the journal and before the first write, while the
// page keeps reading it: the user can still see what their own CPA serves.
func TestExternalWritesAreRefusedForAnUnsupportedVersion(t *testing.T) {
	for _, version := range []string{"v7.2.130", "", "latest"} {
		t.Run("version "+version, func(t *testing.T) {
			wire := &fakeWire{document: map[string]any{"routing": map[string]any{"strategy": "round-robin"}}}
			store := &memoryStore{record: managedRecord()}
			store.record.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
				Strategy: bootstrap.ConnectExternal, Stage: "committed", TargetIdentity: "external:sha256:" + strings.Repeat("b", 64), TargetGeneration: 1,
			}
			store.record.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: "https://external.test", ManagementKey: "cpamp_protected_envelope"}
			service := New(store, nil, nil)
			service.wire = wire
			service.transport = nil
			service.EnableExternal(func(context.Context, uint64) (string, string, error) {
				return "https://external.test", "management-test-only", nil
			})
			service.targets = externalTargets()
			service.externalWire = func(string) configWire { return wire }

			// The page still reads the CPA.
			view, err := service.Read(context.Background())
			if err != nil {
				t.Fatalf("read = %v", err)
			}
			if !view.Capability.Readable || !view.Capability.Writable || view.Capability.Mode != ModeExternal {
				t.Fatalf("unsupported CPA is not readable: %+v", view.Capability)
			}
			before := baselineOf(t, view, "routing")

			service.externalProbe = func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
				return cpa.ClassifyExternalVersion(version), nil
			}
			request := GroupRequest{
				RequestID: strings.Repeat("ab", 16),
				Expected:  Expected{Generation: "1", Baseline: before},
				Edits:     []Edit{{Path: "routing.strategy", Action: "set", Value: json.RawMessage(`"fill-first"`)}},
			}
			if _, err = service.SaveGroup(context.Background(), "routing", request); !errors.Is(err, cpa.ErrExternalVersionUnsupported) {
				t.Fatalf("group save on version %q = %v", version, err)
			}
			if _, err = service.SaveFamily(context.Background(), "filter", FamilyRequest{
				RequestID: strings.Repeat("cd", 16),
				Expected:  Expected{Generation: "1", Baseline: familyBaselineFor(t, service, "filter")},
				Rules:     json.RawMessage(`[{"models":[{"name":"*"}],"params":["a"]}]`),
			}); !errors.Is(err, cpa.ErrExternalVersionUnsupported) {
				t.Fatalf("family save on version %q = %v", version, err)
			}
			if len(wire.puts) != 0 || len(wire.patches) != 0 || len(wire.deletes) != 0 {
				t.Fatalf("a refused save reached the CPA: %d puts, %d patches, %d deletes", len(wire.puts), len(wire.patches), len(wire.deletes))
			}
			// No intent was journaled, so nothing is left pending on the connection.
			if len(store.record.ConfigGroups) != 0 {
				t.Fatalf("a refused save journaled %d groups", len(store.record.ConfigGroups))
			}
		})
	}
}

// baselineOf is the rendered baseline of one group in an already-read view.
func baselineOf(t *testing.T, view View, groupID string) string {
	t.Helper()
	for _, group := range view.Groups {
		if group.ID == groupID {
			return group.Baseline
		}
	}
	t.Fatalf("group %q is not projected", groupID)
	return ""
}
