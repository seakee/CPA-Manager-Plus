// Package freshadopt is the Adopt migration executor: it copies a bounded,
// product-owned inventory from a running source CPA v8 to the destination CPA v8
// this Manager has just committed, and records one per-item result for each item.
//
// Three invariants shape every write here.
//
//  1. The source is never written to. The source client has no write method, so
//     no code path in this package can issue a non-GET request to it, and its
//     transport follows no redirect and never proxies.
//  2. Secrets stay in Manager memory. Credential bytes and provider keys travel
//     only inside this process: they are never logged, never journaled, never
//     returned in a result and never written to the durable summary, which
//     carries item identities, outcomes and byte digests only.
//  3. An unknown write result is resolved by reading the destination back, never
//     by sending the write again, and an item already recorded as migrated is
//     never written a second time.
//
// The Manager does not own the destination's management secret, host/port,
// remote-management block, plugin root, auth directory or client key list, and
// the destination's client keys were already installed by the verified
// StageDestination/VerifyV8Adoption path. ManagerOwned reports that exclusion and
// the executor refuses an excluded item even if a caller asks for it.
package freshadopt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

// Endpoint is one CPA v8 Management API target.
type Endpoint struct {
	// BaseURL is the normalized base URL of the CPA.
	BaseURL string
	// ManagementKey authenticates every request to this CPA. It is held in
	// memory for the lifetime of one migration run.
	ManagementKey string
}

// Source is the read-only face of the source CPA. It has no write method, so no
// code path in this package can mutate the source; the test fake implements
// exactly this interface and asserts it observed only GET requests.
type Source interface {
	ListCredentialFiles(context.Context) ([]CredentialFile, error)
	DownloadCredential(context.Context, string) ([]byte, error)
	ReadConfigDocument(context.Context) (map[string]any, error)
}

// Destination is the write and read-back face of the destination CPA.
type Destination interface {
	ListCredentialFiles(context.Context) ([]CredentialFile, error)
	DownloadCredential(context.Context, string) ([]byte, error)
	CredentialState(context.Context, string) (CredentialState, error)
	UploadCredential(context.Context, string, []byte) error
	SetCredentialDisabled(context.Context, string, bool) error
	PatchConfigPath(context.Context, string, []byte) error
	PutConfigValue(context.Context, string, []byte) error
	ReadConfigValue(context.Context, string) (any, cpa.ConfigPresence, error)
}

// CredentialState is one credential as the destination lists it right now. It is
// the read-back half of a credential copy: it says whether the file is there,
// which provider the destination classified it under, and whether it is enabled.
type CredentialState struct {
	Present  bool
	Type     string
	Disabled bool
}

// CredentialFile is one source credential file as the source lists it. It holds
// no credential bytes: the list response is metadata, and the bytes are
// downloaded separately and never stored.
type CredentialFile struct {
	Name        string
	Type        string
	Disabled    bool
	RuntimeOnly bool
}

// Result is the outcome of one whole migration run.
type Result struct {
	Summary freshstore.AdoptionSummary
}

// Executor runs the bounded migration inventory against one source/destination
// pair. It is constructed per run and holds no durable state of its own.
//
// The one durable input it must not invent is the record's decision: which
// references the operator's own installation record marks migrate. Every item
// below one of them is copied; every item below any other reference is recorded
// with that decision's manual action and never written. The executor therefore
// copies exactly what the record authorized, and a widened inventory cannot
// copy a kind the record still calls manual.
type Executor struct {
	source      Source
	destination Destination
	// decisions is the record's verdict per stable adoption reference. A
	// reference missing from it has no migrate verdict, which the rules resolve
	// to that reference's manual action.
	decisions map[bootstrap.ResourceRef]bootstrap.AdoptionResourceDecision
}

// New builds an executor over an already-authenticated source and destination
// and the per-reference decisions of the operation record this run serves.
func New(source Source, destination Destination, decisions []bootstrap.AdoptionResourceDecision) *Executor {
	indexed := make(map[bootstrap.ResourceRef]bootstrap.AdoptionResourceDecision, len(decisions))
	for _, decision := range decisions {
		indexed[decision.ResourceRef] = decision
	}
	return &Executor{source: source, destination: destination, decisions: indexed}
}

