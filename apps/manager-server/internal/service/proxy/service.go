package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type Service struct {
	managerConfigService *managerconfig.Service
	store                *store.Store
	authFileMutations    *cpaauthfiles.MutationCoordinator
}

type authFileOwnershipMutation struct {
	fileNames        []string
	ownershipTargets []model.CodexInspectionDisableOwnershipTarget
	clearAll         bool
	lockAll          bool
	statusMutation   *authFileStatusMutation
	fieldsMutation   *authFileFieldsMutation
	deleteMutation   *authFileDeleteMutation
	writeMutation    *authFileWriteMutation
}

type authFileStatusMutation struct {
	selector         string
	authIndex        string
	sourceFile       bool
	sourceIdentities []cpaauthfiles.Identity
	physicalName     string
	runtimeID        string
	provider         string
	accountID        string
	accountSnapshot  string
	hasIdentity      bool
}

type authFileSourceIdentityPayload struct {
	Name            string          `json:"name"`
	RuntimeID       string          `json:"runtime_id"`
	AuthIndex       json.RawMessage `json:"auth_index"`
	Provider        string          `json:"provider"`
	AccountID       string          `json:"account_id"`
	AccountSnapshot string          `json:"account_snapshot"`
}

type authFileDeleteMutation struct {
	selector     string
	physicalName string
	identities   []cpaauthfiles.Identity
}

type authFileFieldsMutation struct {
	selector string
	identity cpaauthfiles.Identity
}

type authFileWriteMutation struct {
	physicalName  string
	identities    []cpaauthfiles.Identity
	contentSHA256 string
}

const cpaPluginResourcePrefix = "/v0/resource/plugins"
const cpaManagementPrefix = "/v0/management"
const codexInviteOriginHeader = "X-Codex-Invite-Origin"
const managementOriginJSONField = "management_origin"
const authFilePhysicalNameHeader = "X-CPAMP-Auth-File-Physical-Name"
const authFileDeleteIdentitiesHeader = "X-CPAMP-Auth-File-Delete-Identities"
const authFileMutationIdentityHeader = "X-CPAMP-Auth-File-Mutation-Identity"
const authFileWriteIdentitiesHeader = "X-CPAMP-Auth-File-Write-Identities"
const authFileWriteContentSHA256Header = "X-CPAMP-Auth-File-Write-Content-SHA256"

const maxAuthFileMutationRequestBytes int64 = 10*1024*1024 + 64*1024
const maxAuthFileMutationResponseBytes int64 = 1024 * 1024
const authFileOwnershipPersistenceTimeout = 5 * time.Second

var errAuthFileMutationBodyTooLarge = errors.New("auth file mutation body is too large")

var cpaBuiltinManagementPathHeads = map[string]struct{}{
	"account-action-candidates": {},
	"accounts":                  {},
	"api-call":                  {},
	"api-key-aliases":           {},
	"api-key-usage":             {},
	"auth-files":                {},
	"codex-inspection":          {},
	"config":                    {},
	"dashboard":                 {},
	"model-prices":              {},
	"monitoring":                {},
	"plugin-store":              {},
	"plugins":                   {},
	"reload":                    {},
	"usage":                     {},
	"usage-statistics-enabled":  {},
}

func New(managerConfigService *managerconfig.Service, stores ...*store.Store) *Service {
	return NewWithMutationCoordinator(managerConfigService, nil, stores...)
}

func NewWithMutationCoordinator(
	managerConfigService *managerconfig.Service,
	coordinator *cpaauthfiles.MutationCoordinator,
	stores ...*store.Store,
) *Service {
	if coordinator == nil {
		coordinator = cpaauthfiles.NewMutationCoordinator()
	}
	service := &Service{
		managerConfigService: managerConfigService,
		authFileMutations:    coordinator,
	}
	if len(stores) > 0 {
		service.store = stores[0]
	}
	return service
}

func (s *Service) ProxyManagement(w http.ResponseWriter, r *http.Request, writeError func(http.ResponseWriter, int, error)) {
	s.proxyWithSavedManagementKey(w, r, writeError)
}

func (s *Service) ProxyPluginManagement(w http.ResponseWriter, r *http.Request, writeError func(http.ResponseWriter, int, error)) {
	if !IsCPAPluginManagementPath(r.URL.Path) {
		writeError(w, http.StatusNotFound, errors.New("proxy path must be a CPA plugin management path"))
		return
	}
	s.proxyToSavedSetup(w, r, writeError, true, true)
}

func (s *Service) ProxyPluginManagementWithCallerAuth(w http.ResponseWriter, r *http.Request, writeError func(http.ResponseWriter, int, error)) {
	if !IsCPAPluginManagementPath(r.URL.Path) {
		writeError(w, http.StatusNotFound, errors.New("proxy path must be a CPA plugin management path"))
		return
	}
	s.proxyToSavedSetup(w, r, writeError, false, true)
}

func (s *Service) ProxyPluginResource(w http.ResponseWriter, r *http.Request, writeError func(http.ResponseWriter, int, error)) {
	if !IsCPAPluginResourcePath(r.URL.Path) {
		writeError(w, http.StatusNotFound, errors.New("proxy path must be under /v0/resource/plugins/"))
		return
	}
	s.proxyToSavedSetup(w, r, writeError, true, true)
}

func (s *Service) ProxyPluginResourceWithCallerAuth(w http.ResponseWriter, r *http.Request, writeError func(http.ResponseWriter, int, error)) {
	if !IsCPAPluginResourcePath(r.URL.Path) {
		writeError(w, http.StatusNotFound, errors.New("proxy path must be under /v0/resource/plugins/"))
		return
	}
	s.proxyToSavedSetup(w, r, writeError, false, true)
}

func (s *Service) proxyWithSavedManagementKey(w http.ResponseWriter, r *http.Request, writeError func(http.ResponseWriter, int, error)) {
	if !isManagementPath(r.URL.Path) {
		writeError(w, http.StatusNotFound, errors.New("proxy path must be under /v0/management/"))
		return
	}
	s.proxyToSavedSetup(w, r, writeError, true, false)
}

