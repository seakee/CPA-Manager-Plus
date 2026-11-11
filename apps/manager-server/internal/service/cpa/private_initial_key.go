package cpa

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	privateCPAArtifact     = "sha256:e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"
	privateRuntimeIdentity = "embedded-auth-proof"
	privateCPAAddress      = "127.0.0.1:8317"
	maxResponseBytes       = 1 << 20
)

var (
	ErrProofUnavailable = errors.New("private key application proof unavailable")
	ErrSourceConflict   = errors.New("CPA key source is not the isolated empty proof seed")
	ErrApplyUnknown     = errors.New("CPA key write result unknown; do not retry")
	// ErrWriteRejected is a definite upstream refusal: CPA answered with a
	// terminal 4xx, so nothing was written and nothing may be replayed.
	ErrWriteRejected = errors.New("CPA rejected the config field write")
)

// TargetProof is a caller-observed runtime identity. The caller must separately
// prove disposable empty volumes, the exclusive writer and live process.
// These flags are NOT host or network attestation by themselves.
type TargetProof struct {
	Identity        string
	Generation      uint64
	ArtifactID      string
	Ready           bool
	SourceExclusive bool
	Disposable      bool
	ExpiresAtMS     int64
	ProviderSeed    PrivateProviderSeed
}

// InstallResult never exposes secrets or asserts Runtime effectiveness.
type InstallResult struct {
	CPAConfigured     bool   `json:"cpaConfigured"`
	RuntimeState      string `json:"runtimeState"`
	GatewayAuthorized bool   `json:"gatewayAuthorized"`
}

// Transport pins production requests to the private loopback CPA Management
// endpoint. Tests can inject a private synthetic server inside this package.
type Transport struct {
	h1                     *H1Coordinator
	configurationAuthority *privateConfigClient
	mu                     sync.Mutex
	client                 *http.Client
	endpoint               string
	attempted              bool
	providerSeed           func() (PrivateProviderSeed, error)
	posture                proofPosture
	// postureFor resolves the per-install posture of a Manager-owned profile whose
	// strategy is only known after construction (New Managed or Adopt). It is nil
	// for the private profile and for External, whose posture is fixed.
	postureFor                PostureSelector
	supervisorRoots           []string
	supervisorRootsConfigured bool
}

func NewTransport() *Transport {
	direct := http.DefaultTransport.(*http.Transport).Clone()
	direct.Proxy = nil
	direct.DisableKeepAlives = true
	direct.MaxResponseHeaderBytes = 32 << 10
	direct.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", privateCPAAddress)
	}
	return &Transport{client: &http.Client{
		Transport:     direct,
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, endpoint: "http://" + privateCPAAddress, configurationAuthority: newPrivateConfigClient()}
}

