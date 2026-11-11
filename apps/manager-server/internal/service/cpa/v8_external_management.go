package cpa

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// ErrManagementRejected is CPA's own 401 to an External management request: the
// credential this installation persisted no longer authenticates. It is a typed
// blocker, never a retry reason. An External CPA is administered by the user, so
// the only way back is the user rebinding the connection; guessing, resending or
// rebinding automatically would write to a CPA this Manager no longer proves it
// owns.
var ErrManagementRejected = errors.New("external cpa management credential rejected")

// ErrManagementClientForbidden reports CPA's own 403 to an External management
// request. CPA answers 403, not 401, when it refuses the client rather than the
// credential: the address is banned after too many failed authentications, or
// remote management is disabled for it. It is the same blocker — the Manager's
// credential is not accepted — so it wraps ErrManagementRejected, and it is
// separate only because a ban expires: the page can tell the administrator to
// retry the same credential after the ban window instead of asking for a new one.
var ErrManagementClientForbidden = fmt.Errorf("%w: %w", ErrManagementRejected, errors.New("cpa refused this management client"))

// ErrExternalTargetMoved reports a committed External target that no longer names
// the connection this Manager holds: the address the user configured changed after
// the installation was committed. It is a typed blocker, not an unavailable
// target — the installation exists — and only a rebind can reach it again, so no
// surface may treat it as a retry or as a missing proof.
var ErrExternalTargetMoved = errors.New("external cpa target moved")

// ExternalKeySnapshot is the user's CPA client-key list together with the digest
// of the configuration that list belongs to. An External CPA owns its own
// posture, key naming and extra credentials, so the only facts this Manager may
// compare are membership and the whole-document digest: there is no managed
// template to prove here.
type ExternalKeySnapshot struct {
	Keys         []string
	SourceSHA256 string
}

// ExternalKeySnapshot reads the External CPA client-key list and the config
// digest. Both reads authenticate with the persisted management key; a 401 is
// the typed ErrManagementRejected blocker rather than an unavailable proof.
func (t *Transport) ExternalKeySnapshot(ctx context.Context, management string) (ExternalKeySnapshot, error) {
	if t == nil {
		return ExternalKeySnapshot{}, ErrProofUnavailable
	}
	if gate := ExternalSavedCredentialGateFromContext(ctx); gate != nil {
		var out ExternalKeySnapshot
		err := gate(ctx, func(requestCtx context.Context, target ExternalSavedCredentialTarget) error {
			if err := t.verifyExternalTarget(target); err != nil {
				return err
			}
			var readErr error
			out, readErr = t.externalKeySnapshotDirect(requestCtx, target.ManagementKey)
			return readErr
		})
		return out, err
	}
	return t.externalKeySnapshotDirect(ctx, management)
}

func (t *Transport) externalKeySnapshotDirect(ctx context.Context, management string) (ExternalKeySnapshot, error) {
	var snapshot ExternalKeySnapshot
	if t == nil || !validManagementKey(management) {
		return snapshot, ErrProofUnavailable
	}
	code, body, err := t.request(WithoutExternalHeaderRelease(ctx), http.MethodGet, "/v8/management/config/access/api-keys", management, nil)
	if err != nil {
		return snapshot, ErrProofUnavailable
	}
	switch code {
	case http.StatusUnauthorized:
		return snapshot, ErrManagementRejected
	case http.StatusForbidden:
		return snapshot, ErrManagementClientForbidden
	case http.StatusOK:
	default:
		return snapshot, ErrSourceConflict
	}
	if json.Unmarshal(body, &snapshot.Keys) != nil {
		return snapshot, ErrSourceConflict
	}
	for _, key := range snapshot.Keys {
		if !validExternalKey(key) {
			return snapshot, ErrSourceConflict
		}
	}
	code, body, err = t.request(ctx, http.MethodGet, "/v8/management/config.yaml", management, nil)
	if err != nil {
		return snapshot, ErrProofUnavailable
	}
	switch code {
	case http.StatusUnauthorized:
		return snapshot, ErrManagementRejected
	case http.StatusForbidden:
		return snapshot, ErrManagementClientForbidden
	case http.StatusOK:
	default:
		return snapshot, ErrSourceConflict
	}
	digest := sha256.Sum256(body)
	snapshot.SourceSHA256 = hex.EncodeToString(digest[:])
	return snapshot, nil
}

