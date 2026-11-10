// Package freshkeys implements Manager-only staging. No method writes CPA.
package freshkeys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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
	// authentication disabled, and "adopted" for a Key an adopted destination
	// already carried when the adoption completed. It is display metadata, not
	// authority.
	Origin string `json:"origin"`
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
func (s *Service) RegisterAdopted(ctx context.Context, secrets []string) ([]Key, error) {
	if s == nil || s.repo == nil || s.protector == nil || len(secrets) == 0 || len(secrets) > maxAdoptedSecrets {
		return nil, ErrRequest
	}
	// The whole list is validated before the first write: a refusal must leave
	// the workspace exactly as it found it.
	seen := make(map[string]bool, len(secrets))
	for _, secret := range secrets {
		if secret == "" || len(secret) > maxAdoptedSecretLength || !utf8.ValidString(secret) || seen[secret] {
			return nil, ErrRequest
		}
		seen[secret] = true
	}
	runtimeIdentity := s.runtimeIdentity(ctx)
	keys := make([]Key, 0, len(secrets))
	for _, secret := range secrets {
		hash := digest(secret)
		record, err := s.repo.ByHash(ctx, hash)
		if err != nil {
			return nil, err
		}
		if record != nil && record.Identity.Lifecycle != identity.LifecycleActive {
			// A superseded record is retained history, not a reusable identity: it
			// was retired by a committed key operation, so binding the installation
			// to it would name a key no write path may consume. The operator's source
			// CPA still carries a credential this Manager already revoked, which is a
			// conflict to resolve there, never a registration to replay.
			return nil, ErrAdoptedSecretRetired
		}
		if record == nil {
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
			v := repo.Record{
				RequestID:  string(requestID),
				Identity:   identity.APIKeyIdentity{ID: id, Revision: identity.InitialRevision, Lifecycle: identity.LifecycleActive, CreatedAtMS: now, UpdatedAtMS: now},
				Ciphertext: ciphertext, Hash: hash, Preview: model.MaskedImportedClientKey(secret), NameRevision: 1, Origin: repo.OriginAdopted,
			}
			created, err := s.repo.CreateAdopted(ctx, v, runtimeIdentity)
			if err != nil {
				return nil, err
			}
			if created {
				record = &v
			} else if record, err = s.repo.ByHash(ctx, hash); err != nil {
				return nil, err
			}
			if record == nil {
				return nil, errors.New("adopted client key registration lost its record")
			}
		}
		keys = append(keys, public(*record))
	}
	return keys, nil
}

// Bounds of one adoption's client key list. The destination proof already
// refuses a list outside them; they are repeated here so this write path never
// depends on the caller having proved anything.
const (
	maxAdoptedSecrets      = 128
	maxAdoptedSecretLength = 4096
)

// stagedSecretMatches validates one stored record against its decrypted secret.
// Two origins have a canonical shape and the preview that shape owns; an adopted
// key carries the source CPA's arbitrary shape and therefore the imported
// preview. The digest is the same for both, and it is what binds the row.
func stagedSecretMatches(record repo.Record, secret string) bool {
	if digest(secret) != record.Hash {
		return false
	}
	if record.Origin == repo.OriginAdopted {
		return record.Preview == model.MaskedImportedClientKey(secret)
	}
	return model.ValidCanonicalClientKey(secret) && record.Preview == model.MaskedClientKey(secret)
}

func public(v repo.Record) Key {
	return Key{ID: v.Identity.ID, RequestID: v.RequestID, MaskedPreview: v.Preview, CreatedAtMS: v.Identity.CreatedAtMS,
		ManagerState: "persisted", RuntimeState: "not_applied", Name: v.Name, NameRevision: v.NameRevision,
		Lifecycle: v.Identity.Lifecycle, Origin: v.Origin}
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

func (s *Service) Rename(ctx context.Context, rawID, name string, revision int64) (Key, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 120 || strings.ContainsFunc(name, unicode.IsControl) || revision < 1 {
		return Key{}, ErrRequest
	}
	key, err := s.Detail(ctx, rawID)
	if err != nil {
		return Key{}, err
	}
	if err := s.repo.Rename(ctx, key.ID, name, revision); err != nil {
		return Key{}, err
	}
	// Return the committed operation's metadata, not a later writer's readback.
	key.Name, key.NameRevision = name, revision+1
	return key, nil
}
func digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
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
		key := public(v)
		if v.Identity.Lifecycle == identity.LifecycleSuperseded {
			key.RuntimeState = "effect_unknown"
		}
		if attempt != nil && attempt.KeyID == string(key.ID) {
			key.RuntimeState = "effect_unknown"
		}
		if operation != nil && (operation.KeeperID == string(key.ID) || operation.TargetID == string(key.ID)) {
			key.RuntimeState = "effect_unknown"
		}
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
	v := repo.Record{RequestID: requestID, Identity: identity.APIKeyIdentity{ID: id, Revision: identity.InitialRevision, Lifecycle: identity.LifecycleActive, CreatedAtMS: now, UpdatedAtMS: now}, Ciphertext: ciphertext, Hash: digest(secret), Preview: model.MaskedClientKey(secret), NameRevision: 1, Origin: repo.OriginWorkspace}
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
		if record.Identity.Lifecycle == identity.LifecycleActive {
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
func (s *Service) withSecret(ctx context.Context, rawID string, active bool, consume func(string) error) error {
	key, err := s.Detail(ctx, rawID)
	if err != nil {
		return err
	}
	records, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.Identity.ID != key.ID {
			continue
		}
		secret, err := s.protector.UnprotectString(record.Ciphertext)
		if err != nil || digest(secret) != record.Hash || active && record.Identity.Lifecycle != identity.LifecycleActive {
			return errors.New("staged client secret unavailable")
		}
		return consume(secret)
	}
	return ErrNotFound
}
