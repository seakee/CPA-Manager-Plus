package upgrade

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

// rotatedManagementKey is the credential a CPA answers with after its owner
// rotated the one the External connect step proved.
const rotatedManagementKey = "rotated-management-key"

// TestExternalConnectReprovesARotatedKey is the F2 regression for key rotation:
// the operation connected with K1, the CPA rotated to K2 and the Manager stores
// K2. The default repeat refuses, and --reprove proves the CPA again under the
// target fence and seals K2 in place, so the verification can complete.
func TestExternalConnectReprovesARotatedKey(t *testing.T) {
	target := newExternalTarget(t)
	first, err := target.connect(t)
	if err != nil {
		t.Fatalf("first connect: %v", err)
	}
	before := operationRow(t, target.databasePath())
	probes := target.probes
	options := target.options
	options.ManagementKey = rotatedManagementKey
	options.Reprove = true

	reproved, err := target.connectWith(t, options)
	if err != nil {
		t.Fatalf("reprove the rotated key: %v", err)
	}
	if reproved.AlreadyComplete {
		t.Fatalf("the reproof answered from the recorded connection: %+v", reproved)
	}
	if target.probes == probes {
		t.Fatal("the reproof never proved the CPA again")
	}
	if reproved.Generation != first.Generation {
		t.Fatalf("the reproof rebound the operation: %q -> %q", first.Generation, reproved.Generation)
	}
	after := operationRow(t, target.databasePath())
	if after.Stage != model.UpgradeStageExternalConnect || after.Revision <= before.Revision {
		t.Fatalf("the reproof did not record a new revision: %+v -> %+v", before, after)
	}
	if after.ErrorCode != "" || after.ErrorDetail != "" {
		t.Fatalf("the reproof left a refusal on the row: %q / %q", after.ErrorCode, after.ErrorDetail)
	}
	binding, secret := sealedBinding(t, target.databasePath())
	if secret != rotatedManagementKey || !security.IsValidProtectedEnvelope(binding.ManagementKey) {
		t.Fatalf("the reproof sealed %q (%q), want the rotated key", binding.ManagementKey, secret)
	}
	evidence, found, err := readExternalConnectEvidence(target.copied.target)
	if err != nil || !found || evidence.Identity != first.Identity {
		t.Fatalf("the reproof left the evidence stale: %+v, found %t, err %v", evidence, found, err)
	}
}

// TestExternalConnectReproofRefusesARejectedKey proves --reprove is a proof, not
// an override: a CPA that answers 401 keeps the credential the operation did
// connect with.
func TestExternalConnectReproofRefusesARejectedKey(t *testing.T) {
	target := newExternalTarget(t)
	if _, err := target.connect(t); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	before := operationRow(t, target.databasePath())
	target.probErr = cpa.ErrManagementRejected
	probes := target.probes
	options := target.options
	options.ManagementKey = rotatedManagementKey
	options.Reprove = true

	_, err := target.connectWith(t, options)
	if !errors.Is(err, ErrExternalManagementRejected) {
		t.Fatalf("a rejected reproof refused with %v", err)
	}
	if target.probes == probes {
		t.Fatal("a refused reproof never proved the CPA again")
	}
	binding, secret := sealedBinding(t, target.databasePath())
	if secret != externalTestKey {
		t.Fatalf("a refused reproof sealed %q, want the key the operation connected with", secret)
	}
	if !security.IsValidProtectedEnvelope(binding.ManagementKey) {
		t.Fatalf("a refused reproof unsealed the binding: %q", binding.ManagementKey)
	}
	if after := operationRow(t, target.databasePath()); after.Stage != before.Stage {
		t.Fatalf("a refused reproof moved the stage: %q -> %q", before.Stage, after.Stage)
	}
}

