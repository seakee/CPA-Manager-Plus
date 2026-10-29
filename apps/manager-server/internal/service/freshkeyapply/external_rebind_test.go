package freshkeyapply

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

// rebindRecordFor is a committed Connect External record holding one protected
// credential. It is the durable state a rebind replaces a credential in.
func rebindRecordFor(t *testing.T, protector *security.Protector, management string) (freshstore.Record, ExternalInstall) {
	t.Helper()
	const address = "https://cpa.example"
	protected, err := protector.ProtectString(management)
	if err != nil {
		t.Fatal(err)
	}
	record := freshstore.Record{
		Operation: bootstrap.FreshOperation{
			Strategy: bootstrap.FreshStrategyCheckpoint{
				Strategy: bootstrap.ConnectExternal, Stage: "committed",
				TargetIdentity: ExternalIdentity(address), TargetGeneration: 1,
			},
			ProductComplete: true,
			Revision:        1,
		},
		Connection: &model.ManagerCPAConnectionConfig{CPABaseURL: address, ManagementKey: protected},
	}
	return record, ExternalInstall{Identity: ExternalIdentity(address), Checkpoint: 1, BaseURL: address, ManagementKey: management}
}

// rebindHarness is the durable record plus the External authority over it.
type rebindHarness struct {
	store     *rebindStore
	protector *security.Protector
	service   *Service
}

func newRebindHarness(t *testing.T, management string) *rebindHarness {
	t.Helper()
	protector, err := security.NewProtector([]byte("isolated-rebind-test-key"))
	if err != nil {
		t.Fatal(err)
	}
	record, install := rebindRecordFor(t, protector, management)
	store := &rebindStore{record: record}
	service := NewExternal(nil, nil, func(context.Context) (ExternalInstall, error) { return install, nil })
	service.externalRebindVerifier = func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
		return cpa.ClassifyExternalVersion("v8.0.13"), nil
	}
	return &rebindHarness{store: store, protector: protector, service: service}
}

// TestExternalRebindPersistsOnlyAProvenCredential proves a rebind replaces the
// stored envelope and moves the record revision, while leaving the address and
// the installation identity alone so no key-usage history is reattributed.
func TestExternalRebindPersistsOnlyAProvenCredential(t *testing.T) {
	harness := newRebindHarness(t, strings.Repeat("a", 64))
	rotated := strings.Repeat("b", 64)
	result, err := harness.service.Rebind(t.Context(), harness.store, harness.protector, 1, "", rotated)
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if result.Record.Operation.Revision != 2 {
		t.Fatalf("revision = %d, want 2", result.Record.Operation.Revision)
	}
	if result.Record.Operation.Strategy.TargetGeneration != 1 {
		t.Fatalf("the installation generation moved to %d", result.Record.Operation.Strategy.TargetGeneration)
	}
	if result.Record.Operation.Strategy.TargetIdentity != ExternalIdentity("https://cpa.example") {
		t.Fatalf("the identity changed to %q", result.Record.Operation.Strategy.TargetIdentity)
	}
	if result.Record.Connection.CPABaseURL != "https://cpa.example" {
		t.Fatalf("the address changed to %q", result.Record.Connection.CPABaseURL)
	}
	if result.Record.Connection.ManagementKey == rotated {
		t.Fatal("the credential was persisted in plaintext")
	}
	plain, err := harness.protector.UnprotectString(result.Record.Connection.ManagementKey)
	if err != nil || plain != rotated {
		t.Fatalf("the stored envelope does not decrypt to the candidate: %v", err)
	}
	if harness.store.rebounds != 1 {
		t.Fatalf("rebind transactions = %d", harness.store.rebounds)
	}
}

// TestExternalRebindRefusesAnUnprovenCredential proves nothing is written when the
// candidate cannot be proven: a refused credential, an unsupported version, an
// unusable shape and another address each keep the installation exactly as it was.
func TestExternalRebindRefusesAnUnprovenCredential(t *testing.T) {
	rotated := strings.Repeat("b", 64)
	for _, scenario := range []struct {
		name  string
		key   string
		url   string
		probe func() (cpa.ExternalVersionReport, error)
		want  error
	}{
		{
			name: "rejected credential", key: rotated,
			probe: func() (cpa.ExternalVersionReport, error) {
				return cpa.ExternalVersionReport{}, cpa.ErrManagementRejected
			},
			want: cpa.ErrManagementRejected,
		},
		{
			name: "unreachable CPA", key: rotated,
			probe: func() (cpa.ExternalVersionReport, error) { return cpa.ExternalVersionReport{}, cpa.ErrProofUnavailable },
			want:  cpa.ErrProofUnavailable,
		},
		{
			name: "unsupported version", key: rotated,
			probe: func() (cpa.ExternalVersionReport, error) {
				return cpa.ClassifyExternalVersion("v7.2.130"), nil
			},
			want: cpa.ErrExternalVersionUnsupported,
		},
		{
			name: "unusable shape", key: " padded ",
			want: ErrRebindRefused,
		},
		{
			name: "another address", key: rotated, url: "https://other.example",
			want: ErrRebindRefused,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			harness := newRebindHarness(t, strings.Repeat("a", 64))
			before := *harness.store.record.Connection
			verifications := 0
			harness.service.externalRebindVerifier = func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
				verifications++
				if scenario.probe == nil {
					return cpa.ClassifyExternalVersion("v8.0.13"), nil
				}
				return scenario.probe()
			}
			result, err := harness.service.Rebind(t.Context(), harness.store, harness.protector, 1, scenario.url, scenario.key)
			if !errors.Is(err, scenario.want) {
				t.Fatalf("rebind = %v, want %v", err, scenario.want)
			}
			if result.Record.Operation.Revision != 0 {
				t.Fatal("a refused rebind answered a record")
			}
			if harness.store.rebounds != 0 {
				t.Fatal("a refused rebind wrote the durable record")
			}
			if *harness.store.record.Connection != before {
				t.Fatal("a refused rebind changed the stored connection")
			}
			// A shape or address refusal never reaches the CPA at all.
			if scenario.probe == nil && verifications != 0 {
				t.Fatalf("a pre-read refusal still probed the CPA %d times", verifications)
			}
		})
	}
}

