package upgrade

// This file is the Managed CPA import: the Upgrade step that prepares the v2
// destination for a 1.x stack's CPA state while holding the source's ownership
// marker.
//
// # The shape of the step
//
// The four gates the destination is built from are pinned by hashes rather than
// by review (see the lane contract), so this step adds files and calls exported
// functions. It:
//
//   - reads the source through cpaManagedSource, the file-backed
//     freshadopt.Source of cpasource.go;
//   - stages and starts the destination through the Runtime client's existing
//     StageDestination and Start capabilities, with the plugin choice the plan
//     carries;
//   - binds the caller's destination URL to that staged destination with an
//     authenticated read of the client key list, before anything else addresses
//     it;
//   - inventories the source through the existing freshadopt.Executor under the
//     compiled adoption authority, so the credential files, the gateway routing
//     configuration and the provider runtime state are copied into the destination
//     and read back; anything the rules do not mark migrate stays a manual item;
//   - hands the Manager's own connection the destination URL and the secret the
//     destination was staged with, and records the `cpa_config` domain.
//
// # The one authority
//
// The executor copies exactly what a qualification record authorized, and the
// Upgrade surface holds no record of its own. The previous version of this step
// invented an Upgrade authorization that named the references to copy and ran the
// executor under it, which made a bare reference set the authority for unproven
// migration; the version after it passed no decision at all and copied nothing.
// This step now passes `freshadopt.NewAuthorization(...)` built from the compiled
// consumers `freshstrategy.VerifyCompiledUpgradeAdoption` proves: the accepted
// `CPA8-STAGE-r1` staging transition and the accepted `CPA8-ADOPT-RESOURCE-r1`
// replay chain on the same v8 artifact. A build whose record does not prove both
// refuses with upgrade_cpa_import_unproven before the destination is staged, and
// the executor is only built once that authorization reports itself verified —
// the decision slice alone is not the proof that produced it.
//
// # What is never inherited
//
// The destination keeps v2's own management secret, listener, plugin root, auth
// directory and client key list. This step writes the operation row, the
// destination attempt, the Manager's connection and the copied source state; it
// never writes the destination's identity.
//
// # Fail-closed rules
//
//   - the 1.x ownership marker is required, must belong to the recorded source,
//     and is held for the whole read path through a read-only descriptor;
//   - the version gate runs before anything is written;
//   - an absent plugin choice refuses the run — it is never defaulted;
//   - the Runtime's active artifact must be the one the compiled record was
//     accepted for, so the copy cannot land on a destination no chain described;
//   - a destination URL that does not prove itself as the staged destination is
//     refused before the executor reads or writes anything;
//   - the server fingerprint is re-observed after the executor's writes settled,
//     because the configuration the destination serves is then a different
//     document than the one proved before the copy;
//   - an unknown source key or provider section becomes a manual item with its
//     rule's own action, never a silent drop;
//   - the `cpa_config` domain is recorded only after the executor settled every
//     item, so a failed or partial copy never claims the destination holds it.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshadopt"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
)

// cpaImportManualItemLimit bounds one manual item line in the operation row.
const cpaImportManualItemLimit = 400

// qualifiedUpgradeAdoption is the authorization this step copies under, rebuilt
// from the compiled record on every run.
//
// Both halves are required: the decisions say what may be copied, and the
// verification says the record that produced them is the one this binary carries
// and still accepts. A refusal is typed as upgrade_cpa_import_unproven and is
// raised before the destination is staged, so a build without the proof writes
// nothing at all.
func qualifiedUpgradeAdoption() (freshadopt.Authorization, error) {
	decisions, err := freshstrategy.QualifiedUpgradeAdoptionDecisions()
	if err != nil {
		return freshadopt.Authorization{}, newUpgradeError(UpgradeErrorCPAImportUnproven,
			"this build carries no accepted CPA adoption authority for an Upgrade-derived source, so the import did not copy provider configuration or credentials; nothing was staged or written", err)
	}
	auth, err := freshadopt.NewAuthorization(decisions, freshstrategy.VerifyCompiledUpgradeAdoption)
	if err != nil {
		return freshadopt.Authorization{}, newUpgradeError(UpgradeErrorCPAImportUnproven,
			"the compiled CPA adoption authority this build carries does not permit the copy the import performs, so nothing was staged or written", err)
	}
	return auth, nil
}

// CPAImportOptions is the input of one Managed CPA import. The three arms of the
// run — the source and its ownership marker, the destination and the choice —
// are explicit, because none of them has a correct default.
type CPAImportOptions struct {
	// LegacyRoot is the 1.x data directory the copy-in read. It locates the
	// managed CPA directory beside it and must already hold a recorded copy-in.
	LegacyRoot string
	// SourceLock is the held 1.x ownership marker of LegacyRoot. The step reads
	// the CPA tree and writes the destination, so it requires the same fence the
	// copy-in took and keeps it for the whole run: a held marker refuses before
	// the CPA directory is touched. It never substitutes for stopping the CPA
	// container, which this marker cannot observe.
	SourceLock *processlock.Lock
	// CPADirectory overrides the managed CPA directory. Empty uses the sibling
	// `legacy-cpa` directory of LegacyRoot.
	CPADirectory string
	// CPAAuthDirectory overrides the credential directory. Empty uses the
	// source's configured auth directory inside the CPA tree, then the sibling
	// `auths` directory.
	CPAAuthDirectory string
	// CPAVersion is the operator's statement of the source CPA release. Empty
	// makes the gate read the stack file; neither input is ever inferred from the
	// configuration's shape.
	CPAVersion string
	// TargetRoot is the v2 data directory that holds the operation row.
	TargetRoot string
	// Runtime observes and stages the Manager-owned destination CPA.
	Runtime CPAImportRuntime
	// DestinationURL is the destination CPA's base URL, as the Runtime serves it.
	DestinationURL string
	// DestinationManagementKey is the destination's management secret when the
	// destination is already running. A run that finds a recorded attempt must
	// supply the same secret or none at all: the Supervisor replays the journalled
	// request, so a different secret would be a different request and is refused
	// with upgrade_cpa_destination_request_changed instead of being adopted. A run
	// that stages the destination itself uses it for the stage when supplied, and
	// otherwise generates one and records it in the Manager store before staging,
	// so the run that has to retry against a ready destination finds that same
	// secret. A run that finds the destination ready with neither refuses: the
	// import never invents a second secret for a destination it did not stage.
	DestinationManagementKey string
	// PluginsEnabled is v2's explicit "enable plugin runtime" choice. It is
	// never inherited from the source and has no default: a run that does not say
	// so is refused.
	PluginsEnabled *bool
}

// CPAImportRuntime is the Runtime capability set this step uses. It is the subset
// of runtime.RuntimeClient and runtime.DestinationClient the import needs, so a
// test substitutes exactly the calls the step makes.
type CPAImportRuntime interface {
	Status(ctx context.Context) (model.RuntimeObservedStatus, error)
	StageDestination(ctx context.Context, request model.RuntimeDestinationRequest) (model.RuntimeOperationResult, error)
	Start(ctx context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error)
}

// CPAImportManualItem is one source item the import could not copy. It is
// reported to the operator and recorded, never applied.
type CPAImportManualItem struct {
	Item        string `json:"item"`
	Reason      string `json:"reason"`
	Instruction string `json:"instruction"`
}

// CPAImportResult is the observable outcome of one import. It reports what was
// written and read back; it is not an authorization for a later step.
type CPAImportResult struct {
	Stage          model.UpgradeStage `json:"stage"`
	DomainsDone    []string           `json:"domainsDone"`
	SourceVersion  string             `json:"sourceVersion"`
	SourceLayout   string             `json:"sourceLayout"`
	SourceIdentity string             `json:"sourceIdentity"`
	DestinationURL string             `json:"destinationUrl"`
	PluginsEnabled bool               `json:"pluginsEnabled"`
	// ClientKeys counts the client keys the destination was proved to hold. It is
	// reported only after that proof: a run refused before it reports zero,
	// because reading the source's list is not proving the destination holds it.
	// The keys themselves never leave this process.
	ClientKeys int `json:"clientKeys"`
	// MigratedCount counts the source items the executor copied and read back
	// under the compiled adoption authority. The item identities are not reported:
	// a credential's identity is derived from its unscubbed name, and the summary's
	// output boundary never emits one.
	MigratedCount int `json:"migratedCount"`
	// ServerFingerprint is the digest of the destination's own build version and
	// v8 configuration body this run proved with. The completion gate re-proves it
	// against the live CPA, so it is evidence about the target, not a caller's
	// claim.
	ServerFingerprint string `json:"serverFingerprint"`
	// ManualItems are the source items the executor did not copy because the
	// adoption rules do not mark their reference migrate. Each carries the rule's
	// own manual action, never a generic deferral.
	ManualItems []CPAImportManualItem `json:"manualItems"`
	// AlreadyComplete reports that the recorded operation already held this
	// import, so nothing was read from the source or written to the destination.
	AlreadyComplete bool  `json:"alreadyComplete"`
	UpdatedAtMS     int64 `json:"updatedAtMs"`
}

// cpaImportRun is the state one run needs beyond its reported result. It holds
// the destination management secret, which the result must never carry, and the
// adoption authorization every copy runs under.
type cpaImportRun struct {
	options           CPAImportOptions
	source            *cpaManagedSource
	databasePath      string
	dataKeyPath       string
	managementKey     string
	serverFingerprint string
	auth              freshadopt.Authorization
	// qualifiedArtifactID is the v8 artifact both accepted adoption consumers were
	// qualified against. The Supervisor reports the artifact actually active, and a
	// destination staged on another one is an artifact no chain accepted, so the
	// import refuses it instead of copying under a verdict that does not describe
	// the destination it writes.
	qualifiedArtifactID model.RuntimeArtifactID
}

