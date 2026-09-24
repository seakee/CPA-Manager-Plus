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
	"slices"
	"sort"
	"strings"
	"testing"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

const fixtureDir = "../../../../../tests/fixtures/phase4-bootstrap"
const baseline = "f5d12c6ca4af1f36f087e8acd28e565c5a0ae655"

type source struct {
	VersionTag     string `json:"versionTag"`
	SchemaIdentity string `json:"schemaIdentity"`
	SchemaVersion  int    `json:"schemaVersion"`
}

func (s source) domain() bootstrap.DirectUpgradeSource {
	return bootstrap.DirectUpgradeSource{VersionTag: s.VersionTag, SchemaIdentity: s.SchemaIdentity, SchemaVersion: s.SchemaVersion}
}

type modeEntry struct {
	Mode                bootstrap.Mode `json:"mode"`
	Category            string         `json:"category"`
	OrdinaryPathAllowed bool           `json:"ordinaryPathAllowed"`
	Meaning             string         `json:"meaning"`
}

type strategyEntry struct {
	Strategy     bootstrap.CPAStrategy `json:"strategy"`
	AllowedModes []bootstrap.Mode      `json:"allowedModes"`
	RuntimeMode  bootstrap.RuntimeMode `json:"runtimeMode"`
}

type adoptionEntry struct {
	Kind           bootstrap.AdoptionResourceKind `json:"kind"`
	Scope          bootstrap.AdoptionScope        `json:"scope"`
	ReadEvidence   string                         `json:"readEvidence"`
	ReplayEvidence string                         `json:"replayEvidence"`
	VerifyEvidence string                         `json:"verifyEvidence"`
	ManualAction   bootstrap.AdoptionManualAction `json:"manualAction"`
	Disposition    bootstrap.AdoptionDisposition  `json:"disposition"`
}

func (a adoptionEntry) domain() bootstrap.AdoptionResource {
	return bootstrap.AdoptionResource{
		Kind: a.Kind, Scope: a.Scope, ReadEvidence: a.ReadEvidence,
		ReplayEvidence: a.ReplayEvidence, VerifyEvidence: a.VerifyEvidence,
		ManualAction: a.ManualAction, Disposition: a.Disposition,
	}
}

type pathEntry struct {
	ID                string                `json:"id"`
	Mode              bootstrap.Mode        `json:"mode"`
	CPAMPAction       bootstrap.CPAMPAction `json:"cpamp_action"`
	CPAStrategy       bootstrap.CPAStrategy `json:"cpa_strategy"`
	SourceCPAMP       *source               `json:"source_cpamp"`
	SourceCPA         *bootstrap.SourceCPA  `json:"source_cpa"`
	UsageAction       bootstrap.UsageAction `json:"usage_action"`
	BackupRequired    bool                  `json:"backup_required"`
	RuntimeMode       bootstrap.RuntimeMode `json:"runtime_mode"`
	Mutations         []string              `json:"mutations"`
	Preserved         []string              `json:"preserved"`
	Unsupported       []string              `json:"unsupported"`
	AdoptionResources []adoptionEntry       `json:"adoptionResources"`
	ControlBasePath   string                `json:"control_base_path"`
	EstimatedSteps    []string              `json:"estimated_steps"`
}

func (p pathEntry) domain() bootstrap.Plan {
	var cpamp *bootstrap.DirectUpgradeSource
	if p.SourceCPAMP != nil {
		s := p.SourceCPAMP.domain()
		cpamp = &s
	}
	var cpa bootstrap.SourceCPA
	if p.SourceCPA != nil {
		cpa = *p.SourceCPA
	}
	resources := make([]bootstrap.AdoptionResource, len(p.AdoptionResources))
	for i, resource := range p.AdoptionResources {
		resources[i] = resource.domain()
	}
	return bootstrap.Plan{
		Mode: p.Mode, CPAMPAction: p.CPAMPAction, CPAStrategy: p.CPAStrategy,
		SourceCPAMP: cpamp, SourceCPA: cpa, UsageAction: p.UsageAction,
		BackupRequired: p.BackupRequired, RuntimeMode: p.RuntimeMode,
		Mutations: p.Mutations, Preserved: p.Preserved, Unsupported: p.Unsupported,
		AdoptionResources: resources,
		ControlBasePath:   p.ControlBasePath, EstimatedSteps: p.EstimatedSteps,
	}
}

