package proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/cpaidentityinventory"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/credentialdeletemutation"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

var errCredentialDeleteNotApplied = errors.New("credential_delete_not_applied")

func supportedDeleteSources(target cpaauthfiles.DeleteMutationTarget) ([]string, error) {
	if len(target.AffectedFiles) == 0 {
		return nil, cpaauthfiles.ErrDeleteMutationScopeAmbiguous
	}
	sources := make([]string, 0, len(target.AffectedFiles))
	seen := make(map[string]struct{}, len(target.AffectedFiles))
	for _, file := range target.AffectedFiles {
		if file.RuntimeOnly {
			continue
		}
		id := strings.TrimSpace(file.ID)
		if id == "" || id != file.ID {
			return nil, cpaauthfiles.ErrDeleteMutationScopeAmbiguous
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, cpaauthfiles.ErrDeleteMutationScopeAmbiguous
		}
		seen[id] = struct{}{}
		sources = append(sources, id)
	}
	return sources, nil
}

func (s *Service) prepareCredentialDelete(ctx context.Context, setup store.Setup, r *http.Request,
	mutation authFileOwnershipMutation) (string, error) {
	if s.credentialDeletes == nil || r.Method != http.MethodDelete ||
		strings.TrimRight(r.URL.Path, "/") != "/v0/management/auth-files" ||
		mutation.deleteMutation == nil {
		return "", nil
	}
	deletion := mutation.deleteMutation
	sources, err := supportedDeleteSources(deletion.preparedTarget)
	if err != nil {
		return "", err
	}
	if len(sources) == 0 {
		return "", nil
	}
	physicalName := strings.TrimSpace(deletion.preparedTarget.File.Name)
	return s.credentialDeletes.Prepare(ctx, physicalName, sources, func(ctx context.Context) (string, []string, error) {
		target, selector, err := resolveVerifiedAuthFileDelete(ctx, setup, deletion)
		if err != nil || selector != deletion.forwardSelector {
			return "", nil, credentialdeletemutation.ErrPreflight
		}
		revalidated, err := supportedDeleteSources(target)
		if err != nil {
			return "", nil, err
		}
		return strings.TrimSpace(target.File.Name), revalidated, nil
	})
}

type credentialDeleteTransport struct {
	base          http.RoundTripper
	deletes       *credentialdeletemutation.Service
	intentID      string
	baseURL       string
	managementKey string
	outcome       *ports.CredentialDeleteOutcome
}

func (t credentialDeleteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, transportErr := t.base.RoundTrip(request)
	ctx, cancel := context.WithTimeout(context.Background(), postMutationObservationTimeout)
	defer cancel()
	markerErr := t.deletes.MarkForwardComplete(ctx, t.intentID)
	outcome, observeErr := t.deletes.Observe(ctx, t.intentID, func(observeCtx context.Context) ([]string, error) {
		observations, err := cpaidentityinventory.New(nil, nil).FetchCredentials(observeCtx, t.baseURL, t.managementKey)
		if err != nil {
			return nil, err
		}
		ids := make([]string, len(observations))
		for i, item := range observations {
			ids[i] = item.SourceAuthID
		}
		return ids, nil
	})
	if markerErr != nil || observeErr != nil {
		outcome = ports.CredentialDeleteUnknown
	}
	*t.outcome = outcome
	if transportErr != nil {
		if response != nil {
			response.Body.Close()
		}
		return nil, errors.New("CPA credential delete transport failed")
	}
	if response == nil {
		return nil, errors.New("CPA credential delete transport returned no response")
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		switch outcome {
		case ports.CredentialDeleteUnknown:
			response.Body.Close()
			return nil, credentialdeletemutation.ErrOutcomeUnknown
		case ports.CredentialDeleteNotApplied:
			response.Body.Close()
			return nil, errCredentialDeleteNotApplied
		}
	}
	return response, nil
}