// ItemRef is the stable adoption reference one bounded item belongs to. It is
// the single classification of this package, and the reference the record's
// decision is looked up under.
//
// It strips the item prefix and delegates to `cpa.AdoptionItemRef`, the one
// classification discovery also uses, so an item is gated under exactly the
// reference the record decided for it. A writable gateway leaf or a whole payload
// rule family is routing configuration: that table is the exact scope the gateway
// configuration page reads and writes against CPA v8.0.13. Anything outside the
// table keeps the reference that already describes it and stays manual: the
// executor never widens a scope by classifying an item into a migrate verdict it
// was not given.
func ItemRef(item freshstore.AdoptionItem) bootstrap.ResourceRef {
	switch item.Kind {
	case freshstore.AdoptionKindCredential:
		// Every credential item — a copied file, an overlong name, the excess
		// marker — is the source's auth-file inventory.
		return cpa.AdoptionItemRef(cpa.CredentialItemClass)
	case freshstore.AdoptionKindConfigLeaf:
		if path, ok := strings.CutPrefix(item.Item, "config:"); ok {
			return cpa.AdoptionItemRef(path)
		}
		return bootstrap.AdoptionRefConfigUnknown
	case freshstore.AdoptionKindConfigFamily:
		if family, ok := strings.CutPrefix(item.Item, "rules:"); ok {
			return cpa.AdoptionItemRef(family)
		}
		return bootstrap.AdoptionRefConfigUnknown
	case freshstore.AdoptionKindProviderSection:
		if section, ok := strings.CutPrefix(item.Item, "section:"); ok {
			return cpa.AdoptionItemRef(section)
		}
		// A section item without a section name has no reference of its own, and
		// it must not borrow one.
		return bootstrap.AdoptionRefProviderStateProviders
	default:
		return bootstrap.AdoptionRefConfigUnknown
	}
}

// gated reports the item to record instead of copying, when this item's
// reference is not marked migrate by the record. An item the record does not
// mark migrate is never written: the decision is what authorizes the copy, and
// the decision's own manual action is what the operator is told to do.
func (e *Executor) gated(item freshstore.AdoptionItem, name string) (freshstore.AdoptionItem, bool) {
	ref := ItemRef(item)
	decision, ok := bootstrap.FreshAdoptionDecision(ref, e.decisions[ref])
	if ok && decision.Disposition == bootstrap.Migrate {
		return freshstore.AdoptionItem{}, false
	}
	reason := "not_authorized"
	if ok && decision.ManualAction != "" {
		reason = string(decision.ManualAction)
	}
	return freshstore.AdoptionItem{
		Item: item.Item, Kind: item.Kind, Outcome: freshstore.AdoptionManual,
		Reason: reason, Instruction: manualInstruction(item.Kind, name),
	}, true
}

// Checkpoint persists the items recorded so far. It is called once per item,
// after that item's write has been resolved, so the durable record is never
// behind a write that already happened. A nil Checkpoint records nothing, which
// is what a dry reconciliation run wants.
//
// It must only append: the store holds the summary monotone, and a rewrite would
// be refused there rather than here.
type Checkpoint func(freshstore.AdoptionSummary) error

// record appends one resolved item and checkpoints it before the next item is
// attempted. A failed checkpoint stops the run without retrying the item in
// place: the write it describes already happened, and re-issuing it is exactly
// what the record exists to prevent. The retry therefore resumes from the last
// durable item, and any write it repeats is the same value or the same bytes
// under the same name, which CPA accepts idempotently.
func (e *Executor) record(ctx context.Context, summary *freshstore.AdoptionSummary, item freshstore.AdoptionItem, checkpoint Checkpoint) error {
	if err := ctx.Err(); err != nil {
		// The run's deadline or cancellation is the one reason to stop between
		// items. Everything recorded is durable, everything unrecorded is
		// retryable by the next run, and the item in flight is NOT recorded: this
		// run cannot resolve it any more, so recording an outcome it did not
		// observe would be a claim it cannot make.
		return err
	}
	// A provisional record is the one entry this run may replace in place. It is
	// the failed attempt of a run that wrote nothing; the item it names keeps its
	// position, so the record still reads in migration order.
	if index := summary.Index(item.Item); index >= 0 {
		if !summary.Retryable(item.Item) {
			return nil
		}
		summary.Items[index] = item
	} else {
		summary.Items = append(summary.Items, item)
	}
	if checkpoint == nil {
		return nil
	}
	return checkpoint(*summary)
}

var (
	// ErrManagerOwned refuses to migrate an item the Manager owns on the
	// destination.
	ErrManagerOwned = errors.New("freshadopt: item is manager-owned")
	// ErrCredentialNotUploadable reports a source credential v8 cannot accept as
	// an upload, so it becomes a manual item with a concrete instruction.
	ErrCredentialNotUploadable = errors.New("freshadopt: credential is not uploadable")
	// ErrInventoryUnavailable reports a source read this run cannot proceed
	// without. It is the only error that aborts a run: every per-item failure is
	// recorded instead.
	ErrInventoryUnavailable = errors.New("freshadopt: source inventory unavailable")
)

