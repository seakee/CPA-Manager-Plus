package freshkeyapply

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
)

const KeyPermitPath = "/run/cpamp-s1/key-operation.json"

var ErrLastKey = errors.New("cannot revoke last effective key")

// A distinct Host grant for exactly one step of one durable operation. Host must
// exclude/drain every other config/file/reload writer before issuing this file.
// It contains no credential and cannot be supplied through the public API.
type KeyPermit struct {
	Version      int    `json:"version,omitempty"`
	SourceID     string `json:"sourceId,omitempty"`
	Revision     uint64 `json:"revision,omitempty"`
	ParentID     string `json:"parentId,omitempty"`
	StepID       string `json:"stepId,omitempty"`
	OperationID  string `json:"operationId"`
	Step         string `json:"step"`
	RunID        string `json:"runId"`
	Generation   string `json:"generation"`
	SourceSHA256 string `json:"sourceSHA256"`
	IssuedAtMS   int64  `json:"issuedAtMs"`
	ExpiresAtMS  int64  `json:"expiresAtMs"`
}

func loadKeyPermit() (KeyPermit, error) {
	var p KeyPermit
	err := readHostPermit(KeyPermitPath, &p)
	return p, err
}

type keyWire interface {
	KeySnapshot(context.Context, string) (cpa.KeySnapshot, error)
	WriteKeySet(context.Context, string, []string) error
	SampleKeySet(context.Context, []string, []string) (bool, error)
	// ValidKeySet reports whether this destination would accept one client-key
	// list as its own.
	ValidKeySet([]string) bool
}

type OperationResult struct {
	Operation    *repo.Operation `json:"operation"`
	RuntimeState string          `json:"runtimeState"`
	ExpiresAtMS  int64           `json:"expiresAtMs"`
	NextStep     string          `json:"nextStep"`
}

func (s *Service) KeyOperationsEnabled() bool {
	s = s.active()
	if s == nil || !s.keyOperations {
		return false
	}
	if s.profile == ProfileExternal {
		return true
	}
	if s.h1 != nil {
		return os.Getenv("CPAMP_PRIVATE_CONFIG_AUTHORITY") == "1"
	}
	_, e := os.Lstat("/run/cpamp-config/source.json")
	return os.IsNotExist(e) && os.Getenv("CPAMP_PRIVATE_CONFIG_AUTHORITY") == ""
}

func (s *Service) installedKey(ctx context.Context) (string, error) {
	if s.h1 != nil {
		t, ok := s.wire.(*cpa.Transport)
		if !ok {
			return "", ErrUnavailable
		}
		if e := s.h1.Free(ctx, t); e != nil {
			return "", e
		}
	}
	o, err := s.repo.LatestOperation(ctx)
	if err != nil {
		return "", err
	}
	if o != nil {
		if !s.keyOperations || o.Outcome != "committed" {
			return "", ErrUnavailable
		}
		a, e := s.repo.InitialInstall(ctx)
		if e != nil {
			return "", e
		}
		if a == nil || o.RunID != a.RunID || o.Generation != a.Generation || o.ProviderSeedSHA256 != a.ProviderSeedSHA256 {
			return "", ErrGeneration
		}
		return o.KeeperID, nil
	}
	a, err := s.repo.InitialInstall(ctx)
	if err != nil {
		return "", err
	}
	if a == nil {
		return "", ErrUnavailable
	}
	return a.KeyID, nil
}

func validOperationRequestShape(target, keeper, mode string) bool {
	if !freshkeys.ValidRequestID(target) {
		return false
	}
	switch mode {
	case "add", "rotate":
		return keeper == ""
	case "revoke":
		if keeper == "" {
			return true
		}
		return freshkeys.ValidRequestID(keeper) && target != keeper
	case "replace":
		return freshkeys.ValidRequestID(keeper) && target != keeper
	default:
		return false
	}
}

// reconcileUnsettledLatestLocked performs a read-only check on an unsettled
// LatestOperation so an operation whose HTTP reply was lost after CPA committed
// does not block subsequent operations. It never sends a PUT.
func (s *Service) reconcileUnsettledLatestLocked(ctx context.Context, generation uint64) (*repo.Operation, error) {
	latest, err := s.repo.LatestOperation(ctx)
	if err != nil || latest == nil || latest.IsTerminal() {
		return latest, err
	}
	if _, err := s.check(ctx, latest, generation); err != nil && !errors.Is(err, cpa.ErrApplyUnknown) && !errors.Is(err, cpa.ErrSourceConflict) && !errors.Is(err, repo.ErrConflict) {
		return latest, err
	}
	return s.repo.LatestOperation(ctx)
}