func (s *Service) proxyToSavedSetup(w http.ResponseWriter, r *http.Request, writeError func(http.ResponseWriter, int, error), useSavedManagementKey bool, rewritePluginOrigin bool) {
	setup, ok, err := s.resolveSetup(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeError(w, http.StatusPreconditionRequired, errors.New("usage service is not configured"))
		return
	}
	target, err := url.Parse(setup.CPAUpstreamURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if rewritePluginOrigin {
		if err := rewritePluginManagementOriginBody(r, target); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	ownershipMutation, err := inspectAuthFileOwnershipMutation(r)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errAuthFileMutationBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeError(w, status, err)
		return
	}
	releaseAuthFileMutation, err := s.acquireAuthFileMutation(r.Context(), ownershipMutation)
	if err != nil {
		writeError(w, http.StatusRequestTimeout, err)
		return
	}
	defer releaseAuthFileMutation()
	ownershipMutation, err = s.prepareAuthFileMutation(r.Context(), setup, r, ownershipMutation)
	if err != nil {
		status := http.StatusBadGateway
		switch {
		case errors.Is(err, cpaauthfiles.ErrStatusMutationScopeAmbiguous):
			status = http.StatusConflict
		case errors.Is(err, cpaauthfiles.ErrDeleteMutationScopeAmbiguous):
			status = http.StatusConflict
		case errors.Is(err, cpaauthfiles.ErrIdentityMismatch):
			status = http.StatusConflict
		case errors.Is(err, cpaauthfiles.ErrAuthFileNotFound):
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	revokedOwnership, err := s.revokeInspectionOwnershipDetached(r.Context(), ownershipMutation)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if ownershipMutation.clearAll {
		ownershipMutation.fileNames = ownershipFileNames(revokedOwnership)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
		if useSavedManagementKey {
			req.Header.Set("Authorization", "Bearer "+setup.ManagementKey)
		}
		if rewritePluginOrigin {
			rewriteCodexInviteOrigin(req.Header, target)
		}
		req.Header.Del(authFilePhysicalNameHeader)
		req.Header.Del(authFileDeleteIdentitiesHeader)
		req.Header.Del(authFileMutationIdentityHeader)
		req.Header.Del(authFileWriteIdentitiesHeader)
		req.Header.Del(authFileWriteContentSHA256Header)
		if ownershipMutation.clearAll || len(ownershipMutation.fileNames) > 0 || len(ownershipMutation.ownershipTargets) > 0 {
			req.Header.Set("Accept-Encoding", "identity")
		}
	}
	responseProcessed := false
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, proxyErr error) {
		if !responseProcessed {
			if restoreErr := s.restoreInspectionOwnershipDetached(r.Context(), revokedOwnership); restoreErr != nil {
				proxyErr = fmt.Errorf("%w; restore inspection ownership: %v", proxyErr, restoreErr)
			}
		}
		writeError(w, http.StatusBadGateway, proxyErr)
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		responseProcessed = true
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return s.restoreInspectionOwnershipDetached(r.Context(), revokedOwnership)
		}
		mutation, err := successfulAuthFileOwnershipMutation(response, ownershipMutation)
		if err != nil {
			// The upstream request may already have succeeded. Keep ownership revoked
			// when a 2xx response cannot be interpreted, otherwise a later automatic
			// recovery could overwrite the user's manual auth-file mutation.
			return err
		}
		return s.restoreInspectionOwnershipDetached(r.Context(), ownershipItemsNotMutated(revokedOwnership, mutation))
	}
	proxy.ServeHTTP(w, r)
}

func (s *Service) acquireAuthFileMutation(
	ctx context.Context,
	mutation authFileOwnershipMutation,
) (func(), error) {
	keys, all := authFileMutationLockTargets(mutation)
	if !all && len(keys) == 0 {
		return func() {}, nil
	}
	if s == nil || s.authFileMutations == nil {
		return nil, cpaauthfiles.ErrMutationCoordinatorUnavailable
	}
	if all {
		return s.authFileMutations.AcquireAll(ctx)
	}
	return s.authFileMutations.Acquire(ctx, keys...)
}

func authFileMutationLockTargets(mutation authFileOwnershipMutation) ([]string, bool) {
	if mutation.clearAll || mutation.lockAll {
		return nil, true
	}

	names := append([]string(nil), mutation.fileNames...)
	for _, target := range mutation.ownershipTargets {
		names = append(names, target.FileName)
	}
	if mutation.statusMutation != nil {
		names = append(names, mutation.statusMutation.physicalName, mutation.statusMutation.selector)
		for _, identity := range mutation.statusMutation.sourceIdentities {
			names = append(names, identity.AuthFileName)
		}
	}
	if mutation.fieldsMutation != nil {
		names = append(names, mutation.fieldsMutation.identity.AuthFileName)
	}
	if mutation.deleteMutation != nil {
		names = append(names, mutation.deleteMutation.physicalName)
		for _, identity := range mutation.deleteMutation.identities {
			names = append(names, identity.AuthFileName)
		}
	}
	if mutation.writeMutation != nil {
		names = append(names, mutation.writeMutation.physicalName)
	}

	seen := make(map[string]struct{}, len(names))
	keys := make([]string, 0, len(names))
	for _, name := range names {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, false
}

func (s *Service) prepareAuthFileMutation(
	ctx context.Context,
	setup store.Setup,
	r *http.Request,
	mutation authFileOwnershipMutation,
) (authFileOwnershipMutation, error) {
	prepared, err := s.prepareAuthFileStatusMutation(ctx, setup, r, mutation)
	if err != nil {
		return authFileOwnershipMutation{}, err
	}
	prepared, err = s.prepareAuthFileFieldsMutation(ctx, setup, r, prepared)
	if err != nil {
		return authFileOwnershipMutation{}, err
	}
	prepared, err = s.prepareAuthFileDeleteMutation(ctx, setup, r, prepared)
	if err != nil {
		return authFileOwnershipMutation{}, err
	}
	return s.prepareAuthFileWriteMutation(ctx, setup, prepared)
}

func (s *Service) revokeInspectionOwnership(ctx context.Context, mutation authFileOwnershipMutation) ([]store.CodexInspectionDisableOwnership, error) {
	if s.store == nil || (!mutation.clearAll && len(mutation.fileNames) == 0 && len(mutation.ownershipTargets) == 0) {
		return nil, nil
	}
	targets := make([]model.CodexInspectionDisableOwnershipTarget, 0, len(mutation.fileNames)+len(mutation.ownershipTargets))
	for _, fileName := range mutation.fileNames {
		targets = append(targets, model.CodexInspectionDisableOwnershipTarget{FileName: fileName})
	}
	targets = append(targets, mutation.ownershipTargets...)
	return s.store.RevokeCodexInspectionDisableOwnership(ctx, targets, mutation.clearAll)
}

func (s *Service) revokeInspectionOwnershipDetached(
	ctx context.Context,
	mutation authFileOwnershipMutation,
) ([]store.CodexInspectionDisableOwnership, error) {
	persistCtx, cancel := detachedAuthFileOwnershipContext(ctx)
	defer cancel()
	return s.revokeInspectionOwnership(persistCtx, mutation)
}

func (s *Service) prepareAuthFileStatusMutation(
	ctx context.Context,
	setup store.Setup,
	r *http.Request,
	mutation authFileOwnershipMutation,
) (authFileOwnershipMutation, error) {
	if mutation.statusMutation == nil {
		return mutation, nil
	}
	client := cpaauthfiles.New(nil)
	var target cpaauthfiles.StatusMutationTarget
	var err error
	statusMutation := mutation.statusMutation
	physicalName := strings.TrimSpace(statusMutation.physicalName)
	if physicalName == "" {
		physicalName = strings.TrimSpace(statusMutation.selector)
	}
	identity := cpaauthfiles.Identity{
		AuthFileName:      physicalName,
		RuntimeID:         strings.TrimSpace(statusMutation.runtimeID),
		AuthIndex:         strings.TrimSpace(statusMutation.authIndex),
		Provider:          strings.TrimSpace(statusMutation.provider),
		AccountIDSnapshot: strings.TrimSpace(statusMutation.accountID),
		AccountSnapshot:   strings.TrimSpace(statusMutation.accountSnapshot),
	}
	if mutation.statusMutation.sourceFile {
		target, err = client.ResolveVerifiedSourceFileStatusMutationTarget(
			ctx,
			setup.CPAUpstreamURL,
			setup.ManagementKey,
			mutation.statusMutation.selector,
			mutation.statusMutation.authIndex,
			mutation.statusMutation.sourceIdentities,
		)
		if err == nil && statusMutation.hasIdentity {
			err = cpaauthfiles.VerifyResolvedIdentity(target.File, identity)
		}
	} else if statusMutation.hasIdentity {
		target, err = client.ResolveVerifiedStatusMutationTarget(
			ctx,
			setup.CPAUpstreamURL,
			setup.ManagementKey,
			identity,
		)
	} else {
		target, err = client.ResolveStatusMutationTarget(
			ctx,
			setup.CPAUpstreamURL,
			setup.ManagementKey,
			mutation.statusMutation.selector,
			mutation.statusMutation.authIndex,
		)
	}
	if err != nil {
		return authFileOwnershipMutation{}, err
	}
	forwardSelector := target.Selector
	if strings.TrimRight(r.URL.Path, "/") == "/v0/management/auth-files" {
		forwardSelector = mutation.statusMutation.selector
	}
	if err := rewriteAuthFileStatusMutationRequest(r, target, forwardSelector); err != nil {
		return authFileOwnershipMutation{}, err
	}
	if target.Scope == cpaauthfiles.StatusMutationScopeSourceFile {
		mutation.fileNames = []string{target.File.Name}
		mutation.ownershipTargets = nil
		return mutation, nil
	}
	provider := target.File.Provider
	authIndex := target.File.AuthIndex
	accountID := target.File.AccountID
	accountSnapshot := target.File.AccountSnapshot
	mutation.fileNames = nil
	mutation.ownershipTargets = []model.CodexInspectionDisableOwnershipTarget{{
		FileName:        target.File.Name,
		Provider:        &provider,
		AuthIndex:       &authIndex,
		AccountID:       &accountID,
		AccountSnapshot: &accountSnapshot,
	}}
	return mutation, nil
}

func (s *Service) prepareAuthFileDeleteMutation(
	ctx context.Context,
	setup store.Setup,
	r *http.Request,
	mutation authFileOwnershipMutation,
) (authFileOwnershipMutation, error) {
	deleteMutation := mutation.deleteMutation
	if deleteMutation == nil {
		return mutation, nil
	}
	if len(deleteMutation.identities) == 0 {
		return authFileOwnershipMutation{}, fmt.Errorf("%w: delete identity is required", cpaauthfiles.ErrAuthFileNotFound)
	}

	requestedSelector := strings.TrimSpace(deleteMutation.selector)
	physicalName := strings.TrimSpace(deleteMutation.physicalName)
	physicalDeleteRequested := physicalName != "" && requestedSelector == physicalName
	client := cpaauthfiles.New(nil)
	var target cpaauthfiles.DeleteMutationTarget
	var err error
	if len(deleteMutation.identities) == 1 && !physicalDeleteRequested {
		target, err = client.ResolveVerifiedDeleteMutationTarget(
			ctx,
			setup.CPAUpstreamURL,
			setup.ManagementKey,
			deleteMutation.identities[0],
		)
	} else {
		target, err = client.ResolveVerifiedPhysicalFileDeleteTarget(
			ctx,
			setup.CPAUpstreamURL,
			setup.ManagementKey,
			deleteMutation.identities,
		)
	}
	if err != nil {
		return authFileOwnershipMutation{}, err
	}

	if physicalName == "" {
		physicalName = strings.TrimSpace(target.File.Name)
	}
	forwardSelector := strings.TrimSpace(target.Selector)
	if requestedSelector == physicalName {
		forwardSelector = physicalName
	} else if requestedSelector != "" && requestedSelector != forwardSelector {
		return authFileOwnershipMutation{}, fmt.Errorf(
			"%w: delete selector mismatch (expected %q or %q, got %q)",
			cpaauthfiles.ErrIdentityMismatch,
			forwardSelector,
			physicalName,
			requestedSelector,
		)
	}
	if forwardSelector == "" {
		return authFileOwnershipMutation{}, fmt.Errorf("%w: delete selector is empty", cpaauthfiles.ErrAuthFileNotFound)
	}

	query := r.URL.Query()
	query.Set("name", forwardSelector)
	r.URL.RawQuery = query.Encode()
	mutation.fileNames = []string{strings.TrimSpace(target.File.Name)}
	mutation.ownershipTargets = nil
	return mutation, nil
}

func (s *Service) prepareAuthFileFieldsMutation(
	ctx context.Context,
	setup store.Setup,
	r *http.Request,
	mutation authFileOwnershipMutation,
) (authFileOwnershipMutation, error) {
	fieldsMutation := mutation.fieldsMutation
	if fieldsMutation == nil {
		return mutation, nil
	}

	target, err := cpaauthfiles.New(nil).ResolveVerifiedStatusMutationTarget(
		ctx,
		setup.CPAUpstreamURL,
		setup.ManagementKey,
		fieldsMutation.identity,
	)
	if err != nil {
		return authFileOwnershipMutation{}, err
	}
	if target.Scope != cpaauthfiles.StatusMutationScopeCredential {
		return authFileOwnershipMutation{}, fmt.Errorf(
			"%w: auth file fields target %q affects a physical source",
			cpaauthfiles.ErrStatusMutationScopeAmbiguous,
			strings.TrimSpace(target.File.Name),
		)
	}

	requestedSelector := strings.TrimSpace(fieldsMutation.selector)
	physicalName := strings.TrimSpace(fieldsMutation.identity.AuthFileName)
	if requestedSelector != strings.TrimSpace(target.Selector) && requestedSelector != physicalName {
		return authFileOwnershipMutation{}, fmt.Errorf(
			"%w: fields selector mismatch (expected %q or %q, got %q)",
			cpaauthfiles.ErrIdentityMismatch,
			strings.TrimSpace(target.Selector),
			physicalName,
			requestedSelector,
		)
	}
	if err := rewriteAuthFileFieldsMutationRequest(r, target.Selector); err != nil {
		return authFileOwnershipMutation{}, err
	}
	return mutation, nil
}

func (s *Service) prepareAuthFileWriteMutation(
	ctx context.Context,
	setup store.Setup,
	mutation authFileOwnershipMutation,
) (authFileOwnershipMutation, error) {
	writeMutation := mutation.writeMutation
	if writeMutation == nil {
		return mutation, nil
	}
	if len(writeMutation.identities) == 0 {
		return authFileOwnershipMutation{}, fmt.Errorf(
			"%w: auth file write identities are required",
			cpaauthfiles.ErrAuthFileNotFound,
		)
	}

	client := cpaauthfiles.New(nil)
	target, err := client.ResolveVerifiedPhysicalFileDeleteTarget(
		ctx,
		setup.CPAUpstreamURL,
		setup.ManagementKey,
		writeMutation.identities,
	)
	if err != nil {
		return authFileOwnershipMutation{}, err
	}
	physicalName := strings.TrimSpace(writeMutation.physicalName)
	if physicalName == "" || strings.TrimSpace(target.File.Name) != physicalName {
		return authFileOwnershipMutation{}, fmt.Errorf(
			"%w: auth file write target changed (expected %q, got %q)",
			cpaauthfiles.ErrIdentityMismatch,
			physicalName,
			strings.TrimSpace(target.File.Name),
		)
	}
	content, err := client.Download(ctx, setup.CPAUpstreamURL, setup.ManagementKey, physicalName)
	if err != nil {
		return authFileOwnershipMutation{}, err
	}
	actualSHA256 := fmt.Sprintf("%x", sha256.Sum256(content))
	if actualSHA256 != writeMutation.contentSHA256 {
		return authFileOwnershipMutation{}, fmt.Errorf(
			"%w: auth file write content changed (expected SHA-256 %s, got %s)",
			cpaauthfiles.ErrIdentityMismatch,
			writeMutation.contentSHA256,
			actualSHA256,
		)
	}
	// Verified writes are the Manager fallback for credential-scoped field patches.
	// They preserve the current disabled value from the downloaded source, so they
	// must not revoke inspection ownership like an arbitrary user upload does.
	mutation.fileNames = nil
	mutation.ownershipTargets = nil
	return mutation, nil
}

func (s *Service) restoreInspectionOwnership(ctx context.Context, items []store.CodexInspectionDisableOwnership) error {
	if s.store == nil || len(items) == 0 {
		return nil
	}
	return s.store.RestoreCodexInspectionDisableOwnership(ctx, items)
}

func (s *Service) restoreInspectionOwnershipDetached(ctx context.Context, items []store.CodexInspectionDisableOwnership) error {
	persistCtx, cancel := detachedAuthFileOwnershipContext(ctx)
	defer cancel()
	return s.restoreInspectionOwnership(persistCtx, items)
}

func detachedAuthFileOwnershipContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), authFileOwnershipPersistenceTimeout)
}

