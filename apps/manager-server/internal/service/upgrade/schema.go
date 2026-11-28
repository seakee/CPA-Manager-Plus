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
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/datamigration"
	_ "modernc.org/sqlite"
)

var (
	// ErrSourceDatabaseMissing is the credential read's environment verdict: the
	// configured root holds no database to read a credential from. It is never a
	// verdict on the presented key, so a route that reports it must not answer
	// 401.
	ErrSourceDatabaseMissing = errors.New("source database is missing")
	// errDatabaseMissing is the copy-in reader's own missing-file verdict for a
	// database it was handed by path (its copied target, a backup member).
	errDatabaseMissing = errors.New("database file is missing")
	errSourceIntegrity = errors.New("source database integrity check failed")
	// ErrSourceDatabaseChanged is the credential read's before-read verdict: the
	// path no longer names the file that was inspected, or the source was
	// written while its private copy was being taken, so the row that was read is
	// not the credential this step assessed. Nothing was compared and nothing was
	// written.
	ErrSourceDatabaseChanged = errors.New("source database changed during the credential read")
	// ErrSourceSymlinked is the shared file policy's verdict, exported here so a
	// caller of this package does not have to reach into the lock package for it:
	// the configured database is a symbolic link, so the file that would be
	// inspected is not the file that would be read. The preflight and the
	// credential read refuse it with the same code.
	ErrSourceSymlinked = processlock.ErrSymlinkedDatabase
)

// VersionUnknown is reported when the operator declared no source release.
const VersionUnknown = "unknown"

// sourceSchema is the bounded schema, format, and semantic revision vocabulary
// preflight and copy-in inspect on a stopped 1.x source.
//
// Manager 1.x never persisted an exact release tag, and v1.14.0 through
// v1.14.10 share the same core DDL while advancing derived-rollup revisions at
// known boundaries (for example usage_cache_accounting_semantics_revision=2 in
// v1.14.0..v1.14.5 and 3 from v1.14.6..v1.14.10). Admission therefore verifies
// the genuine v1.14.x family profile:
//   - an unlabeled source passes as VersionUnknown ("unknown") only when its
//     tables, columns, format versions, archive runs, and semantic revisions
//     stay inside the proven v1.14.x profile;
//   - an explicit declaration outside v1.14.x is refused;
//   - an explicit v1.14.<patch> declaration (including an unknown patch) is
//     admitted only when the source satisfies the proven v1.14.x profile and
//     carries no marker introduced after that declared patch.
type sourceSchema struct {
	tables            map[string]bool
	indexes           map[string]bool
	columns           map[string]map[string]bool
	settings          map[string]string
	archiveFormats    []string
	unknownMigrations []string
	requiresArchives  bool
}

// requiredLegacyTables is the structural lower bound a v1.14.x source must
// carry. It is not a version discriminator: v1.14.0 through v1.14.10 all have
// these tables, and several v1.13 releases do not. A declaration alone never
// satisfies it, because the tables are the only evidence the source can
// actually produce. usage_archive_event_refs is the table that marks the
// archive/retained-projection shape this upgrade copies, and
// usage_archive_deleted_coverage_daily is part of the v1.14.0 schema itself
// (repository/sqlite/migrate.go creates it in every 1.14.x release), so neither
// can identify a release after the pinned one.
var requiredLegacyTables = []string{
	"settings",
	"usage_events",
	"usage_event_identity_ledger",
	"usage_archive_event_refs",
	"usage_monitoring_event_projection_v1",
	"usage_monitoring_rollup_state",
	"usage_archive_deleted_coverage_daily",
}

