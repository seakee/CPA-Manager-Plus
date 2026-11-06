package freshkeyapply

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

type observedRuntime struct {
	runtimeservice.RuntimeClient
	status model.RuntimeObservedStatus
}

func (r *observedRuntime) Status(context.Context) (model.RuntimeObservedStatus, error) {
	return r.status, nil
}

type fakeWire struct {
	calls            int
	configured, auth bool
	fail             error
}

func (w *fakeWire) InstallOne(context.Context, cpa.TargetProof, string, string) (cpa.InstallResult, error) {
	w.calls++
	return cpa.InstallResult{}, w.fail
}
func (w *fakeWire) Configured(context.Context, string, string) (bool, error) {
	return w.configured, nil
}
func (w *fakeWire) AuthSweep(context.Context, string) (bool, error) { return w.auth, nil }

func fixture(t *testing.T) (*Service, *fakeWire, *observedRuntime, string) {
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
	p, _ := security.NewProtector([]byte("isolated-test-key"))
	keys := freshkeys.New(r, p)
	created, _, err := keys.Create(t.Context(), strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	rt := &observedRuntime{status: model.RuntimeObservedStatus{Identity: cpa.PrivateKeyRuntime, Generation: 7, State: model.RuntimeStateReady, ActiveGatewayArtifact: &model.ActiveGatewayArtifact{ArtifactID: cpa.PrivateKeyArtifact, Version: "8.0.55"}}}
	w := &fakeWire{configured: true, auth: true}
	s := New(r, keys, rt)
	s.wire = w
	s.permit = func() (Permit, error) {
		return Permit{RunID: "cpamp-s1-test-run", Generation: "7", ManagementKey: strings.Repeat("b", 64), IssuedAtMS: time.Now().Add(-time.Second).UnixMilli(), ExpiresAtMS: time.Now().Add(time.Minute).UnixMilli()}, nil
	}
	return s, w, rt, string(created.Key.ID)
}

func TestUnknownAttemptSurvivesNewConsumerAndKeyChoice(t *testing.T) {
	s, w, _, id := fixture(t)
	w.fail = cpa.ErrApplyUnknown
	if _, err := s.Apply(t.Context(), id, 7); !errors.Is(err, cpa.ErrApplyUnknown) {
		t.Fatal(err)
	}
	reopened := New(s.repo, s.keys, s.runtime)
	reopened.permit = s.permit
	reopened.wire = w
	if _, err := reopened.Apply(t.Context(), id, 7); !errors.Is(err, repo.ErrConflict) {
		t.Fatal(err)
	}
	second, _, err := s.keys.Create(t.Context(), strings.Repeat("c", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Apply(t.Context(), string(second.Key.ID), 7); !errors.Is(err, repo.ErrConflict) {
		t.Fatal(err)
	}
	if w.calls != 1 {
		t.Fatal("retried unknown write")
	}
	key, err := s.keys.Detail(t.Context(), id)
	if err != nil || key.RuntimeState != "effect_unknown" {
		t.Fatal(key, err)
	}
}

func TestObserveForeignInitialJournalDoesNotProveThisKeyAttempt(t *testing.T) {
	s, w, _, installedID := fixture(t)
	w.fail = cpa.ErrApplyUnknown
	if _, err := s.Apply(t.Context(), installedID, 7); !errors.Is(err, cpa.ErrApplyUnknown) {
		t.Fatal(err)
	}
	other, _, err := s.keys.Create(t.Context(), strings.Repeat("c", 32))
	if err != nil {
		t.Fatal(err)
	}
	otherID := string(other.Key.ID)
	status, err := s.Observe(t.Context(), otherID, 7)
	if err != nil || status.KeyID != otherID || status.Attempted || status.InstallAvailable ||
		status.RuntimeState != "effect_unknown" || status.CPAConfigured {
		t.Fatal("foreign S1 journal certified this Key", status, err)
	}
	// A missing Host permit does not turn the foreign journal into evidence.
	s.permit = func() (Permit, error) { return Permit{}, ErrUnavailable }
	status, err = s.Observe(t.Context(), otherID, 7)
	if err != nil || status.Attempted || status.InstallAvailable {
		t.Fatal("foreign journal certified this Key without Host permit", status, err)
	}
	if w.calls != 1 {
		t.Fatal("foreign status check replayed the one-shot write")
	}
}

func TestConcurrentConsumersClaimExactlyOneAttempt(t *testing.T) {
	s, w, _, id := fixture(t)
	w.fail = cpa.ErrApplyUnknown
	other := New(s.repo, s.keys, s.runtime)
	other.permit = s.permit
	otherWire := &fakeWire{fail: cpa.ErrApplyUnknown}
	other.wire = otherWire
	var wg sync.WaitGroup
	for _, consumer := range []*Service{s, other} {
		wg.Add(1)
		go func(s *Service) { defer wg.Done(); s.Apply(context.Background(), id, 7) }(consumer)
	}
	wg.Wait()
	if w.calls+otherWire.calls != 1 {
		t.Fatal("more than one writer")
	}
}

func TestObservationIsGenerationAndWindowBoundWithoutPersistentEffect(t *testing.T) {
	s, _, rt, id := fixture(t)
	got, err := s.Apply(t.Context(), id, 7)
	if err != nil || !got.CPAConfigured || got.RuntimeState != "effective_observed" || got.IngressAuthorized || !got.Attempted || got.InstallAvailable {
		t.Fatal(got, err)
	}
	if got.RuntimeGeneration != "7" || got.ObservedUntilMS-got.ObservedFromMS < 500 || got.ExpiresAtMS-got.ObservedUntilMS > 5000 {
		t.Fatal(got)
	}
	key, _ := s.keys.Detail(t.Context(), id)
	if key.RuntimeState != "effect_unknown" {
		t.Fatal("persisted an effective claim")
	}
	rt.status.Generation = 8
	got, err = s.Observe(t.Context(), id, 7)
	if !errors.Is(err, ErrGeneration) || got.RuntimeState == "effective_observed" {
		t.Fatal(got, err)
	}
	rt.status.Generation = 7
	s.permit = func() (Permit, error) { return Permit{}, ErrUnavailable }
	got, err = s.Observe(t.Context(), id, 7)
	if err != nil || got.RuntimeState == "effective_observed" || got.CPAConfigured {
		t.Fatal(got, err)
	}
}

func TestUnprovedGenerationCannotConsumeAttempt(t *testing.T) {
	s, w, rt, id := fixture(t)
	for _, generation := range []uint64{0, 6, 8} {
		if _, err := s.Apply(t.Context(), id, generation); !errors.Is(err, ErrGeneration) {
			t.Fatal(err)
		}
	}
	rt.status.Generation = 8
	if _, err := s.Apply(t.Context(), id, 7); !errors.Is(err, ErrGeneration) {
		t.Fatal(err)
	}
	if attempt, err := s.repo.InitialInstall(t.Context()); attempt != nil || err != nil || w.calls != 0 {
		t.Fatal("unproved write attempted")
	}
}

func TestConfiguredIsNotLiveAuthentication(t *testing.T) {
	s, w, _, id := fixture(t)
	w.auth = false
	got, err := s.Apply(t.Context(), id, 7)
	if err != nil || !got.CPAConfigured || got.RuntimeState != "effect_unknown" || got.ObservedUntilMS != 0 {
		t.Fatal(got, err)
	}
	w.configured = false
	got, err = s.Observe(t.Context(), id, 7)
	if err != nil || got.CPAConfigured || got.RuntimeState != "effect_unknown" {
		t.Fatal(got, err)
	}
}

func TestHostPermitIsNotAnEnvironmentFlag(t *testing.T) {
	t.Setenv("CPAMP_PRIVATE_S1_PERMIT", PermitPath)
	if _, err := loadPermit(); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing host permit accepted", err)
	}
	now := time.Now()
	p := Permit{RunID: "cpamp-s1-test-run", Generation: strconv.Itoa(7), ManagementKey: strings.Repeat("a", 64), IssuedAtMS: now.Add(-time.Second).UnixMilli(), ExpiresAtMS: now.UnixMilli()}
	if p.valid(now) {
		t.Fatal("expired permit accepted")
	}
	p.ExpiresAtMS = now.Add(11 * time.Minute).UnixMilli()
	if p.valid(now) {
		t.Fatal("unbounded permit accepted")
	}
}
