package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
)

const externalTestKey = "external-management-key"

// externalTarget is one invocation's inputs plus the seams a test replaces.
type externalTarget struct {
	copied    copiedVolume
	options   ExternalConnectOptions
	version   cpa.ExternalVersionReport
	probErr   error
	keys      []string
	keyErr    error
	configErr error
	modelErr  error
	probes    int
}

// newExternalTarget builds a recorded copy-in and an External connect pointing
// at a synthetic v8 CPA. Every probe is a seam, so no test reaches the network.
func newExternalTarget(t *testing.T) *externalTarget {
	t.Helper()
	copied := newCopiedVolume(t)
	target := &externalTarget{
		copied:  copied,
		version: cpa.ClassifyExternalVersion("v8.0.13"),
		keys:    []string{"client-key-1"},
	}
	target.options = ExternalConnectOptions{
		TargetRoot:    copied.target,
		BaseURL:       "https://cpa.example",
		ManagementKey: externalTestKey,
		observeExternalVersion: func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
			target.probes++
			return target.version, target.probErr
		},
		readExternalKeys: func(context.Context, string, string) (cpa.ExternalKeySnapshot, error) {
			if target.keyErr != nil {
				return cpa.ExternalKeySnapshot{}, target.keyErr
			}
			return cpa.ExternalKeySnapshot{Keys: target.keys, SourceSHA256: strings.Repeat("a", 64)}, nil
		},
		readV8Config: func(context.Context, string, string) (cpa.V8ConfigObservation, error) {
			if target.configErr != nil {
				return cpa.V8ConfigObservation{}, target.configErr
			}
			return cpa.V8ConfigObservation{BaseURL: "https://cpa.example", ClientKeyCount: len(target.keys)}, nil
		},
		proveModelSurface: func(context.Context, string, string) error { return target.modelErr },
	}
	return target
}

func (e *externalTarget) databasePath() string { return e.copied.databasePath() }

func (e *externalTarget) connect(t *testing.T) (ExternalConnectResult, error) {
	t.Helper()
	return ExternalConnect(context.Background(), e.options)
}

// connectWith runs the step with the harness's seams but the caller's inputs, so
// a test can point one recorded copy-in at another CPA.
func (e *externalTarget) connectWith(t *testing.T, options ExternalConnectOptions) (ExternalConnectResult, error) {
	t.Helper()
	if options.observeExternalVersion == nil {
		options.observeExternalVersion = e.options.observeExternalVersion
	}
	if options.readExternalKeys == nil {
		options.readExternalKeys = e.options.readExternalKeys
	}
	if options.proveModelSurface == nil {
		options.proveModelSurface = e.options.proveModelSurface
	}
	return ExternalConnect(context.Background(), options)
}

// newExternalTargetAt reconnects the same recorded copy-in to another address.
func newExternalTargetAt(t *testing.T, target *externalTarget, address string) (ExternalConnectResult, error) {
	t.Helper()
	return target.connectWith(t, ExternalConnectOptions{
		TargetRoot:    target.copied.target,
		BaseURL:       address,
		ManagementKey: externalTestKey,
	})
}

// TestExternalConnectV8RecordsIdentity proves the v8 path: the row advances to
// external_connect, both domains are marked, and the recorded generation is the
// identity a second read observes.
func TestExternalConnectV8RecordsIdentity(t *testing.T) {
	target := newExternalTarget(t)
	result, err := target.connect(t)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if result.Stage != model.UpgradeStageExternalConnect {
		t.Fatalf("stage = %q", result.Stage)
	}
	if !hasDomains(result.DomainsDone, model.UpgradeDomainCPAConfig, model.UpgradeDomainIdentity) {
		t.Fatalf("domains = %v", result.DomainsDone)
	}
	expected := freshkeyapply.ExternalIdentity("https://cpa.example")
	if result.Identity != expected || result.Generation != expected {
		t.Fatalf("identity/generation = %q/%q, want %q", result.Identity, result.Generation, expected)
	}
	if result.CPAVersion != "v8.0.13" || result.Support != cpa.VersionSupportSupported {
		t.Fatalf("version report = %+v", result)
	}
	recorded := operationRow(t, target.databasePath())
	if recorded.Stage != model.UpgradeStageExternalConnect {
		t.Fatalf("recorded stage = %q", recorded.Stage)
	}
	for _, domain := range []string{model.UpgradeDomainCPAConfig, model.UpgradeDomainIdentity} {
		if !recorded.HasDomain(domain) {
			t.Fatalf("recorded domains = %v, missing %s", recorded.DomainsDone, domain)
		}
	}
	// The copy-in provenance the operation already carried survives the step.
	if recorded.SourceKind != SourceKindUpgradedLegacy || recorded.SourceFingerprintAfter == "" {
		t.Fatalf("copy-in provenance = %+v", recorded)
	}
	evidence, found, err := readExternalConnectEvidence(target.copied.target)
	if err != nil || !found {
		t.Fatalf("evidence = %+v, found %t, err %v", evidence, found, err)
	}
	if evidence.Identity != expected || evidence.Generation != expected {
		t.Fatalf("recorded evidence = %+v", evidence)
	}
}

