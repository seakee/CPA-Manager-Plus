package freshkeyapply

import (
	"context"
	"errors"
	"strings"
	"testing"

	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

func TestSingleExternalObservationWithoutOptionalJournalUsesLiveAuthentication(t *testing.T) {
	h := newExternalHarness(t, "owner-seed")
	created, _, err := h.keys.Create(t.Context(), strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	h.cpa.mu.Lock()
	h.cpa.keys = append(h.cpa.keys, created.CreatedSecret)
	h.cpa.mu.Unlock()
	h.service.repo = nil
	observed, err := h.service.ObserveKey(t.Context(), created.Key, 1)
	if err != nil || observed.RuntimeState != "effective_observed" {
		t.Fatalf("live client observation without journal = %s, %v", observed.RuntimeState, err)
	}
}

func TestExternalObservationsRejectAnOldInstallationGeneration(t *testing.T) {
	h := newExternalHarness(t, "owner-seed")
	created, _, err := h.keys.Create(t.Context(), strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	h.cpa.mu.Lock()
	h.cpa.keys = append(h.cpa.keys, created.CreatedSecret)
	h.cpa.mu.Unlock()
	if _, _, err := h.service.ObservedKeyList(t.Context(), 2); !errors.Is(err, ErrGeneration) {
		t.Fatalf("stale-generation list = %v", err)
	}
	if _, err := h.service.ObserveKey(t.Context(), created.Key, 2); !errors.Is(err, ErrGeneration) {
		t.Fatalf("stale-generation key = %v", err)
	}
}

func TestManagedObservationsCannotCertifyAnUnregisteredInstallation(t *testing.T) {
	s, destination, _, id := managedFixture(t)
	key, err := s.keys.Detail(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.keys.WithStagedSecret(t.Context(), id, func(secret string) error {
		destination.mu.Lock()
		destination.keys = []string{secret}
		destination.mu.Unlock()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	list, unverified, err := s.ObservedKeyList(t.Context(), 9)
	if err != nil || !unverified || len(list) != 1 || list[0].RuntimeState != "effect_unknown" {
		t.Fatalf("unregistered install list certified credentials: unverified=%t, err=%v", unverified, err)
	}
	observed, err := s.ObserveKey(t.Context(), key, 9)
	if err != nil || observed.RuntimeState != "effect_unknown" {
		t.Fatalf("unregistered install key = %s, %v", observed.RuntimeState, err)
	}
}

type restartDuringAuthTransport struct {
	*cpa.Transport
	runtime *observedRuntime
	changed bool
}

func (w *restartDuringAuthTransport) AuthSweep(ctx context.Context, secret string) (bool, error) {
	ok, err := w.Transport.AuthSweep(ctx, secret)
	if ok && err == nil && !w.changed {
		w.changed = true
		w.runtime.status.Generation++
	}
	return ok, err
}

func (w *restartDuringAuthTransport) SampleKeySet(ctx context.Context, accepted, rejected []string) (bool, error) {
	ok, err := w.Transport.SampleKeySet(ctx, accepted, rejected)
	if ok && err == nil && !w.changed {
		w.changed = true
		w.runtime.status.Generation++
	}
	return ok, err
}

func TestManagedKeyReadsFenceRuntimeChangeDuringAuthentication(t *testing.T) {
	for _, surface := range []string{"list", "detail"} {
		t.Run(surface, func(t *testing.T) {
			s, _, _, id := managedFixture(t)
			if _, err := s.Apply(t.Context(), id, 9); err != nil {
				t.Fatal(err)
			}
			s.wire = &restartDuringAuthTransport{Transport: s.wire.(*cpa.Transport), runtime: s.runtime.(*observedRuntime)}
			if surface == "list" {
				_, _, err := s.ObservedKeyList(t.Context(), 9)
				if !errors.Is(err, ErrGeneration) {
					t.Fatalf("list accepted a mid-authentication Runtime change: %v", err)
				}
			} else {
				key, err := s.keys.Detail(t.Context(), id)
				if err != nil {
					t.Fatal(err)
				}
				observed, err := s.ObserveKey(t.Context(), key, 9)
				if !errors.Is(err, ErrGeneration) || observed.RuntimeState == "effective_observed" {
					t.Fatalf("detail accepted a mid-authentication Runtime change: %s, %v", observed.RuntimeState, err)
				}
			}
		})
	}
}

func TestManagedObservationFencesRuntimeChangeDuringAuthentication(t *testing.T) {
	s, _, _, id := managedFixture(t)
	if _, err := s.Apply(t.Context(), id, 9); err != nil {
		t.Fatal(err)
	}
	s.wire = &restartDuringAuthTransport{Transport: s.wire.(*cpa.Transport), runtime: s.runtime.(*observedRuntime)}
	observed, err := s.Observe(t.Context(), id, 9)
	if !errors.Is(err, ErrGeneration) || observed.RuntimeState == "effective_observed" {
		t.Fatalf("Runtime changed while authenticating: state=%s, err=%v", observed.RuntimeState, err)
	}
}

func TestInitialAddRetryObservesTheSameDurableAttemptWithoutResending(t *testing.T) {
	s, destination, _, id := managedFixture(t)
	p, err := s.permit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.repo.ClaimInitialInstall(t.Context(), repo.InitialInstall{KeyID: id, RunID: p.RunID, Generation: p.Generation, AttemptedAtMS: 1, ProviderSeedSHA256: p.S2B.Binding()}); err != nil {
		t.Fatal(err)
	}
	requestID := strings.Repeat("d", 32)
	if _, err = s.ApplyAdd(t.Context(), requestID, id, 9); !errors.Is(err, cpa.ErrApplyUnknown) {
		t.Fatalf("same durable initial attempt before client load = %v", err)
	}
	if err = s.keys.WithStagedSecret(t.Context(), id, func(secret string) error {
		destination.mu.Lock()
		destination.keys = []string{secret}
		destination.mu.Unlock()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	settled, err := s.ApplyAdd(t.Context(), requestID, id, 9)
	if err != nil || settled.RuntimeState != "effective_observed" {
		t.Fatalf("same durable initial attempt after client load = %s, %v", settled.RuntimeState, err)
	}
	destination.mu.Lock()
	defer destination.mu.Unlock()
	if destination.keyWrites != 0 {
		t.Fatal("initial attempt recovery resent a CPA mutation")
	}
}

type authorityLossAfterExternalSample struct {
	externalWire
	sampler externalAuthWire
	after   func()
}

func (w *authorityLossAfterExternalSample) ExternalSampleKeySet(ctx context.Context, accepted, rejected []string) (bool, error) {
	ok, err := w.sampler.ExternalSampleKeySet(ctx, accepted, rejected)
	if ok && err == nil {
		w.after()
	}
	return ok, err
}

func TestExternalKeyReadsPropagateAuthorityReadFailure(t *testing.T) {
	for _, timing := range []string{"before_auth", "during_auth"} {
		for _, surface := range []string{"list", "detail"} {
			t.Run(timing+"/"+surface, func(t *testing.T) {
				h := newExternalHarness(t, "owner-seed")
				created, _, err := h.keys.Create(t.Context(), strings.Repeat("a", 32))
				if err != nil {
					t.Fatal(err)
				}
				h.cpa.mu.Lock()
				h.cpa.keys = append(h.cpa.keys, created.CreatedSecret)
				h.cpa.mu.Unlock()
				storageFailure := errors.New("installation checkpoint read failed")
				failed := timing == "before_auth"
				originalSource := h.service.externalSource
				h.service.externalSource = func(ctx context.Context) (ExternalInstall, error) {
					if failed {
						return ExternalInstall{}, storageFailure
					}
					return originalSource(ctx)
				}
				originalWire := h.service.externalWire
				h.service.externalWire = func(endpoint string) externalWire {
					wire := originalWire(endpoint)
					return &authorityLossAfterExternalSample{externalWire: wire, sampler: wire.(externalAuthWire), after: func() { failed = true }}
				}
				if surface == "list" {
					_, _, err = h.service.ObservedKeyList(t.Context(), 1)
				} else {
					_, err = h.service.ObserveKey(t.Context(), created.Key, 1)
				}
				if !errors.Is(err, storageFailure) {
					t.Fatalf("authority read failure became a successful observation: %v", err)
				}
			})
		}
	}
}

type snapshotErrorExternalWire struct {
	externalWire
	snapshot func(ctx context.Context, management string) (cpa.ExternalKeySnapshot, error)
}

func (w *snapshotErrorExternalWire) ExternalKeySnapshot(ctx context.Context, management string) (cpa.ExternalKeySnapshot, error) {
	return w.snapshot(ctx, management)
}

func TestExternalKeySnapshotPropagatesTypedErrorsAndDegradesOnlyKnownUpstreamUncertainty(t *testing.T) {
	errWaitCancelled := errors.New("external credential gate wait cancelled")
	errPersistence := errors.New("sqlite persistence read failed")

	nonDegradableCases := []struct {
		name      string
		cancelCtx bool
		snapErr   error
		wantErr   error
	}{
		{
			name:      "credential_wait_cancelled_with_canceled_context",
			cancelCtx: true,
			snapErr:   errWaitCancelled,
			wantErr:   errWaitCancelled,
		},
		{
			name:      "context_canceled_during_snapshot",
			cancelCtx: true,
			snapErr:   cpa.ErrProofUnavailable,
			wantErr:   context.Canceled,
		},
		{
			name:    "raw_persistence_error",
			snapErr: errPersistence,
			wantErr: errPersistence,
		},
		{
			name:    "management_rejected",
			snapErr: cpa.ErrManagementRejected,
			wantErr: cpa.ErrManagementRejected,
		},
		{
			name:    "management_client_forbidden",
			snapErr: cpa.ErrManagementClientForbidden,
			wantErr: cpa.ErrManagementClientForbidden,
		},
		{
			name:    "external_rejection_unavailable",
			snapErr: cpa.ErrExternalRejectionUnavailable,
			wantErr: cpa.ErrExternalRejectionUnavailable,
		},
		{
			name:    "external_target_moved",
			snapErr: cpa.ErrExternalTargetMoved,
			wantErr: cpa.ErrExternalTargetMoved,
		},
	}

	for _, tc := range nonDegradableCases {
		for _, surface := range []string{"list", "detail"} {
			t.Run(tc.name+"/"+surface, func(t *testing.T) {
				h := newExternalHarness(t, "owner-seed")
				created, _, err := h.keys.Create(t.Context(), strings.Repeat("b", 32))
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()

				originalWire := h.service.externalWire
				h.service.externalWire = func(endpoint string) externalWire {
					base := originalWire(endpoint)
					return &snapshotErrorExternalWire{
						externalWire: base,
						snapshot: func(callCtx context.Context, management string) (cpa.ExternalKeySnapshot, error) {
							if gate := cpa.ExternalSavedCredentialGateFromContext(callCtx); gate != nil {
								return base.ExternalKeySnapshot(callCtx, management)
							}
							if tc.cancelCtx {
								cancel()
							}
							return cpa.ExternalKeySnapshot{}, tc.snapErr
						},
					}
				}
				if tc.wantErr == errWaitCancelled {
					ctx = cpa.WithExternalSavedCredentialGate(ctx, func(_ context.Context, _ func(context.Context, cpa.ExternalSavedCredentialTarget) error) error {
						cancel()
						return errWaitCancelled
					})
				}

				if surface == "list" {
					_, unverified, obsErr := h.service.ObservedKeyList(ctx, 1)
					if !unverified || !errors.Is(obsErr, tc.wantErr) {
						t.Fatalf("ObservedKeyList err = %v (unverified=%t), want %v", obsErr, unverified, tc.wantErr)
					}
				} else {
					observed, obsErr := h.service.ObserveKey(ctx, created.Key, 1)
					if observed.RuntimeState != "effect_unknown" || !errors.Is(obsErr, tc.wantErr) {
						t.Fatalf("ObserveKey = (%q, %v), want (effect_unknown, %v)", observed.RuntimeState, obsErr, tc.wantErr)
					}
				}
			})
		}
	}

	for _, degErr := range []error{cpa.ErrProofUnavailable, cpa.ErrSourceConflict} {
		for _, surface := range []string{"list", "detail"} {
			t.Run("degradable_"+degErr.Error()+"/"+surface, func(t *testing.T) {
				h := newExternalHarness(t, "owner-seed")
				created, _, err := h.keys.Create(t.Context(), strings.Repeat("c", 32))
				if err != nil {
					t.Fatal(err)
				}
				originalWire := h.service.externalWire
				h.service.externalWire = func(endpoint string) externalWire {
					return &snapshotErrorExternalWire{
						externalWire: originalWire(endpoint),
						snapshot: func(context.Context, string) (cpa.ExternalKeySnapshot, error) {
							return cpa.ExternalKeySnapshot{}, degErr
						},
					}
				}
				if surface == "list" {
					list, unverified, obsErr := h.service.ObservedKeyList(t.Context(), 1)
					if obsErr != nil || !unverified || len(list) != 1 || list[0].RuntimeState != "effect_unknown" {
						t.Fatalf("degradable ObservedKeyList = (%+v, unverified=%t, err=%v)", list, unverified, obsErr)
					}
				} else {
					observed, obsErr := h.service.ObserveKey(t.Context(), created.Key, 1)
					if obsErr != nil || observed.RuntimeState != "effect_unknown" {
						t.Fatalf("degradable ObserveKey = (%q, %v), want (effect_unknown, nil)", observed.RuntimeState, obsErr)
					}
				}
			})
		}
	}
}
