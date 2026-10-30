package upgrade

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

const msPerDay = int64(86_400_000)

// v1BaseSchema is the bounded subset of the 1.x schema the preflight may read.
// It is deliberately hand-written: a fixture built by the v2 migration would
// not represent a 1.x source.
var v1BaseSchema = []string{
	`create table settings (key text primary key, value text not null, updated_at_ms integer not null)`,
	`create table usage_events (id integer primary key autoincrement, event_hash text not null unique, timestamp_ms integer not null)`,
	`create table usage_event_identity_ledger (event_hash text primary key, raw_event_id integer, timestamp_ms integer, bucket_ms integer)`,
	`create table usage_monitoring_event_projection_v1 (event_id integer primary key, timestamp_ms integer not null)`,
	`create table usage_monitoring_rollup_state (
		rollup_name text primary key,
		schema_version integer not null,
		status text not null,
		coverage_event_id integer not null default 0,
		updated_at_ms integer not null default 0
	)`,
	`create table usage_archive_event_refs (
		event_hash text primary key,
		run_id text not null,
		segment_sequence integer not null,
		raw_event_id integer not null,
		timestamp_ms integer not null,
		archived_at_ms integer not null,
		raw_deleted_at_ms integer
	)`,
	`insert into usage_monitoring_rollup_state (rollup_name, schema_version, status, coverage_event_id) values ('projection_v1', 1, 'ready', 100)`,
	`insert into usage_events (event_hash, timestamp_ms) values ('live-event', 1000)`,
}

type sourceFixture struct {
	root   string
	dbPath string
}

// newSourceFixture builds a synthetic 1.x data directory: usage.sqlite with the
// v1 tables, data.key, an archive tree and the v1 ownership lock file.
func newSourceFixture(t *testing.T, configure func(t *testing.T, db *sql.DB)) sourceFixture {
	t.Helper()
	root := t.TempDir()
	writeFixtureFile(t, filepath.Join(root, archivesDirName, "2026-03", "segment-000001.jsonl"), `{"events":1}`+"\n")
	writeFixtureFile(t, filepath.Join(root, archivesDirName, "2026-03", "segment-000002.jsonl"), `{"events":2}`+"\n")
	writeFixtureFile(t, filepath.Join(root, dataKeyFileName), "fixture-data-key\n")

	dbPath := filepath.Join(root, databaseFileName)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	db.SetMaxOpenConns(1)
	for _, statement := range v1BaseSchema {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("apply fixture schema: %v", err)
		}
	}
	if configure != nil {
		configure(t, db)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}
	writeFixtureFile(t, dbPath+".manager.lock", "")
	return sourceFixture{root: root, dbPath: dbPath}
}

func writeFixtureFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
}

func execFixture(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatalf("fixture statement failed: %v", err)
	}
}

// archivedRef inserts one archived event whose raw row is gone.
func archivedRef(t *testing.T, db *sql.DB, eventHash string, rawEventID int64, timestampMS int64) {
	t.Helper()
	execFixture(t, db, `insert into usage_event_identity_ledger (event_hash, raw_event_id, timestamp_ms, bucket_ms)
		values (?, ?, ?, 0)`, eventHash, rawEventID, timestampMS)
	execFixture(t, db, `insert into usage_archive_event_refs (
		event_hash, run_id, segment_sequence, raw_event_id, timestamp_ms, archived_at_ms, raw_deleted_at_ms
	) values (?, 'run-1', 1, ?, ?, ?, ?)`, eventHash, rawEventID, timestampMS, timestampMS, timestampMS)
}

func addProjection(t *testing.T, db *sql.DB, eventID int64, timestampMS int64) {
	t.Helper()
	execFixture(t, db, `insert into usage_monitoring_event_projection_v1 (event_id, timestamp_ms) values (?, ?)`, eventID, timestampMS)
}

func withNewerThanPinned(t *testing.T, db *sql.DB) {
	t.Helper()
	execFixture(t, db, `create table usage_archive_deleted_coverage_daily (
		utc_day integer primary key,
		deleted_event_count integer not null,
		min_timestamp_ms integer not null,
		max_timestamp_ms integer not null
	)`)
	execFixture(t, db, `insert into settings (key, value, updated_at_ms) values ('usage_cache_accounting_semantics_revision', '3', 1)`)
	execFixture(t, db, `create index idx_usage_events_ts_latency_ttft_cover on usage_events(timestamp_ms)`)
}

func preflight(t *testing.T, root string, options Options) Report {
	t.Helper()
	options.LegacyRoot = root
	report, err := Preflight(context.Background(), options)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	return report
}

func checkByName(report Report, name string) (Check, bool) {
	for _, check := range report.Checks {
		if check.Name == name {
			return check, true
		}
	}
	return Check{}, false
}

