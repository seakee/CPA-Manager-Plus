package setting

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const updateCheckKey = "manager_update_check_v1"

func LoadUpdateCheck(ctx context.Context, db *sql.DB) ([]byte, error) {
	var data []byte
	err := db.QueryRowContext(ctx, "select value from settings where key = ?", updateCheckKey).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return data, err
}
func SaveUpdateCheck(ctx context.Context, db *sql.DB, data []byte) error {
	_, err := db.ExecContext(ctx, "insert into settings(key,value,updated_at_ms) values(?,?,?) on conflict(key) do update set value=excluded.value,updated_at_ms=excluded.updated_at_ms", updateCheckKey, string(data), time.Now().UnixMilli())
	return err
}

// The insert is the notification's durable, cross-tab at-most-once boundary.
func ClaimUpdateNotification(ctx context.Context, db *sql.DB, tag string) (bool, error) {
	now := time.Now().UnixMilli()
	result, err := db.ExecContext(ctx, "insert into settings(key,value,updated_at_ms) values(?,json_object('release_tag',?,'notified_at',?,'dismissed_at',null),?) on conflict(key) do nothing", "manager_update_notification_v1:"+tag, tag, now, now)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
func DismissUpdateNotification(ctx context.Context, db *sql.DB, tag string) error {
	_, err := db.ExecContext(ctx, "update settings set value=json_set(value,'$.dismissed_at',?),updated_at_ms=? where key=?", time.Now().UnixMilli(), time.Now().UnixMilli(), "manager_update_notification_v1:"+tag)
	return err
}
