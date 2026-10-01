package providerkeyalias

import (
	"context"
	"errors"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type SaveRequest struct {
	Provider   string `json:"provider"`
	APIKeyHash string `json:"apiKeyHash"`
	Alias      string `json:"alias"`
}

type Service struct {
	store *store.Store
}

func New(store *store.Store) *Service {
	return &Service{store: store}
}

func (s *Service) List(ctx context.Context) ([]model.ProviderKeyAlias, error) {
	return s.store.LoadProviderKeyAliases(ctx)
}

func (s *Service) Save(ctx context.Context, input SaveRequest) ([]model.ProviderKeyAlias, error) {
	if strings.TrimSpace(input.Alias) == "" {
		return nil, errors.New("alias is required")
	}
	if err := s.store.UpsertProviderKeyAlias(ctx, model.ProviderKeyAlias{
		Provider:   input.Provider,
		APIKeyHash: input.APIKeyHash,
		Alias:      input.Alias,
	}); err != nil {
		return nil, err
	}
	return s.List(ctx)
}

func (s *Service) Delete(ctx context.Context, provider, apiKeyHash string) error {
	return s.store.DeleteProviderKeyAlias(ctx, provider, apiKeyHash)
}
