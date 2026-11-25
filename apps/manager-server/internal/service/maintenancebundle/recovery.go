package maintenancebundle

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

const rollbackRecoveryMethod = "sqlite-rollback-journal"

// RecoveryEvidence describes a content transformation, not historical identity,
// authenticated provenance, or permission to migrate the original source.
type RecoveryEvidence struct {
	Method         string `json:"method"`
	SourceRevision string `json:"sourceRevision"`
}

func validateRecoveryEvidence(m Manifest) error {
	if m.Recovery != nil && (m.Recovery.Method != rollbackRecoveryMethod || !validDigest(m.Recovery.SourceRevision)) {
		return errors.New("invalid rollback recovery evidence")
	}
	return nil
}

// RecoverCreate is an explicit opt-in for a stopped/isolated rollback-journal
// source. SQLite recovery runs ONLY on a private copy. It neither deletes the
// source journal nor migrates source data. Ordinary Create/RestoreNew continue
// to reject nonempty journals. The cooperating fence is not external isolation.
func RecoverCreate(ctx context.Context, layout Layout, bundleDir string, lock *processlock.Lock) (Manifest, error) {
	return createBundle(ctx, layout, bundleDir, lock, createHooks{}, true)
}

func requireRollbackJournal(l Layout) error {
	journal, err := os.Lstat(l.DatabasePath + "-journal")
	if err != nil {
		return fmt.Errorf("rollback recovery requires an existing nonempty journal: %w", err)
	}
	if !journal.Mode().IsRegular() || journal.Size() == 0 {
		return errors.New("rollback recovery requires a nonempty regular journal")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		info, err := os.Lstat(l.DatabasePath + suffix)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && (!info.Mode().IsRegular() || info.Size() != 0) {
			return errors.New("rollback recovery refuses mixed WAL/SHM and journal inputs")
		}
	}
	return nil
}