// TestExternalConnectV7Stops proves the stop: a v7 CPA writes no connection, the
// row keeps its stage, and the refusal names the version and the action.
func TestExternalConnectV7Stops(t *testing.T) {
	target := newExternalTarget(t)
	target.version = cpa.ClassifyExternalVersion("v7.2.1")
	_, err := target.connect(t)
	if !errors.Is(err, ErrExternalVersionUnsupported) {
		t.Fatalf("err = %v, want the typed version refusal", err)
	}
	message := err.Error()
	for _, want := range []string{"v7.2.1", "v8", "upgrade the CPA"} {
		if !strings.Contains(message, want) {
			t.Fatalf("refusal %q does not name %q", message, want)
		}
	}
	recorded := operationRow(t, target.databasePath())
	if recorded.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("stage advanced to %q", recorded.Stage)
	}
	if recorded.HasDomain(model.UpgradeDomainCPAConfig) || recorded.HasDomain(model.UpgradeDomainIdentity) {
		t.Fatalf("a v7 stop marked domains: %v", recorded.DomainsDone)
	}
	if recorded.ErrorCode != string(UpgradeErrorExternalVersionUnsupported) {
		t.Fatalf("recorded error code = %q", recorded.ErrorCode)
	}
	if !strings.Contains(recorded.ErrorDetail, "v7.2.1") {
		t.Fatalf("recorded detail = %q", recorded.ErrorDetail)
	}
	if _, found, err := readExternalConnectEvidence(target.copied.target); err != nil || found {
		t.Fatalf("a v7 stop recorded evidence: found %t, err %v", found, err)
	}
}

// TestExternalConnectRejectedKeyIsNotAVersionProblem proves the mapping: a 401 or
// the 403 a ban answers is the management-key code, never the version code.
func TestExternalConnectRejectedKeyIsNotAVersionProblem(t *testing.T) {
	for name, probeErr := range map[string]error{
		"rejected":  cpa.ErrManagementRejected,
		"forbidden": cpa.ErrManagementClientForbidden,
	} {
		t.Run(name, func(t *testing.T) {
			target := newExternalTarget(t)
			target.probErr = probeErr
			_, err := target.connect(t)
			if !errors.Is(err, ErrExternalManagementRejected) {
				t.Fatalf("err = %v, want the management-key refusal", err)
			}
			if errors.Is(err, ErrExternalVersionUnsupported) {
				t.Fatalf("a rejected key was reported as a version problem: %v", err)
			}
			recorded := operationRow(t, target.databasePath())
			if recorded.Stage != model.UpgradeStageCopyIn {
				t.Fatalf("stage advanced to %q", recorded.Stage)
			}
			if recorded.ErrorCode != string(UpgradeErrorExternalManagementRejected) {
				t.Fatalf("recorded error code = %q", recorded.ErrorCode)
			}
		})
	}
}

// TestExternalConnectMovedTarget proves a recorded target that is not this
// address is refused instead of reconnected.
func TestExternalConnectMovedTarget(t *testing.T) {
	target := newExternalTarget(t)
	if _, err := target.connect(t); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	// Point the same operation at another CPA: the recorded identity no longer
	// names it.
	target.options.BaseURL = "https://other.example"
	_, err := target.connect(t)
	if !errors.Is(err, ErrExternalTargetMoved) {
		t.Fatalf("err = %v, want the moved-target refusal", err)
	}
	recorded := operationRow(t, target.databasePath())
	if recorded.Stage != model.UpgradeStageExternalConnect {
		t.Fatalf("stage = %q", recorded.Stage)
	}
	if recorded.ErrorCode != string(UpgradeErrorExternalTargetMoved) {
		t.Fatalf("recorded error code = %q", recorded.ErrorCode)
	}
	evidence, _, err := readExternalConnectEvidence(target.copied.target)
	if err != nil {
		t.Fatalf("read evidence: %v", err)
	}
	if evidence.Identity != freshkeyapply.ExternalIdentity("https://cpa.example") {
		t.Fatalf("the moved-target refusal rewrote the evidence: %+v", evidence)
	}
}

// TestExternalConnectIsIdempotent proves a repeat against the same target
// records nothing new: no second probe, no generation churn, same update time.
func TestExternalConnectIsIdempotent(t *testing.T) {
	target := newExternalTarget(t)
	first, err := target.connect(t)
	if err != nil {
		t.Fatalf("first connect: %v", err)
	}
	before := operationRow(t, target.databasePath())
	probes := target.probes
	second, err := target.connect(t)
	if err != nil {
		t.Fatalf("second connect: %v", err)
	}
	if !second.AlreadyComplete {
		t.Fatalf("repeat reported a fresh connection: %+v", second)
	}
	// The report is compared against the row, not against the first report: the
	// first result carries the stamp of the transaction that recorded it, while
	// reading the same row back can observe a later stamp from the refusal clear
	// that commits in that transaction. What must not happen is a repeat that
	// rewrites the row, so the durable value is the one to compare.
	after := operationRow(t, target.databasePath())
	if second.Generation != first.Generation || second.UpdatedAtMS != after.UpdatedAtMS {
		t.Fatalf("repeat churned the record: %+v vs %+v (row %+v)", second, first, after)
	}
	if target.probes != probes {
		t.Fatalf("repeat probed the CPA %d more time(s)", target.probes-probes)
	}
	if after.UpdatedAtMS != before.UpdatedAtMS || len(after.DomainsDone) != len(before.DomainsDone) {
		t.Fatalf("repeat rewrote the row: %+v vs %+v", after, before)
	}
}

