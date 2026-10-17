package bootstrap_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

const fixtureDir = "../../../../../tests/fixtures/phase4-bootstrap"

type candidateJSON struct {
	Release      string
	SourceCommit string
}

func (c candidateJSON) domain() bootstrap.PlannedCandidate {
	return bootstrap.PlannedCandidate{Release: c.Release, SourceCommit: c.SourceCommit}
}

type admissionJSON struct {
	Shape                 bootstrap.SourceShape
	PlannedCandidate      candidateJSON
	ArtifactEvidenceRef   bootstrap.EvidenceRef
	SourceProvenanceRef   bootstrap.EvidenceRef
	ForwardHopEvidenceRef bootstrap.EvidenceRef
	EvidenceStatus        bootstrap.SourceEvidenceStatus
	Readiness             bootstrap.BridgeReadiness
}

func (a admissionJSON) domain() bootstrap.UpgradeAdmission {
	return bootstrap.UpgradeAdmission{
		Shape:                 a.Shape,
		PlannedCandidate:      a.PlannedCandidate.domain(),
		ArtifactEvidenceRef:   a.ArtifactEvidenceRef,
		SourceProvenanceRef:   a.SourceProvenanceRef,
		ForwardHopEvidenceRef: a.ForwardHopEvidenceRef,
		EvidenceStatus:        a.EvidenceStatus,
		Readiness:             a.Readiness,
	}
}

type adoptionJSON struct {
	ResourceRef       bootstrap.ResourceRef
	Kind              bootstrap.AdoptionResourceKind
	Scope             bootstrap.AdoptionScope
	ReadEvidenceRef   bootstrap.CapabilityEvidenceRef
	ReplayEvidenceRef bootstrap.CapabilityEvidenceRef
	VerifyEvidenceRef bootstrap.CapabilityEvidenceRef
	Qualification     bootstrap.CapabilityQualification
	ManualAction      bootstrap.AdoptionManualAction
	Disposition       bootstrap.AdoptionDisposition
}

func (a adoptionJSON) domain() bootstrap.AdoptionResourceDecision {
	return bootstrap.AdoptionResourceDecision{
		ResourceRef:       a.ResourceRef,
		Kind:              a.Kind,
		Scope:             a.Scope,
		ReadEvidenceRef:   a.ReadEvidenceRef,
		ReplayEvidenceRef: a.ReplayEvidenceRef,
		VerifyEvidenceRef: a.VerifyEvidenceRef,
		Qualification:     a.Qualification,
		ManualAction:      a.ManualAction,
		Disposition:       a.Disposition,
	}
}

type pathJSON struct {
	ID                              string
	Mode                            bootstrap.Mode
	CPAMPAction                     bootstrap.CPAMPAction
	CPAStrategy                     bootstrap.CPAStrategy
	AdoptSourceClass                bootstrap.AdoptSourceClass
	CPAQualification                bootstrap.CPAQualification
	SourceAdmission                 *admissionJSON
	SourceCPA                       bootstrap.SourceCPA
	HistoricalEvidence              *bootstrap.HistoricalEvidence
	UsageAction                     bootstrap.UsageAction
	BackupRequired                  bool
	RuntimeMode                     bootstrap.RuntimeMode
	InitializeAdminCredential       bool
	PreserveExistingAdminCredential bool
	AuthenticationRequired          bool
	Mutations                       []string
	Preserved                       []string
	Unsupported                     []string
	DiscoveredResourceRefs          []bootstrap.ResourceRef
	AdoptionDecisions               []adoptionJSON
	ControlBasePath                 string
	EstimatedSteps                  []string
	MigrationOrdering               []bootstrap.MigrationStage
}

func (p pathJSON) domain() bootstrap.Plan {
	var admission *bootstrap.UpgradeAdmission
	if p.SourceAdmission != nil {
		value := p.SourceAdmission.domain()
		admission = &value
	}
	decisions := make([]bootstrap.AdoptionResourceDecision, len(p.AdoptionDecisions))
	for i := range p.AdoptionDecisions {
		decisions[i] = p.AdoptionDecisions[i].domain()
	}
	return bootstrap.Plan{
		Mode:                            p.Mode,
		CPAMPAction:                     p.CPAMPAction,
		CPAStrategy:                     p.CPAStrategy,
		AdoptSourceClass:                p.AdoptSourceClass,
		CPAQualification:                p.CPAQualification,
		SourceAdmission:                 admission,
		SourceCPA:                       p.SourceCPA,
		HistoricalEvidence:              p.HistoricalEvidence,
		UsageAction:                     p.UsageAction,
		BackupRequired:                  p.BackupRequired,
		RuntimeMode:                     p.RuntimeMode,
		InitializeAdminCredential:       p.InitializeAdminCredential,
		PreserveExistingAdminCredential: p.PreserveExistingAdminCredential,
		AuthenticationRequired:          p.AuthenticationRequired,
		Mutations:                       p.Mutations,
		Preserved:                       p.Preserved,
		Unsupported:                     p.Unsupported,
		DiscoveredResourceRefs:          p.DiscoveredResourceRefs,
		AdoptionDecisions:               decisions,
		ControlBasePath:                 p.ControlBasePath,
		EstimatedSteps:                  p.EstimatedSteps,
		MigrationOrdering:               p.MigrationOrdering,
	}
}

type classificationJSON struct {
	ID                  string
	HasV1ProductState   bool
	Admission           *admissionJSON
	RecoveryReason      bootstrap.RecoveryReason
	BootstrapComplete   bool
	AdminCredential     bootstrap.AdminCredentialState
	Session             bootstrap.SessionState
	RequiredCheckpoints bootstrap.RequiredMigrationCheckpoints
	HistoricalEvidence *bootstrap.HistoricalEvidence
	ExpectedMode        bootstrap.Mode
}

type coreJSON struct {
	ManagementGeneration    string
	CoreV0FallbackAllowed   bool
	ExternalManagementOwner string
	ExternalLifecycleOwner  string
}

type ownershipJSON struct {
	DatabaseOwner            bootstrap.ComponentOwner
	DataKeyOwner             bootstrap.ComponentOwner
	MigrationOwner           bootstrap.ComponentOwner
	ProductCheckpointOwner   bootstrap.ComponentOwner
	ManagedCPALifecycleOwner bootstrap.ComponentOwner
}

func (o ownershipJSON) domain() bootstrap.OwnershipContract {
	return bootstrap.OwnershipContract{
		DatabaseOwner:            o.DatabaseOwner,
		DataKeyOwner:             o.DataKeyOwner,
		MigrationOwner:           o.MigrationOwner,
		ProductCheckpointOwner:   o.ProductCheckpointOwner,
		ManagedCPALifecycleOwner: o.ManagedCPALifecycleOwner,
	}
}

func (c coreJSON) domain() bootstrap.CPACoreContract {
	return bootstrap.CPACoreContract{
		ManagementGeneration:    c.ManagementGeneration,
		CoreV0FallbackAllowed:   c.CoreV0FallbackAllowed,
		ExternalManagementOwner: c.ExternalManagementOwner,
		ExternalLifecycleOwner:  c.ExternalLifecycleOwner,
	}
}

type operationConflictJSON struct {
	ID               string
	Active           bootstrap.OperationClass
	Requested        bootstrap.OperationClass
	Scope            bootstrap.ConflictScope
	SameOperationID  bool
	ExpectedDecision bootstrap.OperationConflictDecision
}

type collectorAssertionJSON struct {
	ID              string
	Condition       bootstrap.CollectorCondition
	ExpectedOutcome bootstrap.CollectorOutcome
}

type prefixObservationJSON struct {
	CurrentInstallationID          string
	CurrentRoutingRevision         uint64
	CurrentEffectivePath           string
	CurrentOperationOwnerQualified bool
	CurrentCollisionFree           bool
	CurrentSourceQualified         bool
	PreviousSnapshotQualified      bool
	PreviousSnapshotEffectivePath  string
	PublicIdentityHealthy          bool
	StaticAssetsHealthy            bool
	APIAuthenticationHealthy       bool
}

func (o prefixObservationJSON) domain() bootstrap.PrefixRecoveryObservation {
	return bootstrap.PrefixRecoveryObservation{
		CurrentInstallationID:          o.CurrentInstallationID,
		CurrentRoutingRevision:         o.CurrentRoutingRevision,
		CurrentEffectivePath:           o.CurrentEffectivePath,
		CurrentOperationOwnerQualified: o.CurrentOperationOwnerQualified,
		CurrentCollisionFree:           o.CurrentCollisionFree,
		CurrentSourceQualified:         o.CurrentSourceQualified,
		PreviousSnapshotQualified:      o.PreviousSnapshotQualified,
		PreviousSnapshotEffectivePath:  o.PreviousSnapshotEffectivePath,
		PublicIdentityHealthy:          o.PublicIdentityHealthy,
		StaticAssetsHealthy:            o.StaticAssetsHealthy,
		APIAuthenticationHealthy:       o.APIAuthenticationHealthy,
	}
}

