package credentiallabel

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/credentiallabel"
)

var ErrInvalid = errors.New("keyHash must be a SHA-256 hex digest and label must contain at most 120 characters without control characters")

type Service struct{ repository repo.Repository }

func New(repository repo.Repository) *Service { return &Service{repository: repository} }
func (s *Service) List(ctx context.Context) ([]model.CredentialLabel, error) {
	return s.repository.List(ctx)
}
func (s *Service) Set(ctx context.Context, keyHash, label string) (model.CredentialLabel, error) {
	keyHash = strings.ToLower(strings.TrimSpace(keyHash))
	rawLabel := label
	label = strings.TrimSpace(label)
	decoded, err := hex.DecodeString(keyHash)
	if err != nil || len(decoded) != 32 || !utf8.ValidString(label) || utf8.RuneCountInString(label) > 120 {
		return model.CredentialLabel{}, ErrInvalid
	}
	for _, char := range rawLabel {
		if unicode.IsControl(char) {
			return model.CredentialLabel{}, ErrInvalid
		}
	}
	return s.repository.Set(ctx, keyHash, label)
}
