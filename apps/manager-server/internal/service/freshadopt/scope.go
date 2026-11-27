package freshadopt

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

// managerOwnedPrefixes are the destination configuration subtrees this Manager
// owns: management credentials, listener/discovery/TLS/KV paths, plugin runtime
// controls, observability host settings, auth directory, and client key list.
// Copying one of them would hand the source's identity to the destination or
// overwrite what the verified destination installation already wrote.
//
// Both the canonical v8 spelling and the historical YAML spelling appear, since
// a caller may name either and neither may slip through:
//
//	internal/config/config_v8.go:73-75  host→server.host, port→server.port,
//	                                    remote-management→management,
//	                                    api-keys→access.api-keys
//	internal/config/config_v8.go:84     auth-dir→oauth.auth-dir
//	internal/config/config_types.go:325 remote-management.disable-control-panel
var managerOwnedPrefixes = []string{
	"management.",
	"remote-management.",
	"disable-control-panel",
	"oauth.auth-dir.",
	// A list has no children, but a path spelled below it still names the same
	// owned client key list, so the subtree form is the conservative one.
	"access.api-keys.",
}

// managerOwnedExact are the Manager-owned paths that are not a prefix of
// anything else, plus whole parent sections whose subtrees contain host-owned
// configuration. Named business children under `server.*`, `plugins.*`,
// `observability.*`, and `oauth.*` are permitted only through explicit allowlists
// in ManagerOwned so unknown keys under shared roots still fail closed.
var managerOwnedExact = map[string]bool{
	"disable-control-panel": true,
	"oauth.auth-dir":        true,
	"access.api-keys":       true,
	"management":            true,
	"server":                true,
	"plugins":               true,
	"oauth":                 true,
	"access":                true,
	"observability":         true,
}

// ManagerOwned reports whether one dotted CPA path belongs to the Manager on the
// destination and therefore must never be copied. It is the single exclusion
// predicate of this package.
func ManagerOwned(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	if managerOwnedExact[path] {
		return true
	}
	for _, prefix := range managerOwnedPrefixes {
		if strings.HasSuffix(prefix, ".") {
			if strings.HasPrefix(path, prefix) {
				return true
			}
			continue
		}
		if path == prefix {
			return true
		}
	}
	switch {
	case strings.HasPrefix(path, "server."):
		_, ok := cpa.LookupConfigLeaf(path)
		return !ok
	case strings.HasPrefix(path, "plugins."):
		return !slices.Contains([]string{
			"plugins.store-sources",
			"plugins.store-auth",
			"plugins.configs",
		}, path)
	case strings.HasPrefix(path, "observability."):
		return path != "observability.logs.debug" && path != "observability.logs.request-log"
	case strings.HasPrefix(path, "oauth."):
		if _, ok := cpa.LookupConfigLeaf(path); ok {
			return false
		}
		if slices.Contains(cpa.ProviderAdoptionSections(), path) || slices.Contains(cpa.StructuredConfigAdoptionSections(), path) {
			return false
		}
		return true
	}
	return false
}

// ProviderSections returns every whole-PUT configuration section in migration
// order: the 12 provider API-key/OAuth map sections followed by the 6 structured
// CPA v8 configuration sections (`oauth.providers.codex.live-media-relay.ice-servers`,
// `credentials.concurrency`, `credentials.in-flight`, `plugins.store-sources`,
// `plugins.store-auth`, `plugins.configs`).
func ProviderSections() []string {
	return slices.Concat(cpa.ProviderAdoptionSections(), cpa.StructuredConfigAdoptionSections())
}
// configLeafPrefix resolves the single PATCH prefix of one gateway leaf: the
// leaf's own page group. A leaf outside the page table has no prefix.
func configLeafPrefix(path string) (string, error) {
	leaf, ok := cpa.LookupConfigLeaf(path)
	if !ok {
		return "", cpa.ErrInvalidConfigPath
	}
	return cpa.ConfigGroupPrefix(leaf.Group)
}

// leafPatchPayload builds the PATCH body of one leaf below its group prefix. CPA
// merges a PATCH mapping key by key, so a scalar body would replace the whole
// group mapping instead of setting one leaf: the body must nest from the prefix
// down to the leaf, exactly like the gateway page's group save does.
func leafPatchPayload(prefix, path string, value any) ([]byte, error) {
	prefixParts := strings.Split(prefix, ".")
	parts := strings.Split(path, ".")
	if len(parts) <= len(prefixParts) {
		return nil, cpa.ErrInvalidConfigPath
	}
	var body any = map[string]any{parts[len(parts)-1]: value}
	for index := len(parts) - 2; index >= len(prefixParts); index-- {
		body = map[string]any{parts[index]: body}
	}
	return encodeJSONValue(body)
}

// encodeJSONValue renders one whole section or rule family as the exact JSON CPA
// accepts. Numbers stay json.Number so a 64-bit rule parameter keeps its digits.
func encodeJSONValue(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}

// errorCode maps one failed destination write to a bounded reason. It never
// carries a value: CPA can quote the offending YAML in a validation message, so
// only a stable code is kept.
func errorCode(err error) string {
	var rejection cpa.ConfigWriteError
	if errors.As(err, &rejection) {
		if rejection.Code != "" {
			return rejection.Code
		}
		return "cpa_rejected"
	}
	switch {
	case errors.Is(err, cpa.ErrApplyUnknown):
		return "apply_unknown"
	case errors.Is(err, cpa.ErrProofUnavailable):
		return "destination_unavailable"
	case errors.Is(err, cpa.ErrSourceConflict):
		return "value_not_canonical"
	case errors.Is(err, ErrCredentialNotUploadable):
		return "not_uploadable"
	default:
		return "write_failed"
	}
}

// adoptionWriteOutcome classifies one destination write failure. A definite
// refusal means the destination document is unchanged, so the item is manual
// work the operator can do themselves. Anything else is an unknown effect and is
// reported as failed: it is reconciled by reading the destination back, never by
// sending the write again.
func adoptionWriteOutcome(err error, instruction string) (freshstore.AdoptionOutcome, string) {
	var rejection cpa.ConfigWriteError
	if errors.As(err, &rejection) && rejection.Definite() || errors.Is(err, ErrCredentialNotUploadable) {
		return freshstore.AdoptionManual, instruction
	}
	return freshstore.AdoptionFailed, ""
}
