package cpaupdate

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

type baselineArtifactFixture struct {
	status      runtimeservice.ArtifactStatus
	statusErr   error
	adopted     runtimeservice.ArtifactStatus
	adoptErr    error
	statusCalls int
	adoptCalls  int
	expectedID  string
}

func (f *baselineArtifactFixture) Status(context.Context) (runtimeservice.ArtifactStatus, error) {
	f.statusCalls++
	return f.status, f.statusErr
}

func (f *baselineArtifactFixture) AdoptBundledBaseline(_ context.Context, id string) (runtimeservice.ArtifactStatus, error) {
	f.adoptCalls++
	f.expectedID = id
	return f.adopted, f.adoptErr
}

func TestBaselineStatusIsAdditiveAndEmbeddedOnly(t *testing.T) {
	runtime := &mutationRuntime{status: mutationReadyStatus("8.0.55", testArtifactID)}
	service, _, _ := mutationTestService(t, runtime)
	fixture := &baselineArtifactFixture{status: runtimeservice.ArtifactStatus{
		SchemaVersion: 1, Pinned: runtimeservice.ArtifactPinned{Version: "8.0.56", ArtifactID: testArtifactIDB},
		Active: &runtimeservice.ArtifactActive{Origin: "acquired", ArtifactID: testArtifactID}, BaselineUpgradeAvailable: true,
	}}
	service.artifact = fixture
	got, err := service.Status(t.Context())
	if err != nil || got.Baseline == nil || *got.Baseline != (BaselineStatus{PinnedVersion: "8.0.56", PinnedArtifactID: testArtifactIDB, BaselineUpgradeAvailable: true}) {
		t.Fatalf("Embedded baseline = %+v, %v", got, err)
	}
	fixture.statusErr = errors.New("artifact endpoint unavailable")
	got, err = service.Status(t.Context())
	if err != nil || got.Baseline != nil || got.CurrentVersion != "8.0.55" {
		t.Fatalf("baseline endpoint failure should leave existing CPA status usable: %+v, %v", got, err)
	}
	service.mode = model.RuntimeModeExternal
	got, err = service.Status(t.Context())
	if err != nil || got.Baseline != nil || got.State != StateManagedExternally || fixture.statusCalls != 2 {
		t.Fatalf("External baseline must be omitted without querying Supervisor: %+v, %v", got, err)
	}
}

func TestAdoptBaselineGatesAndSupervisorResult(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		mode          model.RuntimeMode
		incomplete    bool
		expected      model.RuntimeArtifactID
		supervisorErr error
		wantCode      string
		wantCall      bool
	}{
		{name: "success", version: "8.0.55", wantCall: true},
		{name: "external", version: "8.0.55", mode: model.RuntimeModeExternal, wantCode: "managed_externally"},
		{name: "incomplete", version: "8.0.55", incomplete: true, wantCode: "product_incomplete"},
		{name: "below product floor", version: "8.0.54", wantCode: "current_version_unsupported"},
		{name: "stale active digest", version: "8.0.55", expected: testArtifactIDB, wantCode: "active_artifact_mismatch"},
		{name: "in flight", version: "8.0.55", wantCall: true, wantCode: runtimeservice.ArtifactCodeAcquisitionInProgress,
			supervisorErr: &runtimeservice.ArtifactError{Code: runtimeservice.ArtifactCodeAcquisitionInProgress, HTTPStatus: http.StatusConflict}},
		{name: "readiness failure", version: "8.0.55", wantCall: true, wantCode: runtimeservice.ArtifactCodeInstallFailed,
			supervisorErr: &runtimeservice.ArtifactError{Code: runtimeservice.ArtifactCodeInstallFailed, HTTPStatus: http.StatusInternalServerError}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := &mutationRuntime{status: mutationReadyStatus(tc.version, testArtifactID)}
			service, _, _ := mutationTestService(t, runtime)
			if tc.mode.IsValid() {
				service.mode = tc.mode
			}
			if tc.incomplete {
				service.WithProductCompleteProbe(func(context.Context) (bool, error) { return false, nil })
			}
			fixture := &baselineArtifactFixture{adoptErr: tc.supervisorErr, adopted: runtimeservice.ArtifactStatus{
				SchemaVersion: 1, Pinned: runtimeservice.ArtifactPinned{Version: "8.0.56", ArtifactID: testArtifactIDB},
				Active: &runtimeservice.ArtifactActive{Origin: "bundled", ArtifactID: testArtifactIDB}, BundledAvailable: true,
			}}
			service.artifact = fixture
			expected := tc.expected
			if expected == "" {
				expected = testArtifactID
			}
			got, err := service.AdoptBaseline(t.Context(), expected)
			if tc.wantCode == "" {
				if err != nil || got.ActiveArtifactID != testArtifactIDB || got.Baseline.PinnedArtifactID != testArtifactIDB || got.Baseline.AdoptBaselineAvailable {
					t.Fatalf("adopted result = %+v, %v", got, err)
				}
			} else if _, code, ok := MutationErrorDetails(err); !ok || code != tc.wantCode {
				t.Fatalf("adoption error = %v, want %s", err, tc.wantCode)
			}
			if (fixture.adoptCalls == 1) != tc.wantCall || fixture.adoptCalls > 1 || (tc.wantCall && fixture.expectedID != string(expected)) {
				t.Fatalf("Supervisor calls = %d, digest = %q", fixture.adoptCalls, fixture.expectedID)
			}
		})
	}
}
