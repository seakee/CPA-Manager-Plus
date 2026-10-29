package cpa

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClassifyExternalVersion(t *testing.T) {
	tests := []struct {
		name        string
		version     string
		wantSupport string
		wantBlocked bool
		wantReason  string
	}{
		{name: "v8 is the written contract", version: "v8.0.13", wantSupport: VersionSupportSupported},
		{name: "bare v8 is accepted", version: "8.0.13", wantSupport: VersionSupportSupported},
		{name: "padded v8", version: " v8.1.0 ", wantSupport: VersionSupportSupported},
		{name: "later major", version: "v9.2.1", wantSupport: VersionSupportSupported},
		{
			name: "older major is refused for writes", version: "v7.2.130",
			wantSupport: VersionSupportUnsupported, wantBlocked: true, wantReason: ReasonExternalVersionUnsupported,
		},
		{
			name: "v6 is refused for writes", version: "v6.10.8",
			wantSupport: VersionSupportUnsupported, wantBlocked: true, wantReason: ReasonExternalVersionUnsupported,
		},
		{
			name: "missing version is unknown and refused", version: "",
			wantSupport: VersionSupportUnknown, wantBlocked: true, wantReason: ReasonExternalVersionUnsupported,
		},
		{
			name: "unparsable version is unknown and refused", version: "latest",
			wantSupport: VersionSupportUnknown, wantBlocked: true, wantReason: ReasonExternalVersionUnsupported,
		},
		{
			name: "zero major is unknown and refused", version: "v0.0.1",
			wantSupport: VersionSupportUnknown, wantBlocked: true, wantReason: ReasonExternalVersionUnsupported,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := ClassifyExternalVersion(test.version)
			if report.Support != test.wantSupport {
				t.Fatalf("support = %q, want %q", report.Support, test.wantSupport)
			}
			if report.WriteBlocked != test.wantBlocked {
				t.Fatalf("writeBlocked = %v, want %v", report.WriteBlocked, test.wantBlocked)
			}
			if report.Reason != test.wantReason {
				t.Fatalf("reason = %q, want %q", report.Reason, test.wantReason)
			}
		})
	}
}

func TestObserveExternalVersionClassifiesTheHeader(t *testing.T) {
	tests := []struct {
		name        string
		header      map[string]string
		status      int
		wantSupport string
		wantBlocked bool
		wantErr     error
	}{
		{
			name: "supported CPA", header: map[string]string{"X-CPA-Version": "v8.0.13"}, status: http.StatusOK,
			wantSupport: VersionSupportSupported,
		},
		{
			name: "unsupported CPA", header: map[string]string{"X-CPA-Version": "v7.9.0"}, status: http.StatusOK,
			wantSupport: VersionSupportUnsupported, wantBlocked: true,
		},
		{
			name: "CPA without a version header", status: http.StatusNoContent,
			wantSupport: VersionSupportUnknown, wantBlocked: true,
		},
		{
			name: "refused credential", status: http.StatusUnauthorized,
			wantErr: ErrManagementRejected,
		},
		{
			name: "CPA answered an error", status: http.StatusBadGateway,
			wantErr: ErrProofUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v0/management/config" {
					t.Fatalf("probe path = %q", r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer management-key" {
					t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
				}
				for name, value := range test.header {
					w.Header().Set(name, value)
				}
				w.WriteHeader(test.status)
			}))
			defer server.Close()

			report, err := ObserveExternalVersion(t.Context(), server.URL, "management-key")
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("ObserveExternalVersion() error = %v, want %v", err, test.wantErr)
				}
				if report != (ExternalVersionReport{}) {
					t.Fatalf("report on failure = %+v, want the zero report", report)
				}
				return
			}
			if err != nil {
				t.Fatalf("ObserveExternalVersion() error = %v", err)
			}
			if report.Support != test.wantSupport || report.WriteBlocked != test.wantBlocked {
				t.Fatalf("report = %+v, want support %q blocked %v", report, test.wantSupport, test.wantBlocked)
			}
		})
	}
}

func TestObserveExternalVersionTypesAnUnreachableCPA(t *testing.T) {
	report, err := ObserveExternalVersion(t.Context(), "http://127.0.0.1:1", "management-key")
	if !errors.Is(err, ErrProofUnavailable) {
		t.Fatalf("ObserveExternalVersion() error = %v, want ErrProofUnavailable", err)
	}
	if report != (ExternalVersionReport{}) {
		t.Fatalf("report = %+v, want the zero report", report)
	}
}
