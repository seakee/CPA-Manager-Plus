// Package freshstore persists the bounded Fresh product checkpoint and its
// authentication hashes in the actual Manager SQLite settings store.
package freshstore

import (
	"context"
	"errors"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

var (
	ErrNotFresh = errors.New("matching Fresh ownership unavailable")
	ErrConflict = errors.New("Fresh authority conflict")
)

// Authentication hashes are not part of the non-secret product checkpoint.
type Authority struct {
	ClaimHash          string `json:"claimHash,omitempty"`
	ClaimGeneration    uint64 `json:"claimGeneration"`
	ClaimExpiresAtMS   int64  `json:"claimExpiresAtMs"`
	ClaimExchanged     bool   `json:"claimExchanged"`
	ClaimClosed        bool   `json:"claimClosed"`
	SessionHash        string `json:"sessionHash,omitempty"`
	SessionExpiresAtMS int64  `json:"sessionExpiresAtMs"`
	SessionGeneration  uint64 `json:"sessionGeneration"`
	AdminCommitID      string `json:"adminCommitId,omitempty"`
}

type Record struct {
	Operation bootstrap.FreshOperation
	Authority Authority
	Admin     *model.AdminCredential
	// Canonical server-owned connection. ManagementKey must be a Protector
	// ciphertext at this repository boundary, never a plaintext journal field.
	Connection *model.ManagerCPAConnectionConfig
	// Same Fresh CAS authority; encrypted resource snapshot is never public.
	AdoptionCiphertext string
}

// Store exposes only Fresh CAS transactions, not arbitrary workflow execution.
// Create requires a pristine admitted store and inserts ownership first. Update
// revalidates identity/revision and canonical admin consistency under SQLite's
// immediate write transaction; it may insert admin once but never overwrite it.
type Store interface {
	Load(context.Context) (Record, error)
	Create(context.Context, bootstrap.FreshIdentity, *model.AdminCredential) (Record, error)
	Update(context.Context, bootstrap.FreshIdentity, uint64, func(*Record) error) (Record, error)
}