func inspectAuthFileOwnershipMutation(r *http.Request) (authFileOwnershipMutation, error) {
	if r == nil {
		return authFileOwnershipMutation{}, nil
	}
	path := strings.TrimRight(r.URL.Path, "/")
	if path != "/v0/management/auth-files" &&
		path != "/v0/management/auth-files/status" &&
		path != "/v0/management/auth-files/fields" {
		return authFileOwnershipMutation{}, nil
	}
	if path == "/v0/management/auth-files/fields" {
		if r.Method != http.MethodPatch {
			return authFileOwnershipMutation{}, nil
		}
		return readAuthFileFieldsMutation(r)
	}

	switch r.Method {
	case http.MethodPatch:
		return readJSONAuthFileOwnershipMutation(r, path == "/v0/management/auth-files/status")
	case http.MethodDelete:
		if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("all")), "true") {
			return authFileOwnershipMutation{clearAll: true}, nil
		}
		identities, err := readAuthFileDeleteIdentities(r)
		if err != nil {
			return authFileOwnershipMutation{}, err
		}
		physicalNameProvided := strings.TrimSpace(r.Header.Get(authFilePhysicalNameHeader)) != ""
		fileNames := normalizeFileNames([]string{r.Header.Get(authFilePhysicalNameHeader)})
		if len(fileNames) == 0 {
			fileNames = normalizeFileNames([]string{r.URL.Query().Get("name")})
		}
		if len(fileNames) > 0 {
			mutation := authFileOwnershipMutation{
				fileNames: fileNames,
				lockAll:   !physicalNameProvided && len(identities) == 0,
			}
			if len(identities) > 0 {
				mutation.deleteMutation = &authFileDeleteMutation{
					selector:     strings.TrimSpace(r.URL.Query().Get("name")),
					physicalName: fileNames[0],
					identities:   identities,
				}
			}
			return mutation, nil
		}
		return readJSONAuthFileOwnershipMutation(r, false)
	case http.MethodPost:
		fileNames, err := readMultipartAuthFileNames(r)
		if err != nil {
			return authFileOwnershipMutation{}, err
		}
		mutation := authFileOwnershipMutation{fileNames: fileNames}
		identities, identityErr := readAuthFileIdentitiesHeader(
			r,
			authFileWriteIdentitiesHeader,
			"write",
		)
		if identityErr != nil {
			return authFileOwnershipMutation{}, identityErr
		}
		contentSHA256, contentSHA256Err := readAuthFileContentSHA256Header(r)
		if contentSHA256Err != nil {
			return authFileOwnershipMutation{}, contentSHA256Err
		}
		if len(identities) > 0 {
			if len(fileNames) != 1 {
				return authFileOwnershipMutation{}, errors.New("verified auth file write requires exactly one file")
			}
			if contentSHA256 == "" {
				return authFileOwnershipMutation{}, errors.New("verified auth file write content SHA-256 is required")
			}
			mutation.writeMutation = &authFileWriteMutation{
				physicalName:  fileNames[0],
				identities:    identities,
				contentSHA256: contentSHA256,
			}
		} else if contentSHA256 != "" {
			return authFileOwnershipMutation{}, errors.New("verified auth file write identities are required")
		}
		return mutation, nil
	default:
		return authFileOwnershipMutation{}, nil
	}
}

