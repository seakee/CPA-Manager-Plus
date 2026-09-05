package accountaction

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	managerconfigsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

var ErrCandidateNotFound = errors.New("account action candidate not found")
var ErrCandidateConflict = errors.New("account action candidate no longer matches current CPA auth file")
var ErrCandidateNotPending = errors.New("account action candidate is not pending")

const postActionPersistenceTimeout = 5 * time.Second
const postActionPersistenceAttempts = 3

type Service struct {
	store                *store.Store
	managerConfigService *managerconfigsvc.Service
	client               *http.Client
	authFileMutations    *cpaauthfiles.MutationCoordinator
	actionMu             sync.Mutex
	actionLocks          map[int64]*candidateActionLock
}

type candidateActionLock struct {
	mu   sync.Mutex
	refs int
}

type ListResponse struct {
	Items        []model.AccountActionCandidate `json:"items"`
	PendingCount int64                          `json:"pendingCount"`
}

func New(st *store.Store, managerConfigService *managerconfigsvc.Service, clients ...*http.Client) *Service {
	return NewWithMutationCoordinator(st, managerConfigService, nil, clients...)
}

func NewWithMutationCoordinator(
	st *store.Store,
	managerConfigService *managerconfigsvc.Service,
	coordinator *cpaauthfiles.MutationCoordinator,
	clients ...*http.Client,
) *Service {
	client := &http.Client{Timeout: 30 * time.Second}
	if len(clients) > 0 && clients[0] != nil {
		client = clients[0]
	}
	if coordinator == nil {
		coordinator = cpaauthfiles.NewMutationCoordinator()
	}
	return &Service{
		store:                st,
		managerConfigService: managerConfigService,
		client:               client,
		authFileMutations:    coordinator,
	}
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
	unlock := s.lockCandidateAction(id)
	defer unlock()
	return s.updatePendingStatus(ctx, id, model.AccountActionStatusIgnored)
}

func (s *Service) Resolve(ctx context.Context, id int64) (model.AccountActionCandidate, error) {
	unlock := s.lockCandidateAction(id)
	defer unlock()
	return s.updatePendingStatus(ctx, id, model.AccountActionStatusResolved)
}

func (s *Service) Enable(ctx context.Context, id int64) (model.AccountActionCandidate, error) {
	unlock := s.lockCandidateAction(id)
	defer unlock()
	item, setup, err := s.resolvePendingCandidateAndSetup(ctx, id)
	if err != nil {
		return model.AccountActionCandidate{}, err
	}
	identity, err := accountActionIdentity(item)
	if err != nil {
		s.recordCandidateFailure(ctx, id, err)
		return model.AccountActionCandidate{}, candidateAuthFileError(err)
	}
	releaseMutation, err := s.acquireAuthFileMutation(ctx, identity.AuthFileName)
	if err != nil {
		s.recordCandidateFailure(ctx, id, err)
		return model.AccountActionCandidate{}, err
	}
	defer releaseMutation()
	client := cpaauthfiles.New(s.client)
	target, err := client.ResolveVerifiedStatusMutationTarget(
		ctx,
		setup.CPAUpstreamURL,
		setup.ManagementKey,
		identity,
	)
	if err != nil {
		s.recordCandidateFailure(ctx, id, err)
		return model.AccountActionCandidate{}, candidateAuthFileError(err)
	}
	if err := client.PatchDisabledTarget(ctx, setup.CPAUpstreamURL, setup.ManagementKey, target, false); err != nil {
		s.recordCandidateFailure(ctx, id, err)
		return model.AccountActionCandidate{}, candidateAuthFileError(err)
	}
	updated, persistErr := s.persistExternalActionStatus(
		ctx,
		id,
		model.AccountActionStatusResolved,
	)
	if persistErr == nil {
		return updated, nil
	}
	if !target.File.Disabled {
		resultErr := fmt.Errorf(
			"persist enabled account action result: %w; CPA credential was already enabled",
			persistErr,
		)
		s.recordCandidateFailure(ctx, id, resultErr)
		return model.AccountActionCandidate{}, resultErr
	}
	rollbackErr := s.restoreDisabledCredential(ctx, setup, client, identity)
	if rollbackErr != nil {
		resultErr := fmt.Errorf(
			"CPA credential was enabled, but candidate status persistence failed: %w; restoring the disabled state failed: %v",
			persistErr,
			rollbackErr,
		)
		s.recordCandidateFailure(ctx, id, resultErr)
		return model.AccountActionCandidate{}, resultErr
	}
	resultErr := fmt.Errorf(
		"persist enabled account action result: %w; CPA credential was restored to disabled",
		persistErr,
	)
	s.recordCandidateFailure(ctx, id, resultErr)
	return model.AccountActionCandidate{}, resultErr
}

