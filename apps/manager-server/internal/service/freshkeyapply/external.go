package freshkeyapply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
)

// ProfileExternal marks the authority of a committed Fresh Connect External
// installation: the user's own CPA, reached through the protected Manager
// connection with the management credential the user supplied. It is neither the
// managed destination nor the Host-issued private permit, and this Manager owns
// no Runtime for it, so no lifecycle, artifact or readiness fact exists here.
const ProfileExternal = "external"

// ErrExternalUnavailable reports an External authority that cannot be resolved:
// no committed checkpoint, no protected connection, or a read-back the user's CPA
// did not answer. It is separate from ErrUnavailable because that one names the
// private profile's Host proof, which an External installation never holds.
var ErrExternalUnavailable = errors.New("external cpa authority unavailable")

// externalIdentityPrefix names an External connection by the digest of its
// normalized address. The strategy service persists exactly this value, so the
// address digest is the only comparable durable fact about the connection: the
// management credential itself is a protected envelope.
const externalIdentityPrefix = "external:sha256:"

// ExternalIdentity is the connection identity of one External CPA address. The
// value is the digest of the exact bytes it is given, so a caller that compares
// identities must pass the address it persisted, spelled the same way. The
// Upgrade External connect step canonicalizes the address first
// (identity.CanonicalExternalURL), which is what makes the three spellings of one
// CPA reach this derivation as one value.
func ExternalIdentity(baseURL string) string {
	digest := sha256.Sum256([]byte(baseURL))
	return externalIdentityPrefix + hex.EncodeToString(digest[:])
}

// ExternalServerFingerprint is the informational companion of ExternalIdentity:
// the digest of the server facts a rebind compares when the address alone cannot
// tell two CPAs apart (two CPAs published behind one URL). It is recorded beside
// the identity and is never the input of the moved-target decision, which stays
// address-based so a CPA restarting with a new build cannot strand an
// installation.
func ExternalServerFingerprint(version, sourceSHA256 string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(version) + "\x00" + strings.ToLower(strings.TrimSpace(sourceSHA256))))
	return externalIdentityPrefix + hex.EncodeToString(digest[:])
}

func validExternalIdentity(value string) bool {
	return len(value) == len(externalIdentityPrefix)+64 &&
		value[:len(externalIdentityPrefix)] == externalIdentityPrefix &&
		identity.IsValidSHA256Hex(value[len(externalIdentityPrefix):])
}

// ExternalInstall is the durable authority of a committed External installation.
// It is re-derived from the checkpoint and the protected Manager connection on
// every call; no credential is cached.
type ExternalInstall struct {
	// Identity is external:sha256:<digest of the normalized committed address>.
	Identity string
	// Checkpoint is the committed strategy generation the page and the key
	// journal pass back, exactly as the managed destination's is.
	Checkpoint uint64
	// BaseURL is the normalized address the Manager connection holds.
	BaseURL string
	// ManagementKey is the decrypted credential of that connection.
	ManagementKey string
}

// Generation is the durable generation of one External target, and it is the
// connection identity alone. A user's CPA is administered by the user: its
// configuration changes for reasons this Manager does not control, not least its
// own key writes, which land in the same document the digest would cover. Binding
// the journal to that digest would therefore invalidate every External operation
// after its own successful write and wedge the journal forever. The configuration
// digest is a per-step grant CAS instead: it is read immediately before each PUT
// and compared there (see externalKeyGrant and externalContinue).
func (i ExternalInstall) Generation() string {
	return i.Identity
}

// ExternalSource reloads and validates the External authority on every call. It
// MUST fail closed with ErrExternalUnavailable when the checkpoint or the
// protected connection is unavailable, and with ErrExternalTargetMoved when the
// connection no longer names the committed target. It never contacts the user's
// CPA: an outage there is a property of a single read-back, not of the authority,
// so a credential CPA refuses stays the typed blocker of the read that met it.
type ExternalSource func(context.Context) (ExternalInstall, error)

// externalWire is the External CPA v8 surface this authority drives. There is no
// managed template and no provider seed: the only comparable facts are key
// membership and the whole-document digest of the user's own configuration.
type externalWire interface {
	ExternalKeySnapshot(context.Context, string) (cpa.ExternalKeySnapshot, error)
	ExternalWriteKeySet(context.Context, string, []string) error
}

