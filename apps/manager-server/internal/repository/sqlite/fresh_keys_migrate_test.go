package sqlite

import (
	"database/sql"
	"database/sql/driver"
	"context"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestFreshKeyOriginsAcceptAnAdoptedKeyAfterUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-adoption.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`create table gateway_api_key_identities(id text primary key,revision integer not null,lifecycle text not null,created_at_ms integer not null,updated_at_ms integer not null)`,
		`create table fresh_client_api_keys(request_id text primary key,api_key_id text not null unique references gateway_api_key_identities(id),secret_ciphertext text not null,secret_hash text not null unique,masked_preview text not null,name text not null default '',name_revision integer not null default 1 check(typeof(name_revision) = 'integer' and name_revision >= 1),origin text not null default 'workspace' check(origin in ('workspace','installer')))`,
		`create unique index idx_fresh_client_api_keys_installer on fresh_client_api_keys(origin) where origin = 'installer'`,
		`insert into gateway_api_key_identities values('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',1,'active',1,1)`,
		`insert into gateway_api_key_identities values('cccccccccccccccccccccccccccccccc',1,'active',2,2)`,
		`insert into fresh_client_api_keys values('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','workspace ciphertext','workspace hash','cpamp_sk_…0001','kept',3,'workspace')`,
		`insert into fresh_client_api_keys values('dddddddddddddddddddddddddddddddd','cccccccccccccccccccccccccccccccc','installer ciphertext','installer hash','sk-…0002','',1,'installer')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	// The pre-adoption build refuses the origin the installation now registers.
	if _, err := db.Exec(`insert into gateway_api_key_identities values('eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',1,'active',3,3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into fresh_client_api_keys values('eeeeeeeeeeeeeeeeeeeeeeeeeeeeee01','eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','adopted ciphertext','adopted hash','…0003','',1,'adopted')`); err == nil {
		t.Fatal("the pre-adoption schema accepted an adopted key")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// expected are the rows the rebuild must carry over untouched.
	expected := []string{
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb|workspace hash|cpamp_sk_…0001|kept|workspace|3",
		"dddddddddddddddddddddddddddddddd|installer hash|sk-…0002||installer|1",
	}
	adoptedRow := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeee01|adopted hash|…0003||adopted|1"
	readRows := func(t *testing.T) []string {
		t.Helper()
		rows, err := db.Query(`select request_id,secret_hash,masked_preview,name,name_revision,origin from fresh_client_api_keys order by request_id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var kept []string
		for rows.Next() {
			var requestID, hash, preview, name, origin string
			var revision int64
			if err := rows.Scan(&requestID, &hash, &preview, &name, &revision, &origin); err != nil {
				t.Fatal(err)
			}
			kept = append(kept, requestID+"|"+hash+"|"+preview+"|"+name+"|"+origin+"|"+strconv.FormatInt(revision, 10))
		}
		return kept
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var definition string
	if err := db.QueryRow(`select sql from sqlite_master where type='table' and name='fresh_client_api_keys'`).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(definition, "'adopted'") {
		t.Fatalf("origin vocabulary was not widened: %s", definition)
	}
	if kept := readRows(t); !slices.Equal(kept, expected) {
		t.Fatalf("upgrade changed existing key records: %v", kept)
	}
	if _, err = db.Exec(`insert into fresh_client_api_keys values('eeeeeeeeeeeeeeeeeeeeeeeeeeeeee01','eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','adopted ciphertext','adopted hash','…0003','',1,'adopted')`); err != nil {
		t.Fatalf("widened schema refused an adopted key: %v", err)
	}
	if _, err = db.Exec(`insert into fresh_client_api_keys values('ffffffffffffffffffffffffffffff01','ffffffffffffffffffffffffffffffff','x','y','…','',1,'unknown')`); err == nil {
		t.Fatal("widened schema accepted an unknown origin")
	}
	// The installation origin stays singular: the fence index was recreated with
	// the table, so a concurrent install still cannot mint a second one.
	if _, err = db.Exec(`insert into gateway_api_key_identities values('ffffffffffffffffffffffffffffffff',1,'active',4,4)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`insert into fresh_client_api_keys values('ffffffffffffffffffffffffffffff02','ffffffffffffffffffffffffffffffff','other ciphertext','other hash','sk-…0004','',1,'installer')`); err == nil {
		t.Fatal("the installation origin accepted a second key")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	assertNoForeignKeyViolations(t, path)
	// A second open is a no-op: the widened table is not rebuilt again, and the
	// adopted record it now holds survives.
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if kept := readRows(t); !slices.Equal(kept, append(slices.Clone(expected), adoptedRow)) {
		t.Fatalf("a second open changed the key records: %v", kept)
	}
}

func assertNoForeignKeyViolations(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`pragma foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatalf("pragma foreign_key_check reported a violation on %s", path)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func seedPreAdoptionKeySchema(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, query := range []string{
		`pragma journal_mode = wal`,
		`create table gateway_api_key_identities(id text primary key,revision integer not null,lifecycle text not null,created_at_ms integer not null,updated_at_ms integer not null)`,
		`create table fresh_client_api_keys(request_id text primary key,api_key_id text not null unique references gateway_api_key_identities(id),secret_ciphertext text not null,secret_hash text not null unique,masked_preview text not null,name text not null default '',name_revision integer not null default 1 check(typeof(name_revision) = 'integer' and name_revision >= 1),origin text not null default 'workspace' check(origin in ('workspace','installer')))`,
		`create unique index idx_fresh_client_api_keys_installer on fresh_client_api_keys(origin) where origin = 'installer'`,
		`insert into gateway_api_key_identities values('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',1,'active',1,1)`,
		`insert into fresh_client_api_keys values('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','workspace ciphertext','workspace hash','cpamp_sk_…0001','kept',3,'workspace')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
}

// Two handles that observe the pre-adoption table definition before acquiring
// the write lock must rebuild the table at most once: the second opener
// re-checks sqlite_master inside its write transaction instead of dropping and
// recreating an already-widened table.
type txStartSignalConnector struct {
	base      driver.Driver
	dsn       string
	once      sync.Once
	onTxStart chan struct{}
}

func (c *txStartSignalConnector) Connect(context.Context) (driver.Conn, error) {
	raw, err := c.base.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &txStartSignalConn{Conn: raw, notify: func() { c.once.Do(func() { close(c.onTxStart) }) }}, nil
}

func (c *txStartSignalConnector) Driver() driver.Driver { return c.base }

type txStartSignalConn struct {
	driver.Conn
	notify func()
}

func (c *txStartSignalConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c *txStartSignalConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(query)), "begin") {
		c.notify()
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *txStartSignalConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.notify()
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func TestFreshKeyOriginsConcurrentUpgradeRebuildsAtMostOnce(t *testing.T) {
	t.Run("second writer skips rebuild after first commits", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "concurrent-recheck.sqlite")
		seedPreAdoptionKeySchema(t, path)

		dsn := dataSourceName(path)
		first, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = first.Close() }()
		enteredWriteTx := make(chan struct{})
		second := sql.OpenDB(&txStartSignalConnector{
			base:      first.Driver(),
			dsn:       dsn,
			onTxStart: enteredWriteTx,
		})
		defer func() { _ = second.Close() }()

		ctx := context.Background()
		firstConn, err := first.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = firstConn.Close() }()
		if _, err := firstConn.ExecContext(ctx, `begin immediate`); err != nil {
			t.Fatal(err)
		}

		secondDone := make(chan error, 1)
		go func() {
			secondDone <- ensureFreshKeyOrigins(second)
		}()
		// Wait until `second` has already observed the pre-adoption schema on its
		// WAL snapshot and entered its write transaction behind `firstConn`.
		<-enteredWriteTx
		// Widen on `firstConn` and attach a secondary index plus an adopted row
		// before releasing the write lock. A redundant rebuild on `second` would
		// `DROP TABLE fresh_client_api_keys` and destroy that index.
		for _, stmt := range []string{
			`create table fresh_client_api_keys_adopted (
				request_id text primary key,
				api_key_id text not null unique references gateway_api_key_identities(id),
				secret_ciphertext text not null,
				secret_hash text not null unique,
				masked_preview text not null,
				name text not null default '',
				name_revision integer not null default 1 check(typeof(name_revision) = 'integer' and name_revision >= 1),
				origin text not null default 'workspace' check(origin in ('workspace','installer','adopted'))
			)`,
			`insert into fresh_client_api_keys_adopted select * from fresh_client_api_keys`,
			`drop table fresh_client_api_keys`,
			`alter table fresh_client_api_keys_adopted rename to fresh_client_api_keys`,
			`create unique index idx_fresh_client_api_keys_installer on fresh_client_api_keys(origin) where origin = 'installer'`,
			`create index idx_fresh_client_api_keys_single_rebuild_witness on fresh_client_api_keys(name)`,
			`insert into gateway_api_key_identities values('eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',1,'active',2,2)`,
			`insert into fresh_client_api_keys values('eeeeeeeeeeeeeeeeeeeeeeeeeeeeee01','eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','adopted ciphertext','adopted hash','…0003','',1,'adopted')`,
			`commit`,
		} {
			if _, err := firstConn.ExecContext(ctx, stmt); err != nil {
				t.Fatal(err)
			}
		}

		if err := <-secondDone; err != nil {
			t.Fatalf("second ensureFreshKeyOrigins: %v", err)
		}
		var witnessCount int
		if err := second.QueryRow(`select count(*) from sqlite_master where type='index' and name='idx_fresh_client_api_keys_single_rebuild_witness'`).Scan(&witnessCount); err != nil || witnessCount != 1 {
			t.Fatalf("second handle rebuilt already-widened table (witness=%d, err=%v)", witnessCount, err)
		}
		assertNoForeignKeyViolations(t, path)
	})

	t.Run("concurrent Open handles succeed cleanly", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "concurrent-open.sqlite")
		seedPreAdoptionKeySchema(t, path)

		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				db, err := Open(path)
				if err == nil {
					_ = db.Close()
				}
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent Open: %v", err)
			}
		}
		db, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		if _, err := db.Exec(`insert into gateway_api_key_identities values('eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',1,'active',2,2)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`insert into fresh_client_api_keys values('eeeeeeeeeeeeeeeeeeeeeeeeeeeeee01','eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','adopted ciphertext','adopted hash','…0003','',1,'adopted')`); err != nil {
			t.Fatalf("adopted insert after concurrent Open: %v", err)
		}
		assertNoForeignKeyViolations(t, path)
	})

	t.Run("failed rebuild rolls back cleanly and succeeds on retry", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "rollback-retry.sqlite")
		seedPreAdoptionKeySchema(t, path)
		raw, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		// Without foreign_keys enabled on the raw pre-adoption handle, insert an
		// orphan row so the FK-enforced table rebuild fails mid-transaction.
		if _, err := raw.Exec(`insert into fresh_client_api_keys values('99999999999999999999999999999999','88888888888888888888888888888888','orphan ciphertext','orphan hash','cpamp_sk_…9999','',1,'workspace')`); err != nil {
			t.Fatal(err)
		}
		_ = raw.Close()

		if db, err := Open(path); err == nil {
			_ = db.Close()
			t.Fatal("Open succeeded despite foreign-key violation during rebuild")
		}

		raw, err = sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		var tempTables, rowCount int
		if err := raw.QueryRow(`select count(*) from sqlite_master where name='fresh_client_api_keys_adopted'`).Scan(&tempTables); err != nil || tempTables != 0 {
			t.Fatalf("failed rebuild left temp table count=%d err=%v", tempTables, err)
		}
		if err := raw.QueryRow(`select count(*) from fresh_client_api_keys`).Scan(&rowCount); err != nil || rowCount != 2 {
			t.Fatalf("failed rebuild changed original row count=%d err=%v", rowCount, err)
		}
		if _, err := raw.Exec(`insert into gateway_api_key_identities values('88888888888888888888888888888888',1,'active',2,2)`); err != nil {
			t.Fatal(err)
		}
		_ = raw.Close()

		recovered, err := Open(path)
		if err != nil {
			t.Fatalf("retry Open after restoring parent identity: %v", err)
		}
		defer func() { _ = recovered.Close() }()
		if err := recovered.QueryRow(`select count(*) from fresh_client_api_keys`).Scan(&rowCount); err != nil || rowCount != 2 {
			t.Fatalf("recovered row count=%d err=%v", rowCount, err)
		}
		assertNoForeignKeyViolations(t, path)
	})
}

func TestFreshKeyNamesUpgradeExistingStagedRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-rename.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`create table gateway_api_key_identities(id text primary key,revision integer not null,lifecycle text not null,created_at_ms integer not null,updated_at_ms integer not null)`,
		`create table fresh_client_api_keys(request_id text primary key,api_key_id text not null unique references gateway_api_key_identities(id),secret_ciphertext text not null,secret_hash text not null unique,masked_preview text not null)`,
		`insert into gateway_api_key_identities values('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',1,'active',1,1)`,
		`insert into fresh_client_api_keys values('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','existing ciphertext','existing hash','existing preview')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		db, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var name, cipher, hash, preview, id string
		var revision int64
		if err := db.QueryRow(`select api_key_id,secret_ciphertext,secret_hash,masked_preview,name,name_revision from fresh_client_api_keys`).Scan(&id, &cipher, &hash, &preview, &name, &revision); err != nil {
			t.Fatal(err)
		}
		if id != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || cipher != "existing ciphertext" || hash != "existing hash" || preview != "existing preview" || name != "" || revision != 1 {
			t.Fatal("upgrade changed existing key record")
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
