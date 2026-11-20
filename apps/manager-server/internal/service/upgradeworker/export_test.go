package upgradeworker

import (
	"context"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

var (
	CanonicalExternalBaseURL  = canonicalExternalBaseURL
	SameExternalBaseURL       = sameExternalBaseURL
	IsTransientReconcileError = isTransientReconcileError
)

func SetReadReconcilePostRunSnapshotForTest(fn func(context.Context, string, string) (model.UpgradeOperation, bool, error)) func() {
	prev := readReconcilePostRunSnapshot
	readReconcilePostRunSnapshot = fn
	return func() {
		readReconcilePostRunSnapshot = prev
	}
}

func (s *Service) ExternalConnectionForTest(ctx context.Context, request Request) (string, string, bool, bool, error) {
	return s.externalConnection(ctx, request)
}

func (s *Service) SyncExternalManagerConnectionForTest(ctx context.Context, rawBaseURL, rawManagementKey string) error {
	return s.syncExternalManagerConnection(ctx, rawBaseURL, rawManagementKey)
}