// NewExternal constructs the authority of a committed Connect External
// installation. The key surfaces and the gateway configuration page act through
// it; chat admission, gateway admission and the Runtime surfaces never do.
func NewExternal(r *repo.Repository, keys *freshkeys.Service, source ExternalSource) *Service {
	s := New(r, keys, nil)
	s.profile = ProfileExternal
	s.externalSource = source
	s.externalWire = func(endpoint string) externalWire { return cpa.NewTransportForEndpoint(endpoint) }
	s.externalProbe = cpa.ObserveExternalVersion
	s.externalRebindVerifier = ObserveExternalRebindCredential
	// The adapter is the wire every step of this profile speaks: it resolves the
	// committed target first, so no step is taken against a stale credential.
	s.wire = externalKeyAuthority{s: s}
	s.keyOperations = true
	s.permit = s.externalPermit
	s.keyPermit = s.externalKeyGrant
	return s
}

// externalPermit is the step authority of the currently resolved External
// target. Its generation is the connection identity alone, not a digest of the
// configuration: the user's CPA rewrites that document for reasons this Manager
// does not control — its own key writes included — so a digest-derived
// generation would invalidate every later step. The configuration digest is a
// per-step CAS instead, read immediately before each PUT and compared there (see
// externalKeyGrant and externalContinue).
func (s *Service) externalPermit(ctx context.Context) (Permit, error) {
	install, err := s.externalAuthority(ctx)
	if err != nil {
		return Permit{}, err
	}
	now := time.Now()
	return Permit{
		RunID: install.Identity, Generation: install.Generation(), ManagementKey: install.ManagementKey,
		IssuedAtMS: now.UnixMilli(), ExpiresAtMS: now.Add(managedGrantTTL).UnixMilli(),
		Profile: ProfileExternal, Identity: install.Identity,
	}, nil
}

// externalAuthority resolves and validates the committed External target. The
// identity must name the address the connection holds, so a source that answers
// about a different CPA is refused rather than trusted.
func (s *Service) externalAuthority(ctx context.Context) (ExternalInstall, error) {
	if s == nil || s.profile != ProfileExternal || s.externalSource == nil {
		return ExternalInstall{}, ErrExternalUnavailable
	}
	install, err := s.externalSource(ctx)
	if err != nil {
		return ExternalInstall{}, err
	}
	if install.Checkpoint == 0 || install.BaseURL == "" || !cpa.ValidV8ManagementKey(install.ManagementKey) ||
		!validExternalIdentity(install.Identity) || install.Identity != ExternalIdentity(install.BaseURL) {
		return ExternalInstall{}, ErrExternalUnavailable
	}
	return install, nil
}

// ExternalTarget is the External authority's answer for exactly the committed
// generation the caller holds. It is the gateway configuration page's entry
// point: the address and the credential of the CPA this installation owns.
func (s *Service) ExternalTarget(ctx context.Context, generation uint64) (string, string, error) {
	s = s.active()
	install, err := s.externalAuthority(ctx)
	if err != nil {
		return "", "", err
	}
	if install.Checkpoint != generation {
		return "", "", ErrGeneration
	}
	return install.BaseURL, install.ManagementKey, nil
}

// externalTarget is the same answer for the key surfaces, which also need the
// identity and the read-back digest.
func (s *Service) externalTarget(ctx context.Context, generation uint64) (ExternalInstall, error) {
	install, err := s.externalAuthority(ctx)
	if err != nil {
		return ExternalInstall{}, err
	}
	if install.Checkpoint != generation {
		return ExternalInstall{}, ErrGeneration
	}
	return install, nil
}

// externalKeyWire is the plain wire of one resolved target. It carries no gate:
// the version refusal is asked explicitly by each write entry point, once, before
// the request it guards (see externalWriteGate). A gate here would ask it again
// inside the write, after the journal had already recorded the intent, and a
// timeout or a version flip there would leave a step `attempted` with no byte sent.
func (s *Service) externalKeyWire(install ExternalInstall) externalWire {
	if s.externalWire == nil {
		return nil
	}
	return s.externalWire(install.BaseURL)
}

// externalWriteRefusal is the one verdict of a version probe in front of a write.
// A refused credential and an unreachable CPA are the typed blockers of their own
// reads and pass through unchanged: neither is evidence that the version is wrong.
func externalVersionWriteRefusal(report cpa.ExternalVersionReport, err error) error {
	if err != nil {
		return err
	}
	if report.WriteBlocked {
		return fmt.Errorf("%w: %s", cpa.ErrExternalVersionUnsupported, report.Support)
	}
	return nil
}

