package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxArchiveFiles = 5000
	maxExtractBytes = int64(512 * 1024 * 1024)
)

func ExtractArchive(archivePath, destination string) error {
	if err := ensurePrivateDirectory(destination); err != nil {
		return err
	}
	switch {
	case strings.HasSuffix(strings.ToLower(archivePath), ".zip"):
		return extractZIP(archivePath, destination)
	case strings.HasSuffix(strings.ToLower(archivePath), ".tar.gz"):
		return extractTarGzip(archivePath, destination)
	default:
		return errors.New("unsupported update archive format")
	}
}

func extractZIP(archivePath, destination string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	if len(reader.File) > maxArchiveFiles {
		return errors.New("update archive contains too many files")
	}
	seen := map[string]struct{}{}
	var total int64
	for _, entry := range reader.File {
		clean, err := safeArchivePath(entry.Name)
		if err != nil {
			return err
		}
		key := strings.ToLower(filepath.Clean(clean))
		if _, exists := seen[key]; exists {
			return fmt.Errorf("update archive contains duplicate path: %s", clean)
		}
		seen[key] = struct{}{}
		mode := entry.Mode()
		if mode&os.ModeSymlink != 0 || (!mode.IsDir() && !mode.IsRegular()) {
			return fmt.Errorf("update archive contains unsupported file type: %s", clean)
		}
		total += int64(entry.UncompressedSize64)
		if total > maxExtractBytes {
			return errors.New("update archive exceeds extraction size limit")
		}
		target := filepath.Join(destination, clean)
		if entry.FileInfo().IsDir() {
			if err := ensurePrivateDirectory(target); err != nil {
				return err
			}
			continue
		}
		if err := ensurePrivateDirectory(filepath.Dir(target)); err != nil {
			return err
		}
		source, err := entry.Open()
		if err != nil {
			return err
		}
		if err := writeExtractedFile(target, source, mode.Perm()); err != nil {
			source.Close()
			return err
		}
		if err := source.Close(); err != nil {
			return err
		}
	}
	return nil
}

func extractTarGzip(archivePath, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	seen := map[string]struct{}{}
	var count int
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		count++
		if count > maxArchiveFiles {
			return errors.New("update archive contains too many files")
		}
		clean, err := safeArchivePath(header.Name)
		if err != nil {
			return err
		}
		key := strings.ToLower(filepath.Clean(clean))
		if _, exists := seen[key]; exists {
			return fmt.Errorf("update archive contains duplicate path: %s", clean)
		}
		seen[key] = struct{}{}
		target := filepath.Join(destination, clean)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := ensurePrivateDirectory(target); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 {
				return errors.New("update archive contains a negative file size")
			}
			total += header.Size
			if total > maxExtractBytes {
				return errors.New("update archive exceeds extraction size limit")
			}
			if err := ensurePrivateDirectory(filepath.Dir(target)); err != nil {
				return err
			}
			if err := writeExtractedFile(target, io.LimitReader(reader, header.Size), os.FileMode(header.Mode).Perm()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("update archive contains unsupported file type: %s", clean)
		}
	}
	return nil
}

func safeArchivePath(name string) (string, error) {
	name = strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	if name == "" || strings.HasPrefix(name, "/") || filepath.IsAbs(name) {
		return "", errors.New("update archive contains an invalid path")
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.VolumeName(clean) != "" {
		return "", errors.New("update archive path escapes extraction root")
	}
	return clean, nil
}

func writeExtractedFile(path string, source io.Reader, permissions os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := restrictPrivateFile(path); err != nil {
		file.Close()
		return err
	}
	if _, err := io.Copy(file, source); err != nil {
		file.Close()
		return err
	}
	if permissions&0o111 != 0 {
		if err := file.Chmod(0o700); err != nil {
			file.Close()
			return err
		}
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func FindPackageRoot(destination, assetName string) (string, error) {
	packageName := strings.TrimSuffix(strings.TrimSuffix(assetName, ".zip"), ".tar.gz")
	root := filepath.Join(destination, packageName)
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("update archive does not contain the expected package root")
	}
	return root, nil
}