func readAuthFileContentSHA256Header(r *http.Request) (string, error) {
	if r == nil {
		return "", nil
	}
	raw := strings.TrimSpace(r.Header.Get(authFileWriteContentSHA256Header))
	if raw == "" {
		return "", nil
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != sha256.Size {
		return "", errors.New("auth file write content SHA-256 is invalid")
	}
	return strings.ToLower(raw), nil
}

func readAuthFileDeleteIdentities(r *http.Request) ([]cpaauthfiles.Identity, error) {
	return readAuthFileIdentitiesHeader(r, authFileDeleteIdentitiesHeader, "delete")
}

func readAuthFileIdentitiesHeader(
	r *http.Request,
	headerName string,
	operation string,
) ([]cpaauthfiles.Identity, error) {
	if r == nil {
		return nil, nil
	}
	raw := strings.TrimSpace(r.Header.Get(headerName))
	if raw == "" {
		return nil, nil
	}
	decoded, err := url.QueryUnescape(raw)
	if err != nil {
		return nil, fmt.Errorf("decode auth file %s identities: %w", operation, err)
	}
	if int64(len(decoded)) > maxAuthFileMutationResponseBytes {
		return nil, errAuthFileMutationBodyTooLarge
	}
	var payload []struct {
		Name            string          `json:"name"`
		RuntimeID       string          `json:"runtimeId"`
		AuthIndex       json.RawMessage `json:"authIndex"`
		Provider        string          `json:"provider"`
		AccountID       string          `json:"accountId"`
		AccountSnapshot string          `json:"accountSnapshot"`
	}
	if err := json.Unmarshal([]byte(decoded), &payload); err != nil {
		return nil, fmt.Errorf("decode auth file %s identities: %w", operation, err)
	}
	identities := make([]cpaauthfiles.Identity, 0, len(payload))
	for _, item := range payload {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return nil, fmt.Errorf("auth file %s identity name is required", operation)
		}
		identities = append(identities, cpaauthfiles.Identity{
			AuthFileName:      name,
			RuntimeID:         strings.TrimSpace(item.RuntimeID),
			AuthIndex:         normalizeJSONScalarString(item.AuthIndex),
			Provider:          strings.TrimSpace(item.Provider),
			AccountIDSnapshot: strings.TrimSpace(item.AccountID),
			AccountSnapshot:   strings.TrimSpace(item.AccountSnapshot),
		})
	}
	if len(identities) == 0 {
		return nil, fmt.Errorf("auth file %s identities are required", operation)
	}
	return identities, nil
}