func TestPreflightReadyOnBaseV1Source(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	report := preflight(t, fixture.root, Options{})
	if !report.Ready {
		t.Fatalf("ready = false, blocked = %v", report.Blocked)
	}
	if len(report.Blocked) != 0 {
		t.Fatalf("blocked = %v, want none", report.Blocked)
	}
	if report.SourceKind != SourceKindLegacyManager {
		t.Fatalf("source kind = %q, want %q", report.SourceKind, SourceKindLegacyManager)
	}
	if report.SourceVersion != "v1.14.0" {
		t.Fatalf("source version = %q, want v1.14.0", report.SourceVersion)
	}
	if len(report.Fingerprint) != 64 {
		t.Fatalf("fingerprint = %q, want 64 hex characters", report.Fingerprint)
	}
	wantChecks := []string{CheckSourceOwnership, CheckSourceVersion, CheckFingerprint, CheckDisk, CheckCPAClassification, CheckParity}
	if len(report.Checks) != len(wantChecks) {
		t.Fatalf("checks = %d, want %d: %+v", len(report.Checks), len(wantChecks), report.Checks)
	}
	for index, name := range wantChecks {
		if report.Checks[index].Name != name {
			t.Fatalf("check %d = %q, want %q", index, report.Checks[index].Name, name)
		}
		if report.Checks[index].Status != StatusPass {
			t.Fatalf("check %q status = %q, want pass", name, report.Checks[index].Status)
		}
	}
	if report.ParityGaps.any() || len(report.ParityGaps.AffectedDates) != 0 {
		t.Fatalf("parity gaps = %+v, want none", report.ParityGaps)
	}
}

func TestPreflightReportsSourceBusyWhenLockIsHeld(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	held, err := processlock.AcquireExisting(fixture.dbPath)
	if err != nil {
		t.Fatalf("hold source lock: %v", err)
	}
	defer func() { _ = held.Close() }()

	report := preflight(t, fixture.root, Options{})
	if report.Ready {
		t.Fatal("ready = true while the source lock is held")
	}
	if len(report.Blocked) != 1 || report.Blocked[0] != CodeSourceBusy {
		t.Fatalf("blocked = %v, want [%s]", report.Blocked, CodeSourceBusy)
	}
	check, found := checkByName(report, CheckSourceOwnership)
	if !found {
		t.Fatal("source ownership check missing")
	}
	if check.Status != StatusBlocked || check.Code != CodeSourceBusy {
		t.Fatalf("ownership check = %+v, want blocked %s", check, CodeSourceBusy)
	}
}

// TestPreflightConcurrentReadersBothReachAVerdict proves the read-only preflight
// does not fence the source. Two inspections of the same source at the same time
// both observe it, and neither reports the other as a busy writer.
func TestPreflightConcurrentReadersBothReachAVerdict(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	const readers = 4
	type verdict struct {
		report Report
		err    error
	}
	results := make(chan verdict, readers)
	release := make(chan struct{})
	var group sync.WaitGroup
	for range readers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-release
			report, err := Preflight(context.Background(), Options{LegacyRoot: fixture.root})
			results <- verdict{report: report, err: err}
		}()
	}
	close(release)
	group.Wait()
	close(results)
	seen := 0
	for got := range results {
		seen++
		if got.err != nil {
			t.Fatalf("concurrent preflight: %v", got.err)
		}
		if !got.report.Ready {
			t.Fatalf("concurrent preflight blocked = %v", got.report.Blocked)
		}
		for _, code := range got.report.Blocked {
			if code == CodeSourceBusy {
				t.Fatalf("a concurrent reader was reported as a live writer")
			}
		}
		if check, found := checkByName(got.report, CheckSourceOwnership); !found || check.Status != StatusPass {
			t.Fatalf("ownership check = %+v", check)
		}
	}
	if seen != readers {
		t.Fatalf("verdicts = %d, want %d", seen, readers)
	}
}

// TestPreflightReadsAReadOnlySourceTree proves the source volume an installation
// mounts read-only still yields a real report: the observation needs no writable
// marker and writes nothing to the source.
func TestPreflightReadsAReadOnlySourceTree(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	before := sourceContents(t, fixture.root)
	chmodTree(t, fixture.root, 0o555, 0o444)
	t.Cleanup(func() { chmodTree(t, fixture.root, 0o755, 0o644) })

	report := preflight(t, fixture.root, Options{})
	if !report.Ready {
		t.Fatalf("read-only source blocked = %v", report.Blocked)
	}
	if check, found := checkByName(report, CheckSourceOwnership); !found || check.Status != StatusPass {
		t.Fatalf("ownership check = %+v", check)
	}
	if after := sourceContents(t, fixture.root); !reflect.DeepEqual(before, after) {
		t.Fatalf("preflight changed the source: before %+v after %+v", before, after)
	}
}