// ExternalReadConfigDocument reads the External CPA configuration document and
// distinguishes an authentication refusal (401/403) from an unreachable or
// invalid document, so callers can record the typed credential blocker instead
// of burning the CPA's five-strike lockout window.
func (t *Transport) ExternalReadConfigDocument(ctx context.Context, management string) (map[string]any, error) {
	if t == nil {
		return nil, ErrProofUnavailable
	}
	if gate := ExternalSavedCredentialGateFromContext(ctx); gate != nil {
		var out map[string]any
		err := gate(ctx, func(requestCtx context.Context, target ExternalSavedCredentialTarget) error {
			if err := t.verifyExternalTarget(target); err != nil {
				return err
			}
			var readErr error
			out, readErr = t.externalReadConfigDocumentDirect(requestCtx, target.ManagementKey)
			return readErr
		})
		return out, err
	}
	return t.externalReadConfigDocumentDirect(ctx, management)
}

func (t *Transport) externalReadConfigDocumentDirect(ctx context.Context, management string) (map[string]any, error) {
	if t == nil || !validManagementKey(management) {
		return nil, ErrProofUnavailable
	}
	code, body, err := t.request(ctx, http.MethodGet, "/v8/management/config", management, nil)
	if err != nil {
		return nil, ErrProofUnavailable
	}
	switch code {
	case http.StatusUnauthorized:
		return nil, ErrManagementRejected
	case http.StatusForbidden:
		return nil, ErrManagementClientForbidden
	case http.StatusOK:
	default:
		return nil, ErrProofUnavailable
	}
	return DecodeConfigDocument(body)
}

// ExternalWriteKeySet writes the whole client-key list of an External CPA. It
// sends exactly one request and never retries: an acknowledged 401 is the typed
// blocker, and anything else leaves the effect unknown for the caller's
// read-back, exactly like the managed destination.
func (t *Transport) ExternalWriteKeySet(ctx context.Context, management string, keys []string) error {
	if t == nil || len(keys) == 0 {
		return ErrSourceConflict
	}
	for i, key := range keys {
		if !validExternalKey(key) || slices.Contains(keys[:i], key) {
			return ErrSourceConflict
		}
	}
	body, err := json.Marshal(keys)
	if err != nil {
		return ErrProofUnavailable
	}
	if gate := ExternalSavedCredentialGateFromContext(ctx); gate != nil {
		return gate(ctx, func(requestCtx context.Context, target ExternalSavedCredentialTarget) error {
			if err := t.verifyExternalTarget(target); err != nil {
				return err
			}
			return t.externalWriteKeySetDirect(requestCtx, target.ManagementKey, body)
		})
	}
	if !validManagementKey(management) {
		return ErrSourceConflict
	}
	return t.externalWriteKeySetDirect(ctx, management, body)
}

func (t *Transport) externalWriteKeySetDirect(ctx context.Context, management string, body []byte) error {
	if !validManagementKey(management) {
		return ErrSourceConflict
	}
	code, _, err := t.request(ctx, http.MethodPut, "/v8/management/config/access/api-keys", management, body)
	if err != nil {
		return ErrApplyUnknown
	}
	switch code {
	case http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusUnauthorized:
		return ErrManagementRejected
	case http.StatusForbidden:
		// A banned or remote-management-disabled client is refused before the
		// list is read, so nothing was written: the effect is not unknown.
		return ErrManagementClientForbidden
	}
	return ErrApplyUnknown
}

