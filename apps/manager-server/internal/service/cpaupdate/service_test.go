package cpaupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
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
		{name: "newer stable", current: "8.3.3", capabilities: allCapabilities, wantState: StateUpdateAvailable, wantAction: true},
		{name: "same version", current: "8.3.4", capabilities: allCapabilities, wantState: StateUpToDate},
		{name: "ahead of stable", current: "8.4.0", capabilities: allCapabilities, wantState: StateAheadOfStable},
		{name: "invalid current version", current: "8.3.4-custom", capabilities: allCapabilities, wantState: StateUnknownVersion},
		{name: "missing prepare capability", current: "8.3.3", capabilities: []model.RuntimeCapability{model.RuntimeCapabilityActivateUpdate}, wantState: StateUpdateAvailable},
		{name: "missing activate capability", current: "8.3.3", capabilities: []model.RuntimeCapability{model.RuntimeCapabilityPrepareUpdate}, wantState: StateUpdateAvailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeClient := &observationOnlyRuntime{status: embeddedObservation(test.current, test.capabilities...)}
			source := &fakeSource{target: "9.9.9"}
			service := New(persistedDiscovery(t, "8.3.4", checkedAt), runtimeClient, model.RuntimeModeEmbedded).WithProductCompleteProbe(completedInstallProbe)
			service.source = source
			service.now = func() time.Time { return checkedAt }

			status, err := service.Status(t.Context())
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if status.State != test.wantState || status.Actionable != test.wantAction {
				t.Fatalf("Status() = %#v, want state=%q actionable=%v", status, test.wantState, test.wantAction)
			}
			if status.ActiveArtifactID != testArtifactID || status.CurrentVersion != test.current || status.TargetVersion != "8.3.4" {
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
				persistedDiscovery(t, "8.3.4", test.lastSuccess),
				&observationOnlyRuntime{status: embeddedObservation(
					"8.3.3",
					model.RuntimeCapabilityPrepareUpdate,
					model.RuntimeCapabilityActivateUpdate,
				)},
				model.RuntimeModeEmbedded,
			).WithProductCompleteProbe(completedInstallProbe)
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
		CPAObservedVersion: "8.3.3",
		Capabilities: model.RuntimeCapabilities{
			model.RuntimeCapabilityPrepareUpdate,
			model.RuntimeCapabilityActivateUpdate,
		},
	}}
	status, err := New(
		persistedDiscovery(t, "8.3.4", time.Now().UTC()),
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
			"8.3.3",
			model.RuntimeCapabilityPrepareUpdate,
			model.RuntimeCapabilityActivateUpdate,
		)},
		model.RuntimeModeEmbedded,
	).WithProductCompleteProbe(completedInstallProbe)
	service.source = &fakeSource{target: "8.3.4"}
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
		CPAObservedVersion: "8.3.3",
		Capabilities: model.RuntimeCapabilities{
			model.RuntimeCapabilityPrepareUpdate,
			model.RuntimeCapabilityActivateUpdate,
		},
	}}
	status, err := New(
		persistedDiscovery(t, "8.3.4", time.Now().UTC()),
		runtimeClient,
		model.RuntimeModeExternal,
	).Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateManagedExternally || status.Actionable || status.PrepareSupported || status.ActivateSupported {
		t.Fatalf("External Runtime gained mutation semantics: %#v", status)
	}
	if status.CurrentVersion != "8.3.3" || status.ActiveArtifactID != "" {
		t.Fatalf("External observation projection = %#v", status)
	}
}