// TestVerifyAdminUsesTheSourceCredential proves the credential the bootstrap
// Upgrade entry verified is the only one this read accepts, and that a source
// with no credential proves nothing.
func TestVerifyAdminUsesTheSourceCredential(t *testing.T) {
	const key = "legacy-manager-credential"
	fixture := newSourceFixture(t, func(t *testing.T, db *sql.DB) {
		credential, err := security.NewAdminCredential(key, "test")
		if err != nil {
			t.Fatalf("build credential: %v", err)
		}
		raw, err := json.Marshal(credential)
		if err != nil {
			t.Fatalf("encode credential: %v", err)
		}
		execFixture(t, db, `insert into settings (key, value, updated_at_ms) values ('admin_credential_v1', ?, 1)`, string(raw))
	})
	if err := VerifyAdmin(context.Background(), fixture.root, key); err != nil {
		t.Fatalf("verify the source credential: %v", err)
	}
	for _, candidate := range []string{"", "another-Manager-key"} {
		if err := VerifyAdmin(context.Background(), fixture.root, candidate); !errors.Is(err, ErrAdminUnauthorized) {
			t.Fatalf("candidate %q = %v, want %v", candidate, err, ErrAdminUnauthorized)
		}
	}
	unrecorded := newSourceFixture(t, nil)
	if err := VerifyAdmin(context.Background(), unrecorded.root, key); !errors.Is(err, ErrAdminUnauthorized) {
		t.Fatalf("source without a credential = %v, want %v", err, ErrAdminUnauthorized)
	}
	if err := VerifyAdmin(context.Background(), filepath.Join(fixture.root, "absent"), key); err == nil {
		t.Fatal("absent root verified a credential")
	}
}

// sourceContents reads every file under a source root so a test can prove the
// observation wrote nothing.
func sourceContents(t *testing.T, root string) map[string]string {
	t.Helper()
	contents := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		contents[path] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatalf("read source contents: %v", err)
	}
	return contents
}

// chmodTree rewrites the mode of a fixture tree so a test can mount it read-only.
func chmodTree(t *testing.T, root string, directoryMode, fileMode os.FileMode) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(path, directoryMode)
		}
		if entry.Type().IsRegular() {
			return os.Chmod(path, fileMode)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chmod fixture tree: %v", err)
	}
}

func TestPreflightReportsUninspectableOwnershipMarker(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	if err := os.Remove(fixture.dbPath + ".manager.lock"); err != nil {
		t.Fatalf("remove lock file: %v", err)
	}
	report := preflight(t, fixture.root, Options{})
	// Nothing is holding the source, so this is not a busy source: the marker
	// whose absence the read-only observation cannot read is a distinct code.
	if report.Ready || len(report.Blocked) != 1 || report.Blocked[0] != CodeSourceLockUnavailable {
		t.Fatalf("blocked = %v ready = %t, want [%s]", report.Blocked, report.Ready, CodeSourceLockUnavailable)
	}
	check, found := checkByName(report, CheckSourceOwnership)
	if !found || check.Status != StatusBlocked || check.Code != CodeSourceLockUnavailable {
		t.Fatalf("ownership check = %+v, want blocked %s", check, CodeSourceLockUnavailable)
	}
}

func TestPreflightRejectsSourceNewerThanPinned(t *testing.T) {
	fixture := newSourceFixture(t, withNewerThanPinned)
	report := preflight(t, fixture.root, Options{})
	if report.SourceVersion != "v1.14.7" {
		t.Fatalf("source version = %q, want v1.14.7", report.SourceVersion)
	}
	if report.Ready {
		t.Fatal("ready = true for a source newer than the pinned release")
	}
	if len(report.Blocked) != 1 || report.Blocked[0] != CodeSourceVersionUnsupported {
		t.Fatalf("blocked = %v, want [%s]", report.Blocked, CodeSourceVersionUnsupported)
	}
	check, found := checkByName(report, CheckSourceVersion)
	if !found || check.Status != StatusBlocked || check.Code != CodeSourceVersionUnsupported {
		t.Fatalf("version check = %+v", check)
	}
}

// largeCredentialSource builds a source whose database is large enough for a
// whole-file integrity scan to dominate, then corrupts the root page of its
// padding table. The integrity check must fail while the pages the credential
// row lives on stay readable, which is what separates "this read proves the
// credential" from "this read verifies the whole database".
func largeCredentialSource(t *testing.T, key string, megabytes int) sourceFixture {
	t.Helper()
	credential, err := security.NewAdminCredential(key, "test")
	if err != nil {
		t.Fatalf("build credential: %v", err)
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatalf("encode credential: %v", err)
	}
	paddingRoot := 0
	fixture := newSourceFixture(t, func(t *testing.T, db *sql.DB) {
		execFixture(t, db, `insert into settings (key, value, updated_at_ms) values ('admin_credential_v1', ?, 1)`, string(raw))
		execFixture(t, db, `create table padding (id integer primary key, payload blob)`)
		for range megabytes {
			execFixture(t, db, `insert into padding (payload) values (zeroblob(1048576))`)
		}
		if err := db.QueryRow(`select rootpage from sqlite_master where name = 'padding'`).Scan(&paddingRoot); err != nil {
			t.Fatalf("read the padding root page: %v", err)
		}
	})
	corruptDatabasePage(t, fixture.dbPath, paddingRoot)
	return fixture
}

