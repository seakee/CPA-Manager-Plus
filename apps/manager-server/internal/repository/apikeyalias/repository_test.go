package apikeyalias

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func testSHA256Hex(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func TestLoadAllSupportsMinimalV1SchemaWithoutFreshKeyTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "minimal-v1.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`create table api_key_aliases (
		api_key_hash text primary key,
		alias text not null,
		updated_at_ms integer not null
	)`); err != nil {
		t.Fatalf("create minimal api_key_aliases: %v", err)
	}

	repo := New(db)
	ctx := context.Background()
	hashA := testSHA256Hex("legacy-key-a")
	hashB := testSHA256Hex("legacy-key-b")
	if err := repo.UpsertMany(ctx, []model.APIKeyAlias{
		{APIKeyHash: hashB, Alias: "Beta", UpdatedAtMS: 200},
		{APIKeyHash: hashA, Alias: "alpha", UpdatedAtMS: 100},
	}, nil, false); err != nil {
		t.Fatalf("upsert legacy aliases: %v", err)
	}

	items, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("load minimal v1 aliases: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2: %#v", len(items), items)
	}
	if items[0].APIKeyHash != hashA || items[0].Alias != "alpha" || items[0].APIKeyID != "" || items[0].MaskedPreview != "" {
		t.Fatalf("item[0] = %#v", items[0])
	}
	if items[1].APIKeyHash != hashB || items[1].Alias != "Beta" {
		t.Fatalf("item[1] = %#v", items[1])
	}
}

func TestLoadAllUnifiesActiveRotatedRevokedInstallerStagedAndLegacyKeysWithoutSecrets(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unified.sqlite")
	db, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("open migrated sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `create table if not exists fresh_client_api_key_secret_history (
		secret_hash primary key,
		api_key_id text not null references gateway_api_key_identities(id),
		secret_ciphertext text not null,
		masked_preview text not null,
		origin text not null,
		retired_at_ms integer not null
	)`); err != nil {
		t.Fatalf("ensure secret history table: %v", err)
	}

	const (
		rotatedKeyID   = "11111111111111111111111111111111"
		installerKeyID = "22222222222222222222222222222222"
		renamedInstID  = "33333333333333333333333333333333"
		revokedKeyID   = "44444444444444444444444444444444"
		stagedKeyID    = "55555555555555555555555555555555"

		secretPlainOld = "cpamp_sk_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1111"
		secretPlainNew = "cpamp_sk_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb2222"
		cipherSentinel = "ENC_SECRET_CIPHERTEXT_DO_NOT_EXPOSE"
	)

	oldRotatedHash := testSHA256Hex(secretPlainOld)
	newRotatedHash := testSHA256Hex(secretPlainNew)
	installerHash := testSHA256Hex("sk-installer-default")
	renamedInstHash := testSHA256Hex("sk-installer-renamed")
	revokedHash := testSHA256Hex("cpamp_sk_revoked")
	stagedHash := testSHA256Hex("cpamp_sk_staged_only")
	unmanagedLegacyHash := testSHA256Hex("unmanaged-legacy-key")

	// Drop partial installer unique index only inside this isolated temp DB if we test both unnamed + renamed installer rows;
	// instead keep exactly one 'installer' row first and verify renamed installer via an update step below!
	statements := []struct {
		sql  string
		args []any
	}{
		// 1. In-place rotated workspace key: has both retired old hash binding + active new hash binding.
		{
			`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms)
			 values (?, 3, 'active', 1000, 3500)`,
			[]any{rotatedKeyID},
		},
		{
			`insert into fresh_client_api_keys (request_id, api_key_id, secret_ciphertext, secret_hash, masked_preview, name, name_revision, origin)
			 values ('req-rot', ?, ?, ?, 'cpamp_sk_…2222', '  Production API  ', 2, 'workspace')`,
			[]any{rotatedKeyID, cipherSentinel + "_NEW", newRotatedHash},
		},
		{
			`insert into fresh_client_api_key_secret_history (secret_hash, api_key_id, secret_ciphertext, masked_preview, origin, retired_at_ms)
			 values (?, ?, ?, 'cpamp_sk_…1111', 'workspace', 2500)`,
			[]any{oldRotatedHash, rotatedKeyID, cipherSentinel + "_OLD"},
		},
		{
			`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, first_seen_at_ms, last_seen_at_ms, retired_at_ms)
			 values (?, 'runtime-a', ?, 1000, 2500, 2500)`,
			[]any{rotatedKeyID, oldRotatedHash},
		},
		{
			`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, first_seen_at_ms, last_seen_at_ms, retired_at_ms)
			 values (?, 'runtime-a', ?, 2500, 3500, null)`,
			[]any{rotatedKeyID, newRotatedHash},
		},

		// 2. Unnamed installer key (name == "Initial key", name_revision == 1) -> alias must normalize to "".
		{
			`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms)
			 values (?, 1, 'active', 1000, 1200)`,
			[]any{installerKeyID},
		},
		{
			`insert into fresh_client_api_keys (request_id, api_key_id, secret_ciphertext, secret_hash, masked_preview, name, name_revision, origin)
			 values ('req-inst', ?, ?, ?, 'sk-…0001', 'Initial key', 1, 'installer')`,
			[]any{installerKeyID, cipherSentinel + "_INST", installerHash},
		},
		{
			`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, first_seen_at_ms, last_seen_at_ms, retired_at_ms)
			 values (?, 'runtime-a', ?, 1000, 1200, null)`,
			[]any{installerKeyID, installerHash},
		},

		// 3. Adopted key.
		{
			`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms)
			 values (?, 1, 'active', 1000, 1800)`,
			[]any{renamedInstID},
		},
		{
			`insert into fresh_client_api_keys (request_id, api_key_id, secret_ciphertext, secret_hash, masked_preview, name, name_revision, origin)
			 values ('req-adopted', ?, ?, ?, '…7788', 'Imported CLI', 2, 'adopted')`,
			[]any{renamedInstID, cipherSentinel + "_ADOPT", renamedInstHash},
		},
		{
			`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, first_seen_at_ms, last_seen_at_ms, retired_at_ms)
			 values (?, 'runtime-a', ?, 1000, 1800, null)`,
			[]any{renamedInstID, renamedInstHash},
		},

		// 4. Revoked (superseded) key.
		{
			`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms)
			 values (?, 2, 'superseded', 900, 4000)`,
			[]any{revokedKeyID},
		},
		{
			`insert into fresh_client_api_keys (request_id, api_key_id, secret_ciphertext, secret_hash, masked_preview, name, name_revision, origin)
			 values ('req-revoked', ?, ?, ?, 'cpamp_sk_…dead', 'Deprecated CI', 2, 'workspace')`,
			[]any{revokedKeyID, cipherSentinel + "_REV", revokedHash},
		},
		{
			`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, first_seen_at_ms, last_seen_at_ms, retired_at_ms)
			 values (?, 'runtime-a', ?, 900, 4000, 4000)`,
			[]any{revokedKeyID, revokedHash},
		},

		// 5. Newly staged key without any row in gateway_api_key_source_bindings yet.
		{
			`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms)
			 values (?, 1, 'missing', 4100, 4100)`,
			[]any{stagedKeyID},
		},
		{
			`insert into fresh_client_api_keys (request_id, api_key_id, secret_ciphertext, secret_hash, masked_preview, name, name_revision, origin)
			 values ('req-staged', ?, ?, ?, 'cpamp_sk_…5555', 'Staged Pending', 1, 'workspace')`,
			[]any{stagedKeyID, cipherSentinel + "_STAGED", stagedHash},
		},

		// 6. Legacy api_key_aliases: one stale entry for a managed hash (must NOT override) + one unmanaged hash (must merge).
		{
			`insert into api_key_aliases (api_key_hash, alias, updated_at_ms) values (?, 'Stale V1 Label', 500)`,
			[]any{newRotatedHash},
		},
		{
			`insert into api_key_aliases (api_key_hash, alias, updated_at_ms) values (?, 'Unmanaged Legacy Key', 600)`,
			[]any{unmanagedLegacyHash},
		},
	}

	for _, s := range statements {
		if _, err := db.ExecContext(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("seed statement failed (%s): %v", s.sql, err)
		}
	}

	repo := New(db)
	items, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	rawJSON, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal items: %v", err)
	}
	for _, forbidden := range []string{secretPlainOld, secretPlainNew, cipherSentinel} {
		if strings.Contains(string(rawJSON), forbidden) {
			t.Fatalf("serialized directory exposed secret material %q: %s", forbidden, string(rawJSON))
		}
	}

	byHash := make(map[string]model.APIKeyAlias, len(items))
	for _, item := range items {
		if _, dup := byHash[item.APIKeyHash]; dup {
			t.Fatalf("duplicate apiKeyHash %q in result: %#v", item.APIKeyHash, items)
		}
		byHash[item.APIKeyHash] = item
	}
	if len(byHash) != 7 {
		t.Fatalf("got %d items, want 7: %#v", len(byHash), items)
	}

	// Historical rotated hash keeps old masked preview from secret_history + lifecycle "rotated" + retired_at_ms.
	if got := byHash[oldRotatedHash]; got.Alias != "Production API" ||
		got.APIKeyID != rotatedKeyID ||
		got.MaskedPreview != "cpamp_sk_…1111" ||
		got.Origin != "workspace" ||
		got.Lifecycle != "rotated" ||
		got.UpdatedAtMS != 2500 {
		t.Fatalf("old rotated hash entry = %#v", got)
	}

	// Active rotated hash has new masked preview + current key name, ignoring stale legacy alias.
	if got := byHash[newRotatedHash]; got.Alias != "Production API" ||
		got.APIKeyID != rotatedKeyID ||
		got.MaskedPreview != "cpamp_sk_…2222" ||
		got.Origin != "workspace" ||
		got.Lifecycle != "active" ||
		got.UpdatedAtMS != 3500 {
		t.Fatalf("new rotated hash entry = %#v", got)
	}

	// Unnamed installer key emits alias "" with origin "installer".
	if got := byHash[installerHash]; got.Alias != "" ||
		got.APIKeyID != installerKeyID ||
		got.MaskedPreview != "sk-…0001" ||
		got.Origin != "installer" ||
		got.Lifecycle != "active" {
		t.Fatalf("unnamed installer entry = %#v", got)
	}

	// Adopted key entry.
	if got := byHash[renamedInstHash]; got.Alias != "Imported CLI" ||
		got.APIKeyID != renamedInstID ||
		got.MaskedPreview != "…7788" ||
		got.Origin != "adopted" ||
		got.Lifecycle != "active" {
		t.Fatalf("adopted entry = %#v", got)
	}

	// Revoked key entry.
	if got := byHash[revokedHash]; got.Alias != "Deprecated CI" ||
		got.APIKeyID != revokedKeyID ||
		got.MaskedPreview != "cpamp_sk_…dead" ||
		got.Origin != "workspace" ||
		got.Lifecycle != "superseded" ||
		got.UpdatedAtMS != 4000 {
		t.Fatalf("revoked entry = %#v", got)
	}

	// Staged-only key without source binding.
	if got := byHash[stagedHash]; got.Alias != "Staged Pending" ||
		got.APIKeyID != stagedKeyID ||
		got.MaskedPreview != "cpamp_sk_…5555" ||
		got.Origin != "workspace" ||
		got.Lifecycle != "missing" {
		t.Fatalf("staged entry = %#v", got)
	}

	// Unmanaged legacy alias fallback.
	if got := byHash[unmanagedLegacyHash]; got.Alias != "Unmanaged Legacy Key" ||
		got.APIKeyID != "" ||
		got.MaskedPreview != "" ||
		got.Origin != "" ||
		got.Lifecycle != "" ||
		got.UpdatedAtMS != 600 {
		t.Fatalf("unmanaged legacy entry = %#v", got)
	}

	// Verify renamed installer key (name_revision > 1) emits its custom name.
	if _, err := db.ExecContext(ctx, `update fresh_client_api_keys set name = 'Custom Gateway Key', name_revision = 2 where api_key_id = ?`, installerKeyID); err != nil {
		t.Fatalf("rename installer key: %v", err)
	}
	afterRename, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll after rename: %v", err)
	}
	foundRenamed := false
	for _, item := range afterRename {
		if item.APIKeyHash == installerHash {
			foundRenamed = true
			if item.Alias != "Custom Gateway Key" || item.Origin != "installer" {
				t.Fatalf("renamed installer entry = %#v", item)
			}
		}
	}
	if !foundRenamed {
		t.Fatalf("installer hash missing after rename: %#v", afterRename)
	}

	// Pre-upgrade 1.x secret_history row with empty masked_preview must emit empty MaskedPreview (never copy current k.masked_preview) and lifecycle "rotated".
	preUpgradeHistHash := strings.Repeat("e", 64)
	if _, err := db.ExecContext(ctx, `insert into fresh_client_api_key_secret_history (
		secret_hash, api_key_id, secret_ciphertext, masked_preview, origin, retired_at_ms
	) values (?, ?, '', '', 'adopted', 1800)`, preUpgradeHistHash, rotatedKeyID); err != nil {
		t.Fatalf("insert pre-upgrade history: %v", err)
	}
	withPreUpgradeHist, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll with pre-upgrade history: %v", err)
	}
	var gotPreUpgrade *struct {
		Preview   string
		Lifecycle string
		Updated   int64
	}
	for _, item := range withPreUpgradeHist {
		if item.APIKeyHash == preUpgradeHistHash {
			gotPreUpgrade = &struct {
				Preview   string
				Lifecycle string
				Updated   int64
			}{item.MaskedPreview, item.Lifecycle, item.UpdatedAtMS}
		}
	}
	if gotPreUpgrade == nil || gotPreUpgrade.Preview != "" || gotPreUpgrade.Lifecycle != "rotated" || gotPreUpgrade.Updated != 1800 {
		t.Fatalf("pre-upgrade history alias entry = %+v, want empty preview + rotated + updated=1800", gotPreUpgrade)
	}

	// When canonical identity transitions to lifecycle "missing", historical rotated hashes must keep lifecycle "rotated" while current hash emits "missing".
	if _, err := db.ExecContext(ctx, `update gateway_api_key_identities set lifecycle = 'missing' where id = ?`, rotatedKeyID); err != nil {
		t.Fatalf("mark rotated identity missing: %v", err)
	}
	afterMissing, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll after missing transition: %v", err)
	}
	byMissing := make(map[string]string, len(afterMissing))
	for _, item := range afterMissing {
		byMissing[item.APIKeyHash] = item.Lifecycle
	}
	if byMissing[newRotatedHash] != "missing" {
		t.Fatalf("current hash lifecycle on missing identity = %q, want missing", byMissing[newRotatedHash])
	}
	if byMissing[oldRotatedHash] != "rotated" || byMissing[preUpgradeHistHash] != "rotated" {
		t.Fatalf("historical rotated hash lifecycles on missing identity = old:%q preUpgrade:%q, want rotated", byMissing[oldRotatedHash], byMissing[preUpgradeHistHash])
	}
}
