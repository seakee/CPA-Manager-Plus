package freshkeyapply

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
)

const managedBcryptSecret = "$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY12"

const managedRuntimeIdentity = "managed-install-runtime"

func managedArtifact() string { return "sha256:" + strings.Repeat("b", 64) }

// managedCPA is a synthetic destination CPA: the exact managed posture, the
// management credential gate and the client-key projection the install and key
// operations read back.
type managedCPA struct {
	mu            sync.Mutex
	management    string
	keys          []string
	v8Reads       int
	keyWrites     int
	dirtyConfig   bool
	authOpen      bool
	extraRoots    map[string]any
	pluginsOn     bool
	fileLoggingOn bool
	authDir       string
	pluginDir     string
}

func (m *managedCPA) config() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]any, 0, len(m.keys))
	for _, k := range m.keys {
		keys = append(keys, k)
	}
	usage := true
	if m.dirtyConfig {
		usage = false
	}
	authDir := m.authDir
	if authDir == "" {
		authDir = "/runtime/supervisor/destination/auth"
	}
	pluginDir := m.pluginDir
	if pluginDir == "" {
		pluginDir = "/runtime/supervisor/plugins"
	}
	body := map[string]any{
		"config-version": 8,
		"server":         map[string]any{"host": "0.0.0.0", "port": 8317},
		"management": map[string]any{"allow-remote": true, "secret-key": managedBcryptSecret,
			"disable-control-panel": true, "disable-auto-update-panel": true},
		"access": map[string]any{"api-keys": keys},
		"oauth":  map[string]any{"auth-dir": authDir},
		// The Supervisor template always publishes both the switch and its own
		// plugin root: the root is a sibling of the destination directory so an
		// installed plugin survives a re-stage, and only this exact closed shape
		// is the managed posture.
		"plugins":       map[string]any{"enabled": false, "dir": pluginDir},
		"observability": map[string]any{"logs": map[string]any{"logging-to-file": m.fileLoggingOn}, "usage": map[string]any{"usage-statistics-enabled": usage}},
	}
	if m.pluginsOn {
		body["plugins"] = map[string]any{"enabled": true, "dir": pluginDir, "configs": map[string]any{"sample": map[string]any{"enabled": true}}}
	}
	for k, v := range m.extraRoots {
		body[k] = v
	}
	data, _ := json.Marshal(body)
	return data
}

func (m *managedCPA) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v8/") {
			if r.Header.Get("Authorization") != "Bearer "+m.management {
				w.WriteHeader(http.StatusForbidden)
				return
			}
		}
		switch {
		case r.URL.Path == "/v8/management/config" && r.Method == http.MethodGet:
			m.mu.Lock()
			m.v8Reads++
			m.mu.Unlock()
			_, _ = w.Write(m.config())
		case r.URL.Path == "/v8/management/config.yaml" && r.Method == http.MethodGet:
			_, _ = w.Write(m.config())
		case r.URL.Path == "/v8/management/config/access/api-keys" && r.Method == http.MethodGet:
			var keys []string
			m.mu.Lock()
			keys = slices.Clone(m.keys)
			m.mu.Unlock()
			if keys == nil {
				keys = []string{}
			}
			_ = json.NewEncoder(w).Encode(keys)
		case r.URL.Path == "/v8/management/config/access/api-keys" && r.Method == http.MethodPut:
			var keys []string
			if json.NewDecoder(r.Body).Decode(&keys) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			m.mu.Lock()
			m.keyWrites++
			m.keys = slices.Clone(keys)
			m.mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/v1/models":
			auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			m.mu.Lock()
			accepted := auth != "" && slices.Contains(m.keys, auth)
			open := m.authOpen && auth == ""
			m.mu.Unlock()
			if accepted || open {
				_, _ = w.Write([]byte(`{"data":[]}`))
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func managedFixture(t *testing.T) (*Service, *managedCPA, *ModelSource, string) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "manager.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = sqlite.Migrate(db); err != nil {
		t.Fatal(err)
	}
	r := repo.New(db)
	protector, _ := security.NewProtector([]byte("isolated-managed-test-key"))
	keys := freshkeys.New(r, protector)
	installed, _, err := keys.Create(t.Context(), strings.Repeat("d", 32))
	if err != nil {
		t.Fatal(err)
	}
	cpa := &managedCPA{management: strings.Repeat("e", 64)}
	server := cpa.serve(t)
	rt := &observedRuntime{status: model.RuntimeObservedStatus{Identity: managedRuntimeIdentity, Generation: 9,
		ProtocolVersion:       "v1",
		State:                 model.RuntimeStateReady,
		CPAObservedVersion:    "v8.0.55",
		ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: model.RuntimeArtifactID(managedArtifact()), Version: "v8.0.55"}}}
	source := &ModelSource{install: ManagedInstall{
		RunID: "managed:operation-default-install", Generation: "9", Identity: managedRuntimeIdentity,
		ArtifactID: managedArtifact(), BaseURL: server.URL, ManagementKey: cpa.management,
		Strategy: "new_managed",
	}}
	s := NewManaged(r, keys, rt, server.URL, source.get)
	return s, cpa, source, string(installed.Key.ID)
}