// corruptDatabasePage overwrites one b-tree page with bytes no page type
// accepts. The damage is confined to the tree that page belongs to: the
// integrity check reports it, and every other tree still reads.
func corruptDatabasePage(t *testing.T, path string, page int) {
	t.Helper()
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture database: %v", err)
	}
	pageSize := int(binary.BigEndian.Uint16(file[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	offset := (page - 1) * pageSize
	if page < 2 || offset+pageSize > len(file) {
		t.Fatalf("page %d does not fit a %d-byte database", page, len(file))
	}
	for index := offset; index < offset+pageSize; index++ {
		file[index] = 0xff
	}
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatalf("corrupt fixture database: %v", err)
	}
}

const (
	// credentialFixtureMegabytes keeps the fixture large enough that a whole-file
	// scan, not the single credential page, is what a source of this shape costs.
	credentialFixtureMegabytes = 256
	// credentialBudget is far below a whole-file scan of that fixture and far
	// below the credential read's own timeout.
	credentialBudget = time.Second
)

// TestVerifyAdminReadsOnlyTheCredentialRow is the R1 contract at the service
// layer: a source whose whole-file integrity check fails, and whose size dwarfs
// any credential budget, still proves the right key quickly. Preflight, which
// does qualify the source, still runs the check and blocks on it.
func TestVerifyAdminReadsOnlyTheCredentialRow(t *testing.T) {
	const key = "legacy-manager-credential"
	fixture := largeCredentialSource(t, key, credentialFixtureMegabytes)
	// The corruption is real: the qualification path cannot read this file.
	if db, _, err := openReadOnlySource(context.Background(), fixture.dbPath); err == nil {
		_ = db.Close()
		t.Fatal("the fixture passed the whole-database integrity check")
	}

	started := time.Now()
	if err := VerifyAdmin(context.Background(), fixture.root, key); err != nil {
		t.Fatalf("verify the source credential: %v", err)
	}
	if elapsed := time.Since(started); elapsed > credentialBudget {
		t.Fatalf("credential read took %s, budget %s", elapsed, credentialBudget)
	} else {
		t.Logf("credential read on a %d MB source: %s", credentialFixtureMegabytes, elapsed)
	}
	if err := VerifyAdmin(context.Background(), fixture.root, "another-Manager-key"); !errors.Is(err, ErrAdminUnauthorized) {
		t.Fatalf("foreign key = %v, want %v", err, ErrAdminUnauthorized)
	}

	report := preflight(t, fixture.root, Options{})
	if report.Ready || len(report.Blocked) != 1 || report.Blocked[0] != CodeSourceUnreadable {
		t.Fatalf("blocked = %v, want [%s]", report.Blocked, CodeSourceUnreadable)
	}
}

// newCredentialFixture writes a 1.x source whose administrator credential is key.
func newCredentialFixture(t *testing.T, key string) sourceFixture {
	t.Helper()
	credential, err := security.NewAdminCredential(key, "test")
	if err != nil {
		t.Fatalf("build credential: %v", err)
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatalf("encode credential: %v", err)
	}
	return newSourceFixture(t, func(t *testing.T, db *sql.DB) {
		execFixture(t, db, `insert into settings (key, value, updated_at_ms) values ('admin_credential_v1', ?, 1)`, string(raw))
	})
}

// TestSourceDatabaseFilePolicyIsSharedByBothPaths proves the preflight and the
// credential read apply one file policy: a source database reached through a
// symbolic link is refused by both, with the same code, because the file that is
// inspected has to be the file that is read.
func TestSourceDatabaseFilePolicyIsSharedByBothPaths(t *testing.T) {
	const key = "legacy-manager-credential"
	fixture := newCredentialFixture(t, key)
	real := fixture.dbPath + ".real"
	if err := os.Rename(fixture.dbPath, real); err != nil {
		t.Fatalf("move fixture database: %v", err)
	}
	if err := os.Symlink(real, fixture.dbPath); err != nil {
		t.Skipf("create database symlink: %v", err)
	}

	err := VerifyAdmin(context.Background(), fixture.root, key)
	if !errors.Is(err, ErrSourceSymlinked) {
		t.Fatalf("credential read through a symlink = %v, want %v", err, ErrSourceSymlinked)
	}
	if errors.Is(err, ErrAdminUnauthorized) || errors.Is(err, ErrSourceDatabaseMissing) {
		t.Fatalf("a symlinked source was misreported as %v", err)
	}

	report := preflight(t, fixture.root, Options{})
	// The ownership check names the link with the very code the credential read
	// returns. The fingerprint step refuses the same file for its own reason (it
	// hashes plain files), so it contributes its own code as well.
	if report.Ready || len(report.Blocked) == 0 || report.Blocked[0] != CodeSourceSymlinked {
		t.Fatalf("blocked = %v, want [%s ...]", report.Blocked, CodeSourceSymlinked)
	}
	for _, code := range report.Blocked {
		if code == CodeSourceLockUnavailable || code == CodeSourceMissing || code == CodeSourceBusy {
			t.Fatalf("a symlinked source was reported as %s: %v", code, report.Blocked)
		}
	}
	check, found := checkByName(report, CheckSourceOwnership)
	if !found || check.Status != StatusBlocked || check.Code != CodeSourceSymlinked {
		t.Fatalf("ownership check = %+v, want blocked %s", check, CodeSourceSymlinked)
	}
}

// TestMountDatabaseAppliesTheCredentialFilePolicy proves the surface's mount
// lookup asks exactly the question the credential read will ask: the path must
// name the database itself, with content, never a link. A mount the policy
// refuses does not count as holding a database, so the one credential resolution
// falls back to the Manager store instead of pointing the read at a file it would
// refuse — which is what bricked the entry page. The refusal carries the code the
// preflight reports for the same mount.
func TestMountDatabaseAppliesTheCredentialFilePolicy(t *testing.T) {
	const (
		key          = "legacy-manager-credential"
		managerStore = "/srv/cpamp/usage.sqlite"
	)
	// No mount at all: the Manager store is the only credential file, and there is
	// no mount condition to name.
	if database, refusal := MountDatabase(""); database != "" || refusal != "" {
		t.Fatalf("MountDatabase(no mount) = %q, %q; want both empty", database, refusal)
	}
	if database := CredentialDatabase("", managerStore); database != managerStore {
		t.Fatalf("CredentialDatabase(no mount) = %q, want the Manager store %q", database, managerStore)
	}

	cases := []struct {
		name    string
		prepare func(t *testing.T, fixture sourceFixture)
		refusal string
	}{
		{name: "genuine database", prepare: func(t *testing.T, fixture sourceFixture) {}},
		{name: "symbolic link", prepare: func(t *testing.T, fixture sourceFixture) {
			real := fixture.dbPath + ".real"
			if err := os.Rename(fixture.dbPath, real); err != nil {
				t.Fatalf("move fixture database: %v", err)
			}
			if err := os.Symlink(real, fixture.dbPath); err != nil {
				t.Skipf("create database symlink: %v", err)
			}
		}, refusal: CodeSourceSymlinked},
		{name: "hard link", prepare: func(t *testing.T, fixture sourceFixture) {
			if err := os.Link(fixture.dbPath, fixture.dbPath+".alias"); err != nil {
				t.Skipf("create database hard link: %v", err)
			}
		}, refusal: CodeSourceLockUnavailable},
		{name: "empty database", prepare: func(t *testing.T, fixture sourceFixture) {
			if err := os.WriteFile(fixture.dbPath, nil, 0o600); err != nil {
				t.Fatalf("empty the fixture database: %v", err)
			}
		}, refusal: CodeSourceMissing},
		{name: "absent database", prepare: func(t *testing.T, fixture sourceFixture) {
			if err := os.Remove(fixture.dbPath); err != nil {
				t.Fatalf("remove the fixture database: %v", err)
			}
		}, refusal: CodeSourceMissing},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newCredentialFixture(t, key)
			testCase.prepare(t, fixture)

			database, refusal := MountDatabase(fixture.root)
			resolved := CredentialDatabase(fixture.root, managerStore)
			if testCase.refusal == "" {
				if refusal != "" || database != fixture.dbPath || resolved != fixture.dbPath {
					t.Fatalf("MountDatabase(genuine mount) = %q, %q; resolved %q", database, refusal, resolved)
				}
			} else {
				if refusal != testCase.refusal {
					t.Fatalf("MountDatabase refusal = %q, want %q", refusal, testCase.refusal)
				}
				if database != "" || resolved != managerStore {
					t.Fatalf("a refused mount resolved to %q (database %q); want the Manager store %q", resolved, database, managerStore)
				}
			}

			// One condition, one code: the report the operator reads names the same
			// condition the mount lookup refused.
			report := preflight(t, fixture.root, Options{})
			named := false
			for _, code := range report.Blocked {
				if code == testCase.refusal {
					named = true
				}
			}
			if testCase.refusal != "" && !named {
				t.Fatalf("preflight blocked = %v, want it to name %s", report.Blocked, testCase.refusal)
			}
			if testCase.refusal == "" && !report.Ready {
				t.Fatalf("genuine mount is not ready: %v", report.Blocked)
			}
		})
	}
}

