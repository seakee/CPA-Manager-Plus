package bootstrap_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

const phase401FixtureDir = "../../../../../tests/fixtures/phase4-bootstrap"

type phase401CandidateJSON struct {
	Release      string `json:"release"`
	SourceCommit string `json:"sourceCommit"`
}

type phase401ExpectedJSON struct {
	Accepted                bool                            `json:"accepted"`
	Mode                    bootstrap.Mode                  `json:"mode"`
	Reason                  bootstrap.SourceAdmissionReason `json:"reason"`
	Qualification           bootstrap.QualificationState    `json:"qualification"`
	SourceIdentityPreserved bool                            `json:"sourceIdentityPreserved"`
}

type phase401AdmissionJSON struct {
	ID                   string                       `json:"id"`
	Case                 bootstrap.SourceAdmissionCase `json:"case"`
	Shape                bootstrap.SourceShape         `json:"shape"`
	Provenance           bootstrap.SourceEvidenceStatus `json:"provenance"`
	RequestedTarget      phase401CandidateJSON         `json:"requestedTarget"`
	ObservedSourceCommit string                       `json:"observedSourceCommit"`
	Expected             phase401ExpectedJSON         `json:"expected"`
}

type phase401Fixture struct {
	Schema                         string                    `json:"$schema"`
	ContractVersion                string                    `json:"contractVersion"`
	ProductBaseline                string                    `json:"productBaseline"`
	ProductionUpgradeEnabled       bool                      `json:"productionUpgradeEnabled"`
	FixedBootstrapDiscoveryPath    string                    `json:"fixedBootstrapDiscoveryPath"`
	CompletedDiscoveryTerminal     string                    `json:"completedDiscoveryTerminal"`
	UnauthenticatedDiscoveryFields []string                  `json:"unauthenticatedDiscoveryFields"`
	ForbiddenDiscoveryFields       []string                  `json:"forbiddenDiscoveryFields"`
	AdmissionCases                 []phase401AdmissionJSON   `json:"admissionCases"`
	IntegrationRequirements        []string                  `json:"integrationRequirements"`
}

func readPhase401Fixture(t *testing.T) phase401Fixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(phase401FixtureDir, "phase4-01-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := decodePhase401Fixture(data)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func decodePhase401Fixture(data []byte) (phase401Fixture, error) {
	var fixture phase401Fixture
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixture); err != nil {
		return fixture, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fixture, io.ErrUnexpectedEOF
		}
		return fixture, err
	}
	return fixture, nil
}

