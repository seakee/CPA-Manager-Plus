package usage

import (
	"strings"
	"testing"
)

func TestSanitizeForPersistenceRedactsCredentialText(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "legacy API key", value: "sk-synthetic0123456789"},
		{name: "project API key in source label", value: "paid provider sk-proj-synthetic0123456789"},
		{name: "bearer token", value: "Bearer synthetic0123456789"},
		{name: "OAuth access token", value: "access_token=synthetic0123456789"},
		{name: "OAuth refresh token", value: "refresh_token=synthetic0123456789"},
		{name: "OAuth identity token", value: "id_token=synthetic0123456789"},
		{name: "management key field", value: "management-key=synthetic0123456789"},
		{name: "CPAMP management key", value: "cpamp_synthetic0123456789"},
		{name: "authorization header", value: "Authorization: Bearer synthetic0123456789"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := SanitizeForPersistence(Event{
				Source:  test.value,
				RawJSON: `{"source":"` + test.value + `"}`,
			})
			if strings.Contains(event.Source, "synthetic0123456789") {
				t.Fatalf("source contains synthetic credential marker: %q", event.Source)
			}
			if strings.Contains(event.RawJSON, "synthetic0123456789") {
				t.Fatalf("raw JSON contains synthetic credential marker: %q", event.RawJSON)
			}
		})
	}
}

func TestSanitizeForPersistencePreservesCredentialFilename(t *testing.T) {
	const filename = "codex-account.json"
	event := SanitizeForPersistence(Event{Source: filename, RawJSON: `{"source":"` + filename + `"}`})
	if event.Source != filename {
		t.Fatalf("source = %q, want %q", event.Source, filename)
	}
	if !strings.Contains(event.RawJSON, filename) {
		t.Fatalf("raw JSON lost non-secret source filename: %q", event.RawJSON)
	}
}
