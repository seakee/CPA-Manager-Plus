package upgrade

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	_ "modernc.org/sqlite"
)

var (
	// ErrSourceDatabaseMissing is the credential read's environment verdict: the
	// configured root holds no database to read a credential from. It is never a
	// verdict on the presented key, so a route that reports it must not answer
	// 401.
	ErrSourceDatabaseMissing = errors.New("source database is missing")
	errSourceIntegrity       = errors.New("source database integrity check failed")
	// ErrSourceDatabaseChanged is the credential read's before-read verdict: the
	// path no longer names the file that was inspected and opened, so the row that
	// was read is not the credential this step assessed. Nothing was compared and
	// nothing was written.
	ErrSourceDatabaseChanged = errors.New("source database changed during the credential read")
	// ErrSourceSymlinked is the shared file policy's verdict, exported here so a
	// caller of this package does not have to reach into the lock package for it:
	// the configured database is a symbolic link, so the file that would be
	// inspected is not the file that would be read. The preflight and the
	// credential read refuse it with the same code.
	ErrSourceSymlinked = processlock.ErrSymlinkedDatabase
)

// VersionUnknown is reported when the schema/settings markers do not identify a
// supported 1.x source.
const VersionUnknown = "unknown"

// sourceSchema is the bounded schema and settings vocabulary a preflight is
// allowed to observe. Nothing here is a profile, a version proof or a grant.
type sourceSchema struct {
	tables   map[string]bool
	indexes  map[string]bool
	settings map[string]string
}

// sourceMarkerKeys are the settings rows the version ladder reads. The v1 code
// writes them itself; they are revision facts, not credentials.
var sourceMarkerKeys = []string{"usage_cache_accounting_semantics_revision"}

func (s sourceSchema) sourceKind() string {
	if !s.tables["settings"] || !s.tables["usage_events"] {
		return SourceKindUnknown
	}
	if s.tables["gateway_api_key_identities"] {
		return SourceKindNativeV2
	}
	return SourceKindLegacyManager
}

func (s sourceSchema) has(m sourceMarker) bool {
	switch m.kind {
	case markerTable:
		return s.tables[m.name]
	case markerIndex:
		return s.indexes[m.name]
	case markerSetting:
		value, ok := s.settings[m.name]
		return ok && value == m.value
	default:
		return false
	}
}

// openReadOnlySource opens the legacy database without write access and
// verifies integrity with a single quick_check. It creates no WAL/SHM files,
// applies no pragma that persists state and never migrates.
func openReadOnlySource(ctx context.Context, path string) (*sql.DB, sourceSchema, error) {
	return openSource(ctx, path, false)
}

// openImmutableSource reads a byte-for-byte snapshot nobody writes, such as a
// verified backup member. immutable keeps SQLite from creating a WAL index
// beside it, which would make the snapshot disagree with its own manifest.
func openImmutableSource(ctx context.Context, path string) (*sql.DB, sourceSchema, error) {
	return openSource(ctx, path, true)
}

// openCredentialSource opens the legacy database read-only for the one row a
// credential check reads, and stops there. It deliberately runs no quick_check:
// that read scans every page of the file, and every authenticated request —
// including every status poll — proves the credential, so an integrity scan
// here would make a large source time out on a route that never sees the
// database otherwise. The full preflight keeps the check where the source is
// actually being qualified.
//
// It applies the same database file policy as the preflight: the path must be
// the database itself, never a symbolic or hard link. SQLite opens lazily on the
// first query, so the returned handle pins the descriptor it inspected; the
// caller proves after the read that the path still names that file.
func openCredentialSource(path string) (*credentialSource, error) {
	inspected, err := inspectSourceDatabase(path)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if err := processlock.OpenedDatabase(file, inspected); err != nil {
		_ = file.Close()
		return nil, err
	}
	db, err := openSourceDatabaseFile(path, false)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &credentialSource{db: db, file: file, path: path, inspected: inspected}, nil
}

// credentialSource is the open handle of one credential read. The pinned
// descriptor keeps the inspected file alive, so the read can be checked against
// it afterwards instead of trusting a path that may have been repointed.
type credentialSource struct {
	db        *sql.DB
	file      *os.File
	path      string
	inspected os.FileInfo
}

