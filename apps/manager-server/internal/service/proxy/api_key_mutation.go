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
	"net/url"
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

// An empty kind with no error proves transport-only behavior. An inspection
// error means CPA might still perform an identity-bearing fallback: reject it.
func inspectAPIKeyMutation(r *http.Request) (apiKeyMutationShape, error) {
	if !isAPIKeyMutationRequest(r) {
		return apiKeyMutationShape{}, nil
	}
	if r.Method == http.MethodDelete {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			return apiKeyMutationShape{}, errors.New("ambiguous API-key delete request")
		}
		if _, hasValue := query["value"]; !hasValue {
			return apiKeyMutationShape{}, nil
		}
		if len(query) == 1 && len(query["value"]) == 1 {
			return apiKeyMutationShape{kind: ports.APIKeyMutationDelete, oldRaw: query["value"][0]}, nil
		}
		return apiKeyMutationShape{}, errors.New("ambiguous API-key delete request")
	}
	if r.Method != http.MethodPatch {
		return apiKeyMutationShape{}, nil
	}
	if r.Body == nil {
		return apiKeyMutationShape{}, errors.New("ambiguous API-key patch request")
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
		return apiKeyMutationShape{}, errors.New("API-key patch request too large to inspect")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return apiKeyMutationShape{}, errors.New("ambiguous API-key patch request")
	}
	fields := make(map[string]json.RawMessage, 2)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return apiKeyMutationShape{}, errors.New("ambiguous API-key patch request")
		}
		key, ok := keyToken.(string)
		if !ok || (key != "old" && key != "new" && key != "index" && key != "value") {
			return apiKeyMutationShape{}, errors.New("ambiguous API-key patch request")
		}
		if _, duplicate := fields[key]; duplicate {
			return apiKeyMutationShape{}, errors.New("ambiguous API-key patch request")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return apiKeyMutationShape{}, errors.New("ambiguous API-key patch request")
		}
		fields[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return apiKeyMutationShape{}, errors.New("ambiguous API-key patch request")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return apiKeyMutationShape{}, errors.New("ambiguous API-key patch request")
	}
	if len(fields) != 2 {
		return apiKeyMutationShape{}, errors.New("ambiguous API-key patch request")
	}
	if rawIndex, indexed := fields["index"]; indexed {
		rawValue, valued := fields["value"]
		var index int
		var value string
		if !valued || json.Unmarshal(rawIndex, &index) != nil || json.Unmarshal(rawValue, &value) != nil ||
			bytes.Equal(rawIndex, []byte("null")) || bytes.Equal(rawValue, []byte("null")) {
			return apiKeyMutationShape{}, errors.New("ambiguous API-key patch request")
		}
		return apiKeyMutationShape{}, nil
	}
	oldValue, oldPresent := fields["old"]
	newValue, newPresent := fields["new"]
	var oldRaw, newRaw string
	if !oldPresent || !newPresent || json.Unmarshal(oldValue, &oldRaw) != nil ||
		json.Unmarshal(newValue, &newRaw) != nil || bytes.Equal(oldValue, []byte("null")) ||
		bytes.Equal(newValue, []byte("null")) {
		return apiKeyMutationShape{}, errors.New("ambiguous API-key patch request")
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
	if shape.kind == "" || shape.representationOnly {
		return "", s.apiKeyMutations.CheckTransportAvailable(ctx)
	}
	if err := s.apiKeyMutations.CheckAvailable(ctx); err != nil {
		return "", err
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
