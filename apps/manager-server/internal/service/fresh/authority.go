package fresh

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

var ErrAuthority = errors.New("Fresh Bootstrap authority unavailable")

type Service struct {
	store freshstore.Store
	now   func() time.Time
}

func New(st freshstore.Store) *Service { return &Service{store: st, now: time.Now} }

type ExchangeRequest struct {
	Identity        bootstrap.FreshIdentity `json:"identity"`
	ClaimGeneration uint64                  `json:"claimGeneration"`
	Token           string                  `json:"token"`
}
type AdminCommitRequest struct {
	Identity bootstrap.FreshIdentity `json:"identity"`
	CommitID string                  `json:"commitId"`
	Key      string                  `json:"key"`
}
type Handoff struct {
	Fragment        string                   `json:"fragment"`
	Operation       bootstrap.FreshOperation `json:"operation"`
	ClaimGeneration uint64                   `json:"claimGeneration"`
	Resume          bool                     `json:"resume"`
}
type SessionResult struct {
	Token     string                   `json:"token"`
	Operation bootstrap.FreshOperation `json:"operation"`
}

func opaque() (string, error) {
	v := make([]byte, 32)
	_, err := rand.Read(v)
	return base64.RawURLEncoding.EncodeToString(v), err
}
func hash(v string) string { d := sha256.Sum256([]byte(v)); return hex.EncodeToString(d[:]) }
func tokenMatches(token, digest string) bool {
	return len(token) == 43 && digest != "" && security.EqualHMAC(hash(token), digest)
}
func (s *Service) active(ctx context.Context) (freshstore.Record, error) {
	v, err := s.store.Load(ctx)
	if err != nil || v.Operation.Routing.Committed {
		return freshstore.Record{}, ErrAuthority
	}
	return v, nil
}

// Issue is a host-controlled product action, never a public discovery action.
func (s *Service) Issue(ctx context.Context) (Handoff, error) {
	v, err := s.active(ctx)
	if err != nil {
		return Handoff{}, err
	}
	token, err := opaque()
	if err != nil {
		return Handoff{}, err
	}
	resume := v.Operation.AdminInitialized
	next, err := s.store.Update(ctx, v.Operation.Identity, v.Operation.Revision, func(r *freshstore.Record) error {
		if r.Authority.ClaimGeneration == math.MaxUint64 {
			return ErrAuthority
		}
		r.Authority.ClaimGeneration++
		r.Authority.SessionHash = ""
		r.Authority.SessionExpiresAtMS = 0
		r.Authority.ClaimHash = ""
		r.Authority.ClaimExpiresAtMS = 0
		if resume {
			r.Authority.SessionHash = hash(token)
			r.Authority.SessionGeneration = r.Authority.ClaimGeneration
			r.Authority.SessionExpiresAtMS = s.now().Add(bootstrap.FreshSessionLifetime).UnixMilli()
		} else {
			if r.Authority.ClaimClosed {
				return ErrAuthority
			}
			r.Authority.ClaimHash = hash(token)
			r.Authority.ClaimExpiresAtMS = s.now().Add(bootstrap.FreshClaimLifetime).UnixMilli()
			r.Authority.ClaimExchanged = false
		}
		return nil
	})
	if err != nil {
		return Handoff{}, err
	}
	payload := struct {
		Identity        bootstrap.FreshIdentity `json:"identity"`
		ClaimGeneration uint64                  `json:"claimGeneration"`
		Token           string                  `json:"token"`
		Resume          bool                    `json:"resume"`
	}{next.Operation.Identity, next.Authority.ClaimGeneration, token, resume}
	data, err := json.Marshal(payload)
	if err != nil {
		return Handoff{}, err
	}
	return Handoff{Fragment: "#handoff=" + base64.RawURLEncoding.EncodeToString(data), Operation: next.Operation, ClaimGeneration: next.Authority.ClaimGeneration, Resume: resume}, nil
}
func (s *Service) Exchange(ctx context.Context, req ExchangeRequest) (SessionResult, error) {
	v, err := s.active(ctx)
	if err != nil {
		return SessionResult{}, err
	}
	token, err := opaque()
	if err != nil {
		return SessionResult{}, err
	}
	next, err := s.store.Update(ctx, v.Operation.Identity, v.Operation.Revision, func(r *freshstore.Record) error {
		a := &r.Authority
		if r.Operation.Identity != req.Identity || a.ClaimGeneration != req.ClaimGeneration || a.ClaimClosed || a.ClaimExchanged || s.now().UnixMilli() >= a.ClaimExpiresAtMS || !tokenMatches(req.Token, a.ClaimHash) {
			return ErrAuthority
		}
		a.ClaimExchanged = true
		a.ClaimHash = ""
		a.SessionHash = hash(token)
		a.SessionGeneration = a.ClaimGeneration
		a.SessionExpiresAtMS = s.now().Add(bootstrap.FreshSessionLifetime).UnixMilli()
		return nil
	})
	if err != nil {
		return SessionResult{}, err
	}
	return SessionResult{Token: token, Operation: next.Operation}, nil
}
func (s *Service) session(r freshstore.Record, token string) bool {
	return !r.Operation.Routing.Committed && r.Authority.SessionGeneration == r.Authority.ClaimGeneration && s.now().UnixMilli() < r.Authority.SessionExpiresAtMS && tokenMatches(token, r.Authority.SessionHash)
}

