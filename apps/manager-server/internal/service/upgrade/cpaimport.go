package upgrade

// This file is the Managed CPA import: the Upgrade step that carries a stopped
// 1.x stack's CPA state into the v2 destination.
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
//   - copies configuration and credentials through the existing
//     freshadopt.Executor into the destination CPA;
//   - proves the target twice: cpa.VerifyV8Adoption over the imported client
//     keys, and a readback of every copied path through the destination's own
//     configuration surface.
//
// # What is never inherited
//
// The destination keeps v2's own management secret, listener, plugin root, auth
// directory and client key list. This step writes configuration and credential
// files; it never writes the destination's identity, and it never copies a source
// key under one of the excluded sections.
//
// # Fail-closed rules
//
//   - the version gate runs before anything is written;
//   - an absent plugin choice refuses the run — it is never defaulted;
//   - an item that does not read back fails the step and leaves the operation at
//     cpa_import, with the cpa_config domain unrecorded;
//   - an unknown source key becomes a manual item, never a silent drop.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshadopt"
)

// CPAImportOptions is the input of one Managed CPA import. The three arms of the
// run — the stopped source, the destination and the choice — are explicit,
// because none of them has a correct default.
type CPAImportOptions struct {
	// LegacyRoot is the 1.x data directory the copy-in read. It locates the
	// managed CPA directory beside it and must already hold a recorded copy-in.
	LegacyRoot string
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
	// destination is already staged and running. A run that stages the destination
	// itself generates the secret; a run that finds it ready must be told the
	// secret the Manager stored, because the import never invents a second one for
	// a destination it did not stage.
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
	// ClientKeys counts the client keys the destination was proved with. The keys
	// themselves never leave this process.
	ClientKeys int `json:"clientKeys"`
	// CopiedConfig names every destination path this run wrote and read back:
	// the provider sections, the rule families and the gateway leaves.
	CopiedConfig []string `json:"copiedConfig"`
	// CopiedCredentials names every credential file the destination holds with the
	// bytes the source held.
	CopiedCredentials []string `json:"copiedCredentials"`
	// ManualItems are the source items the import deliberately did not copy.
	ManualItems []CPAImportManualItem `json:"manualItems"`
	// AlreadyComplete reports that the recorded operation already held this
	// import, so nothing was read from the source or written to the destination.
	AlreadyComplete bool  `json:"alreadyComplete"`
	UpdatedAtMS     int64 `json:"updatedAtMs"`
}

// cpaImportRun is the state one run needs beyond its reported result. It holds
// the destination management secret and the source client keys, which the result
// must never carry.
type cpaImportRun struct {
	options        CPAImportOptions
	source         *cpaManagedSource
	managementKey  string
	stagedByRun    bool
	copiedConfig   []string
	copiedCreds    []string
	verificationID string
}

