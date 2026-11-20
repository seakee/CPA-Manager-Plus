package apikeyalias

import (
	"context"
	"errors"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type SaveRequest struct {
	Items                   []store.APIKeyAlias `json:"items"`
	ActiveAPIKeyHashes      []string            `json:"activeApiKeyHashes,omitempty"`
	AllowOrphanAliasCleanup bool                `json:"allowOrphanAliasCleanup,omitempty"`
}

type Service struct {
	store *store.Store
}

func New(store *store.Store) *Service {
	return &Service{store: store}
}

func (s *Service) List(ctx context.Context) ([]store.APIKeyAlias, error) {
	aliases, err := s.store.LoadAPIKeyAliases(ctx)
	if err != nil {
		return nil, err
	}
	if aliases == nil {
		return []store.APIKeyAlias{}, nil
	}
	return aliases, nil
}

func (s *Service) Save(ctx context.Context, items []store.APIKeyAlias, activeHashes []string, allowOrphanCleanup bool) ([]store.APIKeyAlias, error) {
	if items == nil {
		return nil, errors.New("api key aliases are required")
	}
	if err := s.store.UpsertAPIKeyAliasesWithActiveHashes(ctx, items, activeHashes, allowOrphanCleanup); err != nil {
		return nil, err
	}
	return s.store.LoadAPIKeyAliases(ctx)
}

func (s *Service) Delete(ctx context.Context, apiKeyHash string) error {
	return s.store.DeleteAPIKeyAlias(ctx, apiKeyHash)
}
