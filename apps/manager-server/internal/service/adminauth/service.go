package adminauth

import (
	"context"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type Service struct {
	cfg                  config.Config
	store                *store.Store
	managerConfigService *managerconfig.Service
}

func New(cfg config.Config, store *store.Store, managerConfigService ...*managerconfig.Service) *Service {
	var mcs *managerconfig.Service
	if len(managerConfigService) > 0 {
		mcs = managerConfigService[0]
	}
	return &Service{cfg: cfg, store: store, managerConfigService: mcs}
}

func (s *Service) VerifyHeader(ctx context.Context, authorizationHeader string) (bool, error) {
	token := security.ExtractBearerToken(authorizationHeader)
	if token == "" {
		return false, nil
	}

	credential, ok, err := s.store.LoadAdminCredential(ctx)
	if err != nil {
		return false, err
	}
	if ok && security.VerifyAdminKey(credential, token) {
		return true, nil
	}

	if s.managerConfigService != nil {
		if setup, setupOK, err := s.managerConfigService.ResolveSetup(ctx); err == nil && setupOK {
			if strings.TrimSpace(setup.ManagementKey) != "" && token == setup.ManagementKey {
				return true, nil
			}
		}
	}

	return false, nil
}

func (s *Service) VerifyPanelHeader(ctx context.Context, authorizationHeader string) (bool, error) {
	return s.VerifyHeader(ctx, authorizationHeader)
}

func (s *Service) VerifySubmittedExternalConfigHeader(ctx context.Context, authorizationHeader string, cfg store.ManagerConfig) (bool, error) {
	return s.VerifyHeader(ctx, authorizationHeader)
}

func (s *Service) PanelUsesExternalManagementKey(ctx context.Context) (bool, error) {
	return false, nil
}