// ValidateSession rechecks a held Fresh transaction at the final commit fence.
// This lets initial routing reject expiry/reissue after public-entry probes.
func (s *Service) ValidateSession(r freshstore.Record, token string) error {
	if r.Operation.Validate() != nil || !s.session(r, token) {
		return ErrAuthority
	}
	return nil
}
func (s *Service) Authorize(ctx context.Context, token string) (bootstrap.FreshOperation, error) {
	v, err := s.active(ctx)
	if err != nil || !s.session(v, token) {
		return bootstrap.FreshOperation{}, ErrAuthority
	}
	return v.Operation, nil
}
func (s *Service) GenerateAdmin(ctx context.Context, token string) (string, error) {
	v, err := s.active(ctx)
	if err != nil || !s.session(v, token) || v.Operation.AdminInitialized || v.Authority.ClaimClosed {
		return "", ErrAuthority
	}
	return security.GenerateAdminKey()
}
func (s *Service) CommitAdmin(ctx context.Context, token string, req AdminCommitRequest) (bootstrap.FreshOperation, error) {
	v, err := s.active(ctx)
	if err != nil {
		return bootstrap.FreshOperation{}, err
	}
	if !s.session(v, token) || v.Operation.Identity != req.Identity || req.CommitID == "" || len(req.CommitID) > 128 {
		return bootstrap.FreshOperation{}, ErrAuthority
	}
	if v.Operation.AdminInitialized {
		if v.Authority.AdminCommitID == req.CommitID && v.Admin != nil && security.VerifyAdminKey(*v.Admin, req.Key) {
			return v.Operation, nil
		}
		return bootstrap.FreshOperation{}, ErrAuthority
	}
	if err = security.ValidateFreshAdminKey(req.Key); err != nil {
		return bootstrap.FreshOperation{}, err
	}
	credential, err := security.NewAdminCredential(req.Key, "fresh")
	if err != nil {
		return bootstrap.FreshOperation{}, err
	}
	next, err := s.store.Update(ctx, v.Operation.Identity, v.Operation.Revision, func(r *freshstore.Record) error {
		if !s.session(*r, token) || r.Operation.Identity != req.Identity || r.Operation.AdminInitialized || r.Authority.ClaimClosed {
			return ErrAuthority
		}
		r.Admin = &credential
		r.Operation.AdminInitialized = true
		r.Authority.ClaimClosed = true
		r.Authority.ClaimHash = ""
		r.Authority.ClaimExchanged = true
		r.Authority.AdminCommitID = req.CommitID
		return nil
	})
	if errors.Is(err, freshstore.ErrConflict) {
		current, loadErr := s.active(ctx)
		if loadErr == nil && s.session(current, token) && current.Operation.Identity == req.Identity &&
			current.Authority.AdminCommitID == req.CommitID && current.Operation.AdminInitialized &&
			current.Admin != nil && security.VerifyAdminKey(*current.Admin, req.Key) {
			return current.Operation, nil
		}
	}
	return next.Operation, err
}
func (s *Service) Revoke(ctx context.Context) error {
	v, err := s.active(ctx)
	if err != nil {
		return err
	}
	_, err = s.store.Update(ctx, v.Operation.Identity, v.Operation.Revision, func(r *freshstore.Record) error { r.Authority.ClaimHash = ""; r.Authority.SessionHash = ""; return nil })
	return err
}
func (s *Service) Discover(ctx context.Context) bootstrap.PublicDiscovery {
	d := bootstrap.PublicDiscovery{ContractVersion: bootstrap.PublicDiscoveryContractVersion, Status: bootstrap.PublicUnavailable, AuthEntry: bootstrap.AuthEntryUnavailable, Reason: bootstrap.ReasonObservationUnavailable}
	v, err := s.active(ctx)
	if err != nil || v.Operation.AdminInitialized || v.Authority.ClaimClosed {
		return d
	}
	mode := bootstrap.Fresh
	d.Status = bootstrap.PublicFreshClaimRequired
	d.Mode = &mode
	d.AuthenticationRequired = true
	d.AuthEntry = bootstrap.AuthEntryFreshClaim
	d.Reason = bootstrap.ReasonFreshClaimRequired
	return d
}
