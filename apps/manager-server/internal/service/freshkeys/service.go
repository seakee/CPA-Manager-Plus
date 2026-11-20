// Package freshkeys implements Manager-only staging. No method writes CPA.
package freshkeys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

var ErrRequest = errors.New("invalid creation request")
var ErrNotFound = errors.New("creation result not found")
var ErrConflict = repo.ErrConflict
var ErrRetired = repo.ErrRetired

// ErrAdoptedSecretRetired refuses to register a destination key whose secret is
// already a superseded record here. The key operation journal committed that
// retirement, so reusing the identity would bind the installation to a key it
// cannot decrypt-and-write, and reviving it would forge a lifecycle transition
// the journal owns.
var ErrAdoptedSecretRetired = errors.New("adopted client key was already retired")

type Key struct {
	ID            identity.APIKeyID  `json:"id"`
	RequestID     string             `json:"requestId"`
	MaskedPreview string             `json:"maskedPreview"`
	CreatedAtMS   int64              `json:"createdAtMs"`
	ManagerState  string             `json:"managerState"`
	RuntimeState  string             `json:"runtimeState"`
	Name          string             `json:"name"`
	NameRevision  int64              `json:"nameRevision"`
	Lifecycle     identity.Lifecycle `json:"lifecycle"`
	// Origin is "workspace" for a Key the workspace created, "installer" for the
	// single Key the default installation minted so CPA never starts with client
	// authentication disabled, and "adopted" for a Key an adopted or upgraded
	// destination already carried. It is display metadata, not authority.
	Origin          string `json:"origin"`
	OperationID     string `json:"operationId,omitempty"`
	EffectCode      string `json:"effectCode,omitempty"`
	LastRotatedAtMS int64  `json:"lastRotatedAtMs,omitempty"`
	RevokedAtMS     int64  `json:"revokedAtMs,omitempty"`
}

type StagedRotation struct {
	Key        Key
	OldSecret  string
	NewSecret  string
	NewPreview string
	Committed  bool
}

// InitialKey is the installation-minted Client Key. The secret is returned only
// to the install path, which places it in the protected destination snapshot;
// no HTTP response, journal or log ever carries it.
type InitialKey struct {
	ID        identity.APIKeyID
	RequestID string
	Secret    string
}

// Origins of a canonical Client Key, mirrored to the Key workspace so the
// installation's Key and the keys an adopted CPA already carried are displayed
// with a localised label instead of the stored default name.
const (
	OriginWorkspace = repo.OriginWorkspace
	OriginInstaller = repo.OriginInstaller
	OriginAdopted   = repo.OriginAdopted
)

// InstallerOriginName is the default display name of the installation's Key.
const InstallerOriginName = "Initial key"

type Result struct {
	Key           Key    `json:"key"`
	CreatedSecret string `json:"createdSecret,omitempty"`
}

// SourceRuntime resolves the runtime identity a new key's source binding
// belongs to. It returns "" while the installation has no committed target.
type SourceRuntime func(context.Context) (string, error)

// Option configures optional Fresh Key collaborators.
type Option func(*Service)

// WithSourceRuntime enables same-transaction source binding for created keys.
func WithSourceRuntime(source SourceRuntime) Option {
	return func(s *Service) { s.source = source }
}

// WithClock replaces the wall clock that stamps a new Key's creation instant.
// The default is the real clock, so nothing changes for production callers; a
// fixed clock lets a test reason about the exact creation instant, which the
// usage buckets are aligned to. A nil clock is ignored.
func WithClock(now func() int64) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

type Service struct {
	repo      *repo.Repository
	protector *security.Protector
	source    SourceRuntime
	// now stamps the creation instant of a new Key. It is a field, not a direct
	// time.Now call, because that instant becomes the key's attribution window
	// and a test cannot reason about it against a moving clock.
	now func() int64
	// installMu serializes this process's install-key resolution. The origin
	// unique index remains the durable cross-process fence; the lock only stops
	// concurrent callers from racing a redundant mint attempt.
	installMu sync.Mutex
}

func New(r *repo.Repository, p *security.Protector, options ...Option) *Service {
	s := &Service{repo: r, protector: p, now: func() int64 { return time.Now().UnixMilli() }}
	for _, option := range options {
		option(s)
	}
	return s
}

func ValidRequestID(id string) bool { _, err := identity.ParseAPIKeyID(id); return err == nil }

// runtimeIdentity reports the committed installation target. A read failure
// only defers attribution to the startup backfill; it never fails key creation.
func (s *Service) runtimeIdentity(ctx context.Context) string {
	if s.source == nil {
		return ""
	}
	value, err := s.source(ctx)
	if err != nil {
		return ""
	}
	return value
}

