package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
)

// Empty optional locations mean not-inspected, never missing.
type InspectionPaths struct{ Database, Config, DataKey, Archives string }
type InspectionOptions struct {
	// Operator assertion that ALL writers have stopped; stat/hash is no fence.
	SourceStopped bool
	// Retained by the caller through native startup, without release/reacquire.
	ManagerLock *processlock.Lock
}
type InspectionFile struct {
	Role  string `json:"role"`
	State string `json:"state"`
	Size  int64  `json:"size,omitempty"`
	Mode  string `json:"mode,omitempty"`
	info  os.FileInfo
}
type InspectionColumn struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	PrimaryKey int    `json:"primaryKey,omitempty"`
}
type InspectionTable struct {
	Name    string             `json:"name"`
	Columns []InspectionColumn `json:"columns"`
}

// Observations never constitute a profile, backup receipt or upgrade grant.
type LegacyInspection struct {
	Files                    []InspectionFile                        `json:"files"`
	SQLite                   string                                  `json:"sqlite"`
	Consistency              string                                  `json:"consistency"`
	Tables                   []InspectionTable                       `json:"tables,omitempty"`
	SchemaFingerprint        string                                  `json:"schemaFingerprint,omitempty"`
	History                  map[string]string                       `json:"history"`
	HistoryRowsObserved      map[string]int                          `json:"historyRowsObserved,omitempty"`
	ConnectionStorageVersion *int                                    `json:"connectionStorageVersion,omitempty"`
	ConnectionStorage        PersistedCPAConnectionStorageInspection `json:"connectionStorage"`
	Issues                   []string                                `json:"issues"`
}

// Restrict work to known tables rather than exposing arbitrary source names.
var inspectionTableNames = []string{
	"settings", "usage_events", "usage_data_migrations", "usage_archive_runs",
	"usage_archive_segments", "usage_archive_event_refs", "usage_archive_deleted_coverage_daily",
	"usage_maintenance_locks", GatewayAPIKeyIdentitiesTable, GatewayAPIKeyMutationIntentsTable,
	GatewayCredentialIdentitiesTable, GatewayAPIKeySourceBindingsTable,
	GatewayCredentialSourceBindingsTable, GatewayCredentialDeleteIntentsTable,
	GatewayCredentialDeleteIntentItemsTable, GatewayUsageIdentityProjectionTable,
	GatewayUsageIdentityProjectionStateTable, GatewayQuotaPoliciesTable,
	GatewayQuotaPolicyRulesTable, GatewayAPIKeyPolicyBindingsTable,
	GatewayQuotaDecisionEventsTable, "gateway_source_binding_revision",
}