// TestExternalConnectReproofRefusesAChangedServer proves the reproof lands on the
// server the connect step observed, not merely on an address that answers: two
// CPAs can hide behind one URL, and sealing a key for the wrong one would hand
// the Manager a credential the recorded target never accepted.
func TestExternalConnectReproofRefusesAChangedServer(t *testing.T) {
	target := newExternalTarget(t)
	if _, err := target.connect(t); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	target.version = cpa.ClassifyExternalVersion("v8.0.14")
	options := target.options
	options.ManagementKey = rotatedManagementKey
	options.Reprove = true

	_, err := target.connectWith(t, options)
	if !errors.Is(err, ErrExternalTargetMoved) {
		t.Fatalf("a changed server refused with %v", err)
	}
	if !strings.Contains(err.Error(), "server fingerprint") {
		t.Fatalf("the refusal does not name what changed: %v", err)
	}
	_, secret := sealedBinding(t, target.databasePath())
	if secret != externalTestKey {
		t.Fatalf("a refused reproof sealed %q", secret)
	}
}

// TestExternalConnectReproofRequiresARecordedConnection keeps --reprove from
// becoming a first connection: with no binding there is nothing to re-prove.
func TestExternalConnectReproofRequiresARecordedConnection(t *testing.T) {
	target := newExternalTarget(t)
	options := target.options
	options.Reprove = true

	_, err := target.connectWith(t, options)
	if !errors.Is(err, ErrExternalNotApplicable) {
		t.Fatalf("--reprove without a recorded connection refused with %v", err)
	}
	if !strings.Contains(err.Error(), "recorded External connection") {
		t.Fatalf("the refusal does not say what is missing: %v", err)
	}
	if target.probes != 0 {
		t.Fatalf("a refused reproof probed the CPA %d time(s)", target.probes)
	}
}

// TestExternalConnectReproofRefusesATerminalRow proves a completed or
// rolled-back operation is never re-proved: its verdict and its target are
// frozen, and rewriting the credential under it would re-describe an operation
// that already finished.
func TestExternalConnectReproofRefusesATerminalRow(t *testing.T) {
	rolledBack := newExternalTarget(t)
	if _, err := rolledBack.connect(t); err != nil {
		t.Fatalf("connect the rolled-back fixture: %v", err)
	}
	execTarget(t, rolledBack.databasePath(),
		`update upgrade_operations set stage = 'rolled_back',
			rollback_image = 'ghcr.io/example/cpa:v1.14.4',
			rollback_image_source = 'operator',
			rollback_image_form = 'release-tag',
			rollback_status_url = 'http://127.0.0.1:8080/health',
			rollback_source_fingerprint = 'fingerprint',
			rollback_observed_at_ms = 1`)
	options := rolledBack.options
	options.ManagementKey = rotatedManagementKey
	options.Reprove = true
	if _, err := rolledBack.connectWith(t, options); !errors.Is(err, ErrExternalNotApplicable) {
		t.Fatalf("--reprove on a rolled-back row refused with %v", err)
	}
	if _, secret := sealedBinding(t, rolledBack.databasePath()); secret != externalTestKey {
		t.Fatalf("the refused reproof rewrote the sealed secret: %q", secret)
	}

	completed := newExternalVerifyFixture(t)
	if _, err := completed.verify(t); err != nil {
		t.Fatalf("complete the fixture operation: %v", err)
	}
	before := completed.operation(t)
	_, err := ExternalConnect(context.Background(), ExternalConnectOptions{
		TargetRoot:    completed.target,
		BaseURL:       verifyExternalAddress,
		ManagementKey: rotatedManagementKey,
		Reprove:       true,
	})
	if !errors.Is(err, ErrExternalNotApplicable) {
		t.Fatalf("--reprove on a completed row refused with %v", err)
	}
	if after := completed.operation(t); after.Revision != before.Revision || after.Stage != model.UpgradeStageComplete {
		t.Fatalf("the refused reproof rewrote the completed row: %+v -> %+v", before, after)
	}
}