// ApplyAdd executes or recovers the journaled single-step CPA addition and live
// client-auth confirmation for an active Manager-staged canonical Key.
//
// Invariants:
//   - If an operation already exists for id, read-only reconciliation runs
//     first; an "attempted" unknown step is NEVER resent, while an
//     "unattempted" pending step executes its single PUT + confirmation.
//   - On a legacy unseeded NewManaged destination with no InitialInstall row
//     yet, ApplyAdd delegates to the one-shot initial-install path so the
//     empty-list seed transitions cleanly to [target].
func (s *Service) ApplyAdd(ctx context.Context, id, target string, generation uint64) (OperationResult, error) {
	result := OperationResult{RuntimeState: "effect_unknown"}
	s = s.active()
	if s == nil || s.repo == nil {
		return result, ErrUnavailable
	}
	if !freshkeys.ValidRequestID(id) || !freshkeys.ValidRequestID(target) {
		return result, freshkeys.ErrRequest
	}
	if !s.KeyOperationsEnabled() {
		return result, ErrUnavailable
	}
	if s.profile == ProfileExternal {
		return s.externalApplyAdd(ctx, id, target, generation)
	}
	key, err := s.keys.Detail(ctx, target)
	if err != nil {
		return result, err
	}
	if key.Lifecycle == identity.LifecycleSuperseded {
		return result, repo.ErrConflict
	}

	if s.profile == ProfileManaged {
		p, err := s.permit(ctx)
		if err != nil {
			return result, err
		}
		if p.Strategy == string(bootstrap.NewManaged) {
			attempt, err := s.repo.InitialInstall(ctx)
			if err != nil {
				return result, err
			}
			if attempt != nil && (attempt.RunID != p.RunID || attempt.Generation != p.Generation) {
				return result, ErrGeneration
			}
			observeInitial := attempt != nil && attempt.KeyID == target
			if observeInitial {
				key, err := s.keys.Detail(ctx, target)
				if err != nil {
					return result, err
				}
				// The installer record reserves a key before destination staging;
				// it is not an attempted one-shot InstallOne mutation.
				observeInitial = key.Origin != freshkeys.OriginInstaller
			}
			if attempt == nil || observeInitial {
				var state State
				var applyErr error
				if attempt == nil {
					state, applyErr = s.Apply(ctx, target, generation)
				} else {
					state, applyErr = s.Observe(ctx, target, generation)
				}
				if applyErr != nil {
					return OperationResult{RuntimeState: state.RuntimeState}, applyErr
				}
				if state.RuntimeState != "effective_observed" {
					return OperationResult{RuntimeState: state.RuntimeState}, cpa.ErrApplyUnknown
				}
				return OperationResult{
					RuntimeState: "effective_observed",
					ExpiresAtMS:  state.ExpiresAtMS,
				}, nil
			}
		}
	}

	prepared, err := s.Prepare(ctx, id, target, "", "add", generation)
	if err != nil {
		return prepared, err
	}
	if prepared.Operation != nil && prepared.Operation.Outcome == "committed" {
		return s.observeCommittedAdd(ctx, prepared, key, generation)
	}
	if prepared.NextStep == "add" {
		return s.ContinueOperation(ctx, id, generation)
	}
	return prepared, cpa.ErrApplyUnknown
}