// RegisterAdopted persists one canonical record per Client Key an adopted
// destination already carried, in the order the destination presents them.
//
// Adoption is the one path where the secret was not minted here: it was read from
// the CPA this Manager had already staged and verified, so the destination's own
// credentials become Manager-managed keys the Key workspace can list, rename,
// revoke and replace like any other. Nothing here writes CPA, and the secret
// leaves only into the protected record — the page still reads the masked
// preview.
//
// It is idempotent by secret digest: a retried or resumed completion replays the
// existing records instead of registering the same credential twice, and a secret
// that is already a canonical record of this workspace keeps that single identity.
// A digest that resolves to a superseded record is refused instead: that identity
// is retired history, so the installation can neither own it nor revive it.
func (s *Service) PrepareAdopted(secrets []string) ([]repo.Record, error) {
	if s == nil || s.repo == nil || s.protector == nil || len(secrets) == 0 {
		return nil, ErrRequest
	}
	// The whole list is validated and encrypted before the first SQLite write:
	// a refusal or protector failure leaves the workspace untouched.
	seen := make(map[string]bool, len(secrets))
	for _, secret := range secrets {
		if secret == "" || len(secret) > maxAdoptedSecretLength || !utf8.ValidString(secret) || seen[secret] {
			return nil, ErrRequest
		}
		seen[secret] = true
	}
	candidates := make([]repo.Record, 0, len(secrets))
	for _, secret := range secrets {
		ciphertext, err := s.protector.ProtectString(secret)
		if err != nil {
			return nil, err
		}
		id, err := identity.NewAPIKeyID()
		if err != nil {
			return nil, err
		}
		requestID, err := identity.NewAPIKeyID()
		if err != nil {
			return nil, err
		}
		now := s.now()
		candidates = append(candidates, repo.Record{
			RequestID:  string(requestID),
			Identity:   identity.APIKeyIdentity{ID: id, Revision: identity.InitialRevision, Lifecycle: identity.LifecycleActive, CreatedAtMS: now, UpdatedAtMS: now},
			Ciphertext: ciphertext, Hash: digest(secret), Preview: model.MaskedImportedClientKey(secret), NameRevision: 1, Origin: repo.OriginAdopted,
		})
	}
	return candidates, nil
}

func (s *Service) RegisterAdopted(ctx context.Context, secrets []string) ([]Key, error) {
	candidates, err := s.PrepareAdopted(secrets)
	if err != nil {
		return nil, err
	}
	records, err := s.repo.StageAdopted(ctx, candidates, s.runtimeIdentity(ctx))
	if err != nil {
		if errors.Is(err, repo.ErrRetired) {
			return nil, ErrAdoptedSecretRetired
		}
		return nil, err
	}
	return publicKeys(records), nil
}

// RegisterAdoptedWithJournal commits every adopted Client Key and claims the
// one-shot initial install journal in one atomic SQLite transaction.
func (s *Service) RegisterAdoptedWithJournal(ctx context.Context, candidates []repo.Record, journal repo.InitialInstall) ([]Key, error) {
	return s.RegisterAdoptedWithJournalForRuntime(ctx, candidates, s.runtimeIdentity(ctx), journal)
}

// RegisterAdoptedWithJournalForRuntime commits every adopted Client Key and
// claims the one-shot initial install journal for an explicit runtime identity
// in one atomic SQLite transaction.
func (s *Service) RegisterAdoptedWithJournalForRuntime(ctx context.Context, candidates []repo.Record, runtimeIdentity string, journal repo.InitialInstall) ([]Key, error) {
	if s == nil || s.repo == nil {
		return nil, ErrRequest
	}
	records, err := s.repo.RegisterAdoption(ctx, candidates, runtimeIdentity, journal)
	if err != nil {
		if errors.Is(err, repo.ErrRetired) {
			return nil, ErrAdoptedSecretRetired
		}
		return nil, err
	}
	return publicKeys(records), nil
}

// RegisterAdoptedWithJournalTx commits every adopted Client Key and claims the
// one-shot initial install journal inside a caller-owned write transaction.
func (s *Service) RegisterAdoptedWithJournalTx(ctx context.Context, tx *sql.Tx, candidates []repo.Record, runtimeIdentity string, journal repo.InitialInstall) ([]Key, error) {
	if s == nil || s.repo == nil {
		return nil, ErrRequest
	}
	records, err := s.repo.RegisterAdoptionTx(ctx, tx, candidates, runtimeIdentity, journal)
	if err != nil {
		if errors.Is(err, repo.ErrRetired) {
			return nil, ErrAdoptedSecretRetired
		}
		return nil, err
	}
	return publicKeys(records), nil
}

