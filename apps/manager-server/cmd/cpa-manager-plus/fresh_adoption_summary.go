package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

// adoptionSummaryUnavailable is the only code the read route answers with when
// there is no committed adoption to describe. It is a bounded code rather than a
// bare 404 so the browser can tell "this installation is not an adoption" from a
// lost route.
const adoptionSummaryUnavailable = "adopt_summary_unavailable"

// adoptionSummaryItem is one per-item migration result as the completion screen
// reads it.
//
// It is deliberately narrower than the durable `freshstore.AdoptionItem`: the
// record also carries the source and destination SHA-256 digests, and those are
// evidence for the Manager's own bookkeeping rather than something a browser
// needs. Dropping them here is what makes the reply a statement about progress
// only.
//
// The reply is also a separate output boundary from the record, because the
// record's text is not all the Manager's: a credential file name is the source
// CPA's metadata, and a reason can quote an upstream code. Both pass two rules
// here — the project's credential scrubber, then the exact source values the
// migration read — because a Client Key used verbatim as a file name has no
// credential shape for the scrubber to recognise. The record's free-text
// instruction, which interpolates a source name and a destination type string, is
// not emitted at all: the page states the next step from the item's own kind, so
// untrusted metadata cannot reach the browser as prose.
type adoptionSummaryItem struct {
	// ID is the record's stable identity for this item, independent of the text
	// the reply may show. Two token-shaped file names scrub to the same `item`,
	// and a browser that identified them by that text would treat two different
	// resources as one: an acknowledgement, a list key and a request are about the
	// record's item, so they are bound to this digest of its own name.
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
// either in flight right now or it is not. `finished` and `planned` are the
// durable run markers, so a browser that reloads mid-run resumes from the same
// facts the completion path decides on.
type adoptionSummaryReply struct {
	Items     []adoptionSummaryItem `json:"items"`
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
	secrets, ok := a.adoptionSummarySourceSecrets(record)
	if !ok {
		// The source credentials this reply has to redact by value are
		// unreadable, so the reply cannot be proven to carry none of them. An
		// unavailable summary is a state the screen already knows how to state.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		writeFreshJSON(w, map[string]string{"code": adoptionSummaryUnavailable})
		return
	}
	reply := adoptionSummaryFrom(record.AdoptionSummary, adoptionRedactor(secrets))
	reply.Running = a.adoptMigrationInFlight()
	writeFreshJSON(w, reply)
}

// adoptionSummarySourceSecrets reads the source credentials this operation was
// staged with, so the reply can redact them by value.
//
// The scrubber recognises credential shapes. A Client Key used verbatim as a
// credential file name has no shape, and the only rule that catches it is the
// value itself; the protected snapshot is the one place the Manager still holds
// those values. They are read here to be replaced, never emitted: no value is
// returned beyond the redactor's own input, and none of them is logged.
//
// The boolean is whether the snapshot could be read at all. A record with no
// snapshot has nothing to redact; a snapshot that cannot be unread is a reply
// that cannot be proven clean, and the caller answers unavailable instead.
func (a *freshApplication) adoptionSummarySourceSecrets(record freshstore.Record) ([]string, bool) {
	if record.AdoptionCiphertext == "" {
		return nil, true
	}
	if a.protector == nil {
		return nil, false
	}
	plain, err := a.protector.UnprotectString(record.AdoptionCiphertext)
	if err != nil {
		return nil, false
	}
	var snapshot struct {
		SourceManagementKey string
		Destination         struct {
			APIKeys []string `json:"apiKeys"`
		}
	}
	if json.Unmarshal([]byte(plain), &snapshot) != nil {
		return nil, false
	}
	secrets := make([]string, 0, len(snapshot.Destination.APIKeys)+1)
	secrets = append(secrets, snapshot.Destination.APIKeys...)
	if snapshot.SourceManagementKey != "" {
		secrets = append(secrets, snapshot.SourceManagementKey)
	}
	return secrets, true
}

// redactedValue is the marker both rules of the output boundary use, so a reply
// never says which of them replaced a string.
const redactedValue = "[redacted]"

// minRedactedValue is the shortest source credential this boundary replaces by
// value. A shorter string cannot be recognised inside a name without damaging
// every name, and a value that short is not a distinguishable credential.
const minRedactedValue = 8

// adoptionRedactor is the summary's output boundary for one string of the
// record: the project's credential shapes first, then the exact values the
// migration read, longest first so a credential that contains another is
// replaced whole.
func adoptionRedactor(secrets []string) func(string) string {
	ordered := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if len(secret) >= minRedactedValue {
			ordered = append(ordered, secret)
		}
	}
	sort.Slice(ordered, func(left, right int) bool { return len(ordered[left]) > len(ordered[right]) })
	return func(value string) string {
		if value == "" {
			return ""
		}
		value = usage.SanitizeCredentialText(value)
		for _, secret := range ordered {
			value = strings.ReplaceAll(value, secret, redactedValue)
		}
		return value
	}
}

// adoptionItemID is the stable, non-secret identity of one recorded item.
//
// It digests the record's own item name, which the operator who read the source
// already knows and which the reply itself is a projection of: a digest of a
// name is not the name, and nothing here can be turned back into a credential.
func adoptionItemID(item string) string {
	sum := sha256.Sum256([]byte(item))
	return hex.EncodeToString(sum[:])
}

// adoptionSummaryFrom projects the durable summary into the reply. The order is
// the migration's own order, which is the plan order the run fixed before its
// first write.
//
// `redact` is the output boundary for the record's own text, applied to the item
// identity and the reason: both can quote the source.
func adoptionSummaryFrom(summary *freshstore.AdoptionSummary, redact func(string) string) adoptionSummaryReply {
	reply := adoptionSummaryReply{Items: []adoptionSummaryItem{}}
	if summary == nil {
		return reply
	}
	reply.Planned = summary.Planned
	reply.Finished = summary.Finished
	for _, item := range summary.Items {
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
		reply.Items = append(reply.Items, adoptionSummaryItem{
			ID:      adoptionItemID(item.Item),
			Item:    redact(item.Item),
			Kind:    string(item.Kind),
			Outcome: string(item.Outcome),
			Reason:  redact(item.Reason),
			// The record's free-text instruction stays behind: it interpolates a
			// source name and a destination type string, and neither is text this
			// Manager wrote.
			Retryable: retryable,
			Left:      item.Left,
		})
	}
	return reply
}
