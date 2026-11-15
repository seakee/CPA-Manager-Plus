package cpa

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"strings"
	"time"
)

// LegacyManagedAdoptionSnapshot is the bounded, non-secret projection of a
// legacy-managed CPA v8 source read through its v0 compatibility surface. Only
// client API keys (the sole resource this profile replays) and stable resource
// category names escape this read; provider credentials, OAuth/auth contents,
// configuration values, usage and logs never leave the helper.
type LegacyManagedAdoptionSnapshot struct {
	APIKeys          []string
	ResourceSections []string
}

// LegacyReadError exposes stable failures only. Upstream bodies, URLs and
// credentials are never safe diagnostic text at the Fresh authority boundary.
type LegacyReadError string

func (e LegacyReadError) Error() string { return string(e) }

const (
	// LegacyOwnerNotLegacy marks a target whose v0 management surface is absent.
	// This adapter never falls back to the v8 probe.
	LegacyOwnerNotLegacy LegacyReadError = "legacy_management_source_not_legacy"
	// LegacySourceProfileUnsupported marks a present v0 surface whose declared
	// CPA version is not the one accepted legacy-managed v8 source profile (for
	// example any CPA release below v8, an unpinned v8 release, or a missing
	// version header). Body shape cannot identify the source version; only the
	// management version metadata can.
	LegacySourceProfileUnsupported LegacyReadError = "legacy_management_source_profile_unsupported"
	LegacyAuthenticationRejected   LegacyReadError = "legacy_management_authentication_rejected"
	LegacyReadUnavailable          LegacyReadError = "legacy_management_safe_read_unavailable"
	LegacyInvalidConfig            LegacyReadError = "legacy_management_config_invalid"
)

// AcceptedLegacyManagedVersion is the only legacy-managed source release
// accepted by the Fresh legacy transition. CPAMP v2 does not support CPA below
// v8; this is deliberately an exact v8 profile, not a version range: pre-v8
// releases (v6/v7) and unpinned v8 releases are refused fail-closed.
const AcceptedLegacyManagedVersion = "v8.0.55"

// acceptedLegacyManagedHeaderVersion is the value the pinned official CPA
// v8.0.55 release reports in X-CPA-VERSION (the release tag without its "v"
// prefix). The exact pinned Linux amd64 release bytes are bound below.
const acceptedLegacyManagedHeaderVersion = "8.0.55"

const (
	AcceptedLegacyManagedArchiveSHA256 = "4cd593251db85e50c820f42e6ea5cecdd05f902201d3dbdb025671d4f306a123"
	AcceptedLegacyManagedBinarySHA256  = "e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"
)

// legacySectionFields classifies only the top-level keys of the legacy-shaped
// v0 config projection into stable bounded categories. Values are ignored;
// an unrecognized key can only contribute the fixed unknown sentinel.
var legacySectionFields = map[string][]string{
	"server": {
		"tls", "debug", "pprof", "commercial-mode", "logging-to-file",
		"logs-max-total-size-mb", "error-logs-max-files", "request-log",
		"proxy-url", "streaming", "nonstream-keepalive-interval", "ws-auth",
		"passthrough-headers", "disable-image-generation",
		"enable-gemini-cli-endpoint", "force-model-prefix",
		"auth-auto-refresh-workers",
	},
	"routing": {
		"routing", "quota-exceeded", "request-retry", "max-retry-credentials",
		"max-retry-interval", "disable-cooling", "payload",
		"antigravity-signature-cache-enabled",
		"antigravity-signature-bypass-strict", "claude-header-defaults",
		"codex-header-defaults",
	},
	"usage": {
		"usage-statistics-enabled", "redis-usage-queue-retention-seconds",
	},
	"providers": {
		"gemini-api-key", "codex-api-key", "claude-api-key",
		"openai-compatibility", "vertex-api-key", "ampcode",
		"oauth-model-alias", "oauth-excluded-models",
	},
}

// legacySectionOrder makes the bounded inventory deterministic.
var legacySectionOrder = []string{"access", "oauth", "server", "routing", "usage", "providers", "unknown_cpa_native_resource"}

// v8ConfigSections are never emitted by the legacy-shaped v0 config projection.
// They remain an extra fail-closed sanity check only; the accepted source
// profile is decided by the management version metadata, never by body shape.
var v8ConfigSections = []string{"config-version", "server", "management", "access", "oauth"}

