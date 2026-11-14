package freshkeys

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
)

const h1Setting = "fresh_source_handoff_v2"

// Handoff is a bounded transport checkpoint in the existing Manager DB. It
// references, but never replaces or rewrites, immutable business terminals.
type Handoff struct {
	Version        int    `json:"version"`
	RunID          string `json:"runId"`
	SourceID       string `json:"sourceId"`
	Generation     string `json:"generation"`
	InitialDigest  string `json:"initialDigest"`
	Anchor         string `json:"anchor"`
	ParentID       string `json:"parentId"`
	IntentDigest   string `json:"intentDigest"`
	TerminalDigest string `json:"terminalDigest"`
	Receipts       string `json:"receipts"`
	Phase          string `json:"phase"`
}

func (r *Repository) Handoff(ctx context.Context) (*Handoff, error) {
	var raw string
	e := r.db.QueryRowContext(ctx, "select value from settings where key=?", h1Setting).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var h Handoff
	if json.Unmarshal([]byte(raw), &h) != nil || h.Version != 2 || h.RunID == "" || h.SourceID == "" || h.Generation == "" || len(h.InitialDigest) != 64 || len(h.Anchor) != 64 || (h.Phase != "free" && h.Phase != "held" && h.Phase != "terminal") || (h.Phase != "free" && (h.ParentID == "" || len(h.IntentDigest) != 64)) {
		return nil, ErrConflict
	}
	return &h, nil
}
func (r *Repository) SaveHandoff(ctx context.Context, old *Handoff, next Handoff) error {
	data, _ := json.Marshal(next)
	var result sql.Result
	var e error
	if old == nil {
		result, e = r.db.ExecContext(ctx, "insert into settings(key,value,updated_at_ms) values(?,?,?) on conflict(key) do nothing", h1Setting, string(data), time.Now().UnixMilli())
	} else {
		before, _ := json.Marshal(old)
		result, e = r.db.ExecContext(ctx, "update settings set value=?,updated_at_ms=? where key=? and value=?", string(data), time.Now().UnixMilli(), h1Setting, string(before))
	}
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

// Both domains are checked before every claim. The Runtime claim remains the
// cross-process arbiter; this does not pretend SQLite and Runtime are atomic.
func (r *Repository) H1Idle(ctx context.Context) error {
	k, e := r.LatestOperation(ctx)
	if e != nil {
		return e
	}
	if k != nil && !k.IsTerminal() {
		return ErrConflict
	}
	// Every allowlisted config field keeps its own journal entry. A new H1 claim
	// is only safe when no field still holds an original unknown operation; a
	// damaged field entry is skipped because the store already locks that field.
	var raw string
	owned := map[string]bool{}
	e = r.db.QueryRowContext(ctx, "select value from settings where key=?", freshstore.ConfigJournalSettingKey()).Scan(&raw)
	if e == nil {
		if err := freshstore.ValidateConfigJournals([]byte(raw)); err != nil {
			return err
		}
		owned = freshstore.ConfigJournalOwnedPaths([]byte(raw))
	} else if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	// The pre-allowlist rows are read-only but still owned: an interrupted legacy
	// write must block a new claim exactly like a current one. Once the current
	// container owns that field the row is folded in, so it is no longer evidence.
	for _, legacy := range freshstore.LegacyConfigJournalKeys() {
		if owned[legacy.Path] {
			continue
		}
		e = r.db.QueryRowContext(ctx, "select value from settings where key=?", legacy.Key).Scan(&raw)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return e
		}
		if freshstore.ValidateLegacyConfigJournal(legacy.Path, []byte(raw)) != nil {
			return ErrConflict
		}
	}
	return nil
}
