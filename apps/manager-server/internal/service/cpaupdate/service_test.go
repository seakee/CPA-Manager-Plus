package cpaupdate

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

const testArtifactID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type memoryStore struct {
	mu        sync.Mutex
	data      []byte
	loadErr   error
	saveErr   error
	saveCalls atomic.Int32
	loadCalls atomic.Int32
}

func (s *memoryStore) LoadCPAUpdateCheck(context.Context) ([]byte, error) {
	s.loadCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.data...), s.loadErr
}

func (s *memoryStore) SaveCPAUpdateCheck(_ context.Context, data []byte) error {
	s.saveCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.data = append([]byte(nil), data...)
	return nil
}

type fakeSource struct {
	requests atomic.Int32
	target   string
	err      error
}

func (s *fakeSource) LatestStable(context.Context) (string, error) {
	s.requests.Add(1)
	return s.target, s.err
}

type observationOnlyRuntime struct {
	status      model.RuntimeObservedStatus
	statusErr   error
	statusCalls atomic.Int32
}

func (r *observationOnlyRuntime) Status(context.Context) (model.RuntimeObservedStatus, error) {
	r.statusCalls.Add(1)
	return r.status, r.statusErr
}

func (*observationOnlyRuntime) Start(context.Context, model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	panic("unexpected Runtime Start mutation")
}

func (*observationOnlyRuntime) Stop(context.Context, model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	panic("unexpected Runtime Stop mutation")
}

func (*observationOnlyRuntime) Restart(context.Context, model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	panic("unexpected Runtime Restart mutation")
}

func (*observationOnlyRuntime) PrepareUpdate(context.Context, model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
	panic("unexpected Runtime PrepareUpdate mutation")
}

func (*observationOnlyRuntime) ActivateUpdate(context.Context, model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
	panic("unexpected Runtime ActivateUpdate mutation")
}

func (*observationOnlyRuntime) ObserveUpdateOperation(context.Context, model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
	panic("recommendation must not observe update operations")
}

func embeddedObservation(version string, capabilities ...model.RuntimeCapability) model.RuntimeObservedStatus {
	return model.RuntimeObservedStatus{
		Identity:           "runtime-test",
		Generation:         1,
		ProtocolVersion:    "v1",
		State:              model.RuntimeStateReady,
		CPAObservedVersion: model.CPAObservedVersion(version),
		ActiveGatewayArtifact: &model.ActiveGatewayArtifact{
			Engine:     "cpa",
			ArtifactID: model.RuntimeArtifactID(testArtifactID),
			Version:    version,
		},
		Capabilities: capabilities,
	}
}

