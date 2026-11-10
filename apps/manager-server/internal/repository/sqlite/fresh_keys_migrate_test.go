package sqlite

import (
	"database/sql"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
