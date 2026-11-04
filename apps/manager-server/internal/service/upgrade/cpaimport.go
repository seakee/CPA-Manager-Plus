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
//   - inventories the source through the existing freshadopt.Executor with no
//     migration decision, so every provider-configuration and credential item is
//     recorded as deferred manual work and nothing is written;
//   - hands the Manager's own connection the destination URL and the secret the
//     destination was staged with.
//
// # Why nothing is copied
//
// Provider configuration and OAuth credentials may only be copied on a trusted
// qualification proof, and the Upgrade surface has no qualification record. The
// previous version of this step invented an Upgrade authorization that named the
// references to copy and ran the executor under it; that made a bare reference set
// the authority for unproven migration. The executor now serves exactly one
// authority — the Fresh decisions a qualification produced — and this step passes
// none, so every item is recorded with CPAImportRequiresQualification and the
// automatic copy is deferred to the trusted re-pin step (U1-1b).
//
// # What is never inherited
//
// The destination keeps v2's own management secret, listener, plugin root, auth
// directory and client key list. This step writes the operation row, the
// destination attempt and the Manager's connection; it never writes the
// destination's identity.
//
// # Fail-closed rules
//
//   - the 1.x ownership marker is required, must belong to the recorded source,
//     and is held for the whole read path through a read-only descriptor;
//   - the version gate runs before anything is written;
//   - an absent plugin choice refuses the run — it is never defaulted;
//   - a destination URL that does not prove itself as the staged destination is
//     refused before the executor reads or writes anything;
//   - an unknown source key becomes a manual item, never a silent drop;
//   - the cpa_config domain is not recorded, because this build copies nothing:
//     the operation stays at cpa_import and the manual items are the operator's
//     work until the re-pin step copies them.

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
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshadopt"
)

// cpaImportManualItemLimit bounds one manual item line in the operation row.
const cpaImportManualItemLimit = 400

// CPAImportRequiresQualification is the stable reason every source item this
// build defers carries. Provider configuration and OAuth credentials may only be
// copied on a trusted qualification proof, which the Upgrade surface does not
// hold, so the item is recorded as manual work with this code instead of being
// written. The trusted re-pin step (U1-1b) is the step that will copy it.
const CPAImportRequiresQualification = "upgrade_cpa_import_requires_qualification"

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
	// ManualItems are the source items this build did not copy. Until the trusted
	// re-pin step exists, that is every provider-configuration and credential item
	// the source holds, each recorded with CPAImportRequiresQualification.
	ManualItems []CPAImportManualItem `json:"manualItems"`
	// AlreadyComplete reports that the recorded operation already held this
	// import, so nothing was read from the source or written to the destination.
	AlreadyComplete bool  `json:"alreadyComplete"`
	UpdatedAtMS     int64 `json:"updatedAtMs"`
}

// cpaImportRun is the state one run needs beyond its reported result. It holds
// the destination management secret, which the result must never carry.
type cpaImportRun struct {
	options       CPAImportOptions
	source        *cpaManagedSource
	databasePath  string
	dataKeyPath   string
	managementKey string
}