// ImportCPA carries a stopped 1.x stack's managed CPA state into the v2
// destination.
//
// The order is fixed: read the source and gate its version, refuse a missing
// choice, prove the destination is observable, stage and start it, import the
// configuration and credentials through the adoption executor, prove the target
// with the imported keys, read every copied path back, and only then record the
// cpa_config domain and the cpa_import stage. Every refusal before the destination
// is written leaves the operation where it was.
func ImportCPA(ctx context.Context, options CPAImportOptions) (CPAImportResult, error) {
	if err := ctx.Err(); err != nil {
		return CPAImportResult{}, err
	}
	result := CPAImportResult{DomainsDone: []string{}, CopiedConfig: []string{}, CopiedCredentials: []string{}, ManualItems: []CPAImportManualItem{}}
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
	if targetRoot, err = canonicalPath(targetRoot); err != nil {
		return result, fmt.Errorf("resolve the v2 data root: %w", err)
	}
	paths := newTargetPaths(targetRoot)
	recorded, found, err := readRecordedCopyIn(ctx, paths.Database)
	if err != nil {
		return result, err
	}
	if !found {
		return result, errors.New("the v2 data root holds no recorded copy-in, so there is no CPA state to import")
	}
	// The step may only run where it could still record its result. A store the
	// operation already carried past the import cannot take this stage, so
	// importing into it would write configuration for an operation that can never
	// record the work.
	if recorded.Stage != "" && !recorded.Stage.CanAdvanceTo(model.UpgradeStageCPAImport) {
		return result, newUpgradeError(UpgradeErrorCPAStageConflict, fmt.Sprintf(
			"the recorded operation is at stage %q, which cannot enter the CPA import; nothing was written", recorded.Stage), nil)
	}
	if recorded.Stage == model.UpgradeStageCPAImport && recorded.HasDomain(model.UpgradeDomainCPAConfig) {
		// The import already ran and was recorded. Its result is the row, and
		// re-running it would rewrite a destination a later step may already have
		// advanced.
		result.Stage = recorded.Stage
		result.DomainsDone = slices.Clone(recorded.DomainsDone)
		result.UpdatedAtMS = recorded.UpdatedAtMS
		result.AlreadyComplete = true
		return result, nil
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
	run := &cpaImportRun{options: options, source: source}
	result.SourceVersion = source.version
	result.SourceLayout = source.layout
	result.SourceIdentity = source.sourceIdentity()
	result.ClientKeys = len(source.clientKeys)
	for _, item := range source.manual {
		result.ManualItems = append(result.ManualItems, CPAImportManualItem{Item: item.item, Reason: item.reason, Instruction: item.instruction})
	}

	// The stage is entered before the destination is written, so a failure below
	// leaves the operation at cpa_import — the position the operator retries from
	// — instead of pretending the step never started. The version gate above has
	// already passed, so nothing was written for a source this import refuses.
	if err := enterCPAImportStage(ctx, paths.Database); err != nil {
		return result, err
	}
	if err := run.stageDestination(ctx, &result); err != nil {
		return result, err
	}
	summary, err := run.runExecutor(ctx, &result)
	if err != nil {
		return result, err
	}
	if err := requireCPAImportSettled(summary); err != nil {
		return result, err
	}
	if err := run.proveTarget(ctx, &result); err != nil {
		return result, err
	}
	result.CopiedConfig = slices.Clone(run.copiedConfig)
	result.CopiedCredentials = slices.Clone(run.copiedCreds)

	recorded, err = recordCPAImport(ctx, paths.Database, result)
	if err != nil {
		return result, err
	}
	result.Stage = recorded.Stage
	result.DomainsDone = recorded.DomainsDone
	result.UpdatedAtMS = recorded.UpdatedAtMS
	return result, nil
}

// stageDestination proves the destination is observable, then stages and starts
// it with this run's keys and plugin choice.
//
// A destination that already reports itself ready is not staged again: the
// Supervisor journals a terminal destination operation and never resumes it, and a
// second stage would rewrite the identity proof the first one established. The
// proof below re-runs either way, so "already staged" is a claim this step still
// verifies.
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
	if status.State == model.RuntimeStateReady {
		// The Supervisor's CPA is already the staged destination. Its management
		// secret is the one this Manager stored for it, so the caller supplies it;
		// the run never invents a second secret for a destination it did not stage.
		if strings.TrimSpace(r.options.DestinationManagementKey) == "" {
			return newUpgradeError(UpgradeErrorCPADestinationUnready,
				"the destination CPA is already running and its management secret was not supplied, so the import cannot prove it", nil)
		}
		r.managementKey = r.options.DestinationManagementKey
		return nil
	}
	managementKey, err := newCPAImportManagementSecret()
	if err != nil {
		return newUpgradeError(UpgradeErrorCPADestinationUnready, "a destination management secret could not be generated", err)
	}
	request := model.RuntimeDestinationRequest{
		OperationID:               "upgrade-cpa-import:" + digestBytes([]byte(result.SourceIdentity))[:32],
		ExpectedRuntimeIdentity:   status.Identity,
		ExpectedRuntimeGeneration: status.Generation,
		ExpectedActiveArtifactID:  status.ActiveGatewayArtifact.ArtifactID,
		SourceIdentity:            result.SourceIdentity,
		SourceClass:               "legacy_managed",
		APIKeys:                   slices.Clone(r.source.clientKeys),
		ManagementKey:             managementKey,
		PluginsEnabled:            result.PluginsEnabled,
	}
	staged, err := r.options.Runtime.StageDestination(ctx, request)
	if err != nil || staged.Validate() != nil || staged.OperationID != request.OperationID ||
		staged.OperationType != model.RuntimeOperationStageDestination || staged.RuntimeIdentity != request.ExpectedRuntimeIdentity ||
		staged.RuntimeGeneration != request.ExpectedRuntimeGeneration {
		return newUpgradeError(UpgradeErrorCPADestinationUnready, "the destination stage was not confirmed by the Runtime", err)
	}
	if staged.State == model.RuntimeOperationFailed {
		return newUpgradeError(UpgradeErrorCPADestinationUnready, "the Runtime reported the destination stage as failed", nil)
	}
	startRequest := model.RuntimeMutationRequest{
		OperationID:               request.OperationID + ":start",
		ExpectedRuntimeIdentity:   status.Identity,
		ExpectedRuntimeGeneration: status.Generation,
		ExpectedActiveArtifactID:  status.ActiveGatewayArtifact.ArtifactID,
	}
	started, err := r.options.Runtime.Start(ctx, startRequest)
	if err != nil || started.Validate() != nil || started.OperationID != startRequest.OperationID ||
		started.OperationType != model.RuntimeOperationStart || started.RuntimeIdentity != startRequest.ExpectedRuntimeIdentity ||
		started.RuntimeGeneration != startRequest.ExpectedRuntimeGeneration {
		return newUpgradeError(UpgradeErrorCPADestinationUnready, "the destination start was not confirmed by the Runtime", err)
	}
	if started.State == model.RuntimeOperationFailed {
		return newUpgradeError(UpgradeErrorCPADestinationUnready, "the Runtime reported the destination start as failed", nil)
	}
	r.managementKey = managementKey
	r.stagedByRun = true
	return nil
}

