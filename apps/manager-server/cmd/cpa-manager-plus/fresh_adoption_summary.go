package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshadopt"
)

// adoptionSummaryUnavailable is the only code the read route answers with when
// there is no committed adoption to describe. It is a bounded code rather than a
// bare 404 so the browser can tell "this installation is not an adoption" from a
// lost route.
const adoptionSummaryUnavailable = "adopt_summary_unavailable"

// The reply's text for a credential item is a Manager-owned label and the item's
// ordinal, never the record's identity.
//
// A credential file name is the source CPA's metadata. It can be, or contain, a
// credential — a Client Key used verbatim as a file name has no credential shape
// for the project's scrubber to recognise — and the record's overlong identity
// carries an unsalted digest of the unscrubbed name. A redaction rule that tries
// to find every fragment of every known value in that text is a matcher with an
// open-ended failure mode: a middle or suffix fragment, a value that starts
// outside the recorded cut, or a name that merely shares a long prefix is exactly
// what a prefix-only rule misses. The reply therefore does not carry the name at
// all. The label says what the item is; the ordinal keeps two credentials apart;
// the outcome, reason and retryable flag say what happened to it.
const (
	adoptionCredentialLabel         = "Credential file"
	adoptionCredentialOverflowLabel = "Credential files left behind"
	adoptionConfigurationLabel      = "Configuration item"
	adoptionProviderLabel           = "Provider section"
	adoptionUnknownLabel            = "Adoption item"
)

