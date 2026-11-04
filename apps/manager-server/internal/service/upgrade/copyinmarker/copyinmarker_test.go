package copyinmarker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validMarker(root string) Marker {
	return Marker{
		Version:                 Version,
		Phase:                   PhaseMaterializing,
		TargetDatabase:          filepath.Join(root, "usage.sqlite"),
		SourceKind:              "upgraded-legacy",
		SourceFingerprintBefore: "before",
		SourceFingerprintAfter:  "before",
		BackupManifestSHA256:    "digest",
		WrittenAtMS:             1,
	}
}

func TestWriteAndReadRoundTrip(t *testing.T) {
	root := t.TempDir()
	if _, found, err := Read(root); err != nil || found {
		t.Fatalf("read before write = %t/%v", found, err)
	}
	marker := validMarker(root)
	if err := Write(root, marker); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	stored, found, err := Read(root)
	if err != nil || !found {
		t.Fatalf("read marker = %t/%v", found, err)
	}
	if stored != marker {
		t.Fatalf("stored marker = %+v, want %+v", stored, marker)
	}
	if _, err := os.Lstat(Path(root) + tempSuffix); !os.IsNotExist(err) {
		t.Fatalf("write left its temporary behind (err=%v)", err)
	}

	// A second write replaces the first atomically: the reader never sees a
	// merged record.
	marker.BackupManifestSHA256 = "other-digest"
	if err := Write(root, marker); err != nil {
		t.Fatalf("replace marker: %v", err)
	}
	if replaced, _, err := Read(root); err != nil || replaced.BackupManifestSHA256 != "other-digest" {
		t.Fatalf("replaced marker = %+v/%v", replaced, err)
	}
}

func TestWriteRefusesAMarkerThatDescribesNothing(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		name   string
		change func(m *Marker)
	}{
		{name: "unknown version", change: func(m *Marker) { m.Version = Version + 1 }},
		{name: "no phase", change: func(m *Marker) { m.Phase = "" }},
		{name: "unknown phase", change: func(m *Marker) { m.Phase = "copying" }},
		{name: "migrating without identity", change: func(m *Marker) {
			m.Phase = PhaseMigrating
			m.TargetDatabaseIdentity = FileIdentity{}
		}},
		{name: "relative target", change: func(m *Marker) { m.TargetDatabase = "usage.sqlite" }},
		{name: "no source kind", change: func(m *Marker) { m.SourceKind = "" }},
		{name: "no fingerprints", change: func(m *Marker) { m.SourceFingerprintBefore = "" }},
		{name: "no backup digest", change: func(m *Marker) { m.BackupManifestSHA256 = "" }},
		{name: "negative timestamp", change: func(m *Marker) { m.WrittenAtMS = -1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			marker := validMarker(root)
			test.change(&marker)
			if err := Write(root, marker); err == nil {
				t.Fatal("an incomplete marker was published")
			}
			if _, err := os.Lstat(Path(root)); !os.IsNotExist(err) {
				t.Fatalf("a refused write published a marker (err=%v)", err)
			}
		})
	}
}

