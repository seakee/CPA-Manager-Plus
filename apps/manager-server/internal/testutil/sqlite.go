package testutil

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const AdminKey = "cpamp_test_key_0123456789abcdef"

func NewConfig(t testing.TB) config.Config {
	t.Helper()
	dir := t.TempDir()
	return config.Config{
		DataDir:                   dir,
		DBPath:                    filepath.Join(dir, "usage.sqlite"),
		Queue:                     "usage",
		PopSide:                   "right",
		BatchSize:                 100,
		QueryLimit:                50000,
		CORSOrigins:               []string{"*"},
		CollectorMode:             "auto",
		UsageImportChunkBytes:     config.DefaultUsageImportChunkBytes,
		UsageImportDiskQuotaBytes: config.DefaultUsageImportDiskQuotaBytes,
		UsageImportMaxSessions:    config.DefaultUsageImportMaxSessions,
		UsageImportSessionTTL:     config.DefaultUsageImportSessionTTL,
	}
}

func NewStore(t testing.TB, cfg config.Config) *store.Store {
	t.Helper()
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(t.TempDir(), "usage.sqlite")
	}
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	EnsureAdminCredential(t, db)
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func EnsureAdminCredential(t testing.TB, db *store.Store) {
	t.Helper()
	if _, ok, err := db.LoadAdminCredential(context.Background()); err != nil {
		t.Fatalf("load admin credential: %v", err)
	} else if ok {
		return
	}
	credential, err := security.NewAdminCredential(AdminKey, "test")
	if err != nil {
		t.Fatalf("create admin credential: %v", err)
	}
	if err := db.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatalf("save admin credential: %v", err)
	}
}