// Prepare persists an immutable selection before any upstream write. Missing
// step authority never causes an attempted mutation or a new operation retry.
func (s *Service) Prepare(ctx context.Context, id, target, keeper, mode string, generation uint64) (OperationResult, error) {
	result := OperationResult{RuntimeState: "effect_unknown"}
	s = s.active()
	if s == nil {
		return result, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prepareLocked(ctx, id, target, keeper, mode, generation)
}

func (s *Service) prepareLocked(ctx context.Context, id, target, keeper, mode string, generation uint64) (OperationResult, error) {
	result := OperationResult{RuntimeState: "effect_unknown"}
	if !s.KeyOperationsEnabled() {
		return result, ErrUnavailable
	}
	if !freshkeys.ValidRequestID(id) || !validOperationRequestShape(target, keeper, mode) {
		return result, freshkeys.ErrRequest
	}
	if previous, err := s.repo.Operation(ctx, id); err != nil {
		return result, err
	} else if previous != nil {
		if previous.TargetID != target || previous.KeeperID != keeper || previous.Mode != mode {
			return result, repo.ErrConflict
		}
		return s.check(ctx, previous, generation)
	}
	latest, err := s.reconcileUnsettledLatestLocked(ctx, generation)
	if err != nil {
		return result, err
	}
	if latest != nil && !latest.IsTerminal() {
		return OperationResult{Operation: latest, RuntimeState: "effect_unknown"}, repo.ErrConflict
	}
	if s.keys != nil {
		if err := s.keys.DiscardOrphanedRotate(ctx, id, ""); err != nil {
			return result, err
		}
	}
	if s.profile == ProfileExternal {
		return s.externalPrepare(ctx, id, target, keeper, mode, generation)
	}
	p, snapshot, alreadyHeld, err := s.keyOperationPrerequisites(ctx, id, target, keeper, mode, generation)
	if err != nil {
		return result, err
	}
	var steps []repo.KeyStep
	switch mode {
	case "add":
		state := "unattempted"
		if alreadyHeld {
			state = "observed"
		}
		steps = []repo.KeyStep{{Name: "add", State: state}}
	case "rotate":
		state := "unattempted"
		if alreadyHeld {
			state = "observed"
		}
		steps = []repo.KeyStep{{Name: "rotate", State: state}}
	case "revoke":
		steps = []repo.KeyStep{{Name: "revoke", State: "unattempted"}}
	case "replace":
		steps = []repo.KeyStep{{Name: "add", State: "unattempted"}, {Name: "revoke", State: "unattempted"}}
	}
	outcome := "pending"
	var completedAt int64
	if (mode == "add" || mode == "rotate") && alreadyHeld {
		outcome = "committed"
		completedAt = time.Now().UnixMilli()
	}
	o := repo.Operation{
		ID: id, TargetID: target, KeeperID: keeper, Mode: mode,
		RunID: p.RunID, Generation: p.Generation, ProviderSeedSHA256: p.S2B.Binding(),
		SnapshotSHA256: snapshot, Revision: 1, Outcome: outcome, Steps: steps,
		CreatedAtMS: time.Now().UnixMilli(), CompletedAtMS: completedAt,
	}
	if s.h1 != nil {
		if mode == "add" || mode == "rotate" || keeper == "" {
			return result, ErrUnavailable
		}
		err = s.keyPair(ctx, target, keeper, false, func(old, next string) error {
			t, ok := s.wire.(*cpa.Transport)
			if !ok {
				return ErrUnavailable
			}
			i := cpa.H1Intent{RequestID: id, Domain: "key", Mode: mode, TargetID: target, KeeperID: keeper, TargetSHA256: cpa.H1SecretHash(old), KeeperSHA256: cpa.H1SecretHash(next)}
			parent, e := s.h1.Claim(ctx, t, i)
			if e != nil {
				return e
			}
			o.ParentID = parent.ID
			o.SourceID = parent.SourceID
			o.ClaimSHA256 = parent.ClaimSHA256
			o.ClaimRevision = parent.ClaimRevision
			o.IntentDigest = cpa.H1Terminal(i)
			for n := range o.Steps {
				o.Steps[n].RuntimeID = cpa.H1StepID(i, n)
			}
			return nil
		})
		if err != nil {
			return result, err
		}
	}
	if err = s.repo.BeginOperation(ctx, o); err != nil {
		return result, err
	}
	if o.Outcome == "committed" {
		return OperationResult{
			Operation:    &o,
			RuntimeState: "effective_observed",
			ExpiresAtMS:  min(time.Now().Add(5*time.Second).UnixMilli(), p.ExpiresAtMS),
		}, nil
	}
	return s.checkOperation(ctx, &o, generation)
}

func (s *Service) keyOperationPrerequisites(ctx context.Context, opID, target, keeper, mode string, generation uint64) (Permit, []string, bool, error) {
	p, err := s.operationTarget(ctx, generation)
	if err != nil {
		return p, nil, false, err
	}
	if s.profile == ProfilePrivate {
		if mode == "add" || mode == "rotate" || keeper == "" {
			return p, nil, false, repo.ErrConflict
		}
		installed, err := s.installedKey(ctx)
		if err != nil || installed != target && (mode != "revoke" || installed != keeper) {
			return p, nil, false, repo.ErrConflict
		}
	}
	w, ok := s.wire.(keyWire)
	if !ok {
		return p, nil, false, ErrUnavailable
	}
	var snapshot []string
	alreadyHeld := false
	err = s.operationSecrets(ctx, opID, mode, target, keeper, false, func(old, next string) error {
		snap, e := w.KeySnapshot(ctx, p.ManagementKey)
		if e != nil {
			return e
		}
		known, e := s.keys.AllStaged(ctx, snap.Keys)
		if e != nil {
			return e
		}
		if !known {
			return cpa.ErrSourceConflict
		}

		if mode == "add" {
			if slices.Contains(snap.Keys, old) {
				authOK, authErr := w.SampleKeySet(ctx, []string{old}, nil)
				if authErr != nil {
					return authErr
				}
				if !authOK {
					return cpa.ErrApplyUnknown
				}
				if s.profile == ProfileManaged {
					if err := s.managedObservationHeld(ctx, p, w, snap); err != nil {
						return err
					}
				}
				s.authCache.recordProven(authProofVersion{
					profile:           s.profile,
					target:            p.Identity,
					runID:             p.RunID,
					runtimeGeneration: p.runtimeGeneration,
					runtimeArtifact:   p.runtimeArtifact,
					sourceSHA256:      snap.SourceSHA256,
					keysSHA256:        orderedKeysDigest(snap.Keys),
				}, snap.Keys, []string{old})
				snapshot = keyDigests(snap.Keys)
				alreadyHeld = true
				return nil
			}
			after := keySetAfter(mode, snap.Keys, "add", old, "")
			if !w.ValidKeySet(after) {
				return cpa.ErrSourceConflict
			}
			if err := s.verifyPreStepManagedAuth(ctx, w, p, snap, snap.Keys[0]); err != nil {
				return err
			}
			snapshot = keyDigests(snap.Keys)
			return nil
		}
		if mode == "rotate" {
			if old == "" || next == "" || old == next {
				return cpa.ErrSourceConflict
			}
			if !slices.Contains(snap.Keys, old) {
				if !slices.Contains(snap.Keys, next) || !w.ValidKeySet(snap.Keys) {
					return cpa.ErrSourceConflict
				}
				authOK, authErr := w.SampleKeySet(ctx, []string{next}, []string{old})
				if authErr != nil {
					return authErr
				}
				if !authOK {
					return cpa.ErrApplyUnknown
				}
				if s.profile == ProfileManaged {
					if err := s.managedObservationHeld(ctx, p, w, snap); err != nil {
						return err
					}
				}
				s.authCache.recordProven(authProofVersion{
					profile:           s.profile,
					target:            p.Identity,
					runID:             p.RunID,
					runtimeGeneration: p.runtimeGeneration,
					runtimeArtifact:   p.runtimeArtifact,
					sourceSHA256:      snap.SourceSHA256,
					keysSHA256:        orderedKeysDigest(snap.Keys),
				}, snap.Keys, []string{next})
				snapshot = keyDigests(snap.Keys)
				alreadyHeld = true
				return nil
			}
			after := keySetAfter(mode, snap.Keys, "rotate", old, next)
			if len(after) == 0 || !w.ValidKeySet(after) {
				return cpa.ErrSourceConflict
			}
			if err := s.verifyPreStepManagedAuth(ctx, w, p, snap, old); err != nil {
				return err
			}
			snapshot = keyDigests(snap.Keys)
			return nil
		}

		if mode == "revoke" && len(snap.Keys) <= 1 {
			return ErrLastKey
		}
		if !slices.Contains(snap.Keys, old) || (mode == "revoke" && next != "" && !slices.Contains(snap.Keys, next)) {
			return cpa.ErrSourceConflict
		}
		if mode == "replace" && s.profile == ProfilePrivate && slices.Contains(snap.Keys, next) {
			return cpa.ErrSourceConflict
		}
		steps := []string{"revoke"}
		if mode == "replace" {
			steps = []string{"add", "revoke"}
		}
		live := snap.Keys
		for _, step := range steps {
			live = keySetAfter(mode, live, step, old, next)
			if !w.ValidKeySet(live) {
				return cpa.ErrSourceConflict
			}
		}
		if s.profile == ProfilePrivate {
			ok, e := w.SampleKeySet(ctx, snap.Keys, nil)
			if e != nil {
				return e
			}
			if !ok {
				return cpa.ErrSourceConflict
			}
		} else {
			if err := s.verifyPreStepManagedAuth(ctx, w, p, snap, old); err != nil {
				return err
			}
		}
		snapshot = keyDigests(snap.Keys)
		return nil
	})
	return p, snapshot, alreadyHeld, err
}

func (s *Service) verifyPreStepManagedAuth(ctx context.Context, w keyWire, p Permit, snap cpa.KeySnapshot, probeSecret string) error {
	version := authProofVersion{
		profile:           s.profile,
		target:            p.Identity,
		runID:             p.RunID,
		runtimeGeneration: p.runtimeGeneration,
		runtimeArtifact:   p.runtimeArtifact,
		sourceSHA256:      snap.SourceSHA256,
		keysSHA256:        orderedKeysDigest(snap.Keys),
	}
	if s.authCache.isVerified(version, probeSecret) {
		return nil
	}
	ok, err := w.SampleKeySet(ctx, []string{probeSecret}, nil)
	if err != nil {
		return err
	}
	if !ok {
		return cpa.ErrSourceConflict
	}
	if err := s.managedObservationHeld(ctx, p, w, snap); err != nil {
		return err
	}
	s.authCache.recordProven(version, snap.Keys, []string{probeSecret})
	return nil
}

// Preflight answers whether one revoke or replace would pass Prepare right now,
// with the same authority, the same destination proof and nothing written.
func (s *Service) Preflight(ctx context.Context, target, keeper, mode string, generation uint64) error {
	s = s.active()
	if s == nil {
		return ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.KeyOperationsEnabled() {
		return ErrUnavailable
	}
	if (mode != "revoke" && mode != "replace") || !validOperationRequestShape(target, keeper, mode) {
		return freshkeys.ErrRequest
	}
	latest, err := s.reconcileUnsettledLatestLocked(ctx, generation)
	if err != nil {
		return err
	}
	if latest != nil && !latest.IsTerminal() {
		return repo.ErrConflict
	}
	if s.keys != nil {
		if sweepErr := s.keys.DiscardOrphanedRotate(ctx, "", ""); sweepErr != nil {
			return sweepErr
		}
	}
	if s.profile == ProfileExternal {
		return s.externalPreflight(ctx, target, keeper, mode, generation)
	}
	_, _, _, err = s.keyOperationPrerequisites(ctx, "", target, keeper, mode, generation)
	return err
}

func (s *Service) operationTarget(ctx context.Context, generation uint64) (Permit, error) {
	p, err := s.permit(ctx)
	if err != nil {
		return p, err
	}
	if p.Generation != strconv.FormatUint(generation, 10) {
		return p, ErrGeneration
	}
	target, err := s.target(ctx, p)
	if err != nil {
		return p, err
	}
	if s.profile == ProfileManaged {
		p.runtimeGeneration = uint64(target.Generation)
		p.runtimeArtifact = string(target.ActiveGatewayArtifact.ArtifactID)
	}
	if err = s.requireInstallJournal(ctx, p); err != nil {
		return p, err
	}
	return p, nil
}

func (s *Service) keyPair(ctx context.Context, target, keeper string, retained bool, consume func(string, string) error) error {
	return s.operationSecrets(ctx, "", "replace", target, keeper, retained, consume)
}

func (s *Service) operationSecrets(ctx context.Context, opID, mode, target, keeper string, retained bool, consume func(string, string) error) error {
	if mode == "rotate" {
		return s.keys.WithRotationSecrets(ctx, opID, target, retained, consume)
	}
	readTarget := s.keys.WithStagedSecret
	if retained && mode != "add" {
		readTarget = s.keys.WithRetainedSecret
	}
	return readTarget(ctx, target, func(old string) error {
		if keeper == "" {
			return consume(old, "")
		}
		return s.keys.WithStagedSecret(ctx, keeper, func(next string) error {
			if old == next {
				return cpa.ErrSourceConflict
			}
			return consume(old, next)
		})
	})
}

func sameKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, v := range a {
		if !slices.Contains(b, v) {
			return false
		}
	}
	return true
}

func keyDigests(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, freshkeys.SecretDigest(key))
	}
	slices.Sort(out)
	return out
}

