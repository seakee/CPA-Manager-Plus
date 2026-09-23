package quotaevaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/gatewaydecision"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/resourcepolicy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/decisionstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityprojection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/quotaobservation"
)

type Status string

const (
	StatusEvaluated Status = "evaluated"
	StatusSkipped   Status = "skipped"
	StatusPending   Status = "pending"
)

type EvaluationResult struct {
	Status Status
	Reason string
	Events []gatewaydecision.QuotaDecisionEvent
}

type Evaluator struct {
	observations quotaobservation.Repository
	decisions    decisionstore.Repository
}

func New(observations quotaobservation.Repository, decisions decisionstore.Repository) *Evaluator {
	return &Evaluator{observations: observations, decisions: decisions}
}

// EvaluateUsageEvent is invoked explicitly. All evidence and configuration
// are read from one SQLite snapshot before immutable audit rows are appended.
func (e *Evaluator) EvaluateUsageEvent(ctx context.Context, sourceUsageEventID, evaluatedAtMS int64) (EvaluationResult, error) {
	if sourceUsageEventID <= 0 || evaluatedAtMS <= 0 {
		return EvaluationResult{}, errors.New("positive source usage event ID and evaluatedAtMS required")
	}
	var result EvaluationResult
	var planned []gatewaydecision.QuotaDecisionEvent
	err := e.observations.WithSnapshot(ctx, func(v quotaobservation.View) error {
		source, err := v.Source(ctx, sourceUsageEventID)
		if err != nil {
			return err
		}
		state, err := v.State(ctx)
		if err != nil {
			return err
		}
		if state.SchemaVersion != 1 || state.LastProcessedEventID < 0 || state.BindingRevision < -1 {
			return errors.New("invalid projection state")
		}
		if state.Status != "ready" || state.LastProcessedEventID < source.ID {
			result = EvaluationResult{Status: StatusPending, Reason: "projection_not_ready"}
			return nil
		}
		if state.BindingRevision < 0 {
			return errors.New("ready projection has no binding revision")
		}
		projection, err := v.Projection(ctx, source.ID)
		if err != nil {
			return err
		}
		if projection == nil || projection.Validate() != nil || projection.UsageEventID != source.ID ||
			projection.EventHash != source.EventHash || projection.EvidenceTimestampMS != source.TimestampMS ||
			projection.SchemaVersion != state.SchemaVersion {
			return errors.New("source projection missing or inconsistent")
		}
		if projection.APIKeyState != identityprojection.StateMapped {
			result = EvaluationResult{Status: StatusSkipped, Reason: "api_key_" + string(projection.APIKeyState)}
			return nil
		}
		keyID, err := identity.ParseAPIKeyID(*projection.APIKeyID)
		if err != nil {
			return fmt.Errorf("invalid projected APIKeyID: %w", err)
		}
		binding, err := v.Binding(ctx, keyID)
		if err != nil {
			return err
		}
		if binding == nil {
			result = EvaluationResult{Status: StatusSkipped, Reason: "binding_missing"}
			return nil
		}
		if !binding.Enabled {
			result = EvaluationResult{Status: StatusSkipped, Reason: "binding_disabled"}
			return nil
		}
		policy, err := v.Policy(ctx, binding.PolicyID)
		if err != nil {
			return err
		}
		if policy.State == resourcepolicy.StateDisabled {
			result = EvaluationResult{Status: StatusSkipped, Reason: "policy_disabled"}
			return nil
		}
		fingerprintBytes := sha256.Sum256([]byte(source.EventHash))
		fingerprint := hex.EncodeToString(fingerprintBytes[:])
		for _, rule := range policy.Rules {
			event := gatewaydecision.QuotaDecisionEvent{
				SchemaVersion: gatewaydecision.SchemaVersion,
				APIKeyID:      keyID, BindingRevision: binding.Revision,
				PolicyID: policy.ID, PolicyRevision: policy.Revision,
				Metric: rule.Metric, LimitValue: rule.LimitValue,
				Enforcement: policy.Enforcement, Action: policy.Action,
				SourceUsageEventID: source.ID, SourceEventFingerprint: fingerprint,
				EvidenceTimestampMS: source.TimestampMS, EvaluatedAtMS: evaluatedAtMS,
			}
			window, windowErr := resourcepolicy.ResolveWindow(rule.Window, source.TimestampMS)
			if windowErr != nil || source.TimestampMS == math.MaxInt64 {
				event.Outcome, event.ReasonCode = gatewaydecision.OutcomeIndeterminate, "window_unresolvable"
			} else {
				event.WindowStartMS, event.WindowEndMS = &window.StartMS, &window.EndMS
				switch rule.Metric {
				case resourcepolicy.MetricCost:
					event.Outcome, event.ReasonCode = gatewaydecision.OutcomeIndeterminate, "cost_observation_unavailable"
				default:
					cappedEnd := min(window.EndMS, source.TimestampMS+1)
					observation, err := v.Observe(ctx, keyID, rule.Metric, source.ID, window.StartMS, cappedEnd)
					if err != nil {
						return err
					}
					if observation.Overflow {
						event.Outcome, event.ReasonCode = gatewaydecision.OutcomeIndeterminate, "observation_overflow"
					} else if rule.Metric == resourcepolicy.MetricToken && observation.TokenIncomplete {
						event.Outcome, event.ReasonCode = gatewaydecision.OutcomeIndeterminate, "token_evidence_incomplete"
						event.ObservedValue = observation.TokenSum
					} else {
						if rule.Metric == resourcepolicy.MetricRequest {
							event.ObservedValue = &observation.Count
						} else {
							event.ObservedValue = observation.TokenSum
						}
						if *event.ObservedValue < rule.LimitValue {
							event.Outcome, event.ReasonCode = gatewaydecision.OutcomeWithinLimit, "within_limit"
						} else {
							event.Outcome, event.ReasonCode = gatewaydecision.OutcomeNotifyRequired, "limit_reached"
						}
					}
				}
			}
			event.DedupeKey = dedupeKey(event, int64(state.SchemaVersion), state.BindingRevision)
			decisionID, err := gatewaydecision.NewDecisionID()
			if err != nil {
				return err
			}
			event.DecisionID = decisionID
			if err := event.Validate(); err != nil {
				return err
			}
			planned = append(planned, event)
		}
		result.Status = StatusEvaluated
		return nil
	})
	if err != nil {
		return EvaluationResult{}, err
	}
	for _, event := range planned {
		persisted, _, err := e.decisions.Append(ctx, event)
		if err != nil {
			return EvaluationResult{}, err
		}
		result.Events = append(result.Events, persisted)
	}
	return result, nil
}