// Run copies every item of the bounded inventory, appends one result per item to
// the summary in migration order, and reports them.
func (e *Executor) Run(ctx context.Context, previous freshstore.AdoptionSummary, checkpoint Checkpoint) (Result, error) {
	if e == nil || e.source == nil || e.destination == nil {
		return Result{}, errors.New("freshadopt: source and destination are required")
	}
	summary := freshstore.AdoptionSummary{Items: append([]freshstore.AdoptionItem(nil), previous.Items...)}
	// The document is read once: the source is never written, so three
	// independent reads could not observe three different states, and one read
	// bounds both the request count and the memory held.
	document, err := e.source.ReadConfigDocument(ctx)
	if err != nil {
		return Result{Summary: summary}, fmt.Errorf("%w: %v", ErrInventoryUnavailable, err)
	}
	files, err := e.source.ListCredentialFiles(ctx)
	if err != nil {
		return Result{Summary: summary}, fmt.Errorf("%w: %v", ErrInventoryUnavailable, err)
	}
	// The plan is bounded before anything is written. A summary the store would
	// refuse is a run that stops at the offending item, and every replay then
	// repeats that item and stops again: the loop this preflight exists to make
	// impossible.
	files, err = e.preflightInventory(ctx, &summary, files, checkpoint)
	if err != nil {
		return Result{Summary: summary}, err
	}
	if err := e.migrateCredentials(ctx, &summary, files, checkpoint); err != nil {
		return Result{Summary: summary}, err
	}
	if err := e.migrateConfigLeaves(ctx, &summary, document, checkpoint); err != nil {
		return Result{Summary: summary}, err
	}
	if err := e.migrateConfigFamilies(ctx, &summary, document, checkpoint); err != nil {
		return Result{Summary: summary}, err
	}
	if err := e.migrateProviderSections(ctx, &summary, document, checkpoint); err != nil {
		return Result{Summary: summary}, err
	}
	// Every planned item is now settled or recorded as retryable, so this run
	// finished. The marker is written, never inferred: a run that stopped at an
	// item leaves a list that looks no different from a complete one, and
	// completion reads exactly this record. `Planned` is the plan this run
	// actually reached, which a later replay rewrites with its own.
	summary.Finished = true
	summary.Planned = len(summary.Items)
	if checkpoint != nil {
		if err := checkpoint(summary); err != nil {
			return Result{Summary: summary}, err
		}
	}
	return Result{Summary: summary}, nil
}

// digestSHA256 is the canonical digest of item bytes. It is the only thing a
// result may carry about those bytes.
func digestSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// credentialParseable applies CPA's own parse expectations to the bytes this run
// is about to upload. CPA's `writeAuthFile` calls `buildAuthFromFileData`, which
// unmarshals the body as a JSON object and then synthesizes the auth from it
// (`internal/api/handlers/management/auth_files_crud.go:482-508`); a body that is
// not a JSON object can never be accepted, so it is never sent.
func credentialParseable(data []byte) error {
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return ErrCredentialNotUploadable
	}
	if object == nil {
		return ErrCredentialNotUploadable
	}
	return nil
}

func credentialItemName(name string) string { return "credential:" + name }

func configLeafItemName(path string) string { return "config:" + path }

func configFamilyItemName(family string) string { return "rules:" + family }

func providerSectionItemName(section string) string { return "section:" + section }

// overlongCredentialItem is the stable identity of a credential whose own name
// cannot be an item identity because it is longer than the store accepts. It is
// the digest of the name, so it is stable across replays and carries no name
// byte beyond its own bounded prefix.
func overlongCredentialItem(name string) string {
	return "credential-name-sha256:" + digestSHA256([]byte(name))
}

// boundedName quotes a credential name inside an instruction without letting an
// upstream name exceed the instruction bound.
func boundedName(name string) string {
	if len(name) > 120 {
		return name[:120] + "..."
	}
	return name
}

// sanitizeReason bounds a reason to the vocabulary the record accepts without
// ever carrying a value: an upstream message can quote YAML.
func sanitizeReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) > 120 {
		reason = reason[:120]
	}
	return reason
}

// manualInstruction is the concrete thing an operator has to do for one item.
func manualInstruction(kind freshstore.AdoptionItemKind, name string) string {
	switch kind {
	case freshstore.AdoptionKindCredential:
		return "Copy this credential file into the destination CPA's auth directory, or re-authenticate the provider in the destination Console."
	case freshstore.AdoptionKindConfigLeaf:
		return "Set " + name + " on the destination CPA's gateway configuration page."
	case freshstore.AdoptionKindConfigFamily:
		return "Replace the " + name + " payload rule list on the destination CPA's gateway configuration page."
	case freshstore.AdoptionKindProviderSection:
		return "Copy " + name + " into the destination CPA's configuration, including its provider keys."
	default:
		return "Review this item in the destination CPA."
	}
}