// ImportCPA prepares the v2 destination for a 1.x stack's managed CPA state
// while the caller holds the 1.x ownership marker.
//
// The order is fixed: refuse a recorded position that cannot enter the import,
// read the source and gate its version, refuse a missing choice, prove the
// destination is observable, stage and start it, bind the caller's URL to that
// destination, and only then enter the cpa_import stage. The source is then
// inventoried through the adoption executor with no migration decision, so every
// configuration and credential item is recorded as deferred manual work and
// nothing is written; the Manager's connection takes the destination and the
// secret it was staged with. The cpa_config domain is never recorded, because
// this build copies nothing: a refusal before the destination is staged leaves
// the operation at the position it was entered from, and a failure after the
// stage entry leaves it at cpa_import without the domain, which is the position
// the next run resumes from.
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
	// not a position to refuse.
	if recorded.Stage == model.UpgradeStageCPAImport && recorded.HasDomain(model.UpgradeDomainCPAConfig) {
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
	run := &cpaImportRun{options: options, source: source, databasePath: paths.Database, dataKeyPath: paths.DataKey}
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

	// The Manager's own connection takes the destination and the same secret this
	// run staged with, before the operation row records the deferral. A crash
	// between this write and the next one leaves both the connection and the
	// recorded attempt, so a rerun finishes. The attempt row stays for the same
	// reason: this build never records cpa_config, so a rerun must replay the
	// recorded request and the recorded secret rather than mint a new one.
	if err := handDestinationConnection(ctx, paths.Database, paths.DataKey, result.DestinationURL, run.managementKey); err != nil {
		return result, err
	}
	recorded, err = recordCPAImportDeferral(ctx, paths.Database, result)
	if err != nil {
		return result, err
	}
	result.Stage = recorded.Stage
	result.DomainsDone = recorded.DomainsDone
	result.UpdatedAtMS = recorded.UpdatedAtMS
	return result, nil
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
// a ready destination, a resumed attempt, a new stage — ends in
// requireStagedDestinationIdentity, so the URL is proved to be the staged
// destination with this import's client keys before the executor is built; a
// foreign CPA is refused instead of receiving source configuration and
// credentials.
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
	clientKeys := slices.Clone(r.source.clientKeys)
	supplied := strings.TrimSpace(r.options.DestinationManagementKey)
	// The whole request this run would submit is fixed here, before the recorded
	// attempt is read: the Supervisor compares every field of a replacement
	// attempt against the published one, so the retry has to compare the same set
	// itself. SourceIdentity is the source's own file digest, so it changes when
	// the 1.x configuration changes, and the three Runtime fields change when the
	// Supervisor restarts or is updated.
	intent := cpaImportDestinationIntent{
		ExpectedRuntimeIdentity:   status.Identity,
		ExpectedRuntimeGeneration: status.Generation,
		ExpectedActiveArtifactID:  status.ActiveGatewayArtifact.ArtifactID,
		SourceIdentity:            r.source.sourceIdentity(),
		SourceClass:               "legacy_managed",
		APIKeys:                   slices.Clone(clientKeys),
		PluginsEnabled:            result.PluginsEnabled,
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
				BaseURL:     record.BaseURL,
				Intent:      intent,
				OperationID: cpaImportDestinationOperationID(intent.SourceIdentity, 1),
				Attempt:     1,
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
			if err := saveCPAImportDestinationRecord(ctx, r.databasePath, r.dataKeyPath, record, recordedKey); err != nil {
				return newUpgradeError(UpgradeErrorCPADestinationUnready,
					"the recorded destination attempt could not be completed with this run's request, so nothing was staged", err)
			}
		}
		if err := requireSameDestinationRequest(record, intent, baseURL, recordedKey, supplied, status.State == model.RuntimeStateReady); err != nil {
			return err
		}
		if status.State == model.RuntimeStateReady {
			if err := r.requireStagedDestinationIdentity(ctx, baseURL, recordedKey, clientKeys, record.bindsURL(baseURL)); err != nil {
				return err
			}
			return r.recordProvedDestination(ctx, record, baseURL, recordedKey)
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
		if err := r.requireStagedDestinationIdentity(ctx, baseURL, recordedKey, clientKeys, record.bindsURL(baseURL)); err != nil {
			return err
		}
		return r.recordProvedDestination(ctx, record, baseURL, recordedKey)
	}

	if status.State == model.RuntimeStateReady {
		// The Supervisor's CPA is already the staged destination and this Manager
		// did not stage it. Its secret is the caller's to supply — the import never
		// invents a second secret for a destination it did not stage — and the
		// target is proved before the executor writes anything.
		if supplied == "" {
			return newUpgradeError(UpgradeErrorCPADestinationKeyRequired,
				"the destination CPA is already running and this run holds no management secret for it; pass the secret the Manager stored with --destination-management-key-file, or stage the destination from a stopped state", nil)
		}
		return r.requireStagedDestinationIdentity(ctx, baseURL, supplied, clientKeys, false)
	}

	managementKey := supplied
	if managementKey == "" {
		if managementKey, err = newCPAImportManagementSecret(); err != nil {
			return newUpgradeError(UpgradeErrorCPADestinationUnready, "a destination management secret could not be generated", err)
		}
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
		Intent:      intent,
		OperationID: cpaImportDestinationOperationID(intent.SourceIdentity, 1),
		Attempt:     1,
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
	if err := r.requireStagedDestinationIdentity(ctx, baseURL, managementKey, clientKeys, false); err != nil {
		return err
	}
	return r.recordProvedDestination(ctx, record, baseURL, managementKey)
}

// recordProvedDestination records that the caller's URL answered as the
// destination this attempt staged.
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
	if record.urlProved() && record.BaseURL == baseURL {
		return nil
	}
	record.BaseURL = baseURL
	record.URLProved = true
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
// them apart (its file header records the v8.0.13 rejection body that was checked
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
	request := model.RuntimeDestinationRequest{
		OperationID:               record.OperationID,
		ExpectedRuntimeIdentity:   intent.ExpectedRuntimeIdentity,
		ExpectedRuntimeGeneration: intent.ExpectedRuntimeGeneration,
		ExpectedActiveArtifactID:  intent.ExpectedActiveArtifactID,
		SourceIdentity:            intent.SourceIdentity,
		SourceClass:               intent.SourceClass,
		APIKeys:                   slices.Clone(intent.APIKeys),
		ManagementKey:             managementKey,
		PluginsEnabled:            intent.PluginsEnabled,
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
// proved URL, secret, source identity, key set and plugin choice — because it is
// proved, not resubmitted, and a restarted Supervisor must not turn a resumable
// destination into a refusal.
func requireSameDestinationRequest(
	record cpaImportDestinationRecord,
	intent cpaImportDestinationIntent,
	baseURL string,
	managementKey string,
	supplied string,
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
	if record.Intent.PluginsEnabled != intent.PluginsEnabled {
		return refuse("a different plugin choice")
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
	if record.Intent.ExpectedRuntimeIdentity != intent.ExpectedRuntimeIdentity {
		return refuse("a different Runtime identity")
	}
	if record.Intent.ExpectedActiveArtifactID != intent.ExpectedActiveArtifactID {
		return refuse("a different active Runtime artifact")
	}
	return nil
}

// runExecutor inventories the source and records every item as deferred manual
// work.
//
// The executor is built with no migration decision at all. That is the only
// authority it serves: a Fresh adoption's qualification produces per-reference
// decisions, and the Upgrade surface holds none, so passing none makes every
// configuration and credential item resolve to the rule table's manual action and
// nothing is written. The item identity and the executor's own instruction are
// kept, so the operator still sees exactly what has to be copied; the reason is
// replaced with CPAImportRequiresQualification, which is the typed statement that
// the automatic copy waits for the trusted re-pin step.
func (r *cpaImportRun) runExecutor(ctx context.Context, result *CPAImportResult) (freshstore.AdoptionSummary, error) {
	destination, err := freshadopt.NewDestination(ctx, freshadopt.Endpoint{BaseURL: r.options.DestinationURL, ManagementKey: r.managementKey})
	if err != nil {
		return freshstore.AdoptionSummary{}, newUpgradeError(UpgradeErrorCPADestinationUnready,
			"the destination CPA refused the import's management credential", err)
	}
	defer destination.Close()
	run, err := freshadopt.New(r.source, destination, nil).Run(ctx, freshstore.AdoptionSummary{}, nil)
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
			// With no decision the executor copies nothing; a migrated item would
			// mean it wrote without an authority, which this step must never report
			// as a successful inventory.
			return run.Summary, newUpgradeError(UpgradeErrorCPAImportUnproven,
				"the executor copied an item without a migration decision, so the import cannot claim it wrote nothing", nil)
		case freshstore.AdoptionManual:
			result.ManualItems = append(result.ManualItems, CPAImportManualItem{
				Item: item.Item, Reason: CPAImportRequiresQualification, Instruction: item.Instruction,
			})
		}
	}
	return run.Summary, nil
}

// requireCPAImportSettled refuses a summary that is not a complete inventory.
// Nothing is written by this step, so an item with a failed outcome can only mean
// the executor attempted work it had no authority for: the run stops and the
// operation stays at cpa_import.
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
// leave the recorded position untouched. It carries no domain: the cpa_config
// domain is the claim that the destination holds the copied state, and this build
// does not copy, so it never records that domain.
func enterCPAImportStage(ctx context.Context, databasePath string) error {
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

// recordCPAImportDeferral writes the manual items and the cpa_import stage in one
// transaction, so a reader never sees the stage without the work it names.
//
// It records no domain. The step staged and bound the destination and inventoried
// the source, but it copied nothing, and cpa_config is the claim that the
// destination holds the copied state — a claim only a copy can make. Keeping the
// operation at cpa_import without the domain is what makes the next run resume the
// same attempt instead of advancing a pipeline on work nothing performed. The
// destination attempt row is kept for the same reason: a rerun replays the
// recorded request and secret, and clearing it would let the run mint a second
// secret for a destination that already holds the first.
func recordCPAImportDeferral(ctx context.Context, databasePath string, result CPAImportResult) (model.UpgradeOperation, error) {
	// This step appends its instructions and replaces nothing. The append runs on
	// the row the repository reads inside the write transaction, so a rerun can
	// neither duplicate its own line nor drop one an earlier step wrote, and the
	// unchanged list is committed beside the stage that deferred the work.
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
	if _, err := upgradeoperation.AdvanceTx(ctx, tx, "", model.UpgradeStageCPAImport); err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("record the CPA import: %w; the manual items and the stage commit together, so the store is unchanged and a rerun records all of them", err)
	}
	advanced, err := upgradeoperation.AppendManualItemsTx(ctx, tx, lines)
	if err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("record the CPA import's manual items: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("record the CPA import: %w", err)
	}
	return advanced, nil
}

// boundedManualItem renders one manual item for the operation row: bounded, in
// the same one-line shape the other steps write.
//
// Only the instruction is bounded. The item name and the typed reason are short
// and are the machine-readable half — the wizard reads the reason back to tell a
// deferred import from one that never ran — so cutting them would make the row's
// own record unusable. The cut is still on a rune boundary: the instruction names
// a source path, which may be non-ASCII, and slicing bytes would put an invalid
// UTF-8 sequence in the operation row.
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
// under. A retry replays that exact request. The row is kept because this build
// never records cpa_config: a rerun still has to replay the recorded secret, and
// clearing the row would let it mint a second secret for a destination that
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
