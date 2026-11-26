package freshkeyapply

import (
	"context"
	"os"
	"slices"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

// ConfigTarget consumes existing S1/S2 target/permit checks. It grants no Key
// mutation. The serial Host must exclude every K/U writer for the source lease.
func (s *Service) ConfigTarget(ctx context.Context, generation uint64) (string, error) {
	s = s.active()
	if s == nil {
		return "", ErrUnavailable
	}
	if s.profile == ProfileManaged {
		p, err := s.operationTarget(ctx, generation)
		if err != nil {
			return "", err
		}
		return p.ManagementKey, nil
	}
	if s.h1 != nil {
		p, e := s.operationTarget(ctx, generation)
		if e != nil {
			return "", e
		}
		t, ok := s.wire.(*cpa.Transport)
		if !ok {
			return "", ErrUnavailable
		}
		if _, e = s.h1.Ready(ctx, t); e != nil {
			return "", e
		}
		return p.ManagementKey, nil
	}
	if s.keyOperations {
		return "", cpa.ErrWriterIsolation
	}
	// A reachable per-step permit is incompatible with the config-only profile,
	// even when its environment switch is off. Host must never issue one here.
	if _, e := os.Lstat(KeyPermitPath); !os.IsNotExist(e) {
		return "", cpa.ErrWriterIsolation
	}
	if o, e := s.repo.LatestOperation(ctx); e != nil || o != nil {
		return "", cpa.ErrSourceConflict
	}
	p, e := s.operationTarget(ctx, generation)
	if e != nil {
		return "", e
	}
	return p.ManagementKey, nil
}

// DefaultInstallCPAAuthorityProven is the Manager-internal admission predicate
// exposed for Gateway admission. It requires, in this order, that
//
//  1. this Manager was constructed with ProfileManaged (used by both New
//     Managed and Adopt; Connect External does not route model traffic through
//     CPAMP Ingress and carries no ProfileManaged permit);
//  2. the committed New Managed or Adopt checkpoint and the protected Manager
//     connection still decrypt to one credential for the configured address;
//  3. the authenticated Runtime status matches the recorded Runtime identity,
//     carries a live Supervisor generation and valid active CPA artifact, and
//     is Ready;
//  4. the destination CPA still satisfies its strategy's destination posture
//     (new_managed or adopt_existing); and
//  5. for an `adopt_existing` installation, durable `ProductComplete` holds and
//     the one-shot initial key install journal matches this installation's run,
//     generation and provider seed; and
//  6. client authentication is on, proven by at least one valid client key
//     (a CPA with no client key accepts unauthenticated Gateway traffic).
//
// It intentionally returns only a boolean and never a credential. A Gateway
// that needs the authority itself must call the same profile through ConfigTarget
// and must not copy this predicate's observations.
// ManagedProofStamp binds an admission proof and a key-list observation to the
// exact same Supervisor runtime generation, active gateway artifact, and CPA
// configuration/keyset snapshot digest. A zero stamp is never valid.
type ManagedProofStamp struct {
	valid   bool
	version authProofVersion
}

func (m ManagedProofStamp) Valid() bool {
	return m.valid
}

func (m ManagedProofStamp) Matches(other ManagedProofStamp) bool {
	return m.valid && other.valid && m.version == other.version
}

func (s *Service) DefaultInstallCPAAuthorityProven(ctx context.Context) bool {
	proven, _ := s.DefaultInstallCPAAuthorityProvenStamp(ctx)
	return proven
}

// DefaultInstallCPAAuthorityProvenStamp returns both the admission verdict and
// the fenced snapshot stamp that justified it so concurrent callers can verify
// they observed the identical Runtime and CPA keyset.
func (s *Service) DefaultInstallCPAAuthorityProvenStamp(ctx context.Context) (bool, ManagedProofStamp) {
	s = s.active()
	if s == nil || s.profile != ProfileManaged {
		return false, ManagedProofStamp{}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	p, err := s.permit(ctx)
	if err != nil {
		return false, ManagedProofStamp{}
	}
	target, err := s.target(ctx, p)
	if err != nil {
		return false, ManagedProofStamp{}
	}
	p.runtimeGeneration = uint64(target.Generation)
	p.runtimeArtifact = string(target.ActiveGatewayArtifact.ArtifactID)
	if p.Strategy == string(bootstrap.AdoptExisting) {
		if err = s.requireInstallJournal(ctx, p); err != nil {
			return false, ManagedProofStamp{}
		}
	}
	// A configuration read is not proof that the live client middleware has
	// loaded it. Sample anonymous, invalid and one configured client key, then
	// fence the result against both Runtime and the source configuration.
	w, ok := s.wire.(keyWire)
	if !ok {
		return false, ManagedProofStamp{}
	}
	snapshot, err := w.KeySnapshot(ctx, p.ManagementKey)
	if err != nil || len(snapshot.Keys) == 0 {
		return false, ManagedProofStamp{}
	}
	proven, err := w.SampleKeySet(ctx, snapshot.Keys[:1], nil)
	if err != nil || !proven {
		return false, ManagedProofStamp{}
	}
	if _, err = s.target(ctx, p); err != nil {
		return false, ManagedProofStamp{}
	}
	current, err := w.KeySnapshot(ctx, p.ManagementKey)
	if err != nil || current.SourceSHA256 != snapshot.SourceSHA256 || !slices.Equal(current.Keys, snapshot.Keys) {
		return false, ManagedProofStamp{}
	}
	return true, ManagedProofStamp{
		valid: true,
		version: authProofVersion{
			profile:           ProfileManaged,
			target:            p.Identity,
			runID:             p.RunID,
			runtimeGeneration: p.runtimeGeneration,
			runtimeArtifact:   p.runtimeArtifact,
			sourceSHA256:      snapshot.SourceSHA256,
			keysSHA256:        orderedKeysDigest(snapshot.Keys),
		},
	}
}
