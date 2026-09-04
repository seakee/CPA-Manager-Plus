package quotasnapshot

import "testing"

func TestGeneratedCodexSuffixFromID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		valid bool
	}{
		{name: "five hour", input: "-five-hour-0", want: "-five-hour-0", valid: true},
		{name: "five hour index", input: "-five-hour-12", want: "-five-hour-12", valid: true},
		{name: "weekly", input: "-weekly-0", want: "-weekly-0", valid: true},
		{name: "monthly", input: "-monthly-3", want: "-monthly-3", valid: true},
		{name: "generic seconds", input: "-0-window-86400-0", want: "-0-window-86400-0", valid: true},
		{name: "generic duration", input: "-12-window-604800-4", want: "-12-window-604800-4", valid: true},
		{name: "generic day duration", input: "-1-window-1d-0", want: "-1-window-1d-0", valid: true},
		{name: "generic hour duration", input: "-1-window-24h-0", want: "-1-window-24h-0", valid: true},
		{name: "generic unknown duration", input: "-1-window-unknown-0", want: "-1-window-unknown-0", valid: true},
		{name: "normalizes case and whitespace", input: "  -FIVE-HOUR-12  ", want: "-five-hour-12", valid: true},
		{name: "missing index", input: "-five-hour", valid: false},
		{name: "invalid five hour index", input: "-five-hour-x", valid: false},
		{name: "missing weekly index", input: "-weekly", valid: false},
		{name: "invalid weekly index", input: "-weekly-x", valid: false},
		{name: "invalid monthly index", input: "-monthly-name", valid: false},
		{name: "missing generic family index", input: "-window-86400-0", valid: false},
		{name: "invalid generic duration", input: "-0-window-invalid-0", valid: false},
		{name: "invalid generic index", input: "-0-window-86400-x", valid: false},
		{name: "unknown family role", input: "-foo-weekly-0", valid: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, valid := generatedCodexSuffixFromID(test.input)
			if valid != test.valid {
				t.Fatalf("generatedCodexSuffixFromID(%q) valid = %v, want %v", test.input, valid, test.valid)
			}
			if valid && got != test.want {
				t.Fatalf("generatedCodexSuffixFromID(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}