type prefixAssertionJSON struct {
	ID                             string
	OperationID                    bootstrap.OperationID
	InstallationID                 string
	CurrentInstallationID          string
	ExpectedRoutingRevision        uint64
	CurrentRoutingRevision         uint64
	PreviousVerifiedEffectivePath  string
	DesiredControlBasePath         string
	VerifiedEffectivePath          string
	Impact                         bootstrap.PrefixImpact
	Stage                          bootstrap.PrefixOperationStage
	AuthorityQualified             bool
	CollisionFree                  bool
	PublicIdentityHealthy          bool
	StaticAssetsHealthy            bool
	APIAuthenticationHealthy       bool
	Result                         bootstrap.PrefixOperationResult
	ExpectedAction                 bootstrap.PrefixRecoveryAction
	Observation                    *prefixObservationJSON
	ExpectedCompleteAfterObservation bool
	ExpectedConditionalRestore       bool
}

func (p prefixAssertionJSON) domain() bootstrap.PrefixOperation {
	return bootstrap.PrefixOperation{
		OperationID:                   p.OperationID,
		InstallationID:                p.InstallationID,
		CurrentInstallationID:         p.CurrentInstallationID,
		ExpectedRoutingRevision:       p.ExpectedRoutingRevision,
		CurrentRoutingRevision:        p.CurrentRoutingRevision,
		PreviousVerifiedEffectivePath: p.PreviousVerifiedEffectivePath,
		DesiredControlBasePath:        p.DesiredControlBasePath,
		VerifiedEffectivePath:         p.VerifiedEffectivePath,
		Impact:                        p.Impact,
		Stage:                         p.Stage,
		AuthorityQualified:            p.AuthorityQualified,
		CollisionFree:                 p.CollisionFree,
		PublicIdentityHealthy:         p.PublicIdentityHealthy,
		StaticAssetsHealthy:           p.StaticAssetsHealthy,
		APIAuthenticationHealthy:      p.APIAuthenticationHealthy,
		Result:                        p.Result,
	}
}

type fixture struct {
	Schema                      string `json:"$schema"`
	SchemaVersion               int
	ContractID               string
	ExecutionBaseline        string
	PlannedCandidate         candidateJSON
	CPACore                  coreJSON
	Ownership                ownershipJSON
	UpgradeOrdering          []bootstrap.MigrationStage
	BootstrapModes           []bootstrap.Mode
	CPAStrategies            []bootstrap.CPAStrategy
	Paths                    []pathJSON
	ClassificationAssertions    []classificationJSON
	OperationConflictAssertions []operationConflictJSON
	CollectorAssertions         []collectorAssertionJSON
	PrefixAssertions            []prefixAssertionJSON
	GlobalAssertions            []string
}

func rejectDuplicateJSONKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := kt.(string)
				if !ok {
					return errors.New("object key is not string")
				}
				if seen[key] {
					return fmt.Errorf("duplicate JSON key %q", key)
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		case '[':
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		default:
			return fmt.Errorf("unexpected delimiter %q", delim)
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}

func resolveSchema(schema, root map[string]any) (map[string]any, error) {
	ref, ok := schema["$ref"].(string)
	if !ok {
		return schema, nil
	}
	const prefix = "#/$defs/"
	if !strings.HasPrefix(ref, prefix) {
		return nil, fmt.Errorf("unsupported schema ref %q", ref)
	}
	defs, ok := root["$defs"].(map[string]any)
	if !ok {
		return nil, errors.New("schema defs missing")
	}
	resolved, ok := defs[strings.TrimPrefix(ref, prefix)].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema ref missing %q", ref)
	}
	return resolved, nil
}

func validateSchemaValue(value any, schema, root map[string]any) error {
	var err error
	schema, err = resolveSchema(schema, root)
	if err != nil {
		return err
	}
	if variants, ok := schema["oneOf"].([]any); ok {
		var matched int
		for _, variant := range variants {
			if m, ok := variant.(map[string]any); ok && validateSchemaValue(value, m, root) == nil {
				matched++
			}
		}
		if matched != 1 {
			return fmt.Errorf("oneOf matched %d variants", matched)
		}
		return nil
	}
	if c, ok := schema["const"]; ok && !reflect.DeepEqual(value, c) {
		return fmt.Errorf("const mismatch: got=%v want=%v", value, c)
	}
	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, item := range enum {
			if reflect.DeepEqual(value, item) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("enum mismatch: %v", value)
		}
	}
	typeName, _ := schema["type"].(string)
	switch typeName {
	case "":
		// schema may be defined only by const/enum/ref/oneOf
	case "null":
		if value != nil {
			return fmt.Errorf("want null, got %T", value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("want boolean, got %T", value)
		}
	case "number":
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("want number, got %T", value)
		}
	case "string":
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("want string, got %T", value)
		}
		if n, ok := schema["minLength"].(float64); ok && len(s) < int(n) {
			return errors.New("string below minLength")
		}
		if pattern, ok := schema["pattern"].(string); ok && !regexp.MustCompile(pattern).MatchString(s) {
			return fmt.Errorf("string does not match %q", pattern)
		}
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("want object, got %T", value)
		}
		props, _ := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, item := range required {
				key, _ := item.(string)
				if _, exists := obj[key]; !exists {
					return fmt.Errorf("missing required key %q", key)
				}
			}
		}
		if closed, ok := schema["additionalProperties"].(bool); ok && !closed {
			for key := range obj {
				if _, exists := props[key]; !exists {
					return fmt.Errorf("unknown key %q", key)
				}
			}
		}
		for key, child := range obj {
			if childSchema, ok := props[key].(map[string]any); ok {
				if err := validateSchemaValue(child, childSchema, root); err != nil {
					return fmt.Errorf("%s: %w", key, err)
				}
			}
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("want array, got %T", value)
		}
		if n, ok := schema["minItems"].(float64); ok && len(items) < int(n) {
			return errors.New("array below minItems")
		}
		if n, ok := schema["maxItems"].(float64); ok && len(items) > int(n) {
			return errors.New("array above maxItems")
		}
		if unique, ok := schema["uniqueItems"].(bool); ok && unique {
			for i := range items {
				for j := 0; j < i; j++ {
					if reflect.DeepEqual(items[i], items[j]) {
						return errors.New("array contains duplicate item")
					}
				}
			}
		}
		if prefix, ok := schema["prefixItems"].([]any); ok {
			for i, entry := range prefix {
				if i >= len(items) {
					break
				}
				if m, ok := entry.(map[string]any); ok {
					if err := validateSchemaValue(items[i], m, root); err != nil {
						return fmt.Errorf("prefix item %d: %w", i, err)
					}
				}
			}
		}
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for i, item := range items {
				if err := validateSchemaValue(item, itemSchema, root); err != nil {
					return fmt.Errorf("item %d: %w", i, err)
				}
			}
		}
	default:
		return fmt.Errorf("unsupported schema type %q", typeName)
	}
	return nil
}

func strictDecodeFixture(data, schemaData []byte) (fixture, error) {
	var result fixture
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return result, err
	}
	if err := rejectDuplicateJSONKeys(schemaData); err != nil {
		return result, fmt.Errorf("schema: %w", err)
	}
	var rootSchema map[string]any
	var raw any
	if err := json.Unmarshal(schemaData, &rootSchema); err != nil {
		return result, err
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return result, err
	}
	if err := validateSchemaValue(raw, rootSchema, rootSchema); err != nil {
		return result, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&result); err != nil {
		return result, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return result, errors.New("trailing fixture JSON")
	}
	return result, nil
}

func readFixture(t *testing.T) fixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, "contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile(filepath.Join(fixtureDir, "contract.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := strictDecodeFixture(data, schema)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func setEqual[T ~string](got []T, want []T) bool {
	if len(got) != len(want) {
		return false
	}
	a := make([]string, len(got))
	b := make([]string, len(want))
	for i := range got {
		a[i] = string(got[i])
	}
	for i := range want {
		b[i] = string(want[i])
	}
	sort.Strings(a)
	sort.Strings(b)
	return reflect.DeepEqual(a, b)
}