func TestStatusNeverPerformsDiscoveryNetwork(t *testing.T) {
	source := &fakeSource{err: errors.New("must not be called")}
	service := New(
		&memoryStore{},
		&observationOnlyRuntime{status: embeddedObservation("8.3.3")},
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
	source := &fakeSource{target: "8.3.4"}
	runtimeClient := &observationOnlyRuntime{status: embeddedObservation(
		"8.3.3",
		model.RuntimeCapabilityPrepareUpdate,
		model.RuntimeCapabilityActivateUpdate,
	)}
	service := New(&memoryStore{}, runtimeClient, model.RuntimeModeEmbedded).WithProductCompleteProbe(completedInstallProbe)
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

func TestUnsupportedDiscoveryMajorClearsRecommendation(t *testing.T) {
	now := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	client := &observationOnlyRuntime{status: embeddedObservation("8.0.55", model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate)}
	for _, tc := range []struct {
		name       string
		cached     string
		liveTarget string
	}{
		{name: "cached 8.0.56 then live 9.0.0", cached: "8.0.56", liveTarget: "9.0.0"},
		{name: "cached 9.0.0 then live 9.0.0", cached: "9.0.0", liveTarget: "9.0.0"},
		{name: "cached 8.0.54 below floor", cached: "8.0.54", liveTarget: "8.0.54"},
		{name: "cached 8.0.56 then live 8.0.54 below floor", cached: "8.0.56", liveTarget: "8.0.54"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := persistedDiscovery(t, tc.cached, now.Add(-time.Hour))
			service := New(store, client, model.RuntimeModeEmbedded).WithProductCompleteProbe(completedInstallProbe)
			service.source = &fakeSource{target: tc.liveTarget}
			service.now = func() time.Time { return now }
			assertBlocked := func(got Status, err error) {
				t.Helper()
				if err != nil || got.State != StateUnsupported || got.TargetVersion != "" || got.Actionable || got.LastError != errUnsupportedCPAMajor.Error() {
					t.Fatalf("unsupported target = %#v, %v", got, err)
				}
			}
			if tc.cached == "9.0.0" || tc.cached == "8.0.54" {
				assertBlocked(service.Status(t.Context()))
			}
			assertBlocked(service.Check(t.Context()))
			restarted := New(store, client, model.RuntimeModeEmbedded)
			assertBlocked(restarted.Status(t.Context()))
		})
	}
}

func TestMinimumSupportedCPAVersionFloorAllows8055And8056(t *testing.T) {
	now := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	allCaps := []model.RuntimeCapability{
		model.RuntimeCapabilityPrepareUpdate,
		model.RuntimeCapabilityActivateUpdate,
	}
	for _, tc := range []struct {
		current    string
		target     string
		wantState  StatusState
		wantAction bool
	}{
		{current: "8.0.55", target: "8.0.55", wantState: StateUpToDate, wantAction: false},
		{current: "8.0.54", target: "8.0.55", wantState: StateUpdateAvailable, wantAction: true},
		{current: "8.0.55", target: "8.0.56", wantState: StateUpdateAvailable, wantAction: true},
	} {
		t.Run(tc.current+"->"+tc.target, func(t *testing.T) {
			client := &observationOnlyRuntime{status: embeddedObservation(tc.current, allCaps...)}
			cachedService := New(persistedDiscovery(t, tc.target, now), client, model.RuntimeModeEmbedded).WithProductCompleteProbe(completedInstallProbe)
			cachedService.now = func() time.Time { return now }
			gotCached, err := cachedService.Status(t.Context())
			if err != nil || gotCached.State != tc.wantState || gotCached.Actionable != tc.wantAction || gotCached.TargetVersion != tc.target {
				t.Fatalf("cached Status() = %#v, %v", gotCached, err)
			}

			liveStore := &memoryStore{}
			liveService := New(liveStore, client, model.RuntimeModeEmbedded).WithProductCompleteProbe(completedInstallProbe)
			liveService.source = &fakeSource{target: tc.target}
			liveService.now = func() time.Time { return now }
			gotLive, err := liveService.Check(t.Context())
			if err != nil || gotLive.State != tc.wantState || gotLive.Actionable != tc.wantAction || gotLive.TargetVersion != tc.target {
				t.Fatalf("live Check() = %#v, %v", gotLive, err)
			}
		})
	}
}

func TestDiscoveryFailureKeepsCacheStaleAndNonActionable(t *testing.T) {
	previous := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	store := persistedDiscovery(t, "8.3.4", previous)
	service := New(
		store,
		&observationOnlyRuntime{status: embeddedObservation(
			"8.3.3",
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
	if status.State != StateUpdateAvailable || status.TargetVersion != "8.3.4" || status.LastError == "" || !status.Stale || status.Actionable {
		t.Fatalf("failed discovery projection = %#v", status)
	}
	var persisted DiscoveryState
	if err := json.Unmarshal(store.data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.TargetVersion != "8.3.4" || persisted.LastSuccessAt != previous || persisted.LastError == "" {
		t.Fatalf("failed discovery persistence = %#v", persisted)
	}
}

func TestPersistedDiscoveryFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	tests := map[string][]byte{
		"invalid JSON":        []byte(`{`),
		"unknown schema":      []byte(`{"schema_version":2}`),
		"invalid version":     []byte(`{"schema_version":1,"last_attempt_at":"` + now.Format(time.RFC3339Nano) + `","last_success_at":"` + now.Format(time.RFC3339Nano) + `","target_version":"v8.3.4"}`),
		"unknown field":       []byte(`{"schema_version":1,"unexpected":true}`),
		"trailing JSON value": []byte(`{"schema_version":1} {}`),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			runtimeClient := &observationOnlyRuntime{status: embeddedObservation("8.3.3")}
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
				store = persistedDiscovery(t, "8.3.4", previous)
			}
			before := append([]byte(nil), store.data...)
			source := &fakeSource{target: "8.3.5"}
			runtimeClient := &observationOnlyRuntime{status: embeddedObservation("8.3.3")}
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
			runtimeClient := &observationOnlyRuntime{status: embeddedObservation("8.3.3")}
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

func TestActivateReceiptRequiresExactAppliedArtifactAndBoundsCache(t *testing.T) {
	const testArtifactIDC = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	allCaps := []model.RuntimeCapability{
		model.RuntimeCapabilityPrepareUpdate,
		model.RuntimeCapabilityActivateUpdate,
	}

	t.Run("retry after successful Activate returns already_applied without extra ActivateUpdate RPC", func(t *testing.T) {
		runtimeClient := &mutationRuntime{status: mutationReadyStatus("8.3.3", testArtifactID, allCaps...)}
		runtimeClient.activateFn = func(req model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
			runtimeClient.mu.Lock()
			runtimeClient.status = mutationReadyStatus("8.3.7", testArtifactIDB, allCaps...)
			runtimeClient.mu.Unlock()
			return successfulOperation(req.RuntimeMutationRequest, model.RuntimeOperationActivateUpdate), nil
		}
		service, _, _ := mutationTestService(t, runtimeClient)
		req := validMutationRequest()

		first, err := service.Activate(t.Context(), req)
		if err != nil || first.AlreadyApplied || first.State != string(model.RuntimeOperationSucceeded) {
			t.Fatalf("initial Activate() = %#v, %v", first, err)
		}
		if runtimeClient.activateCalls.Load() != 1 {
			t.Fatalf("activateCalls = %d, want 1", runtimeClient.activateCalls.Load())
		}

		retry, err := service.Activate(t.Context(), req)
		if err != nil || !retry.AlreadyApplied || retry.State != string(model.RuntimeOperationSucceeded) {
			t.Fatalf("retry Activate() = %#v, %v", retry, err)
		}
		if runtimeClient.activateCalls.Load() != 1 {
			t.Fatalf("retry activateCalls = %d, want 1", runtimeClient.activateCalls.Load())
		}
	})

	t.Run("same-version replacement with different ArtifactID misses cached receipt and fails closed", func(t *testing.T) {
		runtimeClient := &mutationRuntime{status: mutationReadyStatus("8.3.3", testArtifactID, allCaps...)}
		activatedOnce := false
		runtimeClient.activateFn = func(req model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
			if !activatedOnce {
				activatedOnce = true
				runtimeClient.mu.Lock()
				runtimeClient.status = mutationReadyStatus("8.3.7", testArtifactIDB, allCaps...)
				runtimeClient.mu.Unlock()
				return successfulOperation(req.RuntimeMutationRequest, model.RuntimeOperationActivateUpdate), nil
			}
			return model.RuntimeOperationResult{}, &runtimeservice.ProtocolError{
				HTTPStatus: 409,
				Code:       runtimeservice.ProtocolErrorActiveArtifactMismatch,
			}
		}
		service, _, _ := mutationTestService(t, runtimeClient)
		req := validMutationRequest()

		if _, err := service.Activate(t.Context(), req); err != nil {
			t.Fatalf("initial Activate() failed: %v", err)
		}
		// Replace active binary with another 8.3.7 artifact having a different digest.
		runtimeClient.mu.Lock()
		runtimeClient.status = mutationReadyStatus("8.3.7", testArtifactIDC, allCaps...)
		runtimeClient.mu.Unlock()

		got, err := service.Activate(t.Context(), req)
		kind, code, ok := MutationErrorDetails(err)
		if !ok || kind != MutationErrorConflict || code != string(runtimeservice.ProtocolErrorActiveArtifactMismatch) || got.AlreadyApplied {
			t.Fatalf("Activate() after same-version digest change = (%#v, %v, %q, %q), want conflict/active_artifact_mismatch", got, err, kind, code)
		}
	})

	t.Run("transient post-activate Status failure never caches wildcard receipt", func(t *testing.T) {
		var statusCall int32
		runtimeClient := &mutationRuntime{status: mutationReadyStatus("8.3.3", testArtifactID, allCaps...)}
		activatedOnce := false
		runtimeClient.activateFn = func(req model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
			if !activatedOnce {
				activatedOnce = true
				runtimeClient.mu.Lock()
				runtimeClient.statusErr = errors.New("transient status error")
				runtimeClient.mu.Unlock()
				return successfulOperation(req.RuntimeMutationRequest, model.RuntimeOperationActivateUpdate), nil
			}
			return model.RuntimeOperationResult{}, &runtimeservice.ProtocolError{
				HTTPStatus: 409,
				Code:       runtimeservice.ProtocolErrorActiveArtifactMismatch,
			}
		}
		service, _, _ := mutationTestService(t, runtimeClient)
		req := validMutationRequest()

		if _, err := service.Activate(t.Context(), req); err != nil {
			t.Fatalf("initial Activate() failed: %v", err)
		}
		statusCall = runtimeClient.statusCalls.Load()
		if statusCall != 2 {
			t.Fatalf("statusCalls = %d, want 2", statusCall)
		}
		service.mu.Lock()
		cachedCount := len(service.appliedActivations)
		service.mu.Unlock()
		if cachedCount != 0 {
			t.Fatalf("cached receipts after failed postStatus = %d, want 0", cachedCount)
		}

		// Restore status with a different 8.3.7 digest: must NOT wildcard-match.
		runtimeClient.mu.Lock()
		runtimeClient.statusErr = nil
		runtimeClient.status = mutationReadyStatus("8.3.7", testArtifactIDC, allCaps...)
		runtimeClient.mu.Unlock()
		if got, err := service.Activate(t.Context(), req); err == nil || got.AlreadyApplied {
			t.Fatalf("Activate() wildcard-matched unverified digest: %#v, %v", got, err)
		}
	})

	t.Run("receipt cache bounded to 64 entries across 80 activations and evicts changed runtimeIdentity", func(t *testing.T) {
		runtimeClient := &mutationRuntime{}
		service, _, _ := mutationTestService(t, runtimeClient)

		for i := range 80 {
			prevID := model.RuntimeArtifactID(fmt.Sprintf("sha256:%064x", i+1))
			nextID := model.RuntimeArtifactID(fmt.Sprintf("sha256:%064x", i+1001))
			runtimeClient.mu.Lock()
			runtimeClient.status = mutationReadyStatus("8.3.3", string(prevID), allCaps...)
			runtimeClient.activateFn = func(req model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
				runtimeClient.mu.Lock()
				runtimeClient.status = mutationReadyStatus("8.3.7", string(nextID), allCaps...)
				runtimeClient.mu.Unlock()
				return successfulOperation(req.RuntimeMutationRequest, model.RuntimeOperationActivateUpdate), nil
			}
			runtimeClient.mu.Unlock()

			req := MutationRequest{
				RequestID:                fmt.Sprintf("req-%03d", i),
				TargetVersion:            "8.3.7",
				ExpectedActiveArtifactID: prevID,
			}
			if _, err := service.Activate(t.Context(), req); err != nil {
				t.Fatalf("Activate(%d) failed: %v", i, err)
			}
			service.mu.Lock()
			mapLen := len(service.appliedActivations)
			orderLen := len(service.appliedActivationOrder)
			service.mu.Unlock()
			if mapLen > maxAppliedActivationReceipts || orderLen > maxAppliedActivationReceipts || mapLen != orderLen {
				t.Fatalf("after %d activations: mapLen=%d orderLen=%d (cap=%d)", i+1, mapLen, orderLen, maxAppliedActivationReceipts)
			}
		}

		service.mu.Lock()
		if len(service.appliedActivations) != maxAppliedActivationReceipts {
			service.mu.Unlock()
			t.Fatalf("final receipt count = %d, want %d", len(service.appliedActivations), maxAppliedActivationReceipts)
		}
		_, hasOldest := service.appliedActivations[runtimeOperationID(MutationPhaseActivate, "req-000")]
		_, hasEvictedBoundary := service.appliedActivations[runtimeOperationID(MutationPhaseActivate, "req-015")]
		_, hasRetainedBoundary := service.appliedActivations[runtimeOperationID(MutationPhaseActivate, "req-016")]
		_, hasNewest := service.appliedActivations[runtimeOperationID(MutationPhaseActivate, "req-079")]
		service.mu.Unlock()
		if hasOldest || hasEvictedBoundary || !hasRetainedBoundary || !hasNewest {
			t.Fatalf("unexpected FIFO eviction state: req-000=%v req-015=%v req-016=%v req-079=%v",
				hasOldest, hasEvictedBoundary, hasRetainedBoundary, hasNewest)
		}

		// Switching runtimeIdentity evicts all receipts from the prior runtimeIdentity.
		prevID := model.RuntimeArtifactID(fmt.Sprintf("sha256:%064x", 9001))
		nextID := model.RuntimeArtifactID(fmt.Sprintf("sha256:%064x", 9002))
		newIdentityStatus := mutationReadyStatus("8.3.3", string(prevID), allCaps...)
		newIdentityStatus.Identity = "runtime-reinstalled"
		runtimeClient.mu.Lock()
		runtimeClient.status = newIdentityStatus
		runtimeClient.activateFn = func(req model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
			applied := mutationReadyStatus("8.3.7", string(nextID), allCaps...)
			applied.Identity = "runtime-reinstalled"
			runtimeClient.mu.Lock()
			runtimeClient.status = applied
			runtimeClient.mu.Unlock()
			return successfulOperation(req.RuntimeMutationRequest, model.RuntimeOperationActivateUpdate), nil
		}
		runtimeClient.mu.Unlock()
		if _, err := service.Activate(t.Context(), MutationRequest{
			RequestID:                "req-new-identity",
			TargetVersion:            "8.3.7",
			ExpectedActiveArtifactID: prevID,
		}); err != nil {
			t.Fatalf("Activate on new identity failed: %v", err)
		}
		service.mu.Lock()
		afterIdentityChange := len(service.appliedActivations)
		orderAfterIdentityChange := len(service.appliedActivationOrder)
		service.mu.Unlock()
		if afterIdentityChange != 1 || orderAfterIdentityChange != 1 {
			t.Fatalf("after runtimeIdentity change: mapLen=%d orderLen=%d, want 1", afterIdentityChange, orderAfterIdentityChange)
		}
	})

	t.Run("post-activate generation mismatch or non-ready state never caches receipt", func(t *testing.T) {
		for _, tc := range []struct {
			name       string
			postMutate func(*model.RuntimeObservedStatus)
		}{
			{
				name: "generation changed before postStatus",
				postMutate: func(st *model.RuntimeObservedStatus) {
					st.Generation++
				},
			},
			{
				name: "postStatus not ready",
				postMutate: func(st *model.RuntimeObservedStatus) {
					st.State = model.RuntimeStateStarting
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				runtimeClient := &mutationRuntime{status: mutationReadyStatus("8.3.3", testArtifactID, allCaps...)}
				activatedOnce := false
				runtimeClient.activateFn = func(req model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
					if !activatedOnce {
						activatedOnce = true
						next := mutationReadyStatus("8.3.7", testArtifactIDB, allCaps...)
						tc.postMutate(&next)
						runtimeClient.mu.Lock()
						runtimeClient.status = next
						runtimeClient.mu.Unlock()
						return successfulOperation(req.RuntimeMutationRequest, model.RuntimeOperationActivateUpdate), nil
					}
					return model.RuntimeOperationResult{}, &runtimeservice.ProtocolError{
						HTTPStatus: 409,
						Code:       runtimeservice.ProtocolErrorActiveArtifactMismatch,
					}
				}
				service, _, _ := mutationTestService(t, runtimeClient)
				req := validMutationRequest()

				first, err := service.Activate(t.Context(), req)
				if err != nil || first.AlreadyApplied || first.State != string(model.RuntimeOperationSucceeded) {
					t.Fatalf("initial Activate() = %#v, %v", first, err)
				}
				service.mu.Lock()
				cachedCount := len(service.appliedActivations)
				service.mu.Unlock()
				if cachedCount != 0 {
					t.Fatalf("cached receipts = %d, want 0", cachedCount)
				}

				// Even when status later reports ready on generation 42 with testArtifactIDB,
				// retry must delegate to ActivateUpdate because no receipt was cached.
				runtimeClient.mu.Lock()
				runtimeClient.status = mutationReadyStatus("8.3.7", testArtifactIDB, allCaps...)
				runtimeClient.mu.Unlock()
				got, err := service.Activate(t.Context(), req)
				if err == nil || got.AlreadyApplied || runtimeClient.activateCalls.Load() != 2 {
					t.Fatalf("retry Activate() = %#v, %v, activateCalls=%d; want delegated conflict", got, err, runtimeClient.activateCalls.Load())
				}
			})
		}
	})

	t.Run("generation change misses in-memory receipt and delegates to durable ActivateUpdate replay", func(t *testing.T) {
		runtimeClient := &mutationRuntime{status: mutationReadyStatus("8.3.3", testArtifactID, allCaps...)}
		runtimeClient.activateFn = func(req model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
			switch runtimeClient.activateCalls.Load() {
			case 1:
				runtimeClient.mu.Lock()
				runtimeClient.status = mutationReadyStatus("8.3.7", testArtifactIDB, allCaps...)
				runtimeClient.mu.Unlock()
				return successfulOperation(req.RuntimeMutationRequest, model.RuntimeOperationActivateUpdate), nil
			case 2:
				// Durable replay in generation 43 returns the retained generation-42 record.
				if req.ExpectedRuntimeGeneration != 43 {
					t.Fatalf("ExpectedRuntimeGeneration = %d, want 43", req.ExpectedRuntimeGeneration)
				}
				op := successfulOperation(req.RuntimeMutationRequest, model.RuntimeOperationActivateUpdate)
				op.RuntimeGeneration = 42
				return op, nil
			default:
				return model.RuntimeOperationResult{}, &runtimeservice.ProtocolError{
					HTTPStatus: 409,
					Code:       runtimeservice.ProtocolErrorActiveArtifactMismatch,
				}
			}
		}
		service, _, _ := mutationTestService(t, runtimeClient)
		req := validMutationRequest()

		if _, err := service.Activate(t.Context(), req); err != nil {
			t.Fatalf("initial Activate() failed: %v", err)
		}
		if retrySameGen, err := service.Activate(t.Context(), req); err != nil || !retrySameGen.AlreadyApplied || runtimeClient.activateCalls.Load() != 1 {
			t.Fatalf("same-generation retry = %#v, %v, calls=%d", retrySameGen, err, runtimeClient.activateCalls.Load())
		}

		// Supervisor restarts into generation 43: cached generation-42 receipt must miss and delegate once.
		gen43 := mutationReadyStatus("8.3.7", testArtifactIDB, allCaps...)
		gen43.Generation = 43
		runtimeClient.mu.Lock()
		runtimeClient.status = gen43
		runtimeClient.mu.Unlock()

		replayedGen43, err := service.Activate(t.Context(), req)
		if err != nil || !replayedGen43.AlreadyApplied || runtimeClient.activateCalls.Load() != 2 {
			t.Fatalf("generation-43 replay = %#v, %v, calls=%d; want delegated replay", replayedGen43, err, runtimeClient.activateCalls.Load())
		}
		// Second retry in generation 43 hits the newly cached generation-43 receipt without an extra RPC.
		if cachedGen43, err := service.Activate(t.Context(), req); err != nil || !cachedGen43.AlreadyApplied || runtimeClient.activateCalls.Load() != 2 {
			t.Fatalf("generation-43 cached retry = %#v, %v, calls=%d", cachedGen43, err, runtimeClient.activateCalls.Load())
		}

		// Supervisor restarts into generation 44 where durable verification rejects: must fail closed.
		gen44 := mutationReadyStatus("8.3.7", testArtifactIDB, allCaps...)
		gen44.Generation = 44
		runtimeClient.mu.Lock()
		runtimeClient.status = gen44
		runtimeClient.mu.Unlock()

		rejectedGen44, err := service.Activate(t.Context(), req)
		kind, code, ok := MutationErrorDetails(err)
		if !ok || kind != MutationErrorConflict || code != string(runtimeservice.ProtocolErrorActiveArtifactMismatch) || rejectedGen44.AlreadyApplied || runtimeClient.activateCalls.Load() != 3 {
			t.Fatalf("generation-44 retry = (%#v, %v, %q, %q, calls=%d), want conflict/active_artifact_mismatch", rejectedGen44, err, kind, code, runtimeClient.activateCalls.Load())
		}
	})

	t.Run("same-generation ActivationIntent loss or conflict fails closed on retry and evicts cached receipt", func(t *testing.T) {
		obsCaps := []model.RuntimeCapability{
			model.RuntimeCapabilityPrepareUpdate,
			model.RuntimeCapabilityActivateUpdate,
			model.RuntimeCapabilityObserveUpdateOperation,
		}
		for _, tc := range []struct {
			name         string
			observeErr   error
			observeFn    func(model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error)
			wantMutKind  MutationErrorKind
			wantMutCode  string
			wantObsKind  ObservationErrorKind
			wantObsCode  string
			checkObsSame bool
		}{
			{
				name: "supervisor intent missing or corrupt 503",
				observeErr: &runtimeservice.ProtocolError{
					HTTPStatus: 503,
					Code:       runtimeservice.ProtocolErrorOperationPersistenceUnavailable,
				},
				wantMutKind:  MutationErrorUnavailable,
				wantMutCode:  "runtime_operation_unavailable",
				wantObsKind:  ObservationErrorUnavailable,
				wantObsCode:  "runtime_operation_unavailable",
				checkObsSame: true,
			},
			{
				name: "supervisor selection digest conflict 409",
				observeErr: &runtimeservice.ProtocolError{
					HTTPStatus: 409,
					Code:       runtimeservice.ProtocolErrorOperationStateConflict,
				},
				wantMutKind: MutationErrorConflict,
				wantMutCode: string(runtimeservice.ProtocolErrorOperationStateConflict),
			},
			{
				name: "supervisor observation state unreliable",
				observeFn: func(req model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
					now := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
					return model.RuntimeUpdateOperationObservation{
						OperationID:       req.OperationID,
						OperationType:     req.OperationType,
						RuntimeIdentity:   req.ExpectedRuntimeIdentity,
						RuntimeGeneration: req.ExpectedRuntimeGeneration,
						State:             model.RuntimeOperationRunning,
						CreatedAt:         now,
						UpdatedAt:         now,
					}, nil
				},
				wantMutKind: MutationErrorUnavailable,
				wantMutCode: "runtime_result_unreliable",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				runtimeClient := &mutationRuntime{status: mutationReadyStatus("8.3.3", testArtifactID, obsCaps...)}
				runtimeClient.activateFn = func(req model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
					runtimeClient.mu.Lock()
					runtimeClient.status = mutationReadyStatus("8.3.7", testArtifactIDB, obsCaps...)
					runtimeClient.mu.Unlock()
					return successfulOperation(req.RuntimeMutationRequest, model.RuntimeOperationActivateUpdate), nil
				}
				service, _, _ := mutationTestService(t, runtimeClient)
				req := validMutationRequest()

				first, err := service.Activate(t.Context(), req)
				if err != nil || first.AlreadyApplied || first.State != string(model.RuntimeOperationSucceeded) {
					t.Fatalf("initial Activate() = %#v, %v", first, err)
				}
				if runtimeClient.activateCalls.Load() != 1 || runtimeClient.observeCalls.Load() != 1 {
					t.Fatalf("initial calls activate=%d observe=%d, want 1/1", runtimeClient.activateCalls.Load(), runtimeClient.observeCalls.Load())
				}
				retryOK, err := service.Activate(t.Context(), req)
				if err != nil || !retryOK.AlreadyApplied || runtimeClient.activateCalls.Load() != 1 || runtimeClient.observeCalls.Load() != 2 {
					t.Fatalf("healthy retry = %#v, %v, activate=%d observe=%d", retryOK, err, runtimeClient.activateCalls.Load(), runtimeClient.observeCalls.Load())
				}

				// Simulate durable ActivationIntent / selection verification failure within the same RuntimeGeneration.
				runtimeClient.mu.Lock()
				runtimeClient.observeErr = tc.observeErr
				runtimeClient.observeFn = tc.observeFn
				runtimeClient.mu.Unlock()

				got, err := service.Activate(t.Context(), req)
				kind, code, ok := MutationErrorDetails(err)
				if !ok || kind != tc.wantMutKind || code != tc.wantMutCode || got.AlreadyApplied {
					t.Fatalf("Activate() after durable intent loss = (%#v, %v, %q, %q), want %q/%q", got, err, kind, code, tc.wantMutKind, tc.wantMutCode)
				}
				if runtimeClient.activateCalls.Load() != 1 {
					t.Fatalf("activateCalls on failed receipt verification = %d, want 1", runtimeClient.activateCalls.Load())
				}
				service.mu.Lock()
				remainingMap := len(service.appliedActivations)
				remainingOrder := len(service.appliedActivationOrder)
				service.mu.Unlock()
				if remainingMap != 0 || remainingOrder != 0 {
					t.Fatalf("evicted receipt cache len = map:%d order:%d, want 0/0", remainingMap, remainingOrder)
				}

				if tc.checkObsSame {
					_, obsErr := service.ObserveActivate(t.Context(), ObservationRequest{
						RequestID:     req.RequestID,
						TargetVersion: req.TargetVersion,
					})
					obsKind, obsCode, obsOK := ObservationErrorDetails(obsErr)
					if !obsOK || obsKind != tc.wantObsKind || obsCode != tc.wantObsCode {
						t.Fatalf("ObserveActivate() = (%v, %q, %q), want %q/%q", obsErr, obsKind, obsCode, tc.wantObsKind, tc.wantObsCode)
					}
				}
			})
		}
	})

	t.Run("initial Activate refuses to cache receipt when ObserveUpdateOperation fails", func(t *testing.T) {
		runtimeClient := &mutationRuntime{
			status: mutationReadyStatus("8.3.3", testArtifactID, allCaps...),
			observeErr: &runtimeservice.ProtocolError{
				HTTPStatus: 503,
				Code:       runtimeservice.ProtocolErrorOperationPersistenceUnavailable,
			},
		}
		activatedOnce := false
		runtimeClient.activateFn = func(req model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
			if !activatedOnce {
				activatedOnce = true
				runtimeClient.mu.Lock()
				runtimeClient.status = mutationReadyStatus("8.3.7", testArtifactIDB, allCaps...)
				runtimeClient.mu.Unlock()
				return successfulOperation(req.RuntimeMutationRequest, model.RuntimeOperationActivateUpdate), nil
			}
			return model.RuntimeOperationResult{}, &runtimeservice.ProtocolError{
				HTTPStatus: 409,
				Code:       runtimeservice.ProtocolErrorOperationStateConflict,
			}
		}
		service, _, _ := mutationTestService(t, runtimeClient)
		req := validMutationRequest()

		first, err := service.Activate(t.Context(), req)
		if err != nil || first.AlreadyApplied || first.State != string(model.RuntimeOperationSucceeded) {
			t.Fatalf("initial Activate() = %#v, %v", first, err)
		}
		service.mu.Lock()
		cached := len(service.appliedActivations)
		service.mu.Unlock()
		if cached != 0 {
			t.Fatalf("cached receipts when ObserveUpdateOperation failed = %d, want 0", cached)
		}
		got, err := service.Activate(t.Context(), req)
		if err == nil || got.AlreadyApplied || runtimeClient.activateCalls.Load() != 2 {
			t.Fatalf("retry without cached receipt = %#v, %v, activateCalls=%d; want delegated conflict", got, err, runtimeClient.activateCalls.Load())
		}
	})
}
