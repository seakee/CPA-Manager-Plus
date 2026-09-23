package policystore

import (
	"context"
	"errors"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/resourcepolicy"
)

var (
	ErrNotFound         = errors.New("policy or binding not found")
	ErrRevisionConflict = errors.New("policy or binding revision conflict")
	ErrRevisionOverflow = errors.New("policy or binding revision overflow")
	ErrSupersededAPIKey = errors.New("superseded APIKeyID cannot be bound or rebound")
)

type Repository interface {
	CreatePolicy(context.Context, resourcepolicy.QuotaPolicy) error
	LoadPolicy(context.Context, resourcepolicy.PolicyID) (resourcepolicy.QuotaPolicy, error)
	ReplacePolicySpec(context.Context, resourcepolicy.PolicyID, resourcepolicy.Revision, resourcepolicy.PolicySpec, int64) (resourcepolicy.QuotaPolicy, error)
	SetPolicyState(context.Context, resourcepolicy.PolicyID, resourcepolicy.Revision, resourcepolicy.State, int64) (resourcepolicy.QuotaPolicy, error)
	BindAPIKey(context.Context, resourcepolicy.PolicyBinding) error
	LoadBinding(context.Context, identity.APIKeyID) (resourcepolicy.PolicyBinding, error)
	RebindAPIKey(context.Context, identity.APIKeyID, resourcepolicy.Revision, resourcepolicy.PolicyID, int64) (resourcepolicy.PolicyBinding, error)
	SetBindingEnabled(context.Context, identity.APIKeyID, resourcepolicy.Revision, bool, int64) (resourcepolicy.PolicyBinding, error)
}
