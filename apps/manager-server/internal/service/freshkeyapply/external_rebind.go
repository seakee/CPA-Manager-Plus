package freshkeyapply

import (
	"context"
	"errors"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

// ErrRebindRefused reports a rebind this authority will not perform: an address
// that is not the committed one, a credential the user's CPA refused, a
// credential that does not serve the v8 contract, an installation that is not a
// committed Connect External target, or a request the caller built against
// another generation. It never carries a credential.
var ErrRebindRefused = errors.New("external rebind refused")

// ExternalRebindVerifier proves one candidate management credential against the
// user's CPA without persisting anything. It is the read that decides the rebind:
// an installation is only worth rebinding to a credential its own CPA answers.
type ExternalRebindVerifier func(ctx context.Context, baseURL string, managementKey string) (cpa.ExternalVersionReport, error)

// ExternalRebindResult is what a rebind did. Reverified is the same-key case: the
// stored credential authenticated again, so the installation is unchanged and only
// the rejected state needs clearing. Record is the installation as it now stands —
// the rebound record, or the untouched one on a re-verify.
type ExternalRebindResult struct {
	Record     freshstore.Record
	Reverified bool
}

// ObserveExternalRebindCredential is the production verifier: one authenticated
// management read that also classifies the version the CPA advertises.
func ObserveExternalRebindCredential(ctx context.Context, baseURL string, managementKey string) (cpa.ExternalVersionReport, error) {
	return cpa.ObserveExternalVersion(ctx, baseURL, managementKey)
}

// externalRebind is the durable record operation a rebind needs.
type externalRebindStore interface {
	freshstore.Store
	freshstore.ConnectionRebinder
}

// Rebind replaces the management credential of the committed External
// connection. The user's CPA is administered by the user, so a rotated management
// credential is a fact of their installation rather than an outage: only the
// administrator can supply the new one, and this is the one path that accepts it.
//
// The order is what makes it safe. The record revision is read before the
// candidate is proven, and it is the revision the write is conditional on: a save,
// a key write or another rebind that lands while the candidate is being verified
// therefore conflicts instead of being overwritten by a decision taken against a
// record this installation no longer holds. The candidate itself is proven with one
// authenticated read against exactly the committed address, so nothing is written
// for a credential that does not authenticate or that does not serve the v8
// contract this Manager writes. Only then is it encrypted and persisted, in the
// transaction that replaces the connection and bumps the record revision — which is
// what makes every write prepared against the previous credential a mismatch
// instead of a write against a credential this Manager no longer holds.
//
// A candidate equal to the stored credential is a re-verify instead of a
// rotation: the user's CPA may simply have been banning this address, and the way
// out of that blocker must not require inventing a credential. It does the same
// one read, persists nothing, and leaves the revision alone.
//
// The address is never changed here: a different address is a different
// installation, and reaching it is a reconnect rather than a rebind.
func (s *Service) Rebind(ctx context.Context, store externalRebindStore, admin *security.Protector, generation uint64,
	baseURL string, managementKey string) (ExternalRebindResult, error) {
	if s == nil || s.profile != ProfileExternal || store == nil || admin == nil {
		return ExternalRebindResult{}, ErrExternalUnavailable
	}
	if !cpa.ValidV8ManagementKey(managementKey) {
		// A credential CPA itself could never accept is refused before any read:
		// the shape rule is the same one every write enforces.
		return ExternalRebindResult{}, ErrRebindRefused
	}
	install, err := s.externalTarget(ctx, generation)
	if err != nil {
		return ExternalRebindResult{}, err
	}
	if strings.TrimSpace(baseURL) == "" {
		// The committed address is the only address a rebind may reach. A caller
		// that does not name it is asking for a reconnect, never a rebind.
		baseURL = install.BaseURL
	}
	if baseURL != install.BaseURL {
		return ExternalRebindResult{}, ErrRebindRefused
	}
	// The revision is read here, before the verification that may take as long as
	// one CPA round trip: the write below is conditional on this exact revision, so
	// a record that moves during the verification conflicts instead of being
	// overwritten by a rebind decided against stale state.
	record, err := store.Load(ctx)
	if err != nil {
		return ExternalRebindResult{}, ErrExternalUnavailable
	}
	if record.Operation.Strategy.Strategy != bootstrap.ConnectExternal {
		return ExternalRebindResult{}, ErrExternalUnavailable
	}
	verify := s.externalRebindVerifier
	if verify == nil {
		verify = ObserveExternalRebindCredential
	}
	if managementKey == install.ManagementKey {
		// The candidate is the credential already stored, which makes this a
		// re-verify rather than a rotation: the common cause of a rejected key is
		// a ban window that has since passed, and until now the only way out of
		// that blocker was to supply a credential nobody had rotated. One
		// authenticated read decides it. Nothing is persisted and the revision is
		// untouched, because no credential changed; a candidate the CPA still
		// refuses answers with the same refusal a rotation would, so the page
		// keeps its blocker and gains no false success.
		report, err := verify(ctx, install.BaseURL, managementKey)
		switch {
		case errors.Is(err, cpa.ErrManagementClientForbidden):
			return ExternalRebindResult{}, cpa.ErrManagementClientForbidden
		case errors.Is(err, cpa.ErrManagementRejected):
			return ExternalRebindResult{}, cpa.ErrManagementRejected
		case err != nil:
			return ExternalRebindResult{}, err
		case report.WriteBlocked:
			return ExternalRebindResult{}, cpa.ErrExternalVersionUnsupported
		}
		return ExternalRebindResult{Record: record, Reverified: true}, nil
	}
	report, err := verify(ctx, install.BaseURL, managementKey)
	switch {
	case errors.Is(err, cpa.ErrManagementClientForbidden):
		// CPA refused this client, not the credential: an address ban or a
		// disabled remote management. It is reported unchanged so the page can
		// say that the same key may work again once the window passes.
		return ExternalRebindResult{}, cpa.ErrManagementClientForbidden
	case errors.Is(err, cpa.ErrManagementRejected):
		// The user's CPA refused the candidate, so nothing was proven and nothing
		// may be persisted. It is reported as the same typed rejection the reads
		// carry, so the page keeps asking for a credential instead of a retry.
		return ExternalRebindResult{}, cpa.ErrManagementRejected
	case err != nil:
		return ExternalRebindResult{}, err
	case report.WriteBlocked:
		return ExternalRebindResult{}, cpa.ErrExternalVersionUnsupported
	}
	ciphertext, err := admin.ProtectString(managementKey)
	if err != nil {
		return ExternalRebindResult{}, ErrExternalUnavailable
	}
	next := model.ManagerCPAConnectionConfig{CPABaseURL: install.BaseURL, ManagementKey: ciphertext}
	rebound, err := store.RebindConnection(ctx, record.Operation.Identity, record.Operation.Revision, next)
	if err != nil {
		if errors.Is(err, freshstore.ErrConflict) {
			// A save, a key write or another rebind moved the record after the
			// revision above was read — during the verification included. The page
			// re-reads and asks again: guessing which of the two writes won would
			// persist a credential the user did not intend.
			return ExternalRebindResult{}, ErrGeneration
		}
		if errors.Is(err, freshstore.ErrRebindRefused) {
			return ExternalRebindResult{}, ErrRebindRefused
		}
		return ExternalRebindResult{}, ErrExternalUnavailable
	}
	return ExternalRebindResult{Record: rebound}, nil
}