// runExecutor copies the mapped configuration and credentials into the
// destination and records one outcome per item.
func (r *cpaImportRun) runExecutor(ctx context.Context, result *CPAImportResult) (freshstore.AdoptionSummary, error) {
	destination, err := freshadopt.NewDestination(ctx, freshadopt.Endpoint{BaseURL: r.options.DestinationURL, ManagementKey: r.managementKey})
	if err != nil {
		return freshstore.AdoptionSummary{}, newUpgradeError(UpgradeErrorCPADestinationUnready,
			"the destination CPA refused the import's management credential", err)
	}
	defer destination.Close()
	run, err := freshadopt.New(r.source, destination, cpaImportDecisions()).Run(ctx, freshstore.AdoptionSummary{}, nil)
	if err != nil {
		if errors.Is(err, freshadopt.ErrInventoryUnavailable) {
			return run.Summary, newUpgradeError(UpgradeErrorCPAImportUnavailable,
				"the 1.x CPA inventory could not be read, so this run settled no item and the operation did not advance", err)
		}
		return run.Summary, newUpgradeError(UpgradeErrorCPAImportUnavailable, "the CPA import stopped before it settled every item", err)
	}
	for _, item := range run.Summary.Items {
		switch item.Outcome {
		case freshstore.AdoptionMigrated:
			if name, ok := strings.CutPrefix(item.Item, "credential:"); ok {
				r.copiedCreds = append(r.copiedCreds, name)
				continue
			}
			if path, ok := cpaImportConfigPathOf(item.Item); ok && !slices.Contains(r.copiedConfig, path) {
				r.copiedConfig = append(r.copiedConfig, path)
			}
		case freshstore.AdoptionManual:
			result.ManualItems = append(result.ManualItems, CPAImportManualItem{
				Item: item.Item, Reason: item.Reason, Instruction: item.Instruction,
			})
		}
	}
	return run.Summary, nil
}

