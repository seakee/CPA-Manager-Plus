package cpa

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
)

const PrivateKeyArtifact = privateCPAArtifact
const PrivateKeyRuntime = privateRuntimeIdentity
const privateSyntheticProvider = `{"openai-compatibility":[{"name":"s1-synthetic","base-url":"http://127.0.0.1:18321/v1","keys":[{"api-key":"s1-synthetic-upstream-only","auth_index":"557ea5257c0a559d"}],"models":[{"name":"s1-proof-model","alias":"s1-proof-model"}]}]}`

// Configured reads the CPA management projection. It cannot prove disk durability
// or live data-plane authentication. The disposable host proof checks disk too.
func (t *Transport) Configured(ctx context.Context, management, secret string) (bool, error) {
	if t.configurationAuthority != nil {
		_, observed, e := t.configuration(ctx, "observe")
		if e != nil {
			return false, e
		}
		if t.h1 != nil && observed.H1 != nil && observed.H1.Parent != nil {
			return false, ErrApplyUnknown
		}
		if observed.State == "unknown" && observed.Operation.Scope == "initial-key" &&
			observed.Operation.ID == "s1-"+hashBytes([]byte(secret)) && observed.Operation.KeySHA256 == hashBytes([]byte(secret)) {
			if e := t.reconcilePrivateConfiguration(ctx, observed.Operation.ID); e != nil {
				return false, e
			}
		}
		if _, e := t.committedConfiguration(ctx); e != nil {
			return false, e
		}
	}
	if t.providerSeed != nil && !t.providerConfigured(ctx, management, secret) {
		return false, ErrSourceConflict
	}
	status, body, err := t.request(ctx, http.MethodGet, "/v8/management/config/access/api-keys", management, nil)
	if err != nil || status != 200 {
		return false, ErrProofUnavailable
	}
	var keys []string
	if json.Unmarshal(body, &keys) != nil || keys == nil {
		return false, ErrProofUnavailable
	}
	// One list, two postures. A New Managed destination is staged with exactly one
	// Client Key, so the installation's key is the whole list. An adopted
	// destination carries the source CPA's list instead, and the key this
	// installation was journaled with is one member of it: membership is the only
	// configured fact an adoption can state. Neither answer is a live
	// authentication proof; `AuthSweep` is what asks that.
	if t.postureOf().adopt {
		return slices.Contains(keys, secret), nil
	}
	return len(keys) == 1 && keys[0] == secret, nil
}

// AuthSweep samples the real CPA access middleware. The host's separate
// synthetic chat requests prove upstream arrival, without granting Ingress.
func (t *Transport) AuthSweep(ctx context.Context, secret string) (bool, error) {
	for _, sample := range []struct {
		key  string
		want int
	}{{"", 401}, {"s1-invalid-client-key", 401}, {secret, 200}} {
		status, _, err := t.request(ctx, http.MethodGet, "/v1/models", sample.key, nil)
		if err != nil {
			return false, err
		}
		if status != sample.want {
			return false, nil
		}
	}
	return true, nil
}