func InspectLegacySource(ctx context.Context, paths InspectionPaths, options InspectionOptions) (result LegacyInspection, err error) {
	result = LegacyInspection{SQLite: "not-inspected", Consistency: "unknown", History: map[string]string{
		"rawDeleted": "not-inspected", "archives": "not-inspected",
		"migrations": "not-inspected", "maintenance": "not-inspected",
	}, Issues: []string{}}
	if strings.TrimSpace(paths.Database) == "" {
		return result, errors.New("explicit database path is required")
	}
	paths.Database, err = filepath.Abs(paths.Database)
	if err != nil {
		return result, errors.New("invalid source path")
	}
	paths.Database = filepath.Clean(paths.Database)
	if parent, resolveErr := filepath.EvalSymlinks(filepath.Dir(paths.Database)); resolveErr == nil {
		paths.Database = filepath.Join(parent, filepath.Base(paths.Database))
	} else if !os.IsNotExist(resolveErr) {
		return result, errors.New("source directory cannot be resolved")
	}
	result.Files, err = inspectFiles(paths)
	if err != nil {
		result.Issues = append(result.Issues, "inventory-failed")
		return result, errors.New("source inventory failed")
	}
	// Deferred after db.Close: source comparisons cover both open and closed states.
	defer func() {
		after, checkErr := inspectFiles(paths)
		if checkErr != nil || !sameInspectionFiles(result.Files, after) {
			result.Consistency = "changed"
			result.Issues = append(result.Issues, "source-changed")
			err = errors.Join(err, errors.New("source changed during inspection"))
		}
	}()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	for _, file := range result.Files {
		if file.State == "unsupported" {
			result.Issues = append(result.Issues, "unsupported-source-file")
			return result, nil
		}
	}
	if result.Files[0].State == "missing" {
		return result, nil
	}
	if result.Files[0].Size == 0 {
		result.Issues = append(result.Issues, "empty-database")
		return result, nil
	}
	locked := options.ManagerLock != nil && options.ManagerLock.DatabasePath() == paths.Database
	if locked {
		if err := options.ManagerLock.Validate(); err != nil {
			return result, errors.New("manager ownership fence unavailable")
		}
	}
	if !options.SourceStopped && !locked {
		result.Issues = append(result.Issues, "source-quiescence-unproven")
		return result, nil
	}
	wal, journal := result.Files[1], result.Files[3]
	if journal.State != "missing" && journal.Size > 0 {
		result.Issues = append(result.Issues, "journal-recovery-required")
		return result, nil
	}
	databasePath, immutable := paths.Database, true
	if wal.State != "missing" && wal.Size > 0 {
		if !locked {
			result.Issues = append(result.Issues, "wal-consistency-unproven")
			return result, nil
		}
		if err := validateInspectionWALHeader(paths.Database); err != nil {
			result.Issues = append(result.Issues, "wal-header-invalid")
			return result, nil
		}
		// Existing single-Manager authority establishes quiescence. Matching
		// copied hashes alone would not establish a consistent live snapshot.
		tempDir, tempErr := inspectionTempDirectory(paths)
		if tempErr != nil {
			return result, tempErr
		}
		staging, makeErr := os.MkdirTemp(tempDir, "cpamp-native-inspection-")
		if makeErr != nil {
			return result, errors.New("inspection staging unavailable")
		}
		defer os.RemoveAll(staging)
		databasePath = filepath.Join(staging, "source.sqlite")
		if err := copyInspectionFile(ctx, paths.Database, databasePath); err != nil {
			return result, err
		}
		if err := copyInspectionFile(ctx, paths.Database+"-wal", databasePath+"-wal"); err != nil {
			return result, err
		}
		copied, copyErr := inspectFiles(paths)
		if copyErr != nil || !sameInspectionFiles(result.Files, copied) {
			return result, errors.New("source changed during inspection copy")
		}
		immutable = false
		result.Consistency = "manager-fenced-recovery-copy"
	} else if locked {
		result.Consistency = "manager-fenced-cold"
	} else {
		result.Consistency = "operator-asserted-cold"
	}
	db, openErr := sql.Open("sqlite", inspectionDataSourceName(databasePath, immutable))
	if openErr != nil {
		return result, errors.New("SQLite inspection unavailable")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			err = errors.Join(err, errors.New("SQLite inspection close failed"))
		}
	}()
	if readErr := inspectSchema(ctx, db, &result); readErr != nil {
		result.SQLite = "failed"
		result.Issues = append(result.Issues, "sqlite-inspection-failed")
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, errors.New("SQLite source could not be inspected")
	}
	result.SQLite = "observed"
	storage, storageErr := inspectConnectionStorage(ctx, db, paths.Database)
	if storageErr != nil {
		result.Issues = append(result.Issues, "connection-storage-inspection-failed")
		return result, errors.New("persisted connection storage could not be inspected")
	}
	result.ConnectionStorage = storage
	if readErr := inspectHistory(ctx, db, &result); readErr != nil {
		result.Issues = append(result.Issues, "history-inspection-failed")
		return result, errors.New("source history could not be inspected")
	}
	during, checkErr := inspectFiles(paths)
	if checkErr != nil || !sameInspectionFiles(result.Files, during) {
		return result, errors.New("source changed while SQLite was open")
	}
	if locked {
		if err := options.ManagerLock.Validate(); err != nil {
			return result, errors.New("manager ownership fence changed")
		}
	}
	return result, nil
}