// cpaImportConfigPathOf resolves the destination configuration path of one
// migrated item identity.
func cpaImportConfigPathOf(item string) (string, bool) {
	for _, prefix := range []string{"config:", "section:"} {
		if path, ok := strings.CutPrefix(item, prefix); ok {
			return path, true
		}
	}
	if family, ok := strings.CutPrefix(item, "rules:"); ok {
		if entry, found := cpa.LookupConfigRuleFamily(family); found {
			return entry.Path, true
		}
		return "", false
	}
	return "", false
}

// requireCPAImportSettled refuses a run that left an item's effect unknown. A
// failed item is an unknown destination state: resolving it needs the next run's
// readback, and claiming the stage while it stands would record a copy nobody
// observed.
func requireCPAImportSettled(summary freshstore.AdoptionSummary) error {
	for _, item := range summary.Items {
		if item.Outcome != freshstore.AdoptionFailed {
			continue
		}
		return newUpgradeError(UpgradeErrorCPAImportUnproven, fmt.Sprintf(
			"the destination did not confirm the copy of %s (%s); the import stopped and the operation stays at %s",
			item.Item, item.Reason, model.UpgradeStageCPAImport), nil)
	}
	if !summary.Finished {
		return newUpgradeError(UpgradeErrorCPAImportUnproven,
			"the import recorded a partial plan, so it did not finish; the operation stays at cpa_import", nil)
	}
	return nil
}

// proveTarget reads the target back: the imported client keys through
// cpa.VerifyV8Adoption, then every copied path and credential through the
// destination's own surface.
func (r *cpaImportRun) proveTarget(ctx context.Context, result *CPAImportResult) error {
	if err := cpa.VerifyV8Adoption(ctx, result.DestinationURL, r.managementKey, r.source.clientKeys); err != nil {
		return newUpgradeError(UpgradeErrorCPAImportUnproven,
			"the destination CPA does not answer with the imported client keys, so the import is not proven", err)
	}
	destination, err := freshadopt.NewDestination(ctx, freshadopt.Endpoint{BaseURL: result.DestinationURL, ManagementKey: r.managementKey})
	if err != nil {
		return newUpgradeError(UpgradeErrorCPADestinationUnready,
			"the destination CPA refused the import's management credential", err)
	}
	defer destination.Close()
	for _, path := range r.copiedConfig {
		expected, sourcePresence := lookupDocumentPath(r.source.document, path)
		if sourcePresence != documentPresent {
			return newUpgradeError(UpgradeErrorCPAImportUnproven,
				"the source no longer holds a path this run recorded as copied", nil)
		}
		observed, destinationPresence, err := destination.ReadConfigValue(ctx, path)
		if err != nil || destinationPresence != cpa.ConfigPresent || !cpaImportValuesEqual(expected, observed) {
			return newUpgradeError(UpgradeErrorCPAImportUnproven, fmt.Sprintf(
				"the destination does not read %s back as it was written, so the import is not proven", path), err)
		}
	}
	for _, name := range r.copiedCreds {
		readBack, err := destination.DownloadCredential(ctx, name)
		if err != nil {
			return newUpgradeError(UpgradeErrorCPAImportUnproven, fmt.Sprintf(
				"the destination does not hold the credential file %s this run copied", name), err)
		}
		if digestBytes(readBack) != digestBytes(r.source.credentialData[name]) {
			return newUpgradeError(UpgradeErrorCPAImportUnproven, fmt.Sprintf(
				"the credential file %s does not read back byte-identical from the destination", name), nil)
		}
	}
	return nil
}

// cpaImportValuesEqual compares a source value with a destination read-back by
// canonical JSON, so a value that crossed the YAML and JSON boundaries still
// equals itself.
func cpaImportValuesEqual(source, destination any) bool {
	left, leftErr := json.Marshal(normalizeDocumentValue(source))
	right, rightErr := json.Marshal(normalizeDocumentValue(destination))
	return leftErr == nil && rightErr == nil && string(left) == string(right)
}