// verifyUnchanged is the read's closing half of the file policy: it runs after
// the first real read, and refuses a path that no longer names the file that was
// inspected. Without it a swap between the inspection and the lazy open would
// compare a credential from a file this step never assessed.
func (s *credentialSource) verifyUnchanged() error {
	if s == nil || s.file == nil {
		return ErrSourceDatabaseChanged
	}
	if err := processlock.OpenedDatabase(s.file, s.inspected); err != nil {
		return err
	}
	current, err := os.Lstat(s.path)
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || !os.SameFile(s.inspected, current) {
		return ErrSourceDatabaseChanged
	}
	return nil
}

func (s *credentialSource) close() error {
	if s == nil {
		return nil
	}
	return errors.Join(s.db.Close(), s.file.Close())
}

// openSource opens the source and qualifies it: integrity first, then the
// bounded schema vocabulary a preflight is allowed to observe.
func openSource(ctx context.Context, path string, immutable bool) (*sql.DB, sourceSchema, error) {
	db, err := openSourceDatabase(path, immutable)
	if err != nil {
		return nil, sourceSchema{}, err
	}
	schema, err := qualifySource(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, sourceSchema{}, err
	}
	return db, schema, nil
}

// openSourceWith opens the database at path through dsn and qualifies it the
// same way openSource does. It exists for the caller that must read its own
// copy of the source with a different access mode, and it applies the same
// file policy so both paths refuse the same shapes.
func openSourceWith(ctx context.Context, path, dsn string) (*sql.DB, sourceSchema, error) {
	if _, err := inspectSourceDatabase(path); err != nil {
		return nil, sourceSchema{}, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, sourceSchema{}, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	schema, err := qualifySource(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, sourceSchema{}, err
	}
	return db, schema, nil
}

// qualifySource proves the whole file is structurally readable and reads the
// bounded schema vocabulary a preflight is allowed to observe.
func qualifySource(ctx context.Context, db *sql.DB) (sourceSchema, error) {
	if err := quickCheckSource(ctx, db); err != nil {
		return sourceSchema{}, err
	}
	schema := sourceSchema{tables: map[string]bool{}, indexes: map[string]bool{}, settings: map[string]string{}}
	if err := readSourceSchema(ctx, db, &schema); err != nil {
		return sourceSchema{}, err
	}
	return schema, nil
}

// openSourceDatabase opens an existing source file read-only and reads nothing
// from it: the caller decides how much of the file to verify.
func openSourceDatabase(path string, immutable bool) (*sql.DB, error) {
	if _, err := inspectSourceDatabase(path); err != nil {
		return nil, err
	}
	return openSourceDatabaseFile(path, immutable)
}

// openSourceDatabaseFile opens the DSN without touching the file itself: SQLite
// reads lazily on the first statement, which is why a caller that has to prove
// the file identity does so around that first read.
func openSourceDatabaseFile(path string, immutable bool) (*sql.DB, error) {
	db, err := sql.Open("sqlite", readOnlyDataSourceName(path, immutable))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

// inspectSourceDatabase applies the shared database file policy and rejects a
// file with no content, which is not a readable source. A missing database is
// ErrSourceDatabaseMissing rather than a host error, because it is the
// operator's mount that has to change.
func inspectSourceDatabase(path string) (os.FileInfo, error) {
	info, err := processlock.InspectDatabaseFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrSourceDatabaseMissing
		}
		return nil, err
	}
	if info.Size() == 0 {
		return nil, ErrSourceDatabaseMissing
	}
	return info, nil
}

// quickCheckSource proves the whole database is structurally readable. It scans
// every page, so it belongs to the full preflight and never to a credential
// read.
func quickCheckSource(ctx context.Context, db *sql.DB) error {
	var result string
	if err := db.QueryRowContext(ctx, "pragma quick_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return errSourceIntegrity
	}
	return nil
}

func readOnlyDataSourceName(path string, immutable bool) string {
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := &url.URL{Scheme: "file", Path: uriPath}
	q := dsn.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	if immutable {
		q.Set("immutable", "1")
	}
	dsn.RawQuery = q.Encode()
	return dsn.String()
}