func publicKeys(records []repo.Record) []Key {
	keys := make([]Key, 0, len(records))
	for _, record := range records {
		keys = append(keys, public(record))
	}
	return keys
}

// maxAdoptedSecretLength bounds one imported client credential to CPA v8's
// maximum token length without imposing any cap on total key count.
const maxAdoptedSecretLength = 4096

// stagedSecretMatches validates one stored record against its decrypted secret.
// Two origins have a canonical shape and the preview that shape owns; an adopted
// key carries the source CPA's arbitrary shape and therefore the imported
// preview. The digest is the same for both, and it is what binds the row.
func stagedSecretMatches(record repo.Record, secret string) bool {
	if secret == "" || len(secret) > maxAdoptedSecretLength || !utf8.ValidString(secret) || digest(secret) != record.Hash {
		return false
	}
	if model.ValidCanonicalClientKey(secret) && record.Preview == model.MaskedClientKey(secret) {
		return true
	}
	if record.Origin == repo.OriginAdopted {
		return record.Preview == model.MaskedImportedClientKey(secret)
	}
	if record.Origin == repo.OriginInstaller {
		if strings.IndexFunc(secret, unicode.IsSpace) >= 0 {
			return false
		}
		return record.Preview == model.MaskedImportedClientKey(secret)
	}
	return false
}

func public(v repo.Record) Key {
	revokedAtMS := v.RevokedAtMS
	if revokedAtMS == 0 && v.Identity.Lifecycle == identity.LifecycleSuperseded {
		revokedAtMS = v.Identity.UpdatedAtMS
	}
	runtimeState := "not_applied"
	if v.Identity.Lifecycle == identity.LifecycleSuperseded {
		runtimeState = "revoked_observed"
	}
	return Key{
		ID:              v.Identity.ID,
		RequestID:       v.RequestID,
		MaskedPreview:   v.Preview,
		CreatedAtMS:     v.Identity.CreatedAtMS,
		ManagerState:    "persisted",
		RuntimeState:    runtimeState,
		Name:            v.Name,
		NameRevision:    v.NameRevision,
		Lifecycle:       v.Identity.Lifecycle,
		Origin:          v.Origin,
		LastRotatedAtMS: v.LastRotatedAtMS,
		RevokedAtMS:     revokedAtMS,
	}
}

func projectStoredKey(v repo.Record, attempt *repo.InitialInstall, operation *repo.Operation) Key {
	key := public(v)
	if v.Identity.Lifecycle != identity.LifecycleSuperseded &&
		(attempt != nil && attempt.KeyID == string(key.ID) ||
			operation != nil && operation.Outcome != repo.OutcomeAborted && (operation.KeeperID == string(key.ID) || operation.TargetID == string(key.ID))) {
		key.RuntimeState = "effect_unknown"
	}
	if operation != nil && !operation.IsTerminal() && (operation.KeeperID == string(key.ID) || operation.TargetID == string(key.ID)) {
		key.OperationID = operation.ID
		if operation.Outcome == "pending" && nonRetiredLifecycle(key.Lifecycle) &&
			((operation.Mode == "add" && operation.TargetID == string(key.ID)) || (operation.Mode == "replace" && operation.KeeperID == string(key.ID))) {
			key.RuntimeState = "applying"
		} else {
			key.RuntimeState = "effect_unknown"
		}
	}
	if v.Identity.Lifecycle == identity.LifecycleSuperseded && key.OperationID == "" {
		key.RuntimeState = "revoked_observed"
	}
	return key
}

// IdentityIDs lists the Canonical Keys of this workspace without touching any
// stored secret. Usage reads need identity only, so they never decrypt one.
func (s *Service) IdentityIDs(ctx context.Context) ([]identity.APIKeyID, error) {
	records, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]identity.APIKeyID, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.Identity.ID)
	}
	return ids, nil
}

func (s *Service) Detail(ctx context.Context, rawID string) (Key, error) {
	id, err := identity.ParseAPIKeyID(rawID)
	if err != nil {
		return Key{}, ErrRequest
	}
	keys, err := s.List(ctx)
	if err != nil {
		return Key{}, err
	}
	for _, key := range keys {
		if key.ID == id {
			return key, nil
		}
	}
	return Key{}, ErrNotFound
}

// ValidKeyName trims and validates a Client Key display name.
func ValidKeyName(raw string) (string, bool) {
	if !utf8.ValidString(raw) {
		return "", false
	}
	name := strings.TrimSpace(raw)
	if utf8.RuneCountInString(name) > 120 || strings.ContainsFunc(name, unicode.IsControl) {
		return "", false
	}
	return name, true
}

