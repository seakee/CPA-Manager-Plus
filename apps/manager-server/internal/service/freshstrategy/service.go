// Package freshstrategy implements the bounded strategy adapter of the current
// Fresh operation. Persistence and browser authorization remain with its caller.
package freshstrategy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

type Blocker struct {
	Code        string
	Unsupported []string
}

func (e *Blocker) Error() string { return e.Code }

// Qualification is exclusively server-owned accepted evidence, never a browser
// assertion. In particular, a successful config read cannot set these facts.
type Options struct {
	Runtime             runtimeservice.RuntimeClient
	Protector           *security.Protector
	Qualification       bootstrap.CPAQualification
	QualifiedArtifactID model.RuntimeArtifactID
	AdoptionResources   []bootstrap.AdoptionResourceDecision
	DestinationBaseURL  string
}

type Service struct {
	options    Options
	production bool
	consumers  map[consumerKey]qualifiedConsumer
}

func New(options Options) *Service { return &Service{options: options} }

type Request struct {
	Strategy                  bootstrap.CPAStrategy      `json:"strategy"`
	BaseURL                   string                     `json:"baseUrl,omitempty"`
	ManagementKey             string                     `json:"managementKey,omitempty"`
	ExpectedRuntimeIdentity   model.RuntimeIdentity      `json:"expectedRuntimeIdentity,omitempty"`
	ExpectedRuntimeGeneration model.RuntimeGeneration    `json:"expectedRuntimeGeneration,omitempty"`
	ExpectedArtifactID        model.RuntimeArtifactID    `json:"expectedArtifactId,omitempty"`
	AdoptSourceClass          bootstrap.AdoptSourceClass `json:"adoptSourceClass,omitempty"`
	// Loaded only by the application from the existing protected Fresh store.
	AdoptionCiphertext string `json:"-"`
}

// ProtectedConnection is an internal storage handoff, never part of the public
// result or an operation/routing/installer journal. The destination must persist
// it with canonical connection settings in the same Fresh checkpoint transaction.
type ProtectedConnection struct {
	BaseURL                 string
	ManagementKeyCiphertext string
}

type Result struct {
	Checkpoint         bootstrap.FreshStrategyCheckpoint `json:"checkpoint"`
	Connection         *ProtectedConnection              `json:"-"`
	RuntimeMode        model.RuntimeMode                 `json:"runtimeMode"`
	GatewayPending     bool                              `json:"gatewayPending"`
	SourcePreserved    bool                              `json:"sourcePreserved"`
	AdoptionCiphertext string                            `json:"-"`
}

// Execute requires an authenticated, loaded current operation. NewManaged first
// returns staged intent without mutating Runtime; the caller must CAS-persist
// that checkpoint before invoking Execute again. Repeated Start uses the same
// Fresh-owned child operation ID and the Supervisor's durable idempotency.
func (s *Service) Execute(ctx context.Context, op bootstrap.FreshOperation, request Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	// A completed operation can only reconcile its validated committed shape;
	// both adapters return that checkpoint without another mutation.
	if op.Validate() != nil || !op.AdminInitialized {
		return Result{}, &Blocker{Code: "fresh_strategy_authority_unavailable"}
	}
	if op.Strategy.Strategy != "" && op.Strategy.Strategy != request.Strategy {
		return Result{}, &Blocker{Code: "fresh_strategy_owner_conflict"}
	}
	// Scope an accepted fact to this exact consumer. External safe-read must
	// never implicitly grant network adoption or a legacy transition.
	scoped := *s
	if s.production {
		consumer := s.consumers[consumerKey{request.Strategy, request.AdoptSourceClass}]
		scoped.options.Qualification = consumer.Qualification
		scoped.options.QualifiedArtifactID = consumer.ArtifactID
		scoped.options.AdoptionResources = consumer.AdoptionResources
	}
	switch request.Strategy {
	case bootstrap.NewManaged:
		return scoped.newManaged(ctx, op, request)
	case bootstrap.ConnectExternal, bootstrap.AdoptExisting:
		return scoped.existing(ctx, op, request)
	default:
		return Result{}, &Blocker{Code: "fresh_strategy_invalid"}
	}
}

