package freshconfig

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

type providerNameWire interface {
	ReadProviderNameDocument(context.Context, string) (map[string]any, string, error)
	PutProviderNameGroups(context.Context, string, string, string, []byte) error
}

// ProviderName is display metadata. Provider/index locate only this revision;
// neither the name nor the position is a persistent credential identity.
type ProviderName struct {
	Provider string `json:"provider"`
	Index    int    `json:"index"`
	Name     string `json:"name"`
}

type ProviderNamesView struct {
	Generation string         `json:"generation"`
	ETag       string         `json:"etag"`
	Writable   bool           `json:"writable"`
	Groups     []ProviderName `json:"groups"`
}

type ProviderNameRequest struct {
	Generation string `json:"generation"`
	ETag       string `json:"etag"`
	Provider   string `json:"provider"`
	Index      int    `json:"index"`
	Name       string `json:"name"`
}

func ValidateProviderName(q ProviderNameRequest) error {
	if !cpa.ProviderNameKind(q.Provider) || q.Index < 0 || q.Index > 100000 || q.Generation == "" || q.ETag == "" || !utf8.ValidString(q.Name) || utf8.RuneCountInString(q.Name) > 120 {
		return ErrInvalid
	}
	for _, ch := range q.Name {
		if unicode.IsControl(ch) {
			return ErrInvalid
		}
	}
	if q.Name != strings.TrimSpace(q.Name) {
		return ErrInvalid
	}
	return nil
}

func projectProviderNames(document map[string]any, generation, etag string, writable bool) ProviderNamesView {
	view := ProviderNamesView{Generation: generation, ETag: etag, Writable: writable, Groups: []ProviderName{}}
	keys, _ := document["api-keys"].(map[string]any)
	for _, provider := range []string{"codex", "claude", "gemini", "interactions", "xai", "vertex", "openai-compatibility"} {
		groups, _ := keys[provider].([]any)
		for index, value := range groups {
			group, ok := value.(map[string]any)
			if !ok {
				continue
			}
			name, _ := group["name"].(string)
			view.Groups = append(view.Groups, ProviderName{provider, index, name})
		}
	}
	return view
}

// ProviderNames uses the same install-derived authority as the gateway page.
// Only a name projection crosses the Manager/browser boundary.
func (s *Service) ProviderNames(ctx context.Context) (ProviderNamesView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.store.Load(ctx)
	if err != nil {
		return ProviderNamesView{}, err
	}
	auth, err := s.resolveProfile(ctx, r)
	if err != nil {
		return ProviderNamesView{}, err
	}
	wire, ok := auth.wire.(providerNameWire)
	if !ok || !auth.capable.Readable {
		return ProviderNamesView{}, cpa.ErrProofUnavailable
	}
	document, etag, err := wire.ReadProviderNameDocument(ctx, auth.secret)
	if err != nil {
		return ProviderNamesView{}, err
	}
	return projectProviderNames(document, strconv.FormatUint(r.Operation.Strategy.TargetGeneration, 10), etag, auth.mode != ModePrivate && auth.capable.Writable), nil
}

// RenameProvider never stores aliases. CPA owns the native name and the atomic
// file revision check. All unknown group/key fields are retained verbatim.
func (s *Service) RenameProvider(ctx context.Context, q ProviderNameRequest) error {
	if err := ValidateProviderName(q); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.store.Load(ctx)
	if err != nil {
		return err
	}
	if strconv.FormatUint(r.Operation.Strategy.TargetGeneration, 10) != q.Generation {
		return ErrGenerationMismatch
	}
	auth, err := s.resolveProfile(ctx, r)
	if err != nil {
		return err
	}
	if auth.mode == ModePrivate || !auth.capable.Writable {
		return ErrReadOnly
	}
	if err = s.externalWriteGate(ctx, auth); err != nil {
		return err
	}
	wire, ok := auth.wire.(providerNameWire)
	if !ok {
		return cpa.ErrProofUnavailable
	}
	document, etag, err := wire.ReadProviderNameDocument(ctx, auth.secret)
	if err != nil {
		return err
	}
	if etag != q.ETag {
		return cpa.ErrConfigChanged
	}
	keys, _ := document["api-keys"].(map[string]any)
	groups, _ := keys[q.Provider].([]any)
	if q.Index >= len(groups) {
		return ErrInvalid
	}
	group, ok := groups[q.Index].(map[string]any)
	if !ok {
		return ErrInvalid
	}
	// Empty optional metadata restores the masked-key fallback. CPA may reject
	// clearing a name required by a particular native provider contract.
	if q.Name == "" {
		delete(group, "name")
	} else {
		group["name"] = q.Name
	}
	payload, err := json.Marshal(groups)
	if err != nil {
		return ErrInvalid
	}
	latest, err := s.store.Load(ctx)
	if err != nil {
		return err
	}
	if latest.Operation.Identity != r.Operation.Identity || latest.Operation.Revision != r.Operation.Revision {
		return ErrGenerationMismatch
	}
	return wire.PutProviderNameGroups(ctx, auth.secret, q.Provider, etag, payload)
}
