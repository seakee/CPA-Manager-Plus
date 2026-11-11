package freshkeyapply

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

// Profiles of one installation authority. A Manager holds exactly one of them,
// selected once when the authority is constructed: the profile is never read
// from a request or from the environment.
const (
	// ProfilePrivate marks the Host-attested S1/S2 loopback profile. Its
	// authority is the Host-issued permit file.
	ProfilePrivate = "private"
	// ProfileManaged marks the authority derived from a committed Fresh
	// NewManaged installation instead of a Host-issued S1 permit file.
	ProfileManaged = "managed"
)

// managedGrantTTL bounds one derived step grant. It is never longer than the
// remaining install window, and it is re-derived for every read.
const managedGrantTTL = 60 * time.Second

// ManagedInstall is the durable authority of a default Fresh New Managed
// installation. It is read from the committed checkpoint and the protected
// Manager connection; no secret is cached by the service.
type ManagedInstall struct {
	RunID         string
	Generation    string
	Identity      string
	ArtifactID    string
	BaseURL       string
	ManagementKey string
	// Strategy is the committed Fresh strategy that staged the destination: New
	// Managed or Adopt. It selects the destination posture, so it is a fact of the
	// installation and never a caller input.
	Strategy string
	// ProductComplete reports whether the durable Fresh operation has committed
	// product completion. An adoption holds no install-derived write or admission
	// authority until completion itself has committed.
	ProductComplete bool
}

// ManagedSource reloads and validates that authority on every call. It must
// fail closed with ErrUnavailable when the checkpoint, connection or secret is
// unavailable, unreadable, or bound to a different destination.
type ManagedSource func(context.Context) (ManagedInstall, error)

// NewManaged constructs the install-derived authority used by the default
// Compose installation. Runtime identity, generation and artifact are server
// observations of the same installation; the private loopback profile keeps
// its Host permit and is constructed with New instead.
func NewManaged(r *repo.Repository, keys *freshkeys.Service, runtime runtimeservice.RuntimeClient, endpoint string, source ManagedSource) *Service {
	s := New(r, keys, runtime)
	s.profile = ProfileManaged
	s.endpoint = endpoint
	s.source = source
	managedPermit := func(requireComplete bool) (Permit, error) {
		if source == nil {
			return Permit{}, ErrUnavailable
		}
		now := time.Now()
		install, err := source(context.Background())
		if err != nil {
			return Permit{}, err
		}
		if install.RunID == "" || install.Generation == "" || install.Identity == "" || install.ArtifactID == "" || len(install.ManagementKey) != 64 {
			return Permit{}, ErrUnavailable
		}
		if _, owned := cpa.PostureFor(install.Strategy); !owned {
			return Permit{}, ErrUnavailable
		}
		if requireComplete && install.Strategy == string(bootstrap.AdoptExisting) && !install.ProductComplete {
			return Permit{}, ErrUnavailable
		}
		// The credential and the transport must address the same CPA; a moved
		// destination is a different installation, never a silent rebind.
		if cpa.NormalizeBaseURL(install.BaseURL) != cpa.NormalizeBaseURL(s.endpoint) {
			return Permit{}, ErrUnavailable
		}
		return Permit{
			RunID: install.RunID, Generation: install.Generation, ManagementKey: install.ManagementKey,
			IssuedAtMS: now.Add(-time.Second).UnixMilli(), ExpiresAtMS: now.Add(managedGrantTTL).UnixMilli(),
			Profile: ProfileManaged, Identity: install.Identity, ArtifactID: install.ArtifactID,
			Strategy: install.Strategy,
		}, nil
	}
	s.permit = func() (Permit, error) { return managedPermit(true) }
	s.registrationPermit = func() (Permit, error) { return managedPermit(false) }
	s.keyOperations = true
	transport := cpa.NewTransportForEndpoint(endpoint)
	// The destination posture follows the committed strategy, never the transport's
	// default: an adopted destination is proven against the adopted rules, and a
	// selector that cannot answer refuses instead of proving the loose posture.
	transport.SetPostureSelector(func() (cpa.DestinationPosture, bool) {
		if source == nil {
			return cpa.DestinationPosture{}, false
		}
		install, err := source(context.Background())
		if err != nil {
			return cpa.DestinationPosture{}, false
		}
		return cpa.PostureFor(install.Strategy)
	})
	s.wire = transport
	s.keyPermit = s.managedKeyGrant
	return s
}