func (s *Service) Rename(ctx context.Context, rawID, name string, revision int64) (Key, error) {
	cleaned, ok := ValidKeyName(name)
	if !ok || revision < 1 {
		return Key{}, ErrRequest
	}
	key, err := s.Detail(ctx, rawID)
	if err != nil {
		return Key{}, err
	}
	if err := s.repo.Rename(ctx, key.ID, cleaned, revision); err != nil {
		return Key{}, err
	}
	// Return the committed operation's metadata, not a later writer's readback.
	key.Name, key.NameRevision = cleaned, revision+1
	return key, nil
}
func digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// SecretDigest is the stored form of one Client Key secret: the sha256 hex that
// a record is fenced by. It is what a caller may compare a destination's key
// list against without carrying the secret anywhere it is not already read.
func SecretDigest(secret string) string { return digest(secret) }

// nonRetiredLifecycle reports whether a canonical identity remains non-retired.
// Passive runtime reconciliation marks a previously observed key LifecycleMissing
// when absent from CPA; only LifecycleSuperseded is terminal retirement.
func nonRetiredLifecycle(lifecycle identity.Lifecycle) bool {
	return lifecycle == identity.LifecycleActive || lifecycle == identity.LifecycleMissing
}

// AllStaged reports whether every secret is one of this installation's
// non-retired staged Client Keys. It answers membership without returning any
// secret, which is what a whole-list write needs: a destination key this
// Manager cannot name is one it must not overwrite.
func (s *Service) AllStaged(ctx context.Context, secrets []string) (bool, error) {
	if s == nil || s.repo == nil || len(secrets) == 0 {
		return false, ErrRequest
	}
	var rotationHashes map[string]bool
	latest, err := s.repo.LatestOperation(ctx)
	if err != nil {
		return false, err
	}
	if latest != nil && !latest.IsTerminal() && latest.Mode == "rotate" {
		rot, rotErr := s.repo.RotationByRequestID(ctx, latest.ID)
		if rotErr != nil {
			return false, rotErr
		}
		if rot != nil && string(rot.APIKeyID) == latest.TargetID {
			rotationHashes = map[string]bool{rot.NewHash: true}
		}
	}
	if rotationHashes == nil {
		rotationHashes, err = s.repo.ActiveStagedRotationHashes(ctx)
		if err != nil {
			return false, err
		}
	}
	for _, secret := range secrets {
		if secret == "" {
			return false, nil
		}
		h := digest(secret)
		if rotationHashes[h] {
			continue
		}
		record, err := s.repo.ByHash(ctx, h)
		if err != nil {
			return false, err
		}
		if record == nil || !nonRetiredLifecycle(record.Identity.Lifecycle) {
			return false, nil
		}
	}
	return true, nil
}