func readAuthFileFieldsMutation(r *http.Request) (authFileOwnershipMutation, error) {
	identities, err := readAuthFileIdentitiesHeader(
		r,
		authFileMutationIdentityHeader,
		"mutation",
	)
	if err != nil {
		return authFileOwnershipMutation{}, err
	}
	if len(identities) == 0 {
		// Legacy callers do not identify the physical file before forwarding the
		// fields mutation. Serialize it globally so a runtime selector cannot race
		// a worker that coordinates by physical file name.
		return authFileOwnershipMutation{lockAll: true}, nil
	}
	if len(identities) != 1 {
		return authFileOwnershipMutation{}, errors.New("auth file field mutation requires one identity")
	}
	raw, err := readAndRestoreRequestBody(r, maxAuthFileMutationRequestBytes)
	if err != nil {
		return authFileOwnershipMutation{}, err
	}
	var payload struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return authFileOwnershipMutation{}, fmt.Errorf("decode auth file fields mutation: %w", err)
	}
	selector := strings.TrimSpace(payload.Name)
	if selector == "" {
		return authFileOwnershipMutation{}, errors.New("auth file fields selector is required")
	}
	return authFileOwnershipMutation{
		fieldsMutation: &authFileFieldsMutation{
			selector: selector,
			identity: identities[0],
		},
	}, nil
}