// ImportCPA prepares the v2 destination for a 1.x stack's managed CPA state
// while the caller holds the 1.x ownership marker.
//
// The order is fixed: refuse a recorded position that cannot enter the import,
// prove the compiled adoption authority, read the source and gate its version,
// refuse a missing choice, prove the destination is observable, stage and start
// it, bind the caller's URL to that destination, and only then enter the
// cpa_import stage. The source is then inventoried through the adoption executor
// under that authority, so the credential files, the gateway routing
// configuration and the provider runtime state are copied and read back; items
// the rules do not mark migrate stay manual. The Manager's connection takes the
// destination and the secret it was staged with, and the `cpa_config` domain is
// recorded last, so a failure before it leaves the operation at cpa_import
// without the domain — the position the next run resumes from.
func ImportCPA(ctx context.Context, options CPAImportOptions) (CPAImportResult, error) {
	if err := ctx.Err(); err != nil {
		return CPAImportResult{}, err
	}
	result := CPAImportResult{DomainsDone: []string{}, ManualItems: []CPAImportManualItem{}}
	if options.PluginsEnabled == nil {
		return result, newUpgradeError(UpgradeErrorCPAPluginChoiceRequired,
			"the CPA import requires v2's explicit plugin-runtime choice; it is never inherited from the 1.x source and never defaulted", nil)
	}
	result.PluginsEnabled = *options.PluginsEnabled
	if options.Runtime == nil {
		return result, newUpgradeError(UpgradeErrorCPADestinationUnready,
			"the CPA import needs the Manager-owned Runtime to stage the destination CPA", nil)
	}
	targetRoot, err := resolveTargetRoot(options.TargetRoot)
	if err != nil {
		return result, err
	}
	// canonicalPath resolves as far as the path exists and cleans the rest, so an
	// unmounted or absent root still yields the path this run would use.
	targetRoot = canonicalPath(targetRoot)
	paths := newTargetPaths(targetRoot)
	recorded, found, err := readRecordedCopyIn(ctx, paths.Database)
	if err != nil {
		return result, err
	}
	if !found {
		return result, errors.New("the v2 data root holds no recorded copy-in, so there is no CPA state to import")
	}
	// The import already ran and was recorded. Its result is the row, and
	// re-running it would rewrite a destination a later step may already have
	// advanced. It is checked before the entry gate because a recorded import is
	// not a position to refuse — but the recorded verdict is only reported after
	// the destination evidence and the Manager's own sealed connection are
	// re-read and the live destination is re-proved, so a store whose handoff did
	// not survive, and a destination whose configuration moved after the import,
	// are not answered as a finished import.
	if recorded.Stage == model.UpgradeStageCPAImport && recorded.HasDomain(model.UpgradeDomainCPAConfig) {
		if err := replayRecordedManagedImport(ctx, paths, options, recorded, &result); err != nil {
			return result, err
		}
		result.Stage = recorded.Stage
		result.DomainsDone = slices.Clone(recorded.DomainsDone)
		result.UpdatedAtMS = recorded.UpdatedAtMS
		result.AlreadyComplete = true
		return result, nil
	}
	// The step may only run where it could still record its result, and only from
	// the position the pipeline leaves for it: reconciliation. Entering from
	// copy_in would move the row past reconcile, and reconcile refuses a stage it
	// cannot enter, so one misplaced run would make usage reconciliation
	// permanently unreachable.
	if err := requireCPAImportEntry(recorded); err != nil {
		return result, err
	}

	// The import reads the tree the copy-in recorded and nothing else. The
	// comparison is canonical and runs before the CPA directory is touched, so a
	// run pointed at another installation's tree is refused without reading it.
	if err := requireRecordedSourceRoot(recorded, options.LegacyRoot); err != nil {
		return result, err
	}
	// The source is read on the assumption that it is stopped. The 1.x ownership
	// marker is the only fence that assumption has, so it is held before the CPA
	// directory is opened and, at the command line, for the whole read/write
	// path. A caller that did not take it, or took it for another database, is
	// refused here without a read.
	if err := requireCPAImportSourceLock(options.SourceLock, options.LegacyRoot); err != nil {
		return result, err
	}
	// The authority is proved before anything else is staged: a build whose
	// compiled record does not authorize the copy refuses here, with the source
	// still unread and the destination untouched. The artifact the record was
	// accepted on is proved with it, because the destination the Supervisor
	// reports is only this import's target when it runs on that artifact.
	auth, err := qualifiedUpgradeAdoption()
	if err != nil {
		return result, err
	}
	qualifiedArtifact, err := freshstrategy.QualifiedUpgradeAdoptionArtifactID()
	if err != nil {
		return result, newUpgradeError(UpgradeErrorCPAImportUnproven,
			"this build carries no accepted CPA adoption artifact for an Upgrade-derived source, so the import cannot prove the destination it would stage; nothing was staged or written", err)
	}

	// The source is read and gated before the destination is touched: a source
	// below the floor must be refused before the copy-in, not after it.
	source, err := openManagedCPASource(cpaSourceOptions{
		LegacyRoot:       options.LegacyRoot,
		CPADirectory:     options.CPADirectory,
		CPAAuthDirectory: options.CPAAuthDirectory,
		CPAVersion:       options.CPAVersion,
	})
	if err != nil {
		return result, err
	}
	run := &cpaImportRun{
		options: options, source: source, databasePath: paths.Database, dataKeyPath: paths.DataKey, auth: auth,
		qualifiedArtifactID: qualifiedArtifact,
	}
	result.SourceVersion = source.version
	result.SourceLayout = source.layout
	result.SourceIdentity = source.sourceIdentity()
	for _, item := range source.manual {
		result.ManualItems = append(result.ManualItems, CPAImportManualItem{Item: item.item, Reason: item.reason, Instruction: item.instruction})
	}

	// The destination is staged and started, and its secret is recorded, before
	// the row moves to cpa_import. A refusal while staging therefore leaves the
	// recorded position untouched: entering the stage first made every refusal
	// after the version gate look like a started import, and moved the row past
	// the reconcile step it still had to complete.
	if err := run.stageDestination(ctx, &result); err != nil {
		return result, err
	}
	// The destination was proved with this key list, so the count is a fact about
	// this run only now. A refused proof reports zero: this step read the list from
	// the source, and reading it is not proving the destination holds it.
	result.ClientKeys = len(source.clientKeys)
	// From here the step owns the position: a failure below leaves the operation
	// at cpa_import — the position the operator retries from — instead of
	// pretending the step never started.
	if err := enterCPAImportStage(ctx, paths.Database); err != nil {
		return result, err
	}
	summary, err := run.runExecutor(ctx, &result)
	if err != nil {
		return result, err
	}
	if err := requireCPAImportSettled(summary); err != nil {
		return result, err
	}
	// The executor wrote routing settings, payload rule families and provider
	// sections into the destination, and a real CPA serves the digest of the
	// configuration it now holds. The fingerprint proved before the copy therefore
	// describes a document that no longer exists, and recording it would make the
	// completion gate compare the destination against the state it had before this
	// step wrote into it. The proof is repeated once the writes settled, on the URL
	// this run already proved is the staged destination.
	if err := run.proveDestinationFingerprint(ctx, result.DestinationURL, run.managementKey); err != nil {
		return result, err
	}

	// The Manager's own connection takes the destination and the same secret this
	// run staged with, before the operation row records the completion. A crash
	// between this write and the next one leaves both the connection and the
	// recorded attempt, so a rerun finishes. The attempt row stays for the same
	// reason: a rerun that has not recorded `cpa_config` must replay the recorded
	// request and the recorded secret rather than mint a new one.
	result.ServerFingerprint = run.serverFingerprint
	if err := handDestinationConnection(ctx, paths.Database, paths.DataKey, result.DestinationURL, run.managementKey); err != nil {
		return result, err
	}
	recorded, err = recordCPAImportCompletion(ctx, paths.Database, result)
	if err != nil {
		return result, err
	}
	result.Stage = recorded.Stage
	result.DomainsDone = recorded.DomainsDone
	result.UpdatedAtMS = recorded.UpdatedAtMS
	return result, nil
}

