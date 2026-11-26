//go:build !linux

package sqlite

import (
	"context"
	"os"
)

func copySnapshotFileData(ctx context.Context, dst *os.File, src *os.File) (string, int64, error) {
	return copyAndHashUserspace(ctx, dst, src)
}
