package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestInspectAuthFileOwnershipMutationRestoresStatusBody(t *testing.T) {
	body := `{"name":"auth-a.json","disabled":false}`
	req, err := http.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	if len(mutation.fileNames) != 1 || mutation.fileNames[0] != "auth-a.json" || mutation.clearAll {
		t.Fatalf("mutation = %#v", mutation)
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read restored body: %v", err)
	}
	if string(raw) != body {
		t.Fatalf("restored body = %q, want %q", raw, body)
	}
}

func TestInspectAuthFileOwnershipMutationReadsMultipartUpload(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "auth-a.json")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte(`{"type":"codex"}`)); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "/v0/management/auth-files", bytes.NewReader(body.Bytes()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	if len(mutation.fileNames) != 1 || mutation.fileNames[0] != "auth-a.json" {
		t.Fatalf("mutation = %#v", mutation)
	}
	restored, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read restored request: %v", err)
	}
	if !bytes.Equal(restored, body.Bytes()) {
		t.Fatal("multipart request body was not restored")
	}
}

func TestSuccessfulAuthFileOwnershipMutationKeepsOnlySuccessfulFiles(t *testing.T) {
	response := &http.Response{
		Body: io.NopCloser(strings.NewReader(`{"files":["auth-a.json"],"failed":[{"name":"auth-b.json"}]}`)),
	}
	mutation, err := successfulAuthFileOwnershipMutation(response, authFileOwnershipMutation{
		fileNames: []string{"auth-a.json", "auth-b.json"},
	})
	if err != nil {
		t.Fatalf("resolve successful mutation: %v", err)
	}
	if len(mutation.fileNames) != 1 || mutation.fileNames[0] != "auth-a.json" {
		t.Fatalf("mutation = %#v", mutation)
	}
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read restored response: %v", err)
	}
	if !bytes.Contains(raw, []byte("auth-b.json")) {
		t.Fatalf("restored response = %q", raw)
	}
}

func TestSuccessfulAuthFileOwnershipMutationDerivesClearAllPartialSuccess(t *testing.T) {
	response := &http.Response{
		Body: io.NopCloser(strings.NewReader(`{"deleted":1,"failed":[{"name":"auth-b.json"}]}`)),
	}
	mutation, err := successfulAuthFileOwnershipMutation(response, authFileOwnershipMutation{
		fileNames: []string{"auth-a.json", "auth-b.json"},
		clearAll:  true,
	})
	if err != nil {
		t.Fatalf("resolve clear-all mutation: %v", err)
	}
	if mutation.clearAll || len(mutation.fileNames) != 1 || mutation.fileNames[0] != "auth-a.json" {
		t.Fatalf("mutation = %#v", mutation)
	}
}

func TestSuccessfulAuthFileOwnershipMutationRejectsLogicalFailure(t *testing.T) {
	response := &http.Response{
		Body: io.NopCloser(strings.NewReader(`{"status":"error","deleted":0}`)),
	}
	mutation, err := successfulAuthFileOwnershipMutation(response, authFileOwnershipMutation{
		fileNames: []string{"auth-a.json"},
	})
	if err != nil {
		t.Fatalf("resolve logical failure: %v", err)
	}
	if mutation.clearAll || len(mutation.fileNames) != 0 {
		t.Fatalf("logical failure mutation = %#v, want empty", mutation)
	}
}

func TestSuccessfulAuthFileOwnershipMutationRejectsEncodedResponse(t *testing.T) {
	response := &http.Response{
		Header: http.Header{"Content-Encoding": []string{"gzip"}},
		Body:   io.NopCloser(strings.NewReader("compressed")),
	}
	if _, err := successfulAuthFileOwnershipMutation(response, authFileOwnershipMutation{
		fileNames: []string{"auth-a.json"},
	}); err == nil {
		t.Fatal("encoded response succeeded, want error")
	}
}

func TestRevokeAndRestoreInspectionOwnership(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:  "auth-a.json",
		AuthIndex: "auth-1",
	}); err != nil {
		t.Fatalf("save ownership: %v", err)
	}
	if err := st.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:  "auth-b.json",
		AuthIndex: "auth-2",
	}); err != nil {
		t.Fatalf("save second ownership: %v", err)
	}
	service := New(nil, st)
	revoked, err := service.revokeInspectionOwnership(context.Background(), authFileOwnershipMutation{fileNames: []string{"auth-a.json"}})
	if err != nil {
		t.Fatalf("revoke ownership: %v", err)
	}
	if len(revoked) != 1 || revoked[0].FileName != "auth-a.json" {
		t.Fatalf("revoked ownership = %#v", revoked)
	}
	items, err := st.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list ownership: %v", err)
	}
	if len(items) != 1 || items[0].FileName != "auth-b.json" {
		t.Fatalf("ownership = %#v, want auth-b.json", items)
	}
	if err := service.restoreInspectionOwnership(context.Background(), revoked); err != nil {
		t.Fatalf("restore ownership: %v", err)
	}
	items, err = st.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list ownership after restore: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("ownership after restore = %#v, want 2 items", items)
	}
}