func persistedDiscovery(t *testing.T, target string, now time.Time) *memoryStore {
	t.Helper()
	data, err := json.Marshal(DiscoveryState{
		SchemaVersion: discoverySchemaVersion,
		LastAttemptAt: now,
		LastSuccessAt: now,
		TargetVersion: target,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &memoryStore{data: data}
}

func TestEmbeddedRecommendationStateMatrix(t *testing.T) {
	checkedAt := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	allCapabilities := []model.RuntimeCapability{
		model.RuntimeCapabilityPrepareUpdate,
		model.RuntimeCapabilityActivateUpdate,
	}
	tests := []struct {
		name         string
		current      string
		capabilities []model.RuntimeCapability
		wantState    StatusState
		wantAction   bool
	}{
		{name: "newer stable", current: "7.3.3", capabilities: allCapabilities, wantState: StateUpdateAvailable, wantAction: true},
		{name: "same version", current: "7.3.4", capabilities: allCapabilities, wantState: StateUpToDate},
		{name: "ahead of stable", current: "7.4.0", capabilities: allCapabilities, wantState: StateAheadOfStable},
		{name: "invalid current version", current: "7.3.4-custom", capabilities: allCapabilities, wantState: StateUnknownVersion},
		{name: "missing prepare capability", current: "7.3.3", capabilities: []model.RuntimeCapability{model.RuntimeCapabilityActivateUpdate}, wantState: StateUpdateAvailable},
		{name: "missing activate capability", current: "7.3.3", capabilities: []model.RuntimeCapability{model.RuntimeCapabilityPrepareUpdate}, wantState: StateUpdateAvailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeClient := &observationOnlyRuntime{status: embeddedObservation(test.current, test.capabilities...)}
			source := &fakeSource{target: "9.9.9"}
			service := New(persistedDiscovery(t, "7.3.4", checkedAt), runtimeClient, model.RuntimeModeEmbedded)
			service.source = source
			service.now = func() time.Time { return checkedAt }

			status, err := service.Status(t.Context())
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if status.State != test.wantState || status.Actionable != test.wantAction {
				t.Fatalf("Status() = %#v, want state=%q actionable=%v", status, test.wantState, test.wantAction)
			}
			if status.ActiveArtifactID != testArtifactID || status.CurrentVersion != test.current || status.TargetVersion != "7.3.4" {
				t.Fatalf("trusted projection lost exact identity: %#v", status)
			}
			if source.requests.Load() != 0 {
				t.Fatalf("GET status performed %d discovery requests", source.requests.Load())
			}
		})
	}
}

func TestDiscoveryFreshnessWindow(t *testing.T) {
	now := time.Date(2026, 9, 18, 16, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		lastSuccess time.Time
		wantStale   bool
		wantAction  bool
	}{
		{
			name:        "exactly at freshness limit",
			lastSuccess: now.Add(-discoveryFreshness),
			wantAction:  true,
		},
		{
			name:        "older than freshness limit",
			lastSuccess: now.Add(-discoveryFreshness - time.Nanosecond),
			wantStale:   true,
		},
		{
			name:        "success timestamp is in the future",
			lastSuccess: now.Add(time.Minute),
			wantStale:   true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := New(
				persistedDiscovery(t, "7.3.4", test.lastSuccess),
				&observationOnlyRuntime{status: embeddedObservation(
					"7.3.3",
					model.RuntimeCapabilityPrepareUpdate,
					model.RuntimeCapabilityActivateUpdate,
				)},
				model.RuntimeModeEmbedded,
			)
			service.now = func() time.Time { return now }

			status, err := service.Status(t.Context())
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if status.State != StateUpdateAvailable || status.Stale != test.wantStale || status.Actionable != test.wantAction {
				t.Fatalf("Status() = %#v, want state=%q stale=%v actionable=%v", status, StateUpdateAvailable, test.wantStale, test.wantAction)
			}
		})
	}
}

func TestMissingActiveArtifactIsUnsupported(t *testing.T) {
	runtimeClient := &observationOnlyRuntime{status: model.RuntimeObservedStatus{
		State:              model.RuntimeStateReady,
		CPAObservedVersion: "7.3.3",
		Capabilities: model.RuntimeCapabilities{
			model.RuntimeCapabilityPrepareUpdate,
			model.RuntimeCapabilityActivateUpdate,
		},
	}}
	status, err := New(
		persistedDiscovery(t, "7.3.4", time.Now().UTC()),
		runtimeClient,
		model.RuntimeModeEmbedded,
	).Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateUnsupported || status.Actionable || status.CurrentVersion != "" || status.ActiveArtifactID != "" {
		t.Fatalf("untrusted observation projected as usable: %#v", status)
	}
}

func TestPersistenceFailureNeverExposesUndurableRecommendation(t *testing.T) {
	clock := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	store := &memoryStore{saveErr: errors.New("disk full")}
	service := New(
		store,
		&observationOnlyRuntime{status: embeddedObservation(
			"7.3.3",
			model.RuntimeCapabilityPrepareUpdate,
			model.RuntimeCapabilityActivateUpdate,
		)},
		model.RuntimeModeEmbedded,
	)
	service.source = &fakeSource{target: "7.3.4"}
	service.now = func() time.Time { return clock }

	if _, err := service.Check(t.Context()); err == nil {
		t.Fatal("Check() accepted persistence failure")
	}
	store.mu.Lock()
	store.saveErr = nil
	store.mu.Unlock()
	if _, err := service.Status(t.Context()); err == nil {
		t.Fatal("Status() exposed an undurable recommendation")
	}
	status, err := service.Check(t.Context())
	if err != nil || status.State != StateUpdateAvailable || !status.Actionable {
		t.Fatalf("durability retry Check() = %#v, %v", status, err)
	}
}

