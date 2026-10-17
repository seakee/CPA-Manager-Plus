package legacypreflight

import (
	"context"
	"errors"
	"os"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

// Pending recovery admits only the original host receipt and a bounded inventory.
// Database product content is checked read-only by fresh.Open before Migrate.
func validatePendingSource(ctx context.Context, paths sqlite.InspectionPaths, options Options, lock *processlock.Lock) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, path := range []string{paths.Database, paths.Database + "-wal", paths.Database + "-shm", paths.DataKey} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("Fresh recovery inventory unavailable")
		}
	}
	for _, path := range []string{paths.Archives, paths.Database + "-journal"} {
		if path == "" {
			return errors.New("Fresh recovery inventory incomplete")
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return errors.New("Fresh recovery source evidence conflict")
		}
	}
	if !options.Initialize && paths.Config != "" {
		if _, err := os.Lstat(paths.Config); !os.IsNotExist(err) {
			return errors.New("Fresh recovery config conflict")
		}
	}
	return lock.Validate()
}