func keyOperationSets(o *repo.Operation, step, old, next string) (before, after []string) {
	before = slices.Clone(o.SnapshotSHA256)
	if len(before) == 0 {
		if o.Mode == "add" {
			return nil, []string{freshkeys.SecretDigest(old)}
		}
		before = []string{freshkeys.SecretDigest(old)}
		if o.Mode == "revoke" && next != "" {
			before = insertDigest(before, freshkeys.SecretDigest(next))
		}
	}
	if step == "add" {
		added := next
		if o.Mode == "add" {
			added = old
		}
		return before, insertDigest(before, freshkeys.SecretDigest(added))
	}
	if step == "rotate" {
		target := freshkeys.SecretDigest(old)
		trimmed := slices.DeleteFunc(slices.Clone(before), func(digest string) bool { return digest == target })
		return before, insertDigest(trimmed, freshkeys.SecretDigest(next))
	}
	if o.Mode == "replace" {
		before = insertDigest(before, freshkeys.SecretDigest(next))
	}
	target := freshkeys.SecretDigest(old)
	after = slices.DeleteFunc(slices.Clone(before), func(digest string) bool { return digest == target })
	return before, after
}

func insertDigest(set []string, digest string) []string {
	if slices.Contains(set, digest) {
		out := slices.Clone(set)
		slices.Sort(out)
		return out
	}
	out := append(slices.Clone(set), digest)
	slices.Sort(out)
	return out
}