// quota-shadow-eval-v1: NUL-delimited UTF-8 fields in the order below.
// Numbers are base-10; absent optional integers use the literal <nil>.
func dedupeKey(e gatewaydecision.QuotaDecisionEvent, projectionSchemaVersion, projectionBindingRevision int64) gatewaydecision.DedupeKey {
	fields := []string{
		"quota-shadow-eval-v1", strconv.FormatInt(e.SourceUsageEventID, 10), e.SourceEventFingerprint,
		strconv.FormatInt(e.EvidenceTimestampMS, 10), strconv.FormatInt(projectionSchemaVersion, 10),
		strconv.FormatInt(projectionBindingRevision, 10), string(e.APIKeyID),
		strconv.FormatUint(uint64(e.BindingRevision), 10), string(e.PolicyID),
		strconv.FormatUint(uint64(e.PolicyRevision), 10), string(e.Enforcement), string(e.Action),
		string(e.Metric), strconv.FormatInt(e.LimitValue, 10), optional(e.WindowStartMS),
		optional(e.WindowEndMS), optional(e.ObservedValue), string(e.Outcome), e.ReasonCode,
	}
	sum := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	return gatewaydecision.DedupeKey(hex.EncodeToString(sum[:]))
}

func optional(value *int64) string {
	if value == nil {
		return "<nil>"
	}
	return strconv.FormatInt(*value, 10)
}
