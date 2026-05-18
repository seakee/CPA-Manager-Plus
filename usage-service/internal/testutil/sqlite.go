package testutil

import (
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/config"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/store"
)

func NewConfig(t testing.TB) config.Config {
	t.Helper()
	return config.Config{
		DBPath:        filepath.Join(t.TempDir(), "usage.sqlite"),
		Queue:         "usage",
		PopSide:       "right",
		BatchSize:     100,
		QueryLimit:    50000,
		CORSOrigins:   []string{"*"},
		CollectorMode: "auto",
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
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}
