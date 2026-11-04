package upgrade

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
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
	// Rotating the management key or client keys on the External CPA rewrites
	// its config.yaml, changing SourceSHA256 while the canonical URL and CPA
	// build version stay the same.
	target.configSHA256 = strings.Repeat("b", 64)
	target.keys = []string{"client-key-1", "client-key-2"}
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
	wantFingerprint := freshkeyapply.ExternalServerFingerprint("v8.0.13", strings.Repeat("b", 64))
	binding, secret := sealedBinding(t, target.databasePath())
	if secret != rotatedManagementKey || !security.IsValidProtectedEnvelope(binding.ManagementKey) {
		t.Fatalf("the reproof sealed %q (%q), want the rotated key", binding.ManagementKey, secret)
	}
	if binding.ServerFingerprint != wantFingerprint || binding.ClientKeys != 2 {
		t.Fatalf("the reproof did not refresh the binding config snapshot: %+v, want fingerprint %s and 2 keys", binding, wantFingerprint)
	}
	evidence, found, err := readExternalConnectEvidence(target.copied.target)
	if err != nil || !found || evidence.Identity != first.Identity || evidence.ServerFingerprint != wantFingerprint || evidence.ClientKeys != 2 {
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

// TestExternalConnectReproofTargetIdentityAndObservedVersion locks down the
// N4/N5 target-identity and version-observation contract for existing bindings:
//   - A changed server-advertised CPAVersion behind the recorded URL is refused
//     with ErrExternalTargetMoved even when --reprove is set.
//   - A missing server version header on an existing binding is refused with
//     ErrExternalTargetMoved even when the caller supplies a matching
//     --cpa-version declaration (N5: declared version never substitutes for a
//     live version header during re-proof or backfill).
//   - Because CPA v8 exposes no instance identifier separate from BaseURL,
//     advertised CPAVersion and config.yaml, a live-probing call without
//     --reprove (such as an unsealed-key backfill or a failed-stage resume)
//     requires the exact recorded ServerFingerprint, whereas an explicit
//     --reprove authorizes updating the binding in place for the v8 endpoint
//     answering the recorded canonical URL with the recorded CPAVersion header
//     (N4). (A default repeat at stage=external_connect with a sealed key is a
//     local idempotent credential check and does not issue live probes.)
func TestExternalConnectReproofTargetIdentityAndObservedVersion(t *testing.T) {
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
		t.Fatalf("a changed CPA build version refused with %v", err)
	}
	if !strings.Contains(err.Error(), "version") {
		t.Fatalf("the refusal does not name the changed CPA build version: %v", err)
	}
	_, secret := sealedBinding(t, target.databasePath())
	if secret != externalTestKey {
		t.Fatalf("a refused reproof sealed %q", secret)
	}

	// N5: when the server behind the recorded URL omits the version header,
	// passing a matching --cpa-version declaration must not satisfy the
	// recorded build-version check on --reprove or on an unsealed backfill.
	missingHeaderTarget := newExternalTarget(t)
	if _, err := missingHeaderTarget.connect(t); err != nil {
		t.Fatalf("first connect before missing-header reproof: %v", err)
	}
	beforeMissing := operationRow(t, missingHeaderTarget.databasePath())
	missingHeaderTarget.version = cpa.ClassifyExternalVersion("")
	missingHeaderTarget.configSHA256 = strings.Repeat("b", 64)
	missingOptions := missingHeaderTarget.options
	missingOptions.ManagementKey = rotatedManagementKey
	missingOptions.CPAVersion = "v8.0.13"
	missingOptions.Reprove = true
	_, missingErr := missingHeaderTarget.connectWith(t, missingOptions)
	if !errors.Is(missingErr, ErrExternalTargetMoved) {
		t.Fatalf("reproof without a live version header and with matching --cpa-version = %v, want ErrExternalTargetMoved", missingErr)
	}
	if !strings.Contains(missingErr.Error(), "--cpa-version") {
		t.Fatalf("missing-header reproof refusal %q does not explain that --cpa-version cannot substitute for a live header", missingErr)
	}
	bindingAfterMissing, secretAfterMissing := sealedBinding(t, missingHeaderTarget.databasePath())
	if secretAfterMissing != externalTestKey || bindingAfterMissing.ServerFingerprint != freshkeyapply.ExternalServerFingerprint("v8.0.13", strings.Repeat("a", 64)) {
		t.Fatalf("missing-header reproof mutated the binding: %+v (secret %q)", bindingAfterMissing, secretAfterMissing)
	}
	if afterMissing := operationRow(t, missingHeaderTarget.databasePath()); afterMissing.Stage != beforeMissing.Stage {
		t.Fatalf("missing-header reproof moved the stage: %q -> %q", beforeMissing.Stage, afterMissing.Stage)
	}

	// N4: an implicit unsealed-key backfill (without --reprove) against a same-URL,
	// same-CPAVersion endpoint whose config.yaml changed is refused and tells the
	// operator to pass --reprove.
	backfillTarget := newExternalTarget(t)
	if _, err := backfillTarget.connect(t); err != nil {
		t.Fatalf("first connect for backfill: %v", err)
	}
	execTarget(t, backfillTarget.databasePath(), `update upgrade_external_connect set management_key = '' where id = 1`)
	backfillTarget.configSHA256 = strings.Repeat("b", 64)
	_, backfillErr := backfillTarget.connect(t)
	if !errors.Is(backfillErr, ErrExternalTargetMoved) || !strings.Contains(backfillErr.Error(), "--reprove") {
		t.Fatalf("implicit backfill with changed config.yaml = %v, want ErrExternalTargetMoved naming --reprove", backfillErr)
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

// TestExternalConnectFailedRecoveryRequiresReproveToRotateKey is the M1
// regression: a row parked at stage=failed with last_forward_stage=external_connect
// and an already-sealed binding may resume with the same key, must refuse a
// different key without --reprove before probing or rewriting the binding, may
// rotate its key when --reprove is explicitly set, and must refuse before
// probing when last_forward_stage has already advanced to verify.
func TestExternalConnectFailedRecoveryRequiresReproveToRotateKey(t *testing.T) {
	target := newExternalTarget(t)
	if _, err := target.connect(t); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	execTarget(t, target.databasePath(),
		`update upgrade_operations set stage = 'failed', last_forward_stage = 'external_connect',
			error_code = 'upgrade_external_authority_unavailable', error_detail = 'transient outage' where id = 1`)
	before := operationRow(t, target.databasePath())
	probesBefore := target.probes

	// 1. Supplying a different key without --reprove on a failed row must be
	// refused without probing the CPA or replacing the sealed key.
	unapproved := target.options
	unapproved.ManagementKey = rotatedManagementKey
	_, err := target.connectWith(t, unapproved)
	if !errors.Is(err, ErrExternalManagementRejected) || !strings.Contains(err.Error(), "--reprove") {
		t.Fatalf("failed-row key rotation without --reprove = %v, want ErrExternalManagementRejected naming --reprove", err)
	}
	if target.probes != probesBefore {
		t.Fatalf("unapproved key rotation on failed row probed the CPA %d -> %d", probesBefore, target.probes)
	}
	if _, secret := sealedBinding(t, target.databasePath()); secret != externalTestKey {
		t.Fatalf("unapproved key rotation on failed row overwrote sealed key with %q", secret)
	}
	if after := operationRow(t, target.databasePath()); after.Revision != before.Revision || after.Stage != model.UpgradeStageFailed {
		t.Fatalf("unapproved key rotation on failed row mutated operation: %+v -> %+v", before, after)
	}

	// 2. Resuming the failed row with the same key re-proves the target and
	// advances back to external_connect.
	resumed, err := target.connect(t)
	if err != nil {
		t.Fatalf("resume failed row with original key: %v", err)
	}
	if resumed.AlreadyComplete || resumed.Stage != model.UpgradeStageExternalConnect {
		t.Fatalf("resumed result = %+v, want live recovery to external_connect", resumed)
	}

	// 3. Parking at failed again and passing --reprove with the rotated key
	// re-proves the target, seals the rotated key, and recovers to external_connect.
	execTarget(t, target.databasePath(),
		`update upgrade_operations set stage = 'failed', last_forward_stage = 'external_connect',
			error_code = 'upgrade_external_management_rejected', error_detail = 'key rotated' where id = 1`)
	target.configSHA256 = strings.Repeat("b", 64)
	approved := target.options
	approved.ManagementKey = rotatedManagementKey
	approved.Reprove = true
	reproved, err := target.connectWith(t, approved)
	if err != nil {
		t.Fatalf("reprove failed row with rotated key: %v", err)
	}
	if reproved.Stage != model.UpgradeStageExternalConnect {
		t.Fatalf("reproved stage = %q, want external_connect", reproved.Stage)
	}
	if _, secret := sealedBinding(t, target.databasePath()); secret != rotatedManagementKey {
		t.Fatalf("reproved failed row sealed %q, want %q", secret, rotatedManagementKey)
	}

	// 4. A failed row whose high-water mark is already verify cannot re-enter
	// external_connect and must be refused before any network probe runs.
	execTarget(t, target.databasePath(),
		`update upgrade_operations set stage = 'failed', last_forward_stage = 'verify',
			error_code = 'upgrade_verify_connection_unproven', error_detail = 'verify failed' where id = 1`)
	probesBeforeVerifyGuard := target.probes
	if _, err := target.connectWith(t, approved); !errors.Is(err, ErrExternalNotApplicable) {
		t.Fatalf("failed row at high-water verify refused with %v, want ErrExternalNotApplicable", err)
	}
	if target.probes != probesBeforeVerifyGuard {
		t.Fatalf("failed row at high-water verify probed the CPA %d -> %d", probesBeforeVerifyGuard, target.probes)
	}
}