// replayRecordedManagedImport re-reads and re-proves the evidence a recorded
// Managed import left behind before its verdict is reported.
//
// `cpa_config` says the destination holds the copied state; it says nothing about
// whether the Manager still holds the sealed credential that state was proved
// with, nor about the configuration the destination serves now. The replay
// therefore proves, in order: this build still carries the compiled adoption
// authority the copy ran under, the Supervisor is ready and still runs the one
// artifact that authority was qualified against, the recorded destination is still
// the one the Manager connects to, and the live CPA at the recorded address still
// answers as the destination this import staged.
//
// The artifact check is the one that cannot be inferred from the row. A Supervisor
// that switched to an unqualified artifact B while keeping the same URL, management
// key and client keys would answer the destination proof exactly as A did, and the
// replay would refresh the fingerprint to B's digest — reporting a finished import
// for a destination no accepted chain describes. The recorded attempt names the
// artifact its stage request carried, and the Runtime reports the one it runs, so
// the two are compared before the destination is addressed.
//
// The artifact is reported in every Supervisor state, so a match alone does not
// prove the destination CPA is answering: the replay requires the same `ready`
// state the completion gate requires, with the gate's own reason. A Supervisor that
// is offline, unknown or still starting leaves the recorded fingerprint and the
// operation row untouched rather than reporting a completion the gate would refuse
// a moment later.
//
// The caller's own inputs are compared with the recorded attempt as well, because a
// replay that accepted a different plugin choice, source root, destination URL or
// management secret would report the recorded verdict for a request the Supervisor
// never staged. Nothing is written by a refused replay.
//
// The last check is why this step contacts the destination at all. The verification
// tells the operator to re-run `cpa-import` after applying a manual item to the
// destination, and applying one changes the configuration the CPA serves, so the
// fingerprint the import recorded is stale exactly when the operator followed that
// instruction. Refreshing it here is the recovery; refusing the handoff instead
// would leave the operation permanently unverifiable. An unreachable destination
// fails closed as `upgrade_cpa_import_target_unreachable` rather than pretending
// the stale fingerprint is current.
func replayRecordedManagedImport(
	ctx context.Context,
	paths targetPaths,
	options CPAImportOptions,
	recorded model.UpgradeOperation,
	result *CPAImportResult,
) error {
	// The copy a recorded import claims was made under the compiled authority this
	// build carries. A build whose record no longer proves it cannot re-assert the
	// verdict, and it must say so before it connects to any destination.
	if _, err := qualifiedUpgradeAdoption(); err != nil {
		return err
	}
	qualifiedArtifact, err := freshstrategy.QualifiedUpgradeAdoptionArtifactID()
	if err != nil {
		return newUpgradeError(UpgradeErrorCPAImportUnproven,
			"this build carries no accepted CPA adoption artifact for an Upgrade-derived source, so this run cannot confirm the destination the copy was qualified against; nothing was written", err)
	}
	if options.Runtime == nil {
		return newUpgradeError(UpgradeErrorCPAImportUnproven,
			"the recorded CPA import cannot be re-read without the Manager-owned Runtime that reports the destination's artifact, so this run cannot confirm the import it would report as complete; nothing was written", nil)
	}
	status, err := options.Runtime.Status(ctx)
	if err != nil || status.Validate() != nil || status.ProtocolVersion != "v1" || status.ActiveGatewayArtifact == nil {
		return newUpgradeError(UpgradeErrorCPAImportUnproven,
			"the Manager-owned Runtime did not report a staged destination CPA, so this run cannot confirm the artifact the recorded import was qualified against; nothing was written", err)
	}
	// The Supervisor reports its observed artifact in every state, so the artifact
	// comparison below would accept an offline or starting Supervisor running the
	// qualified artifact. Only a Ready Supervisor is administering the destination
	// CPA that the fingerprint refresh contacts, and the completion gate refuses
	// the other states for that reason; a replay that refreshed the recorded
	// fingerprint and reported the import complete from a status the gate would
	// refuse would publish a verdict the gate cannot repeat.
	if reason := managedRuntimeReadyFailure(status); reason != "" {
		return newUpgradeError(UpgradeErrorCPAImportUnproven, reason+"; nothing was written", nil)
	}
	if artifact := status.ActiveGatewayArtifact.ArtifactID; artifact != qualifiedArtifact {
		return newUpgradeError(UpgradeErrorCPAImportUnproven, fmt.Sprintf(
			"the Runtime's active gateway artifact is %q, and this build's accepted CPA adoption record is for %q; the recorded import copied only into the artifact its staging transition and replay chain were qualified against, so this run cannot report it as complete and nothing was written",
			artifact, qualifiedArtifact), nil)
	}
	// The source root is not part of the attempt row, so it is compared with the
	// operation's own record: a run pointed at another installation's tree would
	// otherwise report that tree's import under this operation's verdict.
	if err := requireRecordedSourceRoot(recorded, options.LegacyRoot); err != nil {
		return err
	}
	if err := requireRecoveredCopyIn(paths.Database); err != nil {
		return err
	}
	db, err := managersqlite.Open(paths.Database)
	if err != nil {
		return newUpgradeError(UpgradeErrorCPAImportUnproven,
			"the recorded CPA import cannot be re-read, so this run cannot confirm the destination it handed over; nothing was written", err)
	}
	defer func() { _ = db.Close() }()
	target, err := verifyTargetConfiguration(ctx, db, paths, model.UpgradeTargetManaged)
	if err != nil {
		return newUpgradeError(UpgradeErrorCPAImportUnproven,
			"the recorded CPA import's destination evidence cannot be read, so this run cannot confirm the handoff survived; nothing was written", err)
	}
	if err := requireVerifyTarget(model.UpgradeTargetManaged, upgradeoperation.ExternalBinding{}, target); err != nil {
		return newUpgradeError(UpgradeErrorCPAImportUnproven,
			"the recorded CPA import no longer matches the Manager's own CPA connection, so this run cannot report it as complete; nothing was written", err)
	}
	if err := requireManagedDestinationEvidence(target.attempt); err != nil {
		return newUpgradeError(UpgradeErrorCPAImportUnproven,
			"the recorded CPA import names no proved destination server, so this run cannot confirm the copy it claims; nothing was written", err)
	}
	// The attempt published its stage request against one artifact; the Supervisor
	// reports the artifact it runs now. Both were checked against the compiled
	// record above, and an attempt that names another artifact is a destination no
	// chain this build accepts ever described.
	if attemptArtifact := target.attempt.Intent.ExpectedActiveArtifactID; attemptArtifact != qualifiedArtifact {
		return newUpgradeError(UpgradeErrorCPAImportUnproven, fmt.Sprintf(
			"the recorded destination attempt was staged on artifact %q, and this build's accepted CPA adoption record is for %q; this run cannot report the import as complete and nothing was written",
			attemptArtifact, qualifiedArtifact), nil)
	}
	if identity := target.attempt.Intent.ExpectedRuntimeIdentity; identity != "" && status.Identity != identity {
		return newUpgradeError(UpgradeErrorCPAImportUnproven, fmt.Sprintf(
			"the recorded destination attempt was staged on Runtime %q, and the Supervisor running now reports %q; the recorded request belongs to an authority this run cannot address, so it cannot be replayed and nothing was written",
			identity, status.Identity), nil)
	}
	// What the next start reads is the envelope on disk, not the plaintext a store
	// happens to return. A handoff whose sealed credential was removed or replaced
	// is not one this step may answer as finished.
	if err := verifyStoredManagedConnection(ctx, db, target); err != nil {
		return newUpgradeError(UpgradeErrorCPAImportUnproven,
			"the Manager's stored CPA connection is not the sealed credential the CPA import handed the destination, so this run cannot report the handoff as complete; nothing was written", err)
	}
	// The recorded import staged one request, and the Supervisor never applies
	// another under it. A replay that named different inputs would report that
	// request's verdict for a run the operator did not ask for, so each immutable
	// input is compared with the attempt before the destination is addressed.
	if err := requireRecordedReplayRequest(target, options); err != nil {
		return err
	}
	fingerprint, err := refreshRecordedManagedDestination(ctx, paths, target, strings.TrimSpace(target.connection.ManagementKey))
	if err != nil {
		return err
	}
	result.DestinationURL = target.attempt.BaseURL
	result.ServerFingerprint = fingerprint
	result.SourceIdentity = target.attempt.Intent.SourceIdentity
	result.ClientKeys = len(target.attempt.Intent.APIKeys)
	result.PluginsEnabled = target.attempt.Intent.PluginsEnabled != nil && *target.attempt.Intent.PluginsEnabled
	result.SourceVersion = target.attempt.SourceVersion
	result.SourceLayout = target.attempt.SourceLayout
	result.ManualItems = restoreManualItems(recorded, target.attempt)
	return nil
}

// requireRecordedReplayRequest compares the inputs a replay still carries with the
// request the recorded attempt submitted.
//
// The plugin choice is part of the Supervisor's published request, and the
// destination URL is the address this operation bound to that destination. A replay
// that named a different value for either would report the recorded verdict for a
// run that never happened, so both are refused as a changed request. The management
// secret is accepted when the caller omits it — the store holds the staged one —
// and compared when it is supplied, by the same constant-time equality the
// completion gate uses.
func requireRecordedReplayRequest(target verifyTarget, options CPAImportOptions) error {
	refuse := func(what string) error {
		return newUpgradeError(UpgradeErrorCPADestinationRequestChanged, fmt.Sprintf(
			"the recorded CPA import was staged with %s; the Supervisor never applies a different request under a recorded operation, so this replay refused before reading or writing the destination and wrote nothing",
			what), nil)
	}
	if options.PluginsEnabled == nil || !pluginsChoiceEqual(options.PluginsEnabled, target.attempt.Intent.PluginsEnabled) {
		return refuse("a different plugin choice")
	}
	baseURL, err := cpa.NormalizeV8BaseURL(strings.TrimSpace(options.DestinationURL))
	if err != nil {
		return refuse("a destination URL that is not a usable endpoint")
	}
	recordedURL, err := cpa.NormalizeV8BaseURL(strings.TrimSpace(target.attempt.BaseURL))
	if err != nil || baseURL != recordedURL {
		return refuse("a different destination URL")
	}
	if supplied := strings.TrimSpace(options.DestinationManagementKey); supplied != "" &&
		!security.EqualHMAC(supplied, strings.TrimSpace(target.attemptSecret)) {
		return refuse("a different management secret")
	}
	return nil
}

