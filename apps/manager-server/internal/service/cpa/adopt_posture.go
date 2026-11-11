package cpa

import (
	"context"
	"encoding/json"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

// This file is the Adopt half of the destination proof. An adopted destination is
// a Manager-owned installation — the Supervisor staged it from this Manager's own
// template and wrote the install-derived management credential into it — but its
// client keys, provider catalogue and gateway configuration belong to the source
// the operator adopted. The new_managed posture describes a template this Manager
// wrote whole, so it refuses exactly the parts an adoption is allowed to carry:
// more than two client keys and extra root sections. The plugin switch is not one
// of them: the plugin page opens for an adoption too, so the operator's own
// decision there must not invalidate the proof.
//
// The two postures therefore differ only in the rules the two strategies differ
// in. The proof itself is shared and unchanged: the authenticated read succeeded
// with the install-derived credential, and CPA rewrote that credential into its
// own bcrypt digest.

// adoptRoots is the closed set of top-level sections an adopted CPA v8 document
// may carry. All 16 CPA v8 roots (`v8AllowedRoots()` in CPA config_v8.go) are
// enumerated explicitly: the 7 Supervisor-staged required sections plus the 9
// optional operator/source sections (`api-keys`, `routing`, `requests`,
// `upstream`, `client`, `multimedia`, `models`, `credentials`, `quota-exceeded`).
var adoptRoots = map[string]bool{
	"config-version": true,
	"api-keys":       true,
	"plugins":        true,
	"quota-exceeded": true,
	"client":         true,
	"models":         true,
	"server":         true,
	"management":     true,
	"access":         true,
	"credentials":    true,
	"routing":        true,
	"requests":       true,
	"oauth":          true,
	"upstream":       true,
	"multimedia":     true,
	"observability":  true,
}

// adoptRequiredRoots are the roots the Supervisor's destination template writes
// and no adoption removes: the version stamp, the listening server, the remote
// management block carrying the install-derived credential, the adopted client
// key list, the destination auth directory, the Supervisor-owned plugin root and
// the usage switch. Their absence is a document this Manager did not stage.
var adoptRequiredRoots = []string{"config-version", "server", "management", "access", "oauth", "plugins", "observability"}

// adoptProofPosture is the adopted destination: the Supervisor template keyed by
// the install-derived management credential, carrying the adopted client list and
// whatever root sections the source carried.
func adoptProofPosture() proofPosture {
	return proofPosture{managed: true, adopt: true, host: "0.0.0.0", allowRemote: true, usageStatistics: true}
}

// DestinationPosture is the per-install posture of the destination CPA. It names
// the fact that is not a package constant: which strategy staged the destination.
// The recorded plugin opt-in is not part of it — that switch is the operator's to
// change on the plugin page, so it is not a proof of this Manager.
type DestinationPosture struct {
	// Adopt marks a destination staged by an adoption, selecting the adopted rules
	// instead of the new_managed template.
	Adopt bool
	// SupervisorRoots, when non-nil, binds directory posture validation to this
	// installation's canonical Runtime Supervisor root(s).
	SupervisorRoots []string
}

// PostureFor resolves the destination posture of one strategy. Only the
// strategies this Manager stages a destination for are answered, so a caller can
// never widen a posture it does not own: Connect External and every unknown
// strategy are refused.
func PostureFor(strategy string) (DestinationPosture, bool) {
	switch strategy {
	case "new_managed":
		return DestinationPosture{}, true
	case "adopt_existing":
		return DestinationPosture{Adopt: true}, true
	default:
		return DestinationPosture{}, false
	}
}

// PostureForWithSupervisorRoots resolves the destination posture of one
// strategy bound to the installation's canonical Runtime Supervisor root(s).
func PostureForWithSupervisorRoots(strategy string, roots []string) (DestinationPosture, bool) {
	posture, ok := PostureFor(strategy)
	if !ok {
		return DestinationPosture{}, false
	}
	if roots == nil {
		posture.SupervisorRoots = []string{}
	} else {
		posture.SupervisorRoots = append([]string{}, roots...)
	}
	return posture, true
}

// PostureSelector resolves the posture of the installation this transport serves.
// It is installed once, at construction, and re-read on every request, so a
// checkpoint that changed underneath fails closed instead of holding a stale
// posture.
type PostureSelector func() (DestinationPosture, bool)

// SetPostureSelector installs the per-install posture of a profile whose strategy
// is known only after construction. The private profile and the External profile
// never call it: their posture is fixed, and a selector would be a second answer.
func (t *Transport) SetPostureSelector(select_ PostureSelector) {
	if t == nil {
		return
	}
	t.postureFor = select_
}

// postureOf resolves the posture of this request. A selector that cannot answer
// refuses; it never degrades to the loosest posture.
func (t *Transport) postureOf() proofPosture {
	if t == nil {
		return proofPosture{deny: true}
	}
	if t.postureFor == nil {
		return t.posture
	}
	v, ok := t.postureFor()
	if !ok {
		return proofPosture{deny: true}
	}
	base := managedProofPosture
	if v.Adopt {
		base = adoptProofPosture()
	}
	if v.SupervisorRoots != nil {
		return base.withSupervisorRoots(v.SupervisorRoots)
	}
	if t.supervisorRootsConfigured {
		base.supervisorRoots = t.supervisorRoots
		base.supervisorRootsConfigured = true
	}
	return base
}

// matchesAdopt accepts the adopted destination posture: the same proof as the
// managed template — the authenticated read, the bcrypt management digest CPA
// rewrote, the Supervisor-owned auth and plugin roots, usage statistics on —
// minus the three rules an adoption legitimately breaks.
func (p proofPosture) matchesAdopt(data []byte) bool {
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil || root == nil {
		return false
	}
	requiredSet := make(map[string]bool, len(adoptRequiredRoots))
	for _, name := range adoptRequiredRoots {
		if _, ok := root[name]; !ok {
			return false
		}
		requiredSet[name] = true
	}
	for name, raw := range root {
		if requiredSet[name] {
			continue
		}
		if !adoptRoots[name] || !isJSONObject(raw) {
			return false
		}
	}
	if !exactJSONInt(root["config-version"], 8) {
		return false
	}
	boundHost, ok := matchesAdoptServer(root["server"])
	if !ok || !p.matchesManagedManagement(root["management"], boundHost) {
		return false
	}
	var access map[string]json.RawMessage
	// The adopted client list is the source's, so only its shape is checked here;
	// the key policy is `validKeys`, which `KeySnapshot` applies before blanking
	// `access.api-keys`. An extra access key is still refused.
	if json.Unmarshal(root["access"], &access) != nil || len(access) != 1 || !isStringSlice(access["api-keys"]) {
		return false
	}
	if !p.matchesManagedOAuth(root["oauth"]) ||
		!p.matchesManagedPlugins(root["plugins"]) ||
		!p.matchesManagedObservability(root["observability"]) {
		return false
	}
	return true
}

// matchesAdoptServer enforces Supervisor's listener bind/port and disabled
// TLS/mDNS/custom-path KV invariants while retaining safe source CPA `server.*`
// settings that an adopted instance may carry:
//   - Enforced: `host` + `port` (= 8317, paired with `management.allow-remote`).
//   - Enforced disabled / confined: `tls` (`enable` absent or false),
//     `discovery` (`enabled` absent or false), `kv-store` (`disabled` bool,
//     `path` absent or empty so bbolt stays inside `<oauth.auth-dir>/kv.db`).
//   - Retained safe source settings: `commercial-mode` (bool),
//     `max-request-body-bytes` (non-negative int), `trusted-proxies` ([]string),
//     `response-compression` ({enabled?: bool, min-bytes?: int}),
//     `github-token` (string).
//   - Rejected: enabled `tls` or `discovery`, non-empty `kv-store.path`, and
//     every unknown `server` key.
func matchesAdoptServer(raw json.RawMessage) (string, bool) {
	var server map[string]json.RawMessage
	if json.Unmarshal(raw, &server) != nil || server == nil {
		return "", false
	}
	var boundHost string
	if json.Unmarshal(server["host"], &boundHost) != nil || !exactJSONInt(server["port"], 8317) {
		return "", false
	}
	for key, val := range server {
		switch key {
		case "host", "port":
		case "discovery":
			if !matchesDisabledDiscovery(val) {
				return "", false
			}
		case "tls":
			if !matchesDisabledTLS(val) {
				return "", false
			}
		case "kv-store":
			if !matchesAdoptKVStore(val) {
				return "", false
			}
		case "commercial-mode":
			if _, ok := parseExactJSONBool(val); !ok {
				return "", false
			}
		case "max-request-body-bytes":
			if !exactJSONNonNegativeInt64(val) {
				return "", false
			}
		case "trusted-proxies":
			if !isStringSlice(val) {
				return "", false
			}
		case "response-compression":
			if !matchesResponseCompression(val) {
				return "", false
			}
		case "github-token":
			var token string
			if json.Unmarshal(val, &token) != nil {
				return "", false
			}
		default:
			return "", false
		}
	}
	return boundHost, true
}

func matchesAdoptKVStore(raw json.RawMessage) bool {
	var kv map[string]json.RawMessage
	if json.Unmarshal(raw, &kv) != nil || kv == nil {
		return false
	}
	for key, val := range kv {
		switch key {
		case "disabled":
			if _, ok := parseExactJSONBool(val); !ok {
				return false
			}
		case "path":
			var p string
			if json.Unmarshal(val, &p) != nil || p != "" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func matchesResponseCompression(raw json.RawMessage) bool {
	var rc map[string]json.RawMessage
	if json.Unmarshal(raw, &rc) != nil || rc == nil {
		return false
	}
	for key, val := range rc {
		switch key {
		case "enabled":
			if _, ok := parseExactJSONBool(val); !ok {
				return false
			}
		case "min-bytes":
			var n int
			if json.Unmarshal(val, &n) != nil {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func exactJSONNonNegativeInt64(raw json.RawMessage) bool {
	var v int64
	return json.Unmarshal(raw, &v) == nil && v >= 0
}

// adoptMaxClientKeys is the adoption bound: a source with more client keys than
// this has nothing this Manager will copy, so a destination above it is not one
// this Manager staged.
const adoptMaxClientKeys = 128

// validKeys is the client-key policy of one posture. The adopted destination
// carries the source's list, which is opaque to this Manager: any printable key
// up to the adoption bound, unique. A Manager-owned New Managed destination only
// ever carries the two shapes this Manager minted.
func (p proofPosture) validKeys(keys []string) bool {
	if p.adopt {
		if len(keys) < 1 || len(keys) > adoptMaxClientKeys {
			return false
		}
		seen := make(map[string]bool, len(keys))
		for _, key := range keys {
			if !validExternalKey(key) || seen[key] {
				return false
			}
			seen[key] = true
		}
		return true
	}
	if len(keys) < 1 || len(keys) > 2 {
		return false
	}
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if !model.ValidCanonicalClientKey(key) || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

// ProveAdoptedDestination proves one adopted destination with the
// install-derived management credential and returns its client keys.
//
// It runs the production proof rather than a subset of it: the adopted posture on
// a transport of its own, then `KeySnapshot`, which is the same function gateway
// admission and the Adopt completion path call. Every rule of the adopted
// destination is therefore exercised here — host and port, the disabled panels,
// the Supervisor auth directory and plugin root, the plugin key set, the usage
// and log switches, the client-key policy and the advertised key list — and a
// document that fails one of them is refused exactly as the product refuses it.
//
// The posture is a package constant of this proof, not a caller input: this
// function has no record to read, and the whole point is that the document alone
// must prove the Supervisor template. It is exported because the real-CPA harness
// runs it after a migration; nothing here grants a write.
func ProveAdoptedDestination(ctx context.Context, endpoint, managementKey string, supervisorRoots ...string) ([]string, error) {
	transport := NewTransportForEndpoint(endpoint)
	// The transport's default is the New Managed template, whose exact root count
	// an adopted document never carries.
	posture := adoptProofPosture()
	if len(supervisorRoots) > 0 {
		posture = posture.withSupervisorRoots(supervisorRoots)
	}
	transport.posture = posture
	snapshot, err := transport.KeySnapshot(ctx, managementKey)
	if err != nil {
		return nil, err
	}
	return snapshot.Keys, nil
}