type classificationAssertion struct {
	ID                string                   `json:"id"`
	HasV1ProductState bool                     `json:"hasV1ProductState"`
	Source            *source                  `json:"source"`
	RecoveryReason    bootstrap.RecoveryReason `json:"recoveryReason"`
	BootstrapComplete bool                     `json:"bootstrapComplete"`
	ExpectedMode      bootstrap.Mode           `json:"expectedMode"`
}

type fixture struct {
	Schema                   string                    `json:"$schema"`
	SchemaVersion            int                       `json:"schemaVersion"`
	ContractID               string                    `json:"contractId"`
	ExecutionBaseline        string                    `json:"executionBaseline"`
	BootstrapModes           []modeEntry               `json:"bootstrapModes"`
	CPAStrategies            []strategyEntry           `json:"cpaStrategies"`
	DirectUpgradeSources     []source                  `json:"directUpgradeSources"`
	Paths                    []pathEntry               `json:"paths"`
	ClassificationAssertions []classificationAssertion `json:"classificationAssertions"`
	GlobalAssertions         []string                  `json:"globalAssertions"`
}

// Require every declared fixture field. DisallowUnknownFields alone does not
// reject omitted booleans, null source fields or empty array fields.
func requireFields(data []byte, fields ...string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	for _, field := range fields {
		if _, ok := object[field]; !ok {
			return fmt.Errorf("missing required field %q", field)
		}
	}
	return nil
}

func requireArrayObjects(data []byte, field string, keys ...string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	var values []json.RawMessage
	if err := json.Unmarshal(object[field], &values); err != nil || values == nil {
		return fmt.Errorf("%s must be an array: %v", field, err)
	}
	for i, value := range values {
		if err := requireFields(value, keys...); err != nil {
			return fmt.Errorf("%s[%d]: %w", field, i, err)
		}
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return fmt.Errorf("duplicate or invalid JSON key %q", key)
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
		default:
			return errors.New("unexpected JSON closing delimiter")
		}
		_, err = decoder.Token()
		return err
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON value or error: %v", err)
	}
	return nil
}