// refreshRecordedManagedDestination re-proves the live destination a recorded
// import proved and returns the server fingerprint it currently serves.
//
// The proof is the import's own: the URL must still answer as the destination this
// attempt staged with these client keys, and the fingerprint is re-derived with the
// secret the Manager itself will present after a restart. When the destination
// moved, the attempt row keeps its secret and replay identity and takes the new
// fingerprint, so the completion gate compares against what the CPA holds now.
// An unreachable destination is refused as `upgrade_cpa_import_target_unreachable`
// so a stopped or restarting CPA never reports a stale fingerprint as refreshed.
func refreshRecordedManagedDestination(ctx context.Context, paths targetPaths, target verifyTarget, managementKey string) (string, error) {
	baseURL := target.attempt.BaseURL
	run := &cpaImportRun{databasePath: paths.Database, dataKeyPath: paths.DataKey}
	if err := run.requireStagedDestinationIdentity(ctx, baseURL, managementKey, target.attempt.Intent.APIKeys, true); err != nil {
		return "", err
	}
	if err := run.proveDestinationFingerprint(ctx, baseURL, managementKey); err != nil {
		return "", err
	}
	if err := run.recordProvedDestination(ctx, target.attempt, baseURL, managementKey); err != nil {
		return "", err
	}
	return run.serverFingerprint, nil
}

// requireCPAImportSourceLock proves the caller holds the 1.x ownership marker of
// the source this run binds to.
//
// AcquireExistingReadOnly is the accepted fence for a command that assumes the
// source is stopped: it takes the same exclusive marker through a read-only
// descriptor, so a source preserved on a read-only mount still proves exclusivity
// and the import never modifies a marker it may not own. The marker itself is
// opened, never the source database. It proves the 1.x Manager is not running. It
// does not prove the managed CPA container is stopped, which is why the operator
// instruction says so.
func requireCPAImportSourceLock(lock *processlock.Lock, legacyRoot string) error {
	if lock == nil {
		return newUpgradeError(UpgradeErrorCPASourceLocked,
			"the CPA import requires the held 1.x ownership lock, so the source cannot be assumed stopped; nothing was read from the source and nothing was written", nil)
	}
	if err := requireSourceLock(lock, SourceDatabasePath(legacyRoot)); err != nil {
		return newUpgradeError(UpgradeErrorCPASourceLocked,
			"the held ownership lock does not belong to the 1.x source this operation recorded, so the source is not provably stopped; nothing was read from it and nothing was written", err)
	}
	return nil
}

// requireCPAImportEntry reports whether the recorded operation stands where this
// step may be entered from.
//
// The import follows the reconciliation: it may be entered from `reconcile`, and
// it may resume from its own stage while the cpa_config domain is unrecorded.
// Anything else — a copy-in that never reconciled, a later stage, a rollback — is
// refused before the source is read, because moving such a row to cpa_import
// would claim a position the pipeline never granted and make the skipped step
// permanently unreachable. A `failed` park resumes only when the operation
// already records a domain reconciliation establishes.
func requireCPAImportEntry(recorded model.UpgradeOperation) error {
	refuse := func() error {
		return newUpgradeError(UpgradeErrorCPAStageConflict, fmt.Sprintf(
			"the recorded operation is at stage %q, which cannot enter the CPA import; the import runs after reconciliation, and this run read and wrote nothing",
			recorded.Stage), nil)
	}
	switch recorded.Stage {
	case model.UpgradeStageReconcile:
		return nil
	case model.UpgradeStageCPAImport:
		if !recorded.HasDomain(model.UpgradeDomainCPAConfig) {
			return nil
		}
	case model.UpgradeStageFailed:
		if cpaImportReconciledDomainsRecorded(recorded) {
			return nil
		}
	}
	return refuse()
}

// cpaImportReconciledDomains are the domains the reconciliation step establishes.
// They are the evidence that a `failed` operation was parked after reconciliation
// rather than before it, so the import may still resume there.
var cpaImportReconciledDomains = []string{
	model.UpgradeDomainUsageEvents,
	model.UpgradeDomainRetainedProjection,
	model.UpgradeDomainPricing,
	model.UpgradeDomainCoverage,
	model.UpgradeDomainIdentity,
}

func cpaImportReconciledDomainsRecorded(recorded model.UpgradeOperation) bool {
	for _, domain := range cpaImportReconciledDomains {
		if recorded.HasDomain(domain) {
			return true
		}
	}
	return false
}

