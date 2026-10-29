package freshstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

// ErrRebindRefused reports a rebind the durable record cannot accept: a
// checkpoint that is not a committed Connect External installation, an address
// the caller tried to change, or a credential that is not a protected envelope. It is typed so the caller answers
// with the reason the page can act on instead of a generic conflict.
var ErrRebindRefused = errors.New("external rebind refused by the durable record")

// RebindConnection replaces the protected Management credential of a committed
// Connect External installation whose configured address did not change.
//
// It is the one write this Manager performs on a connection it already holds, so
// it is a transaction of its own rather than a callback on Update: Update may
// insert a connection once and never rewrite one, which is exactly what keeps a
// committed installation's address from being edited behind its checkpoint.
//
// Three facts make the replacement safe and are enforced here, not by the caller:
//   - the address is unchanged, so the installation identity — and with it every
//     key-usage binding — stays the same value;
//   - the new credential is a protected envelope, so no plaintext reaches the
//     settings table;
//   - the record revision is bumped, so a request built against the previous
//     record is a conflict. The target generation is NOT bumped: the Connect
//     External checkpoint pins it to 1, and it names the installation, which a
//     credential rotation does not change. A write prepared before the rebind is
//     therefore executed with the credential the record holds NOW — the rebind
//     replaces a credential, it never authorizes a write with a stale one.
//
// A different address is refused: that is a reconnect, which commits a new
// installation rather than re-pointing this one.
func (r *repository) RebindConnection(ctx context.Context, id bootstrap.FreshIdentity, revision uint64, next model.ManagerCPAConnectionConfig) (Record, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	v, err := load(ctx, tx)
	if err != nil {
		return Record{}, err
	}
	if v.Operation.Identity != id || v.Operation.Revision != revision || revision == math.MaxUint64 {
		return Record{}, ErrConflict
	}
	checkpoint := v.Operation.Strategy
	if checkpoint.Strategy != bootstrap.ConnectExternal || checkpoint.Stage != "committed" ||
		!v.Operation.ProductComplete || v.Connection == nil {
		return Record{}, ErrRebindRefused
	}
	if next.CPABaseURL == "" || next.CPABaseURL != v.Connection.CPABaseURL ||
		!security.IsValidProtectedEnvelope(next.ManagementKey) {
		return Record{}, ErrRebindRefused
	}
	v.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: next.CPABaseURL, ManagementKey: next.ManagementKey}
	// The address is the installation identity, so the checkpoint keeps naming the
	// same CPA and keeps its pinned target generation; only the record revision
	// moves, which is the CAS every writer of this record holds.
	v.Operation.Revision++
	if err = valid(v); err != nil {
		return Record{}, err
	}
	if err = write(ctx, tx, OperationKey, v.Operation); err == nil {
		err = write(ctx, tx, AuthorityKey, v.Authority)
	}
	// The persisted connection and its compatibility mirrors carry the new
	// envelope in the same transaction, or a legacy reader would keep using the
	// credential this rebind replaced. The rest of the Manager configuration is
	// the operator's and is preserved byte for byte.
	if err == nil {
		err = writeManagerConnection(ctx, tx, *v.Connection)
	}
	if err == nil {
		err = write(ctx, tx, "setup", model.Setup{CPAUpstreamURL: v.Connection.CPABaseURL, ManagementKey: v.Connection.ManagementKey})
	}
	if err != nil {
		return Record{}, err
	}
	return v, tx.Commit()
}

// writeManagerConnection replaces only the CPA connection leaf of the persisted
// Manager configuration. A missing row is created with the Collector disabled,
// exactly as the first connection insert does.
func writeManagerConnection(ctx context.Context, tx *sql.Tx, connection model.ManagerCPAConnectionConfig) error {
	cfg := model.ManagerConfig{Collector: model.ManagerCollectorConfig{Enabled: new(bool)}}
	var data string
	err := tx.QueryRowContext(ctx, "select value from settings where key='manager_config_v1'").Scan(&data)
	switch {
	case err == nil:
		// An unreadable row is refused rather than overwritten: it belongs to the
		// compatibility domain, and this write may not discard it.
		if json.Unmarshal([]byte(data), &cfg) != nil {
			return ErrRebindRefused
		}
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	cfg.CPAConnection = connection
	cfg.UpdatedAtMS = time.Now().UnixMilli()
	return write(ctx, tx, "manager_config_v1", cfg)
}

// ConnectionRebinder is the one capability a committed Connect External
// installation needs beyond the generic CAS store: replacing the protected
// management credential of the address it already holds. It is a separate
// interface because it is a separate authority — a Store implementation that only
// performs Fresh CAS must not appear able to re-point a connection.
type ConnectionRebinder interface {
	RebindConnection(context.Context, bootstrap.FreshIdentity, uint64, model.ManagerCPAConnectionConfig) (Record, error)
}