// enterCPAImportStage records the stage entry alone. It carries no domain: the
// cpa_config domain is the step's claim that the destination holds the copied
// state, and only the proof below may make that claim.
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

// recordCPAImport writes the manual items, the cpa_config domain and the
// cpa_import stage in one transaction, so a reader never sees the stage without
// the work it names.
func recordCPAImport(ctx context.Context, databasePath string, result CPAImportResult) (model.UpgradeOperation, error) {
	// The earlier steps' manual items keep their place: this step appends its own
	// instructions and replaces nothing. The read happens before the write
	// transaction opens, so one connection never waits on another in the same file.
	previous, previousFound, err := recordedOperation(ctx, databasePath, false)
	if err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("read the recorded operation: %w", err)
	}
	merged := []string{}
	if previousFound {
		merged = append(merged, previous.ManualItems...)
	}
	for _, item := range result.ManualItems {
		line := boundedManualItem(item)
		if !slices.Contains(merged, line) {
			merged = append(merged, line)
		}
	}
	payload, err := json.Marshal(merged)
	if err != nil {
		return model.UpgradeOperation{}, err
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
	advanced, err := upgradeoperation.AdvanceTx(ctx, tx, model.UpgradeDomainCPAConfig, model.UpgradeStageCPAImport)
	if err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("record the CPA import: %w; the manual items and the stage commit together, so the store is unchanged and a rerun records all of them", err)
	}
	if _, err := tx.ExecContext(ctx, `update upgrade_operations set manual_items = ? where id = 1`, string(payload)); err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("record the CPA import's manual items: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("record the CPA import: %w", err)
	}
	advanced.ManualItems = merged
	return advanced, nil
}

// boundedManualItem renders one manual item for the operation row: bounded, in
// the same one-line shape the other steps write.
func boundedManualItem(item CPAImportManualItem) string {
	line := item.Item + ": " + item.Instruction
	if item.Reason != "" {
		line += " (" + item.Reason + ")"
	}
	if len(line) > 400 {
		line = line[:400]
	}
	return line
}

// cpaImportDecisions is the record authorization this import runs under.
//
// An Upgrade import of a Managed 1.x stack is authorized by the operator running
// the step, not by a Fresh adoption record: there is no Adopt screen in the
// Upgrade surface and no per-reference decision to look up. It therefore
// authorizes exactly the references the adoption executor copies — the gateway
// configuration, the provider sections and the credential files — through the
// product's own rule table, and nothing else. Every other reference keeps the
// manual action `bootstrap.FreshAdoptionDecision` derives for it, so an item the
// executor classifies outside those three references is never written.
func cpaImportDecisions() []bootstrap.AdoptionResourceDecision {
	refs := []bootstrap.ResourceRef{
		bootstrap.AdoptionRefConfigRouting,
		bootstrap.AdoptionRefProviderStateAPIKeys,
		bootstrap.AdoptionRefCPAAuthFiles,
	}
	decisions := make([]bootstrap.AdoptionResourceDecision, 0, len(refs))
	for _, ref := range refs {
		rule, ok := bootstrap.FreshAdoptionRule(ref)
		if !ok {
			continue
		}
		decision := bootstrap.AdoptionResourceDecision{
			ResourceRef:       ref,
			Kind:              rule.Kind,
			Scope:             rule.Scope,
			ReadEvidenceRef:   bootstrap.CapabilityEvidenceRef("preflight:" + string(ref) + "-read"),
			ReplayEvidenceRef: bootstrap.CapabilityEvidenceRef("preflight:" + string(ref) + "-replay"),
			VerifyEvidenceRef: bootstrap.CapabilityEvidenceRef("preflight:" + string(ref) + "-verify"),
			Qualification:     bootstrap.CapabilityQualified,
			Disposition:       bootstrap.Migrate,
		}
		// The rule table is the only thing that may authorize a copy: a decision
		// the product refuses is dropped rather than forced.
		if _, ok := bootstrap.FreshAdoptionDecision(ref, decision); !ok {
			continue
		}
		decisions = append(decisions, decision)
	}
	return decisions
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
