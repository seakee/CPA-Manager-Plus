package apikeyalias

import (
	"context"
	"errors"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/store"
)

type SaveRequest struct {
	Items []store.APIKeyAlias `json:"items"`
}

type Service struct {
	store *store.Store
}

func New(store *store.Store) *Service {
	return &Service{store: store}
}

func (s *Service) List(ctx context.Context) ([]store.APIKeyAlias, error) {
	return s.store.LoadAPIKeyAliases(ctx)
}

func (s *Service) Save(ctx context.Context, items []store.APIKeyAlias) ([]store.APIKeyAlias, error) {
	if items == nil {
		return nil, errors.New("api key aliases are required")
	}
	if err := s.store.UpsertAPIKeyAliases(ctx, items); err != nil {
		return nil, err
	}
	return s.store.LoadAPIKeyAliases(ctx)
}

func (s *Service) Delete(ctx context.Context, apiKeyHash string) error {
	return s.store.DeleteAPIKeyAlias(ctx, apiKeyHash)
}
