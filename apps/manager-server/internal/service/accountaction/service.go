package accountaction

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	managerconfigsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

var ErrCandidateNotFound = errors.New("account action candidate not found")
var ErrCandidateConflict = errors.New("account action candidate no longer matches current CPA auth file")
var ErrCandidateNotPending = errors.New("account action candidate is not pending")

type Service struct {
	store                *store.Store
	managerConfigService *managerconfigsvc.Service
	client               *http.Client
}

type ListResponse struct {
	Items        []model.AccountActionCandidate `json:"items"`
	PendingCount int64                          `json:"pendingCount"`
}

type authFile = cpaauthfiles.File

func New(st *store.Store, managerConfigService *managerconfigsvc.Service, clients ...*http.Client) *Service {
	client := &http.Client{Timeout: 30 * time.Second}
	if len(clients) > 0 && clients[0] != nil {
		client = clients[0]
	}
	return &Service{store: st, managerConfigService: managerConfigService, client: client}
}

func (s *Service) List(ctx context.Context, status string, limit int) (ListResponse, error) {
	items, err := s.store.ListAccountActionCandidates(ctx, strings.TrimSpace(status), limit)
	if err != nil {
		return ListResponse{}, err
	}
	pendingCount, err := s.store.CountAccountActionCandidates(ctx, model.AccountActionStatusPending)
	if err != nil {
		return ListResponse{}, err
	}
	return ListResponse{Items: items, PendingCount: pendingCount}, nil
}

func (s *Service) Ignore(ctx context.Context, id int64) (model.AccountActionCandidate, error) {
	return s.updatePendingStatus(ctx, id, model.AccountActionStatusIgnored)
}

func (s *Service) Resolve(ctx context.Context, id int64) (model.AccountActionCandidate, error) {
	return s.updatePendingStatus(ctx, id, model.AccountActionStatusResolved)
}

func (s *Service) Enable(ctx context.Context, id int64) (model.AccountActionCandidate, error) {
	item, setup, err := s.resolvePendingCandidateAndSetup(ctx, id)
	if err != nil {
		return model.AccountActionCandidate{}, err
	}
	if _, err := s.verifyCurrentAuthFile(ctx, setup, item); err != nil {
		return model.AccountActionCandidate{}, err
	}
	if err := s.patchAuthFile(ctx, setup, item.AuthFileName, item.AuthIndex, false); err != nil {
		_ = s.store.RecordAccountActionCandidateFailure(ctx, id, err.Error())
		return model.AccountActionCandidate{}, err
	}
	return s.updatePendingStatus(ctx, id, model.AccountActionStatusResolved)
}

func (s *Service) DeleteAuthFile(ctx context.Context, id int64) (model.AccountActionCandidate, error) {
	item, setup, err := s.resolvePendingCandidateAndSetup(ctx, id)
	if err != nil {
		return model.AccountActionCandidate{}, err
	}
	if _, err := s.verifyCurrentAuthFile(ctx, setup, item); err != nil {
		return model.AccountActionCandidate{}, err
	}
	if err := s.deleteAuthFile(ctx, setup, item.AuthFileName); err != nil {
		_ = s.store.RecordAccountActionCandidateFailure(ctx, id, err.Error())
		return model.AccountActionCandidate{}, err
	}
	return s.updatePendingStatus(ctx, id, model.AccountActionStatusDeleted)
}

func (s *Service) updatePendingStatus(ctx context.Context, id int64, status string) (model.AccountActionCandidate, error) {
	item, err := s.store.UpdatePendingAccountActionCandidateStatus(ctx, id, status)
	if errors.Is(err, sql.ErrNoRows) {
		if _, ok, getErr := s.store.GetAccountActionCandidate(ctx, id); getErr != nil {
			return model.AccountActionCandidate{}, getErr
		} else if ok {
			return model.AccountActionCandidate{}, ErrCandidateNotPending
		}
		return model.AccountActionCandidate{}, ErrCandidateNotFound
	}
	if err != nil {
		return model.AccountActionCandidate{}, err
	}
	return item, nil
}

func (s *Service) resolvePendingCandidateAndSetup(ctx context.Context, id int64) (model.AccountActionCandidate, store.Setup, error) {
	item, setup, err := s.resolveCandidateAndSetup(ctx, id)
	if err != nil {
		return model.AccountActionCandidate{}, store.Setup{}, err
	}
	if item.Status != model.AccountActionStatusPending {
		return model.AccountActionCandidate{}, store.Setup{}, ErrCandidateNotPending
	}
	return item, setup, nil
}

func (s *Service) resolveCandidateAndSetup(ctx context.Context, id int64) (model.AccountActionCandidate, store.Setup, error) {
	item, ok, err := s.store.GetAccountActionCandidate(ctx, id)
	if err != nil {
		return model.AccountActionCandidate{}, store.Setup{}, err
	}
	if !ok {
		return model.AccountActionCandidate{}, store.Setup{}, ErrCandidateNotFound
	}
	setup, ok, err := s.managerConfigService.ResolveSetup(ctx)
	if err != nil {
		return model.AccountActionCandidate{}, store.Setup{}, err
	}
	if !ok || strings.TrimSpace(setup.CPAUpstreamURL) == "" || strings.TrimSpace(setup.ManagementKey) == "" {
		return model.AccountActionCandidate{}, store.Setup{}, errors.New("usage service is not configured")
	}
	return item, setup, nil
}

func (s *Service) verifyCurrentAuthFile(ctx context.Context, setup store.Setup, item model.AccountActionCandidate) (authFile, error) {
	file, err := cpaauthfiles.New(s.client).Verify(ctx, setup.CPAUpstreamURL, setup.ManagementKey, cpaauthfiles.Identity{
		AuthFileName:      item.AuthFileName,
		AuthIndex:         item.AuthIndex,
		Provider:          item.Provider,
		AccountSnapshot:   item.AccountSnapshot,
		AccountIDSnapshot: item.AccountIDSnapshot,
	})
	if err != nil {
		if !errors.Is(err, cpaauthfiles.ErrAuthFileNotFound) && !errors.Is(err, cpaauthfiles.ErrIdentityMismatch) {
			_ = s.store.RecordAccountActionCandidateFailure(ctx, item.ID, err.Error())
			return authFile{}, err
		}
		return authFile{}, ErrCandidateConflict
	}
	return file, nil

}

func (s *Service) patchAuthFile(ctx context.Context, setup store.Setup, fileName string, authIndex string, disabled bool) error {
	return cpaauthfiles.New(s.client).PatchDisabled(ctx, setup.CPAUpstreamURL, setup.ManagementKey, fileName, disabled, authIndex)
}

func (s *Service) deleteAuthFile(ctx context.Context, setup store.Setup, fileName string) error {
	return cpaauthfiles.New(s.client).Delete(ctx, setup.CPAUpstreamURL, setup.ManagementKey, fileName)
}