// stageDestination proves the destination is observable, then stages and starts
// it with the attempt this operation already owns, and binds the caller's URL to
// that destination before the run may address it.
//
// The destination request is immutable once recorded: the Supervisor replays a
// journalled operation by ID and never applies a different request under the same
// one, so a retry reuses the recorded secret, plugin choice and client key set
// instead of minting a second secret and submitting it into a replay. A
// destination that already reports itself ready is not staged again. Every arm —
// a recorded ready destination, a resumed attempt, a new stage — ends in
// requireStagedDestinationIdentity, so the URL is proved to be the staged
// destination with this import's client keys before the executor is built; a
// foreign CPA is refused instead of receiving source configuration and
// credentials.
//
// A ready destination is proved only when this operation recorded the attempt
// that staged it. Readiness is the Supervisor's report about the artifact it
// runs, and it says nothing about the process behind the caller's URL: another v8
// CPA that shares the management secret and the ordered client key list answers
// the same proof. Without a recorded attempt this run refuses before the URL is
// contacted at all, and told to stop the destination so the stage can bind it
// under a secret only this operation knows. For the same reason an initial stage
// never accepts the caller's static secret: it mints a per-attempt secret and
// writes it into the Supervisor's staged CPA, so nothing at the address can
// present it.
//
// The URL is not part of the request the Supervisor journals, so it is the one
// input a retry may still change — but only while no run proved it. A refused URL
// is therefore never recorded as the attempt's destination: the attempt keeps the
// request and the secret, and a later run that names the staged destination
// instead proves that URL and records it. Once a URL is proved it is as immutable
// as the request, because swapping it afterwards would silently address another
// CPA with a secret minted for this one.
func (r *cpaImportRun) stageDestination(ctx context.Context, result *CPAImportResult) error {
	baseURL, err := cpa.NormalizeV8BaseURL(r.options.DestinationURL)
	if err != nil {
		return newUpgradeError(UpgradeErrorCPADestinationUnready, "the destination CPA URL is not usable", err)
	}
	result.DestinationURL = baseURL
	status, err := r.options.Runtime.Status(ctx)
	if err != nil || status.Validate() != nil || status.ProtocolVersion != "v1" || status.ActiveGatewayArtifact == nil {
		return newUpgradeError(UpgradeErrorCPADestinationUnready, "the Manager-owned Runtime did not report a staged destination CPA", err)
	}
	if !status.Capabilities.Supports(model.RuntimeCapabilityStageDestination) || !status.Capabilities.Supports(model.RuntimeCapabilityStart) {
		return newUpgradeError(UpgradeErrorCPADestinationUnready,
			"this Runtime cannot stage a destination CPA, so the import has nowhere to write", nil)
	}
	// The destination the Supervisor reports is only this import's target when it
	// runs on the artifact the compiled record was accepted for. Staging onto
	// another one would copy source configuration and credentials under a verdict
	// that never described that destination, so the mismatch is refused before the
	// attempt is read and before anything is submitted.
	if r.qualifiedArtifactID == "" {
		return newUpgradeError(UpgradeErrorCPAImportUnproven,
			"this run carries no qualified CPA adoption artifact, so it cannot prove the destination the Runtime reports is the one the record authorizes; nothing was staged or written", nil)
	}
	if artifact := status.ActiveGatewayArtifact.ArtifactID; artifact != "" && artifact != r.qualifiedArtifactID {
		return newUpgradeError(UpgradeErrorCPAImportUnproven, fmt.Sprintf(
			"the Runtime's active gateway artifact is %q, and this build's accepted CPA adoption record is for %q; the import copies only into the artifact its staging transition and replay chain were qualified against, so nothing was staged or written",
			artifact, r.qualifiedArtifactID), nil)
	}
	clientKeys := slices.Clone(r.source.clientKeys)
	supplied := strings.TrimSpace(r.options.DestinationManagementKey)
	// The whole request this run would submit is fixed here, before the recorded
	// attempt is read: the Supervisor compares every field of a replacement
	// attempt against the published one, so the retry has to compare the same set
	// itself. SourceIdentity is the source's own file digest, so it changes when
	// the 1.x configuration changes, and the three Runtime fields change when the
	// Supervisor restarts or is updated.
	pluginsEnabled := result.PluginsEnabled
	intent := cpaImportDestinationIntent{
		ExpectedRuntimeIdentity:   status.Identity,
		ExpectedRuntimeGeneration: status.Generation,
		ExpectedActiveArtifactID:  status.ActiveGatewayArtifact.ArtifactID,
		SourceIdentity:            r.source.sourceIdentity(),
		SourceClass:               managedDestinationSourceClass,
		APIKeys:                   slices.Clone(clientKeys),
		PluginsEnabled:            &pluginsEnabled,
	}

	record, recordedKey, found, err := loadCPAImportDestinationRecord(ctx, r.databasePath, r.dataKeyPath)
	if err != nil {
		return newUpgradeError(UpgradeErrorCPADestinationUnready,
			"the destination attempt an earlier run recorded cannot be read, so this run did not mint a second secret", err)
	}
	if found {
		if record.OperationID == "" {
			// A record written before the attempt identity existed holds the one
			// thing that cannot be recovered — the staged secret — and nothing
			// else needs to change: attempt 1 has the same derived operation ID and
			// this run's source supplies the request. Its URL is carried over but
			// never as proved: that build recorded the caller's URL before proving
			// it, so only a run that proves one may bind the attempt to it.
			record = cpaImportDestinationRecord{
				BaseURL:                 record.BaseURL,
				Intent:                  intent,
				OperationID:             cpaImportDestinationOperationID(intent.SourceIdentity, 1),
				Attempt:                 1,
				SourceCredentialsDigest: r.source.sourceCredentialsDigest(),
			}
			if err := saveCPAImportDestinationRecord(ctx, r.databasePath, r.dataKeyPath, record, recordedKey); err != nil {
				return newUpgradeError(UpgradeErrorCPADestinationUnready,
					"the recorded destination attempt could not be bound to this import, so nothing was staged", err)
			}
		} else if !record.complete() {
			// An attempt recorded without the request it submitted can only be
			// replayed under its own operation ID: the Supervisor never applies a
			// different request under an existing one, and replaying it is what
			// this run does below. It cannot be superseded, because that requires
			// proving the replacement is the published request and nothing recorded
			// that request.
			if record.TerminalFailure {
				return newUpgradeError(UpgradeErrorCPADestinationRequestChanged,
					"the recorded destination attempt predates the full request record, so this build cannot prove a replacement is the identical request; the Supervisor only supersedes a request it can compare, and this run changed nothing", nil)
			}
			record.Intent = intent
			record.SourceCredentialsDigest = r.source.sourceCredentialsDigest()
			if err := saveCPAImportDestinationRecord(ctx, r.databasePath, r.dataKeyPath, record, recordedKey); err != nil {
				return newUpgradeError(UpgradeErrorCPADestinationUnready,
					"the recorded destination attempt could not be completed with this run's request, so nothing was staged", err)
			}
		}
		if err := requireSameDestinationRequest(record, intent, baseURL, recordedKey, supplied, r.source.sourceCredentialsDigest(), status.State == model.RuntimeStateReady); err != nil {
			return err
		}
		if status.State == model.RuntimeStateReady {
			return r.proveStagedDestination(ctx, record, baseURL, recordedKey, clientKeys, record.bindsURL(baseURL))
		}
		if record.TerminalFailure {
			// The Supervisor never resumes a terminal operation, so the only way to
			// stage the identical destination again is a new attempt ID, and that
			// is exactly the supersede contract: same request, new ID, after the
			// old stage/start is known to be terminal. The intent is copied, so the
			// new ID is the only difference.
			record = nextCPAImportDestinationAttempt(record)
			if err := saveCPAImportDestinationRecord(ctx, r.databasePath, r.dataKeyPath, record, recordedKey); err != nil {
				return newUpgradeError(UpgradeErrorCPADestinationUnready,
					"the next destination attempt could not be recorded, so nothing was staged", err)
			}
		}
		if err := r.submitDestination(ctx, record, recordedKey); err != nil {
			return err
		}
		return r.proveStagedDestination(ctx, record, baseURL, recordedKey, clientKeys, record.bindsURL(baseURL))
	}

	if status.State == model.RuntimeStateReady {
		// The Supervisor's CPA is already running, and this operation holds no
		// record of staging it. The ready state is the Supervisor's report about the
		// artifact it runs, not proof that the URL this Manager would address is the
		// Supervisor's own CPA process: another v8 CPA that happens to share the
		// management secret and the ordered client key list answers the same proof.
		// So a run that did not stage this destination refuses here, before the URL
		// is contacted at all, and tells the operator to stop it so the stage can
		// bind it under a per-attempt secret only this operation knows.
		if supplied == "" {
			return newUpgradeError(UpgradeErrorCPADestinationKeyRequired,
				"the destination CPA is already running and this run holds no management secret for it; stop the destination so cpa-import can stage and bind it under a per-attempt secret (or pass --destination-management-key-file when resuming a recorded attempt)", nil)
		}
		return newUpgradeError(UpgradeErrorCPADestinationUnready,
			"the destination CPA is already running, but this upgrade holds no recorded Supervisor staging attempt for it; stop the destination so cpa-import can stage and bind it under a per-attempt secret, and nothing was written", nil)
	}

	// An initial stage from a stopped state mints its own secret and never accepts
	// the caller's. A static secret an operator supplied is one a foreign CPA at the
	// URL could have been configured with, so it cannot bind this operation to the
	// Supervisor's process; the freshly minted 256-bit secret is written into the
	// staged CPA and cannot be known by anything else at that address.
	if supplied != "" {
		return newUpgradeError(UpgradeErrorCPADestinationUnready,
			"a first destination stage mints its own per-attempt management secret to bind the Supervisor's CPA; --destination-management-key-file is only accepted when resuming a recorded attempt, and nothing was staged or written", nil)
	}
	managementKey, err := newCPAImportManagementSecret()
	if err != nil {
		return newUpgradeError(UpgradeErrorCPADestinationUnready, "a destination management secret could not be generated", err)
	}
	// The attempt is durable before the destination is staged. The stage hands the
	// destination the only copy it will ever hold, so a run that fails after it
	// must find the same secret, plugin choice, client keys and request on retry
	// instead of demanding an operator input the CLI cannot supply. The caller's
	// URL is deliberately not part of it: it is not part of the Supervisor's
	// request, and a URL this run has not proved must stay replaceable, or one
	// mistyped address would refuse every later run that names the destination
	// the Supervisor actually staged.
	record = cpaImportDestinationRecord{
		Intent:                  intent,
		OperationID:             cpaImportDestinationOperationID(intent.SourceIdentity, 1),
		Attempt:                 1,
		SourceCredentialsDigest: r.source.sourceCredentialsDigest(),
	}
	if err := saveCPAImportDestinationRecord(ctx, r.databasePath, r.dataKeyPath, record, managementKey); err != nil {
		return newUpgradeError(UpgradeErrorCPADestinationUnready,
			"the destination management secret could not be recorded, so the destination was not staged", err)
	}
	if err := r.submitDestination(ctx, record, managementKey); err != nil {
		return err
	}
	// The stage is the Supervisor's, not yet proof that the URL this run will
	// address is the destination it staged: the caller supplies that URL, and a
	// Manager pointed at another CPA would otherwise hand it the source's
	// configuration and credentials and only notice at the final proof. The
	// authenticated key-list read binds the two before the executor is built, and
	// only that read records the URL on the attempt.
	return r.proveStagedDestination(ctx, record, baseURL, managementKey, clientKeys, false)
}

// proveStagedDestination is the whole destination proof: the URL answers as the
// staged destination, the CPA reports the server facts the completion gate will
// compare later, and the attempt records both.
//
// It is the only path that records a proved URL and a fingerprint, so no arm of
// stageDestination can continue on a proof nothing durable holds.
func (r *cpaImportRun) proveStagedDestination(
	ctx context.Context,
	record cpaImportDestinationRecord,
	baseURL string,
	managementKey string,
	clientKeys []string,
	boundURL bool,
) error {
	if err := r.requireStagedDestinationIdentity(ctx, baseURL, managementKey, clientKeys, boundURL); err != nil {
		return err
	}
	if err := r.proveDestinationFingerprint(ctx, baseURL, managementKey); err != nil {
		return err
	}
	return r.recordProvedDestination(ctx, record, baseURL, managementKey)
}

// proveDestinationFingerprint reads the destination's build version and the
// digest of the v8 configuration body it serves, and holds the fingerprint the
// completion gate re-derives from the live CPA.
//
// It runs after the authenticated destination proof, so it only ever addresses a
// URL this run has already proved to be the staged destination with this
// import's secret. Two authenticated reads are enough: the version probe and the
// key snapshot that also carries the configuration digest.
func (r *cpaImportRun) proveDestinationFingerprint(ctx context.Context, baseURL, managementKey string) error {
	probes := ExternalConnectOptions{BaseURL: baseURL, ManagementKey: managementKey}
	report, err := externalConnectVersion(ctx, probes, baseURL, managementKey)
	if err != nil {
		return newUpgradeError(UpgradeErrorCPADestinationUnready,
			"the proved destination did not report the build version the completion gate records, so this run stopped before reading or writing it", err)
	}
	if report.WriteBlocked {
		return newUpgradeError(UpgradeErrorCPADestinationUnready,
			"the proved destination does not serve the v8 contract this Manager writes, so this run stopped before reading or writing it", nil)
	}
	snapshot, err := externalConnectKeys(ctx, probes, baseURL, managementKey)
	if err != nil {
		return newUpgradeError(UpgradeErrorCPADestinationUnready,
			"the proved destination did not serve its configuration digest, so this run stopped before reading or writing it", err)
	}
	r.serverFingerprint = freshkeyapply.ExternalServerFingerprint(report.Version, snapshot.SourceSHA256)
	if r.serverFingerprint == "" {
		return newUpgradeError(UpgradeErrorCPADestinationUnready,
			"the proved destination produced no server fingerprint, so the completion gate could not re-prove it", nil)
	}
	return nil
}