// ModelSource is the mutable install authority a test observes.
type ModelSource struct {
	mu      sync.Mutex
	install ManagedInstall
	err     error
}

func (m *ModelSource) get(context.Context) (ManagedInstall, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return ManagedInstall{}, m.err
	}
	return m.install, nil
}

func (m *ModelSource) set(fn func(*ManagedInstall)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(&m.install)
}

func (m *ModelSource) fail(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (c *managedCPA) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.keys)
}

func TestManagedInstallThenReplaceThroughInstallDerivedAuthority(t *testing.T) {
	s, cpa, _, installedID := managedFixture(t)
	state, err := s.Apply(t.Context(), installedID, 9)
	if err != nil || !state.ManagerStaged || !state.CPAConfigured || state.RuntimeState != "effective_observed" || !state.Attempted {
		t.Fatalf("managed install: %#v %v", state, err)
	}
	if len(cpa.snapshot()) != 1 {
		t.Fatalf("install wrote %v", cpa.snapshot())
	}
	observed, err := s.Observe(t.Context(), installedID, 9)
	if err != nil || observed.RuntimeState != "effective_observed" || !observed.CPAConfigured {
		t.Fatalf("managed observe: %#v %v", observed, err)
	}

	next, _, err := s.keys.Create(t.Context(), strings.Repeat("f", 32))
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("1", 32)
	if _, err = s.Prepare(t.Context(), id, installedID, string(next.Key.ID), "replace", 9); err != nil {
		t.Fatalf("managed prepare: %v", err)
	}
	// The step authority is derived from the real operation and the live
	// destination source; no Host permit exists on a default install.
	result, err := s.ContinueOperation(t.Context(), id, 9)
	if err != nil || result.NextStep != "revoke" || len(cpa.snapshot()) != 2 {
		t.Fatalf("managed add step: %#v %v keys=%v", result, err, cpa.snapshot())
	}
	result, err = s.ContinueOperation(t.Context(), id, 9)
	if err != nil || result.Operation.Outcome != "committed" || result.RuntimeState != "effective_observed" || len(cpa.snapshot()) != 1 || cpa.snapshot()[0] != next.CreatedSecret {
		t.Fatalf("managed revoke step: %#v %v keys=%v", result, err, cpa.snapshot())
	}
	if cpa.keyWrites != 3 {
		t.Fatalf("managed install+replace wrote %d times", cpa.keyWrites)
	}
	key, err := s.keys.Detail(t.Context(), installedID)
	if err != nil || key.Lifecycle != "superseded" {
		t.Fatalf("install key not superseded: %#v %v", key, err)
	}
}

func TestManagedLoneKeyRevocationIsRefused(t *testing.T) {
	s, cpa, _, installedID := managedFixture(t)
	if _, err := s.Apply(t.Context(), installedID, 9); err != nil {
		t.Fatal(err)
	}
	next, _, err := s.keys.Create(t.Context(), strings.Repeat("f", 32))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Prepare(t.Context(), strings.Repeat("2", 32), installedID, string(next.Key.ID), "revoke", 9)
	if !errors.Is(err, ErrLastKey) || cpa.keyWrites != 1 {
		t.Fatalf("lone key revocation accepted: %v writes=%d", err, cpa.keyWrites)
	}
}