func keySetAfter(mode string, keys []string, step, old, next string) []string {
	if step == "add" {
		added := next
		if mode == "add" {
			added = old
		}
		if slices.Contains(keys, added) {
			return slices.Clone(keys)
		}
		return append(slices.Clone(keys), added)
	}
	if step == "rotate" {
		out := make([]string, 0, len(keys))
		seen := make(map[string]struct{}, len(keys))
		for _, key := range keys {
			candidate := key
			if key == old {
				candidate = next
			}
			if candidate == "" {
				continue
			}
			if _, dup := seen[candidate]; dup {
				continue
			}
			seen[candidate] = struct{}{}
			out = append(out, candidate)
		}
		return out
	}
	return slices.DeleteFunc(slices.Clone(keys), func(key string) bool { return key == old })
}

func (s *Service) CheckOperation(ctx context.Context, id string, generation uint64) (OperationResult, error) {
	s = s.active()
	if s == nil {
		return OperationResult{}, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.KeyOperationsEnabled() {
		return OperationResult{}, ErrUnavailable
	}
	var o *repo.Operation
	var err error
	if id == "latest" {
		o, err = s.repo.LatestOperation(ctx)
	} else if !freshkeys.ValidRequestID(id) {
		return OperationResult{}, freshkeys.ErrRequest
	} else {
		o, err = s.repo.Operation(ctx, id)
	}
	if err != nil {
		return OperationResult{}, err
	}
	if o == nil {
		return OperationResult{RuntimeState: "effect_unknown"}, nil
	}
	return s.check(ctx, o, generation)
}

func (s *Service) check(ctx context.Context, o *repo.Operation, generation uint64) (OperationResult, error) {
	if s.profile == ProfileExternal {
		return s.externalCheckOperation(ctx, o, generation)
	}
	return s.checkOperation(ctx, o, generation)
}

func (s *Service) checkOperation(ctx context.Context, o *repo.Operation, generation uint64) (OperationResult, error) {
	r := OperationResult{Operation: o, RuntimeState: "effect_unknown"}
	if s.h1 != nil && o.ParentID == "" {
		return r, ErrUnavailable
	}
	historical := false
	if o.Outcome == repo.OutcomeCommitted {
		latest, err := s.repo.LatestOperation(ctx)
		if err != nil {
			return r, err
		}
		if latest == nil {
			return r, repo.ErrConflict
		}
		if latest.ID != o.ID {
			if o.RunID != latest.RunID || o.Generation != latest.Generation || o.ProviderSeedSHA256 != latest.ProviderSeedSHA256 || o.Generation != strconv.FormatUint(generation, 10) {
				return r, ErrGeneration
			}
			historical = true
			if s.profile != ProfileManaged {
				return r, nil
			}
		}
	}
	p, err := s.operationTarget(ctx, generation)
	if err != nil {
		return r, err
	}
	if o.RunID != p.RunID || o.Generation != p.Generation || o.ProviderSeedSHA256 != p.S2B.Binding() {
		return r, ErrGeneration
	}
	if o.Outcome == repo.OutcomeAborted {
		r.RuntimeState = "not_applied"
		return r, nil
	}
	if o.Outcome == "committed" && o.Mode == "rotate" && s.keys != nil {
		if state, superseded, supErr := s.keys.CommittedRotateSupersededState(ctx, o.ID, o.TargetID); supErr == nil && superseded {
			r.RuntimeState = state
			return r, nil
		}
	}
	if historical {
		return r, nil
	}
	if s.h1 != nil && o.Outcome == "committed" {
		if err = s.finishH1(ctx, o); err != nil {
			return r, err
		}
	}
	if s.h1 != nil && o.Outcome != "committed" {
		v, e := s.h1.Parent(ctx, s.wire.(*cpa.Transport), o.ParentID)
		if e != nil {
			return r, e
		}
		parent := v.H1.Parent
		if parent.SourceID != o.SourceID || parent.Generation != o.Generation || parent.ClaimSHA256 != o.ClaimSHA256 || parent.ClaimRevision != o.ClaimRevision || cpa.H1Terminal(parent.Intent) != o.IntentDigest || parent.Intent.RequestID != o.ID || parent.Intent.TargetID != o.TargetID || parent.Intent.KeeperID != o.KeeperID || parent.Intent.Mode != o.Mode {
			return r, cpa.ErrSourceConflict
		}
	}
	w, ok := s.wire.(keyWire)
	if !ok {
		return r, ErrUnavailable
	}
	for i, step := range o.Steps {
		if step.State == "unattempted" {
			// A matching live add can settle without resending a PUT. Standalone
			// add commits here; replace still requires its revoke step.
			if s.h1 == nil && s.profile == ProfileManaged &&
				(((o.Mode == "replace" || o.Mode == "add") && step.Name == "add") || (o.Mode == "rotate" && step.Name == "rotate")) {
				alreadySettled := false
				err := s.operationSecrets(ctx, o.ID, o.Mode, o.TargetID, o.KeeperID, false, func(old, next string) error {
					_, after := keyOperationSets(o, step.Name, old, next)
					snap, err := w.KeySnapshot(ctx, p.ManagementKey)
					if err != nil {
						return err
					}
					if !slices.Equal(keyDigests(snap.Keys), after) {
						return nil
					}
					accepted := next
					var rejected []string
					if o.Mode == "add" {
						accepted = old
					} else if step.Name == "rotate" {
						rejected = []string{old}
					}
					authOK, err := w.SampleKeySet(ctx, []string{accepted}, rejected)
					if err != nil {
						return err
					}
					if !authOK {
						return cpa.ErrApplyUnknown
					}
					if err := s.managedObservationHeld(ctx, p, w, snap); err != nil {
						return err
					}
					s.authCache.recordProven(authProofVersion{
						profile: s.profile, target: p.Identity, runID: p.RunID,
						runtimeGeneration: p.runtimeGeneration, runtimeArtifact: p.runtimeArtifact,
						sourceSHA256: snap.SourceSHA256, keysSHA256: orderedKeysDigest(snap.Keys),
					}, snap.Keys, []string{accepted})
					alreadySettled = true
					return nil
				})
				if err != nil {
					return r, err
				}
				if alreadySettled {
					o.Steps[i].State = "observed"
					o.Outcome = "pending"
					if o.Mode == "add" || o.Mode == "rotate" {
						o.Outcome = "committed"
						o.CompletedAtMS = max(time.Now().UnixMilli(), o.CreatedAtMS)
					}
					if err = s.repo.SaveOperation(ctx, *o); err != nil {
						return r, err
					}
					o.Revision++
					continue
				}
			}
			r.NextStep = step.Name
			return r, nil
		}
		if step.State == "observed" && i < len(o.Steps)-1 {
			continue
		}
		if s.h1 != nil && o.Outcome != "committed" {
			if !step.HTTP200 {
				return r, cpa.ErrApplyUnknown
			}
			t := s.wire.(*cpa.Transport)
			receipt, e := s.h1.Commit(ctx, t, o.ParentID, i)
			if e != nil {
				return r, e
			}
			if receipt.ID != step.RuntimeID || receipt.BeginRevision != step.BeginRevision || receipt.Receipt == "" {
				return r, cpa.ErrApplyUnknown
			}
			if o.Steps[i].Receipt == "" {
				o.Steps[i].Receipt = receipt.Receipt
				if e = s.repo.SaveOperation(ctx, *o); e != nil {
					return r, e
				}
				o.Revision++
			}
		}
		err = s.operationSecrets(ctx, o.ID, o.Mode, o.TargetID, o.KeeperID, o.Outcome == "committed", func(old, next string) error {
			_, after := keyOperationSets(o, step.Name, old, next)
			var reject []string
			if step.Name != "add" {
				reject = []string{old}
			}
			var finalSnap cpa.KeySnapshot
			var provenSecrets []string
			for n := range 3 {
				snap, e := w.KeySnapshot(ctx, p.ManagementKey)
				if e != nil {
					return e
				}
				if !slices.Equal(keyDigests(snap.Keys), after) {
					return cpa.ErrApplyUnknown
				}
				accepted := snap.Keys
				if s.profile == ProfileManaged {
					switch {
					case step.Name == "add" && o.Mode == "add":
						accepted = []string{old}
					case step.Name == "add":
						accepted = []string{next}
					case next != "":
						accepted = []string{next}
					case len(snap.Keys) > 0:
						accepted = []string{snap.Keys[0]}
					}
				}
				ok, e := w.SampleKeySet(ctx, accepted, reject)
				if e != nil {
					return e
				}
				if !ok {
					return cpa.ErrApplyUnknown
				}
				finalSnap = snap
				provenSecrets = accepted
				if n < 2 {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(100 * time.Millisecond):
					}
				}
			}
			_, e := s.target(ctx, p)
			if e != nil {
				return e
			}
			snap, e := w.KeySnapshot(ctx, p.ManagementKey)
			if e != nil || !slices.Equal(keyDigests(snap.Keys), after) {
				return cpa.ErrApplyUnknown
			}
			if s.profile == ProfileManaged {
				if err := s.managedObservationHeld(ctx, p, w, snap); err != nil {
					return err
				}
			}
			if s.profile == ProfileManaged && snap.SourceSHA256 == finalSnap.SourceSHA256 {
				s.authCache.recordProven(authProofVersion{
					profile:           s.profile,
					target:            p.Identity,
					runID:             p.RunID,
					runtimeGeneration: p.runtimeGeneration,
					runtimeArtifact:   p.runtimeArtifact,
					sourceSHA256:      snap.SourceSHA256,
					keysSHA256:        orderedKeysDigest(snap.Keys),
				}, snap.Keys, provenSecrets)
			}
			return nil
		})
		if err != nil {
			return r, err
		}
		if o.Outcome != "committed" {
			o.Steps[i].State = "observed"
			o.Outcome = "pending"
			if step.Name == "revoke" || step.Name == "rotate" || o.Mode == "add" {
				o.Outcome = "committed"
				o.CompletedAtMS = max(time.Now().UnixMilli(), o.CreatedAtMS)
			}
			if err = s.repo.SaveOperation(ctx, *o); err != nil {
				return r, err
			}
			o.Revision++
		}
	}
	if s.h1 != nil {
		if err = s.finishH1(ctx, o); err != nil {
			return r, err
		}
	}
	r.RuntimeState = "effective_observed"
	r.ExpiresAtMS = time.Now().Add(5 * time.Second).UnixMilli()
	if r.ExpiresAtMS > p.ExpiresAtMS {
		r.ExpiresAtMS = p.ExpiresAtMS
	}
	return r, nil
}