func recoverRollbackJournal(ctx context.Context, l Layout) error {
	if err := validateRollbackJournal(ctx, l); err != nil {
		return err
	}
	// mode=rw refuses accidental creation. No production migrations, schema
	// repairs, journal deletion, VACUUM, or journal-mode conversion is performed.
	uriPath := filepath.ToSlash(l.DatabasePath)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := &url.URL{Scheme: "file", Path: uriPath}
	q := u.Query()
	q.Set("mode", "rw")
	q.Add("_pragma", "synchronous(FULL)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	err = func() error {
		// First read triggers SQLite's own hot-journal recovery.
		var tables int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema").Scan(&tables); err != nil {
			return fmt.Errorf("SQLite rollback recovery failed: %w", err)
		}
		rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
		if err != nil {
			return err
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			var result string
			if err := rows.Scan(&result); err != nil {
				return err
			}
			count++
			if strings.TrimSpace(result) != "ok" {
				return errors.New("recovered SQLite integrity check failed")
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if count != 1 {
			return errors.New("recovered SQLite integrity check returned no unique ok result")
		}
		if closeErr := rows.Close(); closeErr != nil {
			return closeErr
		}
		if compatErr := managersqlite.CheckSchemaCompatibility(db); compatErr != nil {
			return compatErr
		}
		return nil
	}()
	err = errors.Join(err, db.Close())
	if err != nil {
		return err
	}
	// A non-hot/invalid journal must never be silently dropped and resealed.
	// SQLite must have consumed it itself. Empty journals are harmless but this
	// explicit recovery operation still requires the original nonempty input.
	if info, err := os.Lstat(l.DatabasePath + "-journal"); err == nil {
		if info.Size() != 0 {
			return errors.New("SQLite did not consume rollback journal; recovery refused")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return ctx.Err()
}

// SQLite may silently ignore an invalid/non-hot journal or stop playback at a
// bad frame while the resulting database still passes integrity_check. Require
// a bounded-memory, fully readable synced rollback-journal subset BEFORE giving
// SQLite a writable copy. Recovery itself remains SQLite's responsibility.
// Format/checksum: SQLite pager readJournalHdr/pager_cksum; no-sync (-1), old
// zero-page-size headers, partial tails and super-journals are refused. An
// unsynced tail requires every preimage to still match its database page exactly.
func validateRollbackJournal(ctx context.Context, l Layout) error {
	f, err := os.Open(l.DatabasePath + "-journal")
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("rollback journal is not regular")
	}
	if err := rejectMultipleLinks(f); err != nil {
		return err
	}
	db, err := os.Open(l.DatabasePath)
	if err != nil {
		return err
	}
	var dbHeader [100]byte
	defer db.Close()
	if _, err := io.ReadFull(db, dbHeader[:]); err != nil {
		return err
	}
	if string(dbHeader[:16]) != "SQLite format 3\x00" {
		return errors.New("invalid rollback database header")
	}
	pageSize := uint32(binary.BigEndian.Uint16(dbHeader[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	var committedHeader [100]byte
	copy(committedHeader[:], dbHeader[:])
	seenPage1Preimage := false
	magic := []byte{0xd9, 0xd5, 0x05, 0xf9, 0x20, 0xa1, 0x63, 0xd7}
	var offset int64
	var initialPages, sectorSize uint32
	for offset < info.Size() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var header [28]byte
		if _, err := f.ReadAt(header[:], offset); err != nil {
			return fmt.Errorf("incomplete rollback header: %w", err)
		}
		unsynced := offset > 0 && bytes.Equal(header[:12], make([]byte, 12))
		if !bytes.Equal(header[:8], magic) && !unsynced {
			return fmt.Errorf("unsupported or unsynced rollback journal header at offset %d", offset)
		}
		n := binary.BigEndian.Uint32(header[8:12])
		nonce := binary.BigEndian.Uint32(header[12:16])
		pages := binary.BigEndian.Uint32(header[16:20])
		sector := binary.BigEndian.Uint32(header[20:24])
		size := binary.BigEndian.Uint32(header[24:28])
		if (n == 0 && !unsynced) || n == 0xffffffff || pages == 0 || sector < 32 || sector > 65536 || sector&(sector-1) != 0 || size < 512 || size > 65536 || size&(size-1) != 0 || size != pageSize {
			return errors.New("unsupported rollback journal geometry or unsynced record count")
		}
		if offset == 0 {
			initialPages, sectorSize = pages, sector
		} else if pages != initialPages || sector != sectorSize {
			return errors.New("rollback journal headers disagree")
		}
		start := offset + int64(sector)
		frameSize := int64(size) + 8
		if unsynced {
			tail := info.Size() - start
			if tail <= 0 || tail%frameSize != 0 || tail/frameSize > int64(^uint32(0)) {
				return errors.New("partial unsynced rollback tail")
			}
			n = uint32(tail / frameSize)
		}
		if start > info.Size() || int64(n) > (info.Size()-start)/frameSize {
			return errors.New("truncated rollback journal records")
		}
		frame := make([]byte, int(frameSize))
		for i := uint32(0); i < n; i++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := f.ReadAt(frame, start+int64(i)*frameSize); err != nil {
				return err
			}
			page := binary.BigEndian.Uint32(frame[:4])
			if page == 0 || page > pages {
				return errors.New("unsupported rollback page or super-journal")
			}
			checksum := nonce
			for at := int(size) - 200; at > 0; at -= 200 {
				checksum += uint32(frame[4+at])
			}
			if checksum != binary.BigEndian.Uint32(frame[4+int(size):]) {
				return errors.New("rollback journal page checksum mismatch")
			}
			if page == 1 && !seenPage1Preimage {
				copy(committedHeader[:], frame[4:104])
				seenPage1Preimage = true
			}
			if unsynced {
				current := make([]byte, int(size))
				if _, err := db.ReadAt(current, int64(page-1)*int64(size)); err != nil {
					return err
				}
				if !bytes.Equal(current, frame[4:4+int(size)]) {
					return errors.New("unsynced rollback page differs from database; recovery refused")
				}
			}
		}
		end := start + int64(n)*frameSize
		if end == info.Size() {
			userVersion := int64(int32(binary.BigEndian.Uint32(committedHeader[60:64])))
			if userVersion < 0 || userVersion > managersqlite.CurrentSchemaGeneration {
				return fmt.Errorf("%w: user_version=%d outside supported generation 0..%d", managersqlite.ErrIncompatibleNewerSchema, userVersion, managersqlite.CurrentSchemaGeneration)
			}
			return nil
		}
		offset = ((end + int64(sector) - 1) / int64(sector)) * int64(sector)
		if offset >= info.Size() {
			return errors.New("unsupported rollback journal tail")
		}
	}
	return errors.New("empty rollback journal")
}
