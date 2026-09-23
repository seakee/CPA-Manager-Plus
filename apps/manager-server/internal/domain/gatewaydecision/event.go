package gatewaydecision

import (
	"errors"
	"fmt"
	"math"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/resourcepolicy"
)

const SchemaVersion int64 = 1

type Outcome string

const (
	OutcomeWithinLimit    Outcome = "within_limit"
	OutcomeNotifyRequired Outcome = "notify_required"
	OutcomeIndeterminate  Outcome = "indeterminate"
)

var ErrInvalidEvent = errors.New("invalid quota decision event")

// QuotaDecisionEvent is both the decision result and its immutable audit event.
// Cost values are integer micro-USD; request and token values are counts.
// SourceEventFingerprint is SHA-256 of the exact bytes of the stored
// usage_events.event_hash text. It does not copy the raw historical hash;
// the future evaluator calculates this fingerprint from its source event.
type QuotaDecisionEvent struct {
	DecisionID             DecisionID
	SchemaVersion          int64
	DedupeKey              DedupeKey
	APIKeyID               identity.APIKeyID
	PolicyID               resourcepolicy.PolicyID
	PolicyRevision         resourcepolicy.Revision
	BindingRevision        resourcepolicy.Revision
	Metric                 resourcepolicy.Metric
	Enforcement            resourcepolicy.Enforcement
	Action                 resourcepolicy.Action
	Outcome                Outcome
	ReasonCode             string
	LimitValue             int64
	ObservedValue          *int64
	WindowStartMS          *int64
	WindowEndMS            *int64
	SourceUsageEventID     int64
	SourceEventFingerprint string
	EvidenceTimestampMS    int64
	EvaluatedAtMS          int64
}

func (e QuotaDecisionEvent) Validate() error {
	if err := e.DecisionID.Validate(); err != nil {
		return err
	}
	if e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: schema_version must be 1", ErrInvalidEvent)
	}
	if err := e.DedupeKey.Validate(); err != nil {
		return err
	}
	if err := e.APIKeyID.Validate(); err != nil {
		return err
	}
	if err := e.PolicyID.Validate(); err != nil {
		return err
	}
	if e.PolicyRevision < 1 || e.PolicyRevision > math.MaxInt64 || e.BindingRevision < 1 || e.BindingRevision > math.MaxInt64 {
		return fmt.Errorf("%w: revisions must be positive signed 64-bit integers", ErrInvalidEvent)
	}
	switch e.Metric {
	case resourcepolicy.MetricRequest, resourcepolicy.MetricToken, resourcepolicy.MetricCost:
	default:
		return fmt.Errorf("%w: unsupported metric", ErrInvalidEvent)
	}
	if e.Enforcement != resourcepolicy.EnforcementObserved || e.Action != resourcepolicy.ActionNotify {
		return fmt.Errorf("%w: only observed/notify is supported", ErrInvalidEvent)
	}
	if !validReasonCode(e.ReasonCode) {
		return fmt.Errorf("%w: invalid reason_code", ErrInvalidEvent)
	}
	if e.LimitValue <= 0 || (e.ObservedValue != nil && *e.ObservedValue < 0) {
		return fmt.Errorf("%w: invalid quota values", ErrInvalidEvent)
	}
	if (e.WindowStartMS == nil) != (e.WindowEndMS == nil) {
		return fmt.Errorf("%w: partial window", ErrInvalidEvent)
	}
	if e.WindowStartMS != nil && (*e.WindowStartMS <= 0 || *e.WindowEndMS <= *e.WindowStartMS) {
		return fmt.Errorf("%w: invalid window", ErrInvalidEvent)
	}
	switch e.Outcome {
	case OutcomeWithinLimit:
		if e.ObservedValue == nil || e.WindowStartMS == nil || *e.ObservedValue >= e.LimitValue {
			return fmt.Errorf("%w: inconsistent within_limit evidence", ErrInvalidEvent)
		}
	case OutcomeNotifyRequired:
		if e.ObservedValue == nil || e.WindowStartMS == nil || *e.ObservedValue < e.LimitValue {
			return fmt.Errorf("%w: inconsistent notify_required evidence", ErrInvalidEvent)
		}
	case OutcomeIndeterminate:
		// Partial observation and an unresolved window are both valid evidence.
	default:
		return fmt.Errorf("%w: unsupported outcome", ErrInvalidEvent)
	}
	if e.SourceUsageEventID <= 0 || !isLowerHex(e.SourceEventFingerprint, 64) || e.EvidenceTimestampMS <= 0 || e.EvaluatedAtMS <= 0 {
		return fmt.Errorf("%w: invalid usage provenance", ErrInvalidEvent)
	}
	return nil
}

func validReasonCode(code string) bool {
	if len(code) < 1 || len(code) > 64 {
		return false
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

// SameSemanticContent compares the stable producer-operation fields. A retry
// may regenerate DecisionID and EvaluatedAtMS; the first persisted values of
// both remain in the audit row returned by Append.
func (e QuotaDecisionEvent) SameSemanticContent(other QuotaDecisionEvent) bool {
	return e.SchemaVersion == other.SchemaVersion && e.DedupeKey == other.DedupeKey &&
		e.APIKeyID == other.APIKeyID && e.PolicyID == other.PolicyID &&
		e.PolicyRevision == other.PolicyRevision && e.BindingRevision == other.BindingRevision &&
		e.Metric == other.Metric && e.Enforcement == other.Enforcement &&
		e.Action == other.Action && e.Outcome == other.Outcome && e.ReasonCode == other.ReasonCode &&
		e.LimitValue == other.LimitValue && equalOptionalInt(e.ObservedValue, other.ObservedValue) &&
		equalOptionalInt(e.WindowStartMS, other.WindowStartMS) && equalOptionalInt(e.WindowEndMS, other.WindowEndMS) &&
		e.SourceUsageEventID == other.SourceUsageEventID && e.SourceEventFingerprint == other.SourceEventFingerprint &&
		e.EvidenceTimestampMS == other.EvidenceTimestampMS
}

func equalOptionalInt(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
