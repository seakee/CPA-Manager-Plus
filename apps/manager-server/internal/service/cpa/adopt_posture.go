package cpa

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

// This file is the Adopt half of the destination proof. An adopted destination is
// a Manager-owned installation — the Supervisor staged it from this Manager's own
// template and wrote the install-derived management credential into it — but its
// client keys, provider catalogue and gateway configuration belong to the source
// the operator adopted. The new_managed posture describes a template this Manager
// wrote whole, so it refuses exactly the parts an adoption is allowed to carry:
// more than two client keys, extra root sections, and a plugin switch the operator
// chose rather than one this Manager fixed.
//
// The two postures therefore differ only in the rules the two strategies differ
// in. The proof itself is shared and unchanged: the authenticated read succeeded
// with the install-derived credential, and CPA rewrote that credential into its
// own bcrypt digest.

// adoptRoots is the closed set of root sections a CPA v8 document may carry. It
// is CPA's own accepted vocabulary, not this Manager's page: v8.0.13 derives
// `v8AllowedRoots()` from `buildV8Paths()` (internal/config/config_v8.go), which
// accepts five literal roots — config-version, api-keys, plugins, quota-exceeded,
// client — plus the first segment of every migrated path (server, management,
// access, credentials, routing, requests, oauth, upstream, multimedia,
// observability).
//
// An adopted destination carries whatever its source carried, so this is a subset
// membership test with no exact count: a root CPA itself would not read is a
// document this Manager never proved, and nothing else is.
var adoptRoots = map[string]bool{
	"config-version": true,
	"api-keys":       true,
	"plugins":        true,
	"quota-exceeded": true,
	"client":         true,
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
// the install-derived management credential, carrying the adopted client list,
// the recorded plugin opt-in, and whatever root sections the source carried.
func adoptProofPosture(pluginsEnabled bool) proofPosture {
	return proofPosture{managed: true, adopt: true, host: "0.0.0.0", allowRemote: true, usageStatistics: true, pluginsEnabled: pluginsEnabled}
}

// DestinationPosture is the per-install posture of the destination CPA. It names
// the facts that are not package constants: which strategy staged the destination
// and the plugin opt-in the operator recorded for it.
type DestinationPosture struct {
	// Adopt marks a destination staged by an adoption, selecting the adopted rules
	// instead of the new_managed template.
	Adopt bool
	// PluginsEnabled is the recorded plugin opt-in. The Supervisor wrote exactly
	// this value into the destination, so a switch that disagrees is a posture
	// this Manager did not stage for this installation.
	PluginsEnabled bool
}

// PostureFor resolves the destination posture of one strategy. Only the
// strategies this Manager stages a destination for are answered, so a caller can
// never widen a posture it does not own: Connect External and every unknown
// strategy are refused.
func PostureFor(strategy string, pluginsEnabled bool) (DestinationPosture, bool) {
	switch strategy {
	case "new_managed":
		return DestinationPosture{}, true
	case "adopt_existing":
		return DestinationPosture{Adopt: true, PluginsEnabled: pluginsEnabled}, true
	default:
		return DestinationPosture{}, false
	}
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
	if v.Adopt {
		return adoptProofPosture(v.PluginsEnabled)
	}
	return managedProofPosture
}

// matchesAdoptedCore checks the adopted rules that do not depend on the record
// that staged the destination: the closed root vocabulary, the required roots,
// the version stamp, the bcrypt management digest and the usage switch.
func (p proofPosture) matchesAdoptedCore(data []byte) bool {
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil {
		return false
	}
	for name := range root {
		if !adoptRoots[name] {
			return false
		}
	}
	for _, name := range adoptRequiredRoots {
		if _, ok := root[name]; !ok {
			return false
		}
	}
	var version int
	if json.Unmarshal(root["config-version"], &version) != nil || version != 8 {
		return false
	}
	var management struct {
		AllowRemote bool   `json:"allow-remote"`
		SecretKey   string `json:"secret-key"`
	}
	if json.Unmarshal(root["management"], &management) != nil || !management.AllowRemote || !looksLikeBcrypt(management.SecretKey) {
		return false
	}
	var observability struct {
		Usage struct {
			UsageStatisticsEnabled *bool `json:"usage-statistics-enabled"`
		} `json:"usage"`
	}
	if json.Unmarshal(root["observability"], &observability) != nil ||
		observability.Usage.UsageStatisticsEnabled == nil || !*observability.Usage.UsageStatisticsEnabled {
		return false
	}
	return true
}

// matchesAdopt accepts the adopted destination posture: the same proof as the
// managed template — the authenticated read, the bcrypt management digest CPA
// rewrote, the Supervisor-owned auth and plugin roots, usage statistics on —
// minus the three rules an adoption legitimately breaks.
func (p proofPosture) matchesAdopt(data []byte) bool {
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil {
		return false
	}
	for name := range root {
		if !adoptRoots[name] {
			return false
		}
	}
	for _, name := range adoptRequiredRoots {
		if _, ok := root[name]; !ok {
			return false
		}
	}
	var version int
	if json.Unmarshal(root["config-version"], &version) != nil || version != 8 {
		return false
	}
	var server struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	if json.Unmarshal(root["server"], &server) != nil || server.Host != p.host || server.Port != 8317 {
		return false
	}
	var management struct {
		AllowRemote            bool   `json:"allow-remote"`
		SecretKey              string `json:"secret-key"`
		DisableControlPanel    bool   `json:"disable-control-panel"`
		DisableAutoUpdatePanel bool   `json:"disable-auto-update-panel"`
	}
	if json.Unmarshal(root["management"], &management) != nil ||
		management.AllowRemote != p.allowRemote ||
		!management.DisableControlPanel || !management.DisableAutoUpdatePanel ||
		// CPA hashed the install-derived key at first startup. A plaintext value
		// means this document is not the one the destination wrote back.
		!looksLikeBcrypt(management.SecretKey) {
		return false
	}
	// The adopted client list is the source's, so only its shape is checked here;
	// the key policy is `validKeys`, which the snapshot caller applies.

	var access map[string]json.RawMessage
	var keys []string
	if json.Unmarshal(root["access"], &access) != nil || json.Unmarshal(access["api-keys"], &keys) != nil || keys == nil {
		return false
	}
	var oauth map[string]json.RawMessage
	var authDir string
	if json.Unmarshal(root["oauth"], &oauth) != nil || json.Unmarshal(oauth["auth-dir"], &authDir) != nil || !p.validAuthDir(authDir) {
		return false
	}
	var plugins map[string]json.RawMessage
	var pluginDir string
	var enabled bool
	if json.Unmarshal(root["plugins"], &plugins) != nil ||
		json.Unmarshal(plugins["enabled"], &enabled) != nil ||
		// The recorded opt-in is the operator's decision for this installation,
		// and the Supervisor wrote exactly that value into the destination.
		enabled != p.pluginsEnabled ||
		json.Unmarshal(plugins["dir"], &pluginDir) != nil || !p.validPluginDir(pluginDir) {
		return false
	}
	var observability struct {
		Usage struct {
			UsageStatisticsEnabled *bool `json:"usage-statistics-enabled"`
		} `json:"usage"`
	}
	if json.Unmarshal(root["observability"], &observability) != nil ||
		observability.Usage.UsageStatisticsEnabled == nil || !*observability.Usage.UsageStatisticsEnabled {
		return false
	}
	var obs map[string]json.RawMessage
	if json.Unmarshal(root["observability"], &obs) != nil {
		return false
	}
	if logs, ok := obs["logs"]; ok {
		var nested struct {
			LoggingToFile *bool `json:"logging-to-file"`
		}
		if json.Unmarshal(logs, &nested) != nil || nested.LoggingToFile == nil || *nested.LoggingToFile {
			return false
		}
	}
	return true
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
// The document is read once: the plugin opt-in is the switch that document
// carries, so this proves the destination is the adopted template without
// needing the record that wrote it. The caller that holds the record pins the
// same switch separately, through the posture selector.
//
// It is the direct proof the real-CPA harness runs after a migration, and it is
// exported so it can be: the posture itself stays unexported, and nothing here
// grants a write.
func ProveAdoptedDestination(ctx context.Context, endpoint, managementKey string) ([]string, error) {
	transport := NewTransportForEndpoint(endpoint)
	code, body, err := transport.request(ctx, http.MethodGet, "/v8/management/config", managementKey, nil)
	if err != nil {
		return nil, ErrProofUnavailable
	}
	if code != http.StatusOK {
		return nil, ErrSourceConflict
	}
	var plugins struct {
		Enabled *bool `json:"enabled"`
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil || json.Unmarshal(root["plugins"], &plugins) != nil || plugins.Enabled == nil {
		return nil, ErrSourceConflict
	}
	posture := adoptProofPosture(*plugins.Enabled)
	// The record-independent rules only: the root set, the required roots, the
	// version, the bcrypt management digest and the usage switch. The Supervisor
	// directory and bind facts are proven against the record by the posture
	// selector, which this harness does not hold; asserting them from a document
	// alone would only restate that document.
	if !posture.matchesAdoptedCore(body) {
		return nil, ErrSourceConflict
	}
	var access map[string]json.RawMessage
	if json.Unmarshal(root["access"], &access) != nil {
		return nil, ErrSourceConflict
	}
	var keys []string
	if json.Unmarshal(access["api-keys"], &keys) != nil || !posture.validKeys(keys) {
		return nil, ErrSourceConflict
	}
	return keys, nil
}