// externalWriteGate asks the probe once for a resolved target and returns its
// verdict. The journal asks it before recording an intent, so an unsupported CPA
// is refused before a durable operation exists that no later step could send: a
// pending journal on a target this Manager cannot write would block every other
// operation of that connection forever.
func (s *Service) externalWriteGate(ctx context.Context, install ExternalInstall) error {
	if s.externalProbe == nil {
		return nil
	}
	return externalVersionWriteRefusal(s.externalProbe(ctx, install.BaseURL, install.ManagementKey))
}

// ExternalKeyPresence reports, per staged key, whether the user's CPA carries it.
// It is the read-back behind the key list: a Manager record alone never claims an
// effect the CPA does not show.
func (s *Service) ExternalKeyPresence(ctx context.Context, ids []identity.APIKeyID) (map[identity.APIKeyID]bool, error) {
	s = s.active()
	install, err := s.externalAuthority(ctx)
	if err != nil {
		return nil, err
	}
	w := s.externalKeyWire(install)
	if w == nil {
		return nil, ErrExternalUnavailable
	}
	snapshot, err := w.ExternalKeySnapshot(ctx, install.ManagementKey)
	if err != nil {
		return nil, err
	}
	present := make(map[identity.APIKeyID]bool, len(ids))
	for _, id := range ids {
		// The staged secret is decrypted here and never leaves this process.
		ok := false
		if err := s.keys.WithStagedSecret(ctx, string(id), func(secret string) error {
			ok = slices.Contains(snapshot.Keys, secret)
			return nil
		}); err != nil {
			return nil, err
		}
		present[id] = ok
	}
	return present, nil
}

// ExternalKeyPresenceOf is ExternalKeyPresence for one key, for the routes that
// answer about exactly one record.
func (s *Service) ExternalKeyPresenceOf(ctx context.Context, id identity.APIKeyID) (bool, error) {
	s = s.active()
	present, err := s.ExternalKeyPresence(ctx, []identity.APIKeyID{id})
	if err != nil {
		return false, err
	}
	return present[id], nil
}

