package store

import (
	"context"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
)

func (s *Store) LoadUpdateCheck(ctx context.Context) ([]byte, error) {
	return setting.LoadUpdateCheck(ctx, s.db)
}
func (s *Store) SaveUpdateCheck(ctx context.Context, data []byte) error {
	return setting.SaveUpdateCheck(ctx, s.db, data)
}
func (s *Store) ClaimUpdateNotification(ctx context.Context, tag string) (bool, error) {
	return setting.ClaimUpdateNotification(ctx, s.db, tag)
}
func (s *Store) DismissUpdateNotification(ctx context.Context, tag string) error {
	return setting.DismissUpdateNotification(ctx, s.db, tag)
}