func TestExternalRuntimeIsManagedExternally(t *testing.T) {
	runtimeClient := &observationOnlyRuntime{status: model.RuntimeObservedStatus{
		State:              model.RuntimeStateReady,
		CPAObservedVersion: "7.3.3",
		Capabilities: model.RuntimeCapabilities{
			model.RuntimeCapabilityPrepareUpdate,
			model.RuntimeCapabilityActivateUpdate,
		},
	}}
	status, err := New(
		persistedDiscovery(t, "7.3.4", time.Now().UTC()),
		runtimeClient,
		model.RuntimeModeExternal,
	).Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateManagedExternally || status.Actionable || status.PrepareSupported || status.ActivateSupported {
		t.Fatalf("External Runtime gained mutation semantics: %#v", status)
	}
	if status.CurrentVersion != "7.3.3" || status.ActiveArtifactID != "" {
		t.Fatalf("External observation projection = %#v", status)
	}
}

func TestStatusNeverPerformsDiscoveryNetwork(t *testing.T) {
	source := &fakeSource{err: errors.New("must not be called")}
	service := New(
		&memoryStore{},
		&observationOnlyRuntime{status: embeddedObservation("7.3.3")},
		model.RuntimeModeEmbedded,
	)
	service.source = source
	status, err := service.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateNeverChecked || !status.Stale || status.Actionable {
		t.Fatalf("never-checked Status() = %#v", status)
	}
	if source.requests.Load() != 0 {
		t.Fatalf("Status() discovery requests = %d, want 0", source.requests.Load())
	}
}

func TestCheckCooldownUsesOneDiscoveryAndFreshRuntimeObservation(t *testing.T) {
	clock := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	source := &fakeSource{target: "7.3.4"}
	runtimeClient := &observationOnlyRuntime{status: embeddedObservation(
		"7.3.3",
		model.RuntimeCapabilityPrepareUpdate,
		model.RuntimeCapabilityActivateUpdate,
	)}
	service := New(&memoryStore{}, runtimeClient, model.RuntimeModeEmbedded)
	service.source = source
	service.now = func() time.Time { return clock }

	for range 2 {
		status, err := service.Check(t.Context())
		if err != nil || status.State != StateUpdateAvailable || !status.Actionable {
			t.Fatalf("Check() = %#v, %v", status, err)
		}
	}
	if source.requests.Load() != 1 {
		t.Fatalf("discovery requests = %d, want 1", source.requests.Load())
	}
	if runtimeClient.statusCalls.Load() != 2 {
		t.Fatalf("Runtime Status calls = %d, want fresh observation for both checks", runtimeClient.statusCalls.Load())
	}
}

func TestDiscoveryFailureKeepsCacheStaleAndNonActionable(t *testing.T) {
	previous := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	store := persistedDiscovery(t, "7.3.4", previous)
	service := New(
		store,
		&observationOnlyRuntime{status: embeddedObservation(
			"7.3.3",
			model.RuntimeCapabilityPrepareUpdate,
			model.RuntimeCapabilityActivateUpdate,
		)},
		model.RuntimeModeEmbedded,
	)
	service.source = &fakeSource{err: errors.New("official source unavailable")}
	service.now = func() time.Time { return previous.Add(2 * time.Minute) }

	status, err := service.Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateUpdateAvailable || status.TargetVersion != "7.3.4" || status.LastError == "" || !status.Stale || status.Actionable {
		t.Fatalf("failed discovery projection = %#v", status)
	}
	var persisted DiscoveryState
	if err := json.Unmarshal(store.data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.TargetVersion != "7.3.4" || persisted.LastSuccessAt != previous || persisted.LastError == "" {
		t.Fatalf("failed discovery persistence = %#v", persisted)
	}
}

func TestPersistedDiscoveryFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	tests := map[string][]byte{
		"invalid JSON":        []byte(`{`),
		"unknown schema":      []byte(`{"schema_version":2}`),
		"invalid version":     []byte(`{"schema_version":1,"last_attempt_at":"` + now.Format(time.RFC3339Nano) + `","last_success_at":"` + now.Format(time.RFC3339Nano) + `","target_version":"v7.3.4"}`),
		"unknown field":       []byte(`{"schema_version":1,"unexpected":true}`),
		"trailing JSON value": []byte(`{"schema_version":1} {}`),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			runtimeClient := &observationOnlyRuntime{status: embeddedObservation("7.3.3")}
			_, err := New(&memoryStore{data: data}, runtimeClient, model.RuntimeModeEmbedded).Status(t.Context())
			if err == nil {
				t.Fatal("Status() accepted corrupt persisted state")
			}
			if runtimeClient.statusCalls.Load() != 0 {
				t.Fatal("Runtime observation ran after persisted-state failure")
			}
		})
	}
}