// ReadLegacyManagedAdoptionSnapshot is the explicit legacy_managed source
// adapter, never a v8-to-v0 fallback. It performs exactly one authenticated GET
// of the legacy read-only config surface with the same direct transport, fixed
// DNS resolution, normal TLS verification, no redirects/retries, bounded
// headers/body and five-second total budget as the v8 safe read. It succeeds
// only for the exact accepted legacy-managed v8 source profile (CPA v8.0.55)
// declared by the management version metadata, refusing any CPA release below
// v8 or unpinned build. No source write, lifecycle mutation, usage/history
// access or queue consumption occurs.
func ReadLegacyManagedAdoptionSnapshot(ctx context.Context, baseURL, managementKey string) (LegacyManagedAdoptionSnapshot, error) {
	baseURL, err := NormalizeV8BaseURL(baseURL)
	if err != nil {
		return LegacyManagedAdoptionSnapshot{}, err
	}
	if !validManagementKey(managementKey) {
		return LegacyManagedAdoptionSnapshot{}, V8InvalidConnection
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client, closeClient, err := directReadClient(ctx, baseURL)
	if err != nil {
		return LegacyManagedAdoptionSnapshot{}, err
	}
	defer closeClient()
	return readLegacyConfigWithClient(ctx, client, baseURL, managementKey)
}

func readLegacyConfigWithClient(ctx context.Context, client *http.Client, baseURL, managementKey string) (LegacyManagedAdoptionSnapshot, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v0/management/config", nil)
	if err != nil {
		return LegacyManagedAdoptionSnapshot{}, V8InvalidConnection
	}
	request.Header.Set("Authorization", "Bearer "+managementKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity, gzip")
	request.Header.Set("Cache-Control", "no-store")
	response, err := client.Do(request)
	if err != nil {
		return LegacyManagedAdoptionSnapshot{}, LegacyReadUnavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusGone:
		return LegacyManagedAdoptionSnapshot{}, LegacyOwnerNotLegacy
	case http.StatusUnauthorized, http.StatusForbidden:
		return LegacyManagedAdoptionSnapshot{}, LegacyAuthenticationRejected
	case http.StatusOK:
	default:
		return LegacyManagedAdoptionSnapshot{}, LegacyReadUnavailable
	}
	// The version header is the source-profile authority. Missing, pre-v8, or
	// non-matching version headers fail closed before any body is interpreted.
	if response.Header.Get("X-CPA-VERSION") != acceptedLegacyManagedHeaderVersion {
		return LegacyManagedAdoptionSnapshot{}, LegacySourceProfileUnsupported
	}
	mediaType, parameters, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || len(parameters) > 1 ||
		(len(parameters) == 1 && !strings.EqualFold(parameters["charset"], "utf-8")) {
		return LegacyManagedAdoptionSnapshot{}, LegacyInvalidConfig
	}
	data, ok := readBoundedJSONBody(response)
	if !ok {
		return LegacyManagedAdoptionSnapshot{}, LegacyInvalidConfig
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return LegacyManagedAdoptionSnapshot{}, LegacyInvalidConfig
	}
	for _, section := range v8ConfigSections {
		if _, ok := fields[section]; ok {
			return LegacyManagedAdoptionSnapshot{}, LegacyOwnerNotLegacy
		}
	}
	keys, err := legacyClientAPIKeys(fields["api-keys"])
	if err != nil {
		return LegacyManagedAdoptionSnapshot{}, err
	}
	// A CPA source always owns an auth directory; the legacy-shaped v0
	// projection deliberately omits auth-dir, so its existence is reported as a
	// bounded category without reading or exporting the value.
	present := map[string]bool{"access": true, "oauth": true}
	classified := map[string]string{}
	for section, names := range legacySectionFields {
		for _, name := range names {
			classified[name] = section
		}
	}
	unknown := false
	for name := range fields {
		if name == "api-keys" {
			continue
		}
		if section, ok := classified[name]; ok {
			present[section] = true
			continue
		}
		unknown = true
	}
	if unknown {
		present["unknown_cpa_native_resource"] = true
	}
	snapshot := LegacyManagedAdoptionSnapshot{APIKeys: keys}
	for _, section := range legacySectionOrder {
		if present[section] {
			snapshot.ResourceSections = append(snapshot.ResourceSections, section)
		}
	}
	return snapshot, nil
}

func legacyClientAPIKeys(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, LegacyInvalidConfig
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil || entries == nil || len(entries) == 0 {
		return nil, LegacyInvalidConfig
	}
	keys := make([]string, 0, len(entries))
	seen := map[string]bool{}
	for _, entry := range entries {
		var key string
		if len(entry) == 0 || entry[0] != '"' || json.Unmarshal(entry, &key) != nil ||
			key == "" || key != strings.TrimSpace(key) || len(key) > 4096 || strings.ContainsAny(key, "\x00\r\n") || seen[key] {
			return nil, LegacyInvalidConfig
		}
		for _, character := range key {
			if character < 0x21 || character == 0x7f {
				return nil, LegacyInvalidConfig
			}
		}
		seen[key] = true
		keys = append(keys, key)
	}
	return keys, nil
}

var _ error = LegacyReadError("")
