package update

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractArchiveRejectsTraversal(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "bad.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("../outside.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte("unsafe"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ExtractArchive(archivePath, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("ExtractArchive() accepted path traversal")
	}
}

func TestExtractArchiveAndFindPackageRoot(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "cpa-manager-plus_v1.2.3_windows_amd64.zip")
	file, _ := os.Create(archivePath)
	writer := zip.NewWriter(file)
	entry, _ := writer.Create("cpa-manager-plus_v1.2.3_windows_amd64/cpa-manager-plus.exe")
	_, _ = entry.Write([]byte("binary"))
	_ = writer.Close()
	_ = file.Close()
	destination := filepath.Join(t.TempDir(), "out")
	if err := ExtractArchive(archivePath, destination); err != nil {
		t.Fatal(err)
	}
	root, err := FindPackageRoot(destination, filepath.Base(archivePath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "cpa-manager-plus.exe")); err != nil {
		t.Fatal(err)
	}
}