func readJSONAuthFileOwnershipMutation(r *http.Request, statusPath bool) (authFileOwnershipMutation, error) {
	if r.Body == nil {
		return authFileOwnershipMutation{}, nil
	}
	raw, err := readAndRestoreRequestBody(r, maxAuthFileMutationRequestBytes)
	if err != nil {
		return authFileOwnershipMutation{}, err
	}
	var payload struct {
		Name                  string                          `json:"name"`
		Names                 []string                        `json:"names"`
		AuthIndex             json.RawMessage                 `json:"auth_index"`
		Disabled              json.RawMessage                 `json:"disabled"`
		CPAMPSourceFile       bool                            `json:"cpamp_source_file"`
		CPAMPPhysicalName     string                          `json:"cpamp_physical_name"`
		CPAMPRuntimeID        string                          `json:"cpamp_runtime_id"`
		CPAMPProvider         string                          `json:"cpamp_provider"`
		CPAMPAccountID        string                          `json:"cpamp_account_id"`
		CPAMPAccountSnapshot  string                          `json:"cpamp_account_snapshot"`
		CPAMPSourceIdentities []authFileSourceIdentityPayload `json:"cpamp_source_identities"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return authFileOwnershipMutation{}, nil
	}
	fileNames := normalizeFileNames(append(payload.Names, payload.Name))
	physicalName := strings.TrimSpace(payload.CPAMPPhysicalName)
	mutation := authFileOwnershipMutation{
		fileNames: fileNames,
		lockAll:   len(fileNames) > 0 && physicalName == "",
	}
	if statusPath || len(payload.Disabled) > 0 {
		sourceIdentities, sourceIdentityErr := normalizeAuthFileSourceIdentities(payload.CPAMPSourceIdentities)
		if sourceIdentityErr != nil {
			return authFileOwnershipMutation{}, sourceIdentityErr
		}
		if statusPath && payload.CPAMPSourceFile && len(sourceIdentities) == 0 {
			return authFileOwnershipMutation{}, errors.New("auth file source identities are required")
		}
		mutation.statusMutation = &authFileStatusMutation{
			selector:         strings.TrimSpace(payload.Name),
			authIndex:        normalizeJSONScalarString(payload.AuthIndex),
			sourceFile:       statusPath && payload.CPAMPSourceFile,
			sourceIdentities: sourceIdentities,
			physicalName:     physicalName,
			runtimeID:        strings.TrimSpace(payload.CPAMPRuntimeID),
			provider:         strings.TrimSpace(payload.CPAMPProvider),
			accountID:        strings.TrimSpace(payload.CPAMPAccountID),
			accountSnapshot:  strings.TrimSpace(payload.CPAMPAccountSnapshot),
			hasIdentity: strings.TrimSpace(payload.CPAMPProvider) != "" ||
				strings.TrimSpace(payload.CPAMPAccountID) != "" ||
				strings.TrimSpace(payload.CPAMPAccountSnapshot) != "",
		}
		if physicalName != "" || len(sourceIdentities) > 0 {
			mutation.lockAll = false
		}
	}
	return mutation, nil
}

func normalizeAuthFileSourceIdentities(payload []authFileSourceIdentityPayload) ([]cpaauthfiles.Identity, error) {
	identities := make([]cpaauthfiles.Identity, 0, len(payload))
	for _, item := range payload {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return nil, errors.New("auth file source identity name is required")
		}
		runtimeID := strings.TrimSpace(item.RuntimeID)
		if runtimeID == "" {
			return nil, errors.New("auth file source identity runtime_id is required")
		}
		identities = append(identities, cpaauthfiles.Identity{
			AuthFileName:      name,
			RuntimeID:         runtimeID,
			AuthIndex:         normalizeJSONScalarString(item.AuthIndex),
			Provider:          strings.TrimSpace(item.Provider),
			AccountIDSnapshot: strings.TrimSpace(item.AccountID),
			AccountSnapshot:   strings.TrimSpace(item.AccountSnapshot),
		})
	}
	return identities, nil
}

func normalizeJSONScalarString(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return strings.TrimSpace(typed.String())
	default:
		return ""
	}
}

func rewriteAuthFileStatusMutationRequest(r *http.Request, target cpaauthfiles.StatusMutationTarget, selector string) error {
	if r == nil || r.Body == nil {
		return errors.New("auth file status mutation body is required")
	}
	raw, err := readAndRestoreRequestBody(r, maxAuthFileMutationRequestBytes)
	if err != nil {
		return err
	}
	payload := make(map[string]json.RawMessage)
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("decode auth file status mutation: %w", err)
	}
	selector = strings.TrimSpace(selector)
	if selector == "" {
		selector = target.Selector
	}
	payload["name"], _ = json.Marshal(selector)
	delete(payload, "cpamp_source_file")
	delete(payload, "cpamp_physical_name")
	delete(payload, "cpamp_runtime_id")
	delete(payload, "cpamp_provider")
	delete(payload, "cpamp_account_id")
	delete(payload, "cpamp_account_snapshot")
	delete(payload, "cpamp_source_identities")
	if authIndex := strings.TrimSpace(target.File.AuthIndex); authIndex != "" {
		payload["auth_index"], _ = json.Marshal(authIndex)
	} else {
		delete(payload, "auth_index")
	}
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode auth file status mutation: %w", err)
	}
	restoreRequestBody(r, rewritten)
	return nil
}

func rewriteAuthFileFieldsMutationRequest(r *http.Request, selector string) error {
	if r == nil || r.Body == nil {
		return errors.New("auth file fields mutation body is required")
	}
	raw, err := readAndRestoreRequestBody(r, maxAuthFileMutationRequestBytes)
	if err != nil {
		return err
	}
	payload := make(map[string]json.RawMessage)
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("decode auth file fields mutation: %w", err)
	}
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return errors.New("auth file fields mutation selector is required")
	}
	payload["name"], _ = json.Marshal(selector)
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode auth file fields mutation: %w", err)
	}
	restoreRequestBody(r, rewritten)
	return nil
}

func readMultipartAuthFileNames(r *http.Request) ([]string, error) {
	if r.Body == nil {
		return nil, nil
	}
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || params["boundary"] == "" {
		return nil, nil
	}
	raw, err := readAndRestoreRequestBody(r, maxAuthFileMutationRequestBytes)
	if err != nil {
		return nil, err
	}
	reader := multipart.NewReader(bytes.NewReader(raw), params["boundary"])
	fileNames := make([]string, 0)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		fileNames = append(fileNames, part.FileName())
		_ = part.Close()
	}
	return normalizeFileNames(fileNames), nil
}

func successfulAuthFileOwnershipMutation(response *http.Response, mutation authFileOwnershipMutation) (authFileOwnershipMutation, error) {
	if !mutation.clearAll && len(mutation.fileNames) == 0 && len(mutation.ownershipTargets) == 0 {
		return mutation, nil
	}
	if response == nil || response.Body == nil {
		return mutation, nil
	}
	contentEncoding := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding")))
	if contentEncoding != "" && contentEncoding != "identity" {
		return mutation, nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxAuthFileMutationResponseBytes+1))
	if err != nil {
		return authFileOwnershipMutation{}, err
	}
	if int64(len(raw)) > maxAuthFileMutationResponseBytes {
		return authFileOwnershipMutation{}, errAuthFileMutationBodyTooLarge
	}
	response.Body.Close()
	response.Body = io.NopCloser(bytes.NewReader(raw))
	response.ContentLength = int64(len(raw))

	var payload struct {
		Status   string   `json:"status"`
		OK       *bool    `json:"ok"`
		Success  *bool    `json:"success"`
		Deleted  *int     `json:"deleted"`
		Uploaded *int     `json:"uploaded"`
		Files    []string `json:"files"`
		Failed   []struct {
			Name string `json:"name"`
		} `json:"failed"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return mutation, nil
	}
	status := strings.ToLower(strings.TrimSpace(payload.Status))
	if (payload.OK != nil && !*payload.OK) ||
		(payload.Success != nil && !*payload.Success) ||
		status == "error" || status == "failed" ||
		(payload.Deleted != nil && *payload.Deleted <= 0) ||
		(payload.Uploaded != nil && *payload.Uploaded <= 0) {
		return authFileOwnershipMutation{}, nil
	}
	if fileNames := normalizeFileNames(payload.Files); len(fileNames) > 0 {
		return filterAuthFileOwnershipMutation(mutation, fileNames), nil
	}
	failed := make(map[string]struct{}, len(payload.Failed))
	for _, item := range payload.Failed {
		if fileName := strings.TrimSpace(item.Name); fileName != "" {
			failed[fileName] = struct{}{}
		}
	}
	if len(failed) == 0 {
		if status == "ok" || status == "success" ||
			(payload.OK != nil && *payload.OK) ||
			(payload.Success != nil && *payload.Success) {
			return mutation, nil
		}
		confirmedCount := -1
		if payload.Deleted != nil {
			confirmedCount = *payload.Deleted
		}
		if payload.Uploaded != nil {
			if confirmedCount >= 0 && confirmedCount != *payload.Uploaded {
				return mutation, nil
			}
			confirmedCount = *payload.Uploaded
		}
		if confirmedCount > 0 && confirmedCount == len(normalizeFileNames(mutation.fileNames)) {
			return mutation, nil
		}
		return mutation, nil
	}
	succeeded := make([]string, 0, len(mutation.fileNames))
	for _, fileName := range mutation.fileNames {
		if _, ok := failed[fileName]; !ok {
			succeeded = append(succeeded, fileName)
		}
	}
	return filterAuthFileOwnershipMutation(mutation, succeeded), nil
}