func decodeFixture(data []byte) (fixture, error) {
	if err := rejectDuplicateKeys(data); err != nil {
		return fixture{}, err
	}
	if err := requireFields(data, "$schema", "schemaVersion", "contractId", "executionBaseline", "bootstrapModes", "cpaStrategies", "directUpgradeSources", "paths", "classificationAssertions", "globalAssertions"); err != nil {
		return fixture{}, err
	}
	for _, check := range []struct {
		field string
		keys  []string
	}{
		{"bootstrapModes", []string{"mode", "category", "ordinaryPathAllowed", "meaning"}},
		{"cpaStrategies", []string{"strategy", "allowedModes", "runtimeMode"}},
		{"directUpgradeSources", []string{"versionTag", "schemaIdentity", "schemaVersion"}},
		{"paths", []string{"id", "mode", "cpamp_action", "cpa_strategy", "source_cpamp", "source_cpa", "usage_action", "backup_required", "runtime_mode", "mutations", "preserved", "unsupported", "adoptionResources", "control_base_path", "estimated_steps"}},
		{"classificationAssertions", []string{"id", "hasV1ProductState", "source", "recoveryReason", "bootstrapComplete", "expectedMode"}},
	} {
		if err := requireArrayObjects(data, check.field, check.keys...); err != nil {
			return fixture{}, err
		}
	}
	var raw struct {
		Paths                    []map[string]json.RawMessage `json:"paths"`
		ClassificationAssertions []map[string]json.RawMessage `json:"classificationAssertions"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fixture{}, err
	}
	for _, item := range raw.Paths {
		var resources []json.RawMessage
		if err := json.Unmarshal(item["adoptionResources"], &resources); err != nil || resources == nil {
			return fixture{}, fmt.Errorf("adoptionResources must be an array: %v", err)
		}
		for _, resource := range resources {
			if err := requireFields(resource, "kind", "scope", "readEvidence", "replayEvidence", "verifyEvidence", "manualAction", "disposition"); err != nil {
				return fixture{}, err
			}
		}
		if string(item["source_cpamp"]) != "null" {
			if err := requireFields(item["source_cpamp"], "versionTag", "schemaIdentity", "schemaVersion"); err != nil {
				return fixture{}, err
			}
		}
	}
	for _, item := range raw.ClassificationAssertions {
		if string(item["source"]) != "null" {
			if err := requireFields(item["source"], "versionTag", "schemaIdentity", "schemaVersion"); err != nil {
				return fixture{}, err
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result fixture
	if err := decoder.Decode(&result); err != nil {
		return fixture{}, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fixture{}, fmt.Errorf("trailing JSON value or error: %v", err)
	}
	return result, nil
}

func sameSet(values, expected []string) bool {
	if len(values) != len(expected) {
		return false
	}
	a, b := append([]string(nil), values...), append([]string(nil), expected...)
	sort.Strings(a)
	sort.Strings(b)
	return reflect.DeepEqual(a, b)
}

func validateFixture(f fixture) error {
	if f.Schema != "./contract.schema.json" || f.SchemaVersion != 1 ||
		f.ContractID != "cpamp-v2-phase4-00-bootstrap-contract-v1" || f.ExecutionBaseline != baseline {
		return errors.New("fixture identity or execution baseline changed")
	}
	modes := map[bootstrap.Mode]struct {
		category string
		ordinary bool
	}{
		bootstrap.Fresh: {"initial", true}, bootstrap.Upgrade: {"initial", true},
		bootstrap.UnsupportedUpgrade: {"blocked", false}, bootstrap.Recovery: {"repair", false},
		bootstrap.Completed: {"terminal", false},
	}
	if len(f.BootstrapModes) != len(modes) {
		return errors.New("bootstrap modes must contain exactly five entries")
	}
	seenModes := map[bootstrap.Mode]bool{}
	for _, item := range f.BootstrapModes {
		want, ok := modes[item.Mode]
		if !ok || seenModes[item.Mode] || item.Category != want.category || item.OrdinaryPathAllowed != want.ordinary || item.Meaning == "" {
			return fmt.Errorf("invalid or duplicate bootstrap mode %q", item.Mode)
		}
		seenModes[item.Mode] = true
	}
	strategies := map[bootstrap.CPAStrategy][]string{
		bootstrap.NewManaged: {"fresh"}, bootstrap.AdoptExisting: {"fresh"},
		bootstrap.MigrateLegacy: {"upgrade"}, bootstrap.ConnectExternal: {"fresh", "upgrade"},
	}
	if len(f.CPAStrategies) != len(strategies) {
		return errors.New("CPA strategies must contain exactly four entries")
	}
	seenStrategies := map[bootstrap.CPAStrategy]bool{}
	for _, item := range f.CPAStrategies {
		want, ok := strategies[item.Strategy]
		runtime, err := bootstrap.RuntimeForStrategy(item.Strategy)
		allowed := make([]string, len(item.AllowedModes))
		for i, mode := range item.AllowedModes {
			allowed[i] = string(mode)
		}
		if !ok || seenStrategies[item.Strategy] || err != nil || runtime != item.RuntimeMode || !sameSet(allowed, want) {
			return fmt.Errorf("invalid or duplicate CPA strategy %q", item.Strategy)
		}
		seenStrategies[item.Strategy] = true
	}
	allowlist := make([]bootstrap.DirectUpgradeSource, len(f.DirectUpgradeSources))
	for i, item := range f.DirectUpgradeSources {
		allowlist[i] = item.domain()
	}
	if len(allowlist) != 1 || allowlist[0] != (bootstrap.DirectUpgradeSource{"v1.synthetic-allowed", "cpamp-v1-synthetic", 7}) {
		return errors.New("fixture must use the deterministic synthetic direct-upgrade source")
	}
	if _, err := bootstrap.Classify(bootstrap.ClassificationEvidence{}, allowlist); err != nil {
		return err
	}
	type pathWant struct {
		mode                              bootstrap.Mode
		strategy                          bootstrap.CPAStrategy
		mutations, preserved, unsupported []string
	}
	paths := map[string]pathWant{
		"P4-BOOT-01": {bootstrap.Fresh, bootstrap.NewManaged, []string{"cpamp_state", "managed_cpa", "control_base_path"}, []string{}, []string{}},
		"P4-BOOT-02": {bootstrap.Fresh, bootstrap.AdoptExisting, []string{"cpamp_state", "managed_cpa", "control_base_path", "migrate_api_keys", "migrate_credentials", "migrate_cpa_configuration"}, []string{"source_cpa_recoverable_until_success", "source_cpa_available_until_commit"}, []string{"historical_usage_import", "request_history_import", "cpa_log_import", "historical_analytics_import", "plugin_runtime_implicit_import", "source_cpa_stop", "source_cpa_delete", "source_cpa_overwrite", "manual_action_provider_runtime_state"}},
		"P4-BOOT-03": {bootstrap.Fresh, bootstrap.ConnectExternal, []string{"cpamp_state", "control_base_path", "external_connection"}, []string{"source_cpa_user_owned"}, []string{"historical_usage_import", "source_cpa_stop", "source_cpa_delete", "source_cpa_takeover"}},
		"P4-BOOT-04": {bootstrap.Upgrade, bootstrap.MigrateLegacy, []string{"cpamp_state", "managed_cpa", "control_base_path"}, []string{"usage_events_authoritative"}, []string{"usage_events_export_transform_reimport"}},
		"P4-BOOT-05": {bootstrap.Upgrade, bootstrap.ConnectExternal, []string{"cpamp_state", "control_base_path", "external_connection"}, []string{"usage_events_authoritative", "source_cpa_user_owned"}, []string{"usage_events_export_transform_reimport", "source_cpa_stop", "source_cpa_delete", "source_cpa_takeover"}},
	}
	if len(f.Paths) != len(paths) {
		return errors.New("exactly five user paths are required")
	}
	seenPaths := map[string]bool{}
	for _, item := range f.Paths {
		want, ok := paths[item.ID]
		if !ok || seenPaths[item.ID] || item.Mode != want.mode || item.CPAStrategy != want.strategy {
			return fmt.Errorf("invalid or duplicate path %q", item.ID)
		}
		seenPaths[item.ID] = true
		if err := item.domain().Validate(allowlist); err != nil {
			return fmt.Errorf("path %s: %w", item.ID, err)
		}
		if !sameSet(item.Mutations, want.mutations) || !sameSet(item.Preserved, want.preserved) || !sameSet(item.Unsupported, want.unsupported) {
			return fmt.Errorf("path %s effects changed", item.ID)
		}
		if item.ID == "P4-BOOT-02" {
			resourceWant := map[bootstrap.AdoptionResourceKind]struct {
				scope       bootstrap.AdoptionScope
				disposition bootstrap.AdoptionDisposition
			}{
				bootstrap.APIKeys:              {bootstrap.ClientAPIKeys, bootstrap.Migrate},
				bootstrap.Credentials:          {bootstrap.PortableAuthFiles, bootstrap.Migrate},
				bootstrap.CPAConfiguration:     {bootstrap.ManagementAPIFields, bootstrap.Migrate},
				bootstrap.ProviderRuntimeState: {bootstrap.DurableProviderState, bootstrap.ManualAction},
			}
			if len(item.AdoptionResources) != len(resourceWant) {
				return errors.New("adoption resource taxonomy must contain four categories")
			}
			seenResources := map[bootstrap.AdoptionResourceKind]bool{}
			for _, resource := range item.AdoptionResources {
				wantResource, ok := resourceWant[resource.Kind]
				if !ok || seenResources[resource.Kind] || resource.Scope != wantResource.scope || resource.Disposition != wantResource.disposition {
					return fmt.Errorf("invalid or duplicate adoption resource %q", resource.Kind)
				}
				seenResources[resource.Kind] = true
			}
		} else if len(item.AdoptionResources) != 0 {
			return fmt.Errorf("path %s cannot declare adoption resources", item.ID)
		}
		if item.ControlBasePath != "/phase4-fixture-control" {
			return fmt.Errorf("path %s must carry the synthetic canonical path", item.ID)
		}
		if item.Mode == bootstrap.Upgrade && (item.SourceCPAMP == nil || item.SourceCPAMP.domain() != allowlist[0]) {
			return fmt.Errorf("path %s has unsupported upgrade source", item.ID)
		}
	}
	assertions := map[string]bootstrap.Mode{
		"no_v1_is_fresh": bootstrap.Fresh, "exact_source_is_upgrade": bootstrap.Upgrade,
		"unknown_version_is_unsupported": bootstrap.UnsupportedUpgrade, "unknown_schema_is_unsupported": bootstrap.UnsupportedUpgrade,
		"missing_source_is_unsupported": bootstrap.UnsupportedUpgrade, "incomplete_operation_is_recovery": bootstrap.Recovery,
		"rollback_is_recovery": bootstrap.Recovery, "damaged_is_recovery": bootstrap.Recovery,
		"manual_is_recovery": bootstrap.Recovery, "completed_state": bootstrap.Completed,
		"recovery_overrides_completed": bootstrap.Recovery,
	}
	if len(f.ClassificationAssertions) != len(assertions) {
		return errors.New("classification assertions are incomplete")
	}
	seenAssertions := map[string]bool{}
	for _, item := range f.ClassificationAssertions {
		want, ok := assertions[item.ID]
		if !ok || seenAssertions[item.ID] || item.ExpectedMode != want {
			return fmt.Errorf("invalid or duplicate classification assertion %q", item.ID)
		}
		seenAssertions[item.ID] = true
		var src *bootstrap.DirectUpgradeSource
		if item.Source != nil {
			s := item.Source.domain()
			src = &s
		}
		got, err := bootstrap.Classify(bootstrap.ClassificationEvidence{
			HasV1ProductState: item.HasV1ProductState, Source: src,
			RecoveryReason: item.RecoveryReason, BootstrapComplete: item.BootstrapComplete,
		}, allowlist)
		if err != nil || got != want {
			return fmt.Errorf("classification %s: got %s, err %v, want %s", item.ID, got, err, want)
		}
	}
	global := []string{"legacy_bootstrap_state_separate", "recovery_server_checkpoint_authority", "recovery_no_direct_completed", "fresh_no_historical_usage", "upgrade_usage_events_authoritative", "upgrade_additive_derived_rebuild_allowed", "direct_upgrade_release_owned_exact_allowlist", "unknown_v1_fail_closed", "runtime_derived_from_strategy", "manager_desired_control_base_path_authority", "bootstrap_discovery_temporary", "completed_fixed_alias_no_path_disclosure", "control_base_path_not_authentication", "control_base_path_apply_deferred", "no_http_persistence_runtime_ui_behavior", "adoption_capability_gated_resource_set", "adoption_source_available_until_commit"}
	if !sameSet(f.GlobalAssertions, global) {
		return errors.New("global assertions missing, duplicated or unknown")
	}
	if err := bootstrap.ValidateTransition(bootstrap.Recovery, bootstrap.Completed); err == nil {
		return errors.New("recovery cannot transition directly to completed")
	}
	return nil
}

func readFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, "contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func schemaObject(root map[string]any, keys ...string) (map[string]any, error) {
	var current any = root
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema path %v is not an object", keys)
		}
		current = object[key]
	}
	object, ok := current.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema path %v is missing", keys)
	}
	return object, nil
}

func schemaStrings(value any) ([]string, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, errors.New("schema value must be an array")
	}
	result := make([]string, len(items))
	for i, item := range items {
		str, ok := item.(string)
		if !ok {
			return nil, errors.New("schema array must contain strings")
		}
		result[i] = str
	}
	return result, nil
}

func validateSchemaStructure(data []byte) error {
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		return err
	}
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" || schema["additionalProperties"] != false {
		return errors.New("schema draft or top-level closed shape changed")
	}
	checkSet := func(object map[string]any, key string, expected []string) error {
		actual, err := schemaStrings(object[key])
		if err != nil || !sameSet(actual, expected) {
			return fmt.Errorf("schema %s changed: %v", key, err)
		}
		return nil
	}
	if err := checkSet(schema, "required", []string{"$schema", "schemaVersion", "contractId", "executionBaseline", "bootstrapModes", "cpaStrategies", "directUpgradeSources", "paths", "classificationAssertions", "globalAssertions"}); err != nil {
		return err
	}
	checks := []struct {
		keys   []string
		field  string
		values []string
	}{
		{[]string{"$defs", "mode"}, "enum", []string{"fresh", "upgrade", "unsupported_upgrade", "recovery", "completed"}},
		{[]string{"$defs", "strategy"}, "enum", []string{"new_managed", "adopt_existing", "migrate_legacy", "connect_external"}},
		{[]string{"$defs", "adoptionKind"}, "enum", []string{"api_keys", "credentials", "cpa_configuration", "provider_runtime_state"}},
		{[]string{"$defs", "path", "properties", "id"}, "enum", []string{"P4-BOOT-01", "P4-BOOT-02", "P4-BOOT-03", "P4-BOOT-04", "P4-BOOT-05"}},
	}
	for _, check := range checks {
		object, err := schemaObject(schema, check.keys...)
		if err != nil {
			return err
		}
		if err := checkSet(object, check.field, check.values); err != nil {
			return err
		}
	}
	for _, name := range []string{"path", "classificationAssertion", "adoptionResource"} {
		object, err := schemaObject(schema, "$defs", name)
		if err != nil || object["additionalProperties"] != false {
			return fmt.Errorf("schema %s must reject additional properties: %v", name, err)
		}
	}
	path, err := schemaObject(schema, "$defs", "path")
	if err != nil {
		return err
	}
	if err := checkSet(path, "required", []string{"id", "mode", "cpamp_action", "cpa_strategy", "source_cpamp", "source_cpa", "usage_action", "backup_required", "runtime_mode", "mutations", "preserved", "unsupported", "adoptionResources", "control_base_path", "estimated_steps"}); err != nil {
		return err
	}
	return nil
}

func TestCheckedInContract(t *testing.T) {
	data := readFixture(t)
	f, err := decodeFixture(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateFixture(f); err != nil {
		t.Fatal(err)
	}
	schemaData, err := os.ReadFile(filepath.Join(fixtureDir, "contract.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSchemaStructure(schemaData); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaRejectsStructuralDrift(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(fixtureDir, "contract.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, old, replacement string }{
		{"top-level additionalProperties", `"additionalProperties": false,`, `"additionalProperties": true,`},
		{"required top-level key", `"classificationAssertions", "globalAssertions"]`, `"classificationAssertions"]`},
		{"mode enum", `"recovery", "completed"]`, `"recovery", "mystery"]`},
		{"strategy enum", `"migrate_legacy", "connect_external"]`, `"migrate_legacy", "mystery"]`},
		{"path ID enum", `"P4-BOOT-04", "P4-BOOT-05"]`, `"P4-BOOT-04", "P4-BOOT-99"]`},
		{"path additionalProperties", `"path": {
      "type": "object", "additionalProperties": false,`, `"path": {
      "type": "object", "additionalProperties": true,`},
		{"classification additionalProperties", `"classificationAssertion": {
      "type": "object", "additionalProperties": false,`, `"classificationAssertion": {
      "type": "object", "additionalProperties": true,`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !bytes.Contains(data, []byte(tc.old)) {
				t.Fatalf("schema mutation target absent: %s", tc.old)
			}
			changed := bytes.Replace(data, []byte(tc.old), []byte(tc.replacement), 1)
			if err := validateSchemaStructure(changed); err == nil {
				t.Fatal("schema drift was accepted")
			}
		})
	}
}

func TestFixtureRejectsContractDrift(t *testing.T) {
	data := readFixture(t)
	for _, tc := range []struct{ name, old, replacement string }{
		{"unknown field", `"schemaVersion": 1,`, `"schemaVersion": 1, "unexpected": true,`},
		{"duplicate JSON key", `"schemaVersion": 1,`, `"schemaVersion": 1, "schemaVersion": 1,`},
		{"missing mandatory path field", `"backup_required": false,`, ``},
		{"duplicate path", `"id": "P4-BOOT-02"`, `"id": "P4-BOOT-01"`},
		{"duplicate mode", `"mode": "unsupported_upgrade", "category"`, `"mode": "fresh", "category"`},
		{"duplicate strategy", `"strategy": "adopt_existing"`, `"strategy": "new_managed"`},
		{"unknown mode", `"mode": "unsupported_upgrade", "category"`, `"mode": "mystery", "category"`},
		{"unknown strategy", `"strategy": "adopt_existing"`, `"strategy": "mystery"`},
		{"invalid pair", `"id": "P4-BOOT-02", "mode": "fresh", "cpamp_action": "initialize", "cpa_strategy": "adopt_existing"`, `"id": "P4-BOOT-02", "mode": "fresh", "cpamp_action": "initialize", "cpa_strategy": "migrate_legacy"`},
		{"runtime mismatch", `"id": "P4-BOOT-03", "mode": "fresh", "cpamp_action": "initialize", "cpa_strategy": "connect_external",`, `"id": "P4-BOOT-03", "mode": "fresh", "cpamp_action": "initialize", "cpa_strategy": "connect_external",`},
		{"fresh usage migration", `"source_cpa": "existing", "usage_action": "none"`, `"source_cpa": "existing", "usage_action": "preserve"`},
		{"upgrade usage lost", `"source_cpa": "legacy", "usage_action": "preserve"`, `"source_cpa": "legacy", "usage_action": "none"`},
		{"adoption history import", `"historical_usage_import", "request_history_import"`, `"usage_events_authoritative", "request_history_import"`},
		{"adoption source stop", `"source_cpa_stop", "source_cpa_delete"`, `"source_cpa_delete"`},
		{"adoption duplicate resource", `"kind": "credentials"`, `"kind": "api_keys"`},
		{"adoption unreadable resource", `"readEvidence": "synthetic:GET /v0/management/api-keys"`, `"readEvidence": ""`},
		{"adoption resource not replayable", `"replayEvidence": "synthetic:PUT /v0/management/api-keys"`, `"replayEvidence": ""`},
		{"adoption resource not verifiable", `"verifyEvidence": "synthetic:GET staged /v0/management/api-keys"`, `"verifyEvidence": ""`},
		{"adoption manual state called migratable", `"manualAction": "reconfigure_after_adoption", "disposition": "manual_action"`, `"manualAction": "reconfigure_after_adoption", "disposition": "migrate"`},
		{"upgrade preserve missing", `"preserved": ["usage_events_authoritative"],`, `"preserved": [],`},
		{"unsupported source called fresh", `"id": "unknown_version_is_unsupported", "hasV1ProductState": true`, `"id": "unknown_version_is_unsupported", "hasV1ProductState": false`},
		{"recovery made user path", `"mode": "recovery", "category": "repair", "ordinaryPathAllowed": false`, `"mode": "recovery", "category": "repair", "ordinaryPathAllowed": true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !bytes.Contains(data, []byte(tc.old)) {
				t.Fatalf("mutation target absent: %s", tc.old)
			}
			changed := bytes.Replace(data, []byte(tc.old), []byte(tc.replacement), 1)
			if tc.name == "runtime mismatch" {
				needle := []byte(`"runtime_mode": "external"`)
				pos := bytes.Index(changed, []byte(`"id": "P4-BOOT-03"`))
				changed = append(changed[:pos], bytes.Replace(changed[pos:], needle, []byte(`"runtime_mode": "embedded"`), 1)...)
			}
			f, err := decodeFixture(changed)
			if err == nil {
				err = validateFixture(f)
			}
			if err == nil {
				t.Fatal("contract drift was accepted")
			}
		})
	}
}

