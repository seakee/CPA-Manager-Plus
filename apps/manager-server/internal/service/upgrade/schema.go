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
	_ "modernc.org/sqlite"
)

var (
	// ErrSourceDatabaseMissing is the credential read's environment verdict: the
	// configured root holds no database to read a credential from. It is never a
	// verdict on the presented key, so a route that reports it must not answer
	// 401.
	ErrSourceDatabaseMissing = errors.New("source database is missing")
	errSourceIntegrity       = errors.New("source database integrity check failed")
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
func openCredentialSource(path string) (*sql.DB, error) {
	return openSourceDatabase(path, false)
}

// openSource opens the source and qualifies it: integrity first, then the
// bounded schema vocabulary a preflight is allowed to observe.
func openSource(ctx context.Context, path string, immutable bool) (*sql.DB, sourceSchema, error) {
	db, err := openSourceDatabase(path, immutable)
	if err != nil {
		return nil, sourceSchema{}, err
	}
	fail := func(err error) (*sql.DB, sourceSchema, error) {
		_ = db.Close()
		return nil, sourceSchema{}, err
	}
	if err := quickCheckSource(ctx, db); err != nil {
		return fail(err)
	}
	schema := sourceSchema{tables: map[string]bool{}, indexes: map[string]bool{}, settings: map[string]string{}}
	if err := readSourceSchema(ctx, db, &schema); err != nil {
		return fail(err)
	}
	return db, schema, nil
}

// openSourceDatabase opens an existing source file read-only and reads nothing
// from it: the caller decides how much of the file to verify.
func openSourceDatabase(path string, immutable bool) (*sql.DB, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrSourceDatabaseMissing
		}
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, ErrSourceDatabaseMissing
	}
	db, err := sql.Open("sqlite", readOnlyDataSourceName(path, immutable))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
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