func inspectSchema(ctx context.Context, db *sql.DB, result *LegacyInspection) error {
	for _, name := range inspectionTableNames {
		var exists int
		if err := db.QueryRowContext(ctx, `select exists(select 1 from sqlite_schema where type = 'table' and name = ?)`, name).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			continue
		}
		rows, err := db.QueryContext(ctx, `pragma table_info("`+name+`")`)
		if err != nil {
			return err
		}
		table := InspectionTable{Name: name}
		for rows.Next() {
			var cid, notNull int
			var defaultValue any
			var column InspectionColumn
			if err := rows.Scan(&cid, &column.Name, &column.Type, &notNull, &defaultValue, &column.PrimaryKey); err != nil {
				_ = rows.Close()
				return err
			}
			table.Columns = append(table.Columns, column)
			if len(table.Columns) > 256 {
				_ = rows.Close()
				return errors.New("schema inspection limit exceeded")
			}
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
		result.Tables = append(result.Tables, table)
	}
	sort.Slice(result.Tables, func(i, j int) bool { return result.Tables[i].Name < result.Tables[j].Name })
	encoded, _ := json.Marshal(result.Tables)
	digest := sha256.Sum256(encoded)
	result.SchemaFingerprint = hex.EncodeToString(digest[:])
	return nil
}

