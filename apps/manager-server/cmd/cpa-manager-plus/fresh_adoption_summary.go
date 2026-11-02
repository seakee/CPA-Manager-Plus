package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

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
// untrusted metadata cannot reach the browser as prose. An overlong credential
// name is recorded with a readable prefix and a digest of the unscrubbed name;
// that digest is dropped here as well, because it would confirm a candidate
// value offline.
type adoptionSummaryItem struct {
	// ID is the record's stable identity for this item, independent of the text
	// the reply may show. Two token-shaped file names scrub to the same `item`,
	// and a browser that identified them by that text would treat two different
	// resources as one: an acknowledgement, a list key and a request are about the
	// record's item, so they are bound to a digest of the scrubbed text and the
	// item's ordinal. The digest never covers the record's own name: that name can
	// be a credential, and a digest of it would confirm a candidate value offline.
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
// value, and the shortest fragment of one it recognises. A shorter string
// cannot be told apart inside a name without replacing arbitrary text, so a
// source credential under eight bytes is the one value this boundary does not
// claim to catch.
const minRedactedValue = 8

// adoptionRedactor is the summary's output boundary for one string of the
// record: the project's credential shapes first, then the values the migration
// read.
//
// The value rule matches prefixes as well as the whole value. An overlong name
// is recorded as a bounded prefix, and a value can cross that cut, so the
// fragment a whole-value match would leave behind is exactly a prefix of the
// value. Every prefix of at least minRedactedValue bytes is therefore replaced,
// not only the value itself.
func adoptionRedactor(secrets []string) func(string) string {
	ordered := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if len(secret) >= minRedactedValue && secret != redactedValue {
			ordered = append(ordered, secret)
		}
	}
	return func(value string) string {
		if value == "" {
			return ""
		}
		// The scrubber's own markers are final: a source value that happens to
		// begin with the marker's text must not rewrite it.
		segments := strings.Split(usage.SanitizeCredentialText(value), redactedValue)
		for index, segment := range segments {
			segments[index] = redactSourceValues(segment, ordered)
		}
		return strings.Join(segments, redactedValue)
	}
}

// redactSourceValues replaces the source values in value in one left-to-right
// pass, writing only text it has not replaced.
//
// At every position it takes the longest prefix any value still matches. That
// choice is what keeps one value's short common prefix from consuming the text
// another value would match whole: two Client Keys may share their opening
// bytes, and rewriting the text once per value would let the longer value
// replace that shared prefix first, leaving the shorter value's own suffix
// behind. Choosing per position keeps the result the same whatever order the
// values were read in, and the marker it writes is never scanned again.
func redactSourceValues(value string, secrets []string) string {
	var out strings.Builder
	for at := 0; at < len(value); {
		matched := 0
		for _, secret := range secrets {
			needle := shortestRedactedPrefix(secret)
			if needle == "" || !strings.HasPrefix(value[at:], needle) {
				continue
			}
			// The match is at least the needle; it is the part of the value this
			// text still holds, which is what a recorded truncation leaves.
			length := len(needle)
			for length < len(secret) && at+length < len(value) && value[at+length] == secret[length] {
				length++
			}
			// Cut on a rune boundary of the value, so the replacement never splits
			// a character.
			for length > len(needle) && at+length < len(value) && !utf8.RuneStart(value[at+length]) {
				length--
			}
			if length > matched {
				matched = length
			}
		}
		if matched == 0 {
			// No value starts here: copy one character and keep scanning.
			_, size := utf8.DecodeRuneInString(value[at:])
			out.WriteString(value[at : at+size])
			at += size
			continue
		}
		out.WriteString(redactedValue)
		at += matched
	}
	return out.String()
}

// shortestRedactedPrefix is the smallest prefix of a source value this boundary
// recognises, cut on a rune boundary so the needle is valid text of its own.
func shortestRedactedPrefix(secret string) string {
	cut := minRedactedValue
	for cut < len(secret) && !utf8.RuneStart(secret[cut]) {
		cut++
	}
	if cut > len(secret) {
		return ""
	}
	return secret[:cut]
}

// overlongNameDigestMarker is how the executor marks a credential identity cut
// short: the readable prefix, this marker and the SHA-256 of the whole name.
const overlongNameDigestMarker = "…#sha256:"

// withoutNameDigest drops the record's digest of an overlong credential name
// from the text the reply shows.
//
// That digest is an unsalted SHA-256 of the unscrubbed name, so a reader who
// knows the rest of the name could confirm a guessed value from it offline. The
// record keeps it — it is what makes the record's own identity stable across
// replays — but the reply states progress, not evidence, and a digest that
// confirms a candidate credential is not something this boundary may emit. The
// readable prefix and the ellipsis that says the name was cut stay; the item's
// ordinal in the reply's identity keeps two cut names apart.
func withoutNameDigest(item string) string {
	at := strings.LastIndex(item, overlongNameDigestMarker)
	if at < 0 || len(item) != at+len(overlongNameDigestMarker)+sha256.Size*2 {
		return item
	}
	if _, err := hex.DecodeString(item[at+len(overlongNameDigestMarker):]); err != nil {
		return item
	}
	return item[:at] + "…"
}

// adoptionItemID is the stable, non-secret identity of one recorded item.
//
// It digests the text the reply shows and the item's ordinal in the record,
// never the record's own name: the record's name can be a credential the reply
// redacted, and a digest of it would confirm a candidate value offline. The
// ordinal keeps two items whose text scrubs to the same string apart, and the
// record's own order keeps the identity stable across reads.
func adoptionItemID(redactedItem string, ordinal int) string {
	sum := sha256.Sum256([]byte(redactedItem + "\x00" + strconv.Itoa(ordinal)))
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
	for ordinal, item := range summary.Items {
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
		itemText := redact(withoutNameDigest(item.Item))
		reply.Items = append(reply.Items, adoptionSummaryItem{
			ID:      adoptionItemID(itemText, ordinal),
			Item:    itemText,
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