func TestCheckedInContract(t *testing.T) {
	f := readFixture(t)
	if f.Schema != "./contract.schema.json" || f.SchemaVersion != 2 ||
		f.ContractID != "cpamp-v2-phase4-00-bootstrap-contract-v2" ||
		f.ExecutionBaseline != "4d2e2ec72b6b65d84fd1d24ed0022ea65a8dcea0" {
		t.Fatal("contract identity drifted")
	}
	if f.PlannedCandidate.domain() != bootstrap.ExactV1144Candidate() {
		t.Fatal("planned direct candidate drifted")
	}
	if err := f.CPACore.domain().Validate(); err != nil {
		t.Fatal(err)
	}
	if err := f.Ownership.domain().Validate(); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.ValidateUpgradeOrdering(f.UpgradeOrdering); err != nil {
		t.Fatal(err)
	}
	if !setEqual(f.BootstrapModes, []bootstrap.Mode{
		bootstrap.Fresh, bootstrap.Upgrade, bootstrap.UnsupportedUpgrade, bootstrap.Recovery, bootstrap.Completed,
	}) {
		t.Fatal("bootstrap modes drifted")
	}
	if !setEqual(f.CPAStrategies, []bootstrap.CPAStrategy{
		bootstrap.NewManaged, bootstrap.AdoptExisting, bootstrap.ConnectExternal, bootstrap.MigrateLegacy,
	}) {
		t.Fatal("CPA strategies drifted")
	}
	if len(f.Paths) != 5 {
		t.Fatalf("got %d paths, want 5", len(f.Paths))
	}
	type pathSignature struct {
		Mode       bootstrap.Mode
		Strategy   bootstrap.CPAStrategy
		Action     bootstrap.CPAMPAction
		Usage      bootstrap.UsageAction
		Backup     bool
		Runtime    bootstrap.RuntimeMode
	}
	wantPaths := map[string]pathSignature{
		"P4-BOOT-01": {bootstrap.Fresh, bootstrap.NewManaged, bootstrap.Initialize, bootstrap.NoUsageAction, false, bootstrap.Embedded},
		"P4-BOOT-02": {bootstrap.Fresh, bootstrap.AdoptExisting, bootstrap.Initialize, bootstrap.NoUsageAction, false, bootstrap.Embedded},
		"P4-BOOT-03": {bootstrap.Fresh, bootstrap.ConnectExternal, bootstrap.Initialize, bootstrap.NoUsageAction, false, bootstrap.External},
		"P4-BOOT-04": {bootstrap.Upgrade, bootstrap.MigrateLegacy, bootstrap.MigrateV1, bootstrap.PreserveUsage, true, bootstrap.Embedded},
		"P4-BOOT-05": {bootstrap.Upgrade, bootstrap.ConnectExternal, bootstrap.MigrateV1, bootstrap.PreserveUsage, true, bootstrap.External},
	}
	seenPaths := map[string]bool{}
	for _, path := range f.Paths {
		if seenPaths[path.ID] {
			t.Fatalf("duplicate path %s", path.ID)
		}
		want, ok := wantPaths[path.ID]
		if !ok {
			t.Fatalf("unexpected path %s", path.ID)
		}
		got := pathSignature{
			Mode: path.Mode, Strategy: path.CPAStrategy, Action: path.CPAMPAction,
			Usage: path.UsageAction, Backup: path.BackupRequired, Runtime: path.RuntimeMode,
		}
		if got != want {
			t.Fatalf("%s canonical path drifted: got=%+v want=%+v", path.ID, got, want)
		}
		seenPaths[path.ID] = true
		if err := path.domain().Validate(); err != nil {
			t.Fatalf("%s: %v", path.ID, err)
		}
	}
	for id := range wantPaths {
		if !seenPaths[id] {
			t.Fatalf("missing path %s", id)
		}
	}
	wantClassificationIDs := map[string]bool{
		"no_v1_is_fresh": true,
		"schema_c_without_authority_is_unsupported": true,
		"verified_exact_ready_is_upgrade": true,
		"verified_hop_ready_is_upgrade": true,
		"verified_but_blocked_is_recovery": true,
		"offline_required_is_recovery": true,
		"unknown_newer_same_shape_is_unsupported": true,
		"damaged_state_overrides": true,
		"completed_is_terminal": true,
		"checkpoint_incomplete_is_recovery": true,
		"historical_authority_incomplete_is_recovery": true,
		"administrator_unverifiable_is_recovery": true,
		"completed_with_uninitialized_admin_is_recovery": true,
	}
	if len(f.ClassificationAssertions) != len(wantClassificationIDs) {
		t.Fatalf("got %d classification cases, want %d", len(f.ClassificationAssertions), len(wantClassificationIDs))
	}
	seenClassificationIDs := map[string]bool{}
	for _, assertion := range f.ClassificationAssertions {
		if !wantClassificationIDs[assertion.ID] || seenClassificationIDs[assertion.ID] {
			t.Fatalf("unexpected or duplicate classification case %q", assertion.ID)
		}
		seenClassificationIDs[assertion.ID] = true
		var admission *bootstrap.UpgradeAdmission
		if assertion.Admission != nil {
			value := assertion.Admission.domain()
			admission = &value
		}
		got, err := bootstrap.Classify(bootstrap.ClassificationEvidence{
			HasV1ProductState: assertion.HasV1ProductState,
			Admission:         admission,
			RecoveryReason:    assertion.RecoveryReason,
			BootstrapComplete: assertion.BootstrapComplete,
			AdminCredential:     assertion.AdminCredential,
			Session:             assertion.Session,
			RequiredCheckpoints: assertion.RequiredCheckpoints,
			HistoricalEvidence: assertion.HistoricalEvidence,
		})
		if err != nil || got != assertion.ExpectedMode {
			t.Fatalf("%s: got %q err=%v want %q", assertion.ID, got, err, assertion.ExpectedMode)
		}
	}
	if len(f.OperationConflictAssertions) == 0 || len(f.CollectorAssertions) == 0 {
		t.Fatal("operation conflict and Collector assertions must be explicit")
	}
	wantConflictIDs := map[string]bool{
		"install_vs_restore": true,
		"migration_vs_maintenance": true,
		"cpa_update_vs_cutover": true,
		"cutover_vs_prefix_apply": true,
		"same_migration_operation_reobserve": true,
	}
	if len(f.OperationConflictAssertions) != len(wantConflictIDs) {
		t.Fatalf("got %d operation conflict cases, want %d", len(f.OperationConflictAssertions), len(wantConflictIDs))
	}
	seenConflictIDs := map[string]bool{}
	for _, assertion := range f.OperationConflictAssertions {
		if !wantConflictIDs[assertion.ID] || seenConflictIDs[assertion.ID] {
			t.Fatalf("unexpected or duplicate operation conflict case %q", assertion.ID)
		}
		seenConflictIDs[assertion.ID] = true
		scope, err := bootstrap.ClassifyOperationConflictScope(assertion.Active, assertion.Requested)
		if err != nil || scope != assertion.Scope {
			t.Fatalf("%s: scope got=%q err=%v want=%q", assertion.ID, scope, err, assertion.Scope)
		}
		got, err := bootstrap.EvaluateOperationConflict(assertion.Active, assertion.Requested, assertion.SameOperationID)
		if err != nil || got != assertion.ExpectedDecision {
			t.Fatalf("%s: conflict got=%q err=%v want=%q", assertion.ID, got, err, assertion.ExpectedDecision)
		}
	}
	wantCollectorIDs := map[string]bool{
		"backup_quiescence_pauses_collector": true,
		"verified_retention_is_queue_window": true,
		"unproven_or_outside_window_is_gap_unknown": true,
	}
	if len(f.CollectorAssertions) != len(wantCollectorIDs) {
		t.Fatalf("got %d Collector cases, want %d", len(f.CollectorAssertions), len(wantCollectorIDs))
	}
	seenCollectorIDs := map[string]bool{}
	for _, assertion := range f.CollectorAssertions {
		if !wantCollectorIDs[assertion.ID] || seenCollectorIDs[assertion.ID] {
			t.Fatalf("unexpected or duplicate Collector case %q", assertion.ID)
		}
		seenCollectorIDs[assertion.ID] = true
		got, err := bootstrap.CollectorOutcomeFor(assertion.Condition)
		if err != nil || got != assertion.ExpectedOutcome {
			t.Fatalf("%s: Collector got=%q err=%v want=%q", assertion.ID, got, err, assertion.ExpectedOutcome)
		}
	}
	wantPrefixIDs := map[string]bool{
		"lost_result_healthy_target_can_complete": true,
		"lost_result_unhealthy_target_can_restore": true,
		"stale_revision_refuses_prefix_recovery": true,
		"authority_reobservation_refuses_recovery": true,
		"restore_snapshot_unqualified_refuses_recovery": true,
		"committed_applied_prefix_is_complete": true,
		"effective_mismatch_requires_resolution": true,
	}
	if len(f.PrefixAssertions) != len(wantPrefixIDs) {
		t.Fatalf("got %d prefix cases, want %d", len(f.PrefixAssertions), len(wantPrefixIDs))
	}
	seenPrefixIDs := map[string]bool{}
	for _, assertion := range f.PrefixAssertions {
		if !wantPrefixIDs[assertion.ID] || seenPrefixIDs[assertion.ID] {
			t.Fatalf("unexpected or duplicate prefix case %q", assertion.ID)
		}
		seenPrefixIDs[assertion.ID] = true
		op := assertion.domain()
		action, err := bootstrap.PrefixActionAfterOperation(op)
		if err != nil || action != assertion.ExpectedAction {
			t.Fatalf("%s: prefix action got=%q err=%v want=%q", assertion.ID, action, err, assertion.ExpectedAction)
		}
		if assertion.Observation != nil {
			obs := assertion.Observation.domain()
			if got := bootstrap.CanCompletePrefixAfterObservation(op, obs); got != assertion.ExpectedCompleteAfterObservation {
				t.Fatalf("%s: complete-after-observation got=%v want=%v", assertion.ID, got, assertion.ExpectedCompleteAfterObservation)
			}
			if got := bootstrap.CanConditionallyRestorePrefix(op, obs); got != assertion.ExpectedConditionalRestore {
				t.Fatalf("%s: conditional-restore got=%v want=%v", assertion.ID, got, assertion.ExpectedConditionalRestore)
			}
		}
	}
	wantGlobal := []string{
		"shape_not_release_identity",
		"planned_candidate_not_artifact_authority",
		"artifact_not_source_or_hop_authority",
		"source_or_hop_evidence_not_readiness",
		"readiness_not_writable_grant",
		"read_only_classification_before_backup",
		"complete_verified_backup_before_write",
		"source_revalidation_before_write",
		"bootstrap_mode_admin_session_orthogonal",
		"fresh_mode_not_admin_initialization_authority",
		"upgrade_preserves_existing_admin_credential",
		"authoritative_history_includes_archive_retained_evidence",
		"cpa_core_v8_only_no_v0_fallback",
		"external_management_not_lifecycle_authority",
		"contract_fixture_never_grants_production_authority",
		"deferred_qualification_remains_pending",
		"required_migration_checkpoints_before_grant",
		"forward_hop_requires_trusted_receipt",
		"trusted_receipt_requires_manager_authenticity",
		"trusted_receipt_binds_operation_target_input_output_archive_executable",
		"trusted_receipt_requires_zero_exit_released_handles_and_reap",
		"actual_operation_record_persisted_with_stage_identity",
		"receipt_replay_or_revocation_rejected",
		"manager_supervisor_ownership_separated",
		"admin_unverifiable_routes_recovery",
		"checkpoints_incomplete_route_recovery",
		"historical_source_state_explicit_fail_closed",
		"cpa_strategy_requires_v8_qualification",
		"adoption_references_not_authority",
		"unauthenticated_discovery_is_minimal",
		"fresh_claim_bound_consume_once",
		"fresh_claim_expire_revoke_restart_csrf_fail_closed",
		"gateway_activation_separate_from_product_initialized",
		"journal_unknown_result_inspect_first_same_id_replay",
		"prefix_cas_desired_effective_lost_result_inspect_first",
		"adopt_network_source_requires_v8_profile",
		"adopt_legacy_managed_requires_staging_v8",
		"journal_retry_requires_accepted_replay",
		"cpa_journal_generation_change_reobserve",
		"operation_conflicts_are_explicit",
		"collector_pause_queue_gap_explicit",
		"completed_requires_initialized_admin",
		"journal_domain_specific_authority_fence",
		"fresh_claim_revision_cas_single_winner",
		"operation_conflict_scoped_not_global",
		"fixture_required_case_ids_locked",
		"control_base_path_rejects_encoded_backslash_bypass",
		"conflict_scope_server_classified",
		"writable_grant_requires_historical_authority",
		"prefix_conditional_recovery_fail_closed",
		"classification_fixture_ids_locked",
		"prefix_recovery_reobserves_current_authority_snapshot",
		"plan_effect_sets_are_disjoint",
	}
	if !setEqual(f.GlobalAssertions, wantGlobal) {
		t.Fatal("global contract assertions drifted")
	}
}