// TestVerifyAdminRefusesASwapBetweenInspectionAndRead proves the credential that
// is compared belongs to the file that was inspected. SQLite opens lazily, so
// the swap lands between the inspection and the first read; the read itself
// still succeeds, and the identity comparison is what refuses the credential.
func TestVerifyAdminRefusesASwapBetweenInspectionAndRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows refuses to replace an open file, so the swap cannot be staged")
	}
	const inspectedKey = "legacy-manager-credential"
	const replacementKey = "another-manager-credential"
	fixture := newCredentialFixture(t, inspectedKey)
	replacement := newCredentialFixture(t, replacementKey)

	source, err := openCredentialSource(fixture.dbPath)
	if err != nil {
		t.Fatalf("open credential source: %v", err)
	}
	defer func() { _ = source.close() }()
	if err := os.Rename(replacement.dbPath, fixture.dbPath); err != nil {
		t.Fatalf("swap fixture database: %v", err)
	}
	// The replacement carries a key of its own: without the identity comparison
	// this read would prove a credential from a file nothing inspected.
	err = verifyAdminCredential(context.Background(), source, replacementKey)
	if !errors.Is(err, ErrSourceDatabaseChanged) {
		t.Fatalf("swapped source = %v, want %v", err, ErrSourceDatabaseChanged)
	}
	if errors.Is(err, ErrAdminUnauthorized) {
		t.Fatalf("the swap was reported as a credential verdict: %v", err)
	}
}

