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

// AdoptionOutcome is the closed set of per-item results. "migrated" is the only
// outcome a rerun skips; "manual" and "failed" are both reported to the
// operator and neither fails the adoption.
type AdoptionOutcome string

const (
	AdoptionMigrated AdoptionOutcome = "migrated"
	AdoptionManual   AdoptionOutcome = "manual"
	AdoptionFailed   AdoptionOutcome = "failed"
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

// Settled reports whether this exact item already has a recorded outcome. A
// rerun skips every settled item: a success must never be written twice, and a
// failed item is an unknown write whose only permitted resolution is the
// read-back that produced the record -- resending it would be exactly the write
// the checkpoint exists to prevent.
func (s AdoptionSummary) Settled(name string) bool {
	_, ok := s.Item(name)
	return ok
}

// Counts reports the number of items per outcome, in a fixed order.
func (s AdoptionSummary) Counts() (migrated, manual, failed int) {
	for _, item := range s.Items {
		switch item.Outcome {
		case AdoptionMigrated:
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
	case AdoptionMigrated, AdoptionManual, AdoptionFailed:
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
	}
	return nil
}

// validAdoptionSummaryUpdate keeps the summary monotone: a callback may append
// an item, and may never rewrite or erase a result the Manager already
// checkpointed. The unknown-result reconcile and the rerun skip both depend on
// a recorded success staying recorded.
func validAdoptionSummaryUpdate(before, after *AdoptionSummary) error {
	if before == nil {
		return nil
	}
	if after == nil || len(after.Items) < len(before.Items) {
		return errors.New("adoption summary may not shrink")
	}
	for index, item := range before.Items {
		if after.Items[index] != item {
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
