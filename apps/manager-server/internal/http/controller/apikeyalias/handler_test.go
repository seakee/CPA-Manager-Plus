package apikeyalias

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	apikeyaliassvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/apikeyalias"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

func handlerTestHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func TestHandleGetReturnsUnifiedKeyDirectoryWithoutPlaintext(t *testing.T) {
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	ctx := t.Context()

	if _, err := st.SQLDB().ExecContext(ctx, `create table if not exists fresh_client_api_key_secret_history (
		secret_hash primary key,
		api_key_id text not null references gateway_api_key_identities(id),
		secret_ciphertext text not null,
		masked_preview text not null,
		origin text not null,
		retired_at_ms integer not null
	)`); err != nil {
		t.Fatalf("create history table: %v", err)
	}

	const (
		keyID      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		instKeyID  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		plainOld   = "cpamp_sk_0000000000000000000000000000000000000000000000000000000000001111"
		plainNew   = "cpamp_sk_0000000000000000000000000000000000000000000000000000000000002222"
		cipherText = "CIPHER_DO_NOT_LEAK"
	)
	oldHash := handlerTestHash(plainOld)
	newHash := handlerTestHash(plainNew)
	instHash := handlerTestHash("sk-initial-secret")

	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{
			`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms) values (?, 2, 'active', 100, 500)`,
			[]any{keyID},
		},
		{
			`insert into fresh_client_api_keys (request_id, api_key_id, secret_ciphertext, secret_hash, masked_preview, name, name_revision, origin)
			 values ('req-1', ?, ?, ?, 'cpamp_sk_…2222', 'Analytics Client', 2, 'workspace')`,
			[]any{keyID, cipherText, newHash},
		},
		{
			`insert into fresh_client_api_key_secret_history (secret_hash, api_key_id, secret_ciphertext, masked_preview, origin, retired_at_ms)
			 values (?, ?, ?, 'cpamp_sk_…1111', 'workspace', 300)`,
			[]any{oldHash, keyID, cipherText},
		},
		{
			`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, first_seen_at_ms, last_seen_at_ms, retired_at_ms)
			 values (?, 'rt-1', ?, 100, 300, 300), (?, 'rt-1', ?, 300, 500, null)`,
			[]any{keyID, oldHash, keyID, newHash},
		},
		{
			`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms) values (?, 1, 'active', 100, 150)`,
			[]any{instKeyID},
		},
		{
			`insert into fresh_client_api_keys (request_id, api_key_id, secret_ciphertext, secret_hash, masked_preview, name, name_revision, origin)
			 values ('req-inst', ?, ?, ?, 'sk-…4321', 'Initial key', 1, 'installer')`,
			[]any{instKeyID, cipherText, instHash},
		},
		{
			`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, first_seen_at_ms, last_seen_at_ms, retired_at_ms)
			 values (?, 'rt-1', ?, 100, 150, null)`,
			[]any{instKeyID, instHash},
		},
	} {
		if _, err := st.SQLDB().ExecContext(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed db: %v", err)
		}
	}

	handler := &Handler{App: &app.Context{
		Config:             cfg,
		AdminAuthService:   adminauthsvc.New(cfg, st),
		APIKeyAliasService: apikeyaliassvc.New(st),
	}}

	unauth := httptest.NewRecorder()
	handler.Handle(unauth, httptest.NewRequest(http.MethodGet, "/v0/management/api-key-aliases", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d, want 401", unauth.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/v0/management/api-key-aliases", nil)
	req.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
	rec := httptest.NewRecorder()
	handler.Handle(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rawBody := rec.Body.String()
	for _, secret := range []string{plainOld, plainNew, cipherText, `"Initial key"`} {
		if strings.Contains(rawBody, secret) {
			t.Fatalf("response body leaked %q: %s", secret, rawBody)
		}
	}

	var payload struct {
		Items []model.APIKeyAlias `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Items) != 3 {
		t.Fatalf("items len = %d, want 3: %#v", len(payload.Items), payload.Items)
	}
	byHash := map[string]model.APIKeyAlias{}
	for _, item := range payload.Items {
		byHash[item.APIKeyHash] = item
	}
	if got := byHash[instHash]; got.Alias != "" || got.Origin != "installer" || got.MaskedPreview != "sk-…4321" || got.APIKeyID != instKeyID {
		t.Fatalf("installer entry = %#v", got)
	}
	if got := byHash[oldHash]; got.Alias != "Analytics Client" || got.MaskedPreview != "cpamp_sk_…1111" || got.APIKeyID != keyID || got.Lifecycle != "rotated" || got.UpdatedAtMS != 300 {
		t.Fatalf("old hash entry = %#v", got)
	}
	if got := byHash[newHash]; got.Alias != "Analytics Client" || got.MaskedPreview != "cpamp_sk_…2222" || got.APIKeyID != keyID || got.Lifecycle != "active" {
		t.Fatalf("new hash entry = %#v", got)
	}
}
