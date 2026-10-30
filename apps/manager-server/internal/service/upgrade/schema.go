package upgrade

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/datamigration"
)

var (
	errDatabaseMissing = errors.New("database file is missing")
	errSourceIntegrity = errors.New("source database integrity check failed")
)

// VersionUnknown is reported when the operator declared no source release.
const VersionUnknown = "unknown"

// sourceSchema is the bounded schema vocabulary a preflight is allowed to
// observe. Nothing here is a profile, a version proof or a grant.
//
// The schema cannot identify a 1.x release: v1.14.0 through v1.14.10 create the
// same tables, and the only per-release markers v1 writes are format revisions
// of derived rollups that several releases share. Preflight therefore never
// infers a release from DDL. What it does require is a structural lower bound:
// a source below the v1.14 shape is refused no matter what release it declares.
// It also refuses a source that carries a marker the pinned release genuinely
// lacks, because that source has already moved past the pin.
type sourceSchema struct {
	tables   map[string]bool
	indexes  map[string]bool
	settings map[string]string
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
var v2OnlyTablePrefixes = []string{"gateway_", "fresh_", "upgrade_operations"}

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
	schema := sourceSchema{tables: map[string]bool{}, indexes: map[string]bool{}, settings: map[string]string{}}
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
	return schema, settingRows.Err()
}

func stringArgs(values []string) []any {
	args := make([]any, len(values))
	for index, value := range values {
		args[index] = value
	}
	return args
}

// pinnedSourceVersion is the accepted source release. It is read from the
// bootstrap contract so this package never holds a second copy of the literal.
func pinnedSourceVersion() string { return bootstrap.PlannedDirectRelease }

// declaredVersion normalizes the operator's declaration. It is deliberately not
// parsed, retried or compared loosely: equality with the pinned release is the
// whole check.
func declaredVersion(declared string) string {
	trimmed := strings.TrimSpace(declared)
	if trimmed == "" {
		return VersionUnknown
	}
	return trimmed
}

// versionCheck decides the source version check from the declaration plus the
// structural lower bound. The release is never read out of the database: v1
// never persisted it, so a value inferred from DDL would be a guess dressed as
// evidence. The required tables, by contrast, are real evidence, and a source
// that lacks one is refused even when the declaration matches. So is a source
// that carries a marker the pinned release genuinely lacks: the declaration
// claims a release, and that claim cannot survive evidence of a later one.
func versionCheck(kind string, v2Tables []string, missingTables []string, newerMarkers []string, declared string, pinned string) Check {
	check := Check{Name: CheckSourceVersion, Status: StatusPass}
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
			pinned, declaredVersion(declared), strings.Join(missingTables, ","))
	case strings.TrimSpace(declared) == "":
		check.Status = StatusBlocked
		check.Code = CodeSourceVersionUndeclared
		check.Detail = fmt.Sprintf("expected=%s declared=none; pass the 1.x release the source was running", pinned)
	case declared != pinned:
		check.Status = StatusBlocked
		check.Code = CodeSourceVersionUnsupported
		check.Detail = fmt.Sprintf("expected=%s declared=%s", pinned, declared)
	case len(newerMarkers) != 0:
		check.Status = StatusBlocked
		check.Code = CodeSourceVersionUnsupported
		check.Detail = fmt.Sprintf("expected=%s declared=%s; markers newer than pinned: %s",
			pinned, declaredVersion(declared), strings.Join(newerMarkers, ","))
	default:
		check.Detail = fmt.Sprintf("expected=%s declared=%s; structural lower bound satisfied", pinned, declared)
	}
	return check
}

const (
	markerIndex   = "index"
	markerSetting = "setting"
)

// sourceMarker is one observation the version check may make against the
// source. label is what the report names for the operator: the marker itself,
// plus the value for a setting.
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

// sourceMarkerKeys are the settings rows the version check reads. The v1 code
// writes them itself; they are revision facts, not credentials.
var sourceMarkerKeys = []string{datamigration.UsageCacheAccountingSemanticsRevisionKey}

func (s sourceSchema) has(marker sourceMarker) bool {
	switch marker.kind {
	case markerIndex:
		return s.indexes[marker.name]
	case markerSetting:
		return s.settings[marker.name] == marker.value
	default:
		return s.tables[marker.name]
	}
}

type versionMarker struct {
	release string
	marker  sourceMarker
}

// laterSourceMarkers lists the markers the 1.x line introduced after the
// pinned release, with the release that introduced each one. None of them is a
// table: the table set is identical across 1.14.x, and every base table in
// requiredLegacyTables is created by the pinned release too. Listing one here —
// usage_archive_deleted_coverage_daily was once mistaken for a v1.14.6 marker —
// refuses a source of the pinned release itself, which is exactly the defect
// the real-Docker harness found. The release is carried so a later re-pin drops
// the markers its own schema accepts instead of refusing itself.
var laterSourceMarkers = []versionMarker{
	{release: "v1.14.6", marker: sourceMarker{
		kind:  markerSetting,
		name:  datamigration.UsageCacheAccountingSemanticsRevisionKey,
		value: strconv.Itoa(datamigration.CurrentUsageCacheAccountingSemanticsRevision),
	}},
	{release: "v1.14.7", marker: sourceMarker{kind: markerIndex, name: "idx_usage_events_ts_latency_ttft_cover"}},
}

// newerMarkers returns the labels of the markers the source carries that the
// pinned release genuinely lacks, in lexical order. A marker introduced at or
// below the pin is part of the schema the pin already accepts, so it can never
// prove that the source moved past it.
func (s sourceSchema) newerMarkers(pinned string) []string {
	newer := []string{}
	pinnedRank, pinnedKnown := parseRelease(pinned)
	if !pinnedKnown {
		return newer
	}
	for _, candidate := range laterSourceMarkers {
		rank, ok := parseRelease(candidate.release)
		if !ok || rank <= pinnedRank {
			continue
		}
		if s.has(candidate.marker) {
			newer = append(newer, candidate.marker.label())
		}
	}
	sort.Strings(newer)
	return newer
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
