package freshkeyapply

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"

	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
)

func TestAdmissionRequiresInstalledIdentityAndFreshLiveAuthority(t *testing.T) {
	s, w, rt, id := fixture(t)
	var secret string
	if err := s.keys.WithStagedSecret(t.Context(), id, func(v string) error { secret = v; return nil }); err != nil {
		t.Fatal(err)
	}
	binding := strings.Repeat("a", 64)
	admit := func() (Verdict, error) { return s.Admit(t.Context(), secret, 7, binding) }
	if _, err := admit(); err == nil {
		t.Fatal("staging alone admitted")
	}
	p, _ := s.permit(context.Background())
	s.permit = func(context.Context) (Permit, error) { return p, nil }
	if err := s.repo.ClaimInitialInstall(t.Context(), repo.InitialInstall{KeyID: id, RunID: p.RunID, Generation: p.Generation, AttemptedAtMS: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "wrong"} {
		if _, err := s.Admit(t.Context(), key, 7, binding); err == nil {
			t.Fatal("invalid credential admitted")
		}
	}
	other, _, err := s.keys.Create(t.Context(), strings.Repeat("d", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admit(t.Context(), other.CreatedSecret, 7, binding); err == nil {
		t.Fatal("uninstalled key admitted")
	}
	got, err := admit()
	if err != nil || got.RequestBinding != binding || got.ExpiresAtMS <= time.Now().UnixMilli() || got.ExpiresAtMS > time.Now().Add(time.Second).UnixMilli() {
		t.Fatal(got, err)
	}
	w.configured = false
	if _, err := admit(); err == nil {
		t.Fatal("cached verdict admitted zero keys")
	}
	w.configured, w.auth = true, false
	if _, err := admit(); err == nil {
		t.Fatal("config read admitted without real auth")
	}
	w.auth = true
	rt.status.Generation++
	if _, err := admit(); !errors.Is(err, ErrGeneration) {
		t.Fatal(err)
	}
	rt.status.Generation--
	s.permit = func(context.Context) (Permit, error) { return Permit{}, ErrUnavailable }
	if _, err := admit(); err == nil {
		t.Fatal("missing permit admitted")
	}
}

func TestS2BJournalBindsFixtureAcrossRequestsAndReload(t *testing.T) {
	t.Setenv("CPAMP_PRIVATE_S2B_CHAT", "1")
	t.Setenv("CPAMP_PRIVATE_S2A_CHAT", "")
	s, _, _, id := fixture(t)
	p, _ := s.permit(context.Background())
	p.S2B = cpa.PrivateProviderSeed{Provider: "unit-provider", Model: "unit-model", Target: "https://provider.invalid/v1", CredentialSHA256: strings.Repeat("a", 64)}
	s.permit = func(context.Context) (Permit, error) { return p, nil }
	var secret string
	s.keys.WithStagedSecret(t.Context(), id, func(v string) error { secret = v; return nil })
	if err := s.repo.ClaimInitialInstall(t.Context(), repo.InitialInstall{KeyID: id, RunID: p.RunID, Generation: p.Generation, AttemptedAtMS: time.Now().UnixMilli(), ProviderSeedSHA256: p.S2B.Binding()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admit(t.Context(), secret, 7, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	// Even a matching CPA read/auth fake cannot authorize a newly retargeted
	// fixture with the original installed Key/run/generation.
	p.S2B.Target = "https://other.invalid/v1"
	if _, err := s.Admit(t.Context(), secret, 7, strings.Repeat("b", 64)); !errors.Is(err, ErrGeneration) {
		t.Fatal("retargeted fixture admitted", err)
	}
	reloaded := New(s.repo, s.keys, s.runtime)
	reloaded.permit = s.permit
	reloaded.wire = s.wire
	if _, err := reloaded.Admit(t.Context(), secret, 7, strings.Repeat("b", 64)); !errors.Is(err, ErrGeneration) {
		t.Fatal("reload lost fixture binding", err)
	}
}

type admissionWire struct {
	wire
	sweep func(context.Context, string) (bool, error)
}

func (w admissionWire) AuthSweep(ctx context.Context, key string) (bool, error) {
	return w.sweep(ctx, key)
}

func TestAdmissionLosesAuthorityDuringObservation(t *testing.T) {
	for _, scenario := range []string{"expired", "permit-change", "generation-change", "slow-auth", "cancelled", "protector", "sqlite", "runtime", "cpa"} {
		t.Run(scenario, func(t *testing.T) {
			s, w, rt, id := fixture(t)
			var secret string
			s.keys.WithStagedSecret(t.Context(), id, func(v string) error { secret = v; return nil })
			p, _ := s.permit(context.Background())
			s.permit = func(context.Context) (Permit, error) { return p, nil }
			if err := s.repo.ClaimInitialInstall(t.Context(), repo.InitialInstall{KeyID: id, RunID: p.RunID, Generation: p.Generation, AttemptedAtMS: time.Now().UnixMilli()}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch scenario {
			case "expired":
				p.ExpiresAtMS = time.Now().UnixMilli()
			case "permit-change":
				s.wire = admissionWire{wire: w, sweep: func(context.Context, string) (bool, error) { p.ExpiresAtMS--; return true, nil }}
			case "generation-change":
				s.wire = admissionWire{wire: w, sweep: func(context.Context, string) (bool, error) { rt.status.Generation++; return true, nil }}
			case "slow-auth":
				s.wire = admissionWire{wire: w, sweep: func(ctx context.Context, _ string) (bool, error) { <-ctx.Done(); return true, nil }}
			case "cancelled":
				cancel()
			case "protector":
				s.keys = freshkeys.New(s.repo, nil)
			case "sqlite":
				db, err := sqlite.Open(filepath.Join(t.TempDir(), "closed.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				db.Close()
				s.repo = repo.New(db)
			case "runtime":
				s.runtime = nil
			case "cpa":
				s.wire = admissionWire{wire: w, sweep: func(context.Context, string) (bool, error) { return false, ErrUnavailable }}
			}
			got, err := s.Admit(ctx, secret, 7, strings.Repeat("a", 64))
			if err == nil || got.ExpiresAtMS != 0 {
				t.Fatal("lost authority admitted", got, err)
			}
		})
	}
}