func (t *Transport) verifyExternalTarget(target ExternalSavedCredentialTarget) error {
	normalized, err := NormalizeV8BaseURL(target.BaseURL)
	if err != nil || normalized != t.endpoint {
		return ErrExternalTargetMoved
	}
	return nil
}

// ExternalConfigTransport adapts Transport for External gateway configuration
// reads and writes so 401/403 responses surface typed credential rejections
// and participate in ExternalSavedCredentialGate without altering Managed or
// Private Transport error mappings.
type ExternalConfigTransport struct {
	*Transport
}

// NewExternalConfigTransport constructs an External configuration transport
// targeting endpoint.
func NewExternalConfigTransport(endpoint string) *ExternalConfigTransport {
	return &ExternalConfigTransport{Transport: NewTransportForEndpoint(endpoint)}
}

func (t *ExternalConfigTransport) ReadConfigDocument(ctx context.Context, management string) (map[string]any, error) {
	if t == nil || t.Transport == nil {
		return nil, ErrProofUnavailable
	}
	return t.Transport.ExternalReadConfigDocument(ctx, management)
}

func (t *ExternalConfigTransport) PatchConfigPath(ctx context.Context, management, prefix string, content []byte) error {
	if prefix == "" || len(content) == 0 {
		return ErrProofUnavailable
	}
	return t.writeExternalConfigPath(ctx, http.MethodPatch, prefix, management, content)
}

func (t *ExternalConfigTransport) PutConfigValue(ctx context.Context, management, path string, content []byte) error {
	if path == "" || len(content) == 0 {
		return ErrProofUnavailable
	}
	return t.writeExternalConfigPath(ctx, http.MethodPut, path, management, content)
}

func (t *ExternalConfigTransport) DeleteConfigPath(ctx context.Context, management, path string) error {
	if path == "" {
		return ErrProofUnavailable
	}
	return t.writeExternalConfigPath(ctx, http.MethodDelete, path, management, nil)
}

func (t *ExternalConfigTransport) writeExternalConfigPath(ctx context.Context, method, path, management string, content []byte) error {
	if t == nil || t.Transport == nil {
		return ErrProofUnavailable
	}
	if gate := ExternalSavedCredentialGateFromContext(ctx); gate != nil {
		return gate(ctx, func(requestCtx context.Context, target ExternalSavedCredentialTarget) error {
			if err := t.Transport.verifyExternalTarget(target); err != nil {
				return err
			}
			return t.writeExternalConfigPathDirect(requestCtx, method, path, target.ManagementKey, content)
		})
	}
	return t.writeExternalConfigPathDirect(ctx, method, path, management, content)
}

func (t *ExternalConfigTransport) writeExternalConfigPathDirect(ctx context.Context, method, path, management string, content []byte) error {
	if !validManagementKey(management) {
		return ErrProofUnavailable
	}
	endpoint := configPathEndpoint(path)
	code, body, err := t.Transport.request(ctx, method, endpoint, management, content)
	if err != nil {
		return ErrApplyUnknown
	}
	switch code {
	case http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusUnauthorized:
		return ErrManagementRejected
	case http.StatusForbidden:
		return ErrManagementClientForbidden
	}
	rejection := parseConfigWriteError(code, body)
	if rejection.Definite() {
		return rejection
	}
	return ErrApplyUnknown
}

func (t *ExternalConfigTransport) ReadConfigFieldValue(ctx context.Context, management, path string) (string, any, error) {
	if t == nil || t.Transport == nil {
		return "unknown", nil, ErrProofUnavailable
	}
	if gate := ExternalSavedCredentialGateFromContext(ctx); gate != nil {
		var presence string
		var val any
		err := gate(ctx, func(requestCtx context.Context, target ExternalSavedCredentialTarget) error {
			if err := t.Transport.verifyExternalTarget(target); err != nil {
				return err
			}
			var readErr error
			presence, val, readErr = t.readExternalConfigFieldValueDirect(requestCtx, target.ManagementKey, path)
			return readErr
		})
		return presence, val, err
	}
	return t.readExternalConfigFieldValueDirect(ctx, management, path)
}

