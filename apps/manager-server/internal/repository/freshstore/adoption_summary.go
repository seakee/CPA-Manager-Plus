package freshstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

// AdoptionItemKind is the closed set of things the Adopt migration executor
// copies. A kind outside this set is never persisted: the summary is a
// product-owned record, so it cannot become a place an upstream CPA writes
// arbitrary taxonomy into.
type AdoptionItemKind string

const (
	AdoptionKindCredential      AdoptionItemKind = "credential"
	AdoptionKindConfigLeaf      AdoptionItemKind = "config_leaf"
	AdoptionKindConfigFamily    AdoptionItemKind = "config_family"
	AdoptionKindProviderSection AdoptionItemKind = "provider_section"
)

// AdoptionOutcome is the closed set of per-item results. Every outcome is a
// recorded fact about one item; none of them fails the adoption, and a rerun
// skips a settled item rather than writing it again.
//
// "absent_at_source" is the source having nothing to copy. Recording it is what
// makes a replay safe after completion: an item the source does not set is
// settled, so a later replay cannot mistake the destination's own value for a
// stale copy and overwrite it.
type AdoptionOutcome string

const (
	AdoptionMigrated       AdoptionOutcome = "migrated"
	AdoptionManual         AdoptionOutcome = "manual"
	AdoptionFailed         AdoptionOutcome = "failed"
	AdoptionAbsentAtSource AdoptionOutcome = "absent_at_source"
)

// AdoptionItem is one bounded, non-secret migration result. It carries the item
// identity an operator can act on, a bounded reason or instruction, and the
// digests of the bytes that were compared — never a credential byte, a provider
// key or a configuration value.
type AdoptionItem struct {
	Item              string           `json:"item"`
	Kind              AdoptionItemKind `json:"kind"`
	Outcome           AdoptionOutcome  `json:"outcome"`
	Reason            string           `json:"reason,omitempty"`
	Instruction       string           `json:"instruction,omitempty"`
	SourceSHA256      string           `json:"sourceSha256,omitempty"`
	DestinationSHA256 string           `json:"destinationSha256,omitempty"`
}

// AdoptionSummary is what the executor checkpointed so far, in migration order.
// It is the durable record Step 4's completion conditions read and the only
// place a per-item result survives a Manager restart.
type AdoptionSummary struct {
	Items []AdoptionItem `json:"items"`
}

// Index returns the position of one item name in the recorded order.
func (s AdoptionSummary) Index(name string) int {
	for index, item := range s.Items {
		if item.Item == name {
			return index
		}
	}
	return -1
}

// Item returns the recorded result of one item name.
func (s AdoptionSummary) Item(name string) (AdoptionItem, bool) {
	for _, item := range s.Items {
		if item.Item == name {
			return item, true
		}
	}
	return AdoptionItem{}, false
}

// Migrated reports whether this exact item already succeeded.
func (s AdoptionSummary) Migrated(name string) bool {
	item, ok := s.Item(name)
	return ok && item.Outcome == AdoptionMigrated
}

// Settled reports whether this exact item already has a recorded outcome that a
// rerun must not repeat.
//
// Every outcome is settled except a failure that happened before any write: that
// one recorded nothing, so retrying it is the only way the item can ever be
// copied. A failure after a write attempt stays settled, because its only
// permitted resolution is the read-back that produced it.
func (s AdoptionSummary) Settled(name string) bool {
	item, ok := s.Item(name)
	return ok && !(item.Outcome == AdoptionFailed && retryableReason(item.Reason))
}

// Retryable reports whether this item's failure happened before anything was
// written, so a later run may attempt it again.
//
// The distinction is the whole point: a run that could not read the source wrote
// nothing, and retrying it is the only way to copy the item. A run that wrote and
// could not decide the result is an unknown effect, and its only permitted
// resolution is the read-back that produced the record, never a second write.
func (s AdoptionSummary) Retryable(name string) bool {
	item, ok := s.Item(name)
	return ok && item.Outcome == AdoptionFailed && retryableReason(item.Reason)
}

// retryableReasons are the failure reasons this package produces before any
// destination write. Everything else is an unknown effect.
var retryableReasons = map[string]bool{
	"download_failed":   true,
	"source_unreadable": true,
}

func retryableReason(reason string) bool { return retryableReasons[reason] }

// RetryableReason reports whether one failure reason was produced before any
// write, so a producer can classify a failure the same way this store does.
func RetryableReason(reason string) bool { return retryableReason(reason) }

// Counts reports the number of items per outcome, in a fixed order.
func (s AdoptionSummary) Counts() (migrated, manual, failed int) {
	for _, item := range s.Items {
		switch item.Outcome {
		case AdoptionMigrated, AdoptionAbsentAtSource:
			migrated++
		case AdoptionManual:
			manual++
		case AdoptionFailed:
			failed++
		}
	}
	return migrated, manual, failed
}

const (
	// maxAdoptionItemName bounds one stable item identity: a credential file
	// name, a dotted configuration path or a provider section name.
	maxAdoptionItemName = 256
	// maxAdoptionItems bounds the whole summary. The migration inventory is a
	// few dozen entries; a larger summary was not produced by this executor.
	maxAdoptionItems       = 1024
	maxAdoptionReason      = 120
	maxAdoptionInstruction = 512
)