func TestReadAndRestoreRequestBodyRejectsOversizedBody(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "/v0/management/auth-files", strings.NewReader("12345"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := readAndRestoreRequestBody(req, 4); !errors.Is(err, errAuthFileMutationBodyTooLarge) {
		t.Fatalf("read oversized body error = %v", err)
	}
}

func TestOwnershipItemsNotMutatedRestoresOnlyFailedFiles(t *testing.T) {
	items := []store.CodexInspectionDisableOwnership{
		{FileName: "auth-a.json"},
		{FileName: "auth-b.json"},
	}
	remaining := ownershipItemsNotMutated(items, authFileOwnershipMutation{fileNames: []string{"auth-a.json"}})
	if len(remaining) != 1 || remaining[0].FileName != "auth-b.json" {
		t.Fatalf("remaining ownership = %#v", remaining)
	}
}

func TestIsManagementPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/v0/management", want: true},
		{path: "/v0/management/", want: true},
		{path: "/v0/management/auth-files", want: true},
		{path: "/v0/management/auth-files/status", want: true},
		{path: "/v0/management/api-call", want: true},
		{path: "/v0/management/api-key-usage", want: true},
		{path: "/v0/resource/plugins", want: true},
		{path: "/v0/resource/plugins/codex-invite/invite", want: true},
		{path: "/v0/resource/plugin", want: false},
		{path: "/v0/resource/plugin-store", want: false},
		{path: "/v1/models", want: false},
		{path: "/models", want: false},
		{path: "/auth-files", want: false},
		{path: "/api-call", want: false},
		{path: "/", want: false},
		{path: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := isManagementPath(tt.path); got != tt.want {
				t.Fatalf("isManagementPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsModelListPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/v1/models", want: true},
		{path: "/v1/models/", want: true},
		{path: "/models", want: true},
		{path: "/models/", want: true},
		{path: "/v1/chat/completions", want: false},
		{path: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := isModelListPath(tt.path); got != tt.want {
				t.Fatalf("isModelListPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsCPAPluginManagementPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/v0/management/codex-invite/accounts", want: true},
		{path: "/v0/management/sample-plugin/custom/action", want: true},
		{path: "/v0/management/accounts", want: false},
		{path: "/v0/management/accounts/", want: false},
		{path: "/v0/management/config", want: false},
		{path: "/v0/management/reload", want: false},
		{path: "/v0/management/plugins/demo/custom", want: false},
		{path: "/v0/management/plugin-store/demo/install", want: false},
		{path: "/v0/management/usage", want: false},
		{path: "/v0/resource/plugins/codex-invite/invite", want: false},
		{path: "/v0/management", want: false},
		{path: "/v0/management/", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := IsCPAPluginManagementPath(tt.path); got != tt.want {
				t.Fatalf("IsCPAPluginManagementPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsCPAPluginResourcePath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/v0/resource/plugins", want: true},
		{path: "/v0/resource/plugins/", want: true},
		{path: "/v0/resource/plugins/codex-invite/invite", want: true},
		{path: "/v0/resource/plugins/codex-invite/assets/app.js", want: true},
		{path: "/v0/resource/plugin", want: false},
		{path: "/v0/resource/plugin-store", want: false},
		{path: "/plugins/codex-invite/invite", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := IsCPAPluginResourcePath(tt.path); got != tt.want {
				t.Fatalf("IsCPAPluginResourcePath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestRewriteCodexInviteOrigin(t *testing.T) {
	target, err := url.Parse("http://cpa.local:8317/base")
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	header := http.Header{}
	header.Set(codexInviteOriginHeader, "http://manager.local:18317")
	header.Set("Origin", "http://manager.local:18317")

	rewriteCodexInviteOrigin(header, target)

	if got := header.Get(codexInviteOriginHeader); got != "http://cpa.local:8317" {
		t.Fatalf("%s = %q", codexInviteOriginHeader, got)
	}
	if got := header.Get("Origin"); got != "http://manager.local:18317" {
		t.Fatalf("Origin = %q", got)
	}

	emptyHeader := http.Header{}
	rewriteCodexInviteOrigin(emptyHeader, target)
	if got := emptyHeader.Get(codexInviteOriginHeader); got != "" {
		t.Fatalf("empty %s = %q", codexInviteOriginHeader, got)
	}
}

func TestRewritePluginManagementOriginBody(t *testing.T) {
	target, err := url.Parse("http://cpa.local:8317")
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	req, err := http.NewRequest(
		http.MethodPost,
		"/v0/management/codex-invite/invite",
		strings.NewReader(`{"management_origin":"http://manager.local:18317","refresh":true}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if err := rewritePluginManagementOriginBody(req, target); err != nil {
		t.Fatalf("rewritePluginManagementOriginBody() error = %v", err)
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	want := `{"management_origin":"http://cpa.local:8317","refresh":true}`
	if string(raw) != want {
		t.Fatalf("body = %q, want %q", raw, want)
	}
	if req.ContentLength != int64(len(want)) {
		t.Fatalf("content length = %d, want %d", req.ContentLength, len(want))
	}
}

func TestRewritePluginManagementOriginBodyLeavesOtherBodies(t *testing.T) {
	target, err := url.Parse("http://cpa.local:8317")
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "/v0/resource/plugins/demo", strings.NewReader(`{"refresh":true}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if err := rewritePluginManagementOriginBody(req, target); err != nil {
		t.Fatalf("rewritePluginManagementOriginBody() error = %v", err)
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(raw) != `{"refresh":true}` {
		t.Fatalf("body = %q", raw)
	}
}