func inspectionHasColumns(result *LegacyInspection, name string, columns ...string) bool {
	for _, table := range result.Tables {
		if table.Name != name {
			continue
		}
		for _, wanted := range columns {
			found := false
			for _, column := range table.Columns {
				if column.Name == wanted {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	return false
}

func inspectHistory(ctx context.Context, db *sql.DB, result *LegacyInspection) error {
	result.HistoryRowsObserved = make(map[string]int)
	if inspectionHasColumns(result, "settings", "key", "value") {
		var raw string
		err := db.QueryRowContext(ctx, `select value from settings where key = 'bootstrap_state_v1'`).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			var state struct {
				Version int `json:"connectionStorageMigrationVersion"`
			}
			if err := json.Unmarshal([]byte(raw), &state); err != nil {
				return errors.New("invalid bootstrap state")
			}
			result.ConnectionStorageVersion = &state.Version
		}
	}
	for _, query := range []struct{ key, table string }{
		{"archives", "usage_archive_runs"}, {"migrations", "usage_data_migrations"},
	} {
		if !inspectionHasColumns(result, query.table, "status") {
			continue
		}
		rows, err := db.QueryContext(ctx, `select status from "`+query.table+`" limit 1001`)
		if err != nil {
			return err
		}
		count, pending, unknown := 0, false, false
		for rows.Next() {
			var status string
			if err := rows.Scan(&status); err != nil {
				_ = rows.Close()
				return err
			}
			count++
			switch status {
			case "completed", "cancelled":
			case "pending", "discovering", "running", "failed", "paused", "archived", "verified", "deleting", "online_cleanup", "rebuilding":
				pending = true
			default:
				unknown = true
			}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
		result.History[query.key] = "none-pending-observed"
		result.HistoryRowsObserved[query.key] = count
		if pending {
			result.History[query.key] = "pending-observed"
		}
		if unknown || count > 1000 {
			result.History[query.key] = "unknown"
		}
	}
	for _, query := range []struct{ key, table, column string }{
		{"maintenance", "usage_maintenance_locks", "name"},
		{"rawDeleted", "usage_archive_deleted_coverage_daily", "utc_day"},
	} {
		if !inspectionHasColumns(result, query.table, query.column) {
			continue
		}
		var exists bool
		if err := db.QueryRowContext(ctx, `select exists(select 1 from "`+query.table+`" limit 1)`).Scan(&exists); err != nil {
			return err
		}
		result.History[query.key] = "none-observed"
		if exists {
			result.History[query.key] = "present-observed"
		}
	}
	return nil
}

func inspectFiles(paths InspectionPaths) ([]InspectionFile, error) {
	entries := []struct {
		role, path string
		directory  bool
	}{
		{"database", paths.Database, false}, {"wal", paths.Database + "-wal", false},
		{"shm", paths.Database + "-shm", false}, {"journal", paths.Database + "-journal", false},
		{"config", paths.Config, false}, {"data-key", paths.DataKey, false},
		{"archives", paths.Archives, true}, {"source-directory", filepath.Dir(paths.Database), true},
		{"process-lock", paths.Database + ".manager.lock", false},
	}
	files := make([]InspectionFile, 0, len(entries))
	for _, entry := range entries {
		file := InspectionFile{Role: entry.role, State: "not-inspected"}
		if entry.path == "" {
			files = append(files, file)
			continue
		}
		info, err := os.Lstat(entry.path)
		if os.IsNotExist(err) {
			file.State = "missing"
			files = append(files, file)
			continue
		}
		if err != nil {
			return nil, err
		}
		file.info, file.Size, file.Mode = info, info.Size(), info.Mode().String()
		file.State = "present"
		if (entry.directory && !info.IsDir()) || (!entry.directory && !info.Mode().IsRegular()) {
			file.State = "unsupported"
		}
		files = append(files, file)
	}
	return files, nil
}

func sameInspectionFiles(before, after []InspectionFile) bool {
	if len(before) != len(after) {
		return false
	}
	for i, a := range before {
		b := after[i]
		if a.State != b.State || a.Size != b.Size || a.Mode != b.Mode {
			return false
		}
		if a.info != nil && (b.info == nil || !os.SameFile(a.info, b.info) || !a.info.ModTime().Equal(b.info.ModTime())) {
			return false
		}
	}
	return true
}

// Only ownership metadata may differ between provisional and fenced reads.
func SameInspectionSource(before, after LegacyInspection) bool {
	if len(before.Files) != len(after.Files) || before.SchemaFingerprint != after.SchemaFingerprint {
		return false
	}
	return sameInspectionFiles(before.Files[:7], after.Files[:7])
}

func inspectionDataSourceName(path string, immutable bool) string {
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := &url.URL{Scheme: "file", Path: uriPath}
	q := dsn.Query()
	q.Set("mode", "ro")
	if immutable {
		q.Set("immutable", "1")
	}
	q.Add("_pragma", "query_only(1)")
	dsn.RawQuery = q.Encode()
	return dsn.String()
}

type inspectionContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *inspectionContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Validate the small WAL header before letting SQLite inspect a copy. A malformed
// header must not silently become a successful read of the checkpointed DB.
// SQLite owns committed-frame validation and native recovery; this is not a
// complete integrity check, legacy recovery authorization or backup validator.
func validateInspectionWALHeader(databasePath string) error {
	wal, err := os.Open(databasePath + "-wal")
	if err != nil {
		return err
	}
	defer wal.Close()
	var header [32]byte
	if _, err := io.ReadFull(wal, header[:]); err != nil {
		return err
	}
	magic := binary.BigEndian.Uint32(header[:4])
	pageSize := binary.BigEndian.Uint32(header[8:12])
	if (magic != 0x377f0682 && magic != 0x377f0683) || binary.BigEndian.Uint32(header[4:8]) != 3007000 || pageSize < 512 || pageSize > 65536 || pageSize&(pageSize-1) != 0 {
		return errors.New("invalid WAL header")
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
	db, err := os.Open(databasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	var dbHeader [18]byte
	if _, err := io.ReadFull(db, dbHeader[:]); err != nil {
		return err
	}
	dbPageSize := uint32(binary.BigEndian.Uint16(dbHeader[16:18]))
	if dbPageSize == 1 {
		dbPageSize = 65536
	}
	if dbPageSize != pageSize {
		return errors.New("WAL/database page size mismatch")
	}
	return nil
}

func inspectionTempDirectory(paths InspectionPaths) (string, error) {
	tempDir, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return "", errors.New("inspection staging unavailable")
	}
	tempDir, err = filepath.Abs(tempDir)
	if err != nil {
		return "", errors.New("inspection staging unavailable")
	}
	sourceDirs := []string{filepath.Dir(paths.Database), paths.Archives}
	for _, file := range []string{paths.Config, paths.DataKey} {
		if file != "" {
			sourceDirs = append(sourceDirs, filepath.Dir(file))
		}
	}
	for _, sourceDir := range sourceDirs {
		if sourceDir == "" {
			continue
		}
		sourceDir, err = filepath.Abs(sourceDir)
		if err != nil {
			return "", err
		}
		if resolved, resolveErr := filepath.EvalSymlinks(sourceDir); resolveErr == nil {
			sourceDir = resolved
		}
		relative, relErr := filepath.Rel(sourceDir, tempDir)
		if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return "", errors.New("inspection staging must be outside source directories")
		}
	}
	return tempDir, nil
}

func copyInspectionFile(ctx context.Context, source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return errors.New("inspection copy source unavailable")
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("inspection copy destination unavailable")
	}
	_, copyErr := io.Copy(output, &inspectionContextReader{ctx: ctx, reader: input})
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		return errors.New("inspection copy failed")
	}
	return nil
}
