package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/cpaidentityinventory"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identitymutation"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const maxAPIKeyMutationInspectionBytes = 1024 * 1024
const postMutationObservationTimeout = 10 * time.Second

type apiKeyMutationShape struct {
	kind               ports.APIKeyMutationKind
	oldRaw, newRaw     string
	representationOnly bool
}

func mutationHash(raw string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	return hex.EncodeToString(sum[:])
}

func isAPIKeyMutationRequest(r *http.Request) bool {
	if r.URL.Path != "/v0/management/api-keys" {
		return false
	}
	switch r.Method {
	case http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func inspectAPIKeyMutation(r *http.Request) (apiKeyMutationShape, error) {
	if !isAPIKeyMutationRequest(r) {
		return apiKeyMutationShape{}, nil
	}
	if r.Method == http.MethodDelete {
		query := r.URL.Query()
		if _, indexed := query["index"]; indexed {
			return apiKeyMutationShape{}, nil
		}
		values, hasValue := query["value"]
		if !hasValue {
			return apiKeyMutationShape{}, nil
		}
		if len(values) != 1 {
			return apiKeyMutationShape{}, errors.New("ambiguous API-key delete value")
		}
		return apiKeyMutationShape{kind: ports.APIKeyMutationDelete, oldRaw: values[0]}, nil
	}
	if r.Method != http.MethodPatch || r.Body == nil {
		return apiKeyMutationShape{}, nil
	}
	originalBody := r.Body
	body, err := io.ReadAll(io.LimitReader(originalBody, maxAPIKeyMutationInspectionBytes+1))
	if err != nil {
		return apiKeyMutationShape{}, errors.New("read API-key mutation request")
	}
	r.Body = struct {
		io.Reader
		io.Closer
	}{Reader: io.MultiReader(bytes.NewReader(body), originalBody), Closer: originalBody}
	if len(body) > maxAPIKeyMutationInspectionBytes {
		return apiKeyMutationShape{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return apiKeyMutationShape{}, nil
	}
	fields := make(map[string]string, 2)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return apiKeyMutationShape{}, nil
		}
		key, ok := keyToken.(string)
		if !ok || (key != "old" && key != "new") {
			return apiKeyMutationShape{}, nil
		}
		if _, duplicate := fields[key]; duplicate {
			return apiKeyMutationShape{}, nil
		}
		var value string
		if err := decoder.Decode(&value); err != nil {
			return apiKeyMutationShape{}, nil
		}
		fields[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return apiKeyMutationShape{}, nil
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return apiKeyMutationShape{}, nil
	}
	oldRaw, oldPresent := fields["old"]
	newRaw, newPresent := fields["new"]
	if len(fields) != 2 || !oldPresent || !newPresent {
		return apiKeyMutationShape{}, nil
	}
	return apiKeyMutationShape{kind: ports.APIKeyMutationRotate, oldRaw: oldRaw,
		newRaw: newRaw, representationOnly: mutationHash(oldRaw) == mutationHash(newRaw)}, nil
}

// prepareAPIKeyMutation runs after the normal proxy connection was resolved.
// The closure retains raw values only in this proxy/CPA transport boundary.
func (s *Service) prepareAPIKeyMutation(ctx context.Context, setup store.Setup, r *http.Request) (string, error) {
	if s.apiKeyMutations == nil || !isAPIKeyMutationRequest(r) {
		return "", nil
	}
	shape, err := inspectAPIKeyMutation(r)
	if err != nil {
		return "", err
	}
	if err := s.apiKeyMutations.CheckAvailable(ctx); err != nil {
		return "", err
	}
	if shape.kind == "" || shape.representationOnly {
		return "", nil
	}
	if strings.TrimSpace(shape.oldRaw) == "" ||
		(shape.kind == ports.APIKeyMutationRotate && strings.TrimSpace(shape.newRaw) == "") {
		return "", identitymutation.ErrPreflight
	}
	return s.apiKeyMutations.Prepare(ctx, shape.kind, func(fetchCtx context.Context) (ports.APIKeyMutationEvidence, error) {
		return cpaidentityinventory.FetchMutationEvidence(fetchCtx,
			setup.CPAUpstreamURL, setup.ManagementKey, shape.oldRaw, shape.newRaw)
	})
}

type apiKeyMutationTransport struct {
	base          http.RoundTripper
	mutations     *identitymutation.Service
	intentID      string
	baseURL       string
	managementKey string
}

func (t apiKeyMutationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, transportErr := t.base.RoundTrip(request)
	ctx, cancel := context.WithTimeout(context.Background(), postMutationObservationTimeout)
	defer cancel()
	if err := t.mutations.MarkForwardComplete(ctx, t.intentID); err != nil {
		if response != nil {
			response.Body.Close()
		}
		return nil, identitymutation.ErrMutationOutcomeUnknown
	}
	outcome, err := t.mutations.Observe(ctx, t.intentID, t.baseURL, t.managementKey)
	if err != nil || outcome == ports.APIKeyMutationUnknown {
		if response != nil {
			response.Body.Close()
		}
		return nil, identitymutation.ErrMutationOutcomeUnknown
	}
	if transportErr != nil {
		if response != nil {
			response.Body.Close()
		}
		return nil, errors.New("CPA API-key mutation transport failed")
	}
	return response, nil
}