// recordProvedDestination records that the caller's URL answered as the
// destination this attempt staged, together with the server facts this run
// observed.
//
// It runs after the authenticated proof and never before it. The URL is the one
// attempt field the Supervisor's request does not carry, so a run that has not
// proved one must still be able to name the staged destination on retry; once a
// URL is proved, this record makes it as immutable as the request itself. The
// write is required, not best effort: without it the attempt's own record would
// not say which CPA it is bound to, and this run would continue on a proof that
// nothing durable holds.
func (r *cpaImportRun) recordProvedDestination(
	ctx context.Context,
	record cpaImportDestinationRecord,
	baseURL string,
	managementKey string,
) error {
	if record.urlProved() && record.BaseURL == baseURL && record.ServerFingerprint == r.serverFingerprint {
		return nil
	}
	record.BaseURL = baseURL
	record.URLProved = true
	record.ServerFingerprint = r.serverFingerprint
	if err := saveCPAImportDestinationRecord(ctx, r.databasePath, r.dataKeyPath, record, managementKey); err != nil {
		return newUpgradeError(UpgradeErrorCPADestinationUnready,
			"the destination URL was proved to be the staged destination but could not be recorded, so this run stopped before reading or writing it", err)
	}
	return nil
}

// requireStagedDestinationIdentity binds the URL this run addresses to the
// destination the Supervisor staged, and is the last gate before anything is
// read from or written to that URL by the executor.
//
// cpa.VerifyV8Adoption reads the destination's client key list with the staged
// management secret and probes the listener without and with it. A CPA that
// answers is therefore one that accepts this run's staged secret; one that
// answers with another key set is refused. Either mismatch means the URL is not
// this operation's destination, so the refusal names a foreign target and the
// recorded operation stays where it was: no configuration or credential is
// written, and no CPA other than the staged destination is mutated.
//
// A URL that does not answer at all is a different outcome and is reported as
// one: the proof's read collapses a transport failure into the same refusal as a
// wrong key set, so it is classified by one unauthenticated probe before it is
// named. A destination that is not listening yet, or a stopped 1.x CPA at the URL
// the CLI defaulted to, is then not described as a foreign CPA. What the operator
// is told depends on the attempt: while no run proved a URL, the attempt holds
// none and a retry may name either this URL or the destination's, so the operator
// is sent to the destination's URL instead of being told to start the 1.x CPA;
// once a run proved this URL, the attempt is bound to it and only the same URL can
// be retried.
//
// The proof is never the first thing sent to the URL. It reads the destination's
// configuration with the secret this run minted, so it runs only after the
// unauthenticated probe has shown that the endpoint rejects both an absent and an
// unknown credential. An endpoint that answers with anything else never receives
// that secret, which is the secret the destination is deployed with. The probe
// cannot name the destination — only the staged secret can — so it is a
// precondition of the proof, not a replacement, and the URL is recorded as proved
// only after the proof passes.
//
// boundURL reports whether an earlier run already proved this URL to be the
// staged destination. It changes only what an unreachable URL tells the operator:
// a URL this attempt is bound to can only be retried as it is, because a different
// one is refused as a changed request.
func (r *cpaImportRun) requireStagedDestinationIdentity(ctx context.Context, baseURL, managementKey string, clientKeys []string, boundURL bool) error {
	if err := requireCredentialGatingListener(ctx, baseURL, boundURL); err != nil {
		return err
	}
	if err := cpa.VerifyV8Adoption(ctx, baseURL, managementKey, clientKeys); err != nil {
		return classifyDestinationProofFailure(ctx, baseURL, err, boundURL)
	}
	r.managementKey = managementKey
	return nil
}

// requireCredentialGatingListener refuses a URL that is silent or that answers
// either an absent or an unknown credential, before any credential of this import
// is sent to it.
//
// The endpoint is asked two questions that need no secret: `GET /v1/models` with
// no credential, and the same read with a fresh random value. A CPA answers 401 to
// both, because gateway admission rejects an absent or unknown key — but so does
// any other endpoint that gates credentials, and cpa.ProbeV8Listener cannot tell
// them apart (its file header records the v8.0.55 rejection body that was checked
// and cannot narrow it). An endpoint that accepts either one is not the destination
// this import stages — it cannot even be shown to enforce the authentication the
// staged destination was built with — and one that answers nothing cannot be
// proved at all.
func requireCredentialGatingListener(ctx context.Context, baseURL string, boundURL bool) error {
	probe, probeErr := cpa.ProbeV8Listener(ctx, baseURL)
	switch probe {
	case cpa.V8ListenerSilent:
		return unreachableDestinationError(probeErr, boundURL)
	case cpa.V8ListenerNotCredentialGated:
		return newUpgradeError(UpgradeErrorCPAImportTargetForeign,
			"the endpoint at the destination URL answered, but it did not reject both an absent and an unknown credential on GET /v1/models, so it is not the destination this import stages and the management secret this run staged was never sent to it; nothing was written to it", probeErr)
	default:
		return nil
	}
}

// unreachableDestinationError is the one wording for "nothing answered": the URL
// is not thereby proven wrong, and the operator must not be told to edit it.
//
// It separates the two states the attempt can be in, because they have different
// recoveries. While no run has proved a URL, the attempt holds none and a retry
// may name either this URL again or the destination's own URL, so the operator is
// told both — and told explicitly not to start the 1.x CPA the URL may name, since
// a started 1.x CPA answers the unauthenticated probe with 401 and is then handed
// the management secret this run staged. Once a run proved this URL, the attempt is
// bound to it and a different URL is refused as a changed request, so the only
// recovery is the same URL once the destination listens again.
func unreachableDestinationError(err error, boundURL bool) error {
	if boundURL {
		return newUpgradeError(UpgradeErrorCPAImportTargetUnreachable,
			"nothing answered at the destination URL, so this run could not confirm the destination an earlier run proved is still the CPA the Supervisor staged; an unreachable CPA is not a foreign one — it may still be starting, or its listener may have stopped — so wait for it to listen and re-run with the same URL; an earlier run proved this URL is the destination, so a different URL is refused as a changed request and this URL is kept; nothing was written to it",
			err)
	}
	return newUpgradeError(UpgradeErrorCPAImportTargetUnreachable,
		"nothing answered at the destination URL, so this run could not prove it is the destination the Supervisor staged; an unreachable endpoint is not a foreign one, and this URL is not known to be wrong — it may be the destination still starting, or a 1.x CPA that is already stopped, which is what the CLI's default CPA_UPSTREAM_URL usually names; re-run with the destination CPA's URL (--destination-url) once it listens, or with the same URL once it does, and never start the 1.x CPA to make this URL answer: a started 1.x CPA rejects an absent and an unknown credential, passes the probe and is then sent the management secret this run staged; no URL is recorded until a run proves one, so the same and a different URL are both still accepted, and nothing was written to it",
		err)
}

// classifyDestinationProofFailure names what a failed proof actually observed.
//
// The probe that decides it presents no credential, so it can only distinguish
// silence from an answer; everything that answered is a foreign target, exactly
// as the proof's own verdict says. Both refusals leave the recorded operation and
// the attempt where they were.
func classifyDestinationProofFailure(ctx context.Context, baseURL string, proofErr error, boundURL bool) error {
	probe, probeErr := cpa.ProbeV8Listener(ctx, baseURL)
	if probe == cpa.V8ListenerSilent {
		return unreachableDestinationError(errors.Join(proofErr, probeErr), boundURL)
	}
	return newUpgradeError(UpgradeErrorCPAImportTargetForeign,
		"the CPA at the destination URL does not answer with the management secret and client keys this run staged, so it is not the destination this operation staged; nothing was written to it", proofErr)
}

// submitDestination sends the recorded attempt to the Runtime and refuses a
// result it did not confirm.
//
// A terminal failure marks the attempt, so the next run knows it must supersede
// it with a new attempt ID instead of replaying a failure forever. No other
// outcome is treated as terminal: a transport error or an unconfirmed result may
// still belong to an operation the Supervisor is running.
func (r *cpaImportRun) submitDestination(
	ctx context.Context,
	record cpaImportDestinationRecord,
	managementKey string,
) error {
	intent := record.Intent
	// The plugin choice is part of the request the Supervisor published, and it has
	// no default: a record that does not hold the one it submitted cannot be
	// resubmitted without inventing a value.
	if intent.PluginsEnabled == nil {
		return newUpgradeError(UpgradeErrorCPADestinationRequestChanged,
			"the recorded destination attempt carries no plugin choice, so this build cannot prove the replacement is the request the Supervisor published; nothing was staged", nil)
	}
	request := model.RuntimeDestinationRequest{
		OperationID:               record.OperationID,
		ExpectedRuntimeIdentity:   intent.ExpectedRuntimeIdentity,
		ExpectedRuntimeGeneration: intent.ExpectedRuntimeGeneration,
		ExpectedActiveArtifactID:  intent.ExpectedActiveArtifactID,
		SourceIdentity:            intent.SourceIdentity,
		SourceClass:               intent.SourceClass,
		APIKeys:                   slices.Clone(intent.APIKeys),
		ManagementKey:             managementKey,
		PluginsEnabled:            *intent.PluginsEnabled,
	}
	staged, err := r.options.Runtime.StageDestination(ctx, request)
	if err != nil || staged.Validate() != nil || staged.OperationID != request.OperationID ||
		staged.OperationType != model.RuntimeOperationStageDestination || staged.RuntimeIdentity != request.ExpectedRuntimeIdentity ||
		staged.RuntimeGeneration != request.ExpectedRuntimeGeneration {
		return newUpgradeError(UpgradeErrorCPADestinationUnready, "the destination stage was not confirmed by the Runtime", err)
	}
	if staged.State == model.RuntimeOperationFailed {
		return errors.Join(
			r.markDestinationAttemptTerminal(ctx, record, managementKey),
			newUpgradeError(UpgradeErrorCPADestinationUnready, "the Runtime reported the destination stage as failed", nil),
		)
	}
	startRequest := model.RuntimeMutationRequest{
		OperationID:               record.OperationID + ":start",
		ExpectedRuntimeIdentity:   intent.ExpectedRuntimeIdentity,
		ExpectedRuntimeGeneration: intent.ExpectedRuntimeGeneration,
		ExpectedActiveArtifactID:  intent.ExpectedActiveArtifactID,
	}
	started, err := r.options.Runtime.Start(ctx, startRequest)
	if err != nil || started.Validate() != nil || started.OperationID != startRequest.OperationID ||
		started.OperationType != model.RuntimeOperationStart || started.RuntimeIdentity != startRequest.ExpectedRuntimeIdentity ||
		started.RuntimeGeneration != startRequest.ExpectedRuntimeGeneration {
		return newUpgradeError(UpgradeErrorCPADestinationUnready, "the destination start was not confirmed by the Runtime", err)
	}
	if started.State == model.RuntimeOperationFailed {
		return errors.Join(
			r.markDestinationAttemptTerminal(ctx, record, managementKey),
			newUpgradeError(UpgradeErrorCPADestinationUnready, "the Runtime reported the destination start as failed", nil),
		)
	}
	r.managementKey = managementKey
	return nil
}