// externalApply executes or reconciles a durable journaled "add" operation for
// one staged Client Key on the user's External CPA and requires live /v1/models
// authentication plus a post-sample checkpoint/source/config fence before
// reporting effective_observed. Because the operation journal is written before
// any PUT, an attempted unknown write is never resent on retry.
func (s *Service) externalApply(ctx context.Context, id string, generation uint64) (State, error) {
	result := State{KeyID: id, RuntimeState: "effect_unknown"}
	key, err := s.keys.Detail(ctx, id)
	if err != nil {
		return result, err
	}
	if key.Lifecycle == identity.LifecycleSuperseded {
		return result, freshkeys.ErrConflict
	}
	if s.repo == nil {
		return result, ErrExternalUnavailable
	}
	result.ManagerStaged = true

	latest, err := s.repo.LatestOperation(ctx)
	if err != nil {
		return result, err
	}
	if latest != nil && !latest.IsTerminal() {
		checkRes, checkErr := s.externalCheckOperation(ctx, latest, generation)
		latestAfter, latestErr := s.repo.LatestOperation(ctx)
		if latestErr != nil {
			return result, latestErr
		}
		latest = latestAfter
		if latest != nil && !latest.IsTerminal() {
			if latest.TargetID != id || latest.Mode != "add" || latest.KeeperID != "" {
				if checkErr != nil && !errors.Is(checkErr, cpa.ErrApplyUnknown) && !errors.Is(checkErr, cpa.ErrSourceConflict) && !errors.Is(checkErr, repo.ErrConflict) {
					return result, checkErr
				}
				return result, freshkeys.ErrConflict
			}
			result.Attempted = !latest.AllStepsUnattempted()
			if checkErr != nil {
				return result, checkErr
			}
			if checkRes.NextStep == "add" && len(latest.Steps) == 1 && latest.Steps[0].State == "unattempted" {
				contRes, contErr := s.externalContinue(ctx, latest, 0, generation)
				result.Attempted = contRes.Operation != nil && !contRes.Operation.AllStepsUnattempted()
				if contErr != nil {
					return result, contErr
				}
				return s.externalStateFromOperationResult(id, generation, true, contRes), nil
			}
			return result, cpa.ErrApplyUnknown
		}
	}

	install, err := s.externalTarget(ctx, generation)
	if err != nil {
		return result, err
	}
	w := s.externalKeyWire(install)
	if w == nil {
		return result, ErrExternalUnavailable
	}

	opID, replayCommitted, err := s.resolveExternalApplyOperationID(ctx, key, latest)
	if err != nil {
		return result, err
	}
	if replayCommitted {
		return s.externalObserve(ctx, id, generation)
	}

	var op repo.Operation
	var alreadySettled bool
	err = s.keys.WithStagedSecret(ctx, id, func(secret string) error {
		snapshot, err := w.ExternalKeySnapshot(ctx, install.ManagementKey)
		if err != nil {
			return err
		}
		if slices.Contains(snapshot.Keys, secret) {
			op = repo.Operation{
				ID: opID, TargetID: id, Mode: "add",
				RunID: install.Identity, Generation: install.Generation(),
				Revision: 1, Outcome: repo.OutcomePending,
				Steps:       []repo.KeyStep{{Name: "add", State: "unattempted"}},
				CreatedAtMS: time.Now().UnixMilli(),
			}
			if err := s.repo.BeginOperation(ctx, op); err != nil {
				return err
			}
			alreadySettled = true
			return nil
		}
		after := externalKeysAfterStep(snapshot.Keys, "add", "add", secret, "")
		if len(after) == 0 {
			return cpa.ErrSourceConflict
		}
		again, err := w.ExternalKeySnapshot(ctx, install.ManagementKey)
		if err != nil {
			return err
		}
		if again.SourceSHA256 != snapshot.SourceSHA256 || !sameKeys(again.Keys, snapshot.Keys) {
			return cpa.ErrSourceConflict
		}
		if err = s.externalWriteGate(ctx, install); err != nil {
			return err
		}
		op = repo.Operation{
			ID: opID, TargetID: id, Mode: "add",
			RunID: install.Identity, Generation: install.Generation(),
			Revision: 1, Outcome: repo.OutcomeUnknown,
			Steps:       []repo.KeyStep{{Name: "add", State: "attempted"}},
			CreatedAtMS: time.Now().UnixMilli(),
		}
		if err = s.repo.BeginOperation(ctx, op); err != nil {
			return err
		}
		result.Attempted = true
		writeErr := w.ExternalWriteKeySet(ctx, install.ManagementKey, after)
		if errors.Is(writeErr, cpa.ErrManagementRejected) ||
			errors.Is(writeErr, cpa.ErrExternalRejectionUnavailable) ||
			errors.Is(writeErr, cpa.ErrExternalTargetMoved) {
			op.Steps[0].Returned = false
			op.Steps[0].State = "unattempted"
			op.Outcome = repo.OutcomePending
			result.Attempted = false
		} else {
			op.Steps[0].Returned = true
		}
		saveCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if saveErr := s.repo.SaveOperation(saveCtx, op); saveErr != nil {
			return saveErr
		}
		op.Revision++
		return writeErr
	})
	if err != nil {
		return result, err
	}
	if alreadySettled {
		result.CPAConfigured = true
	}
	opRes, checkErr := s.externalCheckOperation(ctx, &op, generation)
	if checkErr != nil {
		return result, checkErr
	}
	if opRes.Operation == nil || opRes.Operation.Outcome != repo.OutcomeCommitted {
		return result, cpa.ErrApplyUnknown
	}
	return s.externalStateFromOperationResult(id, generation, true, opRes), nil
}

func (s *Service) resolveExternalApplyOperationID(ctx context.Context, key freshkeys.Key, latest *repo.Operation) (string, bool, error) {
	id := string(key.ID)
	if latest != nil && latest.TargetID == id && latest.Mode == "add" && latest.KeeperID == "" && latest.Outcome == repo.OutcomeCommitted {
		return latest.ID, true, nil
	}
	opID := key.RequestID
	if !freshkeys.ValidRequestID(opID) {
		opID = id
	}
	latestAdd, err := s.repo.LatestAddOperation(ctx, id, opID)
	if err != nil {
		return "", false, err
	}
	if latestAdd != nil {
		if latestAdd.Outcome == repo.OutcomeCommitted {
			return latestAdd.ID, true, nil
		}
		if latestAdd.Outcome != repo.OutcomeAborted {
			return "", false, freshkeys.ErrConflict
		}
	}
	byReq, err := s.repo.Operation(ctx, opID)
	if err != nil {
		return "", false, err
	}
	if byReq == nil {
		return opID, false, nil
	}
	if byReq.TargetID != id || byReq.Mode != "add" || byReq.KeeperID != "" {
		return "", false, freshkeys.ErrConflict
	}
	if byReq.Outcome == repo.OutcomeCommitted {
		return byReq.ID, true, nil
	}
	if byReq.Outcome == repo.OutcomeAborted {
		nextID, genErr := identity.NewAPIKeyID()
		if genErr != nil {
			return "", false, ErrExternalUnavailable
		}
		return string(nextID), false, nil
	}
	return "", false, freshkeys.ErrConflict
}