// excessCredentialItem is the single aggregate identity that reports every
// credential the record had no room for. It is one item, not one per credential,
// because a per-credential report would need exactly the room that is missing.
const excessCredentialItem = "credential-inventory-excess"

// preflightInventory bounds the plan before the first write. It returns the
// credentials this run may attempt and records the rest as manual work.
//
// Two bounds are decided here, both from the store's own limits rather than a
// second copy of them:
//
//   - a credential whose stable item identity is longer than the store accepts
//     can never be recorded, so it is reported as manual with an instruction
//     instead of stopping the run at it;
//   - the whole inventory must fit the summary. The fixed part of the plan (the
//     gateway leaves, the rule families and the provider sections) is known
//     exactly, so the credential allowance is the remainder. Anything past it is
//     reported as manual: a bounded, valid record the operator can act on is
//     strictly better than a record the store refuses.
//
// The summary is validated against the store's own checker before it is
// checkpointed, so a plan that still does not fit ends the run here, before any
// write, instead of failing during it.
func (e *Executor) preflightInventory(ctx context.Context, summary *freshstore.AdoptionSummary, files []CredentialFile, checkpoint Checkpoint) ([]CredentialFile, error) {
	limit := freshstore.MaxAdoptionItems()
	nameLimit := freshstore.MaxAdoptionItemName()
	// The fixed part of every run. A leaf the source never set is not recorded,
	// so this is an upper bound on what the three configuration passes append.
	fixed := len(cpa.ConfigLeafPaths()) + len(cpa.ConfigRuleFamilyIDs()) + len(ProviderSections())
	// One slot is reserved for the single aggregate marker below, so the plan and
	// its own report always fit.
	allowed := limit - len(summary.Items) - fixed - 1
	if allowed < 0 {
		allowed = 0
	}
	if summary.Settled(excessCredentialItem) {
		// An earlier run already reported the overflow. The credentials it left
		// out stay out: a later run may not quietly copy past the bound that run
		// reported, because the operator was told those files were left behind.
		return nil, nil
	}
	migratable := make([]CredentialFile, 0, len(files))
	excess := 0
	for _, file := range files {
		if len(credentialItemName(file.Name)) > nameLimit {
			// The name cannot be the item identity: the store would refuse the
			// whole summary. The record carries a bounded identity derived from the
			// name instead, so the item is still reported, still stable across
			// replays and still actionable.
			item := overlongCredentialItem(file.Name)
			if summary.Settled(item) {
				continue
			}
			if err := e.record(ctx, summary, freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindCredential, Outcome: freshstore.AdoptionManual,
				Reason:      "adoption_item_name_too_long",
				Instruction: "Copy the credential file " + boundedName(file.Name) + " into the destination CPA's auth directory by hand; its name is too long for the migration record.",
			}, checkpoint); err != nil {
				return nil, err
			}
			continue
		}
		if summary.Settled(credentialItemName(file.Name)) {
			// Already recorded: it neither consumes the allowance nor needs work.
			continue
		}
		if allowed == 0 {
			excess++
			continue
		}
		allowed--
		migratable = append(migratable, file)
	}
	if excess > 0 {
		if err := e.record(ctx, summary, freshstore.AdoptionItem{
			Item: excessCredentialItem, Kind: freshstore.AdoptionKindCredential, Outcome: freshstore.AdoptionManual,
			Reason:      "adoption_inventory_too_large",
			Instruction: fmt.Sprintf("%d credential files were left behind because the migration record is full. Copy them into the destination CPA's auth directory by hand.", excess),
		}, checkpoint); err != nil {
			return nil, err
		}
	}
	// The plan is checked against the store's own validator here, before the
	// first write, so a plan that still does not fit ends the run at its start
	// instead of in the middle of it.
	if err := freshstore.ValidateAdoptionSummary(*summary); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInventoryUnavailable, err)
	}
	return migratable, nil
}

func (e *Executor) migrateCredentials(ctx context.Context, summary *freshstore.AdoptionSummary, files []CredentialFile, checkpoint Checkpoint) error {
	for _, file := range files {
		item := credentialItemName(file.Name)
		if summary.Settled(item) {
			continue
		}
		if gated, ok := e.gated(freshstore.AdoptionItem{Item: item, Kind: freshstore.AdoptionKindCredential}, file.Name); ok {
			if err := e.record(ctx, summary, gated, checkpoint); err != nil {
				return err
			}
			continue
		}
		if err := e.record(ctx, summary, e.migrateCredential(ctx, item, file), checkpoint); err != nil {
			return err
		}
	}
	return nil
}

