// Package freshstrategy implements the bounded strategy adapter of the current
// Fresh operation. Persistence and browser authorization remain with its caller.
package freshstrategy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
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

// maxRuntimeOperationIDBytes is the Runtime Protocol v1 operation ID limit. Both
// Supervisor and Manager validate it, so an attempt suffix must fit inside it.
const maxRuntimeOperationIDBytes = 128

// destinationOperationID derives the Supervisor operation ID of one durable
// destination attempt. Attempt zero keeps the historical ID, so a checkpoint
// written before attempts existed stays exactly idempotent. Every later attempt
// is a distinct Supervisor operation for the very same destination: the journal
// never resumes a terminal operation, and replaying one can only replay its
// failure.
func destinationOperationID(base string, attempt uint64) (string, error) {
	operationID := base
	if attempt != 0 {
		operationID = base + ":attempt-" + strconv.FormatUint(attempt, 10)
	}
	if len([]byte(operationID)) > maxRuntimeOperationIDBytes {
		return "", errors.New("destination operation ID exceeds the Runtime limit")
	}
	return operationID, nil
}

// DestinationRetry reports a destination stage or start the Supervisor recorded
// as terminally failed. The Supervisor will only ever replay that terminal
// evidence, so the attempt must be retired before the caller reports the
// blocker. Blocker carries the response code; Checkpoint is the checkpoint the
// caller must commit in the same Fresh CAS as this observation, and its
// advanced DestinationAttempt is the only durable transition it authorizes.
type DestinationRetry struct {
	Blocker    *Blocker
	Checkpoint bootstrap.FreshStrategyCheckpoint
}

func (e *DestinationRetry) Error() string { return e.Blocker.Error() }

// Unwrap keeps the typed response code visible to the existing blocker mapping.
func (e *DestinationRetry) Unwrap() error { return e.Blocker }

// destinationRetry retires the attempt whose operation terminally failed. The
// checkpoint keeps its target, stage and destination identity exactly as
// persisted; only the attempt index advances, so the next Execute re-issues the
// identical destination under the next operation ID instead of replaying a known
// failure forever.
func destinationRetry(checkpoint bootstrap.FreshStrategyCheckpoint, code string) error {
	checkpoint.DestinationAttempt++
	return &DestinationRetry{Blocker: &Blocker{Code: code}, Checkpoint: checkpoint}
}

// Qualification is exclusively server-owned accepted evidence, never a browser
// assertion. In particular, a successful config read cannot set these facts.
type Options struct {
	Runtime             runtimeservice.RuntimeClient
	Protector           *security.Protector
	Qualification       bootstrap.CPAQualification
	QualifiedArtifactID model.RuntimeArtifactID
	AdoptionResources   []bootstrap.AdoptionResourceDecision
	DestinationBaseURL  string
	// InitialClientKey replays or mints the single Client Key the installation
	// persists before its destination exists. CPA v8 disables client
	// authentication when the key list is empty, so the default install must
	// always stage exactly this key.
	InitialClientKey InitialClientKeySource
}

// InitialClientKey is the canonical Client Key of the default installation.
// The secret never leaves the install path: it is sealed into the protected
// destination snapshot, and the canonical record remains the only durable copy.
type InitialClientKey struct {
	KeyID  string
	Secret string
}

// InitialClientKeySource is idempotent for one Fresh operation and runtime
// generation: a resume, retry or restart replays the same canonical Key and
// never mints a second credential.
type InitialClientKeySource func(context.Context, bootstrap.FreshOperation, uint64) (InitialClientKey, error)

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
	// PluginsEnabled is the wizard's explicit plugin opt-in. It is accepted for the
	// strategies whose destination this Manager stages — New Managed and Adopt —
	// and is persisted with the checkpoint, so a retry or a Supervisor restart
	// reads the decision instead of the browser.
	PluginsEnabled bool `json:"pluginsEnabled,omitempty"`
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
	// InitialClientKey is the install-generated plaintext, returned only by the
	// call that commits the destination so the completion screen can show it
	// once. It is never journaled, logged or stored in the browser.
	InitialClientKey string `json:"-"`
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
	case bootstrap.AdoptExisting:
		// An adopted destination is staged from the Supervisor's own template, so
		// its plugin root is a resource this installation owns exactly as a New
		// Managed one does: the opt-in is the operator's decision for the new CPA,
		// not a claim on the source. The source's own plugin posture is untouched.
		return scoped.existing(ctx, op, request)
	case bootstrap.ConnectExternal:
		// An external CPA is the user's own process with its own plugin root.
		// Accepting the flag would silently claim a resource this installation does
		// not own.
		if request.PluginsEnabled {
			return Result{}, &Blocker{Code: "fresh_strategy_plugin_scope_conflict"}
		}
		return scoped.existing(ctx, op, request)
	default:
		return Result{}, &Blocker{Code: "fresh_strategy_invalid"}
	}
}