func TestManagedAuthorityFailsClosedOnTargetDrift(t *testing.T) {
	for _, scenario := range []string{"identity", "zero-generation", "missing-protocol", "missing-artifact", "invalid-artifact", "invalid-engine", "ready", "source", "endpoint"} {
		t.Run(scenario, func(t *testing.T) {
			s, cpa, source, installedID := managedFixture(t)
			switch scenario {
			case "identity":
				s.runtime.(*observedRuntime).status.Identity = "foreign-runtime"
			case "zero-generation":
				s.runtime.(*observedRuntime).status.Generation = 0
			case "missing-protocol":
				s.runtime.(*observedRuntime).status.ProtocolVersion = ""
			case "missing-artifact":
				s.runtime.(*observedRuntime).status.ActiveGatewayArtifact = nil
			case "invalid-artifact":
				s.runtime.(*observedRuntime).status.ActiveGatewayArtifact.ArtifactID = "sha256:bad"
			case "invalid-engine":
				s.runtime.(*observedRuntime).status.ActiveGatewayArtifact.Engine = "envoy"
			case "ready":
				s.runtime.(*observedRuntime).status.State = model.RuntimeStateOffline
			case "source":
				source.fail(ErrUnavailable)
			case "endpoint":
				source.set(func(i *ManagedInstall) { i.BaseURL = "http://other-cpa:8317" })
			}
			_, err := s.Apply(t.Context(), installedID, 9)
			if err == nil || cpa.keyWrites != 0 || len(cpa.snapshot()) != 0 {
				t.Fatalf("drifted target wrote: %v keys=%v", err, cpa.snapshot())
			}
			switch scenario {
			case "identity":
				if !errors.Is(err, ErrGeneration) {
					t.Fatalf("drifted target code %v", err)
				}
			default:
				if !errors.Is(err, ErrUnavailable) {
					t.Fatalf("blocked target code %v", err)
				}
			}
			if state, observeErr := s.Observe(t.Context(), installedID, 9); observeErr == nil && state.CPAConfigured {
				t.Fatal("drifted target observed as configured")
			}
		})
	}
}

// A synthesized grant is exactly what the previous implementation issued: no
// operation, no step and no destination source. It must never authorize a step.
func TestSynthesizedManagedGrantDoesNotAuthorizeAStep(t *testing.T) {
	s, cpa, _, installedID := managedFixture(t)
	if _, err := s.Apply(t.Context(), installedID, 9); err != nil {
		t.Fatal(err)
	}
	next, _, err := s.keys.Create(t.Context(), strings.Repeat("f", 32))
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("3", 32)
	if _, err = s.Prepare(t.Context(), id, installedID, string(next.Key.ID), "replace", 9); err != nil {
		t.Fatal(err)
	}
	writes := cpa.keyWrites
	s.keyPermit = func(context.Context, string, string) (KeyPermit, error) {
		return KeyPermit{Version: 1, SourceID: s.ManagedEndpoint(), Revision: 1, OperationID: "managed-authority", Step: "managed",
			RunID: "managed:operation-default-install", Generation: "9", SourceSHA256: strings.Repeat("a", 64),
			IssuedAtMS: time.Now().UnixMilli(), ExpiresAtMS: time.Now().Add(time.Minute).UnixMilli()}, nil
	}
	if _, err = s.ContinueOperation(t.Context(), id, 9); !errors.Is(err, ErrUnavailable) || cpa.keyWrites != writes {
		t.Fatalf("synthesized grant authorized a write: %v", err)
	}
}

func TestManagedGrantIsRecheckedBeforeTheSingleWrite(t *testing.T) {
	s, cpa, source, installedID := managedFixture(t)
	if _, err := s.Apply(t.Context(), installedID, 9); err != nil {
		t.Fatal(err)
	}
	next, _, err := s.keys.Create(t.Context(), strings.Repeat("f", 32))
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("4", 32)
	if _, err = s.Prepare(t.Context(), id, installedID, string(next.Key.ID), "replace", 9); err != nil {
		t.Fatal(err)
	}
	writes := cpa.keyWrites
	// The committed installation is lost between the snapshot and the intent.
	source.fail(ErrUnavailable)
	if _, err = s.ContinueOperation(t.Context(), id, 9); !errors.Is(err, ErrUnavailable) || cpa.keyWrites != writes {
		t.Fatalf("lost authority still wrote: %v writes=%d", err, cpa.keyWrites)
	}
	operation, err := s.repo.Operation(t.Context(), id)
	if err != nil || operation.Steps[0].State == "attempted" {
		t.Fatalf("lost authority reserved intent: %#v %v", operation, err)
	}
}