// markDestinationAttemptTerminal records that the attempt reached a terminal
// failure, so the next run supersedes it instead of replaying it.
func (r *cpaImportRun) markDestinationAttemptTerminal(
	ctx context.Context,
	record cpaImportDestinationRecord,
	managementKey string,
) error {
	record.TerminalFailure = true
	if err := saveCPAImportDestinationRecord(ctx, r.databasePath, r.dataKeyPath, record, managementKey); err != nil {
		return newUpgradeError(UpgradeErrorCPADestinationUnready,
			"the terminal destination failure could not be recorded, so the next run cannot supersede this attempt", err)
	}
	return nil
}

// requireSameDestinationRequest refuses a run whose inputs would change the
// request the recorded attempt already submitted.
//
// The comparison runs before anything changes: before a new attempt ID is
// recorded and before a stage is submitted. A run that would drift is therefore
// refused with its operation, attempt and destination exactly as they were, and
// the Supervisor is never asked to reconcile a request it would reject. A ready
// destination is compared only on the inputs a staged destination owns — the
// proved URL, secret, source identity, client key set, plugin choice, Runtime
// identity, active artifact and source credentials digest — because it is
// proved, not resubmitted, and a restarted Supervisor must not turn a resumable
// destination into a refusal.
func requireSameDestinationRequest(
	record cpaImportDestinationRecord,
	intent cpaImportDestinationIntent,
	baseURL string,
	managementKey string,
	supplied string,
	sourceCredentialsDigest string,
	ready bool,
) error {
	refuse := func(what string) error {
		return newUpgradeError(UpgradeErrorCPADestinationRequestChanged, fmt.Sprintf(
			"an earlier run already staged this destination with %s; the Supervisor never applies a different request under a recorded operation, so this run refused before changing the attempt and wrote nothing",
			what), nil)
	}
	if record.urlProved() && record.BaseURL != baseURL {
		// The URL is not part of the Supervisor's request, so only a URL this
		// operation already proved to be the staged destination is immutable.
		// A URL that no run proved — including one recorded by a build that wrote
		// it before proving it — stays replaceable, so a corrected address can
		// still be bound instead of refusing the recovery forever.
		return refuse("a different destination URL: an earlier run proved this attempt is the CPA at another URL")
	}
	if supplied != "" && supplied != managementKey {
		return refuse("a different management secret")
	}
	if record.Intent.SourceIdentity != intent.SourceIdentity {
		return refuse("a different source identity: the 1.x configuration changed after the destination was staged")
	}
	if record.Intent.SourceClass != intent.SourceClass {
		return refuse("a different source class")
	}
	if !slices.Equal(record.Intent.APIKeys, intent.APIKeys) {
		return refuse("a different client key set")
	}
	// The Supervisor's request names the configuration digest and the client
	// keys, never the credential files. They are part of what this step hands the
	// destination, so a retry that read different credential bytes or a different
	// set of uploadable files would write credentials the recorded attempt never
	// described under an operation the Supervisor replays as identical.
	//
	// Every record reaching this comparison carries the request it submitted:
	// stageDestination fills in only the attempts whose full request was never
	// published, and stamps the credential digest in the same write. A complete
	// attempt without one was staged by a build that did not snapshot the
	// credential set, so nothing records what the destination was handed. The
	// digest of today's files would only describe today; recording it now would
	// sign the past stage with present bytes, and skipping the comparison would
	// copy whatever the files hold now under the old operation. Either way the
	// attempt is unprovable, so it is refused whether or not the files changed.
	if record.SourceCredentialsDigest == "" {
		return newUpgradeError(UpgradeErrorCPADestinationRequestChanged,
			"the recorded destination attempt was staged by a build that did not record a snapshot of the source credential files, so this build cannot prove a retry would hand the destination the credentials that attempt was staged with, and it does not re-derive that snapshot from the files as they are now; this run refused before contacting the destination and wrote nothing. To recover, roll this upgrade back (cpa-manager-plus upgrade rollback) and run it again with this build, which records the credential snapshot before anything is staged", nil)
	}
	if record.SourceCredentialsDigest != sourceCredentialsDigest {
		return refuse("different source credentials")
	}
	if !pluginsChoiceEqual(record.Intent.PluginsEnabled, intent.PluginsEnabled) {
		return refuse("a different plugin choice")
	}
	if record.Intent.ExpectedRuntimeIdentity != intent.ExpectedRuntimeIdentity {
		return refuse("a different Runtime identity")
	}
	if record.Intent.ExpectedActiveArtifactID != intent.ExpectedActiveArtifactID {
		return refuse("a different active Runtime artifact")
	}
	if ready {
		// A destination that is already answering is proved by
		// requireStagedDestinationIdentity, not resubmitted. A restarted Supervisor
		// reports a new generation and the destination resumes; that is a recovery
		// this run can still make because it submits nothing.
		return nil
	}
	if record.Intent.ExpectedRuntimeGeneration != intent.ExpectedRuntimeGeneration {
		return refuse("a different Runtime generation: the Supervisor restarted after this attempt was recorded, so the journalled operation belongs to an authority this run cannot address")
	}
	return nil
}

// runExecutor inventories the source and copies what the compiled authority
// authorizes.
//
// The executor is built over the verified authorization this run proved before
// staging, so every item the adoption rules mark migrate is copied into the
// destination and read back, and every item they do not is recorded with that
// rule's own manual action. Migrated items are counted, never listed: a
// credential identity is derived from its unscubbed name and must not cross the
// result boundary.
func (r *cpaImportRun) runExecutor(ctx context.Context, result *CPAImportResult) (freshstore.AdoptionSummary, error) {
	// The executor copies what its decisions mark migrate, and a decision set is
	// not the proof that produced it: the value a caller can build by hand and the
	// value NewAuthorization proves are indistinguishable once the decisions are
	// extracted. The proof is therefore asserted here, before the destination is
	// opened, so a run whose authority never ran writes nothing.
	if !r.auth.Verified() {
		return freshstore.AdoptionSummary{}, newUpgradeError(UpgradeErrorCPAImportUnproven,
			"this run holds no verified CPA adoption authority, so it cannot copy the source's configuration or credentials; nothing was read from the source and nothing was written to the destination", nil)
	}
	destination, err := freshadopt.NewDestination(ctx, freshadopt.Endpoint{BaseURL: r.options.DestinationURL, ManagementKey: r.managementKey})
	if err != nil {
		return freshstore.AdoptionSummary{}, newUpgradeError(UpgradeErrorCPADestinationUnready,
			"the destination CPA refused the import's management credential", err)
	}
	defer destination.Close()
	run, err := freshadopt.NewExecutor(r.source, destination, r.auth).Run(ctx, freshstore.AdoptionSummary{}, nil)
	if err != nil {
		if errors.Is(err, freshadopt.ErrInventoryUnavailable) {
			return run.Summary, newUpgradeError(UpgradeErrorCPAImportUnavailable,
				"the 1.x CPA inventory could not be read, so this run settled no item", err)
		}
		return run.Summary, newUpgradeError(UpgradeErrorCPAImportUnavailable, "the CPA import stopped before it settled every item", err)
	}
	for _, item := range run.Summary.Items {
		switch item.Outcome {
		case freshstore.AdoptionMigrated:
			result.MigratedCount++
		case freshstore.AdoptionManual:
			result.ManualItems = append(result.ManualItems, CPAImportManualItem{
				Item: item.Item, Reason: item.Reason, Instruction: item.Instruction,
			})
		}
	}
	return run.Summary, nil
}

// requireCPAImportSettled refuses a summary that is not a complete inventory.
// The executor resolves every item it attempted, so a failed outcome means the
// write's effect could not be read back and the run cannot claim the destination
// holds it: the operation stays at cpa_import without `cpa_config`.
func requireCPAImportSettled(summary freshstore.AdoptionSummary) error {
	for _, item := range summary.Items {
		if item.Outcome != freshstore.AdoptionFailed {
			continue
		}
		return newUpgradeError(UpgradeErrorCPAImportUnproven, fmt.Sprintf(
			"the inventory left the outcome of %s unknown (%s); the import stopped and the operation stays at %s",
			item.Item, item.Reason, model.UpgradeStageCPAImport), nil)
	}
	if !summary.Finished {
		return newUpgradeError(UpgradeErrorCPAImportUnproven,
			"the import recorded a partial inventory, so it did not finish; the operation stays at cpa_import", nil)
	}
	return nil
}

