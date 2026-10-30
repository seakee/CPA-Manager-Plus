package upgrade

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
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
// infers a release from DDL; it accepts exactly the release the operator
// declares (Options.DeclaredVersion) and blocks everything else.
type sourceSchema struct {
	tables map[string]bool
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
	schema := sourceSchema{tables: map[string]bool{}}
	rows, err := db.QueryContext(ctx, `select name from sqlite_master where type = 'table'`)
	if err != nil {
		return sourceSchema{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return sourceSchema{}, err
		}
		schema.tables[name] = true
	}
	if err := rows.Err(); err != nil {
		return sourceSchema{}, err
	}
	return schema, nil
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

// versionCheck decides the source version check from the declaration alone. The
// database is not consulted: v1 never persisted its release, so a value read
// out of the source would be a guess dressed as evidence.
func versionCheck(kind string, v2Tables []string, declared string, pinned string) Check {
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
	case strings.TrimSpace(declared) == "":
		check.Status = StatusBlocked
		check.Code = CodeSourceVersionUndeclared
		check.Detail = fmt.Sprintf("expected=%s declared=none; pass the 1.x release the source was running", pinned)
	case declared != pinned:
		check.Status = StatusBlocked
		check.Code = CodeSourceVersionUnsupported
		check.Detail = fmt.Sprintf("expected=%s declared=%s", pinned, declared)
	default:
		check.Detail = fmt.Sprintf("expected=%s declared=%s", pinned, declared)
	}
	return check
}
