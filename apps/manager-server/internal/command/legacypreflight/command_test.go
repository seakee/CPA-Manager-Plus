package legacypreflight

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	service "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

func TestCommandRequiresExplicitSourceAndDoesNotCreateDirectories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "absent", "usage.sqlite")
	var out, errOut bytes.Buffer
	if err := Run(context.Background(), nil, &out, &errOut); err == nil {
		t.Fatal("implicit source accepted")
	}
	if err := Run(context.Background(), []string{"--db", path}, &out, &errOut); err == nil {
		t.Fatal("uninspected config silently became Fresh")
	}
	var report service.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.SourceKind != "missing-database" || report.UpgradeAuthorized {
		t.Fatalf("report=%+v", report)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("command created source directory")
	}
}

func TestCommandReportsLegacySchemaWithoutSecretsOrUpgradeGrant(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table settings(key text primary key,value text);create table usage_events(id integer primary key);insert into settings values('setup','{"managementKey":"OWNED_SECRET"}')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := Run(context.Background(), []string{"--db", path, "--source-stopped"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var report service.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.SourceKind != "legacy-manager" || report.UpgradeAuthorized || len(report.Unknown) < 5 {
		t.Fatalf("report=%+v", report)
	}
	if strings.Contains(out.String()+errOut.String(), "OWNED_SECRET") {
		t.Fatal("credential escaped")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("source changed")
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("command created artifacts: %v", files)
	}
}

func TestCommandMissingOrMalformedExplicitConfigFailsWithoutCreatingIt(t *testing.T) {
	for _, kind := range []string{"missing", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			cfg := filepath.Join(dir, "config.json")
			if kind == "malformed" {
				if err := os.WriteFile(cfg, []byte(`{"managementKey":"OWNED_SECRET", broken}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out, errOut bytes.Buffer
			if err := Run(context.Background(), []string{"--db", filepath.Join(dir, "usage.sqlite"), "--config", cfg}, &out, &errOut); err == nil {
				t.Fatal("bad config accepted")
			}
			if strings.Contains(out.String()+errOut.String(), "OWNED_SECRET") {
				t.Fatal("config contents escaped")
			}
			if kind == "missing" {
				if _, err := os.Stat(cfg); !os.IsNotExist(err) {
					t.Fatal("config created")
				}
			}
		})
	}
}

func TestCommandHelpCancellationAndOutputFailure(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := Run(context.Background(), []string{"--help"}, &out, &errOut); !IsHelp(err) {
		t.Fatalf("help=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, []string{"--db", filepath.Join(t.TempDir(), "usage.sqlite")}, &out, &errOut); err == nil {
		t.Fatal("cancelled inspection succeeded")
	}
	if err := Run(context.Background(), []string{"--db", filepath.Join(t.TempDir(), "usage.sqlite")}, failingWriter{}, &errOut); err == nil {
		t.Fatal("output failure ignored")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, os.ErrPermission }