type managedDestinationSnapshot struct {
	Destination model.RuntimeDestinationRequest `json:"destination"`
	BaseURL     string                          `json:"baseUrl"`
}

// ManagedDestinationIdentity hashes the exact destination value an identity
// binds: the normalized Manager connection for a checkpoint, or the Fresh
// operation for a destination source.
func ManagedDestinationIdentity(baseURL string) string {
	digest := sha256.Sum256([]byte(baseURL))
	return "managed:sha256:" + hex.EncodeToString(digest[:])
}

// managedDestinationSnapshot creates the protected, durable destination intent
// without touching Runtime. The caller must CAS the returned ciphertext before
// invoking Execute; operationIDSuffix is stable across retries and therefore
// also names the Supervisor journal entry.
func (s *Service) managedDestinationSnapshot(ctx context.Context, op bootstrap.FreshOperation, status model.RuntimeObservedStatus, stage string, bindIdentity bool, operationIDSuffix string, pluginsEnabled bool) (bootstrap.FreshStrategyCheckpoint, string, error) {
	if s.options.Protector == nil || s.options.DestinationBaseURL == "" {
		return bootstrap.FreshStrategyCheckpoint{}, "", &Blocker{Code: "managed_destination_connection_unavailable"}
	}
	// The canonical Client Key is persisted before the destination is described,
	// let alone staged: CPA must never start with client authentication off.
	if s.options.InitialClientKey == nil {
		return bootstrap.FreshStrategyCheckpoint{}, "", &Blocker{Code: "managed_initial_client_key_unavailable"}
	}
	initial, err := s.options.InitialClientKey(ctx, op, uint64(status.Generation))
	if _, keyErr := identity.ParseAPIKeyID(initial.KeyID); err != nil || keyErr != nil || !model.ValidInitialClientKey(initial.Secret) {
		return bootstrap.FreshStrategyCheckpoint{}, "", &Blocker{Code: "managed_initial_client_key_unavailable"}
	}
	baseURL, err := cpa.NormalizeV8BaseURL(s.options.DestinationBaseURL)
	if err != nil {
		return bootstrap.FreshStrategyCheckpoint{}, "", &Blocker{Code: "managed_destination_connection_invalid"}
	}
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return bootstrap.FreshStrategyCheckpoint{}, "", &Blocker{Code: "management_secret_storage_unavailable"}
	}
	managementKey := hex.EncodeToString(keyBytes)
	// The destination carries both the install's first Client Key - CPA must never
	// start with client authentication off - and the plugin opt-in the wizard
	// recorded.
	destination := model.RuntimeDestinationRequest{OperationID: string(op.Identity.OperationID) + ":" + operationIDSuffix, ExpectedRuntimeIdentity: status.Identity, ExpectedRuntimeGeneration: status.Generation, ExpectedActiveArtifactID: status.ActiveGatewayArtifact.ArtifactID, SourceIdentity: ManagedDestinationIdentity(string(op.Identity.OperationID)), SourceClass: "new_managed", APIKeys: []string{initial.Secret}, ManagementKey: managementKey, PluginsEnabled: pluginsEnabled}
	data, err := json.Marshal(managedDestinationSnapshot{Destination: destination, BaseURL: baseURL})
	if err != nil {
		return bootstrap.FreshStrategyCheckpoint{}, "", &Blocker{Code: "management_secret_storage_unavailable"}
	}
	ciphertext, err := s.options.Protector.ProtectString(string(data))
	if err != nil {
		return bootstrap.FreshStrategyCheckpoint{}, "", &Blocker{Code: "management_secret_storage_unavailable"}
	}
	checkpoint := bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, TargetIdentity: string(status.Identity), TargetGeneration: uint64(status.Generation), TargetArtifactID: string(status.ActiveGatewayArtifact.ArtifactID), Stage: stage, PluginsEnabled: pluginsEnabled}
	if bindIdentity {
		checkpoint.DestinationConnectionIdentity = ManagedDestinationIdentity(baseURL)
	}
	return checkpoint, ciphertext, nil
}

