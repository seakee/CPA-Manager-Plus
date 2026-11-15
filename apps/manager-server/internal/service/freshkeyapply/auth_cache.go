package freshkeyapply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
	"slices"
	"strconv"
	"sync"
)

const maxCachedAuthVersions = 8

func formatUint(v uint64) string {
	return strconv.FormatUint(v, 10)
}

type authProofVersion struct {
	profile           string
	target            string
	runID             string
	sourceSHA256      string
	keysSHA256        string
	runtimeGeneration uint64
	runtimeArtifact   string
}

type authProofEntry struct {
	version  authProofVersion
	verified map[string]bool
	rejected map[string]bool
}

type authProofCache struct {
	mu      sync.Mutex
	entries []authProofEntry
}

func orderedKeysDigest(keys []string) string {
	raw, _ := json.Marshal(keys)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (c *authProofCache) verifiedSet(v authProofVersion) map[string]bool {
	if v.target == "" || v.runID == "" || v.sourceSHA256 == "" || v.keysSHA256 == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range c.entries {
		if entry.version == v {
			out := make(map[string]bool, len(entry.verified))
			for k, ok := range entry.verified {
				if ok {
					out[k] = true
				}
			}
			return out
		}
	}
	return nil
}

func (c *authProofCache) isVerified(v authProofVersion, secret string) bool {
	if secret == "" || v.target == "" || v.runID == "" || v.sourceSHA256 == "" || v.keysSHA256 == "" {
		return false
	}
	hash := freshkeys.SecretDigest(secret)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range c.entries {
		if entry.version == v {
			return entry.verified[hash]
		}
	}
	return false
}

// recordProven records live authentication proofs only for this complete source
// version. A successful sample must not promote other keys from an older config,
// even when the destination and process identity have not changed.
func (c *authProofCache) recordProven(v authProofVersion, currentKeys []string, newlyProven []string) {
	if v.target == "" || v.runID == "" || v.sourceSHA256 == "" || v.keysSHA256 == "" {
		return
	}
	presentHashes := make(map[string]bool, len(currentKeys))
	for _, k := range currentKeys {
		presentHashes[freshkeys.SecretDigest(k)] = true
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	merged := make(map[string]bool, len(currentKeys))
	for _, entry := range c.entries {
		if entry.version == v {
			for h, ok := range entry.verified {
				if ok && presentHashes[h] {
					merged[h] = true
				}
			}
		}
	}
	for _, secret := range newlyProven {
		h := freshkeys.SecretDigest(secret)
		if presentHashes[h] {
			merged[h] = true
		}
	}

	for i := range c.entries {
		if c.entries[i].version == v {
			for h, ok := range merged {
				if ok {
					c.entries[i].verified[h] = true
				}
			}
			entry := c.entries[i]
			copy(c.entries[i:], c.entries[i+1:])
			c.entries[len(c.entries)-1] = entry
			return
		}
	}
	if len(c.entries) >= maxCachedAuthVersions {
		copy(c.entries, c.entries[1:])
		c.entries[len(c.entries)-1] = authProofEntry{version: v, verified: merged}
		return
	}
	c.entries = append(c.entries, authProofEntry{version: v, verified: merged})
}

type externalAuthWire interface {
	ExternalSampleKeySet(ctx context.Context, accepted, rejected []string) (bool, error)
}

func externalErrorCode(err error) string {
	switch {
	case errors.Is(err, cpa.ErrManagementClientForbidden), errors.Is(err, cpa.ErrManagementRejected):
		return "external_management_key_rejected"
	case errors.Is(err, cpa.ErrExternalTargetMoved):
		return "external_target_moved"
	case errors.Is(err, cpa.ErrExternalVersionUnsupported):
		return "external_cpa_version_unsupported"
	case errors.Is(err, cpa.ErrSourceConflict):
		return "key_operation_source_conflict"
	case errors.Is(err, ErrGeneration):
		return "key_operation_generation_mismatch"
	case errors.Is(err, ErrLastKey):
		return "last_effective_key"
	case errors.Is(err, repo.ErrConflict):
		return "key_operation_conflict"
	case errors.Is(err, cpa.ErrApplyUnknown), errors.Is(err, ErrExternalUnavailable), errors.Is(err, cpa.ErrExternalRejectionUnavailable):
		return "key_operation_unknown"
	case err != nil:
		return "key_operation_host_proof_required"
	default:
		return ""
	}
}

func markUnverifiedKeys(keys []freshkeys.Key, code string) []freshkeys.Key {
	out := make([]freshkeys.Key, len(keys))
	for i, k := range keys {
		if k.OperationID == "" {
			k.RuntimeState = "effect_unknown"
		}
		if code != "" && k.EffectCode == "" {
			k.EffectCode = code
		}
		out[i] = k
	}
	return out
}

// managedObservationHeld rejects authority, Runtime, or config drift across an
// authentication sweep before any proof can enter the cache or the response.
func (s *Service) managedObservationHeld(ctx context.Context, p Permit, w keyWire, snap cpa.KeySnapshot) error {
	current, err := s.permit(ctx)
	if err != nil {
		return err
	}
	if current.Profile != p.Profile || current.Strategy != p.Strategy || current.Identity != p.Identity ||
		current.RunID != p.RunID || current.Generation != p.Generation || current.ManagementKey != p.ManagementKey ||
		current.S2B.Binding() != p.S2B.Binding() {
		return ErrGeneration
	}
	if err := s.requireInstallJournal(ctx, current); err != nil {
		return err
	}
	if _, err := s.target(ctx, p); err != nil {
		return err
	}
	after, err := w.KeySnapshot(ctx, p.ManagementKey)
	if err != nil {
		return err
	}
	if after.SourceSHA256 != snap.SourceSHA256 || !slices.Equal(after.Keys, snap.Keys) {
		return cpa.ErrApplyUnknown
	}
	return nil
}

// externalObservationHeld binds successful client authentication to the same
// durable external checkpoint, credential, and live configuration snapshot.
func (s *Service) externalObservationHeld(ctx context.Context, install ExternalInstall, w externalWire, snap cpa.ExternalKeySnapshot) error {
	current, err := s.externalAuthority(ctx)
	if err != nil {
		return err
	}
	if current.Checkpoint != install.Checkpoint {
		return ErrGeneration
	}
	if current.Identity != install.Identity || current.BaseURL != install.BaseURL || current.ManagementKey != install.ManagementKey {
		return cpa.ErrExternalTargetMoved
	}
	after, err := w.ExternalKeySnapshot(ctx, current.ManagementKey)
	if err != nil {
		return err
	}
	if after.SourceSHA256 != snap.SourceSHA256 || !slices.Equal(after.Keys, snap.Keys) {
		return cpa.ErrApplyUnknown
	}
	return nil
}

func projectKeyFromSnapshot(
	k freshkeys.Key,
	secret string,
	retainedRejected bool,
	secretInSnapshot bool,
	proven map[string]bool,
	unsettled *repo.Operation,
) freshkeys.Key {
	if unsettled != nil && !unsettled.IsTerminal() && (unsettled.TargetID == string(k.ID) || unsettled.KeeperID == string(k.ID)) {
		k.OperationID = unsettled.ID
		if unsettled.Outcome == "pending" && k.Lifecycle != identity.LifecycleSuperseded &&
			((unsettled.Mode == "add" && unsettled.TargetID == string(k.ID)) || (unsettled.Mode == "replace" && unsettled.KeeperID == string(k.ID))) {
			k.RuntimeState = "applying"
		} else {
			k.RuntimeState = "effect_unknown"
		}
		return k
	}
	if k.Lifecycle == identity.LifecycleSuperseded {
		if retainedRejected {
			k.RuntimeState = "revoked_observed"
		} else {
			k.RuntimeState = "effect_unknown"
		}
		return k
	}
	if secret == "" || !secretInSnapshot {
		k.RuntimeState = "not_applied"
		return k
	}
	if proven[freshkeys.SecretDigest(secret)] {
		k.RuntimeState = "effective_observed"
		return k
	}
	k.RuntimeState = "effect_unknown"
	return k
}

func degradableExternalAuthorityError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return errors.Is(err, ErrExternalUnavailable) || errors.Is(err, ErrUnavailable) ||
		errors.Is(err, cpa.ErrApplyUnknown) || errors.Is(err, cpa.ErrSourceConflict)
}

// ObservedKeyList returns the workspace keys projected against live CPA
// config readback, versioned /v1/models client-auth proofs, and any unsettled
// durable key operation journal. It reconciles an unsettled latest operation
// read-only first, never mutating CPA and never performing O(N) /v1/models
// sweeps once a snapshot version is proven.
func (s *Service) ObservedKeyList(ctx context.Context, generation uint64) (observed []freshkeys.Key, unverified bool, err error) {
	defer func() {
		if ctx.Err() != nil {
			observed = markUnverifiedKeys(observed, "key_operation_unknown")
			unverified = true
			if err == nil || errors.Is(err, cpa.ErrProofUnavailable) || degradableExternalAuthorityError(err) {
				err = ctx.Err()
			}
		}
	}()
	s = s.active()
	if s == nil || s.keys == nil {
		return nil, true, ErrUnavailable
	}
	if s.profile == ProfileExternal && generation != 0 {
		if _, err := s.externalTarget(ctx, generation); err != nil && !degradableExternalAuthorityError(err) {
			return nil, true, err
		}
	}
	// A missing journal is supported, but a failed journal read is not evidence
	// that no mutation is pending. Never project effective keys through that gap.
	var latest *repo.Operation
	var reconciliationErr error
	if s.repo != nil && s.KeyOperationsEnabled() {
		var err error
		latest, err = s.repo.LatestOperation(ctx)
		if err != nil {
			return nil, true, err
		}
		if latest != nil && !latest.IsTerminal() {
			s.mu.Lock()
			current, reconcileErr := s.repo.LatestOperation(ctx)
			if reconcileErr == nil && current != nil && !current.IsTerminal() {
				var resolved uint64
				resolved, reconcileErr = s.readGeneration(ctx, generation)
				if reconcileErr == nil {
					_, reconcileErr = s.check(ctx, current, resolved)
				}
			}
			s.mu.Unlock()
			if reconcileErr != nil && !readOnlyReconciliationUnavailable(reconcileErr) {
				return nil, true, reconcileErr
			}
			reconciliationErr = reconcileErr
			latest, err = s.repo.LatestOperation(ctx)
			if err != nil {
				return nil, true, err
			}
		}
	}
	keys, _, secretByID, err := s.keys.ActiveSecretMap(ctx)
	if err != nil {
		return nil, true, err
	}
	if reconciliationErr != nil {
		return markUnverifiedKeys(keys, externalErrorCode(reconciliationErr)), true, nil
	}

	if s.profile == ProfileExternal {
		return s.observedExternalKeyList(ctx, keys, secretByID, latest, generation)
	}
	if s.profile != ProfileManaged {
		return keys, false, nil
	}
	return s.observedManagedKeyList(ctx, keys, secretByID, latest, generation)
}

func (s *Service) observedManagedKeyList(
	ctx context.Context,
	keys []freshkeys.Key,
	secretByID map[string]string,
	latest *repo.Operation,
	generation uint64,
) ([]freshkeys.Key, bool, error) {
	p, err := s.permit(ctx)
	if err != nil {
		if !unavailableObservationAuthority(err) {
			return nil, true, err
		}
		return markUnverifiedKeys(keys, ""), true, nil
	}
	if generation != 0 && p.Generation != formatUint(generation) {
		return markUnverifiedKeys(keys, "key_operation_generation_mismatch"), true, nil
	}
	if err := s.requireInstallJournal(ctx, p); err != nil {
		if errors.Is(err, ErrGeneration) || errors.Is(err, ErrUnavailable) {
			return markUnverifiedKeys(keys, ""), true, nil
		}
		return nil, true, err
	}
	target, err := s.target(ctx, p)
	if err != nil {
		return markUnverifiedKeys(keys, ""), true, nil
	}
	p.runtimeGeneration = uint64(target.Generation)
	p.runtimeArtifact = string(target.ActiveGatewayArtifact.ArtifactID)
	w, ok := s.wire.(keyWire)
	if !ok {
		return markUnverifiedKeys(keys, ""), true, nil
	}
	snap, err := w.KeySnapshot(ctx, p.ManagementKey)
	if err != nil {
		code := ""
		if errors.Is(err, cpa.ErrSourceConflict) {
			code = "key_operation_source_conflict"
		}
		return markUnverifiedKeys(keys, code), true, nil
	}

	version := authProofVersion{
		profile:           ProfileManaged,
		target:            p.Identity,
		runID:             p.RunID,
		runtimeGeneration: p.runtimeGeneration,
		runtimeArtifact:   p.runtimeArtifact,
		sourceSHA256:      snap.SourceSHA256,
		keysSHA256:        orderedKeysDigest(snap.Keys),
	}
	proven := s.authCache.verifiedSet(version)
	if proven == nil {
		proven = make(map[string]bool)
	}
	snapshotKeys := make(map[string]bool, len(snap.Keys))
	for _, secret := range snap.Keys {
		snapshotKeys[secret] = true
	}

	var unverified []string
	seenUnverified := make(map[string]bool)
	for _, k := range keys {
		if k.Lifecycle == identity.LifecycleSuperseded {
			continue
		}
		secret := secretByID[string(k.ID)]
		if secret == "" || !snapshotKeys[secret] {
			continue
		}
		h := freshkeys.SecretDigest(secret)
		if !proven[h] && !seenUnverified[secret] {
			seenUnverified[secret] = true
			unverified = append(unverified, secret)
		}
	}

	unverifiedFlag := false
	if len(unverified) > 0 {
		authOK, authErr := w.SampleKeySet(ctx, unverified, nil)
		if authErr == nil && authOK {
			if err := s.managedObservationHeld(ctx, p, w, snap); err != nil {
				return markUnverifiedKeys(keys, externalErrorCode(err)), true, err
			}
			s.authCache.recordProven(version, snap.Keys, unverified)
			for _, secret := range unverified {
				proven[freshkeys.SecretDigest(secret)] = true
			}
		} else {
			unverifiedFlag = true
		}
	}

	retired, retirementUnknown, err := s.observeRetirements(ctx, keys, snap.Keys, snapshotKeys, version,
		func(accepted, rejected []string) (bool, error) { return w.SampleKeySet(ctx, accepted, rejected) },
		func() error { return s.managedObservationHeld(ctx, p, w, snap) })
	if err != nil {
		return markUnverifiedKeys(keys, externalErrorCode(err)), true, err
	}
	if err := ctx.Err(); err != nil {
		return nil, true, err
	}
	if len(proven) > 0 || len(retired) > 0 {
		if err := s.managedObservationHeld(ctx, p, w, snap); err != nil {
			return markUnverifiedKeys(keys, externalErrorCode(err)), true, err
		}
	}
	out := make([]freshkeys.Key, 0, len(keys))
	for i, k := range keys {
		secret := secretByID[string(k.ID)]
		out = append(out, projectKeyFromSnapshot(k, secret, i < len(retired) && retired[i], snapshotKeys[secret], proven, latest))
	}
	return out, unverifiedFlag || retirementUnknown, nil
}

func (s *Service) observedExternalKeyList(
	ctx context.Context,
	keys []freshkeys.Key,
	secretByID map[string]string,
	latest *repo.Operation,
	generation uint64,
) ([]freshkeys.Key, bool, error) {
	install, err := s.externalAuthority(ctx)
	if err == nil && generation != 0 && install.Checkpoint != generation {
		return nil, true, ErrGeneration
	}
	if err != nil {
		if !degradableExternalAuthorityError(err) {
			return nil, true, err
		}
		return markUnverifiedKeys(keys, ""), true, nil
	}
	w := s.externalKeyWire(install)
	if w == nil {
		return markUnverifiedKeys(keys, ""), true, nil
	}
	snap, err := w.ExternalKeySnapshot(ctx, install.ManagementKey)
	if err != nil {
		if ctx.Err() == nil && (errors.Is(err, cpa.ErrProofUnavailable) || errors.Is(err, cpa.ErrSourceConflict)) {
			return markUnverifiedKeys(keys, ""), true, nil
		}
		return nil, true, err
	}

	version := authProofVersion{
		profile:      ProfileExternal,
		target:       install.Identity,
		runID:        install.Generation(),
		sourceSHA256: snap.SourceSHA256,
		keysSHA256:   orderedKeysDigest(snap.Keys),
	}
	proven := s.authCache.verifiedSet(version)
	if proven == nil {
		proven = make(map[string]bool)
	}
	snapshotKeys := make(map[string]bool, len(snap.Keys))
	for _, secret := range snap.Keys {
		snapshotKeys[secret] = true
	}

	var unverified []string
	seenUnverified := make(map[string]bool)
	for _, k := range keys {
		if k.Lifecycle == identity.LifecycleSuperseded {
			continue
		}
		secret := secretByID[string(k.ID)]
		if secret == "" || !snapshotKeys[secret] {
			continue
		}
		h := freshkeys.SecretDigest(secret)
		if !proven[h] && !seenUnverified[secret] {
			seenUnverified[secret] = true
			unverified = append(unverified, secret)
		}
	}

	unverifiedFlag := false
	if len(unverified) > 0 {
		if sampler, ok := w.(externalAuthWire); ok {
			authOK, authErr := sampler.ExternalSampleKeySet(ctx, unverified, nil)
			if authErr == nil && authOK {
				if err := s.externalObservationHeld(ctx, install, w, snap); err != nil {
					return markUnverifiedKeys(keys, externalErrorCode(err)), true, err
				}
				s.authCache.recordProven(version, snap.Keys, unverified)
				for _, secret := range unverified {
					proven[freshkeys.SecretDigest(secret)] = true
				}
			} else {
				unverifiedFlag = true
			}
		} else {
			unverifiedFlag = true
		}
	}

	var sample func([]string, []string) (bool, error)
	if sampler, ok := w.(externalAuthWire); ok {
		sample = func(accepted, rejected []string) (bool, error) {
			return sampler.ExternalSampleKeySet(ctx, accepted, rejected)
		}
	}
	retired, retirementUnknown, err := s.observeRetirements(ctx, keys, snap.Keys, snapshotKeys, version, sample,
		func() error { return s.externalObservationHeld(ctx, install, w, snap) })
	if err != nil {
		return markUnverifiedKeys(keys, externalErrorCode(err)), true, err
	}
	if err := ctx.Err(); err != nil {
		return nil, true, err
	}
	if len(proven) > 0 || len(retired) > 0 {
		if err := s.externalObservationHeld(ctx, install, w, snap); err != nil {
			return markUnverifiedKeys(keys, externalErrorCode(err)), true, err
		}
	}
	out := make([]freshkeys.Key, 0, len(keys))
	for i, k := range keys {
		secret := secretByID[string(k.ID)]
		out = append(out, projectKeyFromSnapshot(k, secret, i < len(retired) && retired[i], snapshotKeys[secret], proven, latest))
	}
	return out, unverifiedFlag || retirementUnknown, nil
}

// ObserveKey reconciles any unsettled operation for key read-only and returns
// key projected with live targeted CPA config + /v1/models observation (O(1)
// probes on cold cache, 0 probes on cached snapshot version).
func (s *Service) ObserveKey(ctx context.Context, key freshkeys.Key, generation uint64) (observed freshkeys.Key, err error) {
	defer func() {
		if ctx.Err() != nil {
			observed.RuntimeState = "effect_unknown"
			if err == nil || errors.Is(err, cpa.ErrProofUnavailable) || degradableExternalAuthorityError(err) {
				err = ctx.Err()
			}
		}
	}()
	s = s.active()
	if s == nil || s.keys == nil {
		return key, ErrUnavailable
	}
	if s.profile == ProfileExternal && generation != 0 {
		if _, err := s.externalTarget(ctx, generation); err != nil && !degradableExternalAuthorityError(err) {
			return key, err
		}
	}
	var reconcileErr error
	var latest *repo.Operation
	if s.repo != nil {
		var err error
		latest, err = s.repo.LatestOperation(ctx)
		if err != nil {
			return key, err
		}
		if s.KeyOperationsEnabled() {
			op, err := s.repo.LatestAddOperation(ctx, string(key.ID), key.RequestID)
			if err != nil {
				return key, err
			}
			if op == nil || op.IsTerminal() {
				op = latest
			}
			if op != nil && !op.IsTerminal() && (op.TargetID == string(key.ID) || op.KeeperID == string(key.ID)) {
				s.mu.Lock()
				var resolved uint64
				resolved, reconcileErr = s.readGeneration(ctx, generation)
				if reconcileErr == nil {
					_, reconcileErr = s.check(ctx, op, resolved)
				}
				s.mu.Unlock()
				if reconcileErr != nil && !readOnlyReconciliationUnavailable(reconcileErr) {
					return key, reconcileErr
				}
				latest, err = s.repo.LatestOperation(ctx)
				if err != nil {
					return key, err
				}
			}
		}
	}
	refreshed, err := s.keys.Detail(ctx, string(key.ID))
	if err != nil {
		return key, err
	}
	key = refreshed
	if latest != nil && !latest.IsTerminal() && (latest.TargetID == string(key.ID) || latest.KeeperID == string(key.ID)) {
		key.OperationID = latest.ID
		if reconcileErr == nil && latest.Outcome == "pending" && key.Lifecycle != identity.LifecycleSuperseded &&
			((latest.Mode == "add" && latest.TargetID == string(key.ID)) || (latest.Mode == "replace" && latest.KeeperID == string(key.ID))) {
			key.RuntimeState = "applying"
		} else {
			key.RuntimeState = "effect_unknown"
		}
		if reconcileErr != nil {
			key.EffectCode = externalErrorCode(reconcileErr)
		}
		return key, nil
	}
	if s.profile == ProfileExternal {
		return s.observeSingleExternalKey(ctx, key, generation, reconcileErr)
	}
	if s.profile != ProfileManaged {
		return key, nil
	}
	return s.observeSingleManagedKey(ctx, key, generation, reconcileErr)
}

func (s *Service) observeSingleManagedKey(ctx context.Context, key freshkeys.Key, generation uint64, reconcileErr error) (freshkeys.Key, error) {
	p, err := s.permit(ctx)
	if err != nil {
		key.RuntimeState = "effect_unknown"
		if !unavailableObservationAuthority(err) {
			return key, err
		}
		return key, nil
	}
	if generation != 0 && p.Generation != formatUint(generation) {
		key.RuntimeState = "effect_unknown"
		key.EffectCode = "key_operation_generation_mismatch"
		return key, nil
	}
	if err := s.requireInstallJournal(ctx, p); err != nil {
		key.RuntimeState = "effect_unknown"
		if errors.Is(err, ErrGeneration) || errors.Is(err, ErrUnavailable) {
			return key, nil
		}
		return key, err
	}
	target, err := s.target(ctx, p)
	if err != nil {
		key.RuntimeState = "effect_unknown"
		return key, nil
	}
	p.runtimeGeneration = uint64(target.Generation)
	p.runtimeArtifact = string(target.ActiveGatewayArtifact.ArtifactID)
	w, ok := s.wire.(keyWire)
	if !ok {
		key.RuntimeState = "effect_unknown"
		return key, nil
	}
	snap, err := w.KeySnapshot(ctx, p.ManagementKey)
	if err != nil {
		key.RuntimeState = "effect_unknown"
		if errors.Is(err, cpa.ErrSourceConflict) {
			key.EffectCode = "key_operation_source_conflict"
		}
		return key, nil
	}
	version := authProofVersion{
		profile:           ProfileManaged,
		target:            p.Identity,
		runID:             p.RunID,
		runtimeGeneration: p.runtimeGeneration,
		runtimeArtifact:   p.runtimeArtifact,
		sourceSHA256:      snap.SourceSHA256,
		keysSHA256:        orderedKeysDigest(snap.Keys),
	}
	if key.Lifecycle == identity.LifecycleSuperseded {
		retired, _, err := s.observeRetirements(ctx, []freshkeys.Key{key}, snap.Keys, nil, version,
			func(accepted, rejected []string) (bool, error) { return w.SampleKeySet(ctx, accepted, rejected) },
			func() error { return s.managedObservationHeld(ctx, p, w, snap) })
		key.RuntimeState = "effect_unknown"
		if err != nil {
			return key, err
		}
		if len(retired) > 0 && retired[0] {
			if err := s.managedObservationHeld(ctx, p, w, snap); err != nil {
				return key, err
			}
			key.RuntimeState = "revoked_observed"
		}
		return key, nil
	}
	var secret string
	if err := s.keys.WithStagedSecret(ctx, string(key.ID), func(val string) error {
		secret = val
		return nil
	}); err != nil {
		return key, err
	}
	if !slices.Contains(snap.Keys, secret) {
		key.RuntimeState = "not_applied"
		if reconcileErr != nil {
			key.EffectCode = externalErrorCode(reconcileErr)
		}
		return key, nil
	}
	if s.authCache.isVerified(version, secret) {
		if err := s.managedObservationHeld(ctx, p, w, snap); err != nil {
			key.RuntimeState = "effect_unknown"
			return key, err
		}
		key.RuntimeState = "effective_observed"
		return key, nil
	}
	authOK, authErr := w.SampleKeySet(ctx, []string{secret}, nil)
	if authErr == nil && authOK {
		if err := s.managedObservationHeld(ctx, p, w, snap); err != nil {
			key.RuntimeState = "effect_unknown"
			return key, err
		}
		s.authCache.recordProven(version, snap.Keys, []string{secret})
		key.RuntimeState = "effective_observed"
		return key, nil
	}
	key.RuntimeState = "effect_unknown"
	return key, nil
}

func (s *Service) observeSingleExternalKey(ctx context.Context, key freshkeys.Key, generation uint64, reconcileErr error) (freshkeys.Key, error) {
	install, err := s.externalAuthority(ctx)
	if err == nil && generation != 0 && install.Checkpoint != generation {
		return key, ErrGeneration
	}
	if err != nil {
		if !degradableExternalAuthorityError(err) {
			return key, err
		}
		key.RuntimeState = "effect_unknown"
		return key, nil
	}
	w := s.externalKeyWire(install)
	if w == nil {
		key.RuntimeState = "effect_unknown"
		return key, nil
	}
	snap, err := w.ExternalKeySnapshot(ctx, install.ManagementKey)
	if err != nil {
		key.RuntimeState = "effect_unknown"
		if ctx.Err() == nil && (errors.Is(err, cpa.ErrProofUnavailable) || errors.Is(err, cpa.ErrSourceConflict)) {
			return key, nil
		}
		return key, err
	}
	version := authProofVersion{
		profile:      ProfileExternal,
		target:       install.Identity,
		runID:        install.Generation(),
		sourceSHA256: snap.SourceSHA256,
		keysSHA256:   orderedKeysDigest(snap.Keys),
	}
	if key.Lifecycle == identity.LifecycleSuperseded {
		var sample func([]string, []string) (bool, error)
		if sampler, ok := w.(externalAuthWire); ok {
			sample = func(accepted, rejected []string) (bool, error) {
				return sampler.ExternalSampleKeySet(ctx, accepted, rejected)
			}
		}
		retired, _, err := s.observeRetirements(ctx, []freshkeys.Key{key}, snap.Keys, nil, version, sample,
			func() error { return s.externalObservationHeld(ctx, install, w, snap) })
		key.RuntimeState = "effect_unknown"
		if err != nil {
			return key, err
		}
		if len(retired) > 0 && retired[0] {
			if err := s.externalObservationHeld(ctx, install, w, snap); err != nil {
				return key, err
			}
			key.RuntimeState = "revoked_observed"
		}
		return key, nil
	}
	var secret string
	if err := s.keys.WithStagedSecret(ctx, string(key.ID), func(val string) error {
		secret = val
		return nil
	}); err != nil {
		return key, err
	}
	if !slices.Contains(snap.Keys, secret) {
		key.RuntimeState = "not_applied"
		if reconcileErr != nil {
			key.EffectCode = externalErrorCode(reconcileErr)
		}
		return key, nil
	}
	if s.authCache.isVerified(version, secret) {
		if err := s.externalObservationHeld(ctx, install, w, snap); err != nil {
			key.RuntimeState = "effect_unknown"
			return key, err
		}
		key.RuntimeState = "effective_observed"
		return key, nil
	}
	if sampler, ok := w.(externalAuthWire); ok {
		authOK, authErr := sampler.ExternalSampleKeySet(ctx, []string{secret}, nil)
		if authErr == nil && authOK {
			if err := s.externalObservationHeld(ctx, install, w, snap); err != nil {
				key.RuntimeState = "effect_unknown"
				return key, err
			}
			s.authCache.recordProven(version, snap.Keys, []string{secret})
			key.RuntimeState = "effective_observed"
			return key, nil
		}
		key.RuntimeState = "effect_unknown"
		return key, nil
	}
	key.RuntimeState = "effect_unknown"
	return key, nil
}
