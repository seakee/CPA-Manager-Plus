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