// TestVerifyAdminReportsADeadlineAsATimeout is the L-a contract: a healthy
// source that outlives the credential budget is a deadline, never an unreadable
// source and never a wrong key. The bound is placed inside the read, so the
// failure happens after the source opened.
func TestVerifyAdminReportsADeadlineAsATimeout(t *testing.T) {
	const key = "legacy-manager-credential"
	fixture := newCredentialFixture(t, key)
	previous := credentialVerifyTimeout
	credentialVerifyTimeout = -time.Second
	t.Cleanup(func() { credentialVerifyTimeout = previous })

	err := VerifyAdmin(context.Background(), fixture.root, key)
	if !errors.Is(err, ErrCredentialVerifyTimeout) {
		t.Fatalf("expired credential read = %v, want %v", err, ErrCredentialVerifyTimeout)
	}
	if errors.Is(err, ErrSourceDatabaseMissing) || errors.Is(err, ErrAdminUnauthorized) || errors.Is(err, ErrSourceDatabaseChanged) {
		t.Fatalf("a deadline was misreported as %v", err)
	}
}

func TestPreflightReportsMissingDatabase(t *testing.T) {
	root := t.TempDir()
	report := preflight(t, root, Options{})
	if report.Ready {
		t.Fatal("ready = true without a source database")
	}
	if report.SourceKind != SourceKindMissingDatabase {
		t.Fatalf("source kind = %q, want %q", report.SourceKind, SourceKindMissingDatabase)
	}
	// An empty directory has neither the ownership marker nor the database. The
	// absent database is what the operator has to fix, so it is named as itself;
	// the ownership marker is a separate code and only when a source exists.
	wantBlocked := []string{CodeSourceMissing}
	if len(report.Blocked) != len(wantBlocked) {
		t.Fatalf("blocked = %v, want %v", report.Blocked, wantBlocked)
	}
	for index, code := range wantBlocked {
		if report.Blocked[index] != code {
			t.Fatalf("blocked = %v, want %v", report.Blocked, wantBlocked)
		}
	}
	check, found := checkByName(report, CheckSourceOwnership)
	if !found || check.Status != StatusBlocked || check.Code != CodeSourceMissing {
		t.Fatalf("ownership check = %+v, want blocked %s", check, CodeSourceMissing)
	}
	if !strings.Contains(check.Detail, "missing") {
		t.Fatalf("ownership detail = %q, want the missing database named", check.Detail)
	}
}

func TestPreflightFingerprintIsDeterministicAndDetectsChanges(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	first := preflight(t, fixture.root, Options{})
	second := preflight(t, fixture.root, Options{})
	if first.Fingerprint == "" || first.Fingerprint != second.Fingerprint {
		t.Fatalf("fingerprints differ between identical runs: %q vs %q", first.Fingerprint, second.Fingerprint)
	}

	archive := filepath.Join(fixture.root, archivesDirName, "2026-03", "segment-000001.jsonl")
	if err := os.WriteFile(archive, []byte(`{"events":99}`+"\n"), 0o600); err != nil {
		t.Fatalf("rewrite archive file: %v", err)
	}
	afterArchive := preflight(t, fixture.root, Options{})
	if afterArchive.Fingerprint == first.Fingerprint {
		t.Fatal("fingerprint did not change after an archive file changed")
	}

	if err := os.WriteFile(filepath.Join(fixture.root, dataKeyFileName), []byte("rotated-key\n"), 0o600); err != nil {
		t.Fatalf("rewrite data key: %v", err)
	}
	afterKey := preflight(t, fixture.root, Options{})
	if afterKey.Fingerprint == afterArchive.Fingerprint {
		t.Fatal("fingerprint did not change after the data key changed")
	}
}

