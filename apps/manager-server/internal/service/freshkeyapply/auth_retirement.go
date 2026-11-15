package freshkeyapply

import (
	"context"
	"errors"
	"slices"
	"strconv"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
)

// Zero is a read-only caller convention, not mutation authority. Resolve the
// durable current epoch before reconciling a journal; never use its old epoch
// to authorize a probe against a replacement installation.
func (s *Service) readGeneration(ctx context.Context, generation uint64) (uint64, error) {
	if generation != 0 {
		return generation, nil
	}
	if s.profile == ProfileExternal {
		install, err := s.externalAuthority(ctx)
		if err != nil {
			return 0, err
		}
		if install.Checkpoint == 0 {
			return 0, ErrGeneration
		}
		return install.Checkpoint, nil
	}
	p, err := s.permit(ctx)
	if err != nil {
		return 0, err
	}
	resolved, err := strconv.ParseUint(p.Generation, 10, 64)
	if err != nil || resolved == 0 {
		return 0, ErrGeneration
	}
	return resolved, nil
}

func readOnlyReconciliationUnavailable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, cpa.ErrManagementRejected) || errors.Is(err, cpa.ErrManagementClientForbidden) ||
		errors.Is(err, cpa.ErrExternalTargetMoved) || errors.Is(err, cpa.ErrExternalRejectionUnavailable) {
		return false
	}
	return errors.Is(err, cpa.ErrApplyUnknown) || errors.Is(err, cpa.ErrSourceConflict) ||
		errors.Is(err, repo.ErrConflict) || errors.Is(err, ErrUnavailable) ||
		errors.Is(err, ErrExternalUnavailable) || errors.Is(err, cpa.ErrProofUnavailable) ||
		errors.Is(err, cpa.ErrExternalVersionUnsupported) || errors.Is(err, ErrGeneration)
}

func unavailableObservationAuthority(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return errors.Is(err, ErrUnavailable) || errors.Is(err, cpa.ErrProofUnavailable)
}

func (c *authProofCache) isRejected(v authProofVersion, hash string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range c.entries {
		if entry.version == v {
			return entry.rejected[hash]
		}
	}
	return false
}

// Called only after the accepted baseline, all rejected probes, and the exact
// post-probe authority/config fence succeeded for this version.
func (c *authProofCache) recordRejected(v authProofVersion, secrets []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.entries {
		entry := &c.entries[i]
		if entry.version != v {
			continue
		}
		if entry.rejected == nil {
			entry.rejected = make(map[string]bool, len(secrets))
		}
		for _, secret := range secrets {
			entry.rejected[freshkeys.SecretDigest(secret)] = true
		}
		return
	}
}

// A missing config entry is not revocation evidence: an empty keyset enables
// anonymous CPA access, and reload can lag config readback. Batch retired keys
// into one real closed-auth sample and cache only same-version negative proofs.
func (s *Service) observeRetirements(
	ctx context.Context,
	keys []freshkeys.Key,
	snapKeys []string,
	snapshotKeys map[string]bool,
	version authProofVersion,
	sample func([]string, []string) (bool, error),
	held func() error,
) ([]bool, bool, error) {
	var confirmed []bool
	var pending []string
	var pendingIndexes []int
	unverified := false
	for i, key := range keys {
		if key.Lifecycle != identity.LifecycleSuperseded {
			continue
		}
		if confirmed == nil {
			confirmed = make([]bool, len(keys))
		}
		if len(snapKeys) == 0 {
			unverified = true
			continue
		}
		err := s.keys.WithRetainedSecret(ctx, string(key.ID), func(secret string) error {
			present := snapshotKeys[secret]
			if snapshotKeys == nil {
				present = slices.Contains(snapKeys, secret)
			}
			if present {
				unverified = true
				return nil
			}
			if s.authCache.isRejected(version, freshkeys.SecretDigest(secret)) {
				confirmed[i] = true
				return nil
			}
			pending = append(pending, secret)
			pendingIndexes = append(pendingIndexes, i)
			return nil
		})
		if err != nil {
			return nil, true, err
		}
	}
	if len(pending) == 0 {
		return confirmed, unverified, nil
	}
	if sample == nil {
		return confirmed, true, nil
	}
	accepted := snapKeys[:1]
	ok, err := sample(accepted, pending)
	if err != nil || !ok {
		if ctx.Err() != nil {
			return nil, true, ctx.Err()
		}
		return confirmed, true, nil
	}
	if err := held(); err != nil {
		return nil, true, err
	}
	s.authCache.recordProven(version, snapKeys, accepted)
	s.authCache.recordRejected(version, pending)
	for _, i := range pendingIndexes {
		confirmed[i] = true
	}
	return confirmed, unverified, nil
}