// NewTransportForEndpoint is the default-install equivalent of NewTransport. It
// reaches the destination CPA through the normalized Manager connection, never
// through the private loopback address, and applies the managed posture.
func NewTransportForEndpoint(endpoint string) *Transport {
	direct := http.DefaultTransport.(*http.Transport).Clone()
	direct.Proxy = nil
	direct.DisableKeepAlives = true
	direct.MaxResponseHeaderBytes = 32 << 10
	return &Transport{client: &http.Client{Transport: direct, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, endpoint: strings.TrimRight(endpoint, "/"), posture: managedProofPosture}
}

// SetTrustedSupervisorRoots binds a Manager-owned Transport to the canonical
// Runtime Supervisor root(s) configured for this installation. An empty or
// non-canonical root slice fails closed rather than falling back to defaults.
func (t *Transport) SetTrustedSupervisorRoots(roots []string) {
	if t == nil {
		return
	}
	t.supervisorRoots = canonicalSupervisorRoots(roots)
	t.supervisorRootsConfigured = true
	t.posture = t.posture.withSupervisorRoots(roots)
}

func (t *Transport) request(ctx context.Context, method, path, managementKey string, payload []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, t.endpoint+path, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, ErrProofUnavailable
	}
	if managementKey != "" {
		req.Header.Set("Authorization", "Bearer "+managementKey)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-store")
	if len(payload) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := t.httpClient(ctx).Do(req)
	if err != nil {
		return 0, nil, ErrProofUnavailable
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity" {
		return 0, nil, ErrProofUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return 0, nil, ErrProofUnavailable
	}
	return resp.StatusCode, body, nil
}

// InstallOne makes at most one mutation and never retries. Its caller must
// separately verify process/generation, empty volumes and sole source/writer.
func (t *Transport) InstallOne(ctx context.Context, proof TargetProof, managementKey, stagedSecret string) (InstallResult, error) {
	unknown := InstallResult{RuntimeState: "effect_unknown", GatewayAuthorized: false}
	if t == nil {
		return unknown, ErrProofUnavailable
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.attempted {
		return unknown, ErrApplyUnknown
	}
	// The destination posture follows the strategy that staged it: an adopted
	// destination is proven against the adopted rules.
	posture := t.postureOf()
	if posture.managed {
		// A default install cannot attest a disposable, exclusive writer. The
		// caller replaces that attestation with the committed Fresh checkpoint
		// and the authenticated destination read-back below.
		if proof.ArtifactID == "" {
			return unknown, ErrProofUnavailable
		}
	} else if !proof.Disposable || !proof.SourceExclusive || proof.Identity != privateRuntimeIdentity || proof.ArtifactID != privateCPAArtifact {
		return unknown, ErrProofUnavailable
	}
	if t.client == nil || !proof.Ready || proof.ExpiresAtMS <= time.Now().UnixMilli() || proof.Generation == 0 || len(managementKey) < 32 || len(managementKey) > 72 || len(stagedSecret) != 73 || stagedSecret[:9] != "cpamp_sk_" || !validHex(stagedSecret[9:]) || !printable(managementKey) {
		return unknown, ErrProofUnavailable
	}
	status, full, err := t.request(ctx, http.MethodGet, "/v8/management/config", managementKey, nil)
	validSeed := posture.matches(full) && proof.ProviderSeed == (PrivateProviderSeed{})
	if t.providerSeed != nil {
		seed, sourceErr := t.providerSeed()
		validSeed = sourceErr == nil && seed == proof.ProviderSeed && privateProviderConfig(full, seed, "")
	}
	if err != nil || status != http.StatusOK || !validSeed {
		return unknown, ErrSourceConflict
	}
	status, current, err := t.request(ctx, http.MethodGet, "/v8/management/config/access/api-keys", managementKey, nil)
	if err != nil || status != http.StatusOK || !emptyStringList(current) {
		return unknown, ErrSourceConflict
	}
	payload, err := json.Marshal([]string{stagedSecret})
	if err != nil {
		return unknown, ErrProofUnavailable
	}
	// After the PUT attempt, failed/unknown outcomes remain unknown: no retry.
	if proof.ExpiresAtMS <= time.Now().UnixMilli() {
		return unknown, ErrProofUnavailable
	}
	intent, err := t.beginInitialConfiguration(ctx, proof, stagedSecret)
	if err != nil {
		return unknown, err
	}
	t.attempted = true
	// Durable reservation may outlive the S1 permit while storage or transport
	// is slow. Leave its original intention unknown; never PUT after expiry.
	if proof.ExpiresAtMS <= time.Now().UnixMilli() {
		return unknown, ErrApplyUnknown
	}
	status, _, err = t.request(ctx, http.MethodPut, "/v8/management/config/access/api-keys", managementKey, payload)
	if err != nil || status != http.StatusOK {
		return unknown, ErrApplyUnknown
	}
	status, written, err := t.request(ctx, http.MethodGet, "/v8/management/config/access/api-keys", managementKey, nil)
	if err != nil || status != http.StatusOK {
		return unknown, ErrApplyUnknown
	}
	var list []string
	if json.Unmarshal(written, &list) != nil || len(list) != 1 || list[0] != stagedSecret {
		return unknown, ErrApplyUnknown
	}
	if err := t.commitConfiguration(ctx, intent); err != nil {
		return unknown, err
	}
	// Config readback is not a live CPA authentication acknowledgment.
	return InstallResult{CPAConfigured: true, RuntimeState: "effect_unknown", GatewayAuthorized: false}, nil
}

// proofPosture is the expected destination posture of the CPA management
// source. The zero value is the private S1 seed: loopback only, remote
// management disabled, usage statistics off, fixed Gateway auth directory and
// plugins published at the Supervisor-owned plugin root. Fields are
// server-owned facts of the destination template, never caller input.
type proofPosture struct {
	managed bool
	// adopt selects the adopted destination rules: a subset root set and the
	// source's client list, instead of the new_managed template. It is set only by
	// `adoptProofPosture`.
	adopt bool
	// deny refuses every read. It is the posture of a Manager-owned profile whose
	// selector could not answer, which is a destination this Manager may not prove.
	deny                      bool
	host                      string
	allowRemote               bool
	usageStatistics           bool
	authDir                   string
	supervisorRoots           []string
	supervisorRootsConfigured bool
}

// privateProofPosture is the attested private S1 seed; it is the zero value.
var privateProofPosture = proofPosture{}

// managedProofPosture is the default new_managed destination: the Supervisor
// template binds either 0.0.0.0 with remote management enabled (Docker) or
// 127.0.0.1/::1 with remote management disabled (Native
// CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK=1), enables usage statistics, publishes the
// Supervisor-owned plugin root, and keeps its own Gateway auth directory.
var managedProofPosture = proofPosture{managed: true, host: "0.0.0.0", allowRemote: true, usageStatistics: true}

// validDestinationBind reports whether (boundHost, allowRemote) matches a
// bind posture the Runtime Supervisor writes for this proofPosture.
func (p proofPosture) validDestinationBind(boundHost string, allowRemote bool) bool {
	host := p.host
	if host == "" {
		host = "127.0.0.1"
	}
	if boundHost == host && allowRemote == p.allowRemote {
		return true
	}
	return p.managed && (boundHost == "127.0.0.1" || boundHost == "::1") && !allowRemote
}

// looksLikeBcrypt reports whether CPA persisted a bcrypt digest. CPA hashes a
// plaintext secret-key at load and rewrites the file; a plaintext value here
// would mean the key was never adopted by this CPA.
func looksLikeBcrypt(value string) bool {
	if len(value) != 60 || value[0] != '$' || value[3] != '$' || value[6] != '$' {
		return false
	}
	if value[1] != '2' || (value[2] != 'a' && value[2] != 'b' && value[2] != 'y') {
		return false
	}
	for _, c := range value[4:6] {
		if c < '0' || c > '9' {
			return false
		}
	}
	for _, c := range value[7:] {
		if !(c == '.' || c == '/' || c == '+' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}

// matches accepts only the exact expected posture with an empty client-key
// list. Authentication of the read itself already proves that the persisted
// management digest belongs to the presented credential.
func (p proofPosture) matches(data []byte) bool {
	if p.deny {
		return false
	}
	if p.adopt {
		return p.matchesAdopt(data)
	}
	if p.managed {
		return p.matchesManaged(data)
	}
	host := p.host
	if host == "" {
		host = "127.0.0.1"
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil || (len(root) != 7 && len(root) != 8) {
		return false
	}
	if len(root) == 8 {
		if p.managed {
			return false
		}
		// Only this fixed loopback synthetic provider may accompany the seed.
		var actual, expected any
		if json.Unmarshal(root["api-keys"], &actual) != nil || json.Unmarshal([]byte(privateSyntheticProvider), &expected) != nil {
			return false
		}
		a, _ := json.Marshal(actual)
		b, _ := json.Marshal(expected)
		if !bytes.Equal(a, b) {
			return false
		}
	}
	for _, name := range []string{"config-version", "server", "management", "access", "oauth", "plugins", "observability"} {
		if _, ok := root[name]; !ok {
			return false
		}
	}
	var version int
	if json.Unmarshal(root["config-version"], &version) != nil || version != 8 {
		return false
	}
	var server map[string]json.RawMessage
	if json.Unmarshal(root["server"], &server) != nil || len(server) != 2 {
		return false
	}
	var boundHost string
	var port int
	if json.Unmarshal(server["host"], &boundHost) != nil ||
		json.Unmarshal(server["port"], &port) != nil || port != 8317 {
		return false
	}
	var access map[string]json.RawMessage
	if json.Unmarshal(root["access"], &access) != nil || len(access) != 1 || !emptyStringList(access["api-keys"]) {
		return false
	}
	var oauth map[string]json.RawMessage
	if json.Unmarshal(root["oauth"], &oauth) != nil || len(oauth) != 1 {
		return false
	}
	var authDir string
	if json.Unmarshal(oauth["auth-dir"], &authDir) != nil || !p.validAuthDir(authDir) {
		return false
	}
	var management map[string]json.RawMessage
	if json.Unmarshal(root["management"], &management) != nil || len(management) != 4 {
		return false
	}
	var allowRemote bool
	if (!bytes.Equal(management["allow-remote"], []byte("true")) && !bytes.Equal(management["allow-remote"], []byte("false"))) ||
		json.Unmarshal(management["allow-remote"], &allowRemote) != nil ||
		!p.validDestinationBind(boundHost, allowRemote) {
		return false
	}
	for _, name := range []string{"disable-control-panel", "disable-auto-update-panel"} {
		var value bool
		if (!bytes.Equal(management[name], []byte("true")) && !bytes.Equal(management[name], []byte("false"))) || json.Unmarshal(management[name], &value) != nil || !value {
			return false
		}
	}
	// Plugins are a Supervisor-owned resource. On the default destination the
	// enabled switch is the operator's own decision, so either value is the
	// template's; on the private empty seed plugins stay disabled. What both pin
	// is the directory, which must be exactly a Supervisor-owned plugin root, and
	// the closed key set: a store source, a per-plugin config or an auth revision
	// is a posture this Manager never wrote.
	var plugins map[string]json.RawMessage
	var pluginDir string
	var enabled bool
	if json.Unmarshal(root["plugins"], &plugins) != nil || len(plugins) != 2 ||
		(!bytes.Equal(plugins["enabled"], []byte("true")) && !bytes.Equal(plugins["enabled"], []byte("false"))) ||
		json.Unmarshal(plugins["enabled"], &enabled) != nil ||
		json.Unmarshal(plugins["dir"], &pluginDir) != nil || !p.validPluginDir(pluginDir) {
		return false
	}
	if !p.managed && enabled {
		return false
	}

	var observability struct {
		Logs struct {
			LoggingToFile bool `json:"logging-to-file"`
		} `json:"logs"`
		Usage struct {
			UsageStatisticsEnabled bool `json:"usage-statistics-enabled"`
		} `json:"usage"`
	}
	if json.Unmarshal(root["observability"], &observability) != nil ||
		observability.Logs.LoggingToFile || observability.Usage.UsageStatisticsEnabled != p.usageStatistics {
		return false
	}
	var obs map[string]json.RawMessage
	if json.Unmarshal(root["observability"], &obs) != nil || len(obs) != 2 {
		return false
	}
	for _, sub := range []string{"logs", "usage"} {
		var nested map[string]json.RawMessage
		if json.Unmarshal(obs[sub], &nested) != nil || len(nested) != 1 {
			return false
		}
		name := map[string]string{"logs": "logging-to-file", "usage": "usage-statistics-enabled"}[sub]
		want := []byte("false")
		if sub == "usage" && p.usageStatistics {
			want = []byte("true")
		}
		if !bytes.Equal(nested[name], want) {
			return false
		}
	}
	var secret string
	if json.Unmarshal(management["secret-key"], &secret) != nil {
		return false
	}
	if p.managed {
		return looksLikeBcrypt(secret)
	}
	return len(secret) > 0
}

var defaultSupervisorRoots = []string{
	"/data/runtime/supervisor",
	"/runtime/supervisor",
}

func canonicalSupervisorRoots(roots []string) []string {
	var out []string
	for _, raw := range roots {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		slashed := filepath.ToSlash(trimmed)
		if !strings.HasPrefix(slashed, "/") {
			continue
		}
		cleaned := path.Clean(slashed)
		if cleaned == "/" || cleaned == "." {
			continue
		}
		if !slices.Contains(out, cleaned) {
			out = append(out, cleaned)
		}
	}
	return out
}

func (p proofPosture) withSupervisorRoots(roots []string) proofPosture {
	p.supervisorRoots = canonicalSupervisorRoots(roots)
	p.supervisorRootsConfigured = true
	return p
}

func (p proofPosture) effectiveSupervisorRoots() []string {
	if p.supervisorRootsConfigured {
		return p.supervisorRoots
	}
	return defaultSupervisorRoots
}

// validAuthDir accepts the private fixed path, or the canonical Supervisor-owned
// destination auth directory of this installation: the immutable destination
// root is <supervisorRoot>/destination and publishes <supervisorRoot>/destination/auth.
// Child-writable <runtimeRoot>/gateway paths are never admitted.
func (p proofPosture) validAuthDir(dir string) bool {
	if p.authDir != "" {
		return dir == p.authDir
	}
	if !p.managed {
		return dir == "/runtime/gateway/auth"
	}
	if dir == "" || path.Clean(dir) != dir {
		return false
	}
	for _, root := range p.effectiveSupervisorRoots() {
		if dir == root+"/destination/auth" {
			return true
		}
	}
	return false
}

// privatePluginDir is the private S1 seed's plugin root.
const privatePluginDir = "/runtime/gateway/plugins"

// validPluginDir accepts the private fixed path, or the canonical
// Supervisor-owned plugin root (<supervisorRoot>/plugins) of this installation.
// The immutable destination is <supervisorRoot>/destination, which publish
// replaces whole, so the plugin root is its sibling <supervisorRoot>/plugins:
// it survives every re-staged destination and a Runtime restart, and no request
// can point it elsewhere.
func (p proofPosture) validPluginDir(dir string) bool {
	if !p.managed {
		return dir == privatePluginDir
	}
	if dir == "" || path.Clean(dir) != dir {
		return false
	}
	for _, root := range p.effectiveSupervisorRoots() {
		if dir == root+"/plugins" {
			return true
		}
	}
	return false
}

func emptyProofConfig(data []byte) bool { return privateProofPosture.matches(data) }

func emptyStringList(data []byte) bool {
	var v []string
	return json.Unmarshal(data, &v) == nil && v != nil && len(v) == 0
}
func validHex(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}
func printable(s string) bool {
	for _, c := range s {
		if c < '!' || c > '~' {
			return false
		}
	}
	return true
}
