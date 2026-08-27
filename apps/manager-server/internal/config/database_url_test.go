package config

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLoadUsesSQLiteDatabaseURL(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	dbPath := filepath.Join(dir, "custom data", "usage ? #.sqlite")
	dbURL := testDatabaseURL(dbPath, "DELETE", "EXTRA", 15000,
		"mmap_size(0)",
		"cell_size_check(1)",
		"temp_store(FILE)",
	)
	t.Setenv(configEnvKey, configPath)
	t.Setenv(usageDBURLEnvKey, dbURL)

	cfg, err := LoadWithoutCreatingDefault()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DBURL != dbURL {
		t.Fatalf("DBURL = %q, want original URL", cfg.DBURL)
	}
	if cfg.DBPath != filepath.Clean(dbPath) {
		t.Fatalf("DBPath = %q, want %q", cfg.DBPath, filepath.Clean(dbPath))
	}
	if cfg.DBJournalMode != "delete" || cfg.DBSynchronous != 3 || cfg.DBBusyTimeout != 15000 {
		t.Fatalf("database expectations = mode:%q synchronous:%d busy:%d", cfg.DBJournalMode, cfg.DBSynchronous, cfg.DBBusyTimeout)
	}
	if want := filepath.Join(filepath.Dir(dbPath), "data.key"); cfg.DataKeyPath != want {
		t.Fatalf("DataKeyPath = %q, want %q", cfg.DataKeyPath, want)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config file exists or stat failed: %v", err)
	}
}

func TestLoadDatabaseURLPrecedenceAndConflicts(t *testing.T) {
	t.Run("environment URL overrides file path and data directory", func(t *testing.T) {
		clearConfigEnv(t)
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.json")
		if err := os.WriteFile(configPath, []byte(`{"dbPath":"file-data/usage.sqlite"}`), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		dbPath := filepath.Join(dir, "env-data", "usage.sqlite")
		t.Setenv(configEnvKey, configPath)
		t.Setenv("USAGE_DATA_DIR", filepath.Join(dir, "ignored-data"))
		t.Setenv(usageDBURLEnvKey, testDatabaseURL(dbPath, "WAL", "FULL", 7000))

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.DBPath != filepath.Clean(dbPath) || cfg.DBJournalMode != "wal" {
			t.Fatalf("database config = path:%q mode:%q", cfg.DBPath, cfg.DBJournalMode)
		}
	})

	t.Run("environment URL conflicts with environment path", func(t *testing.T) {
		clearConfigEnv(t)
		dir := t.TempDir()
		t.Setenv(usageDBURLEnvKey, testDatabaseURL(filepath.Join(dir, "usage.sqlite"), "DELETE", "EXTRA", 5000))
		t.Setenv(usageDBPathEnvKey, filepath.Join(dir, "other.sqlite"))
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "cannot both be set") {
			t.Fatalf("Load() error = %v", err)
		}
	})

	t.Run("file URL conflicts with file path", func(t *testing.T) {
		clearConfigEnv(t)
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.json")
		content, err := json.Marshal(fileConfig{
			DBPath: "usage.sqlite",
			DBURL:  testDatabaseURL(filepath.Join(dir, "url.sqlite"), "DELETE", "EXTRA", 5000),
		})
		if err != nil {
			t.Fatalf("marshal config: %v", err)
		}
		if err := os.WriteFile(configPath, content, 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		t.Setenv(configEnvKey, configPath)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "dbUrl and dbPath cannot both be set") {
			t.Fatalf("Load() error = %v", err)
		}
	})
}