func (s *Service) List(ctx context.Context) ([]Key, error) {
	if s.protector == nil {
		return nil, errors.New("secret protection unavailable")
	}
	records, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	keys := make([]Key, 0, len(records))
	attempt, err := s.repo.InitialInstall(ctx)
	if err != nil {
		return nil, err
	}
	operation, err := s.repo.LatestOperation(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range records {
		secret, err := s.protector.UnprotectString(v.Ciphertext)
		if err != nil || !stagedSecretMatches(v, secret) {
			return nil, errors.New("staged client secret unavailable")
		}
		key := projectStoredKey(v, attempt, operation)
		keys = append(keys, key)
	}
	return keys, nil
}
func (s *Service) Lookup(ctx context.Context, requestID string) (Key, error) {
	if !ValidRequestID(requestID) {
		return Key{}, ErrRequest
	}
	keys, err := s.List(ctx)
	if err != nil {
		return Key{}, err
	}
	for _, key := range keys {
		if key.RequestID == requestID {
			return key, nil
		}
	}
	return Key{}, ErrNotFound
}
func (s *Service) Create(ctx context.Context, requestID string) (Result, bool, error) {
	return s.CreateNamed(ctx, requestID, "")
}

func (s *Service) CreateNamed(ctx context.Context, requestID, name string) (Result, bool, error) {
	cleanedName, ok := ValidKeyName(name)
	if !ok || !ValidRequestID(requestID) {
		return Result{}, false, ErrRequest
	}
	key, err := s.Lookup(ctx, requestID)
	if err == nil {
		return Result{Key: key}, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Result{}, false, err
	}
	id, err := identity.NewAPIKeyID()
	if err != nil {
		return Result{}, false, err
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return Result{}, false, err
	}
	secret := model.WorkspaceClientKeyPrefix + hex.EncodeToString(raw)
	ciphertext, err := s.protector.ProtectString(secret)
	if err != nil {
		return Result{}, false, err
	}
	now := s.now()
	v := repo.Record{
		RequestID: requestID,
		Identity: identity.APIKeyIdentity{
			ID:          id,
			Revision:    identity.InitialRevision,
			Lifecycle:   identity.LifecycleActive,
			CreatedAtMS: now,
			UpdatedAtMS: now,
		},
		Ciphertext:   ciphertext,
		Hash:         digest(secret),
		Preview:      model.MaskedClientKey(secret),
		Name:         cleanedName,
		NameRevision: 1,
		Origin:       repo.OriginWorkspace,
	}
	created, err := s.repo.Create(ctx, v, s.runtimeIdentity(ctx))
	if err != nil {
		return Result{}, false, err
	}
	if !created {
		key, err = s.Lookup(ctx, requestID)
		return Result{Key: key}, false, err
	}
	return Result{Key: public(v), CreatedSecret: secret}, true, nil
}

// StageRotate stages or replays an Option B in-place secret rotation candidate
// for targetID without modifying the active key until SaveOperation commits.
func (s *Service) StageRotate(ctx context.Context, requestID, rawTargetID string) (StagedRotation, bool, error) {
	if !ValidRequestID(requestID) {
		return StagedRotation{}, false, ErrRequest
	}
	targetID, err := identity.ParseAPIKeyID(rawTargetID)
	if err != nil {
		return StagedRotation{}, false, ErrRequest
	}
	if s == nil || s.repo == nil || s.protector == nil {
		return StagedRotation{}, false, errors.New("secret protection unavailable")
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return StagedRotation{}, false, err
	}
	generatedSecret := model.WorkspaceClientKeyPrefix + hex.EncodeToString(raw)
	generatedCiphertext, err := s.protector.ProtectString(generatedSecret)
	if err != nil {
		return StagedRotation{}, false, err
	}
	now := s.now()
	candidate := repo.RotationRecord{
		RequestID:     requestID,
		APIKeyID:      targetID,
		NewCiphertext: generatedCiphertext,
		NewHash:       digest(generatedSecret),
		NewPreview:    model.MaskedClientKey(generatedSecret),
		CreatedAtMS:   now,
	}
	storedRot, targetRec, created, committed, err := s.repo.StageRotation(ctx, candidate)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return StagedRotation{}, false, ErrNotFound
		}
		return StagedRotation{}, false, err
	}
	currentSecret, err := s.protector.UnprotectString(targetRec.Ciphertext)
	if err != nil || !stagedSecretMatches(targetRec, currentSecret) {
		return StagedRotation{}, false, errors.New("staged client secret unavailable")
	}
	key, err := s.Detail(ctx, string(targetID))
	if err != nil {
		key = public(targetRec)
	}
	if committed {
		return StagedRotation{
			Key:       key,
			Committed: true,
		}, false, nil
	}
	newSecret := generatedSecret
	if !created {
		newSecret, err = s.protector.UnprotectString(storedRot.NewCiphertext)
		if err != nil || !model.ValidCanonicalClientKey(newSecret) || digest(newSecret) != storedRot.NewHash || storedRot.NewPreview != model.MaskedClientKey(newSecret) {
			return StagedRotation{}, false, errors.New("staged rotation secret unavailable")
		}
	}
	return StagedRotation{
		Key:        key,
		OldSecret:  currentSecret,
		NewSecret:  newSecret,
		NewPreview: storedRot.NewPreview,
	}, created, nil
}

// DiscardUnstartedRotate deletes a staged rotation candidate only when no
// operation journal row was ever written for requestID.
func (s *Service) DiscardUnstartedRotate(ctx context.Context, requestID, rawTargetID string) error {
	if s == nil || s.repo == nil || !ValidRequestID(requestID) {
		return nil
	}
	targetID, err := identity.ParseAPIKeyID(rawTargetID)
	if err != nil {
		return nil
	}
	return s.repo.DiscardUnstartedRotation(ctx, requestID, targetID)
}

// DiscardOrphanedRotate deletes any staged rotation row for rawTargetID (other
// than keepRequestID) whose request_id never opened an operation journal row.
// When rawTargetID is empty, all orphaned rotation rows across keys are swept.
func (s *Service) DiscardOrphanedRotate(ctx context.Context, keepRequestID, rawTargetID string) error {
	if s == nil || s.repo == nil {
		return nil
	}
	if rawTargetID == "" {
		return s.repo.DiscardOrphanedRotation(ctx, keepRequestID, "")
	}
	targetID, err := identity.ParseAPIKeyID(rawTargetID)
	if err != nil {
		return nil
	}
	return s.repo.DiscardOrphanedRotation(ctx, keepRequestID, targetID)
}

