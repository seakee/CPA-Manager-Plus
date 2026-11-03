package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// digestChunkSize is the bounded batch size shared by the source fingerprint
// and the snapshot copy. Cancellation is observed between batches: a read that
// is already blocked inside the kernel is not interrupted by ctx alone, so the
// bound limits how long a cancelled run can remain inside a single read.
const digestChunkSize = 64 << 10

// sourceFingerprintFiles are the fixed v1 data files, in the order the v1
// Manager owns them. Optional files are skipped when absent.
var sourceFingerprintFiles = []string{
	databaseFileName,
	databaseFileName + "-wal",
	databaseFileName + "-shm",
	dataKeyFileName,
}

type manifestEntry struct {
	relative string
	size     int64
}

// sourceFingerprint is the deterministic manifest of a source tree: relative
// path, size and content digest of every data file, in lexical order.
type sourceFingerprint struct {
	Digest string
	Bytes  int64
}

// fingerprintSource hashes a stable manifest of the source tree. Repeating it on
// an unchanged stopped source always returns the same value, and any change to
// a covered file, including a -wal/-shm file appearing, changes it. It reads
// only, and observes ctx between the bounded read batches so a cancelled
// preflight stops instead of hashing a multi-gigabyte archive to the end.
func fingerprintSource(ctx context.Context, root string) (sourceFingerprint, error) {
	if err := ctx.Err(); err != nil {
		return sourceFingerprint{}, err
	}
	entries := make([]manifestEntry, 0, len(sourceFingerprintFiles)+8)
	for _, name := range sourceFingerprintFiles {
		if err := ctx.Err(); err != nil {
			return sourceFingerprint{}, err
		}
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return sourceFingerprint{}, err
		}
		if !info.Mode().IsRegular() {
			return sourceFingerprint{}, fmt.Errorf("source file %s is not a regular file", name)
		}
		entries = append(entries, manifestEntry{relative: name, size: info.Size()})
	}
	archives, err := collectArchiveEntries(ctx, root, filepath.Join(root, archivesDirName))
	if err != nil {
		return sourceFingerprint{}, err
	}
	entries = append(entries, archives...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].relative < entries[j].relative })

	manifest := sha256.New()
	var total int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return sourceFingerprint{}, err
		}
		content, err := fileContentDigest(ctx, filepath.Join(root, filepath.FromSlash(entry.relative)))
		if err != nil {
			return sourceFingerprint{}, err
		}
		if _, err := fmt.Fprintf(manifest, "%s\x00%d\x00%s\n", entry.relative, entry.size, content); err != nil {
			return sourceFingerprint{}, err
		}
		total += entry.size
	}
	return sourceFingerprint{Digest: hex.EncodeToString(manifest.Sum(nil)), Bytes: total}, nil
}

func collectArchiveEntries(ctx context.Context, root string, archivesRoot string) ([]manifestEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(archivesRoot)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("source archive path %s is not a directory", archivesDirName)
	}
	entries := []manifestEntry{}
	walkErr := filepath.WalkDir(archivesRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("source archive entry %s is not a regular file", entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, manifestEntry{relative: filepath.ToSlash(relative), size: info.Size()})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return entries, nil
}

func fileContentDigest(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	if _, err := copyChunks(ctx, digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// copyChunks copies source into destination with one fixed buffer per call and
// observes ctx between batches. The bytes and the digest are identical to a
// single io.Copy; only the cancellation points are new. Callers keep their own
// file lifetimes: a cancelled copy returns the context error and leaves the
// partial destination for the caller's cleanup.
func copyChunks(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, digestChunkSize)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			written, writeErr := destination.Write(buffer[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}
