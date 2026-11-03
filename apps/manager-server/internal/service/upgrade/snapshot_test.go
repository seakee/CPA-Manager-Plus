package upgrade

import (
	"path/filepath"
	"testing"
)

// TestPathWithinTreatsTheFilesystemRootAsAParent pins the containment rule the
// snapshot exclusion rests on. The root's canonical spelling is the separator
// alone, so a prefix built by appending a separator there becomes "//", which no
// cleaned path starts with: every descendant of "/" was reported as outside it,
// and a source root configured as "/" let the whole database copy land inside
// the tree it was meant to preserve. Equal paths, a parent already ending in a
// separator, and a sibling whose name merely starts with the parent's are the
// other boundaries of the same comparison.
func TestPathWithinTreatsTheFilesystemRootAsAParent(t *testing.T) {
	separator := string(filepath.Separator)
	root := separator
	nested := filepath.Join(root, "a", "b", "c")
	sibling := filepath.Join(root, "ab")

	for _, testCase := range []struct {
		name   string
		child  string
		parent string
		want   bool
	}{
		{name: "equal paths", child: nested, parent: nested, want: true},
		{name: "filesystem root itself", child: root, parent: root, want: true},
		{name: "descendant of the filesystem root", child: nested, parent: root, want: true},
		{name: "deep descendant", child: nested, parent: filepath.Join(root, "a", "b"), want: true},
		{name: "sibling with the parent as a name prefix", child: sibling, parent: filepath.Join(root, "a"), want: false},
		{name: "parent with a trailing separator", child: nested, parent: filepath.Join(root, "a") + separator, want: true},
		{name: "child with a trailing separator", child: nested + separator, parent: root, want: true},
		{name: "root is not under a child", child: filepath.Join(root, "a"), parent: nested, want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := pathWithin(testCase.child, testCase.parent); got != testCase.want {
				t.Fatalf("pathWithin(%q, %q) = %t, want %t", testCase.child, testCase.parent, got, testCase.want)
			}
		})
	}
}