func (s *Service) DeleteAuthFile(ctx context.Context, id int64) (model.AccountActionCandidate, error) {
	unlock := s.lockCandidateAction(id)
	defer unlock()
	item, setup, err := s.resolvePendingCandidateAndSetup(ctx, id)
	if err != nil {
		return model.AccountActionCandidate{}, err
	}
	identity, err := accountActionIdentity(item)
	if err != nil {
		s.recordCandidateFailure(ctx, id, err)
		return model.AccountActionCandidate{}, candidateAuthFileError(err)
	}
	releaseMutation, err := s.acquireAuthFileMutation(ctx, identity.AuthFileName)
	if err != nil {
		s.recordCandidateFailure(ctx, id, err)
		return model.AccountActionCandidate{}, err
	}
	defer releaseMutation()
	err = cpaauthfiles.New(s.client).DeleteVerifiedSingleCredential(
		ctx,
		setup.CPAUpstreamURL,
		setup.ManagementKey,
		identity,
	)
	if err != nil {
		s.recordCandidateFailure(ctx, id, err)
		return model.AccountActionCandidate{}, candidateAuthFileError(err)
	}
	updated, persistErr := s.persistExternalActionStatus(
		ctx,
		id,
		model.AccountActionStatusDeleted,
	)
	if persistErr == nil {
		return updated, nil
	}
	resultErr := fmt.Errorf(
		"CPA auth file delete succeeded but candidate status persistence failed: %w",
		persistErr,
	)
	s.recordCandidateFailure(ctx, id, resultErr)
	return model.AccountActionCandidate{}, resultErr
}

func (s *Service) acquireAuthFileMutation(ctx context.Context, fileName string) (func(), error) {
	if s == nil || s.authFileMutations == nil {
		return nil, cpaauthfiles.ErrMutationCoordinatorUnavailable
	}
	return s.authFileMutations.Acquire(ctx, fileName)
}

func (s *Service) persistExternalActionStatus(
	ctx context.Context,
	id int64,
	status string,
) (model.AccountActionCandidate, error) {
	var lastErr error
	for attempt := 0; attempt < postActionPersistenceAttempts; attempt++ {
		persistCtx, cancel := detachedActionContext(ctx)
		item, err := s.updatePendingStatus(persistCtx, id, status)
		cancel()
		if err == nil {
			return item, nil
		}
		lastErr = err

		readCtx, readCancel := detachedActionContext(ctx)
		current, ok, readErr := s.store.GetAccountActionCandidate(readCtx, id)
		readCancel()
		if readErr == nil && ok && current.Status == status {
			return current, nil
		}
		if errors.Is(err, ErrCandidateNotFound) || errors.Is(err, ErrCandidateNotPending) {
			break
		}
		if readErr != nil {
			lastErr = fmt.Errorf("%w; verify candidate status: %v", err, readErr)
		}
	}
	return model.AccountActionCandidate{}, lastErr
}

