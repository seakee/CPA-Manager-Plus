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
type Executor struct {
	source      Source
	destination Destination
}

// New builds an executor over an already-authenticated source and destination.
func New(source Source, destination Destination) *Executor {
	return &Executor{source: source, destination: destination}
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
func (e *Executor) record(summary *freshstore.AdoptionSummary, item freshstore.AdoptionItem, checkpoint Checkpoint) error {
	summary.Items = append(summary.Items, item)
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
	return Result{Summary: summary}, nil
}

// digestSHA256 is the canonical digest of item bytes. It is the only thing a
// result may carry about those bytes.
func digestSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func credentialItemName(name string) string { return "credential:" + name }

func configLeafItemName(path string) string { return "config:" + path }

func configFamilyItemName(family string) string { return "rules:" + family }

func providerSectionItemName(section string) string { return "section:" + section }

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

func (e *Executor) migrateCredentials(ctx context.Context, summary *freshstore.AdoptionSummary, files []CredentialFile, checkpoint Checkpoint) error {
	for _, file := range files {
		item := credentialItemName(file.Name)
		if summary.Settled(item) {
			continue
		}
		if err := e.record(summary, e.migrateCredential(ctx, item, file), checkpoint); err != nil {
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
		return freshstore.AdoptionItem{
			Item: item, Kind: freshstore.AdoptionKindCredential, Outcome: freshstore.AdoptionFailed,
			Reason: "download_failed", Instruction: manualInstruction(freshstore.AdoptionKindCredential, file.Name),
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

	// The bytes are byte-identical. What can still diverge is the enabled state
	// and the provider the destination classified the file under; both are
	// manual work, never a claimed success and never a failed copy.
	reason, instruction := "", ""
	if file.Type != "" && state.Type != "" && !strings.EqualFold(state.Type, file.Type) {
		reason, instruction = "type_diverged",
			"The credential was copied but the destination classifies it as "+state.Type+". Check the provider in the destination Console."
	}
	if file.Disabled && !state.Disabled {
		if err := e.destination.SetCredentialDisabled(ctx, file.Name, true); err != nil {
			reason, instruction = "status_write_failed",
				"The credential was copied but stays enabled in the destination. Disable it in the destination Console."
		}
	}
	outcome := freshstore.AdoptionMigrated
	if reason != "" {
		outcome = freshstore.AdoptionManual
	}
	return freshstore.AdoptionItem{
		Item: item, Kind: freshstore.AdoptionKindCredential, Outcome: outcome,
		Reason: reason, Instruction: instruction, SourceSHA256: sourceDigest, DestinationSHA256: sourceDigest,
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
			if err := e.record(summary, freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindConfigLeaf, Outcome: freshstore.AdoptionManual,
				Reason: "manager_owned", Instruction: manualInstruction(freshstore.AdoptionKindConfigLeaf, path),
			}, checkpoint); err != nil {
				return err
			}
			continue
		}
		rawValue, presence := cpa.LookupConfigValue(document, path)
		if presence != cpa.ConfigPresent {
			// A leaf the source never set resolves to CPA's own default on the
			// destination too, so there is nothing to copy and nothing to report.
			continue
		}
		value := normalizeValue(rawValue)
		if err := cpa.ValidateConfigLeafValue(path, value); err != nil {
			if err := e.record(summary, freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindConfigLeaf, Outcome: freshstore.AdoptionManual,
				Reason: "value_shape_unsupported", Instruction: manualInstruction(freshstore.AdoptionKindConfigLeaf, path),
			}, checkpoint); err != nil {
				return err
			}
			continue
		}
		prefix, err := configLeafPrefix(path)
		if err != nil {
			if err := e.record(summary, freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindConfigLeaf, Outcome: freshstore.AdoptionFailed,
				Reason: "path_unresolved", Instruction: manualInstruction(freshstore.AdoptionKindConfigLeaf, path),
			}, checkpoint); err != nil {
				return err
			}
			continue
		}
		body, err := leafPatchPayload(prefix, path, value)
		if err != nil {
			if err := e.record(summary, freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindConfigLeaf, Outcome: freshstore.AdoptionFailed,
				Reason: "payload_unresolved", Instruction: manualInstruction(freshstore.AdoptionKindConfigLeaf, path),
			}, checkpoint); err != nil {
				return err
			}
			continue
		}
		if err := e.record(summary, e.writeLeaf(ctx, item, path, prefix, value, body), checkpoint); err != nil {
			return err
		}
	}
	return nil
}

// writeLeaf writes one leaf and resolves the outcome by reading it back. A
// definite refusal is manual work; an unknown effect is decided by what the
// destination holds now and never by resending the write.
func (e *Executor) writeLeaf(ctx context.Context, item, path, prefix string, value any, payload []byte) freshstore.AdoptionItem {
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
			if err := e.record(summary, freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindConfigFamily, Outcome: freshstore.AdoptionFailed, Reason: "family_unresolved",
			}, checkpoint); err != nil {
				return err
			}
			continue
		}
		value, presence := cpa.LookupConfigValue(document, entry.Path)
		if presence == cpa.ConfigMissing {
			// Absent is CPA's own empty rule list; there is nothing to write.
			continue
		}
		payload, err := encodeJSONValue(value)
		if err != nil {
			if err := e.record(summary, freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindConfigFamily, Outcome: freshstore.AdoptionManual,
				Reason: "value_shape_unsupported", Instruction: manualInstruction(freshstore.AdoptionKindConfigFamily, family),
			}, checkpoint); err != nil {
				return err
			}
			continue
		}
		if err := e.record(summary, e.writeWhole(ctx, freshstore.AdoptionKindConfigFamily, item, entry.Path, value, payload), checkpoint); err != nil {
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
			if err := e.record(summary, freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindProviderSection, Outcome: freshstore.AdoptionManual,
				Reason: "manager_owned", Instruction: manualInstruction(freshstore.AdoptionKindProviderSection, section),
			}, checkpoint); err != nil {
				return err
			}
			continue
		}
		value, presence := cpa.LookupConfigValue(document, section)
		if presence == cpa.ConfigMissing {
			continue
		}
		payload, err := encodeJSONValue(value)
		if err != nil {
			if err := e.record(summary, freshstore.AdoptionItem{
				Item: item, Kind: freshstore.AdoptionKindProviderSection, Outcome: freshstore.AdoptionManual,
				Reason: "value_shape_unsupported", Instruction: manualInstruction(freshstore.AdoptionKindProviderSection, section),
			}, checkpoint); err != nil {
				return err
			}
			continue
		}
		if err := e.record(summary, e.writeWhole(ctx, freshstore.AdoptionKindProviderSection, item, section, value, payload), checkpoint); err != nil {
			return err
		}
	}
	return nil
}

// writeWhole replaces one whole section or rule family and resolves the outcome
// by reading that path back, on the same terms as writeLeaf.
func (e *Executor) writeWhole(ctx context.Context, kind freshstore.AdoptionItemKind, item, path string, value any, payload []byte) freshstore.AdoptionItem {
	digest := digestSHA256(payload)
	writeErr := e.destination.PutConfigValue(ctx, path, payload)
	observed, presence, readErr := e.destination.ReadConfigValue(ctx, path)
	applied := readErr == nil && presence == cpa.ConfigPresent && equalConfigValue(value, observed)
	switch {
	case applied:
		return freshstore.AdoptionItem{
			Item: item, Kind: kind, Outcome: freshstore.AdoptionMigrated,
			SourceSHA256: digest, DestinationSHA256: digest,
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