func readSourceSchema(ctx context.Context, db *sql.DB, schema *sourceSchema) error {
	rows, err := db.QueryContext(ctx, `select type, name from sqlite_master where type in ('table', 'index')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, name string
		if err := rows.Scan(&kind, &name); err != nil {
			return err
		}
		if kind == "table" {
			schema.tables[name] = true
		} else {
			schema.indexes[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	marks := make([]string, 0, len(sourceMarkerKeys))
	for range sourceMarkerKeys {
		marks = append(marks, "?")
	}
	settingRows, err := db.QueryContext(ctx,
		`select key, value from settings where key in (`+strings.Join(marks, ", ")+`)`, stringArgs(sourceMarkerKeys)...)
	if err != nil {
		return err
	}
	defer settingRows.Close()
	for settingRows.Next() {
		var key, value string
		if err := settingRows.Scan(&key, &value); err != nil {
			return err
		}
		schema.settings[key] = value
	}
	return settingRows.Err()
}

func stringArgs(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}

const (
	markerTable   = "table"
	markerIndex   = "index"
	markerSetting = "setting"
)

type sourceMarker struct {
	kind  string
	name  string
	value string
}

func (m sourceMarker) label() string {
	if m.kind == markerSetting {
		return m.name + "=" + m.value
	}
	return m.name
}

// sourceVersionLadder lists the schema/settings markers the 1.x line introduced,
// in release order. The database is the only source of truth: 1.x never
// persisted a release string, so a source is identified by the highest step
// whose markers it satisfies. That value is a floor, never an exact release,
// and a marker from a step newer than the pinned release is a mismatch.
var sourceVersionLadder = []versionStep{
	{release: "v1.14.0", markers: []sourceMarker{
		{kind: markerTable, name: "settings"},
		{kind: markerTable, name: "usage_events"},
		{kind: markerTable, name: "usage_event_identity_ledger"},
		{kind: markerTable, name: "usage_archive_event_refs"},
		{kind: markerTable, name: "usage_monitoring_event_projection_v1"},
		{kind: markerTable, name: "usage_monitoring_rollup_state"},
	}},
	{release: "v1.14.6", markers: []sourceMarker{
		{kind: markerTable, name: "usage_archive_deleted_coverage_daily"},
		{kind: markerSetting, name: "usage_cache_accounting_semantics_revision", value: "3"},
	}},
	{release: "v1.14.7", markers: []sourceMarker{
		{kind: markerIndex, name: "idx_usage_events_ts_latency_ttft_cover"},
	}},
}

type versionStep struct {
	release string
	markers []sourceMarker
}

type versionObservation struct {
	Release string
	Pinned  string
	Missing []string
	Newer   []string
}

func (o versionObservation) compatible() bool {
	if o.Release == VersionUnknown {
		return false
	}
	if len(o.Newer) != 0 {
		return false
	}
	observed, okObserved := parseRelease(o.Release)
	pinned, okPinned := parseRelease(o.Pinned)
	if !okObserved || !okPinned {
		return false
	}
	return observed <= pinned
}

func (o versionObservation) detail() string {
	detail := fmt.Sprintf("expected=%s observed=%s", o.Pinned, o.Release)
	if len(o.Newer) != 0 {
		detail += "; markers newer than pinned: " + strings.Join(o.Newer, ",")
	}
	if len(o.Missing) != 0 {
		detail += "; markers missing: " + strings.Join(o.Missing, ",")
	}
	return detail
}

// pinnedSourceVersion is the accepted source release. It is read from the
// bootstrap contract so this package never holds a second copy of the literal.
func pinnedSourceVersion() string { return bootstrap.PlannedDirectRelease }

func detectSourceVersion(schema sourceSchema, pinned string) versionObservation {
	observation := versionObservation{Release: VersionUnknown, Pinned: pinned, Newer: []string{}, Missing: []string{}}
	floor := 0
	for index, step := range sourceVersionLadder {
		if !step.satisfiedBy(schema) {
			break
		}
		floor = index + 1
	}
	pinnedRank, pinnedKnown := parseRelease(pinned)
	if floor > 0 {
		observation.Release = sourceVersionLadder[floor-1].release
	}
	for _, step := range sourceVersionLadder {
		rank, ok := parseRelease(step.release)
		if !ok || !pinnedKnown || rank <= pinnedRank {
			continue
		}
		for _, marker := range step.markers {
			if schema.has(marker) {
				observation.Newer = append(observation.Newer, marker.label())
			}
		}
	}
	for _, marker := range sourceVersionLadder[0].markers {
		if !schema.has(marker) {
			observation.Missing = append(observation.Missing, marker.label())
		}
	}
	sort.Strings(observation.Newer)
	sort.Strings(observation.Missing)
	return observation
}

func (s versionStep) satisfiedBy(schema sourceSchema) bool {
	for _, marker := range s.markers {
		if !schema.has(marker) {
			return false
		}
	}
	return true
}

func parseRelease(release string) (int, bool) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(release), "v")
	parts := strings.Split(trimmed, ".")
	if len(parts) != 3 {
		return 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, false
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return 0, false
	}
	if major < 0 || minor < 0 || patch < 0 {
		return 0, false
	}
	return major*1_000_000 + minor*1_000 + patch, true
}
