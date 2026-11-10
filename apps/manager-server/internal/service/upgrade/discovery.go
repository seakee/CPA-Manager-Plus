package upgrade

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
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

// LegacySourceIdentity describes one state of a source database as a read-only
// observation cache key: the database, the journal files it may carry, and the
// ownership marker whose permissions decide whether the source can be fenced at
// all. The POSIX change stamp is part of it because a chown or a chmod is
// exactly the repair the guidance asks the operator to make, and a remembered
// refusal must not outlive it.
//
// The second result is false when the database itself is absent or its metadata
// cannot be described: a source with no database is cheap to re-inspect, its
// tree can change without any of these files changing, and an in-place rewrite
// that restored the modification time must never be mistaken for an unchanged
// file.
func LegacySourceIdentity(databasePath string) (string, bool) {
	databasePath = strings.TrimSpace(databasePath)
	if databasePath == "" {
		return "", false
	}
	if _, err := os.Lstat(databasePath); err != nil {
		return "", false
	}
	var identity strings.Builder
	for _, path := range []string{
		databasePath,
		databasePath + "-wal",
		databasePath + "-shm",
		databasePath + "-journal",
		databasePath + ".manager.lock",
	} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			identity.WriteString(filepath.Base(path))
			identity.WriteString("=absent;")
			continue
		}
		if err != nil {
			return "", false
		}
		stamp, err := copyinmarker.ChangeStamp(path)
		if err != nil {
			return "", false
		}
		fmt.Fprintf(&identity, "%s=%s:%d:%s;", filepath.Base(path), info.Mode(), info.Size(), stamp)
	}
	return identity.String(), true
}

// InspectReadOnlyLegacySource inspects a stopped 1.x source through a shared
// read-only ownership hold and a private scratch copy outside the database's
// directory, the data-key directory, and extraExcludedRoot. The verdict is handed
// to consume before the hold is released, so the answer is written while
// quiescence is still proved.
//
// The hold is shared rather than exclusive: this is the one unauthenticated read
// of the surface, and two concurrent observations of the same stopped source must
// be able to prove the same thing at the same time. A live writer holds an
// exclusive fence and still excludes both.
//
// The second result reports whether that verdict is a statement about the
// source's content — which a caller may reuse while LegacySourceIdentity is
// unchanged — rather than a report about this moment's contention or this
// request's deadline.
func InspectReadOnlyLegacySource(
	ctx context.Context,
	source PreservedSource,
	extraExcludedRoot string,
	consume func(LegacyDiscoveryObservation),
) (LegacyDiscoveryObservation, bool) {
	if ctx.Err() != nil {
		consume(LegacyDiscoveryNone)
		return LegacyDiscoveryNone, false
	}
	dbPath := strings.TrimSpace(source.Database)
	if dbPath == "" {
		consume(LegacyDiscoveryNone)
		return LegacyDiscoveryNone, false
	}
	info, lstatErr := os.Lstat(dbPath)
	if lstatErr != nil {
		// A directory this process may not search says nothing about what it
		// holds: an unreadable path is not evidence of a 1.x volume, so it stays
		// the fail-closed answer instead of a host-intervention instruction.
		consume(LegacyDiscoveryNone)
		return LegacyDiscoveryNone, false
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 {
		consume(LegacyDiscoveryNone)
		return LegacyDiscoveryNone, false
	}

	lock, lockErr := processlock.AcquireExistingShared(dbPath)
	if lockErr != nil {
		if errors.Is(lockErr, os.ErrPermission) {
			// A marker this process may not open is not proof of anything by
			// itself: a native v2 store left behind by a root member (0644
			// database, 0600 marker) answers EACCES exactly like a root-owned
			// 1.x volume. Only the database can tell the two apart, and it can
			// be read without the marker.
			// An unfenceable verdict is never marked reusable: cachedProjection
			// requires ProbeExisting to prove quiescence, which an unreadable
			// marker fails by definition, and classifyUnfenceableSource reads
			// only the header and immutable catalogue without copying the store.
			verdict := classifyUnfenceableSource(ctx, dbPath, info)
			consume(verdict)
			return verdict, false
		}
		// A busy marker is this source being written, and a missing one is a
		// source that cannot prove exclusivity: neither is a fact about the
		// content, so neither is remembered.
		consume(LegacyDiscoveryNone)
		return LegacyDiscoveryNone, false
	}
	defer func() { _ = lock.Close() }()

	if err := ValidatePreservedScratchDir("", source, ScratchRoot{Label: "the configured source root", Dir: extraExcludedRoot}); err != nil {
		consume(LegacyDiscoveryNone)
		return LegacyDiscoveryNone, false
	}

	snapshot, snapErr := OpenSnapshot(ctx, dbPath, "")
	if snapErr != nil {
		// Nothing this stage can fail on is a fact about the ownership marker: the
		// marker is held. A permission error here belongs to the database or one of
		// its sidecars — the very files the verdict is about — so it stays the
		// fail-closed answer instead of being projected as the one condition the
		// operator can repair with a chown. Only classifyUnfenceableSource, which
		// proves a readable 1.x store whose marker alone is out of reach, may say
		// "ownership unavailable".
		consume(LegacyDiscoveryNone)
		return LegacyDiscoveryNone, false
	}
	observation := classifyLegacySnapshot(ctx, snapshot.DB())
	closeErr := snapshot.Close()
	if closeErr != nil || lock.Validate() != nil || ctx.Err() != nil {
		consume(LegacyDiscoveryNone)
		return LegacyDiscoveryNone, false
	}
	consume(observation)
	return observation, true
}

// classifyUnfenceableSource answers for a database whose ownership marker this
// process may not open. No fence can be taken, so nothing is copied, locked or
// created: the file is read through an immutable handle, which ignores any
// journal beside it instead of joining it, and only the schema and the one
// operation marker decide between the two conditions that look identical from
// the outside. A store v2 has already opened — its own tables, or the Fresh
// operation marker the v2 image writes at the start of its first boot — is
// never presented as a 1.x source; a 1.x store whose files are root-owned is the
// condition the host has to repair.
func classifyUnfenceableSource(ctx context.Context, databasePath string, inspected os.FileInfo) LegacyDiscoveryObservation {
	if ctx.Err() != nil || !readableSQLiteHeader(databasePath, inspected) {
		return LegacyDiscoveryNone
	}
	// The database is not the only file this verdict speaks for. A sidecar beside
	// it that is a link, a FIFO or a second name for another file describes a
	// store no read of the file alone can account for, so it is refused instead of
	// being projected as the ownership condition of a 1.x volume.
	if err := AuditSourceSidecars(databasePath); err != nil {
		return LegacyDiscoveryNone
	}
	db, err := sql.Open("sqlite", readOnlyDataSourceName(databasePath, true))
	if err != nil {
		return LegacyDiscoveryNone
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() { _ = db.Close() }()
	// sourceKind is SourceKindLegacyManager only when the v2-only tables are
	// absent, so a native v2 schema can never reach the ownership verdict.
	schema, err := readSourceSchema(ctx, db)
	if err != nil || schema.sourceKind() != SourceKindLegacyManager {
		return LegacyDiscoveryNone
	}
	var freshPresent bool
	if err := db.QueryRowContext(ctx, `select exists(select 1 from settings where key = 'fresh_operation_v1')`).Scan(&freshPresent); err != nil || freshPresent {
		return LegacyDiscoveryNone
	}
	return LegacyDiscoveryOwnershipUnavailable
}

func readableSQLiteHeader(databasePath string, inspected os.FileInfo) bool {
	file, err := os.Open(databasePath)
	if err != nil {
		return false
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