// TestPreflightFingerprintCoversTheJournalAndNewRootFiles is the regression for
// the two gaps the fingerprint had: a rollback journal was invisible even though
// it changes what the database means on its first open, and a file appearing
// beside the source did not change the fingerprint at all.
func TestPreflightFingerprintCoversTheJournalAndNewRootFiles(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	base := preflight(t, fixture.root, Options{})

	journal := filepath.Join(fixture.root, databaseFileName+"-journal")
	writeFixtureFile(t, journal, "journal-frames\n")
	if withJournal := preflight(t, fixture.root, Options{}); withJournal.Fingerprint == base.Fingerprint {
		t.Fatal("fingerprint did not change after a rollback journal appeared")
	}
	if err := os.Remove(journal); err != nil {
		t.Fatalf("remove the rollback journal: %v", err)
	}
	if restored := preflight(t, fixture.root, Options{}); restored.Fingerprint != base.Fingerprint {
		t.Fatal("fingerprint did not return to its original value after the journal was removed")
	}

	// A file beside the source is a change to the source, even though the copy
	// never carries it.
	writeFixtureFile(t, filepath.Join(fixture.root, "written-by-another-tool.bin"), "evidence\n")
	if withExtra := preflight(t, fixture.root, Options{}); withExtra.Fingerprint == base.Fingerprint {
		t.Fatal("fingerprint did not change after a new file appeared in the source root")
	}
}

func TestPreflightDiskRequirement(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	sourceBytes := coveredBytes(t, fixture.root)
	required := sourceBytes + (sourceBytes+4)/5
	if required <= sourceBytes {
		t.Fatalf("required bytes = %d, want more than source bytes %d", required, sourceBytes)
	}

	report := preflight(t, fixture.root, Options{
		AvailableBytes: func(string) (int64, error) { return required - 1, nil },
	})
	if report.Ready || len(report.Blocked) != 1 || report.Blocked[0] != CodeDiskInsufficient {
		t.Fatalf("blocked = %v ready = %t, want [%s]", report.Blocked, report.Ready, CodeDiskInsufficient)
	}
	check, found := checkByName(report, CheckDisk)
	if !found || check.Status != StatusBlocked || check.Code != CodeDiskInsufficient {
		t.Fatalf("disk check = %+v", check)
	}

	report = preflight(t, fixture.root, Options{
		AvailableBytes: func(string) (int64, error) { return required, nil },
	})
	if !report.Ready {
		t.Fatalf("ready = false at exactly the requirement, blocked = %v", report.Blocked)
	}
}

func TestPreflightParityDetectsProjectionlessRefOnly(t *testing.T) {
	healthyTimestamp := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	missingTimestamp := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC).UnixMilli()
	fixture := newSourceFixture(t, func(t *testing.T, db *sql.DB) {
		archivedRef(t, db, "healthy-event", 10, healthyTimestamp)
		addProjection(t, db, 10, healthyTimestamp)
		archivedRef(t, db, "projectionless-event", 20, missingTimestamp)
	})
	report := preflight(t, fixture.root, Options{})
	if !report.ParityGaps.ProjectionStateUsable {
		t.Fatal("projection watermark was not considered usable")
	}
	if report.ParityGaps.DeletedEventRefs != 2 {
		t.Fatalf("deleted event refs = %d, want 2", report.ParityGaps.DeletedEventRefs)
	}
	if report.ParityGaps.MissingProjection != 1 {
		t.Fatalf("missing projection = %d, want 1 (the healthy row must not be flagged)", report.ParityGaps.MissingProjection)
	}
	if report.ParityGaps.MismatchedProjection != 0 {
		t.Fatalf("mismatched projection = %d, want 0", report.ParityGaps.MismatchedProjection)
	}
	if report.ParityGaps.PastWatermark != 0 {
		t.Fatalf("past watermark = %d, want 0", report.ParityGaps.PastWatermark)
	}
	wantDates := []string{"2026-03-02"}
	if len(report.ParityGaps.AffectedDates) != 1 || report.ParityGaps.AffectedDates[0] != wantDates[0] {
		t.Fatalf("affected dates = %v, want %v", report.ParityGaps.AffectedDates, wantDates)
	}
	check, _ := checkByName(report, CheckParity)
	if check.Status != StatusGap {
		t.Fatalf("parity check status = %q, want %q", check.Status, StatusGap)
	}
}

func TestPreflightParityDetectsMismatchedProjectionAndWatermark(t *testing.T) {
	timestamp := time.Date(2026, 3, 3, 8, 0, 0, 0, time.UTC).UnixMilli()
	fixture := newSourceFixture(t, func(t *testing.T, db *sql.DB) {
		archivedRef(t, db, "shifted-event", 10, timestamp)
		addProjection(t, db, 10, timestamp+1)
		archivedRef(t, db, "past-watermark-event", 500, timestamp)
		addProjection(t, db, 500, timestamp)
	})
	report := preflight(t, fixture.root, Options{})
	if report.ParityGaps.MismatchedProjection != 1 {
		t.Fatalf("mismatched projection = %d, want 1", report.ParityGaps.MismatchedProjection)
	}
	if report.ParityGaps.PastWatermark != 1 {
		t.Fatalf("past watermark = %d, want 1", report.ParityGaps.PastWatermark)
	}
	if report.ParityGaps.MissingProjection != 0 {
		t.Fatalf("missing projection = %d, want 0", report.ParityGaps.MissingProjection)
	}
}