// migrateCredential copies one credential file and resolves its outcome from
// what the destination holds afterwards. The write is never repeated: an
// unknown upload is decided by reading the destination back.
func (e *Executor) migrateCredential(ctx context.Context, item string, file CredentialFile) freshstore.AdoptionItem {
	if file.RuntimeOnly {
		// A runtime-only credential exists only inside the source process: there
		// is no file to copy, so only the operator can bring it over.
		return freshstore.AdoptionItem{
			Item: item, Kind: freshstore.AdoptionKindCredential, Outcome: freshstore.AdoptionManual,
			Reason:      "runtime_only_credential",
			Instruction: "This credential exists only in the source CPA process and has no file. Re-authenticate it in the destination CPA.",
		}
	}
	data, err := e.source.DownloadCredential(ctx, file.Name)
	if err != nil {
		// Nothing was written, so this is retryable rather than an unknown write:
		// the next run attempts it again instead of settling on a copy that never
		// started.
		return freshstore.AdoptionItem{
			Item: item, Kind: freshstore.AdoptionKindCredential, Outcome: freshstore.AdoptionFailed,
			Reason: "download_failed", Instruction: manualInstruction(freshstore.AdoptionKindCredential, file.Name),
		}
	}
	// Before writing anything, read what the destination already holds. An equal
	// value is the copy this run would make, so it is recorded without a write:
	// a crash before the checkpoint, or a user's later change on the destination,
	// is never overwritten by a write that changes nothing.
	if state, stateErr := e.destination.CredentialState(ctx, file.Name); stateErr == nil && state.Present {
		if existing, readErr := e.destination.DownloadCredential(ctx, file.Name); readErr == nil {
			if digestSHA256(existing) == digestSHA256(data) {
				return e.credentialOutcome(ctx, item, file.Name, file, state, digestSHA256(data), digestSHA256(data))
			}
		}
	}
	if err := credentialParseable(data); err != nil {
		// CPA could not take these bytes, so uploading them would only produce
		// its refusal. The verdict is recorded where it can be acted on and the
		// file is never sent.
		return freshstore.AdoptionItem{
			Item: item, Kind: freshstore.AdoptionKindCredential, Outcome: freshstore.AdoptionManual,
			Reason: "not_uploadable", Instruction: manualInstruction(freshstore.AdoptionKindCredential, file.Name),
			SourceSHA256: digestSHA256(data),
		}
	}
	sourceDigest := digestSHA256(data)

	uploadErr := e.destination.UploadCredential(ctx, file.Name, data)
	if errors.Is(uploadErr, ErrCredentialNotUploadable) {
		// CPA refused the file itself: no resend can ever succeed.
		return freshstore.AdoptionItem{
			Item: item, Kind: freshstore.AdoptionKindCredential, Outcome: freshstore.AdoptionManual,
			Reason: "not_uploadable", Instruction: manualInstruction(freshstore.AdoptionKindCredential, file.Name),
			SourceSHA256: sourceDigest,
		}
	}

	// The read-back is the only evidence of what the destination holds now. It
	// decides an acknowledged and an unknown upload on the same terms: a lost
	// reply whose bytes did land IS a successful copy, and it is never resent.
	state, stateErr := e.destination.CredentialState(ctx, file.Name)
	readBack, readErr := e.destination.DownloadCredential(ctx, file.Name)
	bytesMatch := stateErr == nil && state.Present && readErr == nil && digestSHA256(readBack) == sourceDigest
	if !bytesMatch {
		if errors.Is(uploadErr, errCredentialServerRefusal) && stateErr == nil && !state.Present {
			// CPA answered 500 and the file is absent: it refused the bytes, and
			// no resend of the same bytes can succeed. This is the answer CPA
			// really gives for a file it cannot parse.
			return freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindCredential, Outcome: freshstore.AdoptionManual,
				Reason: "not_uploadable", Instruction: manualInstruction(freshstore.AdoptionKindCredential, file.Name),
				SourceSHA256: sourceDigest,
			}
		}
		reason := "verify_mismatch"
		if uploadErr != nil {
			reason = sanitizeReason(errorCode(uploadErr))
		}
		return freshstore.AdoptionItem{
			Item: item, Kind: freshstore.AdoptionKindCredential, Outcome: freshstore.AdoptionFailed,
			Reason: reason, Instruction: manualInstruction(freshstore.AdoptionKindCredential, file.Name),
			SourceSHA256: sourceDigest,
		}
	}

	// The bytes are byte-identical, observed from the destination.
	return e.credentialOutcome(ctx, item, file.Name, file, state, sourceDigest, digestSHA256(readBack))
}

