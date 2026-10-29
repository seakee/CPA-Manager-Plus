package cpa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
)

type KeySnapshot struct {
	Keys         []string
	SourceSHA256 string
	SourceID     string
	Generation   string
	Revision     uint64
}

// KeySnapshot checks the same exact private seed, allowing only its client list
// to differ. H1 uses Runtime raw bytes; the standalone legacy path retains the
// old CPA GET serialization fingerprint and must never share H1 permits.
func (t *Transport) KeySnapshot(ctx context.Context, management string) (KeySnapshot, error) {
	var result KeySnapshot
	var authority configObservation
	if t.h1 != nil {
		v, e := t.h1.Ready(ctx, t)
		if e != nil || v.State != "persisted" {
			return result, ErrSourceConflict
		}
		authority = v
	}
	code, body, err := t.request(ctx, http.MethodGet, "/v8/management/config", management, nil)
	if err != nil || code != 200 {
		return result, ErrProofUnavailable
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return result, ErrSourceConflict
	}
	posture := t.postureOf()
	var access map[string]json.RawMessage
	if json.Unmarshal(root["access"], &access) != nil || len(access) != 1 || json.Unmarshal(access["api-keys"], &result.Keys) != nil {
		return result, ErrSourceConflict
	}
	// The list belongs to the destination's own strategy: a Manager-owned New
	// Managed destination starts with the install-generated key, so both canonical
	// shapes are legitimate projections; an adopted destination carries the
	// source's opaque list up to the adoption bound.
	if !posture.validKeys(result.Keys) {
		return result, ErrSourceConflict
	}
	root["access"] = json.RawMessage(`{"api-keys":[]}`)
	empty, _ := json.Marshal(root)
	if t.h1 != nil {
		var e error
		empty, e = t.postInstallProjection(ctx, empty)
		if e != nil {
			return result, e
		}
	}
	valid := posture.matches(empty)
	if t.providerSeed != nil {
		seed, e := t.providerSeed()
		valid = e == nil && privateProviderConfig(empty, seed, "")
	}
	if !valid {
		return result, ErrSourceConflict
	}
	code, body, err = t.request(ctx, http.MethodGet, "/v8/management/config/access/api-keys", management, nil)
	var keys []string
	if err != nil || code != 200 || json.Unmarshal(body, &keys) != nil || !slices.Equal(keys, result.Keys) {
		return result, ErrSourceConflict
	}
	if t.h1 != nil {
		v, e := t.h1.Ready(ctx, t)
		if e != nil || h1Digest(v) != h1Digest(authority) {
			return result, ErrSourceConflict
		}
		result.SourceSHA256 = v.SHA256
		result.SourceID = v.SourceID
		result.Generation = v.Generation
		result.Revision = v.Revision
		return result, nil
	}
	code, body, err = t.request(ctx, http.MethodGet, "/v8/management/config.yaml", management, nil)
	if err != nil || code != 200 {
		return result, ErrSourceConflict
	}
	h := sha256.Sum256(body)
	result.SourceSHA256 = hex.EncodeToString(h[:])
	return result, nil
}

// WriteKeySet is called only after a durable per-step claim and a current Host
// source/writer grant. It sends one request, never retries, and never clears Keys.
func (t *Transport) WriteKeySet(ctx context.Context, management string, keys []string) error {
	// The writer accepts exactly what the snapshot policy of this destination
	// accepts, so a list the read would refuse can never be written.
	if !t.postureOf().validKeys(keys) {
		return ErrSourceConflict
	}
	body, _ := json.Marshal(keys)
	code, _, err := t.request(ctx, http.MethodPut, "/v8/management/config/access/api-keys", management, body)
	if err != nil || code != 200 {
		return ErrApplyUnknown
	}
	return nil
}

// SampleKeySet observes the real CPA auth middleware, including the revoked
// credential. This is independent of the persisted management projection.
func (t *Transport) SampleKeySet(ctx context.Context, accepted, rejected []string) (bool, error) {
	samples := []struct {
		key    string
		status int
	}{{"", 401}, {"ku-invalid-client-key", 401}}
	for _, key := range accepted {
		samples = append(samples, struct {
			key    string
			status int
		}{key, 200})
	}
	for _, key := range rejected {
		samples = append(samples, struct {
			key    string
			status int
		}{key, 401})
	}
	for _, sample := range samples {
		code, _, err := t.request(ctx, http.MethodGet, "/v1/models", sample.key, nil)
		if err != nil {
			return false, err
		}
		if code != sample.status {
			return false, nil
		}
	}
	return true, nil
}