// HasStagedRotate reports whether rawTargetID currently has an active staged
// rotation candidate in fresh_client_api_key_rotations.
func (s *Service) HasStagedRotate(ctx context.Context, rawTargetID string) (bool, error) {
	if s == nil || s.repo == nil {
		return false, nil
	}
	targetID, err := identity.ParseAPIKeyID(rawTargetID)
	if err != nil {
		return false, ErrRequest
	}
	return s.repo.HasStagedRotation(ctx, targetID)
}

// WithRotationSecrets supplies (oldSecret, nextSecret) for one Option B rotation
// operation. Before commit, oldSecret is the current active secret and nextSecret
// comes from fresh_client_api_key_rotations; after commit, nextSecret is the
// current active secret and oldSecret comes from fresh_client_api_key_secret_history.
func (s *Service) WithRotationSecrets(ctx context.Context, opID, rawTargetID string, committed bool, consume func(oldSecret, nextSecret string) error) error {
	if !ValidRequestID(opID) {
		return ErrRequest
	}
	targetID, err := identity.ParseAPIKeyID(rawTargetID)
	if err != nil {
		return ErrRequest
	}
	if s == nil || s.repo == nil || s.protector == nil {
		return errors.New("staged client secret unavailable")
	}
	record, err := s.repo.ByID(ctx, targetID)
	if err != nil {
		return err
	}
	if record == nil {
		return ErrNotFound
	}
	activeSecret, err := s.protector.UnprotectString(record.Ciphertext)
	if err != nil || !stagedSecretMatches(*record, activeSecret) {
		return errors.New("staged client secret unavailable")
	}
	rot, err := s.repo.RotationByRequestID(ctx, opID)
	if err != nil {
		return err
	}
	if rot != nil {
		if rot.APIKeyID != targetID || !nonRetiredLifecycle(record.Identity.Lifecycle) || rot.OldHash != record.Hash {
			return errors.New("staged rotation secret unavailable")
		}
		nextSecret, decErr := s.protector.UnprotectString(rot.NewCiphertext)
		if decErr != nil || !model.ValidCanonicalClientKey(nextSecret) || digest(nextSecret) != rot.NewHash || rot.NewPreview != model.MaskedClientKey(nextSecret) || activeSecret == nextSecret {
			return errors.New("staged rotation secret unavailable")
		}
		return consume(activeSecret, nextSecret)
	}
	if !committed {
		op, opErr := s.repo.Operation(ctx, opID)
		if opErr != nil {
			return opErr
		}
		if op == nil || op.Mode != "rotate" || op.TargetID != string(targetID) || op.Outcome != repo.OutcomeCommitted {
			return errors.New("staged rotation secret unavailable")
		}
	}
	hist, err := s.repo.SecretHistoryByRequestID(ctx, opID)
	if err != nil {
		return err
	}
	if hist == nil {
		hist, err = s.repo.LatestSecretHistoryByID(ctx, targetID)
		if err != nil {
			return err
		}
	}
	if hist == nil || hist.APIKeyID != targetID {
		return errors.New("retired rotation secret unavailable")
	}
	histRecord := repo.Record{
		Hash:    hist.Hash,
		Preview: hist.Preview,
		Origin:  hist.Origin,
	}
	oldSecret, err := s.protector.UnprotectString(hist.Ciphertext)
	if err != nil || !stagedSecretMatches(histRecord, oldSecret) {
		return errors.New("retired rotation secret unavailable")
	}
	nextSecret := activeSecret
	if hist.NextHash != "" && hist.NextHash != record.Hash {
		nextHist, nextErr := s.repo.SecretHistoryByHash(ctx, hist.NextHash)
		if nextErr != nil {
			return nextErr
		}
		if nextHist == nil || nextHist.APIKeyID != targetID {
			return errors.New("retired rotation secret unavailable")
		}
		nextHistRecord := repo.Record{
			Hash:            nextHist.Hash,
			Preview:         nextHist.Preview,
			Origin:          nextHist.Origin,
			LastRotatedAtMS: nextHist.RetiredAtMS,
		}
		nextSecret, err = s.protector.UnprotectString(nextHist.Ciphertext)
		if err != nil || !stagedSecretMatches(nextHistRecord, nextSecret) {
			return errors.New("retired rotation secret unavailable")
		}
	}
	if oldSecret == nextSecret {
		return errors.New("retired rotation secret unavailable")
	}
	return consume(oldSecret, nextSecret)
}