// TestExternalRebindReportsAStaleRecord proves a record that moved while the
// candidate was being proven is a conflict, not a rebind: the page re-reads and
// asks again rather than this Manager guessing which write won.
func TestExternalRebindReportsAStaleRecord(t *testing.T) {
	harness := newRebindHarness(t, strings.Repeat("a", 64))
	harness.store.conflict = true
	_, err := harness.service.Rebind(t.Context(), harness.store, harness.protector, 1, "", strings.Repeat("b", 64))
	if !errors.Is(err, ErrGeneration) {
		t.Fatalf("stale record = %v, want a generation mismatch", err)
	}
	if harness.store.rebounds != 0 {
		t.Fatal("a stale rebind wrote the durable record")
	}
}

// TestExternalRebindDetectsARevisionChangeDuringVerification proves the revision
// the write is conditional on was read before the candidate was proven. A record
// edited during the verification — the window a real CPA round trip opens — must
// conflict: reading the revision afterwards would compare the CAS against the
// state this rebind itself last saw and overwrite the concurrent edit.
func TestExternalRebindDetectsARevisionChangeDuringVerification(t *testing.T) {
	harness := newRebindHarness(t, strings.Repeat("a", 64))
	harness.service.externalRebindVerifier = func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
		// Another save lands while the candidate is being verified.
		harness.store.record.Operation.Revision++
		return cpa.ClassifyExternalVersion("v8.0.13"), nil
	}
	_, err := harness.service.Rebind(t.Context(), harness.store, harness.protector, 1, "", strings.Repeat("b", 64))
	if !errors.Is(err, ErrGeneration) {
		t.Fatalf("revision change during verification = %v, want a generation mismatch", err)
	}
	if harness.store.rebounds != 0 {
		t.Fatal("a rebind decided against a stale revision wrote the record")
	}
}

// TestExternalRebindNeedsTheCommittedGeneration proves the authority's own
// generation check runs first: a rebind against another generation never probes
// the CPA.
func TestExternalRebindNeedsTheCommittedGeneration(t *testing.T) {
	harness := newRebindHarness(t, strings.Repeat("a", 64))
	probed := false
	harness.service.externalRebindVerifier = func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
		probed = true
		return cpa.ClassifyExternalVersion("v8.0.13"), nil
	}
	if _, err := harness.service.Rebind(t.Context(), harness.store, harness.protector, 2, "", strings.Repeat("b", 64)); !errors.Is(err, ErrGeneration) {
		t.Fatalf("another generation = %v", err)
	}
	if probed {
		t.Fatal("a foreign generation was probed against the CPA")
	}
}

// rebindStore is the durable record a rebind needs, in memory.
type rebindStore struct {
	record   freshstore.Record
	rebounds int
	conflict bool
}

func (s *rebindStore) Load(context.Context) (freshstore.Record, error) { return s.record, nil }

func (s *rebindStore) Create(context.Context, bootstrap.FreshIdentity, *model.AdminCredential) (freshstore.Record, error) {
	return freshstore.Record{}, errors.New("unsupported")
}

func (s *rebindStore) Update(context.Context, bootstrap.FreshIdentity, uint64, func(*freshstore.Record) error) (freshstore.Record, error) {
	return freshstore.Record{}, errors.New("unsupported")
}

func (s *rebindStore) RebindConnection(_ context.Context, _ bootstrap.FreshIdentity, revision uint64, next model.ManagerCPAConnectionConfig) (freshstore.Record, error) {
	if s.conflict {
		return freshstore.Record{}, freshstore.ErrConflict
	}
	// The real store compares the revision it is given, so a rebind decided
	// against a record that has since moved conflicts here too.
	if revision != s.record.Operation.Revision {
		return freshstore.Record{}, freshstore.ErrConflict
	}
	s.rebounds++
	s.record.Connection = &next
	s.record.Operation.Revision++
	return s.record, nil
}