func (s *Service) externalStateFromOperationResult(id string, generation uint64, attempted bool, opRes OperationResult) State {
	now := time.Now().UnixMilli()
	expires := opRes.ExpiresAtMS
	if expires <= 0 {
		expires = now + int64(5*time.Second/time.Millisecond)
	}
	return State{
		KeyID:             id,
		ManagerStaged:     true,
		CPAConfigured:     true,
		RuntimeState:      "effective_observed",
		RuntimeGeneration: strconv.FormatUint(generation, 10),
		ObservedFromMS:    now,
		ObservedUntilMS:   now,
		ExpiresAtMS:       expires,
		Attempted:         attempted,
	}
}

// externalObserve reads the user's CPA and verifies live /v1/models client
// authentication plus a post-sample checkpoint/source/config fence before
// reporting effective_observed. Management config echo alone never certifies an
// External Client Key as effective.
func (s *Service) externalObserve(ctx context.Context, id string, generation uint64) (State, error) {
	result := State{KeyID: id, RuntimeState: "effect_unknown"}
	key, err := s.keys.Detail(ctx, id)
	if err != nil {
		return result, err
	}
	result.ManagerStaged = true

	var latest *repo.Operation
	if s.repo != nil {
		latest, err = s.repo.LatestOperation(ctx)
		if err != nil {
			return result, err
		}
		if latest != nil && !latest.IsTerminal() && (latest.TargetID == id || latest.KeeperID == id) {
			if _, reconcileErr := s.externalCheckOperation(ctx, latest, generation); reconcileErr != nil &&
				!errors.Is(reconcileErr, cpa.ErrApplyUnknown) &&
				!errors.Is(reconcileErr, cpa.ErrSourceConflict) &&
				!errors.Is(reconcileErr, repo.ErrConflict) {
				return result, reconcileErr
			}
			latest, err = s.repo.LatestOperation(ctx)
			if err != nil {
				return result, err
			}
		}
	}

	install, err := s.externalTarget(ctx, generation)
	if err != nil {
		return result, err
	}
	w := s.externalKeyWire(install)
	if w == nil {
		return result, ErrExternalUnavailable
	}
	snap, err := w.ExternalKeySnapshot(ctx, install.ManagementKey)
	if err != nil {
		return result, err
	}
	result.RuntimeGeneration = strconv.FormatUint(generation, 10)

	key, err = s.keys.Detail(ctx, id)
	if err != nil {
		return result, err
	}
	if key.Lifecycle == identity.LifecycleSuperseded {
		return result, nil
	}

	var secret string
	if err := s.keys.WithStagedSecret(ctx, id, func(val string) error {
		secret = val
		return nil
	}); err != nil {
		return result, err
	}

	present := slices.Contains(snap.Keys, secret)
	attempted := present
	if s.repo != nil {
		if op, opErr := s.repo.LatestAddOperation(ctx, id, key.RequestID); opErr != nil {
			return result, opErr
		} else if op != nil && !op.AllStepsUnattempted() {
			attempted = true
		}
		if latest != nil && (latest.TargetID == id || latest.KeeperID == id) && !latest.AllStepsUnattempted() {
			attempted = true
		}
	}
	result.CPAConfigured = present
	result.Attempted = attempted
	result.InstallAvailable = !present && !attempted
	if !present {
		return result, nil
	}
	if latest != nil && !latest.IsTerminal() && (latest.TargetID == id || latest.KeeperID == id) {
		return result, nil
	}

	sampler, ok := w.(externalAuthWire)
	if !ok {
		return result, nil
	}
	from := time.Now()
	authOK, authErr := sampler.ExternalSampleKeySet(ctx, []string{secret}, nil)
	if authErr != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, nil
	}
	if !authOK {
		return result, nil
	}
	if err := s.externalObservationHeld(ctx, install, w, snap); err != nil {
		if errors.Is(err, cpa.ErrApplyUnknown) {
			return result, nil
		}
		return result, err
	}
	now := time.Now()
	s.authCache.recordProven(authProofVersion{
		profile:      ProfileExternal,
		target:       install.Identity,
		runID:        install.Generation(),
		sourceSHA256: snap.SourceSHA256,
		keysSHA256:   orderedKeysDigest(snap.Keys),
	}, snap.Keys, []string{secret})
	result.RuntimeState = "effective_observed"
	result.ObservedFromMS = from.UnixMilli()
	result.ObservedUntilMS = now.UnixMilli()
	result.ExpiresAtMS = now.Add(5 * time.Second).UnixMilli()
	return result, nil
}

