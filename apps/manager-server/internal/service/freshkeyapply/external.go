package freshkeyapply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
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

// ExternalIdentity is the connection identity of one External CPA address.
func ExternalIdentity(baseURL string) string {
	digest := sha256.Sum256([]byte(baseURL))
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
func (s *Service) externalPermit() (Permit, error) {
	install, err := s.externalAuthority(context.Background())
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
		if errors.Is(err, cpa.ErrManagementRejected) {
			return ExternalInstall{}, cpa.ErrManagementRejected
		}
		// A moved target is the committed installation's own blocker: it is
		// reported as such on every surface instead of as a missing proof.
		if errors.Is(err, cpa.ErrExternalTargetMoved) {
			return ExternalInstall{}, cpa.ErrExternalTargetMoved
		}
		if errors.Is(err, ErrGeneration) || errors.Is(err, cpa.ErrSourceConflict) || errors.Is(err, cpa.ErrApplyUnknown) {
			return ExternalInstall{}, err
		}
		return ExternalInstall{}, ErrExternalUnavailable
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

// ExternalKeyList is the key list as the user's CPA answers it. Every staged key
// carries the presence read back from that CPA, so the page never reports an
// effect this Manager merely intended: a key the CPA does not carry stays
// "not_applied" until its install is proven.
//
// The Manager's own records are the list either way. A CPA that cannot be read is
// not a blocker — it is reported as unverified, so an outage never hides the keys
// the Manager staged. A credential the CPA refuses and a target it no longer names
// are blockers: no read can settle them, and only a rebind resolves them. The list
// of another profile is returned unchanged.
func (s *Service) ExternalKeyList(ctx context.Context, keys []freshkeys.Key) ([]freshkeys.Key, bool, error) {
	if s == nil || s.profile != ProfileExternal {
		return keys, false, nil
	}
	ids := make([]identity.APIKeyID, 0, len(keys))
	for _, key := range keys {
		ids = append(ids, key.ID)
	}
	present, err := s.ExternalKeyPresence(ctx, ids)
	if err != nil {
		if errors.Is(err, cpa.ErrManagementRejected) || errors.Is(err, cpa.ErrExternalTargetMoved) {
			return nil, true, err
		}
		return keys, true, nil
	}
	annotated := make([]freshkeys.Key, 0, len(keys))
	for _, key := range keys {
		// A revoked or finalized record keeps its own state: presence only
		// refines a key that is still meant to be live.
		if present[key.ID] && key.Lifecycle == identity.LifecycleActive {
			key.RuntimeState = "effective_observed"
		}
		annotated = append(annotated, key)
	}
	return annotated, false, nil
}

// ExternalKeyPresenceOf is ExternalKeyPresence for one key, for the routes that
// answer about exactly one record.
func (s *Service) ExternalKeyPresenceOf(ctx context.Context, id identity.APIKeyID) (bool, error) {
	present, err := s.ExternalKeyPresence(ctx, []identity.APIKeyID{id})
	if err != nil {
		return false, err
	}
	return present[id], nil
}

// externalSecretPresent reports whether the staged secret is one of the client
// credentials the user's CPA currently carries.
func (s *Service) externalSecretPresent(ctx context.Context, rawID string, snapshot cpa.ExternalKeySnapshot) (bool, error) {
	if _, err := identity.ParseAPIKeyID(rawID); err != nil {
		return false, freshkeys.ErrRequest
	}
	present := false
	err := s.keys.WithStagedSecret(ctx, rawID, func(secret string) error {
		present = slices.Contains(snapshot.Keys, secret)
		return nil
	})
	return present, err
}

// externalApply makes one staged key a client credential of the user's CPA and
// proves it by reading the list back. A key the CPA already carries is not
// written again, so a replay never duplicates a credential; the write itself is
// sent exactly once, and a refused management credential is the typed blocker,
// never a retry reason.
//
// The write replaces the CPA's whole list, so it never builds that list from a
// read the user has since edited: the document digest and the membership are read
// again immediately before the PUT and a change there is a source conflict with no
// write at all. A journaled operation owns the key set until it settles, exactly as
// the managed Apply is bound by its own journal.
func (s *Service) externalApply(ctx context.Context, id string, generation uint64) (State, error) {
	result := State{KeyID: id, RuntimeState: "effect_unknown"}
	if _, err := s.keys.Detail(ctx, id); err != nil {
		return result, err
	}
	if latest, err := s.repo.LatestOperation(ctx); err != nil {
		return result, err
	} else if latest != nil && latest.Outcome != "committed" {
		return result, freshkeys.ErrConflict
	}
	result.ManagerStaged = true
	install, err := s.externalTarget(ctx, generation)
	if err != nil {
		return result, err
	}
	w := s.externalKeyWire(install)
	if w == nil {
		return result, ErrExternalUnavailable
	}
	err = s.keys.WithStagedSecret(ctx, id, func(secret string) error {
		snapshot, err := w.ExternalKeySnapshot(ctx, install.ManagementKey)
		if err != nil {
			return err
		}
		if slices.Contains(snapshot.Keys, secret) {
			return nil
		}
		after := append(slices.Clone(snapshot.Keys), secret)
		// The list was built from this one read. The read that gates the write must
		// show the same document and the same membership, or the user changed their
		// CPA under it — a credential they revoked directly on the CPA is never
		// added back by a stale list.
		again, err := w.ExternalKeySnapshot(ctx, install.ManagementKey)
		if err != nil {
			return err
		}
		if again.SourceSHA256 != snapshot.SourceSHA256 || !sameKeys(again.Keys, snapshot.Keys) {
			return cpa.ErrSourceConflict
		}
		// The version is asked here, once, immediately before the single PUT: the
		// wire itself carries no gate, so this is the only probe this write costs
		// and a refusal leaves nothing attempted.
		if err = s.externalWriteGate(ctx, install); err != nil {
			return err
		}
		result.Attempted = true
		if err = w.ExternalWriteKeySet(ctx, install.ManagementKey, after); err != nil {
			return err
		}
		back, err := w.ExternalKeySnapshot(ctx, install.ManagementKey)
		if err != nil {
			return err
		}
		if slices.Contains(back.Keys, secret) {
			return nil
		}
		return cpa.ErrApplyUnknown
	})
	if err != nil {
		return result, err
	}
	return s.externalObserve(ctx, id, generation)
}

// externalObserve reads the user's CPA and reports the presence of this key. It
// claims no window this Manager cannot renew, so its evidence expires with the
// observation itself.
func (s *Service) externalObserve(ctx context.Context, id string, generation uint64) (State, error) {
	result := State{KeyID: id, RuntimeState: "effect_unknown"}
	if _, err := s.keys.Detail(ctx, id); err != nil {
		return result, err
	}
	result.ManagerStaged = true
	present, err := s.ExternalKeyPresenceOf(ctx, identity.APIKeyID(id))
	if err != nil {
		return result, err
	}
	if _, err = s.externalTarget(ctx, generation); err != nil {
		return result, err
	}
	result.Attempted = present
	result.InstallAvailable = !present
	result.RuntimeGeneration = strconv.FormatUint(generation, 10)
	if !present {
		return result, nil
	}
	now := time.Now()
	result.CPAConfigured = true
	result.RuntimeState = "effective_observed"
	result.ObservedFromMS = now.UnixMilli()
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