// credentialOutcome resolves a credential whose bytes the destination holds.
// What can still diverge is the enabled state and the provider the destination
// classified the file under; both are manual work, never a claimed success and
// never a failed copy.
//
// Both digests are what this run observed: the source bytes it read, and the
// destination bytes it read back. Neither is copied from the other.
func (e *Executor) credentialOutcome(ctx context.Context, item, name string, file CredentialFile, state CredentialState, sourceDigest, destinationDigest string) freshstore.AdoptionItem {
	reason, instruction := "", ""
	if file.Type != "" && state.Type != "" && !strings.EqualFold(state.Type, file.Type) {
		reason, instruction = "type_diverged",
			"The credential was copied but the destination classifies it as "+boundedName(state.Type)+". Check the provider in the destination Console."
	}
	if file.Disabled && !state.Disabled {
		// The PATCH is read back on the same terms as every other write: an
		// unknown reply is resolved by what the destination reports now, never by
		// reporting a state this run did not observe.
		// The PATCH reply itself is not the answer: only the read-back is. It is
		// issued and its outcome deliberately ignored, because an acknowledged
		// PATCH that did not take and a lost reply that did both resolve the same
		// way, from what the destination reports next.
		_ = e.destination.SetCredentialDisabled(ctx, file.Name, true)
		after, afterErr := e.destination.CredentialState(ctx, name)
		switch {
		case afterErr == nil && after.Present && after.Disabled:
			// The read-back is the answer, whatever the PATCH replied.
		case afterErr == nil && after.Present:
			// The destination answers and reports the credential enabled: the
			// disable did not take, whether or not the PATCH said it did.
			reason, instruction = "status_write_failed",
				"The credential was copied but the destination does not report it disabled. Disable it in the destination Console."
		default:
			// The destination did not answer the read-back, so this run cannot say
			// what the state is. Reporting "stays enabled" would be a claim it did
			// not observe.
			reason, instruction = "status_unverified",
				"The credential was copied but the destination's disabled state could not be read back. Check it in the destination Console."
		}
	}
	outcome := freshstore.AdoptionMigrated
	if reason != "" {
		outcome = freshstore.AdoptionManual
	}
	return freshstore.AdoptionItem{
		Item: item, Kind: freshstore.AdoptionKindCredential, Outcome: outcome,
		Reason: reason, Instruction: instruction, SourceSHA256: sourceDigest, DestinationSHA256: destinationDigest,
	}
}

func (e *Executor) migrateConfigLeaves(ctx context.Context, summary *freshstore.AdoptionSummary, document map[string]any, checkpoint Checkpoint) error {
	for _, path := range cpa.ConfigLeafPaths() {
		item := configLeafItemName(path)
		if summary.Settled(item) {
			continue
		}
		if ManagerOwned(path) {
			// The page table cannot name a Manager-owned path; this is defence in
			// depth that would surface a widened gateway table, not a normal path.
			if err := e.record(ctx, summary, freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindConfigLeaf, Outcome: freshstore.AdoptionManual,
				Reason: "manager_owned", Instruction: manualInstruction(freshstore.AdoptionKindConfigLeaf, path),
			}, checkpoint); err != nil {
				return err
			}
			continue
		}
		if gated, ok := e.gated(freshstore.AdoptionItem{Item: item, Kind: freshstore.AdoptionKindConfigLeaf}, path); ok {
			if err := e.record(ctx, summary, gated, checkpoint); err != nil {
				return err
			}
			continue
		}
		rawValue, presence := cpa.LookupConfigValue(document, path)
		if presence != cpa.ConfigPresent {
			// The source does not set this leaf, so there is nothing to copy. It is
			// recorded anyway: a later replay must not mistake a value the operator
			// set on the destination after completion for a stale copy and overwrite
			// it with the source's absence.
			if err := e.record(ctx, summary, freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindConfigLeaf, Outcome: freshstore.AdoptionAbsentAtSource,
			}, checkpoint); err != nil {
				return err
			}
			continue
		}
		value := normalizeValue(rawValue)
		if err := cpa.ValidateConfigLeafValue(path, value); err != nil {
			if err := e.record(ctx, summary, freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindConfigLeaf, Outcome: freshstore.AdoptionManual,
				Reason: "value_shape_unsupported", Instruction: manualInstruction(freshstore.AdoptionKindConfigLeaf, path),
			}, checkpoint); err != nil {
				return err
			}
			continue
		}
		prefix, err := configLeafPrefix(path)
		if err != nil {
			if err := e.record(ctx, summary, freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindConfigLeaf, Outcome: freshstore.AdoptionFailed,
				Reason: "path_unresolved", Instruction: manualInstruction(freshstore.AdoptionKindConfigLeaf, path),
			}, checkpoint); err != nil {
				return err
			}
			continue
		}
		body, err := leafPatchPayload(prefix, path, value)
		if err != nil {
			if err := e.record(ctx, summary, freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindConfigLeaf, Outcome: freshstore.AdoptionFailed,
				Reason: "payload_unresolved", Instruction: manualInstruction(freshstore.AdoptionKindConfigLeaf, path),
			}, checkpoint); err != nil {
				return err
			}
			continue
		}
		if err := e.record(ctx, summary, e.writeLeaf(ctx, item, path, prefix, value, body), checkpoint); err != nil {
			return err
		}
	}
	return nil
}