func TestDomainRejectsForbiddenPlans(t *testing.T) {
	f, err := decodeFixture(readFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	allowlist := []bootstrap.DirectUpgradeSource{f.DirectUpgradeSources[0].domain()}
	for _, tc := range []struct {
		name string
		plan bootstrap.Plan
	}{
		{"fresh migrate legacy", func() bootstrap.Plan { p := f.Paths[0].domain(); p.CPAStrategy = bootstrap.MigrateLegacy; return p }()},
		{"upgrade new managed", func() bootstrap.Plan { p := f.Paths[3].domain(); p.CPAStrategy = bootstrap.NewManaged; return p }()},
		{"upgrade adopt existing", func() bootstrap.Plan { p := f.Paths[3].domain(); p.CPAStrategy = bootstrap.AdoptExisting; return p }()},
		{"recovery cannot select strategy", func() bootstrap.Plan { p := f.Paths[0].domain(); p.Mode = bootstrap.Recovery; return p }()},
		{"unsupported upgrade cannot select strategy", func() bootstrap.Plan { p := f.Paths[0].domain(); p.Mode = bootstrap.UnsupportedUpgrade; return p }()},
		{"completed cannot select strategy", func() bootstrap.Plan { p := f.Paths[0].domain(); p.Mode = bootstrap.Completed; return p }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.plan.Validate(allowlist); err == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
	unsupported := f.Paths[3].domain()
	unsupported.SourceCPAMP = &bootstrap.DirectUpgradeSource{VersionTag: "v1.synthetic-newer", SchemaIdentity: "cpamp-v1-synthetic", SchemaVersion: 7}
	if err := unsupported.Validate(allowlist); err == nil {
		t.Fatal("unsupported upgrade source accepted by plan")
	}
	if _, err := bootstrap.Classify(bootstrap.ClassificationEvidence{HasV1ProductState: true}, []bootstrap.DirectUpgradeSource{{"v1.synthetic-allowed", "cpamp-v1-synthetic", 7}, {"v1.synthetic-allowed", "cpamp-v1-synthetic", 7}}); err == nil {
		t.Fatal("duplicate allowlist source accepted")
	}
	if _, err := bootstrap.Classify(bootstrap.ClassificationEvidence{RecoveryReason: "unknown"}, nil); err == nil {
		t.Fatal("unknown recovery reason accepted")
	}
	if _, err := bootstrap.RuntimeForStrategy("embedded"); err == nil {
		t.Fatal("runtime mode accepted as strategy")
	}
	if strings.Contains(string(readFixture(t)), "v1.13.1") || strings.Contains(string(readFixture(t)), "v1.13.2") {
		t.Fatal("release tags must not be pinned by domain fixture")
	}
}

func TestClassifyIsolatesReleaseAllowlist(t *testing.T) {
	bad := []bootstrap.DirectUpgradeSource{{VersionTag: "", SchemaIdentity: "cpamp-v1-synthetic", SchemaVersion: 7}}
	for _, tc := range []struct {
		name     string
		evidence bootstrap.ClassificationEvidence
		want     bootstrap.Mode
	}{
		{"recovery", bootstrap.ClassificationEvidence{HasV1ProductState: true, RecoveryReason: bootstrap.OperationIncomplete}, bootstrap.Recovery},
		{"completed", bootstrap.ClassificationEvidence{HasV1ProductState: true, BootstrapComplete: true}, bootstrap.Completed},
		{"fresh", bootstrap.ClassificationEvidence{}, bootstrap.Fresh},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := bootstrap.Classify(tc.evidence, bad)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, err %v; want %q", got, err, tc.want)
			}
		})
	}
	if mode, err := bootstrap.Classify(bootstrap.ClassificationEvidence{HasV1ProductState: true}, bad); err == nil || mode != "" {
		t.Fatalf("v1 state with malformed release allowlist must fail closed: mode %q, err %v", mode, err)
	}
	if mode, err := bootstrap.Classify(bootstrap.ClassificationEvidence{HasV1ProductState: true}, []bootstrap.DirectUpgradeSource{{"v1.synthetic-allowed", "cpamp-v1-synthetic", 7}, {"v1.synthetic-allowed", "cpamp-v1-synthetic", 7}}); err == nil || mode != "" {
		t.Fatalf("v1 state with duplicate release allowlist must fail closed: mode %q, err %v", mode, err)
	}
}