// SetTrustedSupervisorRoots binds a Manager-owned key authority to the
// installation's canonical Runtime Supervisor root(s).
func (s *Service) SetTrustedSupervisorRoots(roots []string) {
	if s == nil {
		return
	}
	if transport, ok := s.wire.(*cpa.Transport); ok {
		transport.SetTrustedSupervisorRoots(roots)
	}
}

// ManagedEndpoint is the normalized destination CPA address this authority
// writes through. It is configuration, not a credential.
func (s *Service) ManagedEndpoint() string {
	s = s.active()
	if s == nil || s.profile != ProfileManaged {
		return ""
	}
	return s.endpoint
}

// Surface names the one canonical caller asking its installation question.
// Every caller asks Targets about the same durable checkpoint, so no surface can
// accept a state another one refuses; the name exists because the surfaces do
// not all ship the same scope. The key surfaces have always acted only on a
// committed managed-strategy checkpoint, while the gateway configuration page of
// the private H1/direct scope predates that strategy and selects the attested
// target alone. Naming the surface keeps each rule here, in the one
// discriminator, instead of re-adding profile checks at every caller.
type Surface string

const (
	// SurfaceKeyInstall is the initial Client Key install route.
	SurfaceKeyInstall Surface = "key_install"
	// SurfaceKeyOperation is the durable revoke/replace journal route.
	SurfaceKeyOperation Surface = "key_operation"
	// SurfaceChatAdmission is the private chat proof consumer.
	SurfaceChatAdmission Surface = "chat_admission"
	// SurfaceGatewayConfig is the gateway configuration page and its private
	// field journal.
	SurfaceGatewayConfig Surface = "gateway_config"
)

// Targets is the single installation-profile discriminator of the canonical
// HTTP surface: key install, key operation, chat admission and the gateway
// configuration page all ask this authority. The profile is read from durable
// checkpoint facts, never from an environment variable or a request. An unknown
// surface is answered by the strictest rule, never by the loosest.
func (s *Service) Targets(op bootstrap.FreshOperation, surface Surface) bool {
	s = s.activeFor(op)
	if s == nil || op.Strategy.Stage != "committed" {
		return false
	}
	switch s.profile {
	case ProfileManaged:
		// One install-derived authority: the committed Manager-owned destination
		// is the credential's destination on every surface, so keys, admission and
		// configuration share exactly one answer. New Managed and Adopt both
		// qualify; an adopted destination is bound by the `external:` digest the
		// adoption recorded, so the prefix is read from the strategy and never
		// guessed from the value.
		if op.Strategy.Strategy == bootstrap.AdoptExisting && !op.ProductComplete {
			return false
		}
		return ValidManagerOwnedDestinationCheckpoint(op.Strategy)
	case ProfilePrivate:
		// The private H1/direct profile names one attested runtime and artifact;
		// the Host permit remains the write authority for whatever it names.
		if op.Strategy.TargetIdentity != cpa.PrivateKeyRuntime || op.Strategy.TargetArtifactID != cpa.PrivateKeyArtifact {
			return false
		}
		if surface == SurfaceGatewayConfig {
			// Its page and field journal shipped before the managed strategy and
			// never required one: the attested target alone is their scope.
			return true
		}
		// Key install, key operations and chat admission require the managed
		// strategy as well; an adopted checkpoint is never a key write path here.
		return op.Strategy.Strategy == bootstrap.NewManaged
	case ProfileExternal:
		// The user's own CPA: keys and its configuration page are managed through
		// the committed connection. This Manager owns no Runtime there, so chat
		// admission and gateway admission are never answered by it. The surfaces
		// are an allowlist rather than one exclusion, so a surface added later is
		// refused here until it is deliberately admitted.
		if op.Strategy.Strategy != bootstrap.ConnectExternal ||
			!validExternalIdentity(op.Strategy.TargetIdentity) {
			return false
		}
		switch surface {
		case SurfaceKeyInstall, SurfaceKeyOperation, SurfaceGatewayConfig:
			return true
		}
		return false
	}
	return false
}

// TargetsFunc binds Targets to one canonical surface for a consumer that takes a
// plain predicate, such as the gateway configuration page. The rule stays in
// Targets: this only names which surface is asking.
func (s *Service) TargetsFunc(surface Surface) func(bootstrap.FreshOperation) bool {
	return func(op bootstrap.FreshOperation) bool { return s.Targets(op, surface) }
}