func (s *Service) newManaged(ctx context.Context, op bootstrap.FreshOperation, request Request) (Result, error) {
	result := Result{RuntimeMode: model.RuntimeModeEmbedded, GatewayPending: true}
	if s.options.Qualification.ValidateForStrategy(bootstrap.NewManaged, bootstrap.AdoptSourceNone) != nil {
		return result, &Blocker{Code: "exact_v8_artifact_qualification_required"}
	}
	if s.options.Runtime == nil {
		return result, &Blocker{Code: "managed_target_staging_required"}
	}
	status, err := s.options.Runtime.Status(ctx)
	if err != nil || status.Validate() != nil || status.ProtocolVersion != "v1" || status.ActiveGatewayArtifact == nil {
		return result, &Blocker{Code: "managed_target_observation_unavailable"}
	}
	if !s.options.QualifiedArtifactID.IsValid() || status.ActiveGatewayArtifact.ArtifactID != s.options.QualifiedArtifactID {
		return result, &Blocker{Code: "exact_v8_artifact_qualification_required"}
	}
	checkpoint := op.Strategy
	if request.ExpectedRuntimeIdentity != status.Identity || request.ExpectedRuntimeGeneration != status.Generation || request.ExpectedArtifactID != status.ActiveGatewayArtifact.ArtifactID {
		return result, &Blocker{Code: "managed_target_identity_mismatch"}
	}
	if checkpoint.Strategy == "" {
		result.Checkpoint = bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged,
			TargetIdentity: string(status.Identity), TargetGeneration: uint64(status.Generation),
			TargetArtifactID: string(status.ActiveGatewayArtifact.ArtifactID), Stage: "staged"}
		return result, nil
	}
	result.Checkpoint = checkpoint
	if checkpoint.TargetIdentity != string(status.Identity) || checkpoint.TargetGeneration != uint64(status.Generation) || checkpoint.TargetArtifactID != string(status.ActiveGatewayArtifact.ArtifactID) {
		return result, &Blocker{Code: "managed_target_identity_mismatch"}
	}
	switch checkpoint.Stage {
	case "committed":
		return result, nil
	case "staged", "starting":
	default:
		return result, &Blocker{Code: "managed_target_checkpoint_invalid"}
	}
	if !status.Capabilities.Supports(model.RuntimeCapabilityStart) {
		return result, &Blocker{Code: "managed_target_start_unsupported"}
	}
	response, err := s.options.Runtime.Start(ctx, model.RuntimeMutationRequest{
		OperationID:             string(op.Identity.OperationID) + ":managed-start",
		ExpectedRuntimeIdentity: status.Identity, ExpectedRuntimeGeneration: status.Generation,
		ExpectedActiveArtifactID: model.RuntimeArtifactID(checkpoint.TargetArtifactID),
	})
	if err != nil {
		return result, &Blocker{Code: "managed_target_start_unresolved"}
	}
	if response.Validate() != nil || response.OperationID != string(op.Identity.OperationID)+":managed-start" || response.OperationType != model.RuntimeOperationStart || response.RuntimeIdentity != status.Identity || response.RuntimeGeneration != status.Generation {
		return result, &Blocker{Code: "managed_target_start_unresolved"}
	}
	switch response.State {
	case model.RuntimeOperationSucceeded:
		result.Checkpoint.Stage = "committed"
	case model.RuntimeOperationAccepted, model.RuntimeOperationRunning:
		result.Checkpoint.Stage = "starting"
	default:
		return result, &Blocker{Code: "managed_target_start_failed"}
	}
	return result, nil
}

func (s *Service) existing(ctx context.Context, op bootstrap.FreshOperation, request Request) (Result, error) {
	result := Result{GatewayPending: true, SourcePreserved: true}
	if request.Strategy == bootstrap.AdoptExisting {
		result.RuntimeMode = model.RuntimeModeEmbedded
		if s.options.Qualification.ValidateForStrategy(bootstrap.AdoptExisting, request.AdoptSourceClass) != nil {
			return result, &Blocker{Code: "adoption_source_qualification_required"}
		}
		return s.adopt(ctx, op, request)
	}

	result.RuntimeMode = model.RuntimeModeExternal
	if s.options.Qualification.ValidateForStrategy(bootstrap.ConnectExternal, bootstrap.AdoptSourceNone) != nil {
		return result, &Blocker{Code: "required_v8_profile_qualification_required"}
	}
	baseURL, err := cpa.NormalizeV8BaseURL(request.BaseURL)
	if err != nil {
		return result, &Blocker{Code: cpa.V8InvalidConnection.Error()}
	}
	targetHash := sha256.Sum256([]byte(baseURL))
	targetID := "external:sha256:" + hex.EncodeToString(targetHash[:])
	if op.Strategy.TargetIdentity != "" && op.Strategy.TargetIdentity != targetID {
		return result, &Blocker{Code: "fresh_strategy_owner_conflict"}
	}
	observation, err := cpa.ReadV8Config(ctx, request.BaseURL, request.ManagementKey)
	if err != nil {
		var readErr cpa.V8ReadError
		if errors.As(err, &readErr) {
			return result, &Blocker{Code: readErr.Error()}
		}
		return result, &Blocker{Code: "management_safe_read_unavailable"}
	}
	if op.Strategy.Stage == "committed" {
		result.Checkpoint = op.Strategy
		return result, nil
	}
	result.Checkpoint = bootstrap.FreshStrategyCheckpoint{Strategy: request.Strategy, TargetIdentity: targetID, TargetGeneration: 1, Stage: "validated"}
	if s.options.Protector == nil {
		return result, &Blocker{Code: "management_secret_storage_unavailable"}
	}
	ciphertext, err := s.options.Protector.ProtectString(request.ManagementKey)
	if err != nil {
		return result, &Blocker{Code: "management_secret_storage_unavailable"}
	}
	result.Connection = &ProtectedConnection{BaseURL: observation.BaseURL, ManagementKeyCiphertext: ciphertext}
	result.Checkpoint.Stage = "committed"
	// A config read, keys present/absent, or a running process cannot establish
	// effective client authentication and Gateway configuration. It stays shut.
	return result, nil
}
