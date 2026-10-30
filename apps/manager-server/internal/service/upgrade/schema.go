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
// same tables, and the only per-release values v1 writes into settings are
// format revisions of derived rollups, each shared by several releases. The
// cache-accounting semantics revision, for one, is 2 in v1.14.4 and v1.14.5 and
// 3 from v1.14.6 on, so a settings value is a floor at release granularity and
// never an identity. Preflight therefore never infers a release from DDL. It
// requires a structural lower bound — a source below the v1.14 shape is refused
// no matter what release it declares — and it refuses a source that carries a
// marker the pinned release genuinely lacks, because that source has already
// moved past the pin.
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
//
// The newer branch deliberately repeats neither the pin nor the declaration.
// Printing "expected=v1.14.4 declared=v1.14.4" next to a refusal reads as if the
// check contradicted itself; what actually decided it is the marker, so the
// detail names the marker and the release that introduced it.
func versionCheck(kind string, v2Tables []string, missingTables []string, newer []markerEvidence, declared string, pinned string) Check {
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
	case len(newer) != 0:
		check.Status = StatusBlocked
		check.Code = CodeSourceVersionUnsupported
		labels := make([]string, len(newer))
		for index, evidence := range newer {
			labels[index] = evidence.String()
		}
		check.Detail = fmt.Sprintf("source is newer than the pinned release %s; markers from later releases: %s",
			pinned, strings.Join(labels, ", "))
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
func (s sourceSchema) observedLabel(marker sourceMarker) (string, bool) {
	switch marker.kind {
	case markerIndex:
		return marker.name, s.indexes[marker.name]
	case markerSetting:
		observed, found := s.settings[marker.name]
		if !found {
			return "", false
		}
		value, err := strconv.Atoi(strings.TrimSpace(observed))
		if err != nil || value < marker.floor {
			return "", false
		}
		return marker.name + "=" + observed, true
	default:
		return marker.name, s.tables[marker.name]
	}
}

// sourceMarkerKeys are the settings rows the version check reads. The v1 code
// writes them itself; they are revision facts, not credentials.
var sourceMarkerKeys = []string{datamigration.UsageCacheAccountingSemanticsRevisionKey}

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
type versionLadder struct {
	after []versionMarker
}

func newVersionLadder(pinned string) (versionLadder, error) {
	pinnedRank, err := parseRelease(pinned)
	if err != nil {
		return versionLadder{}, fmt.Errorf("pinned source release: %w", err)
	}
	ladder := versionLadder{}
	for _, candidate := range laterSourceMarkers {
		rank, err := parseRelease(candidate.release)
		if err != nil {
			return versionLadder{}, fmt.Errorf("later marker release: %w", err)
		}
		if rank <= pinnedRank {
			continue
		}
		ladder.after = append(ladder.after, candidate)
	}
	return ladder, nil
}

// newer returns the markers the source carries that the pinned release
// genuinely lacks, in the order the ladder lists them. A marker introduced at
// or below the pin is part of the schema the pin already accepts, so it can
// never prove that the source moved past it.
func (l versionLadder) newer(s sourceSchema) []markerEvidence {
	newer := []markerEvidence{}
	for _, candidate := range l.after {
		label, ok := s.observedLabel(candidate.marker)
		if !ok {
			continue
		}
		newer = append(newer, markerEvidence{label: label, release: candidate.release})
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
