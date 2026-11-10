package upgrade

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

// LegacyDiscoveryObservation is the bounded, read-only verdict on a stopped
// 1.x Manager volume that could not be inspected through the rollback-only
// Phase4-01 snapshot (every 1.x v1.14.x release runs in SQLite WAL mode, and a
// 1.x Docker volume has root-owned 0600 lock/key files when mounted into v2).
//
// It never grants startup admission, migration authority, or writes, and it
// deliberately reports LegacyDiscoveryNone on native-v2 WAL stores, busy
// locks, absent lock files, or any source that changes while being copied.
type LegacyDiscoveryObservation string

const (
	LegacyDiscoveryNone                 LegacyDiscoveryObservation = ""
	LegacyDiscoveryOwnershipUnavailable LegacyDiscoveryObservation = "ownership_unavailable"
	LegacyDiscoveryUnsupportedUpgrade   LegacyDiscoveryObservation = "unsupported_upgrade"
	LegacyDiscoveryRecoveryRequired     LegacyDiscoveryObservation = "recovery_required"
)

var sqliteFileMagic = []byte("SQLite format 3\x00")

// WithReadOnlyLegacyObservation inspects a stopped 1.x source at databasePath
// through an exclusive read-only ownership fence and a private scratch copy
// outside both the database's directory and extraExcludedRoot, invoking
// consume before releasing the ownership fence.
func WithReadOnlyLegacyObservation(
	ctx context.Context,
	databasePath string,
	extraExcludedRoot string,
	consume func(LegacyDiscoveryObservation),
) {
	if ctx.Err() != nil {
		consume(LegacyDiscoveryNone)
		return
	}
	dbPath := strings.TrimSpace(databasePath)
	if dbPath == "" {
		consume(LegacyDiscoveryNone)
		return
	}
	info, lstatErr := os.Lstat(dbPath)
	if lstatErr != nil {
		if errors.Is(lstatErr, os.ErrPermission) && ctx.Err() == nil {
			consume(LegacyDiscoveryOwnershipUnavailable)
			return
		}
		consume(LegacyDiscoveryNone)
		return
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 {
		consume(LegacyDiscoveryNone)
		return
	}

	lock, lockErr := processlock.AcquireExistingReadOnly(dbPath)
	if lockErr != nil {
		if errors.Is(lockErr, os.ErrPermission) && ctx.Err() == nil && isSQLiteHeaderOrPermissionBlocked(dbPath, info) {
			consume(LegacyDiscoveryOwnershipUnavailable)
			return
		}
		consume(LegacyDiscoveryNone)
		return
	}
	defer func() { _ = lock.Close() }()

	if err := ValidateScratchDir("", filepath.Dir(dbPath), strings.TrimSpace(extraExcludedRoot)); err != nil {
		consume(LegacyDiscoveryNone)
		return
	}

	snapshot, snapErr := OpenSnapshot(ctx, dbPath, "")
	if snapErr != nil {
		if errors.Is(snapErr, os.ErrPermission) && ctx.Err() == nil {
			consume(LegacyDiscoveryOwnershipUnavailable)
			return
		}
		consume(LegacyDiscoveryNone)
		return
	}
	observation := classifyLegacySnapshot(ctx, snapshot.DB())
	closeErr := snapshot.Close()
	if closeErr != nil || lock.Validate() != nil || ctx.Err() != nil {
		consume(LegacyDiscoveryNone)
		return
	}
	consume(observation)
}

func isSQLiteHeaderOrPermissionBlocked(databasePath string, inspected os.FileInfo) bool {
	file, err := os.Open(databasePath)
	if err != nil {
		return errors.Is(err, os.ErrPermission)
	}
	defer func() { _ = file.Close() }()
	if err := processlock.OpenedDatabase(file, inspected); err != nil {
		return false
	}
	var header [16]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return false
	}
	return bytes.Equal(header[:], sqliteFileMagic)
}

func classifyLegacySnapshot(ctx context.Context, db *sql.DB) LegacyDiscoveryObservation {
	if err := quickCheckSource(ctx, db); err != nil {
		return LegacyDiscoveryNone
	}
	schema, err := readSourceSchema(ctx, db)
	if err != nil {
		return LegacyDiscoveryNone
	}
	if schema.sourceKind() != SourceKindLegacyManager || len(schema.v2OnlyTables()) != 0 {
		return LegacyDiscoveryNone
	}

	var freshPresent bool
	if err := db.QueryRowContext(ctx, `select exists(select 1 from settings where key = 'fresh_operation_v1')`).Scan(&freshPresent); err != nil || freshPresent {
		return LegacyDiscoveryNone
	}

	rows, err := db.QueryContext(ctx, `select key, value from settings where key in ('bootstrap_state_v1', 'admin_credential_v1')`)
	if err != nil {
		return LegacyDiscoveryNone
	}
	defer rows.Close()

	var bootstrapRaw, adminRaw string
	var bootstrapFound, adminFound bool
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return LegacyDiscoveryNone
		}
		switch key {
		case "bootstrap_state_v1":
			bootstrapFound, bootstrapRaw = true, value
		case "admin_credential_v1":
			adminFound, adminRaw = true, value
		}
	}
	if err := rows.Err(); err != nil {
		return LegacyDiscoveryNone
	}
	if bootstrapFound {
		var state map[string]any
		if err := json.Unmarshal([]byte(bootstrapRaw), &state); err != nil {
			return LegacyDiscoveryNone
		}
	}
	if adminFound {
		var credential model.AdminCredential
		if err := json.Unmarshal([]byte(adminRaw), &credential); err != nil || !security.IsValidAdminCredential(credential) {
			return LegacyDiscoveryRecoveryRequired
		}
		return LegacyDiscoveryUnsupportedUpgrade
	}
	return LegacyDiscoveryRecoveryRequired
}
