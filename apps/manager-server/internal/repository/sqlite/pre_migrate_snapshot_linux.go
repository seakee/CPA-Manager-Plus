//go:build linux

package sqlite

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

func copySnapshotFileData(ctx context.Context, dst *os.File, src *os.File) (string, int64, error) {
	const maxChunk = 16 * 1024 * 1024
	srcFD := int(src.Fd())
	dstFD := int(dst.Fd())
	var total int64
	usedKernelCopy := false
	for {
		if err := ctx.Err(); err != nil {
			return "", total, err
		}
		n, err := unix.CopyFileRange(srcFD, nil, dstFD, nil, maxChunk, 0)
		runtime.KeepAlive(src)
		runtime.KeepAlive(dst)
		if n > 0 {
			usedKernelCopy = true
			total += int64(n)
		}
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if isCopyFileRangeFallbackError(err) {
				if !usedKernelCopy {
					return copyAndHashUserspace(ctx, dst, src)
				}
				return finishFallbackAndHash(ctx, dst, src, total)
			}
			return "", total, err
		}
		if n == 0 {
			break
		}
	}
	digest, hashedBytes, err := hashOpenFileFromStart(ctx, dst)
	if err != nil {
		return "", total, err
	}
	if hashedBytes != total {
		return "", total, io.ErrUnexpectedEOF
	}
	return digest, total, nil
}

func isCopyFileRangeFallbackError(err error) bool {
	return errors.Is(err, unix.ENOSYS) ||
		errors.Is(err, unix.EXDEV) ||
		errors.Is(err, unix.EINVAL) ||
		errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.ENOTSUP) ||
		errors.Is(err, unix.EPERM) ||
		errors.Is(err, unix.EBADF)
}