// enterCPAImportStage records the stage entry alone. It runs after the
// destination is staged, started and bound, because a refusal while staging must
// leave the recorded position untouched. It carries no domain: `cpa_config` is
// the claim that the destination holds the copied state, and only the completion
// below may make it.
func enterCPAImportStage(ctx context.Context, databasePath string) error {
	if err := requireRecoveredCopyIn(databasePath); err != nil {
		return err
	}
	db, err := managersqlite.Open(databasePath)
	if err != nil {
		return fmt.Errorf("open the v2 database to enter the CPA import: %w", err)
	}
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin the CPA import transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := upgradeoperation.AdvanceTx(ctx, tx, "", model.UpgradeStageCPAImport); err != nil {
		return fmt.Errorf("record the CPA import stage: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("record the CPA import stage: %w", err)
	}
	return nil
}

// recordCPAImportCompletion writes the manual items, the `cpa_config` domain and
// the destination evidence in one transaction, so a reader never sees the domain
// without the work it names or the target it proves.
//
// `cpa_config` is recorded here and nowhere else: it is the claim that the
// destination holds the copied state, and the executor has just settled every
// item. The destination evidence merged into `upgrade_cpa_import_destination_v1`
// in the same statement is what the completion gate re-proves against the live
// CPA, so a crash before this commit leaves `cpa_import` without the domain and
// the next run resumes the same attempt instead of advancing on work nothing
// recorded.
func recordCPAImportCompletion(ctx context.Context, databasePath string, result CPAImportResult) (model.UpgradeOperation, error) {
	if err := requireRecoveredCopyIn(databasePath); err != nil {
		return model.UpgradeOperation{}, err
	}
	// This step appends its instructions and replaces nothing. The append runs on
	// the row the repository reads inside the write transaction, so a rerun can
	// neither duplicate its own line nor drop one an earlier step wrote, and the
	// unchanged list is committed beside the domain that completed the step.
	lines := make([]string, 0, len(result.ManualItems))
	for _, item := range result.ManualItems {
		lines = append(lines, boundedManualItem(item))
	}
	db, err := managersqlite.Open(databasePath)
	if err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("open the v2 database to record the CPA import: %w", err)
	}
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("begin the CPA import transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := upgradeoperation.AdvanceTx(ctx, tx, model.UpgradeDomainCPAConfig, model.UpgradeStageCPAImport); err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("record the CPA import: %w; the manual items, the cpa_config domain and the destination evidence commit together, so the store is unchanged and a rerun records all of them", err)
	}
	advanced, err := upgradeoperation.AppendManualItemsTx(ctx, tx, lines)
	if err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("record the CPA import's manual items: %w", err)
	}
	if err := recordCPAImportDestinationEvidenceTx(ctx, tx, result); err != nil {
		return model.UpgradeOperation{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("record the CPA import: %w", err)
	}
	return advanced, nil
}

// restoreManualItems is the manual items a replay reports.
//
// The attempt row holds them as the step reported them, so a completed import that
// found N items answers N on every later run — including zero, because the row
// records that it reported them at all. A row written before the attempt kept them
// has only the operation row's rendered one-liners, and a replay must still report
// the items this step wrote — answering "manual items: 0" for a store that lists
// them would tell the operator the opposite of what happened. That fallback is
// filtered to the item prefixes this file's own manual items carry: the row is
// shared with every other step, so a reconcile acknowledgement or a copy-in
// instruction is not this step's report and must not be echoed back as one.
func restoreManualItems(recorded model.UpgradeOperation, attempt cpaImportDestinationRecord) []CPAImportManualItem {
	if attempt.ManualItemsRecorded || len(attempt.ManualItems) > 0 {
		return append([]CPAImportManualItem{}, attempt.ManualItems...)
	}
	items := make([]CPAImportManualItem, 0, len(recorded.ManualItems))
	for _, line := range recorded.ManualItems {
		parsed := parseManualItemLine(line)
		if !cpaImportOwnsManualItem(parsed.Item) {
			continue
		}
		items = append(items, parsed)
	}
	return items
}

// cpaImportManualItemPrefixes are the identity prefixes this step's manual items
// carry: the source's credential and configuration findings, the sections and
// leaves the adoption rules keep manual, and the digest-only identity an earlier
// executor gave an overlong credential name.
var cpaImportManualItemPrefixes = [...]string{"credential:", "config:", "section:", "rules:", "credential-name-sha256:"}

// cpaImportManualItemExactNames are the manual-item identities this step writes
// with no colon prefix: the executor's credential-excess summary and the source
// reader's unknown-config overflow summary.
const (
	cpaImportManualItemExcessCredential = "credential-inventory-excess"
	cpaImportManualItemExcessConfig     = "cpa-config-unknown-remainder"
)

// cpaImportOwnsManualItem reports whether one rendered line belongs to this
// step. A line another step wrote is not this step's report.
func cpaImportOwnsManualItem(item string) bool {
	if item == cpaImportManualItemExcessCredential || item == cpaImportManualItemExcessConfig {
		return true
	}
	for _, prefix := range cpaImportManualItemPrefixes {
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}
	return false
}

// parseManualItemLine reverses boundedManualItem where it can: the rendered line is
// `<item>: <instruction> (<reason>)`, and anything else is kept whole.
func parseManualItemLine(line string) CPAImportManualItem {
	item, rest, ok := strings.Cut(line, ": ")
	if !ok || strings.TrimSpace(item) == "" {
		return CPAImportManualItem{Item: line}
	}
	instruction := rest
	reason := ""
	if index := strings.LastIndex(rest, " ("); index >= 0 && strings.HasSuffix(rest, ")") {
		instruction = rest[:index]
		reason = rest[index+2 : len(rest)-1]
	}
	return CPAImportManualItem{Item: item, Reason: reason, Instruction: instruction}
}

// boundedManualItem renders one manual item for the operation row: bounded, in
// the same one-line shape the other steps write.
//
// Only the instruction is bounded. The item name and the typed reason are short
// and are the machine-readable half — the reason is the adoption rule's own
// manual action, which the operator and the wizard read back — so cutting them
// would make the row's own record unusable. The cut is still on a rune boundary:
// the instruction names a source path, which may be non-ASCII, and slicing bytes
// would put an invalid UTF-8 sequence in the operation row.
func boundedManualItem(item CPAImportManualItem) string {
	head := item.Item + ": "
	tail := ""
	if item.Reason != "" {
		tail = " (" + item.Reason + ")"
	}
	instruction := item.Instruction
	room := cpaImportManualItemLimit - len(head) - len(tail)
	if room < 0 {
		room = 0
	}
	if len(instruction) > room {
		cut := room
		for cut > 0 && !utf8.RuneStart(instruction[cut]) {
			cut--
		}
		instruction = instruction[:cut]
	}
	return head + instruction + tail
}

// cpaImportDestinationKeySetting is the settings row that carries the destination
// attempt this step already submitted.
//
// The stage hands the destination the only copy of its management secret it will
// ever hold, so the run records the attempt in the same Manager store that holds
// the operation row, before the stage: the secret, the full request the
// Supervisor published, the client key list and the operation ID it was staged
// under. A retry replays that exact request. The proved URL and the destination
// server fingerprint are merged into the same row when the destination answers,
// and they are the evidence the completion gate re-proves against the live CPA;
// clearing the row would let a rerun mint a second secret for a destination that
// already holds the first.
const cpaImportDestinationKeySetting = "upgrade_cpa_import_destination_v1"

// requireRecordedSourceRoot binds this run to the 1.x tree the copy-in read.
//
// The copy-in recorded the canonical root it read, and the CPA directory the
// import walks sits beside it. Without this comparison any tree the operator
// names — including another installation's — would be imported into an operation
// whose stored provenance names a different one. Both sides are canonicalised, so
// a relative or symlinked spelling of the recorded directory still matches.
//
// It runs before the CPA directory is opened, so a run pointed at another tree is
// refused without reading it.
func requireRecordedSourceRoot(recorded model.UpgradeOperation, legacyRoot string) error {
	named := strings.TrimSpace(legacyRoot)
	if named == "" {
		return newUpgradeError(UpgradeErrorCPASourceRootMismatch,
			"the CPA import did not name the 1.x source root the copy-in read, so it cannot be bound to the recorded operation; nothing was read or written", nil)
	}
	namedRoot := canonicalPath(named)
	if strings.TrimSpace(recorded.SourceRoot) == "" {
		return newUpgradeError(UpgradeErrorCPASourceRootMismatch,
			"the recorded operation names no 1.x source root, so the import cannot be bound to the tree the copy-in read; nothing was read or written", nil)
	}
	recordedRoot := canonicalPath(recorded.SourceRoot)
	if namedRoot != recordedRoot {
		return newUpgradeError(UpgradeErrorCPASourceRootMismatch,
			"the named 1.x source root is not the directory this operation's copy-in read, so its CPA state belongs to another installation; nothing was read or written", nil)
	}
	return nil
}

// newCPAImportManagementSecret returns a fresh 64-hex destination management
// secret, the shape the Runtime destination request requires.
func newCPAImportManagementSecret() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}