func filterAuthFileOwnershipMutation(mutation authFileOwnershipMutation, fileNames []string) authFileOwnershipMutation {
	normalizedFileNames := normalizeFileNames(fileNames)
	allowed := make(map[string]struct{}, len(normalizedFileNames))
	for _, fileName := range normalizedFileNames {
		allowed[fileName] = struct{}{}
	}
	filteredFileNames := make([]string, 0, len(mutation.fileNames))
	for _, fileName := range mutation.fileNames {
		fileName = strings.TrimSpace(fileName)
		if _, ok := allowed[fileName]; ok {
			filteredFileNames = append(filteredFileNames, fileName)
		}
	}
	ownershipTargets := make([]model.CodexInspectionDisableOwnershipTarget, 0, len(mutation.ownershipTargets))
	for _, target := range mutation.ownershipTargets {
		if _, ok := allowed[strings.TrimSpace(target.FileName)]; ok {
			ownershipTargets = append(ownershipTargets, target)
		}
	}
	return authFileOwnershipMutation{
		fileNames:        filteredFileNames,
		ownershipTargets: ownershipTargets,
		statusMutation:   mutation.statusMutation,
	}
}

func ownershipFileNames(items []store.CodexInspectionDisableOwnership) []string {
	fileNames := make([]string, 0, len(items))
	for _, item := range items {
		fileNames = append(fileNames, item.FileName)
	}
	return normalizeFileNames(fileNames)
}

func ownershipItemsNotMutated(items []store.CodexInspectionDisableOwnership, mutation authFileOwnershipMutation) []store.CodexInspectionDisableOwnership {
	if mutation.clearAll {
		return nil
	}
	result := make([]store.CodexInspectionDisableOwnership, 0, len(items))
	for _, item := range items {
		if !authFileOwnershipMutationMatchesItem(mutation, item) {
			result = append(result, item)
		}
	}
	return result
}

func authFileOwnershipMutationMatchesItem(mutation authFileOwnershipMutation, item store.CodexInspectionDisableOwnership) bool {
	for _, fileName := range mutation.fileNames {
		if item.FileName == fileName {
			return true
		}
	}
	for _, target := range mutation.ownershipTargets {
		if ownershipTargetMatchesItem(target, item) {
			return true
		}
	}
	return false
}

