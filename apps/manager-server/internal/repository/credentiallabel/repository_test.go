package credentiallabel_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/credentiallabel"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	service "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/credentiallabel"
)

func TestLabelsPersistAcrossReopenAndIndependentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.sqlite")
	db, err := sqliterepo.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s := service.New(repo.New(db))
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	var wg sync.WaitGroup
	for _, hash := range []string{a, b} {
		wg.Go(func() {
			if _, e := s.Set(ctx, hash, "same-account@example.com"); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	if _, err = s.Set(ctx, a, "  更新后的备注  "); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sqliterepo.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = service.New(repo.New(db))
	rows, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Label != "更新后的备注" || rows[1].Label != "same-account@example.com" || rows[0].UpdatedAtMS <= 0 {
		t.Fatalf("reopened: %#v", rows)
	}
	if _, err = s.Set(ctx, strings.Repeat("c", 64), strings.Repeat("界", 120)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Set(ctx, a, " "); err != nil {
		t.Fatal(err)
	}
	rows, err = s.List(ctx)
	if err != nil || len(rows) != 2 || rows[0].KeyHash != b {
		t.Fatalf("clear changed another label: %#v %v", rows, err)
	}
}
