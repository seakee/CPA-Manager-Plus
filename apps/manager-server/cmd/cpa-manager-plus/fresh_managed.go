package main

import (
	"context"
	"fmt"
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

// LoadEmbeddedRuntimeDesiredState is the reconcile gate, so it runs on every
// cycle and reads the committed Fresh checkpoint first. A Manager-owned
// installation is the only one whose Runtime this process may drive: Connect
// External points at the operator's CPA, an uncommitted operation has no
// installation yet, and an unmigrated or operator-managed checkpoint was never
// staged by this Manager. All three answer "no desired state", which makes the
// reconciler decide nothing at all — no Start and, just as importantly, no Stop.
//
// The checkpoint read is also the durable lock on the domain: an unreadable
// checkpoint returns its error, so this cycle mutates nothing rather than acting
// on `runtime_desired_v1` alone. That row can outlive a mode change, and it is
// not evidence that this process owns the Runtime it names.
func (s freshDesiredStateStore) LoadEmbeddedRuntimeDesiredState(ctx context.Context) (model.EmbeddedRuntimeDesiredState, bool, error) {
	var zero model.EmbeddedRuntimeDesiredState
	if s.app == nil || s.app.runtimeSettings == nil {
		return zero, false, nil
	}
	op, owned, err := s.app.managedCheckpoint(ctx)
	if err != nil {
		return zero, false, err
	}
	if !owned {
		return zero, false, nil
	}
	// The persisted row is the completed installation's desire. While
	// ProductComplete is false it must stay absent, and the obligation is derived
	// in memory instead.
	desired, found, err := s.app.runtimeSettings.LoadEmbeddedRuntimeDesiredState(ctx)
	if err != nil || found {
		return desired, found, err
	}
	return s.app.incompleteManagedDesiredRunning(ctx, op)
}

// managedCheckpoint reads the committed Fresh checkpoint and reports whether it
// names an installation this Manager staged, which is the only authority that may
// drive this process's Runtime. It reads on every call rather than caching a
// startup decision: an operation that was uncommitted when the process started
// can commit `connect_external` later, and the reconciler must stop driving the
// Runtime from that moment on.
//
// A checkpoint it cannot read is an error, never a silent "not owned": the
// distinguishing claim is that the Manager can name the installation whose
// Runtime it drives.
func (a *freshApplication) managedCheckpoint(ctx context.Context) (bootstrap.FreshOperation, bool, error) {
	if a == nil || a.store == nil {
		return bootstrap.FreshOperation{}, false, nil
	}
	record, err := a.store.Load(ctx)
	if err != nil {
		return bootstrap.FreshOperation{}, false, err
	}
	return record.Operation, freshkeyapply.ValidManagerOwnedDestinationCheckpoint(record.Operation.Strategy), nil
}

// LoadEmbeddedRuntimeCommittedArtifact reports the engine artifact a *running*
// Manager-owned installation is pinned to, and nothing else. It reads the Fresh
// checkpoint — the Manager's own committed target — and never a Runtime
// observation.
//
// Only an incomplete installation is pinned. `prepare_update`/`activate_update`
// replace the active engine in place and persist the new selection in the
// Supervisor, while the checkpoint deliberately keeps the install-time artifact
// as audit and qualification evidence; fencing a completed installation to it
// would refuse every restart after an update. A completed installation therefore
// answers "not found" and the reconciler fences the Supervisor's same-cycle
// observation instead, exactly as the Console Start does.
//
// An unreadable checkpoint, or a Manager-owned checkpoint whose target is not a
// canonical artifact identity, is an error: the reconciler refuses to start an
// engine it cannot name.
func (s freshDesiredStateStore) LoadEmbeddedRuntimeCommittedArtifact(ctx context.Context) (model.RuntimeArtifactID, bool, error) {
	if s.app == nil {
		return "", false, nil
	}
	op, owned, err := s.app.managedCheckpoint(ctx)
	if err != nil {
		return "", false, err
	}
	if !owned || op.ProductComplete {
		return "", false, nil
	}
	artifactID := model.RuntimeArtifactID(op.Strategy.TargetArtifactID)
	if !artifactID.IsValid() {
		return "", false, fmt.Errorf("Fresh managed checkpoint target artifact %q is not a canonical SHA-256 identity",
			op.Strategy.TargetArtifactID)
	}
	return artifactID, true, nil
}

// incompleteManagedDesiredRunning is the in-memory obligation of a Manager-owned
// installation that has not completed yet: while ProductComplete is false the
// durable row must stay absent, so the desire is derived from the pinned engine
// the Manager already started. op is the checkpoint the caller's gate read, so
// this decision and the ownership decision are made over the same bytes.
func (a *freshApplication) incompleteManagedDesiredRunning(ctx context.Context, op bootstrap.FreshOperation) (model.EmbeddedRuntimeDesiredState, bool, error) {
	var zero model.EmbeddedRuntimeDesiredState
	if a == nil || a.runtime == nil || a.managedSource == nil {
		return zero, false, nil
	}
	if op.ProductComplete {
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