// externalKeyGrant derives the per-step authority for one durable External key
// operation from the live target: the connection identity, the checkpoint
// generation and the configuration digest observed immediately before the step.
func (s *Service) externalKeyGrant(ctx context.Context, operationID, step string) (KeyPermit, error) {
	if operationID == "" || (step != "add" && step != "revoke") {
		return KeyPermit{}, ErrExternalUnavailable
	}
	install, err := s.externalAuthority(ctx)
	if err != nil {
		return KeyPermit{}, err
	}
	w := s.externalKeyWire(install)
	if w == nil {
		return KeyPermit{}, ErrExternalUnavailable
	}
	snapshot, err := w.ExternalKeySnapshot(ctx, install.ManagementKey)
	if err != nil || !identity.IsValidSHA256Hex(snapshot.SourceSHA256) {
		if err != nil {
			return KeyPermit{}, err
		}
		return KeyPermit{}, ErrExternalUnavailable
	}
	now := time.Now()
	return KeyPermit{
		Version: 1, SourceID: install.Identity, Revision: 1, OperationID: operationID, Step: step,
		RunID: install.Identity, Generation: install.Generation(),
		SourceSHA256: snapshot.SourceSHA256,
		IssuedAtMS:   now.UnixMilli(), ExpiresAtMS: now.Add(managedGrantTTL).UnixMilli(),
	}, nil
}

// externalKeyAuthority adapts the External CPA to the key loop the durable
// journal already speaks. Every method resolves the committed target first and
// refuses a credential that is not the current one, so a step can never be taken
// against a connection this installation no longer proves.
type externalKeyAuthority struct{ s *Service }

func (a externalKeyAuthority) resolved(ctx context.Context, management string) (ExternalInstall, externalWire, error) {
	install, err := a.s.externalAuthority(ctx)
	if err != nil {
		return ExternalInstall{}, nil, err
	}
	if management != install.ManagementKey {
		return ExternalInstall{}, nil, ErrGeneration
	}
	w := a.s.externalKeyWire(install)
	if w == nil {
		return ExternalInstall{}, nil, ErrExternalUnavailable
	}
	return install, w, nil
}

func (a externalKeyAuthority) KeySnapshot(ctx context.Context, management string) (cpa.KeySnapshot, error) {
	install, w, err := a.resolved(ctx, management)
	if err != nil {
		return cpa.KeySnapshot{}, err
	}
	snapshot, err := w.ExternalKeySnapshot(ctx, install.ManagementKey)
	if err != nil {
		return cpa.KeySnapshot{}, err
	}
	return cpa.KeySnapshot{Keys: snapshot.Keys, SourceSHA256: snapshot.SourceSHA256, SourceID: install.Identity, Generation: install.Generation()}, nil
}

func (a externalKeyAuthority) WriteKeySet(ctx context.Context, management string, keys []string) error {
	install, w, err := a.resolved(ctx, management)
	if err != nil {
		return err
	}
	// The same explicit gate the journaled step and the Apply install take, so no
	// path reaches the user's CPA with a write the version does not allow.
	if err = a.s.externalWriteGate(ctx, install); err != nil {
		return err
	}
	return w.ExternalWriteKeySet(ctx, install.ManagementKey, keys)
}

// The External profile owns no install, no chat admission and no Runtime
// observation. These three are unreachable for it — Apply and Observe are
// dispatched to the External read-back before any of them is called — and they
// refuse rather than pretend.
func (a externalKeyAuthority) InstallOne(context.Context, cpa.TargetProof, string, string) (cpa.InstallResult, error) {
	return cpa.InstallResult{}, ErrExternalUnavailable
}

func (a externalKeyAuthority) Configured(context.Context, string, string) (bool, error) {
	return false, ErrExternalUnavailable
}

func (a externalKeyAuthority) AuthSweep(context.Context, string) (bool, error) {
	return false, ErrExternalUnavailable
}