func exactAdmission() bootstrap.UpgradeAdmission {
	return bootstrap.UpgradeAdmission{
		Shape:               bootstrap.LegacySchemaC,
		PlannedCandidate:    bootstrap.ExactV1144Candidate(),
		ArtifactEvidenceRef: "evidence:artifact",
		SourceProvenanceRef: "evidence:source",
		EvidenceStatus:      bootstrap.ExactCandidateVerified,
		Readiness:           bootstrap.BridgeReady,
	}
}

func baseClassification(admission *bootstrap.UpgradeAdmission) bootstrap.ClassificationEvidence {
	history := bootstrap.HistoricalEvidence{
		SourceState:     bootstrap.HistoryFullRaw,
		AuthorityStatus: bootstrap.HistoryAvailable,
	}
	return bootstrap.ClassificationEvidence{
		HasV1ProductState: true,
		Admission:         admission,
		AdminCredential:   bootstrap.AdminCredentialInitialized,
		Session:           bootstrap.SessionMissing,
		RequiredCheckpoints: bootstrap.RequiredMigrationCheckpoints{
			CacheAccounting: bootstrap.CheckpointComplete,
			QuotaSnapshot: bootstrap.CheckpointComplete,
		},
		HistoricalEvidence: &history,
	}
}

func TestShapeNeverProvesExactRelease(t *testing.T) {
	admission := exactAdmission()
	admission.ArtifactEvidenceRef = ""
	admission.SourceProvenanceRef = ""
	admission.EvidenceStatus = bootstrap.SourceUnverified
	got, err := bootstrap.Classify(baseClassification(&admission))
	if err != nil {
		t.Fatal(err)
	}
	if got != bootstrap.UnsupportedUpgrade {
		t.Fatalf("LegacySchemaC without trusted source evidence classified %q", got)
	}
	admission.EvidenceStatus = bootstrap.UnknownOrNewerSource
	admission.Readiness = bootstrap.BridgeReady
	got, err = bootstrap.Classify(baseClassification(&admission))
	if err != nil {
		t.Fatal(err)
	}
	if got != bootstrap.UnsupportedUpgrade {
		t.Fatalf("unknown/newer SchemaC classified %q", got)
	}
}

func TestVerifiedCandidateRequiresCurrentSchemaC(t *testing.T) {
	admission := exactAdmission()
	admission.Shape = bootstrap.LegacySchemaB
	if err := admission.Validate(); err == nil {
		t.Fatal("verified exact candidate accepted with non-SchemaC current shape")
	}

	admission = exactAdmission()
	admission.EvidenceStatus = bootstrap.ForwardHopVerified
	admission.ForwardHopEvidenceRef = "evidence:hop"
	admission.Shape = bootstrap.LegacySchemaA
	if err := admission.Validate(); err == nil {
		t.Fatal("verified forward-hop output accepted with non-SchemaC current shape")
	}
}

