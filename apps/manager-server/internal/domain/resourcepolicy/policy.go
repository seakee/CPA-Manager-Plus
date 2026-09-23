package resourcepolicy

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
)

type Revision uint64

const InitialRevision Revision = 1

type State string
type Enforcement string
type Action string
type Metric string
type WindowKind string

const (
	StateActive   State = "active"
	StateDisabled State = "disabled"

	EnforcementObserved Enforcement = "observed"
	ActionNotify        Action      = "notify"

	MetricRequest Metric = "request"
	MetricToken   Metric = "token"
	MetricCost    Metric = "cost" // limit_value is in micro-USD (1 USD = 1,000,000).

	WindowRolling WindowKind = "rolling"
	WindowFixed   WindowKind = "fixed"
)

var (
	ErrInvalidPolicy  = errors.New("invalid quota policy")
	ErrInvalidBinding = errors.New("invalid API-key policy binding")
)

// WindowSpec describes a window without calculating its boundaries.
// An elapsed duration uses DurationMS; a calendar month uses CalendarMonths.
type WindowSpec struct {
	Kind           WindowKind
	DurationMS     *int64
	CalendarMonths *int64
	AnchorAtMS     *int64
	Timezone       string
}

func (w WindowSpec) Validate() error {
	switch w.Kind {
	case WindowRolling:
		if w.DurationMS == nil || *w.DurationMS <= 0 || w.CalendarMonths != nil || w.AnchorAtMS != nil || w.Timezone != "" {
			return fmt.Errorf("%w: invalid rolling duration window", ErrInvalidPolicy)
		}
	case WindowFixed:
		if w.AnchorAtMS == nil || *w.AnchorAtMS <= 0 {
			return fmt.Errorf("%w: fixed window requires a positive anchor", ErrInvalidPolicy)
		}
		if w.DurationMS != nil && *w.DurationMS > 0 && w.CalendarMonths == nil && w.Timezone == "" {
			return nil
		}
		if w.DurationMS == nil && w.CalendarMonths != nil && *w.CalendarMonths > 0 && w.Timezone != "" && w.Timezone != "Local" {
			if _, err := time.LoadLocation(w.Timezone); err == nil {
				return nil
			}
		}
		return fmt.Errorf("%w: invalid fixed duration or calendar window", ErrInvalidPolicy)
	default:
		return fmt.Errorf("%w: unknown window kind %q", ErrInvalidPolicy, w.Kind)
	}
	return nil
}

type QuotaRule struct {
	Metric     Metric
	LimitValue int64
	Window     WindowSpec
}

func (r QuotaRule) Validate() error {
	switch r.Metric {
	case MetricRequest, MetricToken, MetricCost:
	default:
		return fmt.Errorf("%w: unsupported metric %q", ErrInvalidPolicy, r.Metric)
	}
	if r.LimitValue <= 0 {
		return fmt.Errorf("%w: limit_value must be positive", ErrInvalidPolicy)
	}
	return r.Window.Validate()
}

type PolicySpec struct {
	Enforcement Enforcement
	Action      Action
	Rules       []QuotaRule
}

func (s PolicySpec) Validate() error {
	if s.Enforcement != EnforcementObserved || s.Action != ActionNotify {
		return fmt.Errorf("%w: only observed/notify is supported", ErrInvalidPolicy)
	}
	if len(s.Rules) < 1 || len(s.Rules) > 3 {
		return fmt.Errorf("%w: expected 1 to 3 rules", ErrInvalidPolicy)
	}
	seen := make(map[Metric]bool, len(s.Rules))
	for _, rule := range s.Rules {
		if err := rule.Validate(); err != nil {
			return err
		}
		if seen[rule.Metric] {
			return fmt.Errorf("%w: duplicate metric %q", ErrInvalidPolicy, rule.Metric)
		}
		seen[rule.Metric] = true
	}
	return nil
}

type QuotaPolicy struct {
	ID       PolicyID
	Revision Revision
	State    State
	PolicySpec
	CreatedAtMS int64
	UpdatedAtMS int64
}

func (p QuotaPolicy) Validate() error {
	if err := p.ID.Validate(); err != nil {
		return err
	}
	if p.Revision < 1 || p.Revision > math.MaxInt64 {
		return fmt.Errorf("%w: revision out of range", ErrInvalidPolicy)
	}
	if p.State != StateActive && p.State != StateDisabled {
		return fmt.Errorf("%w: unsupported state %q", ErrInvalidPolicy, p.State)
	}
	if p.CreatedAtMS <= 0 || p.UpdatedAtMS < p.CreatedAtMS {
		return fmt.Errorf("%w: invalid timestamps", ErrInvalidPolicy)
	}
	return p.PolicySpec.Validate()
}

// PolicyBinding has one stable slot per Canonical APIKeyID. It contains no
// source key, alias, runtime identity, or credential information.
type PolicyBinding struct {
	APIKeyID    identity.APIKeyID
	PolicyID    PolicyID
	Revision    Revision
	Enabled     bool
	CreatedAtMS int64
	UpdatedAtMS int64
}

func (b PolicyBinding) Validate() error {
	if err := b.APIKeyID.Validate(); err != nil {
		return err
	}
	if err := b.PolicyID.Validate(); err != nil {
		return err
	}
	if b.Revision < 1 || b.Revision > math.MaxInt64 || b.CreatedAtMS <= 0 || b.UpdatedAtMS < b.CreatedAtMS {
		return ErrInvalidBinding
	}
	return nil
}