// CommittedRotateSupersededState reports whether a committed rotate operation
// opID on rawTargetID has already been superseded by a subsequent committed
// rotation ("effective_observed") or a subsequent revocation ("revoked_observed").
func (s *Service) CommittedRotateSupersededState(ctx context.Context, opID, rawTargetID string) (string, bool, error) {
	if s == nil || s.repo == nil {
		return "", false, nil
	}
	targetID, err := identity.ParseAPIKeyID(rawTargetID)
	if err != nil {
		return "", false, ErrRequest
	}
	record, err := s.repo.ByID(ctx, targetID)
	if err != nil || record == nil {
		return "", false, err
	}
	if record.Identity.Lifecycle == identity.LifecycleSuperseded {
		return "revoked_observed", true, nil
	}
	hist, err := s.repo.SecretHistoryByRequestID(ctx, opID)
	if err != nil || hist == nil || hist.APIKeyID != targetID {
		return "", false, err
	}
	if hist.NextHash != "" && hist.NextHash != record.Hash {
		nextHist, nextErr := s.repo.SecretHistoryByHash(ctx, hist.NextHash)
		if nextErr != nil {
			return "", false, nextErr
		}
		if nextHist != nil && nextHist.APIKeyID == targetID {
			return "effective_observed", true, nil
		}
	}
	return "", false, nil
}

// EnsureInitialKey returns the single Client Key the installation minted, or
// mints and persists it now. The origin-unique canonical record is the only
// durable copy, so a resume, retry or process restart replays that exact key and
// never generates a second one. The plaintext reaches only the install path,
// which stores it in the protected destination snapshot.
func (s *Service) EnsureInitialKey(ctx context.Context) (InitialKey, error) {
	if s == nil || s.repo == nil || s.protector == nil {
		return InitialKey{}, errors.New("install client key authority unavailable")
	}
	s.installMu.Lock()
	defer s.installMu.Unlock()
	record, err := s.repo.Installer(ctx)
	if err != nil {
		return InitialKey{}, err
	}
	if record != nil {
		return s.installerKey(record)
	}
	requestID, err := identity.NewAPIKeyID()
	if err != nil {
		return InitialKey{}, err
	}
	id, err := identity.NewAPIKeyID()
	if err != nil {
		return InitialKey{}, err
	}
	secret, err := GenerateInitialClientKey()
	if err != nil {
		return InitialKey{}, err
	}
	ciphertext, err := s.protector.ProtectString(secret)
	if err != nil {
		return InitialKey{}, err
	}
	now := s.now()
	record = &repo.Record{
		RequestID:  string(requestID),
		Identity:   identity.APIKeyIdentity{ID: id, Revision: identity.InitialRevision, Lifecycle: identity.LifecycleActive, CreatedAtMS: now, UpdatedAtMS: now},
		Ciphertext: ciphertext, Hash: digest(secret), Preview: model.MaskedClientKey(secret),
		Name: InstallerOriginName, NameRevision: 1, Origin: repo.OriginInstaller,
	}
	created, err := s.repo.CreateInstaller(ctx, *record)
	if err != nil || !created {
		// Another attempt may have won the origin fence. That durable record is
		// authoritative; this call's freshly generated key is discarded.
		stored, readErr := s.repo.Installer(ctx)
		if readErr != nil {
			return InitialKey{}, readErr
		}
		if stored == nil {
			return InitialKey{}, err
		}
		return s.installerKey(stored)
	}
	return InitialKey{ID: id, RequestID: record.RequestID, Secret: secret}, nil
}

// installerKey decrypts and re-verifies the installation's stored Client Key.
func (s *Service) installerKey(record *repo.Record) (InitialKey, error) {
	secret, err := s.protector.UnprotectString(record.Ciphertext)
	if err != nil || !stagedSecretMatches(*record, secret) {
		return InitialKey{}, errors.New("install client key unavailable")
	}
	return InitialKey{ID: record.Identity.ID, RequestID: record.RequestID, Secret: secret}, nil
}

// GenerateInitialClientKey returns "sk-" + 64 uniformly random [A-Za-z0-9]
// characters, the shape v1.x installations were seeded with. 62 does not divide
// 256, so each byte is reduced to six bits and rejected unless it names one of
// the 62 characters: taking the byte modulo 62 would bias the first eight
// characters towards the low half of the alphabet.
func GenerateInitialClientKey() (string, error) {
	secret := make([]byte, 0, model.InitialClientKeyLength)
	secret = append(secret, model.InitialClientKeyPrefix...)
	buffer := make([]byte, 64)
	for len(secret) < model.InitialClientKeyLength {
		if _, err := rand.Read(buffer); err != nil {
			return "", err
		}
		secret = appendInitialClientKeyAlnum(secret, buffer)
	}
	return string(secret), nil
}

const initialClientKeyAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// initialClientKeyChar maps one random byte to an alphabet character. The two of
// every sixty-four values that name no character are rejected instead of folded,
// so every character is drawn with the same probability.
func initialClientKeyChar(value byte) (byte, bool) {
	if accepted := int(value) & 63; accepted < len(initialClientKeyAlphabet) {
		return initialClientKeyAlphabet[accepted], true
	}
	return 0, false
}

// appendInitialClientKeyAlnum appends accepted characters from buffer to secret
// until the key is complete. Bytes left over when the key fills are discarded.
func appendInitialClientKeyAlnum(secret, buffer []byte) []byte {
	for _, value := range buffer {
		if len(secret) >= model.InitialClientKeyLength {
			return secret
		}
		if character, ok := initialClientKeyChar(value); ok {
			secret = append(secret, character)
		}
	}
	return secret
}

// MatchStagedSecret verifies a Manager-staged credential against the existing
// canonical records. This result is NOT Runtime-effective Client authentication:
// callers must never use it alone to forward a model request to CPA.
func (s *Service) MatchStagedSecret(ctx context.Context, candidate string) (bool, error) {
	if s == nil || s.repo == nil || s.protector == nil {
		return false, errors.New("staged client key authority unavailable")
	}
	records, err := s.repo.List(ctx)
	if err != nil {
		return false, err
	}
	// The candidate shape matches secrets minted by Create. No caller input
	// is interpolated into SQL or emitted in a response/log.
	shape := model.ValidWorkspaceClientKey(candidate)
	sum := sha256.Sum256([]byte(candidate))
	matched := 0
	for _, record := range records {
		secret, err := s.protector.UnprotectString(record.Ciphertext)
		if err != nil || !stagedSecretMatches(record, secret) {
			return false, errors.New("staged client key authority corrupt")
		}
		stored, err := hex.DecodeString(record.Hash)
		if err != nil || len(stored) != len(sum) {
			return false, errors.New("staged client key digest invalid")
		}
		if nonRetiredLifecycle(record.Identity.Lifecycle) {
			matched |= subtle.ConstantTimeCompare(sum[:], stored)
		}
	}
	return shape && matched == 1, nil
}

// WithStagedSecret is an internal server-side consumer of the canonical encrypted
// record. No HTTP response may contain the secret passed to consume.
func (s *Service) WithStagedSecret(ctx context.Context, rawID string, consume func(string) error) error {
	return s.withSecret(ctx, rawID, true, consume)
}

// WithRetainedSecret is exclusively for read-only revocation reconciliation.
// Superseded identity history stays readable; it cannot grant authentication.
func (s *Service) WithRetainedSecret(ctx context.Context, rawID string, consume func(string) error) error {
	return s.withSecret(ctx, rawID, false, consume)
}
func (s *Service) withSecret(ctx context.Context, rawID string, nonRetired bool, consume func(string) error) error {
	id, err := identity.ParseAPIKeyID(rawID)
	if err != nil {
		return ErrRequest
	}
	if s == nil || s.repo == nil || s.protector == nil {
		return errors.New("staged client secret unavailable")
	}
	record, err := s.repo.ByID(ctx, id)
	if err != nil {
		return err
	}
	if record == nil {
		return ErrNotFound
	}
	secret, err := s.protector.UnprotectString(record.Ciphertext)
	if err != nil || !stagedSecretMatches(*record, secret) || nonRetired && !nonRetiredLifecycle(record.Identity.Lifecycle) {
		return errors.New("staged client secret unavailable")
	}
	return consume(secret)
}

// ActiveSecretMap decrypts all non-retired staged keys once and maps
// secret -> Key, avoiding repeated table scans and decryptions during multi-key
// observations and operations. Any corrupt row fails closed.
func (s *Service) ActiveSecretMap(ctx context.Context) (keys []Key, bySecret map[string]Key, secretByID map[string]string, err error) {
	if s == nil || s.repo == nil || s.protector == nil {
		return nil, nil, nil, errors.New("secret protection unavailable")
	}
	records, err := s.repo.List(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	attempt, err := s.repo.InitialInstall(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	operation, err := s.repo.LatestOperation(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	keys = make([]Key, 0, len(records))
	bySecret = make(map[string]Key, len(records))
	secretByID = make(map[string]string, len(records))
	for _, v := range records {
		secret, decErr := s.protector.UnprotectString(v.Ciphertext)
		if decErr != nil || !stagedSecretMatches(v, secret) {
			return nil, nil, nil, errors.New("staged client secret unavailable")
		}
		key := projectStoredKey(v, attempt, operation)
		keys = append(keys, key)
		if nonRetiredLifecycle(v.Identity.Lifecycle) {
			bySecret[secret] = key
			secretByID[string(key.ID)] = secret
		}
	}
	return keys, bySecret, secretByID, nil
}
