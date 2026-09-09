package sqlite

import (
	"database/sql"
	"fmt"
)

type RowQueryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

func HistoricalRawDeletionExists(queryer RowQueryer) (bool, error) {
	var tableExists int
	if err := queryer.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = 'usage_archive_event_refs'`).Scan(&tableExists); err != nil {
		return false, fmt.Errorf("inspect usage archive event refs table for raw deletion: %w", err)
	}
	if tableExists == 0 {
		return false, nil
	}
	var hasDeletedRaw int
	err := queryer.QueryRow(`select exists (
		select 1 from usage_archive_event_refs where raw_deleted_at_ms is not null
	)`).Scan(&hasDeletedRaw)
	if err != nil {
		return false, fmt.Errorf("inspect deleted raw events: %w", err)
	}
	return hasDeletedRaw != 0, nil
}

func ensureCompleteRawSourceForDerivedRebuild(queryer RowQueryer, reason string) error {
	hasDeletedRaw, err := HistoricalRawDeletionExists(queryer)
	if err != nil {
		return fmt.Errorf("inspect raw source for derived rebuild: %w", err)
	}
	if hasDeletedRaw {
		return fmt.Errorf(
			"cannot rebuild %s: historical raw usage events have been archived and deleted; derived rebuild requires complete raw usage history",
			reason,
		)
	}
	return nil
}
