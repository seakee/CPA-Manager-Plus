package buildinfo

import "testing"

func TestShortCommit(t *testing.T) {
	orig := SourceCommit
	t.Cleanup(func() { SourceCommit = orig })

	tests := []struct {
		name   string
		commit string
		want   string
	}{
		{name: "default unknown", commit: "unknown", want: "unknown"},
		{name: "blank fallback", commit: "   ", want: "unknown"},
		{name: "40-hex lowercase", commit: "72ea1eb9f452e9d9b02d161d33f5736d4cba4d88", want: "72ea1eb9"},
		{name: "40-hex uppercase", commit: "72EA1EB9F452E9D9B02D161D33F5736D4CBA4D88", want: "72EA1EB9"},
		{name: "short commit preserved", commit: "e6e5708e", want: "e6e5708e"},
		{name: "40-char non-hex preserved", commit: "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", want: "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			SourceCommit = tc.commit
			if got := ShortCommit(); got != tc.want {
				t.Fatalf("ShortCommit() with %q = %q, want %q", tc.commit, got, tc.want)
			}
		})
	}
}

func TestStartupLine(t *testing.T) {
	origVersion, origCommit, origDate := Version, SourceCommit, BuildDate
	t.Cleanup(func() {
		Version = origVersion
		SourceCommit = origCommit
		BuildDate = origDate
	})

	t.Run("official build identity", func(t *testing.T) {
		Version = "v2.0.0-beta.1"
		SourceCommit = "72ea1eb9f452e9d9b02d161d33f5736d4cba4d88"
		BuildDate = "2026-11-23T08:00:00Z"
		want := "CPA Manager Plus Version: v2.0.0-beta.1, Commit: 72ea1eb9, BuiltAt: 2026-11-23T08:00:00Z"
		if got := StartupLine(); got != want {
			t.Fatalf("StartupLine() = %q, want %q", got, want)
		}
	})

	t.Run("empty fallback", func(t *testing.T) {
		Version = ""
		SourceCommit = ""
		BuildDate = ""
		want := "CPA Manager Plus Version: dev, Commit: unknown, BuiltAt: unknown"
		if got := StartupLine(); got != want {
			t.Fatalf("StartupLine() = %q, want %q", got, want)
		}
	})
}