func TestPreflightParityChecksDeletedCoverageDailyBothWays(t *testing.T) {
	day1 := time.Date(2026, 4, 1, 6, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 4, 2, 6, 0, 0, 0, time.UTC)
	fixture := newSourceFixture(t, func(t *testing.T, db *sql.DB) {
		withNewerThanPinned(t, db)
		for index, event := range []struct {
			hash string
			id   int64
		}{{"d1-a", 31}, {"d1-b", 32}, {"d1-c", 33}} {
			archivedRef(t, db, event.hash, event.id, day1.UnixMilli()+int64(index)*1000)
		}
		// The coverage table claims two deletions on day 1 (refs hold three) and
		// one on day 2 (refs hold none).
		execFixture(t, db, `insert into usage_archive_deleted_coverage_daily
			(utc_day, deleted_event_count, min_timestamp_ms, max_timestamp_ms) values (?, 2, ?, ?)`,
			day1.UnixMilli()/msPerDay, day1.UnixMilli(), day1.UnixMilli())
		execFixture(t, db, `insert into usage_archive_deleted_coverage_daily
			(utc_day, deleted_event_count, min_timestamp_ms, max_timestamp_ms) values (?, 1, ?, ?)`,
			day2.UnixMilli()/msPerDay, day2.UnixMilli(), day2.UnixMilli())
	})
	report := preflight(t, fixture.root, Options{})
	if report.ParityGaps.CoverageDaysWithGaps != 2 {
		t.Fatalf("coverage days with gaps = %d, want 2", report.ParityGaps.CoverageDaysWithGaps)
	}
	dates := map[string]bool{}
	for _, date := range report.ParityGaps.AffectedDates {
		dates[date] = true
	}
	for _, want := range []string{"2026-04-01", "2026-04-02"} {
		if !dates[want] {
			t.Fatalf("affected dates = %v, want %s", report.ParityGaps.AffectedDates, want)
		}
	}
}

func TestPreflightDoesNotWriteTheSource(t *testing.T) {
	fixture := newSourceFixture(t, func(t *testing.T, db *sql.DB) {
		archivedRef(t, db, "projectionless-event", 20, time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC).UnixMilli())
	})
	before := snapshotTree(t, fixture.root)
	report := preflight(t, fixture.root, Options{})
	if report.Fingerprint == "" {
		t.Fatal("preflight produced no fingerprint")
	}
	after := snapshotTree(t, fixture.root)
	if len(before) != len(after) {
		t.Fatalf("source tree entry count changed: %d -> %d", len(before), len(after))
	}
	for path, digest := range before {
		if after[path] != digest {
			t.Fatalf("source entry %s changed: %q -> %q", path, digest, after[path])
		}
	}
}

func TestPreflightWorksOnAReadOnlySourceTree(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	// A stopped source is normally preserved read-only. Only the ownership lock
	// file keeps write access: the v1 writer opens it read-write.
	walkErr := filepath.WalkDir(fixture.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(path, 0o555)
		}
		if path == fixture.dbPath+".manager.lock" {
			return os.Chmod(path, 0o600)
		}
		return os.Chmod(path, 0o444)
	})
	if walkErr != nil {
		t.Fatalf("make source read-only: %v", walkErr)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(fixture.root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				return os.Chmod(path, 0o755)
			}
			return os.Chmod(path, 0o600)
		})
	})

	report := preflight(t, fixture.root, Options{})
	if !report.Ready {
		t.Fatalf("ready = false on a read-only source, blocked = %v", report.Blocked)
	}
}

func TestModelStageOrderMatchesPreflightStages(t *testing.T) {
	// The operation model's forward order is the contract the preflight report
	// describes to the operator.
	order := []model.UpgradeStage{
		model.UpgradeStagePreflight, model.UpgradeStageCopyIn, model.UpgradeStageReconcile,
		model.UpgradeStageCPAImport, model.UpgradeStageExternalConnect, model.UpgradeStageVerify,
		model.UpgradeStageComplete,
	}
	for index := 1; index < len(order); index++ {
		if !order[index-1].CanAdvanceTo(order[index]) {
			t.Fatalf("%q cannot advance to %q", order[index-1], order[index])
		}
		if order[index].CanAdvanceTo(order[index-1]) {
			t.Fatalf("%q advanced backwards to %q", order[index], order[index-1])
		}
	}
}

func coveredBytes(t *testing.T, root string) int64 {
	t.Helper()
	total := int64(0)
	for _, name := range sourceFingerprintFiles {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			continue
		}
		total += info.Size()
	}
	walkErr := filepath.WalkDir(filepath.Join(root, archivesDirName), func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk archives: %v", walkErr)
	}
	return total
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			snapshot[relative+string(filepath.Separator)] = ""
			return nil
		}
		digest, err := fileContentDigest(path)
		if err != nil {
			return err
		}
		snapshot[relative] = fmt.Sprintf("%d:%s", entrySize(t, entry), digest)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("snapshot source tree: %v", walkErr)
	}
	return snapshot
}

func entrySize(t *testing.T, entry os.DirEntry) int64 {
	t.Helper()
	info, err := entry.Info()
	if err != nil {
		t.Fatalf("stat %s: %v", entry.Name(), err)
	}
	return info.Size()
}