// writeLeaf writes one leaf and resolves the outcome by reading it back. A
// definite refusal is manual work; an unknown effect is decided by what the
// destination holds now and never by resending the write.
func (e *Executor) writeLeaf(ctx context.Context, item, path, prefix string, value any, payload []byte) freshstore.AdoptionItem {
	// Read before writing: a value the destination already holds is recorded as
	// migrated without a write, so a crash before the checkpoint and a change the
	// operator made on the destination both survive a replay.
	if observed, presence, err := e.destination.ReadConfigValue(ctx, path); err == nil && presence == cpa.ConfigPresent && equalConfigValue(value, observed) {
		return freshstore.AdoptionItem{Item: item, Kind: freshstore.AdoptionKindConfigLeaf, Outcome: freshstore.AdoptionMigrated}
	}
	writeErr := e.destination.PatchConfigPath(ctx, prefix, payload)
	observed, presence, readErr := e.destination.ReadConfigValue(ctx, path)
	applied := readErr == nil && presence == cpa.ConfigPresent && equalConfigValue(value, observed)
	switch {
	case applied:
		return freshstore.AdoptionItem{Item: item, Kind: freshstore.AdoptionKindConfigLeaf, Outcome: freshstore.AdoptionMigrated}
	case writeErr != nil && isDefiniteRefusal(writeErr):
		return freshstore.AdoptionItem{
			Item: item, Kind: freshstore.AdoptionKindConfigLeaf, Outcome: freshstore.AdoptionManual,
			Reason: sanitizeReason(errorCode(writeErr)), Instruction: manualInstruction(freshstore.AdoptionKindConfigLeaf, path),
		}
	default:
		return freshstore.AdoptionItem{
			Item: item, Kind: freshstore.AdoptionKindConfigLeaf, Outcome: freshstore.AdoptionFailed,
			Reason: "verify_mismatch", Instruction: manualInstruction(freshstore.AdoptionKindConfigLeaf, path),
		}
	}
}

func (e *Executor) migrateConfigFamilies(ctx context.Context, summary *freshstore.AdoptionSummary, document map[string]any, checkpoint Checkpoint) error {
	for _, family := range cpa.ConfigRuleFamilyIDs() {
		item := configFamilyItemName(family)
		if summary.Settled(item) {
			continue
		}
		entry, ok := cpa.LookupConfigRuleFamily(family)
		if !ok {
			if err := e.record(ctx, summary, freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindConfigFamily, Outcome: freshstore.AdoptionFailed, Reason: "family_unresolved",
			}, checkpoint); err != nil {
				return err
			}
			continue
		}
		if gated, ok := e.gated(freshstore.AdoptionItem{Item: item, Kind: freshstore.AdoptionKindConfigFamily}, family); ok {
			if err := e.record(ctx, summary, gated, checkpoint); err != nil {
				return err
			}
			continue
		}
		value, presence := cpa.LookupConfigValue(document, entry.Path)
		if presence == cpa.ConfigMissing {
			// Absent is CPA's own empty rule list; there is nothing to write, but
			// the absence is still recorded so a replay cannot overwrite what the
			// operator later put here.
			if err := e.record(ctx, summary, freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindConfigFamily, Outcome: freshstore.AdoptionAbsentAtSource,
			}, checkpoint); err != nil {
				return err
			}
			continue
		}
		payload, err := encodeJSONValue(value)
		if err != nil {
			if err := e.record(ctx, summary, freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindConfigFamily, Outcome: freshstore.AdoptionManual,
				Reason: "value_shape_unsupported", Instruction: manualInstruction(freshstore.AdoptionKindConfigFamily, family),
			}, checkpoint); err != nil {
				return err
			}
			continue
		}
		if err := e.record(ctx, summary, e.writeWhole(ctx, freshstore.AdoptionKindConfigFamily, item, entry.Path, value, payload), checkpoint); err != nil {
			return err
		}
	}
	return nil
}