func (t *ExternalConfigTransport) readExternalConfigFieldValueDirect(ctx context.Context, management, path string) (string, any, error) {
	endpoint, err := ConfigLeafEndpoint(path)
	if err != nil || !validManagementKey(management) {
		return "unknown", nil, ErrProofUnavailable
	}
	code, body, err := t.Transport.request(ctx, http.MethodGet, endpoint, management, nil)
	if err != nil {
		return "unknown", nil, err
	}
	switch code {
	case http.StatusUnauthorized:
		return "unknown", nil, ErrManagementRejected
	case http.StatusForbidden:
		return "unknown", nil, ErrManagementClientForbidden
	case http.StatusNotFound:
		return "missing", nil, nil
	case http.StatusOK:
		trimmed := bytes.TrimSpace(body)
		if bytes.Equal(trimmed, []byte("null")) {
			return "null", nil, nil
		}
		value, parseErr := ParseConfigLeafValue(path, trimmed)
		if parseErr != nil {
			return "unknown", nil, ErrSourceConflict
		}
		return "present", value, nil
	default:
		return "unknown", nil, ErrProofUnavailable
	}
}

// ExternalModelSurface proves the gateway behind the External CPA serves the
// model list, over the same transport every other External read and write uses.
//
// The probe carries a client credential, so where it goes is as much a security
// fact as what it answers: a probe built on the default transport would honour
// HTTP_PROXY and hand that credential to whatever the environment names. This
// method therefore refuses to exist outside this Transport, whose client is
// direct, unbuffered by a proxy, unbounded in no dimension and redirect-free.
//
// The client key is optional: a CPA with no client keys serves the list
// anonymously. A non-200 answer, an unparsable body or an empty list is
// ErrProofUnavailable; the caller maps that onto the External vocabulary.
func (t *Transport) ExternalModelSurface(ctx context.Context, clientKey string) error {
	if t == nil || t.client == nil || (clientKey != "" && !validExternalKey(clientKey)) {
		return ErrProofUnavailable
	}
	code, body, err := t.request(ctx, http.MethodGet, "/v1/models", clientKey, nil)
	if err != nil || code != http.StatusOK {
		return ErrProofUnavailable
	}
	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &models) != nil {
		return ErrProofUnavailable
	}
	for _, entry := range models.Data {
		if strings.TrimSpace(entry.ID) != "" {
			return nil
		}
	}
	return ErrProofUnavailable
}

// ManagedModelSurface proves a Managed CPA destination serves a well-formed
// /v1/models list to an imported client credential, mirroring the empty-allowed
// list predicate in VerifyV8Adoption (v8_adoption.go:57-65). An empty model
// catalogue is legitimate on a freshly imported Managed destination that has no
// provider models configured yet.
func (t *Transport) ManagedModelSurface(ctx context.Context, clientKey string) error {
	if t == nil || t.client == nil || !validExternalKey(clientKey) {
		return errors.New("destination model surface was not provable")
	}
	code, body, err := t.request(ctx, http.MethodGet, "/v1/models", clientKey, nil)
	if err != nil {
		return errors.New("destination model surface was unreachable")
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return errors.New("destination model surface rejected the client key")
	}
	if code != http.StatusOK {
		return fmt.Errorf("destination model surface answered with status %d", code)
	}
	var list struct {
		Object string            `json:"object"`
		Data   []json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &list) != nil || list.Object != "list" || list.Data == nil {
		return errors.New("destination model surface was not provable")
	}
	return nil
}

// validExternalKey accepts the client credentials a CPA v8 configuration stores:
// a printable, non-empty token. CPA itself mints arbitrary shapes, so this guard
// bounds what may be echoed into a write body; it never rewrites a value.
func validExternalKey(key string) bool {
	if key == "" || len(key) > 4096 {
		return false
	}
	for _, c := range key {
		if c < 0x21 || c == 0x7f {
			return false
		}
	}
	return true
}