// managedKeyGrant derives the per-step authority for one durable key operation.
// The Host permit is replaced by durable facts only: the committed installation
// connection (management credential, run and generation) and the destination
// source digest observed immediately before the step.
func (s *Service) managedKeyGrant(ctx context.Context, operationID, step string) (KeyPermit, error) {
	if operationID == "" || (step != "add" && step != "revoke") {
		return KeyPermit{}, ErrUnavailable
	}
	p, err := s.permit()
	if err != nil {
		return KeyPermit{}, err
	}
	w, ok := s.wire.(keyWire)
	if !ok {
		return KeyPermit{}, ErrUnavailable
	}
	snap, err := w.KeySnapshot(ctx, p.ManagementKey)
	if err != nil || len(snap.SourceSHA256) != 64 {
		return KeyPermit{}, ErrUnavailable
	}
	now := time.Now()
	expires := now.Add(managedGrantTTL).UnixMilli()
	if expires > p.ExpiresAtMS {
		expires = p.ExpiresAtMS
	}
	if expires <= now.UnixMilli() {
		return KeyPermit{}, ErrUnavailable
	}
	return KeyPermit{
		Version: 1, SourceID: p.RunID, Revision: 1, OperationID: operationID, Step: step,
		RunID: p.RunID, Generation: p.Generation, SourceSHA256: snap.SourceSHA256,
		IssuedAtMS: now.UnixMilli(), ExpiresAtMS: expires,
	}, nil
}

// AdoptedDestinationKeys is completion's authenticated read of an adopted
// destination: it proves the Adopt posture with the install-derived credential
// and returns the client keys the destination actually carries. It never writes,
// and it refuses a destination whose posture is a New Managed template, because
// that is a different installation.
//
// Completion calls it before it decides anything, so "the CPA this Manager
// installed is still the CPA it holds" is a fact it observed rather than one it
// asserted from the checkpoint.
func (s *Service) AdoptedDestinationKeys(ctx context.Context, managementKey string) ([]string, error) {
	s = s.active()
	if s == nil || s.profile != ProfileManaged || len(managementKey) != 64 {
		return nil, ErrUnavailable
	}
	w, ok := s.wire.(keyWire)
	if !ok {
		return nil, ErrUnavailable
	}
	snap, err := w.KeySnapshot(ctx, managementKey)
	if err != nil || len(snap.SourceSHA256) != 64 || len(snap.Keys) == 0 {
		return nil, ErrUnavailable
	}
	return snap.Keys, nil
}

// PreparedAdoption holds the encrypted candidate records and one-shot journal
// metadata prepared ahead of the completion transaction, so the SQLite write
// transaction performs no key generation, encryption or nested store reads.
type PreparedAdoption struct {
	records []repo.Record
	journal repo.InitialInstall
}

// RunID reports the managed installation run identity the adoption was prepared for.
func (p PreparedAdoption) RunID() string { return p.journal.RunID }

// Generation reports the Runtime target generation the adoption was prepared for.
func (p PreparedAdoption) Generation() string { return p.journal.Generation }

