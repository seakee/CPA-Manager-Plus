package usage

import (
	"encoding/json"
	"reflect"
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
		{name: "CPAMP management key", value: "cpamp_0123456789abcdefghijklmnopqrstuv"},
		{name: "session token", value: "sess-synthetic0123456789abc"},
		{name: "GitHub token", value: "ghp_synthetic0123456789abc"},
		{name: "Hugging Face token", value: "hf_synthetic0123456789abc"},
		{name: "publishable key", value: "pk_synthetic0123456789abc"},
		{name: "restricted key", value: "rk_synthetic0123456789abc"},
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
	for _, filename := range []string{
		"codex-account.json",
		"cpamp_account.json",
		"ghp_account.json",
		"hf_account.json",
		"pk_account.json",
		"rk_account.json",
		"sess-account.json",
	} {
		event := SanitizeForPersistence(Event{Source: filename, RawJSON: `{"source":"` + filename + `"}`})
		if event.Source != filename {
			t.Fatalf("source = %q, want %q", event.Source, filename)
		}
		if !strings.Contains(event.RawJSON, filename) {
			t.Fatalf("raw JSON lost non-secret source filename: %q", event.RawJSON)
		}
	}
}

func TestSanitizeForPersistenceRedactsStructuredCredentialAliases(t *testing.T) {
	const secretMarker = "ordinary-unprefixed-synthetic0123456789"
	for _, key := range []string{
		"managementKey",
		"cpaManagementKey",
		"cpa-management-key",
		"x-api-key",
		"xApiKey",
	} {
		t.Run(key, func(t *testing.T) {
			event := SanitizeForPersistence(Event{
				RawJSON: `{"` + key + `":"` + secretMarker + `"}`,
			})
			if strings.Contains(event.RawJSON, secretMarker) {
				t.Fatalf("raw JSON contains structured %s: %q", key, event.RawJSON)
			}
		})
	}
}

func TestSanitizeForPersistenceKeepsCompleteRedactedFailBody(t *testing.T) {
	const secretMarker = "synthetic0123456789"
	body := strings.Repeat("diagnostic ", maxFailSummaryBytes) +
		"access_token=" + secretMarker + " trailing-diagnostic"

	event := SanitizeForPersistence(Event{FailBody: body})

	if strings.Contains(event.FailBody, secretMarker) {
		t.Fatalf("fail body contains synthetic credential marker")
	}
	if !strings.Contains(event.FailBody, "trailing-diagnostic") {
		t.Fatalf("fail body was truncated: length=%d", len(event.FailBody))
	}
	if len(event.FailBody) <= maxFailSummaryBytes {
		t.Fatalf("fail body length=%d, want complete diagnostic", len(event.FailBody))
	}
}

func TestSanitizeForPersistenceCoversEveryEventTextField(t *testing.T) {
	const syntheticToken = "sk-proj-synthetic0123456789"
	event := Event{}
	value := reflect.ValueOf(&event).Elem()
	typeOfEvent := value.Type()
	for index := 0; index < value.NumField(); index++ {
		field := value.Field(index)
		if field.Kind() == reflect.String && field.CanSet() {
			field.SetString("metadata " + syntheticToken)
		}
	}
	event.ResponseMetadata = &ResponseHeaderMetadata{
		Errors: &HeaderErrorMetadata{AuthorizationError: syntheticToken},
	}

	sanitized := SanitizeForPersistence(event)
	sanitizedValue := reflect.ValueOf(sanitized)
	for index := 0; index < sanitizedValue.NumField(); index++ {
		field := sanitizedValue.Field(index)
		if field.Kind() == reflect.String && strings.Contains(field.String(), syntheticToken) {
			t.Errorf("%s contains synthetic credential marker", typeOfEvent.Field(index).Name)
		}
	}
	metadataJSON, err := json.Marshal(sanitized.ResponseMetadata)
	if err != nil {
		t.Fatalf("marshal sanitized response metadata: %v", err)
	}
	if strings.Contains(string(metadataJSON), syntheticToken) {
		t.Fatalf("response metadata contains synthetic credential marker")
	}
}