// setKeys replaces the destination client-key projection. It stands in for a
// key written or removed outside this Manager so the predicate is never proven
// from Manager state alone.
func (c *managedCPA) setKeys(keys []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys = slices.Clone(keys)
}

func TestDefaultInstallCPAAuthorityProvenNeedsClientAuthAndExactTarget(t *testing.T) {
	var nilService *Service
	if nilService.DefaultInstallCPAAuthorityProven(t.Context()) {
		t.Fatal("nil authority proven")
	}
	// A private attested profile is not the default install authority.
	if New(nil, nil, nil).DefaultInstallCPAAuthorityProven(t.Context()) {
		t.Fatal("private profile proven as a default install")
	}
	s, cpa, _, installedID := managedFixture(t)
	// Committed installation, but the destination still accepts unauthenticated
	// Gateway traffic: client authentication is off.
	if s.DefaultInstallCPAAuthorityProven(t.Context()) {
		t.Fatal("proven without any installed client key")
	}
	if _, err := s.Apply(t.Context(), installedID, 9); err != nil {
		t.Fatal(err)
	}
	if !s.DefaultInstallCPAAuthorityProven(t.Context()) {
		t.Fatal("committed managed install with an installed client key not proven")
	}
	// Losing the only key reopens the Gateway; the predicate must follow the
	// destination, not Manager memory.
	installed := cpa.snapshot()
	cpa.setKeys(nil)
	if s.DefaultInstallCPAAuthorityProven(t.Context()) {
		t.Fatal("proven after the client key disappeared")
	}
	cpa.setKeys(installed)
	if !s.DefaultInstallCPAAuthorityProven(t.Context()) {
		t.Fatal("restored destination not proven")
	}
	for _, scenario := range []string{"identity", "zero-generation", "missing-protocol", "missing-artifact", "invalid-artifact", "invalid-engine", "ready", "source", "endpoint"} {
		t.Run(scenario, func(t *testing.T) {
			s, cpa, source, installedID := managedFixture(t)
			if _, err := s.Apply(t.Context(), installedID, 9); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "identity":
				s.runtime.(*observedRuntime).status.Identity = "foreign-runtime"
			case "zero-generation":
				s.runtime.(*observedRuntime).status.Generation = 0
			case "missing-protocol":
				s.runtime.(*observedRuntime).status.ProtocolVersion = ""
			case "missing-artifact":
				s.runtime.(*observedRuntime).status.ActiveGatewayArtifact = nil
			case "invalid-artifact":
				s.runtime.(*observedRuntime).status.ActiveGatewayArtifact.ArtifactID = "sha256:bad"
			case "invalid-engine":
				s.runtime.(*observedRuntime).status.ActiveGatewayArtifact.Engine = "envoy"
			case "ready":
				s.runtime.(*observedRuntime).status.State = model.RuntimeStateOffline
			case "source":
				source.fail(ErrUnavailable)
			case "endpoint":
				source.set(func(i *ManagedInstall) { i.BaseURL = "http://other-cpa:8317" })
			}
			writes := cpa.keyWrites
			if s.DefaultInstallCPAAuthorityProven(t.Context()) {
				t.Fatalf("%s drift proven", scenario)
			}
			if cpa.keyWrites != writes {
				t.Fatalf("%s drift wrote", scenario)
			}
		})
	}
}