// PrepareAdoption validates and encrypts the destination's Client Keys and binds
// the one-shot install journal metadata without touching SQLite.
func (s *Service) PrepareAdoption(ctx context.Context, secrets []string) (PreparedAdoption, error) {
	if s == nil || s.profile != ProfileManaged || s.repo == nil || s.keys == nil || s.registrationPermit == nil {
		return PreparedAdoption{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return PreparedAdoption{}, err
	}
	p, err := s.registrationPermit()
	if err != nil {
		return PreparedAdoption{}, err
	}
	records, err := s.keys.PrepareAdopted(secrets)
	if err != nil {
		return PreparedAdoption{}, mapAdoptionRegistrationError(err)
	}
	journal := repo.InitialInstall{
		RunID: p.RunID, Generation: p.Generation,
		AttemptedAtMS: time.Now().UnixMilli(), ProviderSeedSHA256: p.S2B.Binding(),
	}
	return PreparedAdoption{records: records, journal: journal}, nil
}

// CommitAdoptionTx commits every prepared adopted Client Key and claims the
// one-shot install journal inside the caller's completion CAS transaction.
func (s *Service) CommitAdoptionTx(ctx context.Context, tx *sql.Tx, prepared PreparedAdoption, runtimeIdentity string) ([]freshkeys.Key, error) {
	if s == nil || s.profile != ProfileManaged || s.repo == nil || s.keys == nil || tx == nil || len(prepared.records) == 0 {
		return nil, ErrUnavailable
	}
	keys, err := s.keys.RegisterAdoptedWithJournalTx(ctx, tx, prepared.records, runtimeIdentity, prepared.journal)
	if err != nil {
		return nil, mapAdoptionRegistrationError(err)
	}
	return keys, nil
}

// RegisterAdoption persists the Manager-side installation an adopted destination
// needs in one atomic SQLite transaction:
//
//  1. one canonical record per Client Key the destination carries, so the Key
//     workspace lists the credentials the adopted CPA actually has and the normal
//     revoke and replace journal can act on them; and
//  2. the install journal every install-derived surface reads — the gateway
//     configuration page, the key operation journal and the completion of a later
//     install all require the run, generation and provider seed the destination
//     was staged under, and an adoption staged its destination itself.
//
// Either every key and the one-shot journal commit together, or nothing is
// written. Replays with the same binding are idempotent; a retired key, a
// source binding owned by another identity, or a journal owned by another
// installation is refused as ErrAdoptionConflict.
func (s *Service) RegisterAdoption(ctx context.Context, secrets []string) ([]freshkeys.Key, error) {
	prepared, err := s.PrepareAdoption(ctx, secrets)
	if err != nil {
		return nil, err
	}
	keys, err := s.keys.RegisterAdoptedWithJournal(ctx, prepared.records, prepared.journal)
	if err != nil {
		return nil, mapAdoptionRegistrationError(err)
	}
	return keys, nil
}

func (s *Service) requireInstallJournal(ctx context.Context, p Permit) error {
	if s == nil || s.repo == nil {
		return ErrUnavailable
	}
	a, err := s.repo.InitialInstall(ctx)
	if err != nil {
		return err
	}
	if a == nil || a.RunID != p.RunID || a.Generation != p.Generation || a.ProviderSeedSHA256 != p.S2B.Binding() {
		return ErrGeneration
	}
	return nil
}

// AdoptionRegistrationReady reports whether a completed Managed Adopt
// installation already holds its matching one-shot install journal and primary
// key record. It returns (false, nil) when no journal exists yet so an upgraded
// installation completed before journaled adoption existed can backfill once,
// and returns ErrAdoptionConflict when the journal belongs to a different run,
// generation or provider seed.
func (s *Service) AdoptionRegistrationReady(ctx context.Context) (bool, error) {
	if s == nil || s.profile != ProfileManaged || s.repo == nil || s.keys == nil {
		return false, ErrUnavailable
	}
	p, err := s.permit()
	if err != nil {
		return false, err
	}
	inst, err := s.repo.InitialInstall(ctx)
	if err != nil {
		return false, err
	}
	if inst == nil {
		return false, nil
	}
	if inst.RunID != p.RunID || inst.Generation != p.Generation || inst.ProviderSeedSHA256 != p.S2B.Binding() {
		return false, fmt.Errorf("%w: %w", ErrAdoptionConflict, ErrGeneration)
	}
	if _, err := s.keys.Detail(ctx, inst.KeyID); err != nil {
		if errors.Is(err, freshkeys.ErrNotFound) {
			return false, fmt.Errorf("%w: %w", ErrAdoptionConflict, err)
		}
		return false, err
	}
	return true, nil
}

// VerifyRuntimeTarget proves the live Runtime identity, Ready state and active
// CPA artifact for the committed Managed installation.
func (s *Service) VerifyRuntimeTarget(ctx context.Context) error {
	if s == nil || s.profile != ProfileManaged {
		return ErrUnavailable
	}
	p, err := s.permit()
	if err != nil {
		return err
	}
	_, err = s.target(ctx, p)
	return err
}

func mapAdoptionRegistrationError(err error) error {
	if errors.Is(err, freshkeys.ErrRequest) || errors.Is(err, freshkeys.ErrAdoptedSecretRetired) ||
		errors.Is(err, repo.ErrSourceOwned) {
		return fmt.Errorf("%w: %w", ErrAdoptionConflict, err)
	}
	if errors.Is(err, repo.ErrJournalConflict) {
		return fmt.Errorf("%w: %w", ErrAdoptionConflict, ErrGeneration)
	}
	return err
}

// targetProof is the caller-observed target handed to the single write. A
// default install replaces the private attestation flags with the committed
// checkpoint and the authenticated destination read-back.
func (s *Service) targetProof(target model.RuntimeObservedStatus, p Permit) cpa.TargetProof {
	proof := cpa.TargetProof{Identity: string(target.Identity), Generation: uint64(target.Generation), Ready: true, ExpiresAtMS: p.ExpiresAtMS, ProviderSeed: p.S2B}
	if s.profile == ProfileManaged {
		if target.ActiveGatewayArtifact != nil {
			proof.ArtifactID = string(target.ActiveGatewayArtifact.ArtifactID)
		}
		return proof
	}
	proof.ArtifactID = cpa.PrivateKeyArtifact
	proof.SourceExclusive, proof.Disposable = true, true
	return proof
}