func TestRuntimeObservationFailureIsReturned(t *testing.T) {
	want := errors.New("Runtime authentication failed")
	_, err := New(
		&memoryStore{},
		&observationOnlyRuntime{statusErr: want},
		model.RuntimeModeEmbedded,
	).Status(t.Context())
	if !errors.Is(err, want) {
		t.Fatalf("Status() error = %v, want wrapped Runtime error", err)
	}
}

func TestCheckFailsClosedOnInvalidModeBeforeDiscoveryOrPersistence(t *testing.T) {
	previous := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		mode     model.RuntimeMode
		resolver func() model.RuntimeMode
		seeded   bool
	}{
		{name: "empty static mode on empty store", mode: ""},
		{name: "invalid static mode on seeded store", mode: model.RuntimeMode("unsupported"), seeded: true},
		{
			name:     "invalid dynamic mode resolver on seeded store",
			mode:     model.RuntimeModeEmbedded,
			resolver: func() model.RuntimeMode { return "" },
			seeded:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryStore{}
			if tc.seeded {
				store = persistedDiscovery(t, "7.3.4", previous)
			}
			before := append([]byte(nil), store.data...)
			source := &fakeSource{target: "7.3.5"}
			runtimeClient := &observationOnlyRuntime{status: embeddedObservation("7.3.3")}
			service := New(store, runtimeClient, tc.mode)
			if tc.resolver != nil {
				service.WithModeResolver(tc.resolver)
			}
			service.source = source
			service.now = func() time.Time { return previous.Add(10 * time.Minute) }

			if _, err := service.Check(t.Context()); err == nil {
				t.Fatal("Check() accepted invalid Runtime mode")
			}
			if source.requests.Load() != 0 {
				t.Fatalf("Check() discovery requests = %d, want 0", source.requests.Load())
			}
			if store.saveCalls.Load() != 0 {
				t.Fatalf("Check() SaveCPAUpdateCheck calls = %d, want 0", store.saveCalls.Load())
			}
			if runtimeClient.statusCalls.Load() != 0 {
				t.Fatalf("Check() Runtime Status calls = %d, want 0", runtimeClient.statusCalls.Load())
			}
			if string(store.data) != string(before) {
				t.Fatalf("Check() mutated persisted cpa_update_check_v1: got %q, want %q", store.data, before)
			}
		})
	}
}

func TestStatusFailsClosedOnInvalidModeBeforeLoadingStore(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     model.RuntimeMode
		resolver func() model.RuntimeMode
	}{
		{name: "invalid static mode", mode: model.RuntimeMode("unsupported")},
		{
			name:     "invalid dynamic mode resolver",
			mode:     model.RuntimeModeEmbedded,
			resolver: func() model.RuntimeMode { return "" },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryStore{loadErr: errors.New("unexpected LoadCPAUpdateCheck")}
			runtimeClient := &observationOnlyRuntime{status: embeddedObservation("7.3.3")}
			service := New(store, runtimeClient, tc.mode)
			if tc.resolver != nil {
				service.WithModeResolver(tc.resolver)
			}
			if _, err := service.Status(t.Context()); err == nil || err.Error() != "CPA update Runtime mode is invalid" {
				t.Fatalf("Status() error = %v, want invalid mode error", err)
			}
			if got := store.loadCalls.Load(); got != 0 {
				t.Fatalf("Status() LoadCPAUpdateCheck calls = %d, want 0", got)
			}
			if got := runtimeClient.statusCalls.Load(); got != 0 {
				t.Fatalf("Status() Runtime Status calls = %d, want 0", got)
			}
		})
	}
}
