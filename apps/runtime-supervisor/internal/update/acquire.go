package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
)

// AcquireDownloadFunc streams a candidate release archive into destination while
// reporting byte progress.
type AcquireDownloadFunc func(ctx context.Context, destination io.Writer, progress ProgressFunc) error

// CopyUploadArchive adapts a raw .tar.gz upload stream into an AcquireDownloadFunc
// bounded by MaxAcquisitionArchiveBytes (64 MiB).
func CopyUploadArchive(src io.Reader, contentLength int64) AcquireDownloadFunc {
	return func(ctx context.Context, destination io.Writer, progress ProgressFunc) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if src == nil || destination == nil {
			return fmt.Errorf("%w: upload archive stream is required", ErrArchiveInvalid)
		}
		written, err := copyBoundedArchive(src, destination, contentLength, progress)
		if err != nil {
			if errors.Is(err, ErrArchiveTooLarge) {
				return err
			}
			return fmt.Errorf("%w: read uploaded archive: %v", ErrArchiveInvalid, err)
		}
		if contentLength >= 0 && written != contentLength {
			return fmt.Errorf("%w: uploaded size differs from Content-Length", ErrArchiveInvalid)
		}
		if written == 0 {
			return fmt.Errorf("%w: uploaded archive is empty", ErrArchiveInvalid)
		}
		return nil
	}
}

// AcquireStageResult records the verified stage metadata alongside whether this
// acquisition created the finalized <version>/ directory itself (so pre-commit
// failures can remove a newly created stage without ever removing a
// pre-existing prepare_update stage).
type AcquireStageResult struct {
	Metadata     Metadata
	CreatedStage bool
}

var (
	acquirePostRenameHook         func(finalPath string) error
	acquireRemoveCreatedStageHook func(finalPath string) error
)

// SetAcquireStageHooksForTesting configures fault-injection hooks around stage
// publication and uncommitted stage removal in tests.
func SetAcquireStageHooksForTesting(postRename, removeCreated func(finalPath string) error) {
	acquirePostRenameHook = postRename
	acquireRemoveCreatedStageHook = removeCreated
}

// Acquire verifies and atomically stages the pinned CPA version and exact
// executable artifactId from any transport (official GitHub, CDN prefix, or
// raw upload). It always consumes and verifies the archive stream first; an
// existing finalized stage for version with the same artifactId is then reused
// without overwriting, while a conflicting finalized stage is refused with
// ErrArtifactConflict.
func (s *Store) Acquire(
	ctx context.Context,
	version string,
	expectedArtifactID artifact.ID,
	download AcquireDownloadFunc,
	progress ProgressFunc,
	onPhase func(string),
) (Metadata, error) {
	res, err := s.AcquireStage(ctx, version, expectedArtifactID, download, progress, onPhase)
	if err != nil {
		return Metadata{}, err
	}
	return res.Metadata, nil
}