// TestExternalConnectRefusesTheWrongStage proves the step cannot run from a stage
// its result could never be recorded in, and that a root without a copy-in is
// refused before any probe.
func TestExternalConnectRefusesTheWrongStage(t *testing.T) {
	target := newExternalTarget(t)
	if _, err := target.connect(t); err != nil {
		t.Fatalf("connect: %v", err)
	}
	// A rolled-back operation abandons the forward order, so the recorded row is
	// rewritten as the terminal verdict it would carry.
	execTarget(t, target.databasePath(),
		`update upgrade_operations set stage = 'rolled_back',
			rollback_image = 'ghcr.io/example/cpa:v1.14.4',
			rollback_status_url = 'http://127.0.0.1:8080/health',
			rollback_source_fingerprint = 'fingerprint',
			rollback_observed_at_ms = 1`)
	_, err := target.connect(t)
	if !errors.Is(err, ErrExternalNotApplicable) {
		t.Fatalf("a rolled-back operation refused with %v", err)
	}
	if _, found, evidenceErr := readExternalConnectEvidence(target.copied.target); evidenceErr != nil || !found {
		t.Fatalf("the refusal rewrote the evidence: found %t, err %v", found, evidenceErr)
	}
	bare := canonicalTempDir(t)
	_, err = ExternalConnect(context.Background(), ExternalConnectOptions{
		TargetRoot:    bare,
		BaseURL:       "https://cpa.example",
		ManagementKey: externalTestKey,
	})
	if !errors.Is(err, ErrExternalNotApplicable) {
		t.Fatalf("a root without a copy-in refused with %v", err)
	}
}

// TestExternalConnectModelSurfaceGap proves a target whose gateway does not
// answer is not connected.
func TestExternalConnectModelSurfaceGap(t *testing.T) {
	target := newExternalTarget(t)
	target.modelErr = cpa.ErrProofUnavailable
	_, err := target.connect(t)
	if !errors.Is(err, ErrExternalAuthorityUnavailable) {
		t.Fatalf("err = %v", err)
	}
	recorded := operationRow(t, target.databasePath())
	if recorded.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("stage advanced to %q", recorded.Stage)
	}
	if _, found, err := readExternalConnectEvidence(target.copied.target); err != nil || found {
		t.Fatalf("a model-surface gap recorded evidence: found %t, err %v", found, err)
	}
	if _, err := os.Stat(copyinmarkerDirectory(target.copied.target)); err != nil && !os.IsNotExist(err) {
		t.Fatalf("stat work directory: %v", err)
	}
}

// TestExternalConnectDeclaredVersion proves the declared version is consulted
// only when the CPA answers without one.
func TestExternalConnectDeclaredVersion(t *testing.T) {
	// A CPA that answers no version cannot be trusted to be new enough.
	target := newExternalTarget(t)
	target.version = cpa.ClassifyExternalVersion("")
	_, err := target.connect(t)
	if !errors.Is(err, ErrExternalVersionUnsupported) {
		t.Fatalf("err = %v, want the version refusal", err)
	}
	if !strings.Contains(err.Error(), externalConnectUnknownVersion) {
		t.Fatalf("refusal %q does not name the unknown version", err)
	}

	// The operator's declaration is the only thing that can classify it, and a
	// v8 declaration lets the step proceed.
	declared := newExternalTarget(t)
	declared.version = cpa.ClassifyExternalVersion("")
	declared.options.CPAVersion = "v8.0.3"
	if _, err := declared.connect(t); err != nil {
		t.Fatalf("connect with a declared v8 = %v", err)
	}

	// A v7 declaration stops, and it names the version the operator declared.
	v7 := newExternalTarget(t)
	v7.version = cpa.ClassifyExternalVersion("")
	v7.options.CPAVersion = "v7.9.9"
	if _, err := v7.connect(t); !errors.Is(err, ErrExternalVersionUnsupported) {
		t.Fatalf("err = %v, want the version refusal", err)
	} else if !strings.Contains(err.Error(), "v7.9.9") {
		t.Fatalf("refusal %q does not name the declared version", err)
	}
}

func copyinmarkerDirectory(targetRoot string) string {
	return filepath.Join(targetRoot, ".upgrade-copy-in")
}

// hasDomains reports whether every named domain is recorded.
func hasDomains(domains []string, want ...string) bool {
	for _, expected := range want {
		found := false
		for _, domain := range domains {
			if domain == expected {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