func ownershipTargetMatchesItem(target model.CodexInspectionDisableOwnershipTarget, item store.CodexInspectionDisableOwnership) bool {
	if strings.TrimSpace(target.FileName) != strings.TrimSpace(item.FileName) {
		return false
	}
	if target.Provider != nil {
		itemProvider := strings.TrimSpace(item.Provider)
		if itemProvider != "" && normalizeOwnershipProvider(*target.Provider) != normalizeOwnershipProvider(itemProvider) {
			return false
		}
	}
	if target.AuthIndex != nil {
		authIndex := strings.TrimSpace(*target.AuthIndex)
		itemAuthIndex := strings.TrimSpace(item.AuthIndex)
		if authIndex == "" {
			if itemAuthIndex != "" {
				return false
			}
		} else if itemAuthIndex != "" && itemAuthIndex != authIndex {
			return false
		}
	}
	if target.AccountID != nil {
		accountID := strings.TrimSpace(*target.AccountID)
		itemAccountID := strings.TrimSpace(item.AccountID)
		if accountID == "" {
			if itemAccountID != "" {
				return false
			}
		} else if itemAccountID != "" {
			if itemAccountID != accountID {
				return false
			}
		} else if itemAccountSnapshot := strings.TrimSpace(item.AccountSnapshot); itemAccountSnapshot != "" {
			if target.AccountSnapshot == nil || strings.TrimSpace(*target.AccountSnapshot) != itemAccountSnapshot {
				return false
			}
		}
	}
	if (target.AccountID == nil || strings.TrimSpace(*target.AccountID) == "") && target.AccountSnapshot != nil {
		accountSnapshot := strings.TrimSpace(*target.AccountSnapshot)
		itemAccountSnapshot := strings.TrimSpace(item.AccountSnapshot)
		if accountSnapshot == "" {
			if itemAccountSnapshot != "" {
				return false
			}
		} else if itemAccountSnapshot != "" && itemAccountSnapshot != accountSnapshot {
			return false
		}
	}
	return true
}

func normalizeOwnershipProvider(value string) string {
	provider := strings.ToLower(strings.TrimSpace(value))
	provider = strings.ReplaceAll(provider, "_", "-")
	switch provider {
	case "", "codex":
		return "codex"
	case "x-ai", "grok":
		return "xai"
	default:
		return provider
	}
}

func readAndRestoreRequestBody(r *http.Request, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if errClose := r.Body.Close(); errClose != nil {
		return nil, errClose
	}
	if int64(len(raw)) > limit {
		return nil, errAuthFileMutationBodyTooLarge
	}
	restoreRequestBody(r, raw)
	return raw, nil
}

func normalizeFileNames(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		fileName := strings.TrimSpace(value)
		if fileName == "" {
			continue
		}
		if _, ok := seen[fileName]; ok {
			continue
		}
		seen[fileName] = struct{}{}
		result = append(result, fileName)
	}
	return result
}

func restoreRequestBody(r *http.Request, body []byte) {
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	bodyCopy := append([]byte(nil), body...)
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(bodyCopy)), nil
	}
}

func rewriteCodexInviteOrigin(header http.Header, target *url.URL) {
	if header == nil || target == nil || header.Get(codexInviteOriginHeader) == "" {
		return
	}
	origin := target.Scheme + "://" + target.Host
	if origin == "://" {
		return
	}
	header.Set(codexInviteOriginHeader, origin)
}

func rewritePluginManagementOriginBody(r *http.Request, target *url.URL) error {
	if r == nil || r.Body == nil || target == nil || !isJSONContentType(r.Header.Get("Content-Type")) {
		return nil
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if errClose := r.Body.Close(); errClose != nil {
		return errClose
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		restoreRequestBody(r, raw)
		return nil
	}

	var payload map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		restoreRequestBody(r, raw)
		return nil
	}
	if _, ok := payload[managementOriginJSONField]; !ok {
		restoreRequestBody(r, raw)
		return nil
	}
	origin := target.Scheme + "://" + target.Host
	if origin == "://" {
		restoreRequestBody(r, raw)
		return nil
	}
	encodedOrigin, errMarshal := json.Marshal(origin)
	if errMarshal != nil {
		restoreRequestBody(r, raw)
		return errMarshal
	}
	payload[managementOriginJSONField] = encodedOrigin
	next, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		restoreRequestBody(r, raw)
		return errMarshal
	}
	restoreRequestBody(r, next)
	return nil
}

func isJSONContentType(value string) bool {
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	return contentType == "application/json" || strings.HasSuffix(contentType, "+json")
}

func (s *Service) ProxyModelList(w http.ResponseWriter, r *http.Request, writeError func(http.ResponseWriter, int, error), methodNotAllowed func(http.ResponseWriter)) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !isModelListPath(r.URL.Path) {
		writeError(w, http.StatusNotFound, errors.New("model list proxy path must be /v1/models"))
		return
	}
	setup, ok, err := s.resolveSetup(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeError(w, http.StatusPreconditionRequired, errors.New("usage service is not configured"))
		return
	}
	target, err := url.Parse(setup.CPAUpstreamURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		writeError(w, http.StatusBadGateway, err)
	}
	proxy.ServeHTTP(w, r)
}

func isModelListPath(path string) bool {
	cleaned := strings.TrimRight(path, "/")
	return cleaned == "/v1/models" || cleaned == "/models"
}

func isManagementPath(path string) bool {
	if isStrictManagementPath(path) {
		return true
	}
	return IsCPAPluginResourcePath(path)
}

func isStrictManagementPath(path string) bool {
	return path == "/v0/management" || strings.HasPrefix(path, "/v0/management/")
}

func IsCPAPluginManagementPath(path string) bool {
	cleaned := strings.TrimRight(path, "/")
	if !strings.HasPrefix(cleaned, cpaManagementPrefix+"/") {
		return false
	}
	rest := strings.TrimPrefix(cleaned, cpaManagementPrefix+"/")
	head, _, _ := strings.Cut(rest, "/")
	if head == "" {
		return false
	}
	_, reserved := cpaBuiltinManagementPathHeads[head]
	return !reserved
}

func IsCPAPluginResourcePath(path string) bool {
	cleaned := strings.TrimRight(path, "/")
	return cleaned == cpaPluginResourcePrefix || strings.HasPrefix(cleaned, cpaPluginResourcePrefix+"/")
}

func (s *Service) resolveSetup(ctx context.Context) (store.Setup, bool, error) {
	return s.managerConfigService.ResolveSetup(ctx)
}