// AcquireStage performs Acquire and reports whether the finalized <version>/
// stage directory was newly created by this operation.
func (s *Store) AcquireStage(
	ctx context.Context,
	version string,
	expectedArtifactID artifact.ID,
	download AcquireDownloadFunc,
	progress ProgressFunc,
	onPhase func(string),
) (AcquireStageResult, error) {
	if err := ctx.Err(); err != nil {
		return AcquireStageResult{}, err
	}
	if s == nil || download == nil || ValidateVersion(version) != nil || !expectedArtifactID.IsValid() {
		return AcquireStageResult{}, fmt.Errorf("%w: invalid acquire request", ErrStaging)
	}
	finalPath := filepath.Join(s.root, version)
	if _, _, _, err := s.verifyAcquiredFinal(finalPath, version, expectedArtifactID); err != nil {
		return AcquireStageResult{}, err
	}
	temporary, err := os.MkdirTemp(s.root, temporaryPrefix)
	if err != nil {
		return AcquireStageResult{}, fmt.Errorf("%w: create operation temporary directory: %v", ErrStaging, err)
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0o700); err != nil {
		return AcquireStageResult{}, fmt.Errorf("%w: restrict operation temporary directory: %v", ErrStaging, err)
	}

	if onPhase != nil {
		onPhase("downloading")
	}
	archivePath := filepath.Join(temporary, "source.tar.gz")
	archiveFile, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return AcquireStageResult{}, fmt.Errorf("%w: create temporary archive: %v", ErrStaging, err)
	}
	archiveHasher := sha256.New()
	boundedWriter := &boundedArchiveWriter{
		writer: io.MultiWriter(archiveFile, archiveHasher),
		limit:  MaxAcquisitionArchiveBytes,
	}
	downloadErr := download(ctx, boundedWriter, progress)
	syncErr := archiveFile.Sync()
	closeErr := archiveFile.Close()
	if downloadErr != nil {
		return AcquireStageResult{}, downloadErr
	}
	if boundedWriter.written == 0 {
		return AcquireStageResult{}, fmt.Errorf("%w: archive is empty", ErrArchiveInvalid)
	}
	if err := errors.Join(syncErr, closeErr); err != nil {
		return AcquireStageResult{}, fmt.Errorf("%w: persist temporary archive: %v", ErrStaging, err)
	}
	sourceArchiveDigest := "sha256:" + hex.EncodeToString(archiveHasher.Sum(nil))

	if onPhase != nil {
		onPhase("verifying")
	}
	publication := filepath.Join(temporary, "final")
	if err := os.Mkdir(publication, 0o700); err != nil {
		return AcquireStageResult{}, fmt.Errorf("%w: create publication directory: %v", ErrStaging, err)
	}
	if err := extractAcquiredStage(archivePath, publication, expectedArtifactID); err != nil {
		return AcquireStageResult{}, err
	}
	metadata := Metadata{
		SchemaVersion:       stagedArtifactSchemaVersion,
		Engine:              artifact.EngineCPA,
		Version:             version,
		ArtifactID:          expectedArtifactID,
		SourceArchiveDigest: sourceArchiveDigest,
	}
	if err := writeMetadata(filepath.Join(publication, stagedMetadataName), metadata); err != nil {
		return AcquireStageResult{}, err
	}
	if err := syncDirectory(publication); err != nil {
		return AcquireStageResult{}, fmt.Errorf("%w: sync publication directory: %v", ErrStaging, err)
	}

	if onPhase != nil {
		onPhase("installing")
	}
	existing, complete, existsWithoutLicense, err := s.verifyAcquiredFinal(finalPath, version, expectedArtifactID)
	if err != nil {
		return AcquireStageResult{}, err
	}
	if complete {
		return AcquireStageResult{Metadata: existing, CreatedStage: false}, nil
	}
	createdStage := false
	if existsWithoutLicense {
		if err := s.installLicenseIntoExistingStage(finalPath, filepath.Join(publication, stagedLicenseName)); err != nil {
			return AcquireStageResult{}, err
		}
	} else {
		if err := os.Rename(publication, finalPath); err != nil {
			raceMeta, raceComplete, raceNoLicense, verifyErr := s.verifyAcquiredFinal(finalPath, version, expectedArtifactID)
			if verifyErr != nil {
				return AcquireStageResult{}, verifyErr
			}
			if raceComplete {
				return AcquireStageResult{Metadata: raceMeta, CreatedStage: false}, nil
			}
			if !raceNoLicense {
				return AcquireStageResult{}, fmt.Errorf("%w: atomically publish finalized stage: %v", ErrStaging, err)
			}
			if installErr := s.installLicenseIntoExistingStage(finalPath, filepath.Join(publication, stagedLicenseName)); installErr != nil {
				return AcquireStageResult{}, installErr
			}
		} else {
			createdStage = true
		}
		if createdStage {
			postRenameErr := func() error {
				if acquirePostRenameHook != nil {
					if hookErr := acquirePostRenameHook(finalPath); hookErr != nil {
						return fmt.Errorf("%w: %v", ErrStaging, hookErr)
					}
				}
				if prepErr := s.prepareFinalizedExecutionDirectory(finalPath); prepErr != nil {
					return prepErr
				}
				if syncErr := syncDirectory(s.root); syncErr != nil {
					return fmt.Errorf("%w: sync staging root: %v", ErrStaging, syncErr)
				}
				return nil
			}()
			if postRenameErr != nil {
				return AcquireStageResult{}, errors.Join(postRenameErr, s.RemoveCreatedStage(version))
			}
		}
	}
	verified, verifiedComplete, _, err := s.verifyAcquiredFinal(finalPath, version, expectedArtifactID)
	if err != nil || !verifiedComplete {
		if err == nil {
			err = fmt.Errorf("%w: finalized stage disappeared", ErrStaging)
		}
		if createdStage {
			err = errors.Join(err, s.RemoveCreatedStage(version))
		}
		return AcquireStageResult{}, err
	}
	return AcquireStageResult{Metadata: verified, CreatedStage: createdStage}, nil
}