func (s *Service) ContinueOperation(ctx context.Context, id string, generation uint64) (OperationResult, error) {
	r := OperationResult{RuntimeState: "effect_unknown"}
	s = s.active()
	if s == nil {
		return r, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.KeyOperationsEnabled() || !freshkeys.ValidRequestID(id) {
		return r, ErrUnavailable
	}
	o, err := s.repo.Operation(ctx, id)
	if err != nil {
		return r, err
	}
	if o == nil {
		return r, freshkeys.ErrNotFound
	}
	// Always reconcile first. An attempted unknown step is never sent again.
	r, err = s.check(ctx, o, generation)
	if err != nil {
		return r, err
	}
	if o.Outcome == repo.OutcomeAborted {
		return r, repo.ErrConflict
	}
	latest, err := s.repo.LatestOperation(ctx)
	if err != nil || latest == nil || latest.ID != id {
		return r, repo.ErrConflict
	}
	if o.Outcome == "committed" {
		return r, nil
	}
	index := -1
	for i, step := range o.Steps {
		if step.State == "unattempted" {
			index = i
			break
		}
	}
	if index < 0 {
		return r, cpa.ErrApplyUnknown
	}
	if s.h1 != nil {
		return s.continueH1(ctx, o, index, generation)
	}
	if s.profile == ProfileExternal {
		return s.externalContinue(ctx, o, index, generation)
	}
	p, err := s.operationTarget(ctx, generation)
	if err != nil {
		return r, err
	}
	step := o.Steps[index].Name
	grant, err := s.keyPermit(ctx, o.ID, step)
	if err != nil {
		return r, err
	}
	now := time.Now().UnixMilli()
	if grant.OperationID != o.ID || grant.Step != step || grant.RunID != o.RunID || grant.Generation != o.Generation || grant.IssuedAtMS < s.startedAtMS || grant.IssuedAtMS < o.CreatedAtMS || grant.IssuedAtMS > now || grant.ExpiresAtMS <= now || grant.ExpiresAtMS-grant.IssuedAtMS > 60000 || !identity.IsValidSHA256Hex(grant.SourceSHA256) {
		return r, ErrUnavailable
	}
	w, ok := s.wire.(keyWire)
	if !ok {
		return r, ErrUnavailable
	}
	err = s.operationSecrets(ctx, o.ID, o.Mode, o.TargetID, o.KeeperID, false, func(old, next string) error {
		before, _ := keyOperationSets(o, step, old, next)
		snap, e := w.KeySnapshot(ctx, p.ManagementKey)
		if e != nil {
			return e
		}
		if snap.SourceSHA256 != grant.SourceSHA256 || !slices.Equal(keyDigests(snap.Keys), before) {
			return cpa.ErrSourceConflict
		}
		if s.profile == ProfilePrivate {
			ok, e := w.SampleKeySet(ctx, snap.Keys, nil)
			if e != nil {
				return e
			}
			if !ok {
				return cpa.ErrSourceConflict
			}
		} else if len(snap.Keys) > 0 {
			probe := snap.Keys[0]
			if o.Mode != "add" {
				probe = old
			}
			if err := s.verifyPreStepManagedAuth(ctx, w, p, snap, probe); err != nil {
				return err
			}
		}
		if !s.keyGrantHeld(ctx, grant, o.ID, step) {
			return ErrUnavailable
		}
		if _, e = s.target(ctx, p); e != nil {
			return e
		}
		snap, e = w.KeySnapshot(ctx, p.ManagementKey)
		if e != nil || snap.SourceSHA256 != grant.SourceSHA256 || !slices.Equal(keyDigests(snap.Keys), before) {
			return cpa.ErrSourceConflict
		}
		if time.Now().UnixMilli() >= grant.ExpiresAtMS {
			return ErrUnavailable
		}
		o.Steps[index].State = "attempted"
		o.Outcome = "unknown"
		if e = s.repo.SaveOperation(ctx, *o); e != nil {
			return e
		}
		o.Revision++
		if !s.keyGrantHeld(ctx, grant, o.ID, step) || time.Now().UnixMilli() >= grant.ExpiresAtMS {
			return ErrUnavailable
		}
		if _, e = s.target(ctx, p); e != nil {
			return e
		}
		writeErr := w.WriteKeySet(ctx, p.ManagementKey, keySetAfter(o.Mode, snap.Keys, step, old, next))
		o.Steps[index].Returned = true
		saveCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if e = s.repo.SaveOperation(saveCtx, *o); e != nil {
			return e
		}
		o.Revision++
		return writeErr
	})
	if err != nil {
		return OperationResult{Operation: o, RuntimeState: "effect_unknown"}, err
	}
	return s.checkOperation(ctx, o, generation)
}