func (s *Service) restoreDisabledCredential(
	ctx context.Context,
	setup store.Setup,
	client *cpaauthfiles.Client,
	identity cpaauthfiles.Identity,
) error {
	rollbackCtx, cancel := detachedActionContext(ctx)
	defer cancel()
	target, err := client.ResolveVerifiedStatusMutationTarget(
		rollbackCtx,
		setup.CPAUpstreamURL,
		setup.ManagementKey,
		identity,
	)
	if err != nil {
		return candidateAuthFileError(err)
	}
	if err := client.PatchDisabledTarget(
		rollbackCtx,
		setup.CPAUpstreamURL,
		setup.ManagementKey,
		target,
		true,
	); err != nil {
		return candidateAuthFileError(err)
	}
	return nil
}

func (s *Service) recordCandidateFailure(ctx context.Context, id int64, err error) {
	if s == nil || s.store == nil || err == nil {
		return
	}
	persistCtx, cancel := detachedActionContext(ctx)
	defer cancel()
	_ = s.store.RecordAccountActionCandidateFailure(persistCtx, id, err.Error())
}

func detachedActionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), postActionPersistenceTimeout)
}

func (s *Service) lockCandidateAction(id int64) func() {
	s.actionMu.Lock()
	if s.actionLocks == nil {
		s.actionLocks = make(map[int64]*candidateActionLock)
	}
	lock := s.actionLocks[id]
	if lock == nil {
		lock = &candidateActionLock{}
		s.actionLocks[id] = lock
	}
	lock.refs++
	s.actionMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		s.actionMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(s.actionLocks, id)
		}
		s.actionMu.Unlock()
	}
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

func accountActionIdentity(item model.AccountActionCandidate) (cpaauthfiles.Identity, error) {
	fileName := strings.TrimSpace(item.AuthFileName)
	accountSnapshot := strings.TrimSpace(item.AccountSnapshot)
	if accountSnapshot == fileName {
		accountSnapshot = ""
	}
	authIndex := strings.TrimSpace(item.AuthIndex)
	provider := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(item.Provider), "_", "-"))
	if provider == "x-ai" || provider == "grok" {
		provider = "xai"
	}
	accountIDSnapshot := strings.TrimSpace(item.AccountIDSnapshot)
	if provider == "codex" {
		if accountIDSnapshot != "" {
			workspace, ok := usageidentity.NormalizeCodexWorkspaceSnapshot(accountIDSnapshot)
			if !ok {
				return cpaauthfiles.Identity{}, fmt.Errorf("%w: candidate has invalid Codex workspace identity", cpaauthfiles.ErrIdentityMismatch)
			}
			accountIDSnapshot = workspace
		}
		if accountSnapshot != "" {
			member, ok := usageidentity.NormalizeCodexMemberSnapshot(accountSnapshot)
			if ok {
				accountSnapshot = member
			} else {
				// A display label is not a Codex member identity. An explicit
				// auth_index may still identify the credential, but the weak
				// display value must not be used as an ownership check.
				accountSnapshot = ""
			}
		}
		if authIndex == "" {
			return cpaauthfiles.Identity{}, fmt.Errorf("%w: Codex credential mutation requires auth_index", cpaauthfiles.ErrIdentityMismatch)
		}
	}
	identity := cpaauthfiles.Identity{
		AuthFileName:      fileName,
		AuthIndex:         authIndex,
		Provider:          provider,
		AccountSnapshot:   accountSnapshot,
		AccountIDSnapshot: accountIDSnapshot,
	}
	if identity.AuthIndex == "" && identity.AccountSnapshot == "" && identity.AccountIDSnapshot == "" {
		return cpaauthfiles.Identity{}, fmt.Errorf("%w: candidate has no stable auth index, account ID, or account snapshot", cpaauthfiles.ErrIdentityMismatch)
	}
	return identity, nil
}

func candidateAuthFileError(err error) error {
	if errors.Is(err, cpaauthfiles.ErrAuthFileNotFound) ||
		errors.Is(err, cpaauthfiles.ErrIdentityMismatch) ||
		errors.Is(err, cpaauthfiles.ErrStatusMutationScopeAmbiguous) ||
		errors.Is(err, cpaauthfiles.ErrDeleteMutationScopeAmbiguous) {
		return fmt.Errorf("%w: %w", ErrCandidateConflict, err)
	}
	return err
}