func TestReadRefusesAnUnreadableMarker(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "not json", content: "{"},
		{name: "trailing content", content: strings.TrimSpace(`{"version":1,"targetDatabase":"/x/usage.sqlite","sourceKind":"k","sourceFingerprintBefore":"b","sourceFingerprintAfter":"a","backupManifestSha256":"d","writtenAtMs":1}`) + "{}"},
		{name: "unknown field", content: strings.TrimSpace(`{"version":1,"targetDatabase":"/x/usage.sqlite","sourceKind":"k","sourceFingerprintBefore":"b","sourceFingerprintAfter":"a","backupManifestSha256":"d","writtenAtMs":1,"stage":"complete"}`)},
		{name: "unknown version", content: strings.TrimSpace(`{"version":7,"targetDatabase":"/x/usage.sqlite","sourceKind":"k","sourceFingerprintBefore":"b","sourceFingerprintAfter":"a","backupManifestSha256":"d","writtenAtMs":1}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(Directory(root), 0o700); err != nil {
				t.Fatalf("create the work directory: %v", err)
			}
			if err := os.WriteFile(Path(root), []byte(test.content), 0o600); err != nil {
				t.Fatalf("write the marker: %v", err)
			}
			if _, found, err := Read(root); err == nil || found {
				t.Fatalf("unreadable marker = %t/%v, want an error", found, err)
			}
		})
	}

	// A marker that is not a regular file is refused rather than followed.
	root := t.TempDir()
	if err := os.MkdirAll(Path(root), 0o700); err != nil {
		t.Fatalf("create a marker directory: %v", err)
	}
	if _, _, err := Read(root); err == nil {
		t.Fatal("a marker directory was accepted")
	}
}

func TestReadAcceptsThePublishedForm(t *testing.T) {
	root := t.TempDir()
	marker := validMarker(root)
	data, err := json.Marshal(marker)
	if err != nil {
		t.Fatalf("encode marker: %v", err)
	}
	if err := os.MkdirAll(Directory(root), 0o700); err != nil {
		t.Fatalf("create the work directory: %v", err)
	}
	if err := os.WriteFile(Path(root), data, 0o600); err != nil {
		t.Fatalf("write the marker: %v", err)
	}
	stored, found, err := Read(root)
	if err != nil || !found || stored != marker {
		t.Fatalf("read = %+v/%t/%v, want the published marker", stored, found, err)
	}
}

// TestIdentitySurvivesARenameAndARewriteAndDetectsAnotherFile is the property
// the marker depends on: moving a member into the v2 layout and rewriting it in
// place both keep the file the marker recorded, while a different file under the
// same name does not.
func TestIdentitySurvivesARenameAndARewriteAndDetectsAnotherFile(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "usage.sqlite")
	if err := os.WriteFile(original, []byte("copied store"), 0o600); err != nil {
		t.Fatalf("write the copied store: %v", err)
	}
	before, err := Identity(original)
	if err != nil {
		t.Fatalf("read the identity: %v", err)
	}
	if !before.Valid() {
		t.Fatalf("identity %+v does not name a file", before)
	}

	moved := filepath.Join(root, "moved.sqlite")
	if err := os.Rename(original, moved); err != nil {
		t.Fatalf("move the copied store: %v", err)
	}
	if after, err := Identity(moved); err != nil || after != before {
		t.Fatalf("identity after the rename = %+v/%v, want %+v", after, err, before)
	}

	// The migration rewrites the database at its own path; that keeps the file.
	if err := os.WriteFile(moved, []byte("migrated store, much longer"), 0o600); err != nil {
		t.Fatalf("rewrite the copied store: %v", err)
	}
	if rewritten, err := Identity(moved); err != nil || rewritten != before {
		t.Fatalf("identity after an in-place rewrite = %+v/%v, want %+v", rewritten, err, before)
	}

	// A file swapped in under the same name is not the store that was recorded.
	// The replacement is written beside the recorded file and renamed over it, so
	// the two files exist at the same time and the filesystem cannot hand the
	// recorded inode back to the new one. Removing the recorded file first makes
	// this depend on the allocator: ext4 reuses a just-freed inode for the very
	// next file, and the check then fails on a correct product (CI, Linux).
	foreignPath := filepath.Join(root, "foreign.sqlite")
	if err := os.WriteFile(foreignPath, []byte("foreign store"), 0o600); err != nil {
		t.Fatalf("write the foreign store: %v", err)
	}
	if err := os.Rename(foreignPath, moved); err != nil {
		t.Fatalf("swap the foreign store in: %v", err)
	}
	foreign, err := Identity(moved)
	if err != nil {
		t.Fatalf("read the foreign identity: %v", err)
	}
	if foreign == before {
		t.Fatalf("a swapped-in file kept identity %+v", before)
	}
}

// TestSameTargetComparesTheRootLocalIdentity is the provenance rule both readers
// apply: a marker describes the store in the root it sits in, so the same
// database name at another absolute path is the same store, and a different name
// is a different store whatever the path says.
func TestSameTargetComparesTheRootLocalIdentity(t *testing.T) {
	for _, test := range []struct {
		name             string
		recorded, actual string
		want             bool
	}{
		{"same root", filepath.Join("/data", "usage.sqlite"), filepath.Join("/data", "usage.sqlite"), true},
		{"relocated root", filepath.Join("/host/data", "usage.sqlite"), filepath.Join("/mnt/volume/data", "usage.sqlite"), true},
		{"relative actual", filepath.Join("/data", "usage.sqlite"), "usage.sqlite", true},
		{"another database", filepath.Join("/data", "other.sqlite"), filepath.Join("/data", "usage.sqlite"), false},
		{"empty record", "", filepath.Join("/data", "usage.sqlite"), false},
		{"empty actual", filepath.Join("/data", "usage.sqlite"), "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := SameTarget(test.recorded, test.actual); got != test.want {
				t.Fatalf("SameTarget(%q, %q) = %t, want %t", test.recorded, test.actual, got, test.want)
			}
		})
	}
}