// adoptionSummaryItem is one per-item migration result as the completion screen
// reads it.
//
// It is deliberately narrower than the durable `freshstore.AdoptionItem`: the
// record also carries the source and destination SHA-256 digests, and those are
// evidence for the Manager's own bookkeeping rather than something a browser
// needs. Dropping them here is what makes the reply a statement about progress
// only.
//
// The reply is also a separate output boundary from the record. Text in the
// record is not all the Manager's: a credential file name is the source CPA's
// metadata, and a failed write can quote an upstream code. Both are projected
// structurally — a credential item by a generic label and ordinal, a reason by a
// closed set of this Manager's own codes — rather than by trying to scrub
// arbitrary upstream text. The record's free-text instruction is not emitted at
// all: the page states the next step from the item's own kind.
type adoptionSummaryItem struct {
	// ID is the reply's stable identity for this item: a digest of the text the
	// reply shows and the item's ordinal. Two credentials with unreadable names
	// project to different ordinals, so the identity keeps them apart, and it
	// digests only public text — never the record's own name, which may be or
	// contain a credential.
	ID      string `json:"id"`
	Item    string `json:"item"`
	Kind    string `json:"kind"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
	// Retryable is the record's own rule, not a second opinion: an item failed
	// before anything was written, so only a replay can copy it.
	Retryable bool `json:"retryable"`
	// Left is how many source items the record could not hold at all, set on the
	// aggregate excess marker only.
	Left int `json:"left,omitempty"`
}

// adoptionSummaryCounts is the per-outcome tally the progress screen shows.
// `absent_at_source` is reported because it is a settled result the operator may
// want to see, and it is not a failure.
type adoptionSummaryCounts struct {
	Migrated       int `json:"migrated"`
	Manual         int `json:"manual"`
	Failed         int `json:"failed"`
	AbsentAtSource int `json:"absent_at_source"`
}

// adoptionSummaryReply is the read face of the Adopt migration: everything the
// completion screen needs to show progress and per-item results, and nothing
// else.
//
// `running` is the live single-flight flag rather than a recorded value, so a
// poll that arrives between two checkpoints still sees the truth: the copy is
// either in flight right now or it is not. `finished`, `planned` and `runId` are
// the durable run markers, so a browser that reloads mid-run resumes from the
// same facts the completion path decides on.
//
// `runId` is the identity of the run that wrote this record. A completion that
// accepts retryable items names it, and the server refuses the acknowledgement
// when the durable record has moved to a different run: two runs can leave the
// same items and counts, and the generation is the only fact that tells the
// operator's confirmation apart. It is empty on a record written before run
// generations existed.
type adoptionSummaryReply struct {
	Items     []adoptionSummaryItem `json:"items"`
	RunID     string                `json:"runId"`
	Planned   int                   `json:"planned"`
	Finished  bool                  `json:"finished"`
	Running   bool                  `json:"running"`
	Retryable int                   `json:"retryable"`
	Counts    adoptionSummaryCounts `json:"counts"`
}

// adoptionSummary serves `GET installation/adoption`.
//
// It exists because the per-item summary is durable but was only readable by the
// completion decision: the browser could ask whether the adoption finished, and
// nothing else. A migration that ends with retryable items, a reload in the
// middle of a run, and the "what did it actually copy" question all need the
// same recorded items, so the record itself is what this route answers with.
//
// The route is read-only and states only recorded facts plus the in-flight flag.
// A non-adoption, or an adoption that has not committed yet, has no summary to
// describe and is refused with its own code.
func (a *freshApplication) adoptionSummary(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	if op.Strategy.Strategy != bootstrap.AdoptExisting || op.Strategy.Stage != "committed" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		writeFreshJSON(w, map[string]string{"code": adoptionSummaryUnavailable})
		return
	}
	record, err := a.store.Load(r.Context())
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		writeFreshJSON(w, map[string]string{"code": adoptionSummaryUnavailable})
		return
	}
	reply := adoptionSummaryFrom(record.AdoptionSummary)
	reply.Running = a.adoptMigrationInFlight()
	writeFreshJSON(w, reply)
}

// adoptionPublicReasons is the closed set of reason codes this Manager writes
// into the record: the executor's own per-item outcomes and the adoption
// decision's manual actions.
//
// The projection keeps exactly these and drops everything else. A failed
// destination write can quote CPA's own rejection code, and a rejection message
// can embed the value it refused; neither is this Manager's vocabulary, so an
// unrecognised reason is not echoed. The item's outcome and retryable flag still
// answer what happened, which is what the operator acts on.
var adoptionPublicReasons = map[string]bool{
	// Adoption decisions.
	"not_authorized":                true,
	"reauthenticate_after_adoption": true,
	"reconfigure_after_adoption":    true,
	// Credential outcomes.
	"download_failed":              true,
	"not_uploadable":               true,
	"runtime_only_credential":      true,
	"type_diverged":                true,
	"status_write_failed":          true,
	"status_unverified":            true,
	"adoption_item_name_too_long":  true,
	"adoption_inventory_too_large": true,
	// Configuration outcomes.
	"manager_owned":           true,
	"value_shape_unsupported": true,
	"path_unresolved":         true,
	"payload_unresolved":      true,
	"family_unresolved":       true,
	// Destination write outcomes.
	"verify_mismatch":         true,
	"cpa_rejected":            true,
	"apply_unknown":           true,
	"destination_unavailable": true,
	"value_not_canonical":     true,
	"write_failed":            true,
	"source_unreadable":       true,
}

// publicAdoptionReason bounds one recorded reason to this Manager's own codes.
func publicAdoptionReason(reason string) string {
	if adoptionPublicReasons[reason] {
		return reason
	}
	return ""
}

// publicAdoptionItemText projects one durable item identity into the text the
// reply shows.
//
// A credential is always a generic label plus its ordinal: the record's identity
// is source metadata, and its overlong form is a digest of that metadata, so
// neither may reach the browser. The inventory-overflow marker keeps its own
// bounded meaning and the `left` count.
//
// A configuration or provider item keeps its canonical reference only when the
// reference is a name from this Manager's own closed vocabulary
// (`freshadopt.CanonicalItem`): those tables are what built the name, so the text
// is Manager-authored rather than upstream free text. A record from an older
// table that no longer matches falls back to a generic label with its ordinal,
// which is honest and cannot carry anything else.
func publicAdoptionItemText(item freshstore.AdoptionItem, ordinal, credentialOrdinal int) string {
	switch item.Kind {
	case freshstore.AdoptionKindCredential:
		if item.Left > 0 {
			return adoptionCredentialOverflowLabel
		}
		return adoptionCredentialLabel + " #" + strconv.Itoa(credentialOrdinal)
	case freshstore.AdoptionKindConfigLeaf, freshstore.AdoptionKindConfigFamily:
		if freshadopt.CanonicalItem(item) {
			return item.Item
		}
		return adoptionConfigurationLabel + " #" + strconv.Itoa(ordinal+1)
	case freshstore.AdoptionKindProviderSection:
		if freshadopt.CanonicalItem(item) {
			return item.Item
		}
		return adoptionProviderLabel + " #" + strconv.Itoa(ordinal+1)
	default:
		return adoptionUnknownLabel + " #" + strconv.Itoa(ordinal+1)
	}
}

// adoptionItemID is the stable, non-secret identity of one recorded item.
//
// It digests the text the reply shows and the item's ordinal, never the record's
// own name: the record's name can be a credential, and a digest of it would
// confirm a candidate value offline. The ordinal keeps two items whose text is
// the same apart, and the record's own order keeps the identity stable across
// reads.
func adoptionItemID(itemText string, ordinal int) string {
	sum := sha256.Sum256([]byte(itemText + "\x00" + strconv.Itoa(ordinal)))
	return hex.EncodeToString(sum[:])
}

// adoptionSummaryFrom projects the durable summary into the reply. The order is
// the migration's own order, which is the plan order the run fixed before its
// first write.
func adoptionSummaryFrom(summary *freshstore.AdoptionSummary) adoptionSummaryReply {
	reply := adoptionSummaryReply{Items: []adoptionSummaryItem{}}
	if summary == nil {
		return reply
	}
	reply.RunID = summary.RunID
	reply.Planned = summary.Planned
	reply.Finished = summary.Finished
	credentialOrdinal := 0
	for ordinal, item := range summary.Items {
		if item.Kind == freshstore.AdoptionKindCredential {
			credentialOrdinal++
		}
		retryable := summary.Retryable(item.Item)
		if retryable {
			reply.Retryable++
		}
		switch item.Outcome {
		case freshstore.AdoptionMigrated:
			reply.Counts.Migrated++
		case freshstore.AdoptionManual:
			reply.Counts.Manual++
		case freshstore.AdoptionFailed:
			reply.Counts.Failed++
		case freshstore.AdoptionAbsentAtSource:
			reply.Counts.AbsentAtSource++
		}
		itemText := publicAdoptionItemText(item, ordinal, credentialOrdinal)
		reply.Items = append(reply.Items, adoptionSummaryItem{
			ID:      adoptionItemID(itemText, ordinal),
			Item:    itemText,
			Kind:    string(item.Kind),
			Outcome: string(item.Outcome),
			Reason:  publicAdoptionReason(item.Reason),
			// The record's free-text instruction stays behind: it interpolates a
			// source name and a destination type string, and neither is text this
			// Manager wrote.
			Retryable: retryable,
			Left:      item.Left,
		})
	}
	return reply
}
