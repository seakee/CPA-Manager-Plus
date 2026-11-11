package freshkeyapply

import (
	"context"
	"crypto/subtle"
	"strconv"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

// Verdict is private, request-bound and never stored. It is not an effective
// Key projection or a reusable capability. The caller supplies a fresh binding
// of its random nonce, original Bearer and fully validated request bytes.
type Verdict struct {
	RequestBinding string `json:"requestBinding"`
	ExpiresAtMS    int64  `json:"expiresAtMs"`
	Model          string `json:"model,omitempty"`
}

// privateChatKeyLength is the canonical Key workspace secret length. This
// consumer is the private S1/S2 chat proof: it proves a credential this Manager
// minted, by shape first and by the installation's own staged secret second.
//
// An adopted installation carries the source CPA's opaque keys instead, which
// this proof cannot represent, so the length gate refuses them fail-closed rather
// than admitting a shape this Manager never minted. Gateway traffic for an
// adopted destination is admitted by the installation authority, not here.
const privateChatKeyLength = model.WorkspaceClientKeyLength

// Admit consumes the existing S1 authority with NEW observations for this call.
// It neither reads the five-second UI observation nor installs/replaces a Key.
func (s *Service) Admit(ctx context.Context, candidate string, generation uint64, binding string) (Verdict, error) {
	deny := Verdict{}
	s = s.active()
	if s == nil || !identity.IsValidSHA256Hex(binding) || len(candidate) != privateChatKeyLength {
		return deny, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	// Waiting for another S1 operation consumes this request's window too.
	until := time.Now().Add(time.Second).UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return deny, ErrUnavailable
	}
	p, err := s.permit()
	if err != nil {
		return deny, ErrUnavailable
	}
	if p.Generation != strconv.FormatUint(generation, 10) {
		return deny, ErrGeneration
	}
	if _, err := s.target(ctx, p); err != nil {
		return deny, err
	}
	attempt, err := s.repo.InitialInstall(ctx)
	if err != nil || attempt == nil {
		return deny, ErrUnavailable
	}
	if attempt.Generation != p.Generation || attempt.RunID != p.RunID || attempt.ProviderSeedSHA256 != p.S2B.Binding() {
		return deny, ErrGeneration
	}
	installedID, err := s.installedKey(ctx)
	if err != nil {
		return deny, err
	}
	err = s.keys.WithStagedSecret(ctx, installedID, func(secret string) error {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(secret)) != 1 {
			return ErrUnavailable
		}
		configured, err := s.wire.Configured(ctx, p.ManagementKey, secret)
		if err != nil || !configured {
			return ErrUnavailable
		}
		ok, err := s.wire.AuthSweep(ctx, secret)
		if err != nil || !ok {
			return ErrUnavailable
		}
		// Fence both the runtime and the single-key config after live auth.
		if _, err := s.target(ctx, p); err != nil {
			return err
		}
		configured, err = s.wire.Configured(ctx, p.ManagementKey, secret)
		if err != nil || !configured {
			return ErrUnavailable
		}
		return nil
	})
	if err != nil {
		return deny, err
	}
	current, err := s.permit()
	if err != nil || current != p || !current.valid(time.Now()) {
		return deny, ErrUnavailable
	}
	if until > p.ExpiresAtMS {
		until = p.ExpiresAtMS
	}
	if ctx.Err() != nil || time.Now().UnixMilli() >= until {
		return deny, ErrUnavailable
	}
	return Verdict{RequestBinding: binding, ExpiresAtMS: until, Model: p.S2B.Model}, nil
}