// MaxAdoptionItems is the bound this store enforces on one whole summary. A
// producer that plans an inventory has to stay below it: a summary the store
// refuses is a run that stops, and stopping on a bound is exactly the loop the
// preflight exists to prevent.
func MaxAdoptionItems() int { return maxAdoptionItems }

// MaxAdoptionItemName is the bound on one stable item identity.
func MaxAdoptionItemName() int { return maxAdoptionItemName }

// ValidateAdoptionSummary reports whether the store would accept this summary.
// It is the same check the write path applies, so a producer can prove its plan
// fits before it writes anything.
func ValidateAdoptionSummary(summary AdoptionSummary) error { return validAdoptionSummary(summary) }

func validAdoptionItemKind(kind AdoptionItemKind) bool {
	switch kind {
	case AdoptionKindCredential, AdoptionKindConfigLeaf, AdoptionKindConfigFamily, AdoptionKindProviderSection:
		return true
	default:
		return false
	}
}

func validAdoptionOutcome(outcome AdoptionOutcome) bool {
	switch outcome {
	case AdoptionMigrated, AdoptionManual, AdoptionFailed, AdoptionAbsentAtSource:
		return true
	default:
		return false
	}
}

// validAdoptionDigest accepts an absent digest or one lower-case SHA-256 hex
// digest. A digest is the only thing the summary may carry about item bytes.
func validAdoptionDigest(digest string) bool {
	if digest == "" {
		return true
	}
	if len(digest) != 64 {
		return false
	}
	for _, c := range digest {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

// validAdoptionSummary accepts only the bounded shape the executor emits. It
// never inspects a value, because a value is never stored.
func validAdoptionSummary(summary AdoptionSummary) error {
	if len(summary.Items) > maxAdoptionItems {
		return errors.New("adoption summary exceeds its item bound")
	}
	seen := map[string]bool{}
	for _, item := range summary.Items {
		if item.Item == "" || len(item.Item) > maxAdoptionItemName || strings.TrimSpace(item.Item) != item.Item || seen[item.Item] {
			return errors.New("invalid or duplicate adoption item")
		}
		seen[item.Item] = true
		if !validAdoptionItemKind(item.Kind) || !validAdoptionOutcome(item.Outcome) {
			return errors.New("invalid adoption item vocabulary")
		}
		if len(item.Reason) > maxAdoptionReason || len(item.Instruction) > maxAdoptionInstruction {
			return errors.New("adoption item text exceeds its bound")
		}
		if !validAdoptionDigest(item.SourceSHA256) || !validAdoptionDigest(item.DestinationSHA256) {
			return errors.New("invalid adoption item digest")
		}
		if item.Outcome == AdoptionMigrated && (item.Instruction != "" || item.Reason != "") {
			return errors.New("migrated adoption item cannot carry a reason or instruction")
		}
		if item.Outcome == AdoptionAbsentAtSource && item.Instruction != "" {
			return errors.New("absent adoption item cannot carry an instruction")
		}
	}
	return nil
}

// validAdoptionSummaryUpdate keeps the summary monotone: a callback may append
// an item, and may never rewrite or erase a result the Manager already
// checkpointed. The unknown-result reconcile and the rerun skip both depend on
// a recorded success staying recorded.
//
// The one permitted difference is the provisional record of a retryable failure:
// a run that wrote nothing recorded nothing to protect, so the first real
// outcome of that item replaces it. It can never replace anything else, and it
// never moves an item.
func validAdoptionSummaryUpdate(before, after *AdoptionSummary) error {
	if before == nil {
		return nil
	}
	if after == nil || len(after.Items) < len(before.Items) {
		return errors.New("adoption summary may not shrink")
	}
	for index, item := range before.Items {
		if after.Items[index] == item {
			continue
		}
		provisional := item.Outcome == AdoptionFailed && retryableReason(item.Reason)
		if !provisional || after.Items[index].Item != item.Item {
			return errors.New("adoption summary item rewritten")
		}
	}
	return nil
}

func cloneAdoptionSummary(summary *AdoptionSummary) *AdoptionSummary {
	if summary == nil {
		return nil
	}
	copied := AdoptionSummary{Items: append([]AdoptionItem(nil), summary.Items...)}
	return &copied
}

// adoptionSummaryKey is the settings row of the per-item Adopt migration record.
// It is written through the same CAS transaction as the operation checkpoint, so
// a recorded success and the revision that recorded it can never disagree.
const adoptionSummaryKey = "fresh_adoption_summary_v1"

// loadAdoptionSummary reads the record. Only an adopted installation carries
// one; any other strategy with a summary row has damaged ownership and is
// refused instead of trusted.
func loadAdoptionSummary(ctx context.Context, q queryer, operation bootstrap.FreshOperation) (*AdoptionSummary, error) {
	var data string
	err := q.QueryRowContext(ctx, "select value from settings where key=?", adoptionSummaryKey).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if operation.Strategy.Strategy != bootstrap.AdoptExisting {
		return nil, ErrNotFresh
	}
	var summary AdoptionSummary
	if json.Unmarshal([]byte(data), &summary) != nil || validAdoptionSummary(summary) != nil {
		return nil, ErrNotFresh
	}
	return &summary, nil
}

func writeAdoptionSummary(ctx context.Context, tx *sql.Tx, summary *AdoptionSummary) error {
	if summary == nil {
		return nil
	}
	if err := validAdoptionSummary(*summary); err != nil {
		return ErrConflict
	}
	return write(ctx, tx, adoptionSummaryKey, summary)
}