func TestManagedAuthoritySurvivesSupervisorRestartUpdateAndOperatorEdits(t *testing.T) {
	s, cpa, _, installedID := managedFixture(t)
	if _, err := s.Apply(t.Context(), installedID, 9); err != nil {
		t.Fatal(err)
	}
	// Operator adds an AI provider, edits routing/requests, enables plugins, and enables file logging.
	cpa.mu.Lock()
	cpa.pluginsOn = true
	cpa.fileLoggingOn = true
	cpa.extraRoots = map[string]any{
		"api-keys": map[string]any{
			"openai-compatibility": []any{
				map[string]any{"name": "mock", "base-url": "http://127.0.0.1:38339/v1", "keys": []any{map[string]any{"api-key": "sk-mock"}}},
			},
		},
		"routing":  map[string]any{"strategy": "round-robin"},
		"requests": map[string]any{"passthrough-headers": true},
	}
	cpa.mu.Unlock()
	// Supervisor restarts with a new random generation and an updated CPA artifact.
	s.runtime.(*observedRuntime).status.Generation = 884219
	s.runtime.(*observedRuntime).status.ActiveGatewayArtifact.ArtifactID = model.RuntimeArtifactID("sha256:" + strings.Repeat("c", 64))

	if !s.DefaultInstallCPAAuthorityProven(t.Context()) {
		t.Fatal("managed admission lost after operator config edit and Supervisor restart")
	}
	if key, err := s.ConfigTarget(t.Context(), 9); err != nil || key == "" {
		t.Fatalf("ConfigTarget failed after Supervisor restart: %v", err)
	}
}

// The destination posture is part of the proof: a CPA that keeps the credential
// but drops client authentication or remote management is not admitted.
func TestDefaultInstallCPAAuthorityProvenRejectsDestinationPostureDrift(t *testing.T) {
	for _, scenario := range []string{"dirty", "forged-management", "untrusted-auth-suffix", "untrusted-plugin-suffix", "gateway-destination-auth"} {
		t.Run(scenario, func(t *testing.T) {
			s, cpa, _, installedID := managedFixture(t)
			if _, err := s.Apply(t.Context(), installedID, 9); err != nil {
				t.Fatal(err)
			}
			cpa.mu.Lock()
			switch scenario {
			case "dirty":
				cpa.dirtyConfig = true
			case "forged-management":
				cpa.management = strings.Repeat("z", 64)
			case "untrusted-auth-suffix":
				cpa.authDir = "/tmp/foreign/supervisor/destination/auth"
			case "untrusted-plugin-suffix":
				cpa.pluginDir = "/tmp/foreign/supervisor/plugins"
			case "gateway-destination-auth":
				cpa.authDir = "/runtime/gateway/destination/auth"
			}
			cpa.mu.Unlock()
			if s.DefaultInstallCPAAuthorityProven(t.Context()) {
				t.Fatalf("%s destination proven", scenario)
			}
		})
	}
}

func TestValidManagerOwnedDestinationCheckpoint(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for name, tc := range map[string]struct {
		cp   bootstrap.FreshStrategyCheckpoint
		want bool
	}{
		"committed new_managed with managed digest": {
			cp:   bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, Stage: "committed", DestinationConnectionIdentity: "managed:sha256:" + digest},
			want: true,
		},
		"committed adopt network_existing with external digest": {
			cp:   bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.AdoptExisting, SourceClass: bootstrap.AdoptNetworkExisting, Stage: "committed", DestinationConnectionIdentity: "external:sha256:" + digest},
			want: true,
		},
		"unmigrated new_managed without destination identity": {
			cp:   bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, Stage: "committed"},
			want: false,
		},
		"new_managed with wrong prefix": {
			cp:   bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, Stage: "committed", DestinationConnectionIdentity: "external:sha256:" + digest},
			want: false,
		},
		"new_managed with truncated digest": {
			cp:   bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, Stage: "committed", DestinationConnectionIdentity: "managed:sha256:abcd"},
			want: false,
		},
		"adopt legacy_managed excluded": {
			cp:   bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.AdoptExisting, SourceClass: bootstrap.AdoptLegacyManaged, Stage: "committed", DestinationConnectionIdentity: "external:sha256:" + digest},
			want: false,
		},
		"connect_external excluded": {
			cp:   bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.ConnectExternal, Stage: "committed", DestinationConnectionIdentity: "external:sha256:" + digest},
			want: false,
		},
	} {
		if got := ValidManagerOwnedDestinationCheckpoint(tc.cp); got != tc.want {
			t.Fatalf("%s: ValidManagerOwnedDestinationCheckpoint = %v, want %v", name, got, tc.want)
		}
	}
}