func TestParseDatabaseURLRejectsUnsafeOrIncompleteURLs(t *testing.T) {
	validQuery := func() string {
		values := url.Values{}
		values.Add("_txlock", "immediate")
		values.Add("_pragma", "journal_mode(DELETE)")
		values.Add("_pragma", "synchronous(EXTRA)")
		values.Add("_pragma", "busy_timeout(15000)")
		values.Add("_pragma", "foreign_keys(1)")
		return values.Encode()
	}
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "scheme", url: "https://localhost/tmp/usage.sqlite?" + validQuery(), want: "scheme must be file"},
		{name: "host", url: "file://server/share/usage.sqlite?" + validQuery(), want: "must not contain a host"},
		{name: "opaque relative path", url: "file:usage.sqlite?" + validQuery(), want: "hierarchical"},
		{name: "memory path", url: "file:///:memory:?" + validQuery(), want: "persistent file"},
		{name: "network path", url: "file:////server/share/usage.sqlite?" + validQuery(), want: "network path"},
		{name: "memory mode", url: "file:///tmp/usage.sqlite?mode=memory&" + validQuery(), want: "persistent file"},
		{name: "immutable", url: "file:///tmp/usage.sqlite?immutable=1&" + validQuery(), want: "must not enable immutable"},
		{name: "no locking", url: "file:///tmp/usage.sqlite?nolock=on&" + validQuery(), want: "must not enable nolock"},
		{name: "custom VFS", url: "file:///tmp/usage.sqlite?vfs=unix-none&" + validQuery(), want: "custom VFS"},
		{name: "missing transaction lock", url: "file:///tmp/usage.sqlite?_pragma=journal_mode%28DELETE%29", want: "_txlock=immediate"},
		{name: "duplicate journal mode using equals syntax", url: "file:///tmp/usage.sqlite?" + validQuery() + "&_pragma=journal_mode%3DOFF", want: "duplicate journal_mode"},
		{name: "duplicate foreign keys using equals syntax", url: "file:///tmp/usage.sqlite?" + validQuery() + "&_pragma=foreign_keys%3DOFF", want: "duplicate foreign_keys"},
		{name: "invalid pragma syntax", url: "file:///tmp/usage.sqlite?" + validQuery() + "&_pragma=cache_size%281%29%3Bjournal_mode%28OFF%29", want: "invalid _pragma"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseDatabaseURL(test.url); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseDatabaseURL() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestDatabaseURLErrorsDoNotExposeRawURL(t *testing.T) {
	const secret = "sqlite-url-secret-sentinel"
	for _, rawURL := range []string{
		"file:///tmp/%zz?token=" + secret,
		"file:///tmp/usage.sqlite?_txlock=immediate&_pragma=journal_mode%28DELETE%29&_pragma=synchronous%28EXTRA%29&_pragma=busy_timeout%2815000%29&_pragma=foreign_keys%281%29&_pragma=" + url.QueryEscape("cache_size(1);"+secret),
	} {
		_, err := parseDatabaseURL(rawURL)
		if err == nil {
			t.Fatalf("parseDatabaseURL(%q) error = nil", rawURL)
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), rawURL) {
			t.Fatalf("database URL error leaked raw input: %v", err)
		}
	}
}

func TestSplitPragmaAcceptsDriverSyntaxes(t *testing.T) {
	for _, test := range []struct {
		raw       string
		wantName  string
		wantValue string
	}{
		{raw: "journal_mode(DELETE)", wantName: "journal_mode", wantValue: "DELETE"},
		{raw: "synchronous=EXTRA", wantName: "synchronous", wantValue: "EXTRA"},
		{raw: "main.cache_size(-2000)", wantName: "main.cache_size", wantValue: "-2000"},
	} {
		name, value, ok := splitPragma(test.raw)
		if !ok || name != test.wantName || value != test.wantValue {
			t.Fatalf("splitPragma(%q) = %q, %q, %v", test.raw, name, value, ok)
		}
	}
}

func testDatabaseURL(path string, journalMode string, synchronous string, busyTimeout int, extraPragmas ...string) string {
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := url.URL{Scheme: "file", Path: uriPath}
	query := url.Values{}
	query.Add("_txlock", "immediate")
	query.Add("_pragma", "journal_mode("+journalMode+")")
	query.Add("_pragma", "synchronous("+synchronous+")")
	query.Add("_pragma", "busy_timeout("+strconv.Itoa(busyTimeout)+")")
	query.Add("_pragma", "foreign_keys(1)")
	for _, pragma := range extraPragmas {
		query.Add("_pragma", pragma)
	}
	dsn.RawQuery = query.Encode()
	return dsn.String()
}