// RemoveCreatedStage removes a finalized <version>/ stage directory that was
// created by the current acquisition when selection commit has definitely not
// occurred.
func (s *Store) RemoveCreatedStage(version string) error {
	if s == nil || ValidateVersion(version) != nil {
		return fmt.Errorf("%w: invalid stage version for removal", ErrStaging)
	}
	finalPath := filepath.Join(s.root, version)
	if acquireRemoveCreatedStageHook != nil {
		if hookErr := acquireRemoveCreatedStageHook(finalPath); hookErr != nil {
			return fmt.Errorf("%w: remove uncommitted stage: %v", ErrStaging, hookErr)
		}
	}
	var chmodErr error
	if info, err := os.Lstat(finalPath); err == nil && info.IsDir() {
		chmodErr = os.Chmod(finalPath, 0o700)
	}
	removeErr := os.RemoveAll(finalPath)
	if _, statErr := os.Lstat(finalPath); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
		if removeErr == nil {
			removeErr = statErr
		}
		return fmt.Errorf("%w: remove uncommitted stage: %v", ErrStaging, errors.Join(chmodErr, removeErr))
	}
	if removeErr != nil {
		return fmt.Errorf("%w: remove uncommitted stage: %v", ErrStaging, errors.Join(chmodErr, removeErr))
	}
	if syncErr := syncDirectory(s.root); syncErr != nil {
		return fmt.Errorf("%w: sync staging root after removing uncommitted stage: %v", ErrStaging, syncErr)
	}
	return nil
}

// HasAcquiredStage reports whether version has a finalized stage matching
// expectedArtifactID that also includes a verified LICENSE file.
func (s *Store) HasAcquiredStage(version string, expectedArtifactID artifact.ID) bool {
	if s == nil || ValidateVersion(version) != nil || !expectedArtifactID.IsValid() {
		return false
	}
	_, complete, _, err := s.verifyAcquiredFinal(filepath.Join(s.root, version), version, expectedArtifactID)
	return err == nil && complete
}

func (s *Store) verifyAcquiredFinal(finalPath, version string, expectedArtifactID artifact.ID) (Metadata, bool, bool, error) {
	metadata, found, err := s.verifyFinalizedPath(finalPath, version)
	if !found && err == nil {
		return Metadata{}, false, false, nil
	}
	if err != nil {
		if errors.Is(err, ErrStageConflict) {
			return Metadata{}, false, false, fmt.Errorf("%w: %v", ErrArtifactConflict, err)
		}
		return Metadata{}, false, false, err
	}
	if metadata.ArtifactID != expectedArtifactID {
		return Metadata{}, false, false, fmt.Errorf("%w: finalized artifact ID %s differs from pinned %s", ErrArtifactConflict, metadata.ArtifactID, expectedArtifactID)
	}
	if _, err := os.Lstat(filepath.Join(finalPath, stagedLicenseName)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return metadata, false, true, nil
		}
		return Metadata{}, false, true, fmt.Errorf("%w: stat finalized license: %v", ErrStaging, err)
	}
	if err := s.prepareFinalizedExecutionDirectory(finalPath); err != nil {
		return Metadata{}, false, false, err
	}
	return metadata, true, false, nil
}

