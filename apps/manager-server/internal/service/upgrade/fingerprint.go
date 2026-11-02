package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// sourceFingerprintFiles are the fixed v1 data files, in the order the v1
// Manager owns them. Optional files are skipped when absent. The rollback
// journal is covered because a nonempty one changes what the database means on
// its first open, so a copy that ignored it would not describe its source.
var sourceFingerprintFiles = []string{
	databaseFileName,
	databaseFileName + "-wal",
	databaseFileName + "-shm",
	databaseFileName + "-journal",
	dataKeyFileName,
}

// sourceRootReserved are the names the 1.x data root owns itself. Every other
// entry of the root is reported as unexpected, so a new file appearing beside
// the source changes the fingerprint even when no covered file changed.
var sourceRootReserved = map[string]bool{
	databaseFileName:              true,
	databaseFileName + "-wal":     true,
	databaseFileName + "-shm":     true,
	databaseFileName + "-journal": true,
	dataKeyFileName:               true,
	archivesDirName:               true,
	// The ownership fence is taken by every reader of this volume and its
	// content is the holder's, not the source's: including it would make two
	// consecutive inspections of an unchanged source disagree.
	databaseFileName + ".manager.lock": true,
}

type manifestEntry struct {
	relative string
	size     int64
	// directory marks an entry covered by name only: it is not copied, so it has
	// no content digest and no bytes to budget for.
	directory bool
	// uncopied marks an entry the copy neither carries nor budgets for. It still
	// contributes to the digest so its appearance or change is detected.
	uncopied bool
}

// sourceFingerprint is the deterministic manifest of a source tree: relative
// path, size and content digest of every data file, in lexical order.
type sourceFingerprint struct {
	Digest string
	Bytes  int64
}

// symlinkedSourceFile is one covered source file the shared file policy refused
// because it is a link. Carrying the name lets the preflight report data.key or
// usage.sqlite-wal rather than the lock package's database wording, which would
// describe a key or a WAL sidecar as the database.
type symlinkedSourceFile struct{ name string }

func (e *symlinkedSourceFile) Error() string { return "source file " + e.name + " is a symbolic link" }

func (e *symlinkedSourceFile) Unwrap() error { return ErrSourceSymlinked }

// fingerprintSource hashes a stable manifest of the source tree. Repeating it on
// an unchanged stopped source always returns the same value, and any change to
// a covered file, including a -wal/-shm file appearing, changes it; so does a
// file the 1.x layout does not own appearing beside them. It reads only.
func fingerprintSource(root string) (sourceFingerprint, error) {
	entries := make([]manifestEntry, 0, len(sourceFingerprintFiles)+8)
	for _, name := range sourceFingerprintFiles {
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return sourceFingerprint{}, err
		}
		if !info.Mode().IsRegular() {
			// A link is refused by the shared database file policy, not merely
			// unreadable: the file inspected and the file read have to be the
			// same one. The preflight names that as its own code and names this
			// exact file, so the report never calls a policy refusal an
			// unreadable database or a different file.
			if info.Mode()&os.ModeSymlink != 0 {
				return sourceFingerprint{}, &symlinkedSourceFile{name: name}
			}
			return sourceFingerprint{}, fmt.Errorf("source file %s is not a regular file", name)
		}
		entries = append(entries, manifestEntry{relative: name, size: info.Size()})
	}
	archives, err := collectArchiveEntries(root, filepath.Join(root, archivesDirName))
	if err != nil {
		return sourceFingerprint{}, err
	}
	entries = append(entries, archives...)
	unexpected, err := collectUnexpectedRootEntries(root)
	if err != nil {
		return sourceFingerprint{}, err
	}
	entries = append(entries, unexpected...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].relative < entries[j].relative })

	manifest := sha256.New()
	var total int64
	for _, entry := range entries {
		content := ""
		if !entry.directory {
			var err error
			if content, err = fileContentDigest(filepath.Join(root, filepath.FromSlash(entry.relative))); err != nil {
				return sourceFingerprint{}, err
			}
		}
		if _, err := fmt.Fprintf(manifest, "%s\x00%d\x00%s\n", entry.relative, entry.size, content); err != nil {
			return sourceFingerprint{}, err
		}
		if !entry.uncopied {
			total += entry.size
		}
	}
	return sourceFingerprint{Digest: hex.EncodeToString(manifest.Sum(nil)), Bytes: total}, nil
}

// collectUnexpectedRootEntries describes every entry of the source root that the
// 1.x data layout does not own. A file appearing beside the source is a change
// to the source, so it must change the fingerprint; it is not part of the copy,
// so it never contributes to the copied byte total.
func collectUnexpectedRootEntries(root string) ([]manifestEntry, error) {
	listing, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	entries := make([]manifestEntry, 0, len(listing))
	for _, entry := range listing {
		name := filepath.ToSlash(entry.Name())
		if sourceRootReserved[entry.Name()] {
			continue
		}
		if entry.IsDir() {
			// Directories are covered by name only: their content is not copied
			// and is not the 1.x data layout's to describe.
			entries = append(entries, manifestEntry{relative: name, directory: true, uncopied: true})
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		entries = append(entries, manifestEntry{relative: name, size: info.Size(), uncopied: true})
	}
	return entries, nil
}

func collectArchiveEntries(root string, archivesRoot string) ([]manifestEntry, error) {
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

func fileContentDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
