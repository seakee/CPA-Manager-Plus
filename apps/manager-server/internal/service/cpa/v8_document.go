package cpa

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"strings"
	"time"
)

// V8ConfigDocumentInvalid reports a /v8 response that cannot be a v8
// configuration document at all: a body that does not claim to be JSON, a
// truncated or unparsable body, a port outside the TCP range, or a field whose
// shape is not the one a v8 document carries.
//
// It is deliberately distinct from V8InvalidConfig. That one is a well-formed
// document declaring another generation, so it means "this CPA is not v8 yet"
// and only it may tell an operator to upgrade the CPA. This one means the CPA
// may already be v8 and the document — or a proxy rewriting the answer — is the
// problem, so sending the operator after a CPA upgrade would name the wrong
// cause.
const V8ConfigDocumentInvalid V8ReadError = "management_v8_config_document_invalid"

// ReadV8ConfigDocument is the document proof the v2 External connect step
// performs: one authenticated, direct, bounded GET of /v8/management/config,
// classified so that "another generation" and "not a document this Manager can
// write into" stay distinguishable.
//
// It exists beside ReadV8Config rather than inside it because ReadV8Config is in
// `SafeReadImplementationSHA256`'s embed set: its bytes are bound to the Fresh
// adoption acceptance record, so relaxing it would invalidate an accepted
// consumer until that record is re-emitted from a real run. The two readers do
// answer slightly different questions — adoption needs the resource projection
// and refuses a source with no client Key, while connect only needs to know
// whether the target serves a v8 document — and their verdicts are allowed to
// differ. The transport, the body envelope and the URL normalisation are shared,
// so this reader adds classification only. Do not merge the two without a
// maintainer re-acceptance.
//
// The projection drops config values for the same reason ReadV8Config does: a
// configuration may carry provider and client secrets. Nothing here mutates the
// target, follows a redirect, retries, or writes.
func ReadV8ConfigDocument(ctx context.Context, baseURL, managementKey string) (V8ConfigObservation, error) {
	baseURL, err := NormalizeV8BaseURL(baseURL)
	if err != nil {
		return V8ConfigObservation{}, err
	}
	if !validManagementKey(managementKey) {
		return V8ConfigObservation{}, V8InvalidConnection
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client, closeClient, err := directReadClient(ctx, baseURL)
	if err != nil {
		return V8ConfigObservation{}, err
	}
	defer closeClient()
	return readV8ConfigDocumentWithClient(ctx, client, baseURL, managementKey)
}

// readV8ConfigDocumentWithClient classifies one already-fetched document. The
// verdict order matters: the generation question is asked before the shape
// questions, because a document that says "I am not v8" has answered the
// generation question whatever else it contains.
func readV8ConfigDocumentWithClient(ctx context.Context, client *http.Client, baseURL, managementKey string) (V8ConfigObservation, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v8/management/config", nil)
	if err != nil {
		return V8ConfigObservation{}, V8InvalidConnection
	}
	request.Header.Set("Authorization", "Bearer "+managementKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity, gzip")
	request.Header.Set("Cache-Control", "no-store")
	response, err := client.Do(request)
	if err != nil {
		return V8ConfigObservation{}, V8ReadUnavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusGone:
		// The surface is absent, so the document question was never asked.
		return V8ConfigObservation{}, V8OwnerUpgradeRequired
	case http.StatusUnauthorized, http.StatusForbidden:
		return V8ConfigObservation{}, V8AuthenticationRejected
	case http.StatusOK:
	default:
		return V8ConfigObservation{}, V8ReadUnavailable
	}
	mediaType, parameters, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || len(parameters) > 1 ||
		(len(parameters) == 1 && !strings.EqualFold(parameters["charset"], "utf-8")) {
		// A proxy or rewriter in front of a v8 CPA answers with its own page.
		return V8ConfigObservation{}, V8ConfigDocumentInvalid
	}
	data, ok := readBoundedJSONBody(response)
	if !ok {
		return V8ConfigObservation{}, V8ConfigDocumentInvalid
	}
	// The fields are pointers so "absent" stays distinguishable from a zero
	// value: a missing config-version is a document this reader does not
	// understand, while config-version 7 is the CPA declaring its generation.
	var document struct {
		Version *int `json:"config-version"`
		Server  struct {
			Port *int `json:"port"`
		} `json:"server"`
		OAuth struct {
			AuthDir *string `json:"auth-dir"`
		} `json:"oauth"`
		Access struct {
			APIKeys json.RawMessage `json:"api-keys"`
		} `json:"access"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return V8ConfigObservation{}, V8ConfigDocumentInvalid
	}
	if document.Version == nil {
		return V8ConfigObservation{}, V8ConfigDocumentInvalid
	}
	if *document.Version != 8 {
		// A document that names another generation is the CPA's own statement,
		// so it is the only answer that may ask the operator to upgrade the CPA.
		return V8ConfigObservation{}, V8InvalidConfig
	}
	if document.Server.Port == nil || *document.Server.Port < 1 || *document.Server.Port > 65535 {
		return V8ConfigObservation{}, V8ConfigDocumentInvalid
	}
	// oauth.auth-dir is deliberately not required and may be empty: a v8 CPA may
	// serve its default auth directory without writing it into the document. A
	// value of the wrong shape fails the typed Unmarshal above and is reported as
	// a document error there.
	// access.api-keys is deliberately optional, and Go serialises a nil Key
	// slice as null rather than []. A v8 CPA with no client Key is still a v8
	// CPA; refusing it told the operator to upgrade a CPA that already was.
	// Only a list whose entries are not strings makes the document unusable,
	// because that is not the shape any v8 CPA serves.
	var clientKeys []json.RawMessage
	if len(document.Access.APIKeys) > 0 {
		if err := json.Unmarshal(document.Access.APIKeys, &clientKeys); err != nil {
			return V8ConfigObservation{}, V8ConfigDocumentInvalid
		}
		for _, raw := range clientKeys {
			var key string
			if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &key) != nil {
				return V8ConfigObservation{}, V8ConfigDocumentInvalid
			}
		}
	}
	return V8ConfigObservation{BaseURL: baseURL, ClientKeyCount: len(clientKeys)}, nil
}
