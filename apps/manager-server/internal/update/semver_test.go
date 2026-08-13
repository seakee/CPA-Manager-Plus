package update

import "testing"

func TestCompareVersionsUsesSemverPrereleaseRules(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		want    int
	}{
		{"v1.11.12", "v1.12.0", 1},
		{"v1.12.0-beta.3", "v1.12.0", 1},
		{"v1.12.0", "v1.12.0-beta.3", -1},
		{"1.12.0", "v1.12.0", 0},
	}
	for _, test := range tests {
		got, ok := CompareVersions(test.current, test.latest)
		if !ok || got != test.want {
			t.Errorf("CompareVersions(%q, %q) = (%d, %v), want (%d, true)", test.current, test.latest, got, ok, test.want)
		}
	}
}

func TestCompareVersionsRejectsUnknownVersions(t *testing.T) {
	for _, version := range []string{"", "dev", "v1.2", "latest"} {
		if _, ok := CompareVersions(version, "v1.2.3"); ok {
			t.Errorf("CompareVersions(%q, v1.2.3) unexpectedly succeeded", version)
		}
	}
}

func TestVersionAtLeast(t *testing.T) {
	if !VersionAtLeast("v1.1.0", "v1.0.0") || !VersionAtLeast("v1.0.0", "v1.0.0") || VersionAtLeast("v0.9.9", "v1.0.0") {
		t.Fatal("VersionAtLeast() returned an unexpected comparison")
	}
}