// missingTables returns the required tables the source does not have, in the
// order they are required so the first missing table is the most specific.
func (s sourceSchema) missingTables(required []string) []string {
	missing := []string{}
	for _, name := range required {
		if !s.tables[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

func (s sourceSchema) sourceKind() string {
	if !s.tables["settings"] || !s.tables["usage_events"] {
		return SourceKindUnknown
	}
	if len(s.v2OnlyTables()) != 0 {
		return SourceKindNativeV2
	}
	return SourceKindLegacyManager
}

// v2OnlyTablePrefixes are the tables only the v2 Manager migration creates:
// the Fresh/gateway workspaces and the upgrade operation row itself. A source
// that already carries one has been opened by v2 and cannot be imported again.
var v2OnlyTablePrefixes = []string{"gateway_", "fresh_", "upgrade_"}

// v2OnlyTables returns the v2-only tables found in the source, in lexical order.
func (s sourceSchema) v2OnlyTables() []string {
	found := []string{}
	for name := range s.tables {
		for _, prefix := range v2OnlyTablePrefixes {
			if strings.HasPrefix(name, prefix) {
				found = append(found, name)
				break
			}
		}
	}
	sort.Strings(found)
	return found
}

// quickCheckSource runs the single read-only integrity command the preflight is
// allowed to issue against the snapshot.
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

func readSourceSchema(ctx context.Context, db *sql.DB) (sourceSchema, error) {
	schema := sourceSchema{
		tables:   map[string]bool{},
		indexes:  map[string]bool{},
		columns:  map[string]map[string]bool{},
		settings: map[string]string{},
	}

	rows, err := db.QueryContext(ctx, `select type, name from sqlite_master where type in ('table', 'index')`)
	if err != nil {
		return sourceSchema{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, name string
		if err := rows.Scan(&kind, &name); err != nil {
			return sourceSchema{}, err
		}
		if kind == "index" {
			schema.indexes[name] = true
			continue
		}
		schema.tables[name] = true
	}
	if err := rows.Err(); err != nil {
		return sourceSchema{}, err
	}
	// A source without a settings table is not a Manager database at all; the
	// version check refuses it by kind, so there is nothing to read there.
	if !schema.tables["settings"] {
		return schema, nil
	}
	for _, table := range v114ProfileCheckedTableOrder {
		if !schema.tables[table] {
			continue
		}
		cols, err := readTableColumns(ctx, db, table)
		if err != nil {
			return sourceSchema{}, err
		}
		schema.columns[table] = cols
	}
	if schema.columns["settings"]["key"] && schema.columns["settings"]["value"] {
		marks := make([]string, len(sourceMarkerKeys))
		for index := range sourceMarkerKeys {
			marks[index] = "?"
		}
		settingRows, err := db.QueryContext(ctx,
			`select key, value from settings where key in (`+strings.Join(marks, ", ")+`)`, stringArgs(sourceMarkerKeys)...)
		if err != nil {
			return sourceSchema{}, err
		}
		defer settingRows.Close()
		for settingRows.Next() {
			var key, value string
			if err := settingRows.Scan(&key, &value); err != nil {
				return sourceSchema{}, err
			}
			schema.settings[key] = value
		}
		if err := settingRows.Err(); err != nil {
			return sourceSchema{}, err
		}
	}
	if schema.tables["usage_archive_runs"] &&
		schema.columns["usage_archive_runs"]["schema_version"] &&
		schema.columns["usage_archive_runs"]["format"] {
		archiveRows, err := db.QueryContext(ctx,
			`select distinct schema_version, format from usage_archive_runs order by schema_version asc, format asc`)
		if err != nil {
			return sourceSchema{}, err
		}
		defer archiveRows.Close()
		for archiveRows.Next() {
			var schemaVersion int
			var format string
			if err := archiveRows.Scan(&schemaVersion, &format); err != nil {
				return sourceSchema{}, err
			}
			if schemaVersion != v114ArchiveSchemaVersion || format != v114ArchiveFormat {
				schema.archiveFormats = append(schema.archiveFormats,
					fmt.Sprintf("schema_version=%d format=%q", schemaVersion, format))
			}
		}
		if err := archiveRows.Err(); err != nil {
			return sourceSchema{}, err
		}
	}
	for _, archiveTable := range []string{
		"usage_archive_runs",
		"usage_archive_segments",
		"usage_archive_event_refs",
		"usage_archive_deleted_coverage_daily",
	} {
		if !schema.tables[archiveTable] {
			continue
		}
		var hasRows int
		if err := db.QueryRowContext(ctx, `select exists(select 1 from `+archiveTable+` limit 1)`).Scan(&hasRows); err != nil {
			return sourceSchema{}, err
		}
		if hasRows != 0 {
			schema.requiresArchives = true
			break
		}
	}
	if schema.tables["usage_data_migrations"] {
		migrationCols, err := readTableColumns(ctx, db, "usage_data_migrations")
		if err != nil {
			return sourceSchema{}, err
		}
		if migrationCols["name"] {
			migrationRows, err := db.QueryContext(ctx,
				`select distinct name from usage_data_migrations order by name asc`)
			if err != nil {
				return sourceSchema{}, err
			}
			defer migrationRows.Close()
			for migrationRows.Next() {
				var name string
				if err := migrationRows.Scan(&name); err != nil {
					return sourceSchema{}, err
				}
				switch name {
				case datamigration.UsageCacheAccountingMigrationName,
					"usage_cache_accounting_v1", "quota_snapshot_lifecycle_v1":
					// Both historical markers exist in the released v1.14.4 and
					// v1.14.10 migrations; neither denotes an unknown format.
				default:
					schema.unknownMigrations = append(schema.unknownMigrations, name)
				}
			}
			if err := migrationRows.Err(); err != nil {
				return sourceSchema{}, err
			}
		}
	}
	return schema, nil
}

func readTableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `pragma table_info(`+table+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var (
			cid       int
			name      string
			colType   string
			notNull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return cols, nil
}

// openImmutableSource reads a byte-for-byte snapshot nobody writes, such as a
// verified backup member. immutable keeps SQLite from creating a WAL index
// beside it, which would make the snapshot disagree with its own manifest.
func openImmutableSource(ctx context.Context, path string) (*sql.DB, sourceSchema, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, sourceSchema{}, errDatabaseMissing
		}
		return nil, sourceSchema{}, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, sourceSchema{}, errDatabaseMissing
	}
	db, err := sql.Open("sqlite", readOnlyDataSourceName(path, true))
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
	schema, err := readSourceSchema(ctx, db)
	if err != nil {
		return fail(err)
	}
	return db, schema, nil
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
	if err := AuditSourceSidecars(path); err != nil {
		return nil, err
	}
	if info.Size() == 0 && isDatabaseFileMissingOrEmpty(path) {
		return nil, ErrSourceDatabaseMissing
	}
	return info, nil
}

// credentialSource is the read-only handle of one credential read: a private
// copy of a preserved source and its sidecars, taken and proved by OpenSnapshot.
// Nothing is ever opened in place, so SQLite cannot create or update a sidecar
// beside the source and cannot miss one that appears while the copy is taken.
type credentialSource struct {
	snapshot *Snapshot
}

func (s *credentialSource) database() *sql.DB {
	if s == nil || s.snapshot == nil {
		return nil
	}
	return s.snapshot.DB()
}

func (s *credentialSource) close() error {
	if s == nil {
		return nil
	}
	return s.snapshot.Close()
}

// openCredentialSource opens the one row a credential check reads without ever
// opening the 1.x source in place.
//
// Every open of the source is a write risk, not only a WAL one. `mode=ro` plus
// query_only forbids SQL writes but still lets SQLite create a -wal/-shm beside
// the database, and an immutable handle on a file that was cold when it was
// inspected reads a state that a writer may already have replaced with a newer
// one (the new WAL is simply not seen). The source is therefore always read
// through the same private snapshot the preflight takes: the copy is digested
// before and after it is made, so a source that gains a WAL, a checkpoint or a
// rewrite while the read is being set up is refused with ErrSourceDatabaseChanged
// instead of answering from a stale or mixed state.
//
// It deliberately runs no quick_check: that read scans every page of the file,
// and every authenticated request — including every status poll — proves the
// credential, so an integrity scan here would make a large source time out on a
// route that never sees the database otherwise. The full preflight keeps the
// check where the source is actually being qualified.
func openCredentialSource(ctx context.Context, preserved PreservedSource, extraRoots ...ScratchRoot) (*credentialSource, error) {
	path := preserved.Database
	if _, err := inspectSourceDatabase(path); err != nil {
		return nil, err
	}
	// The private copy must never land in the tree this step preserves, whichever
	// state the source is in: the source database's own directory, the directory
	// holding the data key its secrets are sealed with, and the default scratch
	// directory are all checked, because a host whose TMPDIR points into the source
	// root would otherwise hide a full database copy there. extraRoots is the rest
	// of the trees a materialized Manager target must stay out of: the Manager data
	// root that owns the store and the configured 1.x source volume the copy-in was
	// reading.
	if err := ValidatePreservedScratchDir("", preserved, extraRoots...); err != nil {
		return nil, credentialSourceError(err)
	}
	snapshot, err := OpenSnapshot(ctx, path, "")
	if err != nil {
		return nil, credentialSourceError(err)
	}
	return &credentialSource{snapshot: snapshot}, nil
}

// credentialSourceError translates the shared readers' verdicts into the
// credential read's own vocabulary, so a route can answer for the mount instead
// of guessing at the key.
func credentialSourceError(err error) error {
	switch {
	case errors.Is(err, errDatabaseMissing):
		return ErrSourceDatabaseMissing
	case errors.Is(err, processlock.ErrSymlinkedDatabase):
		return ErrSourceSymlinked
	case errors.Is(err, ErrSourceChanged):
		return ErrSourceDatabaseChanged
	default:
		return err
	}
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

func stringArgs(values []string) []any {
	args := make([]any, len(values))
	for index, value := range values {
		args[index] = value
	}
	return args
}

// pinnedSourceVersion is the verified reference release fixture ceiling
// (v1.14.10) from the bootstrap contract. Product admission accepts the proven
// v1.14.x family rather than only this single patch tag.
func pinnedSourceVersion() string { return bootstrap.PlannedDirectRelease }

// declaredVersion normalizes an operator declaration into its canonical vX.Y.Z
// tag when valid, or VersionUnknown ("unknown") when omitted. It never invents
// a patch tag for an unlabeled source.
func declaredVersion(declared string) string {
	trimmed := strings.TrimSpace(declared)
	if trimmed == "" {
		return VersionUnknown
	}
	if parsed, ok := bootstrap.ParseUpgradeSourceRelease(trimmed); ok {
		return parsed.Canonical
	}
	return trimmed
}

// evaluateSourceCompatibility evaluates a stopped source against the supported
// v1.14.x family and its proven schema, format, and semantic revision profile.
func evaluateSourceCompatibility(kind string, schema sourceSchema, declared string) (string, Check, error) {
	resolved := declaredVersion(declared)
	missing := schema.missingTables(requiredLegacyTables)
	trimmed := strings.TrimSpace(declared)
	if kind == SourceKindNativeV2 || kind != SourceKindLegacyManager || len(missing) != 0 {
		return resolved, versionCheck(kind, schema.v2OnlyTables(), missing, nil, trimmed, pinnedSourceVersion(), false), nil
	}
	var ladderTarget string
	if trimmed != "" {
		parsed, ok := bootstrap.ParseUpgradeSourceRelease(trimmed)
		if !ok || !parsed.InSupportedUpgradeFamily() {
			return resolved, versionCheck(kind, nil, nil, nil, trimmed, pinnedSourceVersion(), false), nil
		}
		resolved = parsed.Canonical
		ladderTarget = parsed.Canonical
	} else {
		ladderTarget = pinnedSourceVersion()
	}
	if unproven := schema.unprovenProfileObservations(); len(unproven) != 0 {
		return resolved, Check{
			Name:   CheckSourceVersion,
			Status: StatusBlocked,
			Code:   CodeSourceVersionUnsupported,
			Detail: fmt.Sprintf("source carries unproven schema or format markers outside %s: %s",
				bootstrap.SupportedUpgradeSourceFamily, strings.Join(unproven, ", ")),
		}, nil
	}
	ladder, err := newVersionLadder(ladderTarget)
	if err != nil {
		return resolved, Check{}, err
	}
	return resolved, versionCheck(kind, nil, nil, ladder.newer(schema), trimmed, ladderTarget, false), nil
}

// versionCheck decides the source version check from the v1.14.x family
// contract, structural lower bound, and post-pin / post-family marker observations.
func versionCheck(kind string, v2Tables []string, missingTables []string, newer []markerEvidence, declared string, _ string, _ bool) Check {
	check := Check{Name: CheckSourceVersion, Status: StatusPass}
	trimmed := strings.TrimSpace(declared)
	expectedFamily := bootstrap.SupportedUpgradeSourceFamily
	switch {
	case kind == SourceKindNativeV2:
		check.Status = StatusBlocked
		check.Code = CodeSourceAlreadyV2
		check.Detail = "v2-only tables present: " + strings.Join(v2Tables, ",")
	case kind != SourceKindLegacyManager:
		check.Status = StatusBlocked
		check.Code = CodeSourceUnreadable
		check.Detail = "source database is not a 1.x Manager database"
	case len(missingTables) != 0:
		check.Status = StatusBlocked
		check.Code = CodeSourceVersionUnsupported
		check.Detail = fmt.Sprintf("expected=%s declared=%s missingTables=%s; the source does not carry the v1.14 structure",
			expectedFamily, declaredVersion(trimmed), strings.Join(missingTables, ","))
	case trimmed != "" && !bootstrap.IsSupportedUpgradeSourceRelease(trimmed):
		check.Status = StatusBlocked
		check.Code = CodeSourceVersionUnsupported
		check.Detail = fmt.Sprintf("expected=%s declared=%s", expectedFamily, trimmed)
	case len(newer) != 0:
		check.Status = StatusBlocked
		check.Code = CodeSourceVersionUnsupported
		labels := make([]string, len(newer))
		for index, evidence := range newer {
			labels[index] = evidence.String()
		}
		if trimmed == "" {
			check.Detail = fmt.Sprintf("source carries markers beyond supported family %s: %s",
				expectedFamily, strings.Join(labels, ", "))
		} else {
			check.Detail = fmt.Sprintf("source is newer than the pinned release %s; markers from later releases: %s",
				declaredVersion(trimmed), strings.Join(labels, ", "))
		}
	case trimmed == "":
		check.Detail = fmt.Sprintf("expected=%s declared=none; compatible v1.14.x profile verified", expectedFamily)
	default:
		check.Detail = fmt.Sprintf("expected=%s declared=%s; compatible v1.14.x profile verified",
			expectedFamily, declaredVersion(trimmed))
	}
	return check
}

const (
	markerIndex   = "index"
	markerSetting = "setting"
)

// sourceMarker is one observation the version check may make against the
// source. A setting marker is a revision floor, not an exact value: v1 only
// ever raises a revision and never lowers it, so any value at or above the one
// the introducing release wrote proves the source is at least that release.
// Matching the exact value instead would accept a source carrying a revision
// the 1.x line has not written yet, which is the one direction this check must
// never fail in.
type sourceMarker struct {
	kind string
	name string
	// floor is the revision value the introducing release wrote. Only setting
	// markers use it; v1 writes no numeric index or table marker.
	floor int
}

// observedLabel reports whether the source carries the marker and, when it
// does, names what was actually observed: the index, or the setting with the
// value the source holds rather than the floor the check looked for.
//
// A setting row that is present but not a positive integer is reported as an
// invalid observation rather than as "no marker". The v1 code only ever writes
// strconv.Itoa of a positive revision, so a row that cannot be read as one was
// not written by a 1.x release this build knows; treating it as absent is the
// fail-open direction — the source would pass as if it carried nothing newer.
func (s sourceSchema) observedLabel(marker sourceMarker) (string, bool, bool) {
	switch marker.kind {
	case markerIndex:
		return marker.name, false, s.indexes[marker.name]
	case markerSetting:
		observed, found := s.settings[marker.name]
		if !found {
			return "", false, false
		}
		value, err := strconv.Atoi(strings.TrimSpace(observed))
		if err != nil || value < 1 {
			return fmt.Sprintf("%s=%q", marker.name, observed), true, true
		}
		if value < marker.floor {
			return "", false, false
		}
		return fmt.Sprintf("%s=%d", marker.name, value), false, true
	default:
		return marker.name, false, s.tables[marker.name]
	}
}

const (
	settingMonitoringModelFormatVersion        = "usage_monitoring_model_format_version"
	settingAccountHistoryIdentityFormatVersion = "usage_account_history_identity_format_version"
	settingDashboardHourlyFormatVersion        = "usage_dashboard_hourly_format_version"

	v114MonitoringModelFormatVersion        = "1"
	v114AccountHistoryIdentityFormatVersion = "identity-3:codex-2:model-1"
	v114ArchiveSchemaVersion                = 1
	v114ArchiveFormat                       = "gzip-jsonl-v1"
)

// sourceMarkerKeys are the settings rows the version and format profile checks
// read. The 1.x code writes them itself; they are format/revision facts, not
// credentials.
var sourceMarkerKeys = []string{
	datamigration.UsageCacheAccountingSemanticsRevisionKey,
	settingMonitoringModelFormatVersion,
	settingAccountHistoryIdentityFormatVersion,
	settingDashboardHourlyFormatVersion,
}

type tableColumnProfile struct {
	required []string
	allowed  map[string]bool
}

func columnSet(names ...string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = true
	}
	return out
}

var v114ProfileCheckedTableOrder = []string{
	"settings",
	"usage_events",
	"usage_event_identity_ledger",
	"usage_monitoring_rollup_state",
	"usage_monitoring_event_projection_v1",
	"usage_archive_runs",
	"usage_archive_segments",
	"usage_archive_event_refs",
	"usage_archive_deleted_coverage_daily",
}

var v114TableColumnProfiles = map[string]tableColumnProfile{
	"settings": {
		required: []string{"key", "value"},
		allowed:  columnSet("key", "value", "updated_at_ms"),
	},
	"usage_events": {
		required: []string{"id", "event_hash", "timestamp_ms"},
		allowed: columnSet(
			"id", "request_id", "event_hash", "timestamp_ms", "timestamp",
			"provider", "executor_type", "model", "endpoint", "method", "path",
			"client_ip", "x_forwarded_for", "user_agent", "auth_type", "auth_index",
			"source", "source_hash", "api_key_hash", "account_snapshot",
			"auth_label_snapshot", "auth_file_snapshot", "auth_provider_snapshot",
			"auth_account_id_snapshot", "auth_project_id_snapshot", "auth_snapshot_at_ms",
			"requested_model", "resolved_model", "reasoning_effort", "service_tier",
			"request_service_tier", "response_service_tier", "cache_input_mode",
			"input_tokens", "output_tokens", "reasoning_tokens", "cached_tokens",
			"cache_tokens", "cache_read_tokens", "cache_creation_tokens",
			"normalized_uncached_input_tokens", "normalized_total_input_tokens",
			"normalized_cache_read_tokens", "normalized_cache_creation_tokens",
			"total_tokens", "latency_ms", "ttft_ms", "failed", "fail_status_code",
			"fail_summary", "response_metadata_json", "header_quota_recover_at_ms",
			"header_quota_used_percent", "header_quota_plan_type", "header_error_kind",
			"header_error_code", "header_trace_id", "fail_body", "raw_json",
			"response_model", "session_id", "parent_session_id", "access_token_sha256",
			"generate", "stream", "created_at_ms",
		),
	},
	"usage_event_identity_ledger": {
		required: []string{"event_hash"},
		allowed: columnSet(
			"event_hash", "raw_event_id", "timestamp_ms", "bucket_ms",
			"aggregate_schema_version", "aggregate_structure_revision",
			"first_seen_at_ms", "updated_at_ms",
		),
	},
	"usage_monitoring_rollup_state": {
		required: []string{"rollup_name", "schema_version", "structure_revision", "status", "coverage_event_id"},
		allowed: columnSet(
			"rollup_name", "schema_version", "structure_revision", "status",
			"backfill_last_event_id", "coverage_event_id", "target_event_id",
			"processed_events", "last_run_started_at_ms", "updated_at_ms",
			"finished_at_ms", "last_error",
		),
	},
	"usage_monitoring_event_projection_v1": {
		required: []string{"event_id", "timestamp_ms"},
		allowed: columnSet(
			"event_id", "timestamp_ms", "search_text", "account_key", "provider",
			"executor_type", "model", "requested_model", "analytics_model",
			"resolved_model", "auth_index", "source", "source_hash", "api_key_hash",
			"account_snapshot", "auth_label_snapshot", "auth_file_snapshot",
			"auth_provider_snapshot", "auth_account_id_snapshot", "auth_project_id_snapshot",
			"reasoning_effort", "service_tier", "failed", "latency_ms", "input_tokens",
			"output_tokens", "reasoning_tokens", "cached_tokens", "cache_tokens",
			"cache_read_tokens", "cache_creation_tokens", "normalized_total_input_tokens",
			"total_tokens", "header_quota_plan_type", "header_error_kind",
			"header_error_code", "header_trace_id", "updated_at_ms",
		),
	},
	"usage_archive_runs": {
		required: []string{"id", "schema_version", "format", "status"},
		allowed: columnSet(
			"id", "mode", "schema_version", "format", "status", "resume_status",
			"requested_stage", "progress_phase", "progress_current", "progress_total",
			"progress_unit", "progress_updated_at_ms", "cutoff_timestamp_ms",
			"target_event_id", "event_count", "estimated_bytes", "last_archived_event_id",
			"archived_event_count", "archived_uncompressed_bytes",
			"archived_compressed_bytes", "archive_digest", "manifest_file",
			"manifest_sha256", "last_deleted_event_id", "deleted_event_count",
			"created_at_ms", "updated_at_ms", "started_at_ms", "archived_at_ms",
			"verified_at_ms", "delete_started_at_ms", "completed_at_ms", "last_error",
		),
	},
	"usage_archive_segments": {
		required: []string{"run_id", "sequence", "file_name"},
		allowed: columnSet(
			"run_id", "sequence", "status", "file_name", "first_event_id",
			"last_event_id", "min_timestamp_ms", "max_timestamp_ms", "event_count",
			"uncompressed_bytes", "compressed_bytes", "content_sha256",
			"event_hash_digest", "created_at_ms", "verified_at_ms",
		),
	},
	"usage_archive_event_refs": {
		required: []string{"event_hash", "raw_event_id", "timestamp_ms", "raw_deleted_at_ms"},
		allowed: columnSet(
			"event_hash", "run_id", "segment_sequence", "raw_event_id",
			"timestamp_ms", "archived_at_ms", "raw_deleted_at_ms",
		),
	},
	"usage_archive_deleted_coverage_daily": {
		required: []string{"utc_day", "deleted_event_count"},
		allowed: columnSet(
			"utc_day", "deleted_event_count", "min_timestamp_ms", "max_timestamp_ms",
		),
	},
}

var knownV114SourceTables = columnSet(
	"settings",
	"usage_events",
	"usage_rollup_checkpoints",
	"usage_hourly_aggregate_v1",
	"usage_hourly_aggregate_state",
	"usage_account_model_rollups",
	"usage_pricing_account_rollups_v1",
	"usage_dashboard_hourly_rollups",
	"usage_rollup_rebuild_state",
	"usage_pricing_hourly_rollups_v1",
	"usage_pricing_rollup_state",
	"usage_monitoring_account_daily_rollups_v1",
	"usage_monitoring_api_key_daily_rollups_v1",
	"usage_monitoring_selector_daily_rollups_v1",
	"usage_monitoring_event_projection_v1",
	"usage_monitoring_header_latest_v1",
	"usage_monitoring_rollup_state",
	"usage_monitoring_search_index_state",
	"usage_event_identity_ledger",
	"usage_archive_runs",
	"usage_archive_segments",
	"usage_archive_event_refs",
	"usage_archive_deleted_coverage_daily",
	"usage_maintenance_locks",
	"usage_data_migrations",
	"usage_cache_accounting_v2_changes",
	"usage_codex_legacy_identity_evidence_v1",
	"usage_derived_cleanup_jobs",
	"usage_derived_cleanup_cursors",
	"usage_derived_deferred_indexes",
	"dead_letter_events",
	"model_prices",
	"model_price_context_tiers",
	"model_price_service_tiers",
	"api_key_aliases",
	"account_action_candidates",
	"codex_inspection_runs",
	"codex_inspection_leases",
	"codex_inspection_results",
	"codex_inspection_logs",
	"codex_inspection_disable_ownership",
	"codex_inspection_disable_ownership_v2",
	"quota_cooldowns",
	"account_quota_observations",
	"account_quota_windows",
	"account_quota_window_activations",
	"account_quota_cycles",
	"account_quota_snapshots",
	// Parked 1.x derived rollup tables retained until offline cleanup-derived.
	"usage_account_model_rollups_legacy_v1120_rc2",
	"usage_account_model_rollups_legacy_identity_v3",
	"usage_account_model_rollups_legacy_identity_v3_codex_v2",
	"usage_pricing_account_rollups_v1_legacy_v1120_rc2",
	"usage_dashboard_hourly_rollups_legacy_v1120_rc2",
	"usage_hourly_aggregate_v1_legacy_v1120_rc2",
	"usage_monitoring_account_daily_rollups_v1_legacy_recovery",
	"usage_monitoring_api_key_daily_rollups_v1_legacy_recovery",
	"usage_monitoring_account_daily_rollups_v1_legacy_identity_v3",
	"usage_monitoring_api_key_daily_rollups_v1_legacy_identity_v3",
	"usage_monitoring_account_daily_rollups_v1_legacy_identity_v3_codex_v2",
	"usage_monitoring_api_key_daily_rollups_v1_legacy_identity_v3_codex_v2",
	"usage_monitoring_selector_daily_rollups_v1_legacy_recovery",
	"usage_monitoring_header_latest_v1_legacy_recovery",
	"usage_monitoring_event_projection_v1_legacy_recovery",
	"usage_dashboard_hourly_rollups_legacy_source_recovery",
	"usage_pricing_hourly_rollups_v1_legacy_source_recovery",
	"usage_cache_accounting_v2_changes_legacy_source_recovery",
	"usage_account_model_rollups_legacy_source_recovery",
	"usage_pricing_account_rollups_v1_legacy_source_recovery",
	"usage_codex_legacy_identity_evidence_v1_legacy_recovery",
)

var v114FTSShadowSuffixes = []string{"", "_data", "_idx", "_content", "_docsize", "_config"}

func isDigitsOnly(value string) bool {
	if value == "" {
		return false
	}
	for i := range len(value) {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func isKnownV114FTSTable(name string) bool {
	for _, base := range []string{
		"usage_monitoring_event_search_v1",
		"usage_monitoring_event_search_v1_legacy_v1120_rc2",
	} {
		for _, suffix := range v114FTSShadowSuffixes {
			if name == base+suffix {
				return true
			}
		}
	}
	const genPrefix = "usage_monitoring_event_search_v1_legacy_g"
	if strings.HasPrefix(name, genPrefix) {
		rest := strings.TrimPrefix(name, genPrefix)
		for _, suffix := range v114FTSShadowSuffixes {
			if suffix == "" {
				if isDigitsOnly(rest) {
					return true
				}
				continue
			}
			if strings.HasSuffix(rest, suffix) && isDigitsOnly(strings.TrimSuffix(rest, suffix)) {
				return true
			}
		}
	}
	return false
}

func isKnownV114SourceTable(name string) bool {
	if knownV114SourceTables[name] || isKnownV114FTSTable(name) {
		return true
	}
	const projectionGenPrefix = "usage_monitoring_event_projection_v1_legacy_g"
	if strings.HasPrefix(name, projectionGenPrefix) {
		return isDigitsOnly(strings.TrimPrefix(name, projectionGenPrefix))
	}
	return false
}

// unprovenProfileObservations returns any schema, column, format-revision,
// archive-format, or semantic-migration observations that fall outside the
// proven v1.14.x family profile.
func (s sourceSchema) unprovenProfileObservations() []string {
	observations := []string{}

	unexpectedTables := []string{}
	for name := range s.tables {
		if strings.HasPrefix(name, "sqlite_") {
			continue
		}
		isV2 := false
		for _, prefix := range v2OnlyTablePrefixes {
			if strings.HasPrefix(name, prefix) {
				isV2 = true
				break
			}
		}
		if isV2 || isKnownV114SourceTable(name) {
			continue
		}
		unexpectedTables = append(unexpectedTables, name)
	}
	sort.Strings(unexpectedTables)
	for _, name := range unexpectedTables {
		observations = append(observations, "unexpectedTable="+name)
	}

	if len(s.columns) > 0 {
		for _, table := range v114ProfileCheckedTableOrder {
			presentCols, ok := s.columns[table]
			if !ok {
				continue
			}
			profile := v114TableColumnProfiles[table]
			for _, reqCol := range profile.required {
				if !presentCols[reqCol] {
					observations = append(observations, fmt.Sprintf("missingColumn=%s.%s", table, reqCol))
				}
			}
			extraCols := []string{}
			for col := range presentCols {
				if !profile.allowed[col] {
					extraCols = append(extraCols, col)
				}
			}
			sort.Strings(extraCols)
			for _, col := range extraCols {
				observations = append(observations, fmt.Sprintf("unexpectedColumn=%s.%s", table, col))
			}
		}
	}

	if val, found := s.settings[settingMonitoringModelFormatVersion]; found && val != v114MonitoringModelFormatVersion {
		observations = append(observations, fmt.Sprintf("%s=%q", settingMonitoringModelFormatVersion, val))
	}
	if val, found := s.settings[settingAccountHistoryIdentityFormatVersion]; found && val != v114AccountHistoryIdentityFormatVersion {
		observations = append(observations, fmt.Sprintf("%s=%q", settingAccountHistoryIdentityFormatVersion, val))
	}
	if val, found := s.settings[settingDashboardHourlyFormatVersion]; found && val != "1" && val != "2" && val != "3" {
		observations = append(observations, fmt.Sprintf("%s=%q", settingDashboardHourlyFormatVersion, val))
	}
	for _, entry := range s.archiveFormats {
		observations = append(observations, "usage_archive_runs("+entry+")")
	}
	for _, name := range s.unknownMigrations {
		observations = append(observations, fmt.Sprintf("usage_data_migrations=%q", name))
	}
	return observations
}

type versionMarker struct {
	// release is the 1.x release that introduced the marker. It is carried so
	// a later re-pin drops the markers its own schema accepts instead of
	// refusing itself, and so a refusal can name what the marker proves.
	release string
	marker  sourceMarker
}

// usageCacheAccountingSemanticsRevisionV1146 is the value v1.14.6 wrote into
// usage_cache_accounting_semantics_revision (datamigration/repository.go at tag
// v1.14.6: CurrentUsageCacheAccountingSemanticsRevision = 3; v1.14.4 and
// v1.14.5 wrote 2). It is a literal on purpose. This is a historical fact about
// the 1.x line, not a property of this build: reading
// datamigration.CurrentUsageCacheAccountingSemanticsRevision would tie the
// marker to a value v2 is free to bump, and a bump to 4 would stop matching a
// 1.14.6–1.14.10 source and silently let it through.
const usageCacheAccountingSemanticsRevisionV1146 = 3

// usageCacheAccountingSemanticsRevisionPostV11410 is the first revision above
// the one the pin itself accepts. The v1.14.6 marker is a floor, so once a pin
// accepts revision 3 the marker row cannot also prove that a source has not
// moved past it. This ceiling is what restores that refusal: a source holding 4
// or more was not written by the pinned release. It is a literal for the same
// reason as the floor — a value this build happens to write must not decide
// what a historical 1.x source can hold.
const usageCacheAccountingSemanticsRevisionPostV11410 = usageCacheAccountingSemanticsRevisionV1146 + 1

// laterSourceMarkers lists the markers the 1.x line introduced after the
// pinned release, with the release that introduced each one. None of them is a
// table: the table set is identical across 1.14.x, and every base table in
// requiredLegacyTables is created by the pinned release too. Listing one here —
// usage_archive_deleted_coverage_daily was once mistaken for a v1.14.6 marker —
// refuses a source of the pinned release itself, which is exactly the defect
// the real-Docker harness found. The release is carried so a later re-pin drops
// the markers its own schema accepts instead of refusing itself.
//
// The two markers do not have the same strength. The settings revision is
// written by the v1.14.6 migration itself and a real 1.14.6+ source carries it,
// so its presence is reliable positive evidence. The index is created through
// the deferred/offline derived-index lifecycle: on a non-empty usage_events
// table the post-listen maintenance defers it, and only the offline
// `cleanup-derived` command builds it (release note v1.14.7-en.md, "Upgrade
// Notes"). Its presence is reliable positive evidence, its absence proves
// nothing at all.
var laterSourceMarkers = []versionMarker{
	{release: "v1.14.6", marker: sourceMarker{
		kind:  markerSetting,
		name:  datamigration.UsageCacheAccountingSemanticsRevisionKey,
		floor: usageCacheAccountingSemanticsRevisionV1146,
	}},
	{release: "v1.14.7", marker: sourceMarker{kind: markerIndex, name: "idx_usage_events_ts_latency_ttft_cover"}},
}

// markerEvidence is one marker the source carries that the pinned release
// lacks. It names the observation and the release that introduced it, so the
// refusal reads as evidence instead of a bare token.
type markerEvidence struct {
	label   string
	release string
}

func (e markerEvidence) String() string {
	return e.label + " (" + e.release + ")"
}

// versionLadder is the marker table narrowed to the releases after the pinned
// one, in release order. Constructing it parses every release string involved,
// so a malformed release is an error instead of a silently empty ladder: an
// empty ladder accepts a source that has moved past the pin, which is exactly
// the state the check exists to refuse.
//
// The revision setting needs one entry the marker table cannot express. Its
// marker is a floor, so a pin at or above v1.14.6 accepts revision 3 and the
// marker itself drops out of the ladder — leaving nothing at all to refuse
// revision 4. The ladder therefore adds a ceiling entry, named after the pin,
// holding the first revision above the one the pin accepts.
type versionLadder struct {
	after []versionMarker
}

func newVersionLadder(pinned string) (versionLadder, error) {
	pinnedRank, err := parseRelease(pinned)
	if err != nil {
		return versionLadder{}, fmt.Errorf("pinned source release: %w", err)
	}
	ladder := versionLadder{}
	ceiling := false
	for _, candidate := range laterSourceMarkers {
		rank, err := parseRelease(candidate.release)
		if err != nil {
			return versionLadder{}, fmt.Errorf("later marker release: %w", err)
		}
		if rank > pinnedRank {
			ladder.after = append(ladder.after, candidate)
			continue
		}
		// The pin already accepts this marker, so it is not evidence that a
		// source moved past the pin. The cache-accounting revision is the one
		// exception: dropping its floor would drop the only check the setting
		// has, so it is replaced by the ceiling above the pin. At most one
		// ceiling exists, and only when the pin is at or above the release
		// that wrote the floor.
		if ceiling || candidate.marker.kind != markerSetting ||
			candidate.marker.name != datamigration.UsageCacheAccountingSemanticsRevisionKey {
			continue
		}
		ladder.after = append(ladder.after, versionMarker{
			release: "after " + pinned,
			marker: sourceMarker{
				kind:  markerSetting,
				name:  datamigration.UsageCacheAccountingSemanticsRevisionKey,
				floor: usageCacheAccountingSemanticsRevisionPostV11410,
			},
		})
		ceiling = true
	}
	return ladder, nil
}

// newer returns the markers the source carries that the pinned release
// genuinely lacks, in the order the ladder lists them. A marker introduced at
// or below the pin is part of the schema the pin already accepts, so it can
// never prove that the source moved past it.
//
// An invalid setting row is named as invalid instead of as the release that
// introduced the setting: the row is not a value any 1.x release wrote, so
// blaming a release would misdescribe what was observed.
func (l versionLadder) newer(s sourceSchema) []markerEvidence {
	newer := []markerEvidence{}
	for _, candidate := range l.after {
		label, invalid, ok := s.observedLabel(candidate.marker)
		if !ok {
			continue
		}
		release := candidate.release
		if invalid {
			release = "invalid"
		}
		newer = append(newer, markerEvidence{label: label, release: release})
	}
	return newer
}

// releaseRank packs a major.minor.patch release into one comparable number.
type releaseRank int

func parseRelease(release string) (releaseRank, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(release), "v")
	parts := strings.Split(trimmed, ".")
	if len(parts) != 3 {
		return 0, fmt.Errorf("release %q is not major.minor.patch", release)
	}
	components := make([]int, len(parts))
	for index, part := range parts {
		if part == "" || strings.Trim(part, "0123456789") != "" {
			return 0, fmt.Errorf("release %q has a non-numeric component %q", release, part)
		}
		value, err := strconv.Atoi(part)
		// The rank packs three decimal components into one integer; a wider
		// component would collide with the next one up.
		if err != nil || value > 999 {
			return 0, fmt.Errorf("release %q has an out-of-range component %q", release, part)
		}
		components[index] = value
	}
	return releaseRank(components[0]*1_000_000 + components[1]*1_000 + components[2]), nil
}