func TestEvidenceReadinessAndGrantRemainIndependent(t *testing.T) {
	admission := exactAdmission()
	admission.Readiness = bootstrap.BridgeBlocked
	got, err := bootstrap.Classify(baseClassification(&admission))
	if err != nil {
		t.Fatal(err)
	}
	if got != bootstrap.Recovery {
		t.Fatalf("verified but blocked source classified %q", got)
	}

	admission = exactAdmission()
	ordering := []bootstrap.MigrationStage{
		bootstrap.ReadOnlyClassification,
		bootstrap.CompleteVerifiedBackup,
		bootstrap.SourceRevalidation,
		bootstrap.WritableMigration,
	}
	history := bootstrap.HistoricalEvidence{
		SourceState: bootstrap.HistoryFullRaw,
		AuthorityStatus: bootstrap.HistoryAvailable,
	}
	base := bootstrap.WritablePreconditions{
		Mode:              bootstrap.Upgrade,
		Admission:         admission,
		BackupVerified:    true,
		SourceRevalidated: true,
		RequiredCheckpoints: bootstrap.RequiredMigrationCheckpoints{
			CacheAccounting: bootstrap.CheckpointComplete,
			QuotaSnapshot: bootstrap.CheckpointComplete,
		},
		HistoricalEvidence:        &history,
		ManagerAuthorityQualified: true,
		Ordering:                  ordering,
	}
	if !bootstrap.CanIssueWritableGrant(base) {
		t.Fatal("complete future preconditions should satisfy the pure grant predicate")
	}
	tests := []struct {
		name string
		edit func(*bootstrap.WritablePreconditions)
	}{
		{"backup missing", func(p *bootstrap.WritablePreconditions) { p.BackupVerified = false }},
		{"source not revalidated", func(p *bootstrap.WritablePreconditions) { p.SourceRevalidated = false }},
		{"authority unqualified", func(p *bootstrap.WritablePreconditions) { p.ManagerAuthorityQualified = false }},
		{"cache checkpoint incomplete", func(p *bootstrap.WritablePreconditions) { p.RequiredCheckpoints.CacheAccounting = bootstrap.CheckpointPending }},
		{"quota checkpoint incomplete", func(p *bootstrap.WritablePreconditions) { p.RequiredCheckpoints.QuotaSnapshot = bootstrap.CheckpointPending }},
		{"historical authority missing", func(p *bootstrap.WritablePreconditions) { p.HistoricalEvidence = nil }},
		{"historical authority incomplete", func(p *bootstrap.WritablePreconditions) {
			value := bootstrap.HistoricalEvidence{SourceState: bootstrap.HistoryIncomplete, AuthorityStatus: bootstrap.HistoryIndeterminate}
			p.HistoricalEvidence = &value
		}},
		{"readiness blocked", func(p *bootstrap.WritablePreconditions) { p.Admission.Readiness = bootstrap.BridgeBlocked }},
		{"source unverified", func(p *bootstrap.WritablePreconditions) { p.Admission.EvidenceStatus = bootstrap.SourceUnverified }},
		{"ordering broken", func(p *bootstrap.WritablePreconditions) {
			p.Ordering = []bootstrap.MigrationStage{
				bootstrap.ReadOnlyClassification,
				bootstrap.SourceRevalidation,
				bootstrap.CompleteVerifiedBackup,
				bootstrap.WritableMigration,
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			value := base
			value.Ordering = append([]bootstrap.MigrationStage(nil), base.Ordering...)
			tc.edit(&value)
			if bootstrap.CanIssueWritableGrant(value) {
				t.Fatal("unsafe writable grant predicate returned true")
			}
		})
	}
}

func TestForwardHopGrantRequiresTrustedReceiptQualification(t *testing.T) {
	admission := exactAdmission()
	admission.EvidenceStatus = bootstrap.ForwardHopVerified
	admission.ForwardHopEvidenceRef = "evidence:hop"
	history := bootstrap.HistoricalEvidence{
		SourceState: bootstrap.HistoryFullRaw,
		AuthorityStatus: bootstrap.HistoryAvailable,
	}
	base := bootstrap.WritablePreconditions{
		Mode:              bootstrap.Upgrade,
		Admission:         admission,
		BackupVerified:    true,
		SourceRevalidated: true,
		RequiredCheckpoints: bootstrap.RequiredMigrationCheckpoints{
			CacheAccounting: bootstrap.CheckpointComplete,
			QuotaSnapshot: bootstrap.CheckpointComplete,
		},
		HistoricalEvidence:        &history,
		ManagerAuthorityQualified: true,
		Ordering: []bootstrap.MigrationStage{
			bootstrap.ReadOnlyClassification,
			bootstrap.CompleteVerifiedBackup,
			bootstrap.SourceRevalidation,
			bootstrap.WritableMigration,
		},
	}
	if bootstrap.CanIssueWritableGrant(base) {
		t.Fatal("forward-hop admission granted without trusted receipt qualification")
	}
	base.TrustedForwardHopQualified = true
	if !bootstrap.CanIssueWritableGrant(base) {
		t.Fatal("fully qualified forward-hop preconditions were rejected")
	}
}

func TestActualOperationRecordRequiresPersistentIdentityFields(t *testing.T) {
	valid := bootstrap.ActualOperationRecord{
		OperationID:         "operation-0001",
		StartedAtUnixMilli:  1,
		OperatorIdentityRef: "admin-session:fingerprint",
		Stage:               bootstrap.OperationClassified,
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []bootstrap.ActualOperationRecord{
		{StartedAtUnixMilli: 1, OperatorIdentityRef: "admin-session:fingerprint", Stage: bootstrap.OperationClassified},
		{OperationID: "operation-0001", OperatorIdentityRef: "admin-session:fingerprint", Stage: bootstrap.OperationClassified},
		{OperationID: "operation-0001", StartedAtUnixMilli: 1, Stage: bootstrap.OperationClassified},
		{OperationID: "operation-0001", StartedAtUnixMilli: 1, OperatorIdentityRef: "admin-session:fingerprint", Stage: "unknown"},
	}
	for _, tc := range cases {
		if err := tc.Validate(); err == nil {
			t.Fatalf("invalid actual operation record accepted: %+v", tc)
		}
	}
}

func TestTrustedReceiptBindingIsExactAndReplaySafe(t *testing.T) {
	binding := bootstrap.ReceiptBinding{
		OperationID:          "operation-0001",
		TargetFingerprint:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		InputManifestDigest:  "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		OutputManifestDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		ArtifactArchiveDigest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		ExecutableDigest:      "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
	}
	base := bootstrap.TrustedReceiptPreconditions{
		Receipt:                     binding,
		CurrentOperationID:          binding.OperationID,
		CurrentTargetFingerprint:    binding.TargetFingerprint,
		CurrentInputManifestDigest:  binding.InputManifestDigest,
		CurrentOutputManifestDigest:   binding.OutputManifestDigest,
		CurrentArtifactArchiveDigest: binding.ArtifactArchiveDigest,
		CurrentExecutableDigest:      binding.ExecutableDigest,
		ManagerAuthenticityVerified:  true,
		JournalRecordPresent:        true,
		ProcessExited:               true,
		ProcessExitCode:             0,
		ProcessHandlesReleased:      true,
		ProcessReaped:               true,
	}
	if !bootstrap.CanAcceptTrustedHopReceipt(base) {
		t.Fatal("matching trusted receipt preconditions were rejected")
	}
	tests := []struct {
		name string
		edit func(*bootstrap.TrustedReceiptPreconditions)
	}{
		{"forged", func(p *bootstrap.TrustedReceiptPreconditions) { p.ManagerAuthenticityVerified = false }},
		{"journal missing", func(p *bootstrap.TrustedReceiptPreconditions) { p.JournalRecordPresent = false }},
		{"revoked", func(p *bootstrap.TrustedReceiptPreconditions) { p.JournalRevoked = true }},
		{"operation replay", func(p *bootstrap.TrustedReceiptPreconditions) { p.CurrentOperationID = "operation-9999" }},
		{"target mismatch", func(p *bootstrap.TrustedReceiptPreconditions) { p.CurrentTargetFingerprint = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" }},
		{"input mismatch", func(p *bootstrap.TrustedReceiptPreconditions) { p.CurrentInputManifestDigest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" }},
		{"output mismatch", func(p *bootstrap.TrustedReceiptPreconditions) { p.CurrentOutputManifestDigest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" }},
		{"archive mismatch", func(p *bootstrap.TrustedReceiptPreconditions) { p.CurrentArtifactArchiveDigest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" }},
		{"executable mismatch", func(p *bootstrap.TrustedReceiptPreconditions) { p.CurrentExecutableDigest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" }},
		{"process not exited", func(p *bootstrap.TrustedReceiptPreconditions) { p.ProcessExited = false }},
		{"nonzero exit", func(p *bootstrap.TrustedReceiptPreconditions) { p.ProcessExitCode = 1 }},
		{"handles not released", func(p *bootstrap.TrustedReceiptPreconditions) { p.ProcessHandlesReleased = false }},
		{"process not reaped", func(p *bootstrap.TrustedReceiptPreconditions) { p.ProcessReaped = false }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			value := base
			tc.edit(&value)
			if bootstrap.CanAcceptTrustedHopReceipt(value) {
				t.Fatal("invalid or replayed receipt preconditions were accepted")
			}
		})
	}
}

func TestOwnershipContractSeparatesManagerAndSupervisor(t *testing.T) {
	valid := bootstrap.RequiredOwnershipContract()
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	valid.DatabaseOwner = bootstrap.RuntimeSupervisorOwner
	if err := valid.Validate(); err == nil {
		t.Fatal("Supervisor was allowed to own Manager database")
	}
}

func TestBootstrapModeDoesNotDependOnSessionState(t *testing.T) {
	admission := exactAdmission()
	for _, session := range []bootstrap.SessionState{
		bootstrap.SessionMissing, bootstrap.SessionAuthenticated, bootstrap.SessionExpired,
	} {
		input := baseClassification(&admission)
		input.Session = session
		got, err := bootstrap.Classify(input)
		if err != nil || got != bootstrap.Upgrade {
			t.Fatalf("session=%s got=%s err=%v", session, got, err)
		}
	}
}

func TestUnverifiableAdministratorRoutesRecovery(t *testing.T) {
	admission := exactAdmission()
	input := baseClassification(&admission)
	input.AdminCredential = bootstrap.AdminCredentialUnverifiable
	got, err := bootstrap.Classify(input)
	if err != nil || got != bootstrap.Recovery {
		t.Fatalf("got=%s err=%v, want recovery", got, err)
	}
}

func TestCompletedRequiresInitializedAdministrator(t *testing.T) {
	for _, admin := range []bootstrap.AdminCredentialState{
		bootstrap.AdminCredentialUninitialized,
		bootstrap.AdminCredentialUnverifiable,
	} {
		got, err := bootstrap.Classify(bootstrap.ClassificationEvidence{
			BootstrapComplete: true,
			AdminCredential:   admin,
			Session:           bootstrap.SessionMissing,
		})
		if err != nil || got != bootstrap.Recovery {
			t.Fatalf("admin=%s got=%s err=%v, want recovery", admin, got, err)
		}
	}
}

func TestUpgradeOrderingRejectsEarlyWrite(t *testing.T) {
	if err := bootstrap.ValidateUpgradeOrdering([]bootstrap.MigrationStage{
		bootstrap.ReadOnlyClassification,
		bootstrap.CompleteVerifiedBackup,
		bootstrap.SourceRevalidation,
		bootstrap.WritableMigration,
	}); err != nil {
		t.Fatal(err)
	}
	for _, stages := range [][]bootstrap.MigrationStage{
		{bootstrap.ReadOnlyClassification, bootstrap.WritableMigration},
		{bootstrap.ReadOnlyClassification, bootstrap.SourceRevalidation, bootstrap.CompleteVerifiedBackup, bootstrap.WritableMigration},
		{bootstrap.CompleteVerifiedBackup, bootstrap.ReadOnlyClassification, bootstrap.SourceRevalidation, bootstrap.WritableMigration},
	} {
		if err := bootstrap.ValidateUpgradeOrdering(stages); err == nil {
			t.Fatalf("unsafe ordering accepted: %v", stages)
		}
	}
}

func TestUpgradePlanCannotReinitializeAdminOrReplaceHistory(t *testing.T) {
	f := readFixture(t)
	var upgrade bootstrap.Plan
	for _, path := range f.Paths {
		if path.ID == "P4-BOOT-04" {
			upgrade = path.domain()
			break
		}
	}
	if err := upgrade.Validate(); err != nil {
		t.Fatal(err)
	}
	upgrade.InitializeAdminCredential = true
	if err := upgrade.Validate(); err == nil {
		t.Fatal("upgrade reinitialized administrator credential")
	}

	for _, path := range f.Paths {
		if path.ID == "P4-BOOT-04" {
			upgrade = path.domain()
			break
		}
	}
	upgrade.Preserved = []string{"authoritative_usage_history"}
	if err := upgrade.Validate(); err == nil {
		t.Fatal("upgrade dropped archive/retained evidence preservation")
	}
}

func TestPreviewPlansUseCanonicalControlBasePath(t *testing.T) {
	for _, path := range readFixture(t).Paths {
		base := path.domain()
		if err := base.Validate(); err != nil {
			t.Fatalf("invalid baseline plan %s: %v", path.ID, err)
		}
		for _, target := range []string{"/control/sub-path", "/AZaz09-._~$&+,/:;=@", "/控制", "/control!", "/control/"} {
			plan := base
			plan.ControlBasePath = target
			if got, want := plan.Validate() == nil, bootstrap.ValidControlBasePath(target); got != want {
				t.Errorf("plan %s path %q: valid=%v, canonical=%v", path.ID, target, got, want)
			}
		}
	}
}

func TestPlanEffectSetsAreDisjoint(t *testing.T) {
	f := readFixture(t)
	find := func(id string) bootstrap.Plan {
		t.Helper()
		for _, path := range f.Paths {
			if path.ID == id {
				return path.domain()
			}
		}
		t.Fatalf("missing path %s", id)
		return bootstrap.Plan{}
	}

	adopt := find("P4-BOOT-02")
	adopt.Mutations = append(adopt.Mutations, "historical_usage_import")
	if err := adopt.Validate(); err == nil {
		t.Fatal("Fresh Adoption accepted historical_usage_import in both mutations and unsupported")
	}

	external := find("P4-BOOT-03")
	external.Mutations = append(external.Mutations, "source_cpa_stop")
	if err := external.Validate(); err == nil {
		t.Fatal("External plan accepted source_cpa_stop in both mutations and unsupported")
	}

	fresh := find("P4-BOOT-01")
	fresh.Preserved = append(fresh.Preserved, fresh.Mutations[0])
	if err := fresh.Validate(); err == nil {
		t.Fatal("plan accepted the same effect in mutations and preserved")
	}
}

func TestCPACoreContractIsV8OnlyAndKeepsExternalLifecycleUserOwned(t *testing.T) {
	valid := bootstrap.CPACoreContract{
		ManagementGeneration:    bootstrap.CPAManagementGenerationV8,
		CoreV0FallbackAllowed:   false,
		ExternalManagementOwner: "cpamp_manager",
		ExternalLifecycleOwner:  "operator",
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := []bootstrap.CPACoreContract{
		{ManagementGeneration: "v0", ExternalManagementOwner: "cpamp_manager", ExternalLifecycleOwner: "operator"},
		{ManagementGeneration: "v8", CoreV0FallbackAllowed: true, ExternalManagementOwner: "cpamp_manager", ExternalLifecycleOwner: "operator"},
		{ManagementGeneration: "v8", ExternalManagementOwner: "cpamp_manager", ExternalLifecycleOwner: "cpamp_manager"},
	}
	for _, value := range invalid {
		if err := value.Validate(); err == nil {
			t.Fatalf("invalid core contract accepted: %+v", value)
		}
	}
}

func TestBootstrapTransitionsFailClosed(t *testing.T) {
	if err := bootstrap.ValidateTransition(bootstrap.Upgrade, bootstrap.Completed); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.ValidateTransition(bootstrap.Completed, bootstrap.Completed); err != nil {
		t.Fatal(err)
	}
	for _, transition := range [][2]bootstrap.Mode{
		{bootstrap.Recovery, bootstrap.Completed},
		{bootstrap.UnsupportedUpgrade, bootstrap.Fresh},
		{bootstrap.Completed, bootstrap.Fresh},
		{bootstrap.Completed, bootstrap.Upgrade},
		{bootstrap.Completed, bootstrap.Recovery},
		{"invalid", bootstrap.Fresh},
		{bootstrap.Fresh, "invalid"},
	} {
		if err := bootstrap.ValidateTransition(transition[0], transition[1]); err == nil {
			t.Fatalf("unsafe bootstrap transition accepted: %q -> %q", transition[0], transition[1])
		}
	}
}

func TestStrictFixtureRejectsUnknownDuplicateAndMissing(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(fixtureDir, "contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile(filepath.Join(fixtureDir, "contract.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	lf := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	for _, fixture := range []struct {
		name string
		data []byte
	}{
		{"LF", lf},
		{"CRLF", bytes.ReplaceAll(lf, []byte("\n"), []byte("\r\n"))},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			unknown := bytes.Replace(fixture.data, []byte(`"schemaVersion": 2,`), []byte(`"schemaVersion": 2, "unexpected": true,`), 1)
			if _, err := strictDecodeFixture(unknown, schema); err == nil {
				t.Fatal("unknown field accepted")
			}
			duplicate := bytes.Replace(fixture.data, []byte(`"schemaVersion": 2,`), []byte(`"schemaVersion": 2, "schemaVersion": 2,`), 1)
			if _, err := strictDecodeFixture(duplicate, schema); err == nil {
				t.Fatal("duplicate key accepted")
			}
			// Remove the JSON member independently of checkout line endings.
			missing := bytes.Replace(fixture.data, []byte(`"contractId": "cpamp-v2-phase4-00-bootstrap-contract-v2",`), nil, 1)
			if bytes.Equal(missing, fixture.data) {
				t.Fatal("negative fixture did not remove the required field")
			}
			if _, err := strictDecodeFixture(missing, schema); err == nil {
				t.Fatal("missing required field accepted")
			}
		})
	}
}

func TestEveryIncompleteCheckpointRoutesRecovery(t *testing.T) {
	admission := exactAdmission()
	states := []bootstrap.CheckpointState{
		bootstrap.CheckpointPending, bootstrap.CheckpointRunning, bootstrap.CheckpointApplying,
		bootstrap.CheckpointClearing, bootstrap.CheckpointFailed, bootstrap.CheckpointOfflineRequired,
	}
	for _, state := range states {
		input := baseClassification(&admission)
		input.RequiredCheckpoints.CacheAccounting = state
		got, err := bootstrap.Classify(input)
		if err != nil || got != bootstrap.Recovery {
			t.Fatalf("cache %s got=%s err=%v", state, got, err)
		}
		input = baseClassification(&admission)
		input.RequiredCheckpoints.QuotaSnapshot = state
		got, err = bootstrap.Classify(input)
		if err != nil || got != bootstrap.Recovery {
			t.Fatalf("quota %s got=%s err=%v", state, got, err)
		}
	}
}

func TestHistoricalAuthorityAndAdoptionReferencesFailClosed(t *testing.T) {
	admission := exactAdmission()
	for _, history := range []bootstrap.HistoricalEvidence{
		{SourceState: bootstrap.HistoryRawDeleted, AuthorityStatus: bootstrap.HistoryAvailable, RetainedEvidenceComplete: false},
		{SourceState: bootstrap.HistoryIncomplete, AuthorityStatus: bootstrap.HistoryIndeterminate},
		{SourceState: bootstrap.HistoryFullRaw, AuthorityStatus: bootstrap.HistoryUnavailable},
	} {
		input := baseClassification(&admission)
		input.HistoricalEvidence = &history
		got, err := bootstrap.Classify(input)
		if err != nil || got != bootstrap.Recovery {
			t.Fatalf("history %+v got=%s err=%v", history, got, err)
		}
	}

	decision := bootstrap.AdoptionResourceDecision{
		ResourceRef: "credential-a", Kind: bootstrap.Credentials, Scope: bootstrap.CPAAuthFiles,
		ReadEvidenceRef: "preflight:read-a", ReplayEvidenceRef: "preflight:replay-a",
		VerifyEvidenceRef: "preflight:verify-a", Qualification: bootstrap.CapabilityPending,
	}
	got, err := bootstrap.DecideAdoptionResource(decision)
	if err != nil || got != bootstrap.Unsupported {
		t.Fatalf("pending references got=%s err=%v", got, err)
	}
	decision.Qualification = bootstrap.CapabilityQualified
	got, err = bootstrap.DecideAdoptionResource(decision)
	if err != nil || got != bootstrap.Migrate {
		t.Fatalf("qualified references got=%s err=%v", got, err)
	}
}

func TestCPAQualificationFreshClaimAndGatewayAreIndependentGates(t *testing.T) {
	for _, item := range []struct {
		name      string
		strategy  bootstrap.CPAStrategy
		adopt     bootstrap.AdoptSourceClass
		q         bootstrap.CPAQualification
		wantError bool
	}{
		{"new managed missing artifact", bootstrap.NewManaged, bootstrap.AdoptSourceNone, bootstrap.CPAQualification{ManagementGeneration: "v8"}, true},
		{"external missing profile", bootstrap.ConnectExternal, bootstrap.AdoptSourceNone, bootstrap.CPAQualification{ManagementGeneration: "v8"}, true},
		{"migrate legacy missing staging", bootstrap.MigrateLegacy, bootstrap.AdoptSourceNone, bootstrap.CPAQualification{ManagementGeneration: "v8"}, true},
		{"adopt missing class", bootstrap.AdoptExisting, bootstrap.AdoptSourceNone, bootstrap.CPAQualification{ManagementGeneration: "v8", RequiredProfileQualified: true}, true},
		{"network adopt cannot use staging", bootstrap.AdoptExisting, bootstrap.AdoptNetworkExisting, bootstrap.CPAQualification{ManagementGeneration: "v8", StagingV8TransitionQualified: true}, true},
		{"legacy adopt cannot use network profile", bootstrap.AdoptExisting, bootstrap.AdoptLegacyManaged, bootstrap.CPAQualification{ManagementGeneration: "v8", RequiredProfileQualified: true}, true},
		{"network adopt qualified", bootstrap.AdoptExisting, bootstrap.AdoptNetworkExisting, bootstrap.CPAQualification{ManagementGeneration: "v8", RequiredProfileQualified: true}, false},
		{"legacy adopt cannot use staging alone", bootstrap.AdoptExisting, bootstrap.AdoptLegacyManaged, bootstrap.CPAQualification{ManagementGeneration: "v8", StagingV8TransitionQualified: true}, true},
		{"legacy adopt qualified", bootstrap.AdoptExisting, bootstrap.AdoptLegacyManaged, bootstrap.CPAQualification{ManagementGeneration: "v8", RequiredProfileQualified: true, StagingV8TransitionQualified: true}, false},
	} {
		err := item.q.ValidateForStrategy(item.strategy, item.adopt)
		if item.wantError && err == nil {
			t.Fatalf("%s unexpectedly accepted", item.name)
		}
		if !item.wantError && err != nil {
			t.Fatalf("%s rejected: %v", item.name, err)
		}
	}

	claim := bootstrap.FreshClaim{
		InstallationID: "install-a", GenerationID: "gen-a",
		CurrentInstallationID: "install-a", CurrentGenerationID: "gen-a",
		ExpectedRevision: 7, CurrentRevision: 7,
		ExpiresAtUnixMilli: 200, NowUnixMilli: 100,
		PersistedAcrossRestart: true, CSRFVerified: true, HostAuthenticityVerified: true,
	}
	if !bootstrap.CanConsumeFreshClaim(bootstrap.Fresh, bootstrap.AdminCredentialUninitialized, claim) {
		t.Fatal("eligible fresh claim rejected")
	}
	negative := []struct {
		name string
		edit func(*bootstrap.FreshClaim)
	}{
		{"expired", func(c *bootstrap.FreshClaim) { c.NowUnixMilli = c.ExpiresAtUnixMilli }},
		{"revoked", func(c *bootstrap.FreshClaim) { c.Revoked = true }},
		{"already consumed", func(c *bootstrap.FreshClaim) { c.Consumed = true }},
		{"restart persistence missing", func(c *bootstrap.FreshClaim) { c.PersistedAcrossRestart = false }},
		{"csrf missing", func(c *bootstrap.FreshClaim) { c.CSRFVerified = false }},
		{"host authenticity missing", func(c *bootstrap.FreshClaim) { c.HostAuthenticityVerified = false }},
		{"installation missing", func(c *bootstrap.FreshClaim) { c.InstallationID = "" }},
		{"generation missing", func(c *bootstrap.FreshClaim) { c.GenerationID = "" }},
		{"installation mismatch", func(c *bootstrap.FreshClaim) { c.CurrentInstallationID = "install-b" }},
		{"generation mismatch", func(c *bootstrap.FreshClaim) { c.CurrentGenerationID = "gen-b" }},
		{"stale claim revision", func(c *bootstrap.FreshClaim) { c.CurrentRevision++ }},
	}
	for _, tc := range negative {
		t.Run("claim/"+tc.name, func(t *testing.T) {
			value := claim
			tc.edit(&value)
			if bootstrap.CanConsumeFreshClaim(bootstrap.Fresh, bootstrap.AdminCredentialUninitialized, value) {
				t.Fatal("unsafe fresh claim accepted")
			}
		})
	}
	if bootstrap.CanConsumeFreshClaim(bootstrap.Upgrade, bootstrap.AdminCredentialUninitialized, claim) {
		t.Fatal("Upgrade acquired Fresh claim authority")
	}
	if bootstrap.CanConsumeFreshClaim(bootstrap.Fresh, bootstrap.AdminCredentialInitialized, claim) {
		t.Fatal("initialized administrator reacquired Fresh claim authority")
	}

	before := bootstrap.FreshInitializationState{Claim: claim, AdminCredential: bootstrap.AdminCredentialUninitialized}
	after := before
	after.Claim.Consumed = true
	after.Claim.CurrentRevision++
	after.AdminCredential = bootstrap.AdminCredentialInitialized
	after.ProductInitialized = true
	if err := bootstrap.ValidateFreshInitializationCommit(before, after); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.ValidateFreshInitializationCommit(after, after); err == nil {
		t.Fatal("consumed claim replay accepted")
	}
	partial := before
	partial.Claim.Consumed = true
	partial.Claim.CurrentRevision++
	if err := bootstrap.ValidateFreshInitializationCommit(before, partial); err == nil {
		t.Fatal("claim consumption without administrator/product commit accepted")
	}
	staleConcurrent := claim
	staleConcurrent.CurrentRevision++
	if bootstrap.CanConsumeFreshClaim(bootstrap.Fresh, bootstrap.AdminCredentialUninitialized, staleConcurrent) {
		t.Fatal("stale concurrent claim revision acquired initialization authority")
	}

	if bootstrap.CanActivatePublicGateway(bootstrap.GatewayActivationPreconditions{
		ProductInitialized: true, ManagementProfileQualified: true, RequiredEffectiveConfigQualified: true,
	}) {
		t.Fatal("initialized product activated Gateway without client-auth qualification")
	}
}

func TestDiscoveryPrefixAndJournalFailClosed(t *testing.T) {
	public := []bootstrap.DiscoveryField{
		bootstrap.DiscoveryMode, bootstrap.DiscoveryAuthenticationRequired, bootstrap.DiscoveryRecoveryRequired,
	}
	if err := bootstrap.ValidateDiscoveryProjection(false, public); err != nil {
		t.Fatal(err)
	}
	for _, field := range []bootstrap.DiscoveryField{
		bootstrap.DiscoverySourceCPA, bootstrap.DiscoverySourceAdmission, bootstrap.DiscoveryDatabase,
		bootstrap.DiscoveryBackup, bootstrap.DiscoveryResourceRefs, bootstrap.DiscoveryMutations,
		bootstrap.DiscoveryPreserved, bootstrap.DiscoveryUnsupported,
	} {
		if err := bootstrap.ValidateDiscoveryProjection(false, []bootstrap.DiscoveryField{bootstrap.DiscoveryMode, field}); err == nil {
			t.Fatalf("unauthenticated discovery leak accepted: %s", field)
		}
	}

	prefix := bootstrap.PrefixOperation{
		OperationID: "operation-1000",
		InstallationID: "install-a", CurrentInstallationID: "install-a",
		ExpectedRoutingRevision: 7, CurrentRoutingRevision: 7,
		PreviousVerifiedEffectivePath: "/admin-old",
		DesiredControlBasePath: "/admin-x",
		Impact: bootstrap.PrefixControlRouteOnly, Stage: bootstrap.PrefixHealthVerified,
		AuthorityQualified: true, CollisionFree: true,
		PublicIdentityHealthy: true, StaticAssetsHealthy: true, APIAuthenticationHealthy: true,
		Result: bootstrap.PrefixPending,
	}
	if !bootstrap.CanCommitPrefix(prefix) {
		t.Fatal("valid prefix commit rejected")
	}
	prefix.CurrentRoutingRevision = 8
	if bootstrap.CanCommitPrefix(prefix) {
		t.Fatal("routing CAS conflict accepted")
	}
	prefix.CurrentRoutingRevision = 7
	prefix.Stage = bootstrap.PrefixRouteActivated
	prefix.Result = bootstrap.PrefixUnknown
	action, err := bootstrap.PrefixActionAfterOperation(prefix)
	if err != nil || action != bootstrap.PrefixInspectFirst {
		t.Fatalf("lost prefix result action=%s err=%v", action, err)
	}

	type journalCase struct {
		name       string
		record     bootstrap.JournalRecord
		observation bootstrap.JournalObservation
		mismatch   func(*bootstrap.JournalObservation)
		foreign    func(*bootstrap.JournalObservation)
	}
	cases := []journalCase{
		{
			name: "product",
			record: bootstrap.JournalRecord{
				Domain: bootstrap.ProductJournal, OperationID: "operation-1000",
				ObservedProductRevision: 7,
			},
			observation: bootstrap.JournalObservation{CurrentProductRevision: 7, CurrentGenerationID: "generation-a", CurrentRoutingRevision: 11},
			mismatch: func(o *bootstrap.JournalObservation) { o.CurrentProductRevision = 8 },
			foreign: func(o *bootstrap.JournalObservation) { o.CurrentGenerationID = "generation-b"; o.CurrentRoutingRevision = 12 },
		},
		{
			name: "cpa",
			record: bootstrap.JournalRecord{
				Domain: bootstrap.CPAJournal, OperationID: "operation-1000",
				ObservedGenerationID: "generation-a",
			},
			observation: bootstrap.JournalObservation{CurrentProductRevision: 7, CurrentGenerationID: "generation-a", CurrentRoutingRevision: 11},
			mismatch: func(o *bootstrap.JournalObservation) { o.CurrentGenerationID = "generation-b" },
			foreign: func(o *bootstrap.JournalObservation) { o.CurrentProductRevision = 8; o.CurrentRoutingRevision = 12 },
		},
		{
			name: "routing",
			record: bootstrap.JournalRecord{
				Domain: bootstrap.RoutingJournal, OperationID: "operation-1000",
				ObservedRoutingRevision: 11,
			},
			observation: bootstrap.JournalObservation{CurrentProductRevision: 7, CurrentGenerationID: "generation-a", CurrentRoutingRevision: 11},
			mismatch: func(o *bootstrap.JournalObservation) { o.CurrentRoutingRevision = 12 },
			foreign: func(o *bootstrap.JournalObservation) { o.CurrentProductRevision = 8; o.CurrentGenerationID = "generation-b" },
		},
	}
	for _, tc := range cases {
		t.Run("journal/"+tc.name, func(t *testing.T) {
			record := tc.record
			record.Result = bootstrap.JournalUnknown
			got, err := bootstrap.JournalReplayAction(record, "operation-1000", tc.observation)
			if err != nil || got != bootstrap.JournalInspectFirst {
				t.Fatalf("unknown result action=%s err=%v", got, err)
			}
			got, err = bootstrap.JournalReplayAction(record, "operation-2000", tc.observation)
			if err != nil || got != bootstrap.JournalRejectNewOperation {
				t.Fatalf("different operation action=%s err=%v", got, err)
			}

			record.Result = bootstrap.JournalRetryableFailure
			got, err = bootstrap.JournalReplayAction(record, "operation-1000", tc.observation)
			if err != nil || got != bootstrap.JournalInspectFirst {
				t.Fatalf("unqualified retry action=%s err=%v", got, err)
			}
			record.AcceptedReplayQualified = true
			got, err = bootstrap.JournalReplayAction(record, "operation-1000", tc.observation)
			if err != nil || got != bootstrap.JournalResumeSameOperation {
				t.Fatalf("qualified retry action=%s err=%v", got, err)
			}

			mismatch := tc.observation
			tc.mismatch(&mismatch)
			got, err = bootstrap.JournalReplayAction(record, "operation-1000", mismatch)
			if err != nil || got != bootstrap.JournalInspectFirst {
				t.Fatalf("own fence mismatch action=%s err=%v", got, err)
			}

			foreign := tc.observation
			tc.foreign(&foreign)
			got, err = bootstrap.JournalReplayAction(record, "operation-1000", foreign)
			if err != nil || got != bootstrap.JournalResumeSameOperation {
				t.Fatalf("foreign fence change affected domain action=%s err=%v", got, err)
			}

			record.Result = bootstrap.JournalCommitted
			record.CommitRef = "commit:1"
			got, err = bootstrap.JournalReplayAction(record, "operation-1000", foreign)
			if err != nil || got != bootstrap.JournalReturnCommitted {
				t.Fatalf("committed journal lost authority on foreign fence action=%s err=%v", got, err)
			}
		})
	}

	if _, err := bootstrap.EvaluateOperationConflict(
		bootstrap.OperationMigration, bootstrap.OperationMaintenance, true,
	); err == nil {
		t.Fatal("same operation id was allowed to change operation class")
	}
	if _, err := bootstrap.ClassifyOperationConflictScope(
		bootstrap.OperationMigration, bootstrap.OperationCPAUpdate,
	); err == nil {
		t.Fatal("unfrozen operation pair was prematurely classified")
	}
}

func TestPrefixRecoveryRequiresCurrentAuthorityAndSnapshot(t *testing.T) {
	op := bootstrap.PrefixOperation{
		OperationID: "operation-recovery-1000",
		InstallationID: "install-a", CurrentInstallationID: "install-a",
		ExpectedRoutingRevision: 7, CurrentRoutingRevision: 7,
		PreviousVerifiedEffectivePath: "/old-control",
		DesiredControlBasePath: "/new-control",
		Impact: bootstrap.PrefixControlRouteOnly,
		Stage: bootstrap.PrefixRouteActivated,
		AuthorityQualified: true,
		CollisionFree: true,
		Result: bootstrap.PrefixUnknown,
	}
	healthy := bootstrap.PrefixRecoveryObservation{
		CurrentInstallationID: "install-a",
		CurrentRoutingRevision: 7,
		CurrentEffectivePath: "/new-control",
		CurrentOperationOwnerQualified: true,
		CurrentCollisionFree: true,
		CurrentSourceQualified: true,
		PublicIdentityHealthy: true,
		StaticAssetsHealthy: true,
		APIAuthenticationHealthy: true,
	}
	if !bootstrap.CanCompletePrefixAfterObservation(op, healthy) {
		t.Fatal("qualified recovery observation could not complete prefix")
	}
	for _, tc := range []struct {
		name string
		edit func(*bootstrap.PrefixRecoveryObservation)
	}{
		{"owner", func(o *bootstrap.PrefixRecoveryObservation) { o.CurrentOperationOwnerQualified = false }},
		{"collision", func(o *bootstrap.PrefixRecoveryObservation) { o.CurrentCollisionFree = false }},
		{"source", func(o *bootstrap.PrefixRecoveryObservation) { o.CurrentSourceQualified = false }},
		{"revision", func(o *bootstrap.PrefixRecoveryObservation) { o.CurrentRoutingRevision = 8 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := healthy
			tc.edit(&value)
			if bootstrap.CanCompletePrefixAfterObservation(op, value) {
				t.Fatal("stale or unqualified recovery observation completed prefix")
			}
		})
	}

	unhealthy := healthy
	unhealthy.PublicIdentityHealthy = false
	unhealthy.PreviousSnapshotQualified = true
	unhealthy.PreviousSnapshotEffectivePath = "/old-control"
	if !bootstrap.CanConditionallyRestorePrefix(op, unhealthy) {
		t.Fatal("qualified previous snapshot could not authorize conditional restore")
	}
	unhealthy.PreviousSnapshotQualified = false
	unhealthy.PreviousSnapshotEffectivePath = ""
	if bootstrap.CanConditionallyRestorePrefix(op, unhealthy) {
		t.Fatal("conditional restore accepted without verified previous snapshot")
	}

	stalePersisted := op
	stalePersisted.CurrentRoutingRevision = 8
	staleObserved := healthy
	staleObserved.CurrentRoutingRevision = 8
	if bootstrap.CanCompletePrefixAfterObservation(stalePersisted, staleObserved) ||
		bootstrap.CanConditionallyRestorePrefix(stalePersisted, staleObserved) {
		t.Fatal("stale expected routing revision authorized prefix recovery")
	}
}

func TestControlBasePathRejectsEncodedAndBackslashBypasses(t *testing.T) {
	base := bootstrap.PrefixOperation{
		OperationID: "operation-2000",
		InstallationID: "install-a", CurrentInstallationID: "install-a",
		ExpectedRoutingRevision: 9, CurrentRoutingRevision: 9,
		PreviousVerifiedEffectivePath: "/old-control",
		DesiredControlBasePath: "/valid-control",
		Impact: bootstrap.PrefixControlRouteOnly,
		Stage: bootstrap.PrefixIntentPersisted,
		Result: bootstrap.PrefixPending,
	}
	for _, target := range []string{
		"", "/", "/admin/", "/admin//x", "/admin/../x", "/admin/./x",
		"/admin/%2e%2e/x", "/admin/%2f/x", "/admin/%252e%252e/x",
		"/admin\\x", "/admin x", "/admin?x", "/admin#x",
	} {
		t.Run(target, func(t *testing.T) {
			value := base
			value.DesiredControlBasePath = target
			if err := value.Validate(); err == nil {
				t.Fatalf("unsafe controlBasePath accepted: %q", target)
			}
		})
	}
}

func TestContractSchemaIsClosedV2(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(fixtureDir, "contract.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" || schema["additionalProperties"] != false {
		t.Fatal("schema must be draft 2020-12 and reject top-level additional properties")
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema properties missing")
	}
	version, ok := properties["schemaVersion"].(map[string]any)
	if !ok || version["const"] != float64(2) {
		t.Fatal("schemaVersion must remain 2")
	}
}
