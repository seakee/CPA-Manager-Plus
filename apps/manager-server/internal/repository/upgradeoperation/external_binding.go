package upgradeoperation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

// externalBindingTableName is the single-row table holding the External connect
// step's durable target binding. It is separate from upgrade_operations because
// the binding is one step's own record: the operation row stays the one source
// of stage, domains and provenance, and this table answers only "which CPA did
// the external_connect stage connect".
const externalBindingTableName = "upgrade_external_connect"

// ErrExternalBindingInvalid reports a binding that cannot name the target it
// claims: an empty address, an empty identity, or an identity that is not the
// generation. Such a row would let a rerun compare against nothing.
var ErrExternalBindingInvalid = errors.New("external connect binding is invalid")

// ExternalBinding is the durable record of the External CPA an operation
// connected: the canonical address, the address-derived identity and the
// generation it was recorded as, plus the observations the step reported.
//
// It is written in the same transaction as the stage and the domains it proves,
// so the row can never claim external_connect without naming its target, and it
// is read instead of the evidence file, so a rerun decides "same target" or
// "moved target" from the durable record. The file under the data root is a
// projection of this value.
type ExternalBinding struct {
	BaseURL           string
	Identity          string
	Generation        string
	ServerFingerprint string
	CPAVersion        string
	Support           string
	ClientKeys        int
	ObservedAtMS      int64
}

// Validate reports whether the binding names a target a rerun could compare
// against.
func (b ExternalBinding) Validate() error {
	switch {
	case strings.TrimSpace(b.BaseURL) == "":
		return fmt.Errorf("%w: the target address is empty", ErrExternalBindingInvalid)
	case strings.TrimSpace(b.Identity) == "" || strings.TrimSpace(b.Generation) == "":
		return fmt.Errorf("%w: the target identity or generation is empty", ErrExternalBindingInvalid)
	case b.Identity != b.Generation:
		return fmt.Errorf("%w: the identity must be the generation", ErrExternalBindingInvalid)
	case b.ObservedAtMS <= 0:
		return fmt.Errorf("%w: the observation time is missing", ErrExternalBindingInvalid)
	}
	return nil
}

// ExternalBinding returns the recorded binding. found is false before the
// External connect step recorded one; a store written by a build that predates
// the binding table simply has none, because a writable open creates the table.
func (r *Repository) ExternalBinding(ctx context.Context) (ExternalBinding, bool, error) {
	return readExternalBinding(ctx, r.db.QueryRowContext(ctx,
		`select base_url, identity, generation, server_fingerprint, cpa_version, support, client_keys, observed_at_ms
		from `+externalBindingTableName+` where id = 1`))
}

func readExternalBinding(ctx context.Context, row *sql.Row) (ExternalBinding, bool, error) {
	var binding ExternalBinding
	err := row.Scan(&binding.BaseURL, &binding.Identity, &binding.Generation, &binding.ServerFingerprint,
		&binding.CPAVersion, &binding.Support, &binding.ClientKeys, &binding.ObservedAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		return ExternalBinding{}, false, nil
	}
	if err != nil {
		return ExternalBinding{}, false, err
	}
	if err := binding.Validate(); err != nil {
		return ExternalBinding{}, false, fmt.Errorf("%w: %v", ErrInvalidRow, err)
	}
	return binding, true, nil
}

// RecordExternalBindingTx writes the binding inside the caller's transaction, so
// the External connect step's stage, its domains and the target it connected
// commit together or not at all. A binding that does not name its target is
// refused before the transaction touches the row.
func RecordExternalBindingTx(ctx context.Context, tx Tx, binding ExternalBinding) error {
	if err := binding.Validate(); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `insert into `+externalBindingTableName+` (
		id, base_url, identity, generation, server_fingerprint, cpa_version, support, client_keys, observed_at_ms
	) values (1, ?, ?, ?, ?, ?, ?, ?, ?)
	on conflict(id) do update set
		base_url = excluded.base_url,
		identity = excluded.identity,
		generation = excluded.generation,
		server_fingerprint = excluded.server_fingerprint,
		cpa_version = excluded.cpa_version,
		support = excluded.support,
		client_keys = excluded.client_keys,
		observed_at_ms = excluded.observed_at_ms`,
		binding.BaseURL, binding.Identity, binding.Generation, binding.ServerFingerprint,
		binding.CPAVersion, binding.Support, binding.ClientKeys, binding.ObservedAtMS)
	return err
}

// GetTx reads the single operation row inside the caller's transaction, so a
// writer can return the row it just wrote instead of re-reading it after commit
// and reporting a stamp another writer has already moved on.
func GetTx(ctx context.Context, tx Tx) (model.UpgradeOperation, bool, error) {
	return scanOperation(tx.QueryRowContext(ctx,
		`select `+selectColumns+` from `+tableName+` where id = 1`))
}