func (e *Executor) migrateProviderSections(ctx context.Context, summary *freshstore.AdoptionSummary, document map[string]any, checkpoint Checkpoint) error {
	for _, section := range ProviderSections() {
		item := providerSectionItemName(section)
		if summary.Settled(item) {
			continue
		}
		if ManagerOwned(section) {
			if err := e.record(ctx, summary, freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindProviderSection, Outcome: freshstore.AdoptionManual,
				Reason: "manager_owned", Instruction: manualInstruction(freshstore.AdoptionKindProviderSection, section),
			}, checkpoint); err != nil {
				return err
			}
			continue
		}
		if gated, ok := e.gated(freshstore.AdoptionItem{Item: item, Kind: freshstore.AdoptionKindProviderSection}, section); ok {
			if err := e.record(ctx, summary, gated, checkpoint); err != nil {
				return err
			}
			continue
		}
		value, presence := cpa.LookupConfigValue(document, section)
		if presence == cpa.ConfigMissing {
			if err := e.record(ctx, summary, freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindProviderSection, Outcome: freshstore.AdoptionAbsentAtSource,
			}, checkpoint); err != nil {
				return err
			}
			continue
		}
		payload, err := encodeJSONValue(value)
		if err != nil {
			if err := e.record(ctx, summary, freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindProviderSection, Outcome: freshstore.AdoptionManual,
				Reason: "value_shape_unsupported", Instruction: manualInstruction(freshstore.AdoptionKindProviderSection, section),
			}, checkpoint); err != nil {
				return err
			}
			continue
		}
		if err := e.record(ctx, summary, e.writeWhole(ctx, freshstore.AdoptionKindProviderSection, item, section, value, payload), checkpoint); err != nil {
			return err
		}
	}
	return nil
}

// writeWhole replaces one whole section or rule family and resolves the outcome
// by reading that path back, on the same terms as writeLeaf.
func (e *Executor) writeWhole(ctx context.Context, kind freshstore.AdoptionItemKind, item, path string, value any, payload []byte) freshstore.AdoptionItem {
	digest := digestSHA256(payload)
	// Same rule as writeLeaf: an equal value is this run's own copy, so it is
	// recorded without a write.
	if observed, presence, err := e.destination.ReadConfigValue(ctx, path); err == nil && presence == cpa.ConfigPresent && equalConfigValue(value, observed) {
		return freshstore.AdoptionItem{
			Item: item, Kind: kind, Outcome: freshstore.AdoptionMigrated,
			SourceSHA256: digest, DestinationSHA256: digestOfValue(observed),
		}
	}
	writeErr := e.destination.PutConfigValue(ctx, path, payload)
	observed, presence, readErr := e.destination.ReadConfigValue(ctx, path)
	applied := readErr == nil && presence == cpa.ConfigPresent && equalConfigValue(value, observed)
	switch {
	case applied:
		return freshstore.AdoptionItem{
			Item: item, Kind: kind, Outcome: freshstore.AdoptionMigrated,
			SourceSHA256: digest, DestinationSHA256: digestOfValue(observed),
		}
	case writeErr != nil && isDefiniteRefusal(writeErr):
		return freshstore.AdoptionItem{
			Item: item, Kind: kind, Outcome: freshstore.AdoptionManual,
			Reason: sanitizeReason(errorCode(writeErr)), Instruction: manualInstruction(kind, path),
			SourceSHA256: digest,
		}
	default:
		return freshstore.AdoptionItem{
			Item: item, Kind: kind, Outcome: freshstore.AdoptionFailed,
			Reason: "verify_mismatch", Instruction: manualInstruction(kind, path),
			SourceSHA256: digest,
		}
	}
}

// digestOfValue is the canonical digest of one observed configuration value, so
// a migrated item's destination digest is a measurement rather than a copy of the
// source digest.
func digestOfValue(value any) string {
	encoded, err := encodeJSONValue(normalizeValue(value))
	if err != nil {
		return ""
	}
	return digestSHA256(encoded)
}

// equalConfigValue compares a source value with the destination read-back by
// canonical JSON, so a value that crossed the JSON boundary still equals itself.
// Both sides are normalized first: the source document keeps numbers as
// json.Number and the destination answers with plain JSON.
func equalConfigValue(source, destination any) bool {
	left, errLeft := encodeJSONValue(normalizeValue(source))
	right, errRight := encodeJSONValue(normalizeValue(destination))
	return errLeft == nil && errRight == nil && string(left) == string(right)
}

// normalizeValue converts a decoded JSON value into the exact Go scalars the
// gateway table validates. The source document and the destination read-back are
// both decoded with UseNumber so a 64-bit rule parameter keeps its digits, and
// both sides must reach the same Go type before they can be compared or written.
func normalizeValue(value any) any {
	switch typed := value.(type) {
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, normalizeValue(item))
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = normalizeValue(item)
		}
		return out
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return int(integer)
		}
		return typed
	default:
		return cpa.NormalizeConfigValue(value)
	}
}

// isDefiniteRefusal reports CPA's own verdict: a 4xx it answers identically every
// time. Everything else leaves the effect unknown.
func isDefiniteRefusal(err error) bool {
	var rejection cpa.ConfigWriteError
	if errors.As(err, &rejection) {
		return rejection.Definite()
	}
	return errors.Is(err, ErrCredentialNotUploadable)
}
