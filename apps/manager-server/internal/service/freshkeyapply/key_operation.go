package freshkeyapply

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"time"

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
	// list as its own. It is the policy `WriteKeySet` applies before it sends
	// anything, asked ahead of the attempt: a step whose end state the destination
	// could never hold must be refused while nothing is journaled, because after
	// the attempt is recorded the refusal leaves an unknown effect that blocks
	// every later operation of the installation.
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
		// The External journal is the Manager's own; the private profile's
		// source-file fence describes a config authority External never has.
		return true
	}
	if s.h1 != nil {
		return os.Getenv("CPAMP_PRIVATE_CONFIG_AUTHORITY") == "1"
	}
	// A config-owned source can never use this independent K/U journal. This
	// additional fail-closed fence does NOT attest Host lifecycle exclusivity.
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
	if !s.KeyOperationsEnabled() {
		return result, ErrUnavailable
	}
	for _, v := range []string{id, target, keeper} {
		if !freshkeys.ValidRequestID(v) {
			return result, freshkeys.ErrRequest
		}
	}
	if target == keeper || (mode != "revoke" && mode != "replace") {
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
	if s.profile == ProfileExternal {
		return s.externalPrepare(ctx, id, target, keeper, mode, generation)
	}
	p, snapshot, err := s.keyOperationPrerequisites(ctx, target, keeper, mode, generation)
	if err != nil {
		return result, err
	}
	steps := []repo.KeyStep{{Name: "revoke", State: "unattempted"}}
	if mode == "replace" {
		steps = append([]repo.KeyStep{{Name: "add", State: "unattempted"}}, steps...)
	}
	o := repo.Operation{ID: id, TargetID: target, KeeperID: keeper, Mode: mode, RunID: p.RunID, Generation: p.Generation, ProviderSeedSHA256: p.S2B.Binding(), SnapshotSHA256: snapshot, Revision: 1, Outcome: "pending", Steps: steps, CreatedAtMS: time.Now().UnixMilli()}
	if s.h1 != nil {
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
	return s.checkOperation(ctx, &o, generation)
}

// keyOperationPrerequisites is Prepare's authority and destination proof, and the
// snapshot digests it validated. It journals and writes nothing, so the page can
// ask the same question — and get the same answer — before it offers the
// operation instead of discovering a limit as a conflict after a submit.
func (s *Service) keyOperationPrerequisites(ctx context.Context, target, keeper, mode string, generation uint64) (Permit, []string, error) {
	p, err := s.operationTarget(ctx, generation)
	if err != nil {
		return p, nil, err
	}
	installed, err := s.installedKey(ctx)
	if err != nil || installed != target && (mode != "revoke" || installed != keeper) {
		return p, nil, repo.ErrConflict
	}
	w, ok := s.wire.(keyWire)
	if !ok {
		return p, nil, ErrUnavailable
	}
	var snapshot []string
	err = s.keyPair(ctx, target, keeper, false, func(old, next string) error {
		snap, e := w.KeySnapshot(ctx, p.ManagementKey)
		if e != nil {
			return e
		}
		if mode == "revoke" && len(snap.Keys) == 1 {
			return ErrLastKey
		}
		// Both keys the operation names must be carried by the destination, and the
		// replacement of a replace must not be. The write replaces the whole list,
		// so the rest of that list is authority this step is about to consume: every
		// other key must be one this Manager stages and still keeps. A credential it
		// never registered, or one it already retired, is the operator's change at
		// the destination and is never silently dropped by a whole-list write.
		if !slices.Contains(snap.Keys, old) || (mode == "revoke" && !slices.Contains(snap.Keys, next)) {
			return cpa.ErrSourceConflict
		}
		if mode == "replace" && slices.Contains(snap.Keys, next) {
			return cpa.ErrSourceConflict
		}
		// The write replaces the whole list, so the list each step leaves behind is a
		// fact this Manager states before anything is journaled. Walk the steps in
		// the order they will run — a replace adds before it revokes — and ask the
		// destination's own policy, the same one `WriteKeySet` applies, whether that
		// end state can exist. Otherwise the attempt would be recorded first, the
		// write refused before it was sent, and the journal would hold an unknown
		// effect no read-back can settle.
		steps := []string{"revoke"}
		if mode == "replace" {
			steps = []string{"add", "revoke"}
		}
		live := snap.Keys
		for _, step := range steps {
			live = keySetAfter(live, step, old, next)
			if !w.ValidKeySet(live) {
				return cpa.ErrSourceConflict
			}
		}
		known, e := s.keys.AllStaged(ctx, snap.Keys)
		if e != nil {
			return e
		}
		if !known {
			return cpa.ErrSourceConflict
		}
		// The destination is the set this step is allowed to rewrite, so it is the
		// set that must authenticate before anything is journaled.
		ok, e := w.SampleKeySet(ctx, snap.Keys, nil)
		if e != nil {
			return e
		}
		if !ok {
			return cpa.ErrSourceConflict
		}
		snapshot = keyDigests(snap.Keys)
		return nil
	})
	return p, snapshot, err
}

// Preflight answers whether one revoke or replace would pass Prepare right now,
// with the same authority, the same destination proof and nothing written. Every
// remaining limit is a fact about the destination or the installation, so the
// page can name it where the operator is choosing instead of after a submit.
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
	for _, v := range []string{target, keeper} {
		if !freshkeys.ValidRequestID(v) {
			return freshkeys.ErrRequest
		}
	}
	if target == keeper || (mode != "revoke" && mode != "replace") {
		return freshkeys.ErrRequest
	}
	if s.profile == ProfileExternal {
		return s.externalPreflight(ctx, target, keeper, mode, generation)
	}
	_, _, err := s.keyOperationPrerequisites(ctx, target, keeper, mode, generation)
	return err
}

