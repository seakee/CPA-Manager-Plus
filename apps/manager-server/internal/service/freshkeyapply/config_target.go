package freshkeyapply

import (
	"context"
	"os"
	"time"

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
//  5. client authentication is on, proven by at least one valid client key
//     (a CPA with no client key accepts unauthenticated Gateway traffic).
// It intentionally returns only a boolean and never a credential. A Gateway
// that needs the authority itself must call the same profile through ConfigTarget
// and must not copy this predicate's observations.
func (s *Service) DefaultInstallCPAAuthorityProven(ctx context.Context) bool {
	s = s.active()
	if s == nil || s.profile != ProfileManaged {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	p, err := s.permit()
	if err != nil {
		return false
	}
	if _, err = s.target(ctx, p); err != nil {
		return false
	}
	// KeySnapshot re-reads the destination config, proves the managed posture
	// with an empty client set, and then requires the same non-empty client
	// set from the canonical access projection.
	w, ok := s.wire.(keyWire)
	if !ok {
		return false
	}
	_, err = w.KeySnapshot(ctx, p.ManagementKey)
	return err == nil
}