func TestAdoptionResourceCapabilityGate(t *testing.T) {
	f, err := decodeFixture(readFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	plan := f.Paths[1].domain()
	allowlist := []bootstrap.DirectUpgradeSource{f.DirectUpgradeSources[0].domain()}
	if err := plan.Validate(allowlist); err != nil {
		t.Fatal(err)
	}
	missing := plan
	missing.AdoptionResources = append([]bootstrap.AdoptionResource(nil), plan.AdoptionResources[:3]...)
	if err := missing.Validate(allowlist); err == nil {
		t.Fatal("adoption accepted without a decision for provider runtime state")
	}
	for _, tc := range []struct {
		name  string
		clear func(*bootstrap.AdoptionResource)
	}{
		{"unreadable", func(r *bootstrap.AdoptionResource) { r.ReadEvidence = "" }},
		{"not replayable", func(r *bootstrap.AdoptionResource) { r.ReplayEvidence = "" }},
		{"not verifiable", func(r *bootstrap.AdoptionResource) { r.VerifyEvidence = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := plan.AdoptionResources[0]
			tc.clear(&item)
			got, err := bootstrap.DecideAdoptionResource(item)
			if err != nil || got != bootstrap.Unsupported {
				t.Fatalf("incomplete capability proof got %q, err %v", got, err)
			}
			changed := plan
			changed.AdoptionResources = append([]bootstrap.AdoptionResource(nil), plan.AdoptionResources...)
			changed.AdoptionResources[0] = item
			if err := changed.Validate(allowlist); err == nil {
				t.Fatal("unproven resource remained in mutations")
			}
		})
	}
	manual := plan.AdoptionResources[3]
	if got, err := bootstrap.DecideAdoptionResource(manual); err != nil || got != bootstrap.ManualAction {
		t.Fatalf("unproven provider state must require manual action: %q, %v", got, err)
	}
	manual.ManualAction = ""
	if got, err := bootstrap.DecideAdoptionResource(manual); err != nil || got != bootstrap.Unsupported {
		t.Fatalf("unproven provider state without manual remedy must be unsupported: %q, %v", got, err)
	}
	manual.Kind = "historical_usage"
	if _, err := bootstrap.DecideAdoptionResource(manual); err == nil {
		t.Fatal("historical usage accepted as an adoption resource")
	}
	for _, missing := range []string{"source_cpa_stop", "source_cpa_available_until_commit"} {
		changed := plan
		if missing == "source_cpa_stop" {
			changed.Unsupported = append([]string(nil), plan.Unsupported...)
			changed.Unsupported = slices.DeleteFunc(changed.Unsupported, func(v string) bool { return v == missing })
		} else {
			changed.Preserved = append([]string(nil), plan.Preserved...)
			changed.Preserved = slices.DeleteFunc(changed.Preserved, func(v string) bool { return v == missing })
		}
		if err := changed.Validate(allowlist); err == nil {
			t.Fatalf("adoption accepted without %s", missing)
		}
	}
}