// PrepareManagedMigration creates the first durable step for a legacy
// start-only New Managed install. It refuses to generate or stage a credential
// while CPA is running: the Supervisor's destination auth directory must be
// empty, and an already-running process would make that ownership ambiguous.
func (s *Service) PrepareManagedMigration(ctx context.Context, op bootstrap.FreshOperation, request Request) (Result, error) {
	result := Result{RuntimeMode: model.RuntimeModeEmbedded, GatewayPending: true}
	if request.Strategy != bootstrap.NewManaged || op.Strategy.Strategy != bootstrap.NewManaged || op.Strategy.Stage != "committed" || op.Strategy.DestinationConnectionIdentity != "" {
		return result, &Blocker{Code: "managed_migration_shape_conflict"}
	}
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
	if request.ExpectedRuntimeIdentity != status.Identity || request.ExpectedRuntimeGeneration != status.Generation || request.ExpectedArtifactID != status.ActiveGatewayArtifact.ArtifactID || op.Strategy.TargetIdentity != string(status.Identity) || op.Strategy.TargetGeneration != uint64(status.Generation) || op.Strategy.TargetArtifactID != string(status.ActiveGatewayArtifact.ArtifactID) {
		return result, &Blocker{Code: "managed_target_identity_mismatch"}
	}
	if status.State != model.RuntimeStateOffline {
		return result, &Blocker{Code: "managed_migration_requires_stopped"}
	}
	if !status.Capabilities.Supports(model.RuntimeCapabilityStageDestination) || !status.Capabilities.Supports(model.RuntimeCapabilityStart) {
		return result, &Blocker{Code: "managed_target_staging_required"}
	}
	checkpoint, ciphertext, err := s.managedDestinationSnapshot(ctx, op, status, "committed", false, "managed-migration-destination", op.Strategy.PluginsEnabled)
	if err != nil {
		return result, err
	}
	result.Checkpoint = checkpoint
	result.AdoptionCiphertext = ciphertext
	return result, nil
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
	if request.ExpectedRuntimeIdentity != status.Identity || request.ExpectedRuntimeGeneration != status.Generation || request.ExpectedArtifactID != status.ActiveGatewayArtifact.ArtifactID {
		return result, &Blocker{Code: "managed_target_identity_mismatch"}
	}
	checkpoint := op.Strategy
	if checkpoint.Strategy == "" {
		var snapshotCiphertext string
		checkpoint, snapshotCiphertext, err = s.managedDestinationSnapshot(ctx, op, status, "staged", true, "managed-destination", request.PluginsEnabled)
		if err != nil {
			return result, err
		}
		result.AdoptionCiphertext = snapshotCiphertext
		result.Checkpoint = checkpoint
		return result, nil
	}
	result.Checkpoint = checkpoint
	if checkpoint.TargetIdentity != string(status.Identity) || checkpoint.TargetGeneration != uint64(status.Generation) || checkpoint.TargetArtifactID != string(status.ActiveGatewayArtifact.ArtifactID) {
		return result, &Blocker{Code: "managed_target_identity_mismatch"}
	}
	// The opt-in was persisted with the checkpoint and already reached the
	// Supervisor template. A later attempt presenting another value would claim a
	// decision this operation never recorded, so it is refused, not adopted.
	if checkpoint.PluginsEnabled != request.PluginsEnabled {
		return result, &Blocker{Code: "managed_plugin_optin_conflict"}
	}
	if checkpoint.Stage == "committed" && !(checkpoint.DestinationConnectionIdentity == "" && request.AdoptionCiphertext != "") {
		return result, nil
	}
	if !security.IsValidProtectedEnvelope(request.AdoptionCiphertext) || s.options.Protector == nil {
		return result, &Blocker{Code: "managed_destination_snapshot_unavailable"}
	}
	plain, err := s.options.Protector.UnprotectString(request.AdoptionCiphertext)
	if err != nil {
		return result, &Blocker{Code: "managed_destination_snapshot_unavailable"}
	}
	var snapshot managedDestinationSnapshot
	if json.Unmarshal([]byte(plain), &snapshot) != nil || snapshot.Destination.Validate() != nil || snapshot.Destination.ExpectedRuntimeIdentity != status.Identity || snapshot.Destination.ExpectedRuntimeGeneration != status.Generation || snapshot.Destination.SourceIdentity != ManagedDestinationIdentity(string(op.Identity.OperationID)) || snapshot.Destination.SourceClass != "new_managed" {
		return result, &Blocker{Code: "managed_destination_snapshot_unavailable"}
	}
	if checkpoint.DestinationConnectionIdentity != "" && checkpoint.DestinationConnectionIdentity != ManagedDestinationIdentity(snapshot.BaseURL) {
		return result, &Blocker{Code: "managed_destination_snapshot_unavailable"}
	}
	checkpoint.DestinationConnectionIdentity = ManagedDestinationIdentity(snapshot.BaseURL)
	result.Checkpoint = checkpoint
	runtime, ok := s.options.Runtime.(runtimeservice.DestinationClient)
	if !ok || !status.Capabilities.Supports(model.RuntimeCapabilityStageDestination) || !status.Capabilities.Supports(model.RuntimeCapabilityStart) {
		return result, &Blocker{Code: "managed_target_staging_required"}
	}
	// The staged snapshot is immutable for the lifetime of the Fresh operation,
	// so the attempt index is the only thing that may distinguish one Supervisor
	// operation from the next.
	stageOperationID, err := destinationOperationID(snapshot.Destination.OperationID, checkpoint.DestinationAttempt)
	if err != nil {
		return result, &Blocker{Code: "managed_destination_connection_invalid"}
	}
	destination := snapshot.Destination
	destination.OperationID = stageOperationID
	staged, err := runtime.StageDestination(ctx, destination)
	if err != nil || staged.Validate() != nil {
		if code, ok := runtimeservice.ErrorCode(err); ok && string(code) == "gateway_auth_present" {
			return result, &Blocker{Code: "managed_gateway_auth_present"}
		}
		return result, &Blocker{Code: "managed_destination_stage_unresolved"}
	}
	switch staged.State {
	case model.RuntimeOperationSucceeded:
	case model.RuntimeOperationFailed:
		// A terminal failure is never resumed by the Supervisor, so the attempt
		// is retired here and re-issued under the next operation ID.
		return result, destinationRetry(op.Strategy, "managed_destination_stage_failed")
	default:
		// Accepted or running work stays unresolved under the same attempt: the
		// retry replays exactly that operation instead of racing a second one.
		return result, &Blocker{Code: "managed_destination_stage_unresolved"}
	}
	started, err := s.options.Runtime.Start(ctx, model.RuntimeMutationRequest{OperationID: stageOperationID + ":start", ExpectedRuntimeIdentity: status.Identity, ExpectedRuntimeGeneration: status.Generation, ExpectedActiveArtifactID: snapshot.Destination.ExpectedActiveArtifactID})
	if err != nil || started.Validate() != nil {
		return result, &Blocker{Code: "managed_target_start_unresolved"}
	}
	if started.State == model.RuntimeOperationAccepted || started.State == model.RuntimeOperationRunning {
		result.Checkpoint.Stage = "starting"
		return result, nil
	}
	if started.State == model.RuntimeOperationFailed {
		return result, destinationRetry(op.Strategy, "managed_target_start_failed")
	}
	if started.State != model.RuntimeOperationSucceeded {
		return result, &Blocker{Code: "managed_target_start_unresolved"}
	}
	if _, err := cpa.ReadV8Config(ctx, snapshot.BaseURL, snapshot.Destination.ManagementKey); err != nil {
		return result, &Blocker{Code: "managed_destination_verification_required"}
	}
	ciphertext, err := s.options.Protector.ProtectString(snapshot.Destination.ManagementKey)
	if err != nil {
		return result, &Blocker{Code: "management_secret_storage_unavailable"}
	}
	result.Connection = &ProtectedConnection{BaseURL: snapshot.BaseURL, ManagementKeyCiphertext: ciphertext}
	result.Checkpoint.Stage = "committed"
	// Delivered exactly once: this call commits the destination, and the
	// completed replay returns the checkpoint without the plaintext.
	result.InitialClientKey = snapshot.Destination.APIKeys[0]
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