func (s *Store) installLicenseIntoExistingStage(finalPath, extractedLicensePath string) error {
	licenseBytes, err := os.ReadFile(extractedLicensePath)
	if err != nil || len(licenseBytes) == 0 || len(licenseBytes) > maxExtractedLicenseBytes {
		return fmt.Errorf("%w: read verified LICENSE: %v", ErrStaging, err)
	}
	dirInfo, err := os.Lstat(finalPath)
	if err != nil || !dirInfo.IsDir() {
		return fmt.Errorf("%w: stat existing finalized stage: %v", ErrStaging, err)
	}
	origPerm := dirInfo.Mode().Perm()
	if origPerm&0o200 == 0 {
		if err := os.Chmod(finalPath, origPerm|0o200); err != nil {
			return fmt.Errorf("%w: unlock finalized stage for LICENSE publication: %v", ErrStaging, err)
		}
		defer func() {
			_ = os.Chmod(finalPath, origPerm)
			_ = s.prepareFinalizedExecutionDirectory(finalPath)
		}()
	}
	tempFile, err := os.CreateTemp(finalPath, ".license-tmp-*")
	if err != nil {
		return fmt.Errorf("%w: create temporary LICENSE in finalized stage: %v", ErrStaging, err)
	}
	tempPath := tempFile.Name()
	defer func() {
		if tempPath != "" {
			_ = os.Remove(tempPath)
		}
	}()
	written, writeErr := tempFile.Write(licenseBytes)
	chmodErr := tempFile.Chmod(0o444)
	syncErr := tempFile.Sync()
	closeErr := tempFile.Close()
	if err := errors.Join(writeErr, chmodErr, syncErr, closeErr); err != nil || written != len(licenseBytes) {
		return fmt.Errorf("%w: write temporary LICENSE in finalized stage: %v", ErrStaging, err)
	}
	if err := os.Rename(tempPath, filepath.Join(finalPath, stagedLicenseName)); err != nil {
		return fmt.Errorf("%w: atomically publish LICENSE in finalized stage: %v", ErrStaging, err)
	}
	tempPath = ""
	if origPerm&0o200 == 0 {
		if err := os.Chmod(finalPath, origPerm); err != nil {
			return fmt.Errorf("%w: restore finalized stage permissions: %v", ErrStaging, err)
		}
	}
	if err := s.prepareFinalizedExecutionDirectory(finalPath); err != nil {
		return err
	}
	if err := syncDirectory(finalPath); err != nil {
		return fmt.Errorf("%w: sync finalized stage directory: %v", ErrStaging, err)
	}
	return nil
}

type boundedArchiveWriter struct {
	writer  io.Writer
	limit   int64
	written int64
}

func (w *boundedArchiveWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.limit-w.written {
		return 0, fmt.Errorf("%w: archive exceeds %d bytes", ErrArchiveTooLarge, w.limit)
	}
	n, err := w.writer.Write(p)
	w.written += int64(n)
	return n, err
}