func (s *Service) operationTarget(ctx context.Context, generation uint64) (Permit, error) {
	p, err := s.permit()
	if err != nil {
		return p, err
	}
	if p.Generation != strconv.FormatUint(generation, 10) {
		return p, ErrGeneration
	}
	if _, err = s.target(ctx, p); err != nil {
		return p, err
	}
	if err = s.requireInstallJournal(ctx, p); err != nil {
		return p, err
	}
	return p, nil
}
func (s *Service) keyPair(ctx context.Context, target, keeper string, retained bool, consume func(string, string) error) error {
	read := s.keys.WithStagedSecret
	if retained {
		read = s.keys.WithRetainedSecret
	}
	return read(ctx, target, func(old string) error {
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

// keyDigests is the canonical comparison form of a destination's Client Key list:
// the sorted digests of its secrets. A whole-list write may only replace a set this
// Manager can name, so the set — not the secrets — is what the journal carries and
// what every later read is compared against.
func keyDigests(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, freshkeys.SecretDigest(key))
	}
	slices.Sort(out)
	return out
}

// keyOperationSets derives the digest set a step reads and the one it leaves
// behind, from the single snapshot the journal proved at Prepare. An add step
// keeps every destination key and introduces the replacement; a revoke step removes
// exactly the target and keeps every other key, including keys this Manager did not
// mint but does stage — an adopted destination carries its own list.
//
// A journal written before the snapshot was recorded described each step as exactly
// the keys it named, so a pending one still reconciles by that same rule.
func keyOperationSets(o *repo.Operation, step, old, next string) (before, after []string) {
	before = slices.Clone(o.SnapshotSHA256)
	if len(before) == 0 {
		before = []string{freshkeys.SecretDigest(old)}
		if o.Mode == "revoke" {
			before = insertDigest(before, freshkeys.SecretDigest(next))
		}
	}
	if step == "add" {
		return before, insertDigest(before, freshkeys.SecretDigest(next))
	}
	if o.Mode == "replace" {
		before = insertDigest(before, freshkeys.SecretDigest(next))
	}
	target := freshkeys.SecretDigest(old)
	after = slices.DeleteFunc(slices.Clone(before), func(digest string) bool { return digest == target })
	return before, after
}

// insertDigest adds one digest to a sorted set.
func insertDigest(set []string, digest string) []string {
	out := append(slices.Clone(set), digest)
	slices.Sort(out)
	return out
}

// keySetAfter is the destination list one step leaves behind: the live list it
// proved, with the replacement appended for an add step, or the target removed for
// a revoke step. Every other key survives untouched, in its original order.
func keySetAfter(keys []string, step, old, next string) []string {
	if step == "add" {
		return append(slices.Clone(keys), next)
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

// check dispatches one journaled operation to the profile that wrote it. Both
// loops reconcile by reading their target and neither ever resends a step.
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
	if o.Outcome == "committed" {
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
			// Historical committed operations are durable records, not live
			// effectiveness claims. Their former keeper may now be revoked.
			return r, nil
		}
	}
	p, err := s.operationTarget(ctx, generation)
	if err != nil {
		return r, err
	}
	if o.RunID != p.RunID || o.Generation != p.Generation || o.ProviderSeedSHA256 != p.S2B.Binding() {
		return r, ErrGeneration
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
		p := v.H1.Parent
		if p.SourceID != o.SourceID || p.Generation != o.Generation || p.ClaimSHA256 != o.ClaimSHA256 || p.ClaimRevision != o.ClaimRevision || cpa.H1Terminal(p.Intent) != o.IntentDigest || p.Intent.RequestID != o.ID || p.Intent.TargetID != o.TargetID || p.Intent.KeeperID != o.KeeperID || p.Intent.Mode != o.Mode {
			return r, cpa.ErrSourceConflict
		}
	}
	w, ok := s.wire.(keyWire)
	if !ok {
		return r, ErrUnavailable
	}
	for i, step := range o.Steps {
		if step.State == "unattempted" {
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
		err = s.keyPair(ctx, o.TargetID, o.KeeperID, o.Outcome == "committed", func(old, next string) error {
			_, after := keyOperationSets(o, step.Name, old, next)
			var reject []string
			if step.Name != "add" {
				reject = []string{old}
			}
			// Require multiple spaced live sweeps; a config save is never an ack. The
			// expected set is the transformation the journal proved, so a key that
			// appeared or went missing is reported as unknown effect, never as success.
			for n := 0; n < 3; n++ {
				snap, e := w.KeySnapshot(ctx, p.ManagementKey)
				if e != nil {
					return e
				}
				if !slices.Equal(keyDigests(snap.Keys), after) {
					return cpa.ErrApplyUnknown
				}
				ok, e := w.SampleKeySet(ctx, snap.Keys, reject)
				if e != nil {
					return e
				}
				if !ok {
					return cpa.ErrApplyUnknown
				}
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
			return nil
		})
		if err != nil {
			return r, err
		}
		if o.Outcome != "committed" {
			o.Steps[i].State = "observed"
			o.Outcome = "pending"
			if step.Name == "revoke" {
				o.Outcome = "committed"
				o.CompletedAtMS = time.Now().UnixMilli()
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
		return r, ErrUnavailable
	}
	now := time.Now().UnixMilli()
	if grant.OperationID != o.ID || grant.Step != step || grant.RunID != o.RunID || grant.Generation != o.Generation || grant.IssuedAtMS < s.startedAtMS || grant.IssuedAtMS < o.CreatedAtMS || grant.IssuedAtMS > now || grant.ExpiresAtMS <= now || grant.ExpiresAtMS-grant.IssuedAtMS > 60000 || !identity.IsValidSHA256Hex(grant.SourceSHA256) {
		return r, ErrUnavailable
	}
	w, ok := s.wire.(keyWire)
	if !ok {
		return r, ErrUnavailable
	}
	err = s.keyPair(ctx, o.TargetID, o.KeeperID, false, func(old, next string) error {
		// The set this step reads is the one the journal proved at Prepare,
		// transformed by the steps that settled since. A destination that no longer
		// holds exactly that set — a key added, removed or retired out of band — is a
		// source conflict with nothing written, whatever its size.
		before, _ := keyOperationSets(o, grant.Step, old, next)
		snap, e := w.KeySnapshot(ctx, p.ManagementKey)
		if e != nil {
			return e
		}
		if snap.SourceSHA256 != grant.SourceSHA256 || !slices.Equal(keyDigests(snap.Keys), before) {
			return cpa.ErrSourceConflict
		}
		ok, e := w.SampleKeySet(ctx, snap.Keys, nil)
		if e != nil {
			return e
		}
		if !ok {
			return cpa.ErrSourceConflict
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
		// Recheck authority after intent, immediately before the single PUT.
		if !s.keyGrantHeld(ctx, grant, o.ID, step) || time.Now().UnixMilli() >= grant.ExpiresAtMS {
			return ErrUnavailable
		}
		if _, e = s.target(ctx, p); e != nil {
			return e
		}
		writeErr := w.WriteKeySet(ctx, p.ManagementKey, keySetAfter(snap.Keys, grant.Step, old, next))
		o.Steps[index].Returned = true
		// Use a bounded independent context to retain the attempt on client loss.
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
