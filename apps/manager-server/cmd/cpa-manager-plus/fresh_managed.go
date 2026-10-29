package main

import (
	"context"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
)

// managedInstallSource is the only default-install authority. It re-reads the
// committed Fresh checkpoint and the protected Manager connection on every call
// and never caches the management credential. Any mismatch between the
// checkpoint, the destination identity and the configured Manager connection
// fails closed instead of rebinding to another CPA.
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
		if checkpoint.Strategy != bootstrap.NewManaged || checkpoint.Stage != "committed" ||
			!strings.HasPrefix(checkpoint.DestinationConnectionIdentity, "managed:sha256:") ||
			record.Connection == nil {
			return freshkeyapply.ManagedInstall{}, freshkeyapply.ErrUnavailable
		}
		// The Manager connection is the CPA the destination was staged for. A
		// changed address is a different installation, never a silent rebind.
		if cpa.NormalizeBaseURL(record.Connection.CPABaseURL) != baseURL ||
			checkpoint.DestinationConnectionIdentity != freshstrategy.ManagedDestinationIdentity(baseURL) {
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