func extractAcquiredStage(archivePath, publicationDir string, expectedArtifactID artifact.ID) error {
	archiveFile, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("%w: open archive: %v", ErrStaging, err)
	}
	defer archiveFile.Close()
	gzipReader, err := gzip.NewReader(archiveFile)
	if err != nil {
		return fmt.Errorf("%w: open gzip archive: %v", ErrArchiveInvalid, err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)

	var (
		foundExecutable  bool
		foundLicense     bool
		expandedBytes    int64
		executableDigest artifact.ID
	)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: inspect tar archive: %v", ErrArchiveInvalid, err)
		}
		if err := validateArchivePath(header.Name); err != nil {
			return fmt.Errorf("%w: %v", ErrArchiveInvalid, err)
		}
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeRegA, tar.TypeDir, tar.TypeXHeader, tar.TypeXGlobalHeader:
		default:
			return fmt.Errorf("%w: unsupported archive entry type %d", ErrArchiveInvalid, header.Typeflag)
		}
		if header.Linkname != "" {
			return fmt.Errorf("%w: archive links are not allowed", ErrArchiveInvalid)
		}
		if header.Size < 0 || header.Size > maxExpandedArchiveBytes-expandedBytes {
			return fmt.Errorf("%w: archive expanded size exceeds limit", ErrArchiveInvalid)
		}
		expandedBytes += header.Size

		switch header.Name {
		case stagedExecutableName:
			if foundExecutable {
				return fmt.Errorf("%w: duplicate %s archive entry", ErrArchiveInvalid, stagedExecutableName)
			}
			foundExecutable = true
			if (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) ||
				header.Size <= 0 || header.Size > maxExtractedBinaryBytes {
				return fmt.Errorf("%w: staged executable entry type or size is invalid", ErrArchiveInvalid)
			}
			executablePath := filepath.Join(publicationDir, stagedExecutableName)
			file, err := os.OpenFile(executablePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return fmt.Errorf("%w: create staged executable: %v", ErrStaging, err)
			}
			hasher := sha256.New()
			written, copyErr := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(tarReader, maxExtractedBinaryBytes+1))
			chmodErr := file.Chmod(0o755)
			syncErr := file.Sync()
			closeErr := file.Close()
			if err := errors.Join(copyErr, chmodErr, syncErr, closeErr); err != nil ||
				written != header.Size || written > maxExtractedBinaryBytes {
				return fmt.Errorf("%w: extract staged executable", ErrArchiveInvalid)
			}
			executableDigest = artifact.ID("sha256:" + hex.EncodeToString(hasher.Sum(nil)))

		case stagedLicenseName:
			if foundLicense {
				return fmt.Errorf("%w: duplicate %s archive entry", ErrArchiveInvalid, stagedLicenseName)
			}
			foundLicense = true
			if (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) ||
				header.Size <= 0 || header.Size > maxExtractedLicenseBytes {
				return fmt.Errorf("%w: staged LICENSE entry type or size is invalid", ErrArchiveInvalid)
			}
			licensePath := filepath.Join(publicationDir, stagedLicenseName)
			file, err := os.OpenFile(licensePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return fmt.Errorf("%w: create staged LICENSE: %v", ErrStaging, err)
			}
			written, copyErr := io.Copy(file, io.LimitReader(tarReader, maxExtractedLicenseBytes+1))
			chmodErr := file.Chmod(0o444)
			syncErr := file.Sync()
			closeErr := file.Close()
			if err := errors.Join(copyErr, chmodErr, syncErr, closeErr); err != nil ||
				written != header.Size || written > maxExtractedLicenseBytes {
				return fmt.Errorf("%w: extract staged LICENSE", ErrArchiveInvalid)
			}

		default:
			if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
				written, copyErr := io.Copy(io.Discard, io.LimitReader(tarReader, header.Size+1))
				if copyErr != nil || written != header.Size {
					return fmt.Errorf("%w: read archive entry %s", ErrArchiveInvalid, header.Name)
				}
			}
		}
	}
	if !foundExecutable {
		return fmt.Errorf("%w: archive does not contain %s", ErrArchiveInvalid, stagedExecutableName)
	}
	if executableDigest != expectedArtifactID {
		return fmt.Errorf("%w: got %s, want %s", ErrArtifactMismatch, executableDigest, expectedArtifactID)
	}
	if !foundLicense {
		return fmt.Errorf("%w: archive does not contain %s", ErrArchiveInvalid, stagedLicenseName)
	}
	return nil
}