func TestPhase401FixtureAndSchemaStayNarrow(t *testing.T) {
	fixture := readPhase401Fixture(t)
	if fixture.Schema != "./phase4-01-contract.schema.json" ||
		fixture.ContractVersion != "cpamp.phase4-01.contract.v1" ||
		fixture.ProductBaseline != "c0a4418d02a18247630cec03365637cce0eaf639" ||
		fixture.ProductionUpgradeEnabled ||
		fixture.FixedBootstrapDiscoveryPath != bootstrap.FixedBootstrapDiscoveryPath ||
		fixture.CompletedDiscoveryTerminal != "generic_404" {
		t.Fatal("phase4-01 fixture header drifted")
	}
	for _, field := range []string{"controlBasePath", "sourceCPAUrl", "cpaManagementKey", "adminKey", "dbPath", "archivePath", "backupReceipt", "resourceRefs", "mutations", "internalPorts", "filesystemPaths", "rawVerifierError"} {
		if !contains(fixture.ForbiddenDiscoveryFields, field) {
			t.Fatalf("sensitive discovery field %q is not forbidden", field)
		}
	}
	if len(fixture.AdmissionCases) != 2 {
		t.Fatalf("unexpected source-admission contract count: %d", len(fixture.AdmissionCases))
	}

	schemaData, err := os.ReadFile(filepath.Join(phase401FixtureDir, "phase4-01-contract.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		AdditionalProperties bool                       `json:"additionalProperties"`
		Required             []string                   `json:"required"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schemaData, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.AdditionalProperties {
		t.Fatal("phase4-01 schema is not closed")
	}
	for _, key := range []string{"contractVersion", "fixedBootstrapDiscoveryPath", "admissionCases", "integrationRequirements"} {
		if _, ok := schema.Properties[key]; !ok || !contains(schema.Required, key) {
			t.Fatalf("schema does not require %q", key)
		}
	}

	unknown := bytes.Replace(dataForTest(t), []byte(`"contractVersion": "cpamp.phase4-01.contract.v1",`), []byte(`"contractVersion": "cpamp.phase4-01.contract.v1", "unexpected": true,`), 1)
	if _, err := decodePhase401Fixture(unknown); err == nil {
		t.Fatal("strict fixture decoder accepted an unknown field")
	}
}

func dataForTest(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(phase401FixtureDir, "phase4-01-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPublicDiscoveryAndStatusAxesFailClosed(t *testing.T) {
	mode := bootstrap.Fresh
	for _, value := range []bootstrap.PublicDiscovery{
		{ContractVersion: bootstrap.PublicDiscoveryContractVersion, Status: bootstrap.PublicFreshClaimRequired, Mode: &mode, AuthenticationRequired: true, AuthEntry: bootstrap.AuthEntryFreshClaim, Reason: bootstrap.ReasonFreshClaimRequired},
		{ContractVersion: bootstrap.PublicDiscoveryContractVersion, Status: bootstrap.PublicRecoveryRequired, Mode: modePtr(bootstrap.Recovery), AuthenticationRequired: false, AuthEntry: bootstrap.AuthEntryHostIntervention, Reason: bootstrap.ReasonSourceOwnershipUnavailable},
		{ContractVersion: bootstrap.PublicDiscoveryContractVersion, Status: bootstrap.PublicUnavailable, AuthEntry: bootstrap.AuthEntryUnavailable, Reason: bootstrap.ReasonObservationUnavailable},
	} {
		if err := value.Validate(); err != nil {
			t.Fatalf("valid public discovery rejected: %v", err)
		}
	}
	for _, value := range []bootstrap.PublicDiscovery{
		{ContractVersion: bootstrap.PublicDiscoveryContractVersion, Status: bootstrap.PublicUnsupportedUpgrade, Mode: modePtr(bootstrap.Upgrade), AuthenticationRequired: true, AuthEntry: bootstrap.AuthEntryAdminSession, Reason: bootstrap.ReasonQualificationUnavailable},
		{ContractVersion: bootstrap.PublicDiscoveryContractVersion, Status: bootstrap.PublicRecoveryRequired, Mode: modePtr(bootstrap.Completed), AuthEntry: bootstrap.AuthEntryHostIntervention, Reason: bootstrap.ReasonRecoveryHostIntervention},
		{ContractVersion: bootstrap.PublicDiscoveryContractVersion, Status: bootstrap.PublicRecoveryRequired, Mode: modePtr(bootstrap.Recovery), AuthenticationRequired: true, AuthEntry: bootstrap.AuthEntryHostIntervention, Reason: bootstrap.ReasonSourceOwnershipUnavailable},
		{ContractVersion: bootstrap.PublicDiscoveryContractVersion, Status: bootstrap.PublicUnsupportedUpgrade, Mode: modePtr(bootstrap.UnsupportedUpgrade), AuthenticationRequired: true, AuthEntry: bootstrap.AuthEntryAdminSession, Reason: bootstrap.ReasonSourceOwnershipUnavailable},
		{ContractVersion: bootstrap.PublicDiscoveryContractVersion, Status: bootstrap.PublicUnavailable, Mode: &mode, AuthEntry: bootstrap.AuthEntryUnavailable, Reason: bootstrap.ReasonObservationUnavailable},
	} {
		if err := value.Validate(); err == nil {
			t.Fatal("invalid public discovery accepted")
		}
	}
	for _, value := range []bootstrap.StatusSnapshot{
		{Mode: bootstrap.Fresh, AdminCredential: bootstrap.AdminCredentialUninitialized, Session: bootstrap.SessionMissing},
		{Mode: bootstrap.Upgrade, AdminCredential: bootstrap.AdminCredentialInitialized, Session: bootstrap.SessionExpired},
		{Mode: bootstrap.Recovery, AdminCredential: bootstrap.AdminCredentialUnverifiable, Session: bootstrap.SessionMissing},
		{Mode: bootstrap.Completed, AdminCredential: bootstrap.AdminCredentialInitialized, Session: bootstrap.SessionMissing},
	} {
		if err := value.Validate(); err != nil {
			t.Fatalf("orthogonal status rejected: %+v: %v", value, err)
		}
	}
}

func TestLegacyBootstrapStatusIsEvidenceOnly(t *testing.T) {
	fresh := bootstrap.ClassificationEvidence{
		HasV1ProductState: false,
		AdminCredential:   bootstrap.AdminCredentialUninitialized,
		Session:           bootstrap.SessionMissing,
	}
	for _, legacyStatus := range []string{"fresh", "ready"} {
		mode, err := bootstrap.ClassifyLegacyEvidence(bootstrap.LegacyBootstrapEvidence{LegacyStatus: legacyStatus, Classification: fresh})
		if err != nil || mode != bootstrap.Fresh {
			t.Fatalf("legacy status %q changed Fresh evidence: mode=%s err=%v", legacyStatus, mode, err)
		}
	}
	legacy := bootstrap.ClassificationEvidence{
		HasV1ProductState: true,
		AdminCredential:   bootstrap.AdminCredentialInitialized,
		Session:           bootstrap.SessionMissing,
	}
	mode, err := bootstrap.ClassifyLegacyEvidence(bootstrap.LegacyBootstrapEvidence{LegacyStatus: "ready", Classification: legacy})
	if err != nil || mode != bootstrap.UnsupportedUpgrade {
		t.Fatalf("legacy ready became authority: mode=%s err=%v", mode, err)
	}
}

func TestAUT09AndAUT10RemainPendingAndFailClosed(t *testing.T) {
	fixture := readPhase401Fixture(t)
	for _, item := range fixture.AdmissionCases {
		attempt := bootstrap.SourceAdmissionAttempt{
			Case: item.Case, Shape: item.Shape, Provenance: item.Provenance,
			RequestedTarget: bootstrap.PlannedCandidate{Release: item.RequestedTarget.Release, SourceCommit: item.RequestedTarget.SourceCommit},
			ObservedSourceCommit: item.ObservedSourceCommit,
		}
		decision, err := attempt.Evaluate()
		if err != nil {
			t.Fatalf("%s evaluation failed: %v", item.ID, err)
		}
		if err := decision.Validate(); err != nil {
			t.Fatalf("%s decision invalid: %v", item.ID, err)
		}
		if decision.Accepted || decision.Mode != bootstrap.UnsupportedUpgrade ||
			decision.Qualification != bootstrap.QualificationPending ||
			!decision.SourceIdentityPreserved ||
			decision.Reason != item.Expected.Reason {
			t.Fatalf("%s was promoted or rewritten: %+v", item.ID, decision)
		}
	}
}

func modePtr(value bootstrap.Mode) *bootstrap.Mode { return &value }

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
