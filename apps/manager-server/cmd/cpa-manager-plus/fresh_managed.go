package main

import (
	"context"
	"log"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

// managedInstallSource is the only default-install authority. It re-reads the
// committed Fresh checkpoint and the protected Manager connection on every call
// and never caches the management credential. Any mismatch between the
// checkpoint, the destination identity and the configured Manager connection
// fails closed instead of rebinding to another CPA.
//
// It answers for both Manager-owned strategies, and discriminates by strategy so
// the two can never be confused by their identity value:
//
//   - New Managed is bound by `managed:sha256:` of the normalized Manager
//     connection, the identity the staging step derived.
//   - Adopt is bound by `external:sha256:` of the same normalized address, the
//     identity the adoption recorded for the destination it staged, and carries
//     the install-derived management key the Supervisor wrote into it.
func managedInstallSource(store freshstore.Store, protector *security.Protector, endpoint string) freshkeyapply.ManagedSource {
	baseURL, endpointErr := cpa.NormalizeV8BaseURL(endpoint)
	return func(ctx context.Context) (freshkeyapply.ManagedInstall, error) {
		if store == nil || protector == nil || endpointErr != nil {
			return freshkeyapply.ManagedInstall{}, freshkeyapply.ErrUnavailable
		}
		record, err := store.Load(ctx)
		if err != nil {
			return freshkeyapply.ManagedInstall{}, freshkeyapply.ErrUnavailable
		}
		checkpoint := record.Operation.Strategy
		if !freshkeyapply.ManagerOwnedStrategy(checkpoint) || checkpoint.Stage != "committed" || record.Connection == nil {
			return freshkeyapply.ManagedInstall{}, freshkeyapply.ErrUnavailable
		}
		// The Manager connection is the CPA the destination was staged for. A
		// changed address is a different installation, never a silent rebind.
		if cpa.NormalizeBaseURL(record.Connection.CPABaseURL) != baseURL {
			return freshkeyapply.ManagedInstall{}, freshkeyapply.ErrUnavailable
		}
		var identity string
		switch checkpoint.Strategy {
		case bootstrap.NewManaged:
			identity = freshstrategy.ManagedDestinationIdentity(baseURL)
		case bootstrap.AdoptExisting:
			identity = freshstrategy.AdoptedDestinationIdentity(baseURL)
		}
		if checkpoint.DestinationConnectionIdentity != identity {
			return freshkeyapply.ManagedInstall{}, freshkeyapply.ErrUnavailable
		}
		key, err := protector.UnprotectString(record.Connection.ManagementKey)
		if err != nil || len(key) != 64 {
			return freshkeyapply.ManagedInstall{}, freshkeyapply.ErrUnavailable
		}
		return freshkeyapply.ManagedInstall{
			RunID:         freshkeyapply.ManagedRunID(string(record.Operation.Identity.OperationID)),
			Generation:    strconv.FormatUint(checkpoint.TargetGeneration, 10),
			Identity:      checkpoint.TargetIdentity,
			ArtifactID:    checkpoint.TargetArtifactID,
			BaseURL:       baseURL,
			ManagementKey: key,
			// The strategy is what selects the destination posture; the plugin
			// switch is the operator's own and is not part of the proof.
			Strategy: string(checkpoint.Strategy),
		}, nil
	}
}

// externalInstallSource is the only authority of a committed Connect External
// installation. It re-reads the checkpoint and the protected Manager connection
// on every call and decrypts the credential the user supplied. Nothing here is
// cached, and nothing here contacts the user's CPA: an outage there must not hide
// the target, so every refusal is the typed blocker of the read that met it.
func externalInstallSource(store freshstore.Store, protector *security.Protector) freshkeyapply.ExternalSource {
	return func(ctx context.Context) (freshkeyapply.ExternalInstall, error) {
		if store == nil || protector == nil {
			return freshkeyapply.ExternalInstall{}, freshkeyapply.ErrUnavailable
		}
		record, err := store.Load(ctx)
		if err != nil {
			return freshkeyapply.ExternalInstall{}, freshkeyapply.ErrUnavailable
		}
		checkpoint := record.Operation.Strategy
		// External is the one profile whose completion this Manager cannot infer:
		// no Runtime is staged for it, so the durable product checkpoint is the
		// only proof the installation finished.
		if checkpoint.Strategy != bootstrap.ConnectExternal || checkpoint.Stage != "committed" ||
			!record.Operation.ProductComplete || checkpoint.TargetGeneration == 0 || record.Connection == nil {
			return freshkeyapply.ExternalInstall{}, freshkeyapply.ErrUnavailable
		}
		baseURL, err := cpa.NormalizeV8BaseURL(record.Connection.CPABaseURL)
		if err != nil || baseURL != strings.TrimRight(record.Connection.CPABaseURL, "/") {
			return freshkeyapply.ExternalInstall{}, freshkeyapply.ErrUnavailable
		}
		// The committed target must still be the connection this Manager holds.
		// Comparing the address digests is the only comparison available: the
		// credential is a protected envelope, and a connection the user edited to
		// another CPA is a different installation. It is reported as the typed
		// blocker it is instead of claiming the installation is unavailable, and
		// `POST external/rebind` refuses it too — a rebind replaces a credential
		// and never an address — so the way back is a reconnect.
		if checkpoint.TargetIdentity != freshkeyapply.ExternalIdentity(baseURL) {
			return freshkeyapply.ExternalInstall{}, cpa.ErrExternalTargetMoved
		}
		key, err := protector.UnprotectString(record.Connection.ManagementKey)
		if err != nil || !cpa.ValidV8ManagementKey(key) {
			return freshkeyapply.ExternalInstall{}, freshkeyapply.ErrUnavailable
		}
		return freshkeyapply.ExternalInstall{
			Identity:      freshkeyapply.ExternalIdentity(baseURL),
			Checkpoint:    checkpoint.TargetGeneration,
			BaseURL:       baseURL,
			ManagementKey: key,
		}, nil
	}
}

// installInitialClientKey persists the default installation's Client Key before
// its destination exists, so the destination is never staged for a CPA that
// would start with client authentication disabled. The strategy and the key
// authority replay the same canonical Key and the same initial-install journal
// on every resume.
func (a *freshApplication) installInitialClientKey(ctx context.Context, op bootstrap.FreshOperation, generation uint64) (freshstrategy.InitialClientKey, error) {
	if a == nil || a.keyApply == nil {
		return freshstrategy.InitialClientKey{}, freshkeyapply.ErrUnavailable
	}
	key, err := a.keyApply.EnsureInitialInstall(ctx, freshkeyapply.ManagedRunID(string(op.Identity.OperationID)), generation)
	if err != nil {
		return freshstrategy.InitialClientKey{}, err
	}
	return freshstrategy.InitialClientKey{KeyID: string(key.ID), Secret: key.Secret}, nil
}

// prepareCompletedStartup runs legacy destination migration before HTTP routes
// or the Runtime reconciler start, reloads the operation so a newly migrated
// checkpoint is observed, and seeds the initial Running desired state.
func (a *freshApplication) prepareCompletedStartup(ctx context.Context, op bootstrap.FreshOperation) bootstrap.FreshOperation {
	if a == nil || !op.ProductComplete {
		return op
	}
	if err := a.migrateManagedDestination(ctx); err != nil {
		// Migration is durable and idempotent; keep the completed Fresh
		// server available while a typed blocker waits for the next startup.
		log.Printf("Fresh managed destination migration unavailable: %v", err)
	} else if a.store != nil {
		if reloaded, loadErr := a.store.Load(ctx); loadErr == nil {
			op = reloaded.Operation
		} else {
			log.Printf("Fresh post-migration operation load unavailable: %v", loadErr)
		}
	}
	if _, err := a.ensureManagedRuntimeDesiredRunning(ctx, op); err != nil {
		log.Printf("Fresh managed runtime desired state unavailable: %v", err)
	}
	return op
}

// ensureManagedRuntimeDesiredRunning atomically seeds the durable Running
// desired state once a Manager-owned destination checkpoint (New Managed or
// Adopt) is committed and complete, preserving any existing runtime_desired_v1
// row and refusing unmigrated or operator-managed checkpoints.
func (a *freshApplication) ensureManagedRuntimeDesiredRunning(ctx context.Context, op bootstrap.FreshOperation) (bool, error) {
	if a == nil || a.runtime == nil || a.runtimeSettings == nil || !op.ProductComplete || !freshkeyapply.ValidManagerOwnedDestinationCheckpoint(op.Strategy) {
		return false, nil
	}
	return a.runtimeSettings.SeedEmbeddedRuntimeDesiredLifecycleIfAbsent(ctx, model.EmbeddedRuntimeDesiredRunning)
}

type freshDesiredStateStore struct {
	app *freshApplication
}

// freshDesiredStateStore answers both Runtime reconcile questions the Manager
// owns: what lifecycle the installation wants, and which engine artifact it
// committed to.
var (
	_ runtimeservice.DesiredStateStore      = freshDesiredStateStore{}
	_ runtimeservice.CommittedArtifactStore = freshDesiredStateStore{}
)

func (a *freshApplication) desiredStateStore() runtimeservice.DesiredStateStore {
	if a == nil || a.runtimeSettings == nil {
		return nil
	}
	return freshDesiredStateStore{app: a}
}

// LoadEmbeddedRuntimeDesiredState returns the persisted desired state once
// seeded at completion, or derives an in-memory Running obligation for an
// incomplete Manager-owned installation whose destination is already committed
// and whose pinned CPA engine is ready (#1166). While ProductComplete is false,
// runtime_desired_v1 must remain absent from settings.
func (s freshDesiredStateStore) LoadEmbeddedRuntimeDesiredState(ctx context.Context) (model.EmbeddedRuntimeDesiredState, bool, error) {
	if s.app == nil || s.app.runtimeSettings == nil {
		return model.EmbeddedRuntimeDesiredState{}, false, nil
	}
	desired, found, err := s.app.runtimeSettings.LoadEmbeddedRuntimeDesiredState(ctx)
	if err != nil || found {
		return desired, found, err
	}
	return s.app.incompleteManagedDesiredRunning(ctx)
}

// LoadEmbeddedRuntimeCommittedArtifact reports the engine artifact the committed
// Manager-owned installation pinned. It reads the Fresh checkpoint — the
// Manager's own committed target — and never a Runtime observation, because the
// observation is exactly what the reconcile Start has to fence against: an
// automatic restart that took its expectation from the Supervisor's current
// selection could not detect a selection change at all.
//
// A checkpoint that is not a verified Manager-owned destination, or that carries
// no canonical artifact, has nothing to commit, so the answer is "not found" and
// the Supervisor Start stays unfenced. An unreadable checkpoint is an error: the
// reconcile refuses to start rather than starting an engine it cannot name.
func (s freshDesiredStateStore) LoadEmbeddedRuntimeCommittedArtifact(ctx context.Context) (model.RuntimeArtifactID, bool, error) {
	if s.app == nil || s.app.store == nil {
		return "", false, nil
	}
	record, err := s.app.store.Load(ctx)
	if err != nil {
		return "", false, err
	}
	checkpoint := record.Operation.Strategy
	if !freshkeyapply.ValidManagerOwnedDestinationCheckpoint(checkpoint) {
		return "", false, nil
	}
	artifactID := model.RuntimeArtifactID(checkpoint.TargetArtifactID)
	if !artifactID.IsValid() {
		return "", false, nil
	}
	return artifactID, true, nil
}

func (a *freshApplication) incompleteManagedDesiredRunning(ctx context.Context) (model.EmbeddedRuntimeDesiredState, bool, error) {
	var zero model.EmbeddedRuntimeDesiredState
	if a == nil || a.store == nil || a.runtime == nil || a.runtimeExternal != nil || a.managedSource == nil {
		return zero, false, nil
	}
	record, err := a.store.Load(ctx)
	if err != nil {
		return zero, false, err
	}
	op := record.Operation
	if op.ProductComplete || !freshkeyapply.ValidManagerOwnedDestinationCheckpoint(op.Strategy) {
		return zero, false, nil
	}
	if _, err := a.managedSource(ctx); err != nil {
		return zero, false, nil
	}
	if a.artifact != nil {
		artifactStatus, artifactErr := a.artifact.Status(ctx)
		if artifactErr != nil {
			return zero, false, artifactErr
		}
		if !artifactStatus.Ready() || artifactStatus.Active.ArtifactID != op.Strategy.TargetArtifactID {
			return zero, false, nil
		}
	}
	status, err := a.observeInstallationRuntime(ctx)
	if err != nil {
		return zero, false, err
	}
	if status.ProtocolVersion != "v1" || status.ActiveGatewayArtifact == nil ||
		string(status.Identity) != op.Strategy.TargetIdentity ||
		string(status.ActiveGatewayArtifact.ArtifactID) != op.Strategy.TargetArtifactID {
		return zero, false, nil
	}
	return model.EmbeddedRuntimeDesiredState{
		DesiredLifecycle: model.EmbeddedRuntimeDesiredRunning,
		Revision:         1,
		UpdatedAtMS:      1,
	}, true, nil
}
