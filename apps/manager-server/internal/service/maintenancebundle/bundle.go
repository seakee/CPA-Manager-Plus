// Package maintenancebundle inventories and copies complete offline Manager
// file sets. Its manifests establish content integrity, never release identity,
// writer provenance, or upgrade authorization.
package maintenancebundle

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagearchive"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/usage"
	_ "modernc.org/sqlite"
)

const Version = 2
const CooperatingFence = "cooperating-manager-lock"
const maxManifestBytes = 16 * 1024 * 1024

// Matches the production usage archive manifest bound.
const maxArchiveManifestBytes = 4 * 1024 * 1024
const maxEntries = 10000

// Layout makes external config and archive requirements explicit. Database and
// key must exist. Optional missing config/archive roots still get manifest entries.
type Layout struct {
	DatabasePath    string
	DataKeyPath     string
	ConfigPath      string
	ArchivesPath    string
	RequireConfig   bool
	RequireArchives bool
}

type Entry struct {
	Present   bool   `json:"present"`
	Directory bool   `json:"directory,omitempty"`
	Size      int64  `json:"size,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
}

type Manifest struct {
	Version        int               `json:"version"`
	Fence          string            `json:"fence"`
	SourceRevision string            `json:"sourceRevision"`
	Entries        map[string]Entry  `json:"entries"`
	Recovery       *RecoveryEvidence `json:"recovery,omitempty"`
}

// BundleLayout returns storage paths shared by backup, restore and maintenance
// execution. The database filename is deliberately independent of source paths.
func BundleLayout(root string) Layout {
	return Layout{DatabasePath: filepath.Join(root, "database"), DataKeyPath: filepath.Join(root, "data-key"), ConfigPath: filepath.Join(root, "config"), ArchivesPath: filepath.Join(root, "usage-archives")}
}

func (l Layout) paths() map[string]string {
	return map[string]string{"database": l.DatabasePath, "database-wal": l.DatabasePath + "-wal", "database-shm": l.DatabasePath + "-shm", "database-journal": l.DatabasePath + "-journal", "data-key": l.DataKeyPath, "config": l.ConfigPath, "usage-archives": l.ArchivesPath}
}

func normalize(l Layout) (Layout, error) {
	paths := []*string{&l.DatabasePath, &l.DataKeyPath, &l.ConfigPath, &l.ArchivesPath}
	for _, p := range paths {
		if strings.TrimSpace(*p) == "" {
			return Layout{}, errors.New("all Manager bundle paths must be explicit")
		}
		a, err := filepath.Abs(*p)
		if err != nil {
			return Layout{}, err
		}
		*p = a
		if err := safePath(a); err != nil {
			return Layout{}, err
		}
	}
	items := l.paths()
	for name, a := range items {
		for other, b := range items {
			if name == other {
				continue
			}
			physical, err := physicalOverlap(a, b)
			if err != nil {
				return Layout{}, err
			}
			if overlaps(a, b) || physical {
				return Layout{}, fmt.Errorf("Manager bundle paths overlap: %s and %s", name, other)
			}
		}
	}
	return l, nil
}

// String paths alone miss case aliases on case-insensitive filesystems. Compare
// an existing root with the other path and its ancestors without inferring any
// source identity from this filesystem witness.
func physicalOverlap(a, b string) (bool, error) {
	info, err := os.Lstat(a)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for p := b; ; p = filepath.Dir(p) {
		other, err := os.Lstat(p)
		if err != nil && !os.IsNotExist(err) {
			return false, err
		}
		if err == nil && os.SameFile(info, other) {
			return true, nil
		}
		if p == filepath.Dir(p) {
			return false, nil
		}
	}
}

func overlaps(a, b string) bool {
	if runtime.GOOS == "windows" {
		a = strings.ToLower(a)
		b = strings.ToLower(b)
	}
	if samePath(a, b) {
		return true
	}
	r, err := filepath.Rel(a, b)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func layoutProtectedRoots(l Layout) []string {
	var roots []string
	if strings.TrimSpace(l.DatabasePath) != "" {
		roots = append(roots, filepath.Dir(l.DatabasePath))
	}
	if strings.TrimSpace(l.DataKeyPath) != "" {
		roots = append(roots, filepath.Dir(l.DataKeyPath))
	}
	if strings.TrimSpace(l.ConfigPath) != "" {
		roots = append(roots, filepath.Dir(l.ConfigPath))
	}
	if strings.TrimSpace(l.ArchivesPath) != "" {
		roots = append(roots, l.ArchivesPath)
	}
	return roots
}

func canonicalScratchAncestor(path string) string {
	cleaned := filepath.Clean(path)
	var suffix []string
	cur := cleaned
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return cleaned
		}
		suffix = append(suffix, filepath.Base(cur))
		cur = parent
	}
}

func resolveValidatedScratchBase(scratchDir string, protectedRoots ...string) (string, error) {
	base := strings.TrimSpace(scratchDir)
	if base == "" {
		base = os.TempDir()
	}
	if strings.TrimSpace(base) == "" {
		return "", errors.New("scratch directory is required")
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("resolve scratch directory: %w", err)
	}
	canonBase := canonicalScratchAncestor(absBase)
	for _, rawRoot := range protectedRoots {
		trimmed := strings.TrimSpace(rawRoot)
		if trimmed == "" {
			continue
		}
		absRoot, err := filepath.Abs(trimmed)
		if err != nil {
			return "", fmt.Errorf("resolve protected root: %w", err)
		}
		canonRoot := canonicalScratchAncestor(absRoot)
		if overlaps(absRoot, absBase) || overlaps(canonRoot, canonBase) {
			return "", fmt.Errorf("scratch directory %s must not sit inside protected root %s", absBase, absRoot)
		}
		physical, err := physicalOverlap(absRoot, absBase)
		if err != nil {
			return "", err
		}
		if physical {
			return "", fmt.Errorf("scratch directory %s physically overlaps protected root %s", absBase, absRoot)
		}
		physicalCanon, err := physicalOverlap(canonRoot, canonBase)
		if err != nil {
			return "", err
		}
		if physicalCanon {
			return "", fmt.Errorf("scratch directory %s physically overlaps protected root %s", absBase, absRoot)
		}
	}
	return absBase, nil
}

func computeSourceRevision(entries map[string]Entry) string {
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		e := entries[k]
		fmt.Fprintf(h, "%s:%t:%t:%d:%s\n", k, e.Present, e.Directory, e.Size, e.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Inspect reads every byte, including all nested archive files. Directory and
// missing entries make additions/deletions part of revalidation. It does not
// itself establish a snapshot or a quiescence boundary.
func Inspect(ctx context.Context, layout Layout) (Manifest, error) {
	l, err := normalize(layout)
	if err != nil {
		return Manifest{}, err
	}
	return inspect(ctx, l)
}

func inspect(ctx context.Context, l Layout) (Manifest, error) {
	m := Manifest{Version: Version, Entries: map[string]Entry{}}
	for name, p := range l.paths() {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		info, err := os.Lstat(p)
		if os.IsNotExist(err) {
			if name == "database" || name == "data-key" || (name == "config" && l.RequireConfig) || (name == "usage-archives" && l.RequireArchives) {
				return Manifest{}, fmt.Errorf("required Manager bundle entry missing: %s", name)
			}
			m.Entries[name] = Entry{}
			continue
		}
		if err != nil {
			return Manifest{}, err
		}
		if name != "usage-archives" {
			e, err := digestFile(ctx, p, nil)
			if err != nil {
				return Manifest{}, fmt.Errorf("inspect %s: %w", name, err)
			}
			m.Entries[name] = e
			continue
		}
		if !info.IsDir() {
			return Manifest{}, errors.New("usage-archives must be a directory")
		}
		err = filepath.WalkDir(p, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(m.Entries) >= maxEntries {
				return errors.New("Manager bundle archive inventory exceeds supported bound")
			}
			if err := safePath(path); err != nil {
				return err
			}
			r, err := filepath.Rel(p, path)
			if err != nil {
				return err
			}
			if !utf8.ValidString(r) {
				return errors.New("archive filename cannot be represented in the bundle manifest")
			}
			name := "usage-archives"
			if r != "." {
				name += "/" + filepath.ToSlash(r)
			}
			if d.IsDir() {
				m.Entries[name] = Entry{Present: true, Directory: true}
				return nil
			}
			e, err := digestFile(ctx, path, nil)
			if err != nil {
				return err
			}
			m.Entries[name] = e
			return nil
		})
		if err != nil {
			return Manifest{}, fmt.Errorf("inspect archives: %w", err)
		}
	}
	m.SourceRevision = computeSourceRevision(m.Entries)
	return m, nil
}

func Revalidate(ctx context.Context, layout Layout, expected Manifest) error {
	if expected.Version != Version || expected.SourceRevision == "" {
		return errors.New("unsupported Manager bundle manifest")
	}
	current, err := Inspect(ctx, layout)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current.Entries, expected.Entries) {
		return errors.New("Manager bundle contents changed")
	}
	if current.SourceRevision != expected.SourceRevision {
		return errors.New("Manager bundle source revision mismatch")
	}
	return nil
}

// RevalidateSource verifies that the source files still match the authorized
// bundle manifest. A changed source invalidates the previous authorization.
func RevalidateSource(ctx context.Context, layout Layout, expected Manifest) error {
	return Revalidate(ctx, layout, expected)
}

func validateWALHeader(walPath string, expectedPageSize uint32) error {
	wal, err := os.Open(walPath)
	if err != nil {
		return err
	}
	defer wal.Close()
	var header [32]byte
	if _, err := io.ReadFull(wal, header[:]); err != nil {
		return err
	}
	magic := binary.BigEndian.Uint32(header[:4])
	if magic != 0x377f0682 && magic != 0x377f0683 {
		return errors.New("invalid WAL header magic")
	}
	if binary.BigEndian.Uint32(header[4:8]) != 3007000 {
		return errors.New("unsupported WAL file format version")
	}
	walPageSize := binary.BigEndian.Uint32(header[8:12])
	if walPageSize < 512 || walPageSize > 65536 || walPageSize&(walPageSize-1) != 0 {
		return fmt.Errorf("WAL header contains invalid page size %d", walPageSize)
	}
	if expectedPageSize != 0 && walPageSize != expectedPageSize {
		return fmt.Errorf("WAL page size %d does not match database page size %d", walPageSize, expectedPageSize)
	}
	var order binary.ByteOrder = binary.LittleEndian
	if magic&1 != 0 {
		order = binary.BigEndian
	}
	var s1, s2 uint32
	for i := 0; i < 24; i += 8 {
		s1 += order.Uint32(header[i:i+4]) + s2
		s2 += order.Uint32(header[i+4:i+8]) + s1
	}
	if s1 != binary.BigEndian.Uint32(header[24:28]) || s2 != binary.BigEndian.Uint32(header[28:32]) {
		return errors.New("invalid WAL header checksum")
	}
	return nil
}

func sqliteURI(path string) string {
	clean := filepath.ToSlash(filepath.Clean(path))
	if !strings.HasPrefix(clean, "/") {
		clean = "/" + clean
	}
	dsn := &url.URL{Scheme: "file", Path: clean}
	q := dsn.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	dsn.RawQuery = q.Encode()
	return dsn.String()
}

// ValidateMatchingEvidence verifies cross-file coherence:
//  1. Database passes header/page-size, quick_check and foreign_key_check gates.
//  2. If WAL exists, WAL header and checksum match database page size.
//  3. Data key is a valid 32-byte key.
//  4. Data key decrypts both persisted CPA connection JSON settings, subject to
//     their existing connection-storage migration invariant.
//  5. Config is valid JSON if present.
//  6. Every archive segment has a supported status, safe path, and matches its
//     DB-recorded SHA-256 and compressed byte count. Recorded run manifests also
//     match their DB digest within the production size bound. No provenance is
//     authenticated. Published manifests and payloads pass the production reader.
//  7. Persisted archive run contracts and maintenance locks match production
//     stages. A separate writable copy passes production open/migration and
//     archive reader queries; only the original file set is ever published.
func ValidateMatchingEvidence(ctx context.Context, l Layout) error {
	return ValidateMatchingEvidenceWithScratch(ctx, l, "")
}

// ValidateMatchingEvidenceWithScratch validates l using scratchDir as the base
// directory for transient SQLite inspection copies. Empty scratchDir uses
// os.TempDir.
func ValidateMatchingEvidenceWithScratch(ctx context.Context, l Layout, scratchDir string) error {
	return validateMatchingEvidence(ctx, l, scratchDir)
}

func validateMatchingEvidence(ctx context.Context, l Layout, scratchDir string, extraProtectedRoots ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(l.DatabasePath) == "" {
		return errors.New("database path is required")
	}
	protectedRoots := append(layoutProtectedRoots(l), extraProtectedRoots...)
	scratchBase, err := resolveValidatedScratchBase(scratchDir, protectedRoots...)
	if err != nil {
		return err
	}
	info, err := os.Lstat(l.DatabasePath)
	if err != nil {
		return fmt.Errorf("inspect database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("database is not a regular file")
	}
	// A nonempty rollback journal can change the logical DB on its first open.
	// This slice qualifies offline WAL inputs, not rollback-journal recovery;
	// never validate only the DB bytes and later publish an ignored journal.
	if journalInfo, err := os.Lstat(l.DatabasePath + "-journal"); err == nil {
		if !journalInfo.Mode().IsRegular() {
			return errors.New("database rollback journal is not a regular file")
		}
		if journalInfo.Size() > 0 {
			return errors.New("rollback journal recovery is not qualified for maintenance bundle")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect database rollback journal: %w", err)
	}

	walPath := l.DatabasePath + "-wal"
	if info.Size() == 0 {
		walInfo, err := os.Lstat(walPath)
		if os.IsNotExist(err) {
			return errors.New("database is too small to be a valid SQLite file")
		}
		if err != nil {
			return fmt.Errorf("inspect database WAL: %w", err)
		}
		if !walInfo.Mode().IsRegular() {
			return errors.New("database WAL is not a regular file")
		}
		if walInfo.Size() <= 0 {
			return errors.New("database is too small to be a valid SQLite file")
		}
		if err := validateWALHeader(walPath, 0); err != nil {
			return fmt.Errorf("database WAL validation failed: %w", err)
		}
	} else {
		if info.Size() < 100 {
			return errors.New("database is too small to be a valid SQLite file")
		}
		f, err := os.Open(l.DatabasePath)
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		var header [100]byte
		_, err = io.ReadFull(f, header[:])
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("read database header: %w", err)
		}
		if string(header[:16]) != "SQLite format 3\x00" {
			return errors.New("database is not a valid SQLite database file")
		}
		pageSize := uint32(binary.BigEndian.Uint16(header[16:18]))
		if pageSize == 1 {
			pageSize = 65536
		}
		if pageSize < 512 || pageSize > 65536 || pageSize&(pageSize-1) != 0 {
			return errors.New("database header contains invalid page size")
		}
		if walInfo, err := os.Lstat(walPath); err == nil {
			if !walInfo.Mode().IsRegular() {
				return errors.New("database WAL is not a regular file")
			}
			if walInfo.Size() > 0 {
				if err := validateWALHeader(walPath, pageSize); err != nil {
					return fmt.Errorf("database WAL validation failed: %w", err)
				}
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect database WAL: %w", err)
		}
	}
	if strings.TrimSpace(l.DataKeyPath) == "" {
		return errors.New("data key path is required")
	}
	keyInfo, err := os.Lstat(l.DataKeyPath)
	if err != nil {
		return fmt.Errorf("inspect data key: %w", err)
	}
	if !keyInfo.Mode().IsRegular() {
		return errors.New("data key is not a regular file")
	}
	if strings.TrimSpace(l.ConfigPath) != "" {
		if info, err := os.Lstat(l.ConfigPath); err == nil {
			if !info.Mode().IsRegular() {
				return errors.New("config is not a regular file")
			}
			data, err := os.ReadFile(l.ConfigPath)
			if err != nil {
				return fmt.Errorf("read config: %w", err)
			}
			var js any
			if err := json.Unmarshal(data, &js); err != nil {
				return fmt.Errorf("config is not valid JSON: %w", err)
			}
			if _, ok := js.(map[string]any); !ok {
				return errors.New("config JSON root must be an object")
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect config: %w", err)
		} else if l.RequireConfig {
			return errors.New("required config is missing")
		}
	} else if l.RequireConfig {
		return errors.New("required config path is missing")
	}

	// Stage SQLite files into a dedicated scratch directory for driver inspection
	// so opening SQLite driver never touches or mutates the layout paths or WAL.
	scratch, err := os.MkdirTemp(scratchBase, "cpamp-matching-staging-")
	if err != nil {
		return fmt.Errorf("create matching inspection scratch: %w", err)
	}
	defer os.RemoveAll(scratch)
	absScratch, err := filepath.Abs(scratch)
	if err != nil {
		return fmt.Errorf("resolve matching inspection scratch: %w", err)
	}
	canonicalScratch, err := filepath.EvalSymlinks(absScratch)
	if err != nil {
		return fmt.Errorf("resolve matching inspection scratch: %w", err)
	}
	scratch = canonicalScratch

	// Never invoke load-or-create on the source: a disappearing key must not
	// cause inspection to write a replacement into the original file set.
	scratchKey := filepath.Join(scratch, "data-key")
	if err := copyFile(ctx, l.DataKeyPath, scratchKey); err != nil {
		return fmt.Errorf("stage data key for inspection: %w", err)
	}
	rawKey, created, err := security.LoadOrCreateDataKey("", scratchKey)
	if err != nil {
		return fmt.Errorf("invalid data key: %w", err)
	}
	if created {
		return errors.New("data key disappeared during inspection")
	}

	scratchDB := filepath.Join(scratch, "database")
	if err := copyFile(ctx, l.DatabasePath, scratchDB); err != nil {
		return fmt.Errorf("stage database for inspection: %w", err)
	}
	if _, err := os.Lstat(walPath); err == nil {
		if err := copyFile(ctx, walPath, scratchDB+"-wal"); err != nil {
			return fmt.Errorf("stage WAL for inspection: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect database WAL: %w", err)
	}
	shmPath := l.DatabasePath + "-shm"
	if _, err := os.Lstat(shmPath); err == nil {
		if err := copyFile(ctx, shmPath, scratchDB+"-shm"); err != nil {
			return fmt.Errorf("stage SHM for inspection: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect database SHM: %w", err)
	}
	if info.Size() == 0 {
		if err := replayZeroByteScratchWAL(ctx, scratchDB); err != nil {
			return fmt.Errorf("replay zero-byte database WAL in scratch: %w", err)
		}
	}

	dsn := sqliteURI(scratchDB)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	var pageCount int
	if err := db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount); err != nil {
		return fmt.Errorf("SQLite page_count query failed: %w", err)
	}
	if pageCount < 1 {
		return errors.New("database WAL contains no committed database pages")
	}

	var quickCheck string
	row := db.QueryRowContext(ctx, "PRAGMA quick_check(1)")
	if err := row.Scan(&quickCheck); err != nil {
		return fmt.Errorf("SQLite integrity check query failed: %w", err)
	}
	if quickCheck != "ok" {
		return fmt.Errorf("SQLite integrity check failed: %s", quickCheck)
	}
	if err := validateForeignKeys(ctx, db); err != nil {
		return err
	}

	// Reuse the production raw-storage guard on the scratch database before any
	// protected read can reinterpret a malformed post-migration value as plaintext.
	if _, err := managersqlite.InspectPersistedCPAConnectionStorage(ctx, scratchDB); err != nil {
		return fmt.Errorf("inspect persisted CPA connection storage: %w", err)
	}
	hasSettings, err := tableExists(ctx, db, "settings")
	if err != nil {
		return err
	}
	if hasSettings {
		protector, err := security.NewProtector(rawKey)
		if err != nil {
			return fmt.Errorf("create data key protector: %w", err)
		}
		repo := setting.New(db, protector)
		if _, _, err := repo.LoadSetup(ctx); err != nil {
			return fmt.Errorf("data key/settings setup matching failed: %w", err)
		}
		if _, _, err := repo.LoadManagerConfig(ctx); err != nil {
			return fmt.Errorf("data key/settings manager_config_v1 matching failed: %w", err)
		}
	}
	if err := validateArchiveEvidence(ctx, db, l); err != nil {
		return err
	}
	return validateProductionSQLiteOpen(ctx, scratchDB, scratch)
}

func extractCommittedPage1FromWAL(walPath string) ([]byte, error) {
	wal, err := os.Open(walPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = wal.Close() }()
	var header [32]byte
	if _, err := io.ReadFull(wal, header[:]); err != nil {
		return nil, err
	}
	magic := binary.BigEndian.Uint32(header[:4])
	if magic != 0x377f0682 && magic != 0x377f0683 {
		return nil, errors.New("invalid WAL header magic")
	}
	if binary.BigEndian.Uint32(header[4:8]) != 3007000 {
		return nil, errors.New("unsupported WAL file format version")
	}
	pageSize := binary.BigEndian.Uint32(header[8:12])
	if pageSize < 512 || pageSize > 65536 || pageSize&(pageSize-1) != 0 {
		return nil, fmt.Errorf("invalid WAL page size %d", pageSize)
	}
	var order binary.ByteOrder = binary.LittleEndian
	if magic&1 != 0 {
		order = binary.BigEndian
	}
	checksum := func(buf []byte, s1, s2 uint32) (uint32, uint32) {
		for i := 0; i < len(buf); i += 8 {
			s1 += order.Uint32(buf[i:i+4]) + s2
			s2 += order.Uint32(buf[i+4:i+8]) + s1
		}
		return s1, s2
	}
	s1, s2 := checksum(header[:24], 0, 0)
	if s1 != binary.BigEndian.Uint32(header[24:28]) || s2 != binary.BigEndian.Uint32(header[28:32]) {
		return nil, errors.New("invalid WAL header checksum")
	}
	salt1 := binary.BigEndian.Uint32(header[16:20])
	salt2 := binary.BigEndian.Uint32(header[20:24])
	frame := make([]byte, 24+int(pageSize))
	var pendingPage1 []byte
	var committedPage1 []byte
	for {
		_, err := io.ReadFull(wal, frame)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if binary.BigEndian.Uint32(frame[8:12]) != salt1 || binary.BigEndian.Uint32(frame[12:16]) != salt2 {
			break
		}
		s1, s2 = checksum(frame[:8], s1, s2)
		s1, s2 = checksum(frame[24:], s1, s2)
		if s1 != binary.BigEndian.Uint32(frame[16:20]) || s2 != binary.BigEndian.Uint32(frame[20:24]) {
			break
		}
		pageNumber := binary.BigEndian.Uint32(frame[:4])
		if pageNumber == 0 {
			return nil, errors.New("invalid WAL frame page number")
		}
		if pageNumber == 1 {
			if pendingPage1 == nil {
				pendingPage1 = make([]byte, int(pageSize))
			}
			copy(pendingPage1, frame[24:])
		}
		if binary.BigEndian.Uint32(frame[4:8]) > 0 && pendingPage1 != nil {
			if committedPage1 == nil {
				committedPage1 = make([]byte, int(pageSize))
			}
			copy(committedPage1, pendingPage1)
		}
	}
	if committedPage1 == nil {
		return nil, errors.New("database WAL contains no committed database pages")
	}
	if string(committedPage1[:16]) != "SQLite format 3\x00" {
		return nil, errors.New("database WAL page 1 is not a valid SQLite header")
	}
	dbPageSize := uint32(binary.BigEndian.Uint16(committedPage1[16:18]))
	if dbPageSize == 1 {
		dbPageSize = 65536
	}
	if dbPageSize != pageSize {
		return nil, fmt.Errorf("WAL page size %d does not match page-1 database page size %d", pageSize, dbPageSize)
	}
	return committedPage1, nil
}

func replayZeroByteScratchWAL(ctx context.Context, scratchDB string) error {
	page1, err := extractCommittedPage1FromWAL(scratchDB + "-wal")
	if err != nil {
		return err
	}
	if err := os.Remove(scratchDB + "-shm"); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.WriteFile(scratchDB, page1, 0o600); err != nil {
		return err
	}
	rawDB, err := sql.Open("sqlite", scratchDB)
	if err != nil {
		return err
	}
	rawDB.SetMaxOpenConns(1)
	_, checkpointErr := rawDB.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	closeErr := rawDB.Close()
	if checkpointErr != nil {
		return checkpointErr
	}
	if closeErr != nil {
		return closeErr
	}
	replayedInfo, err := os.Lstat(scratchDB)
	if err != nil {
		return err
	}
	if replayedInfo.Size() < 100 {
		return errors.New("database WAL contains no committed database pages")
	}
	return nil
}

func validateProductionSQLiteOpen(ctx context.Context, sourceDB, scratch string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Migrate can repair/create schema and write WAL. Give the real production
	// opener a second, writable copy; inspection above sees the original snapshot.
	dir := filepath.Join(scratch, "production-open")
	if err := os.Mkdir(dir, 0o700); err != nil {
		return fmt.Errorf("create production SQLite open scratch: %w", err)
	}
	candidateDB := filepath.Join(dir, "database")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Lstat(sourceDB + suffix); os.IsNotExist(err) && suffix != "" {
			continue
		} else if err != nil {
			return fmt.Errorf("inspect production SQLite open input: %w", err)
		}
		if err := copyFile(ctx, sourceDB+suffix, candidateDB+suffix); err != nil {
			return fmt.Errorf("stage production SQLite open input: %w", err)
		}
	}
	db, err := managersqlite.Open(candidateDB)
	if err != nil {
		return fmt.Errorf("production SQLite open/migration failed: %w", err)
	}
	defer db.Close()
	if err := validateForeignKeys(ctx, db); err != nil {
		return fmt.Errorf("production SQLite open/migration integrity: %w", err)
	}
	// Decode real persisted rows with production scanners, including all fields
	// outside the smaller matching-evidence queries. Empty tables still compile
	// the maintained reader SQL rather than a separate schema definition.
	repo := usagearchive.New(db)
	if err := repo.ValidatePersistedRows(ctx); err != nil {
		return fmt.Errorf("production SQLite open/migration archive persisted rows: %w", err)
	}
	if err := repo.ValidateDataPlaneSchema(ctx); err != nil {
		return fmt.Errorf("production SQLite open/migration archive data-plane: %w", err)
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close production SQLite open/migration: %w", err)
	}
	return ctx.Err()
}

func validateForeignKeys(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("query SQLite foreign_key_check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var rowID sql.NullInt64
		var constraintID int64
		if err := rows.Scan(&table, &rowID, &parent, &constraintID); err != nil {
			return fmt.Errorf("scan SQLite foreign_key_check: %w", err)
		}
		return errors.New("SQLite foreign_key_check failed: foreign key violation")
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read SQLite foreign_key_check: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close SQLite foreign_key_check: %w", err)
	}
	return ctx.Err()
}

func tableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var kind string
	err := db.QueryRowContext(ctx, "SELECT type FROM sqlite_schema WHERE name=?", name).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %s table: %w", name, err)
	}
	if kind != "table" {
		return false, fmt.Errorf("inspect %s: persisted object is not a table", name)
	}
	return true, nil
}

func validateArchiveEvidence(ctx context.Context, db *sql.DB, l Layout) error {
	if strings.TrimSpace(l.ArchivesPath) == "" {
		if l.RequireArchives {
			return errors.New("required archive path is missing")
		}
	} else if info, err := os.Lstat(l.ArchivesPath); err == nil {
		if !info.IsDir() {
			return errors.New("archive root is not a directory")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect archive root: %w", err)
	} else if l.RequireArchives {
		return errors.New("required archive root is missing")
	}
	segments := make(map[string]archiveSegmentEvidence)
	if err := validateArchiveSegments(ctx, db, l.ArchivesPath, segments); err != nil {
		return err
	}
	runs := make(map[string]string)
	if err := validateArchiveRunManifests(ctx, db, l.ArchivesPath, segments, runs); err != nil {
		return err
	}
	if err := validateArchiveMaintenanceLocks(ctx, db, runs); err != nil {
		return err
	}
	// The SQL rows above are closed before repository reads: this is a single-
	// connection, read-only snapshot. Never open a writable Store or resume a job.
	repo := usagearchive.New(db)
	for runID := range runs {
		run, err := repo.Run(ctx, runID)
		if err != nil {
			return fmt.Errorf("archive persisted rows payload run: %w", err)
		}
		if run.ManifestFile == "" {
			continue // Legitimate pre-publication/paused/cancelled snapshot.
		}
		segments, err := repo.Segments(ctx, runID)
		if err != nil {
			return fmt.Errorf("archive persisted rows payload segments: %w", err)
		}
		if err := usage.ValidatePublishedArchiveFiles(ctx, l.ArchivesPath, run, segments); err != nil {
			return fmt.Errorf("archive published payload %s: %w", runID, err)
		}
	}
	return ctx.Err()
}

func archiveEvidencePath(root, runID, fileName string) (string, error) {
	// SQLite bytes are untrusted. Re-establish the run/file invariant for both
	// segment and run manifest references, including portable Windows constraints.
	if runID == "" || runID == "." || runID == ".." || path.IsAbs(fileName) ||
		path.Clean(fileName) != fileName || strings.Count(fileName, "/") != 1 ||
		path.Dir(fileName) != runID || path.Base(fileName) == "." || path.Base(fileName) == ".." ||
		strings.ContainsAny(fileName, `\:`) {
		return "", errors.New("invalid archive file name")
	}
	if strings.TrimSpace(root) == "" {
		return "", errors.New("archive path is required for recorded files")
	}
	filePath := filepath.Join(root, filepath.FromSlash(fileName))
	rel, err := filepath.Rel(root, filePath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("archive file escapes archive root")
	}
	return filePath, nil
}

type archiveSegmentEvidence struct {
	published int
	verified  int
}

func validateArchiveSegments(ctx context.Context, db *sql.DB, root string, segments map[string]archiveSegmentEvidence) error {
	hasSegments, err := tableExists(ctx, db, "usage_archive_segments")
	if err != nil {
		return err
	}
	if !hasSegments {
		return nil
	}
	// Do not filter by status: an invalid value must not hide a recorded file.
	rows, err := db.QueryContext(ctx, `SELECT run_id, status, file_name, content_sha256, compressed_bytes, verified_at_ms
		FROM usage_archive_segments`)
	if err != nil {
		return fmt.Errorf("query archive matching evidence: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var runID, status, fileName, expectedSHA string
		var expectedSize int64
		var verifiedAt sql.NullInt64
		if err := rows.Scan(&runID, &status, &fileName, &expectedSHA, &expectedSize, &verifiedAt); err != nil {
			return fmt.Errorf("scan archive matching evidence: %w", err)
		}
		switch status {
		case usagearchive.SegmentStatusPublished:
			if verifiedAt.Valid {
				return errors.New("published archive segment must not have verification timestamp")
			}
		case usagearchive.SegmentStatusVerified:
			if !verifiedAt.Valid || verifiedAt.Int64 <= 0 {
				return errors.New("verified archive segment requires positive verified_at_ms")
			}
		default:
			return errors.New("invalid archive segment status")
		}
		segPath, err := archiveEvidencePath(root, runID, fileName)
		if err != nil {
			return err
		}
		decodedSHA, err := hex.DecodeString(expectedSHA)
		if err != nil || len(decodedSHA) != sha256.Size || expectedSize <= 0 {
			return errors.New("invalid archive segment digest or byte count")
		}
		entry, err := digestFile(ctx, segPath, nil)
		if err != nil {
			return fmt.Errorf("inspect required archive segment: %w", err)
		}
		if entry.SHA256 != expectedSHA || entry.Size != expectedSize {
			return errors.New("archive segment does not match database digest or byte count")
		}
		evidence := segments[runID]
		if status == usagearchive.SegmentStatusPublished {
			evidence.published++
		} else {
			evidence.verified++
		}
		segments[runID] = evidence
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read archive matching evidence: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close archive matching evidence: %w", err)
	}
	return ctx.Err()
}

// The production state machine publishes manifest metadata in MarkArchived and
// atomically changes all segment statuses in MarkVerified. Failed runs retain
// the evidence required by their resume stage. Cancellation can retain files,
// so recorded references are still checked even when publication is optional.
type archiveLifecyclePolicy struct {
	segmentStatus     string // Empty means segments are forbidden.
	segmentsRequired  bool
	manifestRequired  bool
	manifestForbidden bool
	deletionAllowed   bool
}

func archiveRunPolicy(status, resumeStatus string) (archiveLifecyclePolicy, error) {
	if status == usagearchive.StatusFailed {
		switch resumeStatus {
		case usagearchive.StatusArchiving, usagearchive.StatusVerifying, usagearchive.StatusDeleting:
			status = resumeStatus
		default:
			return archiveLifecyclePolicy{}, errors.New("invalid archive run resume status")
		}
	} else if resumeStatus != "" {
		return archiveLifecyclePolicy{}, errors.New("archive run resume status requires failed state")
	}
	switch status {
	case usagearchive.StatusPreviewed:
		return archiveLifecyclePolicy{manifestForbidden: true}, nil
	case usagearchive.StatusArchiving:
		return archiveLifecyclePolicy{segmentStatus: usagearchive.SegmentStatusPublished, manifestForbidden: true}, nil
	case usagearchive.StatusCancelled:
		return archiveLifecyclePolicy{segmentStatus: usagearchive.SegmentStatusPublished}, nil
	case usagearchive.StatusArchived, usagearchive.StatusVerifying:
		return archiveLifecyclePolicy{segmentStatus: usagearchive.SegmentStatusPublished, segmentsRequired: true, manifestRequired: true}, nil
	case usagearchive.StatusVerified:
		return archiveLifecyclePolicy{segmentStatus: usagearchive.SegmentStatusVerified, segmentsRequired: true, manifestRequired: true}, nil
	case usagearchive.StatusDeleting, usagearchive.StatusCompleted:
		return archiveLifecyclePolicy{segmentStatus: usagearchive.SegmentStatusVerified, segmentsRequired: true, manifestRequired: true, deletionAllowed: true}, nil
	default:
		return archiveLifecyclePolicy{}, errors.New("invalid archive run status")
	}
}

func validateArchiveRunManifests(ctx context.Context, db *sql.DB, root string, segments map[string]archiveSegmentEvidence, runs map[string]string) error {
	hasRuns, err := tableExists(ctx, db, "usage_archive_runs")
	if err != nil {
		return err
	}
	if !hasRuns {
		return nil
	}
	// Segment rows are already closed: inspection uses a single DB connection.
	rows, err := db.QueryContext(ctx, `SELECT id, status, resume_status, manifest_file, manifest_sha256,
		deleted_event_count, last_deleted_event_id, delete_started_at_ms, completed_at_ms,
		archive_digest, archived_at_ms, verified_at_ms, mode, schema_version, format, requested_stage FROM usage_archive_runs`)
	if err != nil {
		return fmt.Errorf("query archive run manifest evidence: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var runID, status, mode, format string
		var schemaVersion int
		var resumeStatus, fileName, expectedSHA, archiveDigest, requestedStage sql.NullString
		var deletedCount, lastDeletedID int64
		var deleteStarted, completed, archivedAt, verifiedAt sql.NullInt64
		if err := rows.Scan(&runID, &status, &resumeStatus, &fileName, &expectedSHA,
			&deletedCount, &lastDeletedID, &deleteStarted, &completed, &archiveDigest, &archivedAt, &verifiedAt,
			&mode, &schemaVersion, &format, &requestedStage); err != nil {
			return fmt.Errorf("scan archive run manifest evidence: %w", err)
		}
		decodedID, err := hex.DecodeString(runID)
		if err != nil || len(decodedID) != 16 || hex.EncodeToString(decodedID) != runID ||
			(mode != usagearchive.RunModeManual && mode != usagearchive.RunModeRetention) ||
			schemaVersion != usagearchive.SchemaVersion || format != usagearchive.FormatGzipJSONLV1 {
			return errors.New("invalid persisted archive run contract")
		}
		switch requestedStage.String {
		case "", usagearchive.StatusArchiving, usagearchive.StatusVerifying, usagearchive.StatusDeleting:
			// Known queued stages may be satisfied/stale and cleared by the runner.
		default:
			return errors.New("invalid archive run requested stage")
		}
		policy, err := archiveRunPolicy(status, resumeStatus.String)
		if err != nil {
			return err
		}
		runs[runID] = status
		if deletedCount < 0 || lastDeletedID < 0 {
			return errors.New("invalid archive deletion counters")
		}
		hasDeletion := deletedCount > 0 || lastDeletedID > 0 || deleteStarted.Valid || completed.Valid
		if hasDeletion && !policy.deletionAllowed {
			return errors.New("archive deletion evidence does not match run lifecycle")
		}
		if completed.Valid && status != usagearchive.StatusCompleted {
			return errors.New("archive completion evidence requires completed run")
		}
		evidence := segments[runID]
		if policy.segmentStatus == "" && evidence.published+evidence.verified != 0 {
			return errors.New("archive run lifecycle forbids segment evidence")
		}
		if (policy.segmentStatus == usagearchive.SegmentStatusPublished && evidence.verified != 0) ||
			(policy.segmentStatus == usagearchive.SegmentStatusVerified && evidence.published != 0) {
			return errors.New("archive segment status does not match run lifecycle")
		}
		publicationRecorded := archiveDigest.String != "" || archivedAt.Valid || fileName.String != "" || expectedSHA.String != ""
		if policy.manifestForbidden && publicationRecorded {
			return errors.New("archive run lifecycle forbids manifest metadata")
		}
		if (policy.manifestRequired || publicationRecorded) && (fileName.String == "" || expectedSHA.String == "") {
			return errors.New("published archive run requires manifest file and digest")
		}
		// MarkArchived and MarkVerified publish these fields atomically with the
		// run/segment statuses. Bind the snapshot to those transition receipts,
		// without inferring coverage or historical completeness.
		if (policy.manifestRequired || publicationRecorded) &&
			(strings.TrimSpace(archiveDigest.String) == "" || !archivedAt.Valid || archivedAt.Int64 <= 0) {
			return errors.New("archive publication requires archive_digest and positive archived_at_ms")
		}
		if policy.segmentStatus == usagearchive.SegmentStatusVerified {
			if !verifiedAt.Valid || verifiedAt.Int64 <= 0 {
				return errors.New("verified archive run requires positive verified_at_ms")
			}
		} else if verifiedAt.Valid {
			return errors.New("archive verification evidence does not match run lifecycle")
		}
		if policy.deletionAllowed && (!deleteStarted.Valid || deleteStarted.Int64 <= 0) {
			return errors.New("archive deletion stage requires positive delete_started_at_ms")
		}
		if status == usagearchive.StatusCompleted && (!completed.Valid || completed.Int64 <= 0) {
			return errors.New("completed archive run requires positive completed_at_ms")
		}
		if policy.segmentsRequired && evidence.published+evidence.verified == 0 {
			return errors.New("published archive run requires segment evidence")
		}
		if fileName.String == "" && expectedSHA.String == "" {
			continue
		}
		if fileName.String == "" || expectedSHA.String == "" {
			return errors.New("archive manifest file and digest must be recorded together")
		}
		manifestPath, err := archiveEvidencePath(root, runID, fileName.String)
		if err != nil {
			return fmt.Errorf("invalid archive manifest path: %w", err)
		}
		decodedSHA, err := hex.DecodeString(expectedSHA.String)
		if err != nil || len(decodedSHA) != sha256.Size {
			return errors.New("invalid archive manifest digest")
		}
		info, err := os.Lstat(manifestPath)
		if err != nil {
			return fmt.Errorf("inspect archive manifest: %w", err)
		}
		if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxArchiveManifestBytes {
			return errors.New("archive manifest must be a non-empty regular file within 4 MiB")
		}
		// digestFile also rejects symlink ancestors/hard links and checks that
		// the same file remains published throughout the context-aware read.
		entry, err := digestFile(ctx, manifestPath, nil)
		if err != nil {
			return fmt.Errorf("read archive manifest evidence: %w", err)
		}
		if entry.Size <= 0 || entry.Size > maxArchiveManifestBytes || entry.SHA256 != expectedSHA.String {
			return errors.New("archive manifest does not match database digest or size bound")
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read archive run manifest evidence: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close archive run manifest evidence: %w", err)
	}
	return ctx.Err()
}

func archiveRunRequiresLock(status string) bool {
	return status == usagearchive.StatusArchiving || status == usagearchive.StatusVerifying || status == usagearchive.StatusDeleting
}

func validateArchiveMaintenanceLocks(ctx context.Context, db *sql.DB, runs map[string]string) error {
	hasLocks, err := tableExists(ctx, db, "usage_maintenance_locks")
	if err != nil {
		return err
	}
	owners := make(map[string]bool)
	if hasLocks {
		rows, err := db.QueryContext(ctx, `SELECT name, run_id, operation, acquired_at_ms, updated_at_ms FROM usage_maintenance_locks`)
		if err != nil {
			return fmt.Errorf("query archive maintenance locks: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			var name, runID, operation string
			var acquiredAt, updatedAt int64
			if err := rows.Scan(&name, &runID, &operation, &acquiredAt, &updatedAt); err != nil {
				return fmt.Errorf("scan archive maintenance locks: %w", err)
			}
			status, exists := runs[runID]
			if !exists || name != usagearchive.MaintenanceLockName || !archiveRunRequiresLock(status) ||
				operation != status || acquiredAt <= 0 || updatedAt <= 0 || len(owners) != 0 {
				return errors.New("archive maintenance lock does not match run lifecycle")
			}
			owners[runID] = true
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("read archive maintenance locks: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close archive maintenance locks: %w", err)
		}
	}
	for runID, status := range runs {
		if archiveRunRequiresLock(status) && !owners[runID] {
			return errors.New("active archive run requires matching maintenance lock")
		}
	}
	return ctx.Err()
}

type createHooks struct {
	beforeRevalidate func()
	copy             func(context.Context, string, string) error
	// Package-private, synchronous checkpoints for controlled subprocess tests.
	at            func(string)
	syncDirectory func(string) error
	scratchDir    string
}

// Create requires a held lock bound to this database. The fence excludes
// cooperating Manager writers only. The owner must stop/isolate external
// writers; matching hashes cannot establish that an external writer is absent.
func Create(ctx context.Context, layout Layout, bundleDir string, lock *processlock.Lock) (Manifest, error) {
	return CreateWithScratch(ctx, layout, bundleDir, lock, "")
}

// CreateWithScratch is Create using scratchDir as the base directory for
// transient SQLite matching inspection copies. Empty scratchDir uses os.TempDir.
func CreateWithScratch(ctx context.Context, layout Layout, bundleDir string, lock *processlock.Lock, scratchDir string) (Manifest, error) {
	return create(ctx, layout, bundleDir, lock, createHooks{scratchDir: scratchDir})
}

func create(ctx context.Context, layout Layout, bundleDir string, lock *processlock.Lock, h createHooks) (Manifest, error) {
	return createBundle(ctx, layout, bundleDir, lock, h, false)
}

func createBundle(ctx context.Context, layout Layout, bundleDir string, lock *processlock.Lock, h createHooks, recoverJournal bool) (result Manifest, resultErr error) {
	l, err := normalize(layout)
	if err != nil {
		return Manifest{}, err
	}
	validateLock := func() error {
		if lock == nil || !samePath(lock.DatabasePath(), l.DatabasePath) {
			return errors.New("Manager bundle requires its database ownership lock")
		}
		return lock.Validate()
	}
	if err := validateLock(); err != nil {
		return Manifest{}, err
	}
	dest, err := prepareDestination(bundleDir, l)
	if err != nil {
		return Manifest{}, err
	}
	extraRoots := []string{dest, operationRoot(dest)}
	if _, err := resolveValidatedScratchBase(h.scratchDir, append(layoutProtectedRoots(l), extraRoots...)...); err != nil {
		return Manifest{}, err
	}
	before, err := inspect(ctx, l)
	if err != nil {
		return Manifest{}, err
	}
	before.Fence = CooperatingFence
	if !recoverJournal {
		if err := validateMatchingEvidence(ctx, l, h.scratchDir, extraRoots...); err != nil {
			return Manifest{}, fmt.Errorf("source matching evidence invalid: %w", err)
		}
	} else if err := requireRollbackJournal(l); err != nil {
		return Manifest{}, err
	}
	p, err := beginPublication(ctx, dest, l, h)
	if err != nil {
		return Manifest{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, p.finish()) }()
	tmp := p.staging()
	h.checkpoint("staging-created")
	copyFn := h.copy
	if copyFn == nil {
		copyFn = copyFile
	}
	if err := materialize(ctx, l, tmp, before, copyFn); err != nil {
		return Manifest{}, err
	}
	if h.beforeRevalidate != nil {
		h.beforeRevalidate()
	}
	if err := validateLock(); err != nil {
		return Manifest{}, err
	}
	if err := Revalidate(ctx, l, before); err != nil {
		return Manifest{}, err
	}
	if err := Revalidate(ctx, BundleLayout(tmp), before); err != nil {
		return Manifest{}, err
	}
	sealed := before
	if recoverJournal {
		if err := recoverRollbackJournal(ctx, BundleLayout(tmp)); err != nil {
			return Manifest{}, err
		}
		sealed, err = Inspect(ctx, BundleLayout(tmp))
		if err != nil {
			return Manifest{}, err
		}
		sealed.Fence = CooperatingFence
		sealed.Recovery = &RecoveryEvidence{Method: rollbackRecoveryMethod, SourceRevision: before.SourceRevision}
	}
	if err := validateMatchingEvidence(ctx, BundleLayout(tmp), h.scratchDir, append(layoutProtectedRoots(l), extraRoots...)...); err != nil {
		return Manifest{}, fmt.Errorf("bundle matching evidence invalid: %w", err)
	}
	h.checkpoint("matching-validated")
	if err := writeManifest(tmp, sealed); err != nil {
		return Manifest{}, err
	}
	h.checkpoint("manifest-written")
	// The expensive matching/production checks must precede the final source
	// observation. This still requires external writer isolation by the owner.
	if err := p.publish(ctx, sealed, func() error {
		if err := validateLock(); err != nil {
			return err
		}
		return RevalidateSource(ctx, l, before)
	}); err != nil {
		return Manifest{}, err
	}
	return sealed, nil
}

func materialize(ctx context.Context, l Layout, dest string, m Manifest, copyFn func(context.Context, string, string) error) error {
	// Directories must precede files, independent of map iteration order.
	for name, e := range m.Entries {
		if e.Present && e.Directory {
			if err := os.MkdirAll(filepath.Join(dest, filepath.FromSlash(name)), 0o700); err != nil {
				return err
			}
		}
	}
	for name, e := range m.Entries {
		if !e.Present || e.Directory {
			continue
		}
		source := l.paths()[name]
		if strings.HasPrefix(name, "usage-archives/") {
			source = filepath.Join(l.ArchivesPath, filepath.FromSlash(strings.TrimPrefix(name, "usage-archives/")))
		}
		if err := copyFn(ctx, source, filepath.Join(dest, filepath.FromSlash(name))); err != nil {
			return fmt.Errorf("copy %s: %w", name, err)
		}
	}
	return nil
}

// Verify rejects missing, extra and changed files. An editable manifest remains
// an integrity description; it is not an authenticated maintenance receipt.
func Verify(ctx context.Context, bundleDir string) (Manifest, error) {
	return verify(ctx, bundleDir, false)
}

// The private exception is used only by a held publication owner on its exact
// recorded staging root. Public callers can never verify an unpublished root.
func verify(ctx context.Context, bundleDir string, allowUnpublished bool) (Manifest, error) {
	root, err := filepath.Abs(bundleDir)
	if err != nil {
		return Manifest{}, err
	}
	if err := safePath(root); err != nil {
		return Manifest{}, err
	}
	if !allowUnpublished && unpublishedPath(root) {
		return Manifest{}, errors.New("unpublished operation roots are not committed bundles")
	}
	manifestPath := filepath.Join(root, "manifest.json")
	if err := safePath(manifestPath); err != nil {
		return Manifest{}, err
	}
	info, err := os.Lstat(manifestPath)
	if err != nil {
		return Manifest{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxManifestBytes {
		return Manifest{}, errors.New("bundle manifest is not a bounded regular file")
	}
	f, err := os.Open(manifestPath)
	if err != nil {
		return Manifest{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return Manifest{}, err
	}
	if !os.SameFile(info, opened) {
		return Manifest{}, errors.New("bundle manifest changed while opening")
	}
	if err := rejectMultipleLinks(f); err != nil {
		return Manifest{}, err
	}
	decoder := json.NewDecoder(io.LimitReader(f, maxManifestBytes+1))
	decoder.DisallowUnknownFields()
	var m Manifest
	if err := decoder.Decode(&m); err != nil {
		return Manifest{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Manifest{}, errors.New("bundle manifest has trailing content")
	}
	if m.Version != Version || m.Fence != CooperatingFence || m.SourceRevision == "" {
		return Manifest{}, errors.New("unsupported Manager bundle manifest")
	}
	if expectedRev := computeSourceRevision(m.Entries); m.SourceRevision != expectedRev {
		return Manifest{}, errors.New("bundle manifest source revision mismatch")
	}
	if err := validateRecoveryEvidence(m); err != nil {
		return Manifest{}, err
	}
	if err := Revalidate(ctx, BundleLayout(root), m); err != nil {
		return Manifest{}, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return Manifest{}, err
	}
	for _, e := range entries {
		if e.Name() != "manifest.json" {
			if strings.HasPrefix(e.Name(), ".") || strings.Contains(e.Name(), "unpublished") {
				return Manifest{}, errors.New("unexpected temporary bundle entry")
			}
			if _, ok := m.Entries[e.Name()]; !ok {
				return Manifest{}, errors.New("unexpected bundle entry")
			}
		}
	}
	return m, nil
}

// RestoreNew restores the matching file set into a new owner-controlled root.
// Existing roots are rejected. No running/live Manager file set is replaced.
func RestoreNew(ctx context.Context, bundleDir, newRoot string) (Layout, error) {
	return RestoreNewWithScratch(ctx, bundleDir, newRoot, "")
}

// RestoreNewWithScratch restores the matching file set into targetDir using
// scratchDir as the base directory for transient SQLite matching inspection
// copies. Empty scratchDir uses os.TempDir.
func RestoreNewWithScratch(ctx context.Context, bundleDir, targetDir, scratchDir string) (Layout, error) {
	return restoreNew(ctx, bundleDir, targetDir, createHooks{scratchDir: scratchDir})
}

func restoreNew(ctx context.Context, bundleDir, newRoot string, h createHooks) (result Layout, resultErr error) {
	m, err := Verify(ctx, bundleDir)
	if err != nil {
		return Layout{}, err
	}
	l := BundleLayout(bundleDir)
	dest, err := prepareDestination(newRoot, l)
	if err != nil {
		return Layout{}, err
	}
	bundleRoot, err := filepath.Abs(bundleDir)
	if err != nil {
		return Layout{}, err
	}
	physical, err := physicalOverlap(bundleRoot, dest)
	if err != nil {
		return Layout{}, err
	}
	if overlaps(bundleRoot, dest) || overlaps(dest, bundleRoot) || physical {
		return Layout{}, errors.New("restore target overlaps source bundle root")
	}
	restoreRoots := []string{bundleRoot, dest, operationRoot(dest)}
	if _, err := resolveValidatedScratchBase(h.scratchDir, restoreRoots...); err != nil {
		return Layout{}, err
	}
	p, err := beginPublication(ctx, dest, l, h)
	if err != nil {
		return Layout{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, p.finish()) }()
	tmp := p.staging()
	h.checkpoint("staging-created")
	copyFn := h.copy
	if copyFn == nil {
		copyFn = copyFile
	}
	if err := materialize(ctx, l, tmp, m, copyFn); err != nil {
		return Layout{}, err
	}
	if h.beforeRevalidate != nil {
		h.beforeRevalidate()
	}
	if err := Revalidate(ctx, BundleLayout(tmp), m); err != nil {
		return Layout{}, err
	}
	if err := validateMatchingEvidence(ctx, BundleLayout(tmp), h.scratchDir, restoreRoots...); err != nil {
		return Layout{}, fmt.Errorf("restore matching evidence failed: %w", err)
	}
	h.checkpoint("matching-validated")
	if err := writeManifest(tmp, m); err != nil {
		return Layout{}, err
	}
	h.checkpoint("manifest-written")
	if err := p.publish(ctx, m, func() error {
		current, err := Verify(ctx, bundleDir)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, m) {
			return errors.New("Manager bundle changed before publication")
		}
		return nil
	}); err != nil {
		return Layout{}, err
	}
	return BundleLayout(dest), nil
}

func prepareDestination(path string, l Layout) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("bundle destination is required")
	}
	dest, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if unpublishedPath(dest) {
		return "", errors.New("reserved unpublished destination")
	}
	if err := safePath(dest); err != nil {
		return "", err
	}
	for _, p := range l.paths() {
		a, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		physical, err := physicalOverlap(a, dest)
		if err != nil {
			return "", err
		}
		if overlaps(a, dest) || overlaps(dest, a) || physical {
			return "", errors.New("bundle destination overlaps source")
		}
	}
	if err := absent(dest); err != nil {
		return "", err
	}
	// The parent must already exist; callers own placement and permissions.
	info, err := os.Stat(filepath.Dir(dest))
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("bundle destination parent is not a directory")
	}
	return dest, nil
}

func absent(path string) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("bundle destination already exists")
}
