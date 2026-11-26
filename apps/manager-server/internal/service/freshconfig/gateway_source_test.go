package freshconfig

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

func TestSourceModeStripsSecretsAndControlPlane(t *testing.T) {
	ctx := context.Background()
	doc := map[string]any{}
	if err := json.Unmarshal([]byte(`{
		"config-version": 2,
		"management": {"secret-key": "super-secret-mgmt"},
		"access": {"api-keys": [{"key": "sk-client-secret"}]},
		"api-keys": {"openai": [{"api-key": "sk-upstream-secret"}]},
		"plugins": {"enabled": true},
		"usage-statistics-enabled": true,
		"logging": {"level": "debug"},
		"server": {"host": "0.0.0.0", "port": 8317, "max-request-body-bytes": 1048576},
		"oauth": {"auth-dir": "/data/auth", "auth-auto-refresh-workers": 4},
		"routing": {"strategy": "round-robin"},
		"experimental-v9": {"new-flag": true, "nested": {"threshold": 42}}
	}`), &doc); err != nil {
		t.Fatal(err)
	}
	wire := &fakeWire{document: doc}
	svc, _ := managedService(t, wire)

	view, err := svc.ReadSource(ctx)
	if err != nil {
		t.Fatalf("ReadSource: %v", err)
	}
	if !view.SourceWritable {
		t.Fatalf("expected managed sourceWritable, got reason=%q", view.Reason)
	}
	for _, forbidden := range []string{
		"super-secret-mgmt",
		"sk-client-secret",
		"sk-upstream-secret",
		"management:",
		"access:",
		"api-keys:",
		"plugins:",
		"logging:",
		"port:",
		"auth-dir:",
	} {
		if strings.Contains(view.YAML, forbidden) {
			t.Fatalf("ReadSource leaked protected content %q:\n%s", forbidden, view.YAML)
		}
	}
	for _, expected := range []string{
		"max-request-body-bytes: 1048576",
		"auth-auto-refresh-workers: 4",
		"strategy: round-robin",
		"experimental-v9:",
		"new-flag: true",
		"threshold: 42",
	} {
		if !strings.Contains(view.YAML, expected) {
			t.Fatalf("ReadSource missing expected gateway/unknown field %q:\n%s", expected, view.YAML)
		}
	}
}

func TestSourceModeAllowsUnadaptedUpstreamCPAFields(t *testing.T) {
	ctx := context.Background()
	wire := &fakeWire{
		document: map[string]any{
			"server":  map[string]any{"port": json.Number("8317"), "max-request-body-bytes": json.Number("1048576")},
			"routing": map[string]any{"strategy": "round-robin"},
		},
	}
	svc, _ := managedService(t, wire)
	view, err := svc.ReadSource(ctx)
	if err != nil {
		t.Fatalf("ReadSource: %v", err)
	}

	edited := `experimental-v9:
  mode: turbo
  tags:
    - alpha
routing:
  future-flag: true
  strategy: fill-first
server:
  max-request-body-bytes: 1048576
`
	reqID := strings.Repeat("1a", 16)
	op, err := svc.SaveSource(ctx, SourceRequest{
		RequestID: reqID,
		Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
		YAML:      edited,
	})
	if err != nil {
		t.Fatalf("SaveSource: %v", err)
	}
	if op.State != "persisted" {
		t.Fatalf("expected persisted operation, got %+v", op)
	}
	serverMap, _ := wire.document["server"].(map[string]any)
	if serverMap["port"] == nil {
		t.Fatalf("expected protected server.port to remain untouched, got %v", wire.document["server"])
	}
	routingMap, _ := wire.document["routing"].(map[string]any)
	if routingMap["future-flag"] != true || routingMap["strategy"] != "fill-first" {
		t.Fatalf("expected unadapted routing.future-flag and strategy update, got %v", routingMap)
	}
	expMap, _ := wire.document["experimental-v9"].(map[string]any)
	if expMap["mode"] != "turbo" {
		t.Fatalf("expected experimental-v9 to be persisted, got %v", expMap)
	}

	// Idempotent replay of the exact same requestId returns persisted op without extra writes.
	patchesBefore := len(wire.patches)
	replayed, err := svc.SaveSource(ctx, SourceRequest{
		RequestID: reqID,
		Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
		YAML:      edited,
	})
	if err != nil || replayed.RequestID != reqID || len(wire.patches) != patchesBefore {
		t.Fatalf("expected idempotent replay, err=%v patchesBefore=%d after=%d", err, patchesBefore, len(wire.patches))
	}
}

func TestSourceModeGuardsProtectedAndExternalAndPrivate(t *testing.T) {
	ctx := context.Background()

	// 1. Managed rejects protected path writes, proxy credentials, and invalid YAML.
	wire := &fakeWire{document: map[string]any{"routing": map[string]any{"strategy": "round-robin"}}}
	svc, _ := managedService(t, wire)
	view, err := svc.ReadSource(ctx)
	if err != nil {
		t.Fatalf("ReadSource: %v", err)
	}
	reqID := strings.Repeat("2b", 16)
	_, err = svc.SaveSource(ctx, SourceRequest{
		RequestID: reqID,
		Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
		YAML:      "management:\n  secret-key: hacked\nrouting:\n  strategy: round-robin\n",
	})
	var rej Rejection
	if !errors.As(err, &rej) || rej.Code != CodeSourceProtectedPath {
		t.Fatalf("expected CodeSourceProtectedPath, got %v", err)
	}

	_, err = svc.SaveSource(ctx, SourceRequest{
		RequestID: reqID,
		Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
		YAML:      "requests:\n  proxy-url: http://user:pass@proxy.test:8080\nrouting:\n  strategy: round-robin\n",
	})
	if !errors.As(err, &rej) || rej.Code != CodeProxyURLCredentials {
		t.Fatalf("expected CodeProxyURLCredentials, got %v", err)
	}

	_, err = svc.SaveSource(ctx, SourceRequest{
		RequestID: reqID,
		Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
		YAML:      "routing:\n  strategy: [unclosed\n",
	})
	if !errors.As(err, &rej) || rej.Code != CodeSourceInvalidYAML {
		t.Fatalf("expected CodeSourceInvalidYAML, got %v", err)
	}

	// 2. External mode permits unadapted scalar/object writes, rejects payload rules writes.
	extStore := &memoryStore{record: managedRecord()}
	extStore.record.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
		Strategy: bootstrap.ConnectExternal, Stage: "committed", TargetIdentity: "external:sha256:" + strings.Repeat("b", 64), TargetGeneration: 1,
	}
	extStore.record.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: "https://external.test", ManagementKey: "cpamp_protected_envelope"}
	extWire := &fakeWire{document: map[string]any{
		"routing":  map[string]any{"strategy": "round-robin"},
		"requests": map[string]any{"payload": map[string]any{"default": []any{}}},
	}}
	extSvc := New(extStore, nil, externalTargets())
	extSvc.EnableExternal(func(context.Context, uint64) (string, string, error) {
		return "https://external.test", "management-test-only", nil
	})
	extSvc.externalWire = func(string) configWire { return extWire }
	extSvc.externalProbe = func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
		return cpa.ClassifyExternalVersion("v8.0.64"), nil
	}

	extView, err := extSvc.ReadSource(ctx)
	if err != nil {
		t.Fatalf("External ReadSource: %v", err)
	}
	if !extView.SourceWritable {
		t.Fatalf("expected External sourceWritable=true, got reason=%q", extView.Reason)
	}
	_, err = extSvc.SaveSource(ctx, SourceRequest{
		RequestID: strings.Repeat("3c", 16),
		Expected:  Expected{Generation: extView.Generation, Baseline: extView.Baseline},
		YAML:      "requests:\n  payload:\n    default:\n      - models:\n          - name: gpt-*\n        params:\n          temperature: 0.5\nrouting:\n  strategy: round-robin\n",
	})
	if !errors.Is(err, ErrRulesReadOnly) {
		t.Fatalf("expected ErrRulesReadOnly on External payload rules edit, got %v", err)
	}
	_, err = extSvc.SaveSource(ctx, SourceRequest{
		RequestID: strings.Repeat("4d", 16),
		Expected:  Expected{Generation: extView.Generation, Baseline: extView.Baseline},
		YAML:      "requests:\n  payload:\n    default: []\nrouting:\n  new-ext-flag: 7\n  strategy: round-robin\n",
	})
	if err != nil {
		t.Fatalf("External unadapted field save failed: %v", err)
	}
	if extWire.document["routing"].(map[string]any)["new-ext-flag"] == nil {
		t.Fatalf("expected External unadapted field to persist")
	}

	// 3. Private mode is strictly read-only for source editor.
	privRec := managedRecord()
	privRec.Operation.Strategy.TargetIdentity = cpa.PrivateKeyRuntime
	privRec.Operation.Strategy.TargetArtifactID = cpa.PrivateKeyArtifact
	privRec.Operation.Strategy.DestinationConnectionIdentity = ""
	privStore := &memoryStore{record: privRec}
	privSvc := New(privStore, func(context.Context, uint64) (string, error) { return "secret", nil }, privateTargets)
	privSvc.wire = &fakeWire{document: map[string]any{"routing": map[string]any{"strategy": "round-robin"}}}
	privView, err := privSvc.ReadSource(ctx)
	if err != nil {
		t.Fatalf("Private ReadSource: %v", err)
	}
	if privView.SourceWritable || privView.Reason != ReasonPrivateFrozen {
		t.Fatalf("expected Private read-only source view, got %+v", privView)
	}
	_, err = privSvc.SaveSource(ctx, SourceRequest{
		RequestID: strings.Repeat("5e", 16),
		Expected:  Expected{Generation: privView.Generation, Baseline: privView.Baseline},
		YAML:      "routing:\n  strategy: fill-first\n",
	})
	if !errors.Is(err, ErrReadOnly) {
		t.Fatalf("expected ErrReadOnly on Private SaveSource, got %v", err)
	}
}

func TestSourceModeEnforcesCPAV8ProtectedRootsAndServerSubtrees(t *testing.T) {
	ctx := context.Background()
	wire := &fakeWire{
		document: map[string]any{
			"config-version": json.Number("8"),
			"management":     map[string]any{"secret-key": "mgmt-secret"},
			"access":         map[string]any{"api-keys": []any{"client-key"}},
			"api-keys":       map[string]any{"codex": []any{map[string]any{"api-key": "sk-1"}}},
			"plugins":        map[string]any{"enabled": true, "dir": "/var/lib/cpa/plugins"},
			"observability": map[string]any{
				"usage": map[string]any{"usage-statistics-enabled": true},
				"logs":  map[string]any{"logging-to-file": true},
				"pprof": map[string]any{"enable": false},
			},
			"models": map[string]any{
				"catalog": map[string]any{"url": "https://internal.example/models.json"},
			},
			"credentials": map[string]any{
				"concurrency": map[string]any{"lifecycle-config-revision": "rev-1"},
			},
			"server": map[string]any{
				"host":                   "127.0.0.1",
				"port":                   json.Number("8317"),
				"server-id":              "65788ebc-f409-4e5c-be25-e02a43497741",
				"discovery":              map[string]any{"enabled": true},
				"tls":                    map[string]any{"enable": true},
				"unix-socket":            "/run/cpa.sock",
				"github-token":           "ghp-secret-token",
				"kv-store":               map[string]any{"path": "/var/lib/cpa/kv.db"},
				"max-request-body-bytes": json.Number("2097152"),
			},
			"routing": map[string]any{"strategy": "round-robin"},
		},
	}
	service, _ := managedService(t, wire)

	view, err := service.ReadSource(ctx)
	if err != nil {
		t.Fatalf("ReadSource: %v", err)
	}
	for _, forbidden := range []string{
		"observability", "usage-statistics-enabled", "pprof",
		"models", "internal.example",
		"credentials", "lifecycle-config-revision",
		"github-token", "ghp-secret-token",
		"kv-store", "/var/lib/cpa/kv.db",
		"unix-socket", "discovery", "tls", "127.0.0.1", "8317",
		"server-id", "65788ebc-f409-4e5c-be25-e02a43497741",
	} {
		if strings.Contains(view.YAML, forbidden) {
			t.Fatalf("ReadSource leaked protected CPA v8 token %q in YAML:\n%s", forbidden, view.YAML)
		}
	}
	if !strings.Contains(view.YAML, "max-request-body-bytes: 2097152") {
		t.Fatalf("expected non-protected server leaf preserved, got:\n%s", view.YAML)
	}

	blockedSubmissions := []string{
		"observability:\n  usage:\n    usage-statistics-enabled: false\n",
		"models:\n  catalog:\n    url: file:///etc/hosts\n",
		"credentials:\n  concurrency:\n    global-max-in-flight: 4\n",
		"server:\n  github-token: ghp_new\n",
		"server:\n  kv-store:\n    path: /tmp/other.db\n",
		"server:\n  host: 0.0.0.0\n",
		"server:\n  port: 9000\n",
		"server:\n  discovery:\n    enabled: true\n",
		"server:\n  tls:\n    enable: true\n",
		"server:\n  unix-socket: /tmp/cpa.sock\n",
		"server:\n  server-id: 65788ebc-f409-4e5c-be25-e02a43497741\n",
	}
	for i, rawYAML := range blockedSubmissions {
		req := SourceRequest{
			RequestID: strings.Repeat("9a", 15) + string([]byte{'0' + byte(i%10), 'a'}),
			Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
			YAML:      rawYAML,
		}
		err := ValidateSourceRequest(req)
		var rej Rejection
		if !errors.As(err, &rej) || rej.Code != CodeSourceProtectedPath {
			t.Fatalf("case %d (%q): expected %q, got %v", i, rawYAML, CodeSourceProtectedPath, err)
		}
	}
}

func TestSourceModeKVMapRoundTripsSensitiveKeyNamesDotsAndAtomicDiff(t *testing.T) {
	ctx := context.Background()
	wire := &fakeWire{
		document: map[string]any{
			"oauth": map[string]any{
				"auth-dir": "/data/auth",
				"providers": map[string]any{
					"antigravity": map[string]any{
						"sensitive-words-replace": map[string]any{
							"secret":  "[redacted]",
							"api-key": "[masked]",
							"You are Codex, an agent based on GPT-5.": "You are an assistant.",
							"path/with?chars#frag%20":                 "sanitized",
						},
					},
					"devin": map[string]any{
						"sensitive-words":         []any{"keep"},
						"sensitive-words-replace": map[string]any{},
					},
				},
			},
			"routing": map[string]any{"strategy": "round-robin"},
		},
	}
	service, _ := managedService(t, wire)

	view, err := service.ReadSource(ctx)
	if err != nil {
		t.Fatalf("ReadSource: %v", err)
	}
	roundTripDoc, err := parseStrictSourceYAML(view.YAML)
	if err != nil {
		t.Fatalf("parseStrictSourceYAML(ReadSource YAML): %v\nYAML:\n%s", err, view.YAML)
	}
	rtAntigravity, rtAGPresence := cpa.LookupConfigValue(roundTripDoc, "oauth.providers.antigravity.sensitive-words-replace")
	rtAGMap, ok := rtAntigravity.(map[string]any)
	if rtAGPresence != cpa.ConfigPresent || !ok || len(rtAGMap) != 4 ||
		rtAGMap["secret"] != "[redacted]" ||
		rtAGMap["api-key"] != "[masked]" ||
		rtAGMap["You are Codex, an agent based on GPT-5."] != "You are an assistant." ||
		rtAGMap["path/with?chars#frag%20"] != "sanitized" {
		t.Fatalf("ReadSource failed to preserve KVMap entries: %#v\nYAML:\n%s", rtAntigravity, view.YAML)
	}
	rtDevin, rtDevinPresence := cpa.LookupConfigValue(roundTripDoc, "oauth.providers.devin.sensitive-words-replace")
	rtDevinMap, ok := rtDevin.(map[string]any)
	if rtDevinPresence != cpa.ConfigPresent || !ok || len(rtDevinMap) != 0 {
		t.Fatalf("ReadSource failed to preserve empty KVMap leaf: %#v\nYAML:\n%s", rtDevin, view.YAML)
	}

	// Modify antigravity sensitive-words-replace (editing secret, adding password,
	// deleting the URL-special key) and remove devin sensitive-words-replace.
	nextYAML := "" +
		"oauth:\n" +
		"  providers:\n" +
		"    antigravity:\n" +
		"      sensitive-words-replace:\n" +
		"        \"You are Codex, an agent based on GPT-5.\": \"You are an assistant.\"\n" +
		"        api-key: \"[masked]\"\n" +
		"        password: \"***\"\n" +
		"        secret: \"[hidden]\"\n" +
		"    devin:\n" +
		"      sensitive-words:\n" +
		"        - keep\n" +
		"routing:\n" +
		"  strategy: round-robin\n"

	op, err := service.SaveSource(ctx, SourceRequest{
		RequestID: strings.Repeat("ab", 16),
		Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
		YAML:      nextYAML,
	})
	if err != nil {
		t.Fatalf("SaveSource KVMap update: %v", err)
	}
	if op.State != "persisted" {
		t.Fatalf("expected persisted operation, got %+v", op)
	}
	if len(wire.patches) != 0 {
		t.Fatalf("expected no PATCH calls for KVMap-only diff, got %+v", wire.patches)
	}
	if len(wire.puts) != 1 || wire.puts[0].path != "oauth.providers.antigravity.sensitive-words-replace" {
		t.Fatalf("expected single atomic PUT on antigravity KVMap leaf, got %+v", wire.puts)
	}
	if len(wire.deletes) != 1 || wire.deletes[0] != "oauth.providers.devin.sensitive-words-replace" {
		t.Fatalf("expected single atomic DELETE on devin KVMap leaf, got %+v", wire.deletes)
	}

	gotAntigravity, _ := cpa.LookupConfigValue(wire.document, "oauth.providers.antigravity.sensitive-words-replace")
	agMap, ok := gotAntigravity.(map[string]any)
	if !ok || len(agMap) != 4 || agMap["secret"] != "[hidden]" || agMap["password"] != "***" {
		t.Fatalf("unexpected antigravity KVMap after atomic PUT: %#v", gotAntigravity)
	}
	if _, stillPresent := agMap["path/with?chars#frag%20"]; stillPresent {
		t.Fatalf("deleted KVMap key remained after SaveSource: %#v", agMap)
	}

	// Verify unknown SaveSource operation touching a KVMap leaf reconciles cleanly via read-back.
	reconcileView, err := service.ReadSource(ctx)
	if err != nil {
		t.Fatalf("ReadSource before reconcile check: %v", err)
	}
	wire.putErr = cpa.ErrProofUnavailable
	reconcileReqID := strings.Repeat("ae", 16)
	reconcileYAML := "" +
		"oauth:\n" +
		"  providers:\n" +
		"    antigravity:\n" +
		"      sensitive-words-replace:\n" +
		"        secret: \"[reconciled]\"\n" +
		"    devin:\n" +
		"      sensitive-words:\n" +
		"        - keep\n" +
		"routing:\n" +
		"  strategy: round-robin\n"
	_, err = service.SaveSource(ctx, SourceRequest{
		RequestID: reconcileReqID,
		Expected:  Expected{Generation: reconcileView.Generation, Baseline: reconcileView.Baseline},
		YAML:      reconcileYAML,
	})
	if err == nil {
		t.Fatal("expected SaveSource PUT failure")
	}
	wire.putErr = nil
	// Simulate the PUT having landed in CPA before connection dropped.
	setDocumentValue(wire.document, strings.Split("oauth.providers.antigravity.sensitive-words-replace", "."), map[string]any{"secret": "[reconciled]"})
	reconciledOp, err := service.Operation(ctx, reconcileReqID, true)
	if err != nil || reconciledOp.State != "persisted" {
		t.Fatalf("expected KVMap source operation to reconcile to persisted, got (%+v, %v)", reconciledOp, err)
	}

	// Structural keys outside data maps must still reject dots, path separators,
	// and sensitive key names.
	for _, badStructuralYAML := range []string{
		"routing:\n  \"strategy.v2\": round-robin\n",
		"routing:\n  \"sub/path\": 1\n",
		"routing:\n  \"sub?query\": 1\n",
		"routing:\n  \"sub#frag\": 1\n",
		"routing:\n  \"sub%2f\": 1\n",
	} {
		err := ValidateSourceRequest(SourceRequest{
			RequestID: strings.Repeat("ac", 16),
			Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
			YAML:      badStructuralYAML,
		})
		var rej Rejection
		if !errors.As(err, &rej) || rej.Code != CodeSourceInvalidYAML {
			t.Fatalf("expected %q for structural YAML %q, got %v", CodeSourceInvalidYAML, badStructuralYAML, err)
		}
	}
	err = ValidateSourceRequest(SourceRequest{
		RequestID: strings.Repeat("ad", 16),
		Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
		YAML:      "routing:\n  secret: hidden\n",
	})
	var protRej Rejection
	if !errors.As(err, &protRej) || protRej.Code != CodeSourceProtectedPath {
		t.Fatalf("expected %q for structural sensitive key outside KVMap, got %v", CodeSourceProtectedPath, err)
	}
}

func TestSourceModeAllowsDottedDataMapKeysAndValidatesRuleFamiliesPreWrite(t *testing.T) {
	ctx := context.Background()
	wire := &fakeWire{
		document: map[string]any{
			"routing": map[string]any{"strategy": "round-robin"},
		},
	}
	service, _ := managedService(t, wire)

	view, err := service.ReadSource(ctx)
	if err != nil {
		t.Fatalf("ReadSource: %v", err)
	}

	validRulesYAML := "" +
		"requests:\n" +
		"  payload:\n" +
		"    default:\n" +
		"      - models:\n" +
		"          - name: gpt-*\n" +
		"            headers:\n" +
		"              \"X-Client.Version\": \"2.1\"\n" +
		"            match:\n" +
		"              - \"metadata.client.id\":\n" +
		"                  \"nested.attr\": \"cli\"\n" +
		"            not-match:\n" +
		"              - \"metadata.env.tier\": \"staging\"\n" +
		"        params:\n" +
		"          \"reasoning.effort\": high\n" +
		"          \"extra.schema\":\n" +
		"            \"field.name\": \"id\"\n" +
		"    default-raw:\n" +
		"      - models:\n" +
		"          - name: claude-*\n" +
		"        params:\n" +
		"          \"thinking.config\": '{\"budget_tokens\":2048}'\n" +
		"    replace:\n" +
		"      - models:\n" +
		"          - name: \"*\"\n" +
		"        target:\n" +
		"          role: user\n" +
		"          all: false\n" +
		"          positions: [1, 2]\n" +
		"        replacements:\n" +
		"          - \"You are GPT-5.\": \"You are an assistant.\"\n" +
		"          - \" \": \"\"\n" +
		"routing:\n" +
		"  strategy: round-robin\n"

	op, err := service.SaveSource(ctx, SourceRequest{
		RequestID: strings.Repeat("b1", 16),
		Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
		YAML:      validRulesYAML,
	})
	if err != nil || op.State != "persisted" {
		t.Fatalf("SaveSource valid dotted rules = (%+v, %v)", op, err)
	}

	wire.patches, wire.puts, wire.deletes = nil, nil, nil
	afterView, err := service.ReadSource(ctx)
	if err != nil {
		t.Fatalf("ReadSource after valid rules: %v", err)
	}

	invalidSubmissions := map[string]struct {
		yaml      string
		wantField string
	}{
		"replace wrong role": {
			yaml:      "requests:\n  payload:\n    replace:\n      - models:\n          - name: \"*\"\n        target:\n          role: assistant\n          all: true\n        replacements:\n          - a: b\n",
			wantField: "requests.payload.replace",
		},
		"replace all false without positions": {
			yaml:      "requests:\n  payload:\n    replace:\n      - models:\n          - name: \"*\"\n        target:\n          role: user\n          all: false\n        replacements:\n          - a: b\n",
			wantField: "requests.payload.replace",
		},
		"default-raw invalid json literal": {
			yaml:      "requests:\n  payload:\n    default-raw:\n      - models:\n          - name: \"*\"\n        params:\n          \"thinking.config\": \"{unclosed\"\n",
			wantField: "requests.payload.default-raw",
		},
		"override-raw non-string param": {
			yaml:      "requests:\n  payload:\n    override-raw:\n      - models:\n          - name: \"*\"\n        params:\n          \"thinking.config\":\n            nested: 1\n",
			wantField: "requests.payload.override-raw",
		},
		"payload not a mapping": {
			yaml:      "requests:\n  payload: []\n",
			wantField: "requests.payload",
		},
		"kvmap whitespace-only key": {
			yaml:      "oauth:\n  providers:\n    antigravity:\n      sensitive-words-replace:\n        \"   \": \"x\"\n",
			wantField: "oauth.providers.antigravity.sensitive-words-replace",
		},
		"kvmap non-string value": {
			yaml:      "oauth:\n  providers:\n    devin:\n      sensitive-words-replace:\n        secret: 42\n",
			wantField: "oauth.providers.devin.sensitive-words-replace",
		},
	}
	for name, tc := range invalidSubmissions {
		_, err := service.SaveSource(ctx, SourceRequest{
			RequestID: strings.Repeat("b2", 16),
			Expected:  Expected{Generation: afterView.Generation, Baseline: afterView.Baseline},
			YAML:      tc.yaml,
		})
		var rej Rejection
		if !errors.As(err, &rej) || rej.Code != CodeSourceInvalidYAML || rej.Field != tc.wantField {
			t.Fatalf("%s: expected Rejection(%q, field=%q), got %v", name, CodeSourceInvalidYAML, tc.wantField, err)
		}
	}
	if len(wire.patches) != 0 || len(wire.puts) != 0 || len(wire.deletes) != 0 {
		t.Fatalf("expected zero CPA writes on invalid pre-write rule rejections, got patches=%+v puts=%+v deletes=%+v", wire.patches, wire.puts, wire.deletes)
	}
}

func TestSourceModeH3ProtectedAndPayloadNullDeleteGuards(t *testing.T) {
	ctx := context.Background()

	t.Run("external deleting or nulling requests with payload rules returns ErrRulesReadOnly", func(t *testing.T) {
		extStore := &memoryStore{record: managedRecord()}
		extStore.record.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.ConnectExternal, Stage: "committed", TargetIdentity: "external:sha256:" + strings.Repeat("b", 64), TargetGeneration: 1,
		}
		extStore.record.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: "https://external.test", ManagementKey: "cpamp_protected_envelope"}
		extWire := &fakeWire{document: map[string]any{
			"routing":  map[string]any{"strategy": "round-robin"},
			"requests": map[string]any{"proxy-url": "http://proxy.test:8080", "payload": map[string]any{"default": []any{}}},
		}}
		extSvc := New(extStore, nil, externalTargets())
		extSvc.EnableExternal(func(context.Context, uint64) (string, string, error) {
			return "https://external.test", "management-test-only", nil
		})
		extSvc.externalWire = func(string) configWire { return extWire }
		extSvc.externalProbe = func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
			return cpa.ClassifyExternalVersion("v8.0.64"), nil
		}

		view, err := extSvc.ReadSource(ctx)
		if err != nil {
			t.Fatalf("ReadSource: %v", err)
		}
		for i, rawYAML := range []string{
			"routing:\n  strategy: round-robin\n",
			"requests: null\nrouting:\n  strategy: round-robin\n",
		} {
			_, err := extSvc.SaveSource(ctx, SourceRequest{
				RequestID: strings.Repeat("e", 31) + string([]byte{'1' + byte(i)}),
				Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
				YAML:      rawYAML,
			})
			if !errors.Is(err, ErrRulesReadOnly) {
				t.Fatalf("case %d (%q): expected ErrRulesReadOnly, got %v", i, rawYAML, err)
			}
		}
		if len(extWire.deletes) != 0 || len(extWire.puts) != 0 || len(extWire.patches) != 0 {
			t.Fatalf("expected zero CPA writes on External payload rule delete/null, got deletes=%v puts=%v patches=%v", extWire.deletes, extWire.puts, extWire.patches)
		}
	})

	t.Run("server non-map, null, empty, and stripped client-secret mapping preserve protected keys", func(t *testing.T) {
		wire := &fakeWire{document: map[string]any{
			"server": map[string]any{"port": json.Number("8317"), "host": "127.0.0.1"},
			"oauth": map[string]any{
				"auth-dir": "/data/auth",
				"providers": map[string]any{
					"custom": map[string]any{
						"client-secret": "top-secret",
						"enabled":       true,
					},
					"hidden-only": map[string]any{
						"client-secret": "only-secret",
					},
				},
			},
			"routing": map[string]any{"strategy": "round-robin"},
		}}
		svc, _ := managedService(t, wire)
		view, err := svc.ReadSource(ctx)
		if err != nil {
			t.Fatalf("ReadSource: %v", err)
		}

		for i, badYAML := range []string{
			"server: disabled\nrouting:\n  strategy: round-robin\n",
			"server: null\nrouting:\n  strategy: round-robin\n",
			"server: {}\nrouting:\n  strategy: round-robin\n",
			"oauth: null\nrouting:\n  strategy: round-robin\n",
			"oauth:\n  providers:\n    custom: disabled\nrouting:\n  strategy: round-robin\n",
			"oauth:\n  providers:\n    custom: null\nrouting:\n  strategy: round-robin\n",
			"oauth:\n  providers:\n    hidden-only: {}\nrouting:\n  strategy: round-robin\n",
		} {
			_, err := svc.SaveSource(ctx, SourceRequest{
				RequestID: strings.Repeat("f", 31) + string([]byte{'0' + byte(i)}),
				Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
				YAML:      badYAML,
			})
			var rej Rejection
			if !errors.As(err, &rej) {
				t.Fatalf("case %d (%q): expected Rejection, got %v", i, badYAML, err)
			}
		}
		if len(wire.deletes) != 0 || len(wire.puts) != 0 || len(wire.patches) != 0 {
			t.Fatalf("expected zero CPA writes on blocked overwrites, got deletes=%v puts=%v patches=%v", wire.deletes, wire.puts, wire.patches)
		}

		// Omitting oauth.providers.custom expands to deleting only visible child "enabled",
		// preserving both oauth.providers.custom.client-secret and server.port.
		op, err := svc.SaveSource(ctx, SourceRequest{
			RequestID: strings.Repeat("d", 32),
			Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
			YAML:      "routing:\n  strategy: round-robin\n",
		})
		if err != nil || op.State != "persisted" {
			t.Fatalf("expected child-expanded delete to persist, got (%+v, %v)", op, err)
		}
		for _, delPath := range wire.deletes {
			if delPath == "oauth" || delPath == "oauth.providers" || delPath == "oauth.providers.custom" {
				t.Fatalf("must not emit whole-node DELETE %q; deletes=%v", delPath, wire.deletes)
			}
		}
		srv := wire.document["server"].(map[string]any)
		if srv["port"] == nil {
			t.Fatalf("server.port was wiped: %v", srv)
		}
		custom := wire.document["oauth"].(map[string]any)["providers"].(map[string]any)["custom"].(map[string]any)
		if custom["client-secret"] != "top-secret" {
			t.Fatalf("custom.client-secret was wiped: %v", custom)
		}
	})
}

func TestSourceModeM4LeafSchemaSlicesAndLegacyProxyCredentials(t *testing.T) {
	ctx := context.Background()

	t.Run("validates all GatewayPageLeafPaths schema types pre-write", func(t *testing.T) {
		wire := &fakeWire{document: map[string]any{"routing": map[string]any{"strategy": "round-robin"}}}
		svc, _ := managedService(t, wire)
		view, err := svc.ReadSource(ctx)
		if err != nil {
			t.Fatalf("ReadSource: %v", err)
		}
		badLeaves := []struct {
			yaml      string
			wantField string
		}{
			{"routing:\n  strategy: 123\n", "routing.strategy"},
			{"routing:\n  retry:\n    request-retry: \"three\"\n", "routing.retry.request-retry"},
			{"routing:\n  retry:\n    request-retry: 3.5\n", "routing.retry.request-retry"},
			{"multimedia:\n  disable-image-generation: \"bogus\"\n", "multimedia.disable-image-generation"},
			{"oauth:\n  providers:\n    devin:\n      sensitive-words: \"not-a-list\"\n", "oauth.providers.devin.sensitive-words"},
		}
		for i, tc := range badLeaves {
			_, err := svc.SaveSource(ctx, SourceRequest{
				RequestID: strings.Repeat("c", 31) + string([]byte{'0' + byte(i)}),
				Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
				YAML:      tc.yaml,
			})
			var rej Rejection
			if !errors.As(err, &rej) || rej.Code != CodeSourceInvalidYAML || rej.Field != tc.wantField {
				t.Fatalf("%q: want Rejection(%q, %q), got %v", tc.yaml, CodeSourceInvalidYAML, tc.wantField, err)
			}
		}
		if len(wire.patches) != 0 || len(wire.puts) != 0 || len(wire.deletes) != 0 {
			t.Fatalf("unexpected writes on invalid leaf types: %+v %+v %+v", wire.patches, wire.puts, wire.deletes)
		}
	})

	t.Run("strips and protects sensitive keys nested inside slices and redacts legacy proxy credentials", func(t *testing.T) {
		wire := &fakeWire{document: map[string]any{
			"routing": map[string]any{"strategy": "round-robin"},
			"requests": map[string]any{
				"proxy-url":           "http://alice:s3cr3t@proxy.example.com:8080",
				"passthrough-headers": true,
			},
			"experimental-list": []any{
				map[string]any{"name": "entry1", "client-secret": "slice-secret"},
			},
		}}
		svc, _ := managedService(t, wire)
		view, err := svc.ReadSource(ctx)
		if err != nil {
			t.Fatalf("ReadSource: %v", err)
		}
		for _, leaked := range []string{"slice-secret", "client-secret", "alice", "s3cr3t"} {
			if strings.Contains(view.YAML, leaked) {
				t.Fatalf("ReadSource leaked %q:\n%s", leaked, view.YAML)
			}
		}

		// Submitting sensitive key inside a slice is rejected.
		_, err = svc.SaveSource(ctx, SourceRequest{
			RequestID: strings.Repeat("a", 32),
			Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
			YAML:      "experimental-list:\n  - name: entry1\n    client-secret: hacked\nrouting:\n  strategy: round-robin\n",
		})
		var rej Rejection
		if !errors.As(err, &rej) || rej.Code != CodeSourceProtectedPath {
			t.Fatalf("expected CodeSourceProtectedPath for slice sensitive key, got %v", err)
		}

		// Unrelated edit succeeds and preserves legacy credentialed requests.proxy-url and slice client-secret.
		editedYAML := strings.Replace(view.YAML, "strategy: round-robin", "strategy: fill-first", 1)
		op, err := svc.SaveSource(ctx, SourceRequest{
			RequestID: strings.Repeat("b", 32),
			Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
			YAML:      editedYAML,
		})
		if err != nil || op.State != "persisted" {
			t.Fatalf("SaveSource with legacy proxy credentials failed: (%+v, %v)", op, err)
		}
		if wire.document["requests"].(map[string]any)["proxy-url"] != "http://alice:s3cr3t@proxy.example.com:8080" {
			t.Fatalf("legacy requests.proxy-url was overwritten: %v", wire.document["requests"])
		}
		if wire.document["experimental-list"].([]any)[0].(map[string]any)["client-secret"] != "slice-secret" {
			t.Fatalf("slice client-secret was overwritten: %v", wire.document["experimental-list"])
		}
	})
}

func TestSourceModeLow3EmptyMapAndUnknownReplayAndCrossGroupReuse(t *testing.T) {
	ctx := context.Background()
	wire := &fakeWire{document: map[string]any{
		"custom-scalar": "old",
		"routing":       map[string]any{"strategy": "round-robin", "extra": "old"},
	}}
	svc, _ := managedService(t, wire)
	view, err := svc.ReadSource(ctx)
	if err != nil {
		t.Fatalf("ReadSource: %v", err)
	}

	op, err := svc.SaveSource(ctx, SourceRequest{
		RequestID: strings.Repeat("90", 16),
		Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
		YAML:      "custom-scalar: {}\nrouting:\n  extra: {}\n  strategy: round-robin\n",
	})
	if err != nil || op.State != "persisted" {
		t.Fatalf("SaveSource scalar->empty map = (%+v, %v)", op, err)
	}
	if m, ok := wire.document["custom-scalar"].(map[string]any); !ok || len(m) != 0 {
		t.Fatalf("expected custom-scalar replaced with {}, got %#v", wire.document["custom-scalar"])
	}
	if m, ok := wire.document["routing"].(map[string]any)["extra"].(map[string]any); !ok || len(m) != 0 {
		t.Fatalf("expected routing.extra replaced with {}, got %#v", wire.document["routing"])
	}

	// Replaying an unknown SaveSource operation returns ErrApplyUnknown, and cross-group reuse returns ErrSourceConflict.
	view2, _ := svc.ReadSource(ctx)
	wire.patchErr = cpa.ErrProofUnavailable
	reqUnknown := SourceRequest{
		RequestID: strings.Repeat("91", 16),
		Expected:  Expected{Generation: view2.Generation, Baseline: view2.Baseline},
		YAML:      "custom-scalar: {}\nrouting:\n  extra: {}\n  strategy: fill-first\n",
	}
	if _, err := svc.SaveSource(ctx, reqUnknown); !errors.Is(err, cpa.ErrApplyUnknown) {
		t.Fatalf("expected ErrApplyUnknown on lost patch, got %v", err)
	}
	if _, err := svc.SaveSource(ctx, reqUnknown); !errors.Is(err, cpa.ErrApplyUnknown) {
		t.Fatalf("expected ErrApplyUnknown on unknown SaveSource replay, got %v", err)
	}
	gView, _ := svc.Read(ctx)
	var retryBaseline string
	for _, g := range gView.Groups {
		if g.ID == "retry" {
			retryBaseline = g.Baseline
		}
	}
	if _, err := svc.SaveGroup(ctx, "retry", GroupRequest{
		RequestID: reqUnknown.RequestID,
		Expected:  Expected{Generation: gView.Generation, Baseline: retryBaseline},
		Edits:     []Edit{{Path: "routing.retry.request-retry", Action: "set", Value: json.RawMessage("2")}},
	}); !errors.Is(err, cpa.ErrSourceConflict) {
		t.Fatalf("expected ErrSourceConflict when reusing source requestId in retry group, got %v", err)
	}
}

func TestSourceModePreservesOriginallyEmptyMapsAndScalarToNonEmptyMapSet(t *testing.T) {
	ctx := context.Background()

	t.Run("sanitization preserves originally empty maps and drops stripped-empty maps", func(t *testing.T) {
		wire := &fakeWire{document: map[string]any{
			"empty-root": map[string]any{},
			"server":     map[string]any{"port": json.Number("8317")},
			"oauth": map[string]any{
				"auth-dir": "/data/auth",
				"providers": map[string]any{
					"only-secret": map[string]any{"client-secret": "top-secret"},
				},
			},
			"routing": map[string]any{
				"strategy": "round-robin",
				"empty":    map[string]any{},
			},
			"requests": map[string]any{
				"payload": map[string]any{},
			},
		}}
		svc, _ := managedService(t, wire)
		view, err := svc.ReadSource(ctx)
		if err != nil {
			t.Fatalf("ReadSource: %v", err)
		}
		doc, err := parseStrictSourceYAML(view.YAML)
		if err != nil {
			t.Fatalf("parseStrictSourceYAML: %v\n%s", err, view.YAML)
		}
		if m, ok := doc["empty-root"].(map[string]any); !ok || len(m) != 0 {
			t.Fatalf("expected empty-root: {} preserved, got %#v\n%s", doc["empty-root"], view.YAML)
		}
		routingMap, _ := doc["routing"].(map[string]any)
		if m, ok := routingMap["empty"].(map[string]any); !ok || len(m) != 0 {
			t.Fatalf("expected routing.empty: {} preserved, got %#v\n%s", routingMap, view.YAML)
		}
		reqMap, _ := doc["requests"].(map[string]any)
		if m, ok := reqMap["payload"].(map[string]any); !ok || len(m) != 0 {
			t.Fatalf("expected requests.payload: {} preserved, got %#v\n%s", reqMap, view.YAML)
		}
		if _, hasServer := doc["server"]; hasServer {
			t.Fatalf("stripped-empty server should not appear in ReadSource:\n%s", view.YAML)
		}
		if _, hasOAuth := doc["oauth"]; hasOAuth {
			t.Fatalf("stripped-empty oauth should not appear in ReadSource:\n%s", view.YAML)
		}
	})

	t.Run("external requests with empty payload map blocks delete and null", func(t *testing.T) {
		extStore := &memoryStore{record: managedRecord()}
		extStore.record.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.ConnectExternal, Stage: "committed", TargetIdentity: "external:sha256:" + strings.Repeat("b", 64), TargetGeneration: 1,
		}
		extStore.record.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: "https://external.test", ManagementKey: "cpamp_protected_envelope"}
		extWire := &fakeWire{document: map[string]any{
			"routing":  map[string]any{"strategy": "round-robin"},
			"requests": map[string]any{"payload": map[string]any{}},
		}}
		extSvc := New(extStore, nil, externalTargets())
		extSvc.EnableExternal(func(context.Context, uint64) (string, string, error) {
			return "https://external.test", "management-test-only", nil
		})
		extSvc.externalWire = func(string) configWire { return extWire }
		extSvc.externalProbe = func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
			return cpa.ClassifyExternalVersion("v8.0.64"), nil
		}
		view, err := extSvc.ReadSource(ctx)
		if err != nil {
			t.Fatalf("ReadSource: %v", err)
		}
		for i, badYAML := range []string{
			"routing:\n  strategy: round-robin\n",
			"requests: null\nrouting:\n  strategy: round-robin\n",
			"requests: {}\nrouting:\n  strategy: round-robin\n",
			"requests:\n  payload: null\nrouting:\n  strategy: round-robin\n",
		} {
			_, err := extSvc.SaveSource(ctx, SourceRequest{
				RequestID: strings.Repeat("f", 31) + string([]byte{'0' + byte(i)}),
				Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
				YAML:      badYAML,
			})
			if !errors.Is(err, ErrRulesReadOnly) {
				t.Fatalf("case %d (%q): expected ErrRulesReadOnly, got %v", i, badYAML, err)
			}
		}
		// Keeping requests.payload: {} untouched lets unrelated edits proceed cleanly.
		edited := strings.Replace(view.YAML, "strategy: round-robin", "strategy: fill-first", 1)
		op, err := extSvc.SaveSource(ctx, SourceRequest{
			RequestID: strings.Repeat("f", 31) + "9",
			Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
			YAML:      edited,
		})
		if err != nil || op.State != "persisted" {
			t.Fatalf("SaveSource keeping requests.payload: {} = (%+v, %v)", op, err)
		}
	})

	t.Run("scalar to non-empty map emits recordSet on scalar path without child PATCH", func(t *testing.T) {
		wire := &fakeWire{document: map[string]any{
			"custom-scalar": "old",
			"routing": map[string]any{
				"strategy": "round-robin",
				"extra":    "scalar-value",
			},
		}}
		svc, _ := managedService(t, wire)
		view, err := svc.ReadSource(ctx)
		if err != nil {
			t.Fatalf("ReadSource: %v", err)
		}
		// Reject nested null inside newly introduced map over scalar.
		_, err = svc.SaveSource(ctx, SourceRequest{
			RequestID: strings.Repeat("c1", 16),
			Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
			YAML:      "custom-scalar: old\nrouting:\n  extra:\n    bad: null\n  strategy: round-robin\n",
		})
		var rej Rejection
		if !errors.As(err, &rej) || rej.Code != CodeSourceInvalidYAML {
			t.Fatalf("expected CodeSourceInvalidYAML for nested null, got %v", err)
		}

		nextYAML := "custom-scalar:\n  enabled: true\n  label: custom\nrouting:\n  extra:\n    nested:\n      count: 2\n  strategy: round-robin\n"
		op, err := svc.SaveSource(ctx, SourceRequest{
			RequestID: strings.Repeat("c2", 16),
			Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
			YAML:      nextYAML,
		})
		if err != nil || op.State != "persisted" {
			t.Fatalf("SaveSource scalar->non-empty map = (%+v, %v)", op, err)
		}
		for _, p := range wire.patches {
			if p.path == "custom-scalar" || p.path == "routing.extra" {
				t.Fatalf("unexpected child PATCH against former scalar path %q: %+v", p.path, wire.patches)
			}
		}
		if m, ok := wire.document["custom-scalar"].(map[string]any); !ok || m["enabled"] != true || m["label"] != "custom" {
			t.Fatalf("expected custom-scalar replaced by PUT, got %#v", wire.document["custom-scalar"])
		}
		extra, ok := wire.document["routing"].(map[string]any)["extra"].(map[string]any)
		if !ok || extra["nested"] == nil {
			t.Fatalf("expected routing.extra replaced via parent PATCH, got %#v", wire.document["routing"])
		}
	})

	t.Run("normalizes integral and scientific YAML floats so 1.0 round-trips and 1e6 matches float validation", func(t *testing.T) {
		wire := &fakeWire{document: map[string]any{
			"routing": map[string]any{
				"strategy": "round-robin",
			},
			"experimental": map[string]any{
				"ratio": 1.0,
			},
		}}
		svc, _ := managedService(t, wire)
		view, err := svc.ReadSource(ctx)
		if err != nil {
			t.Fatalf("ReadSource: %v", err)
		}
		_, err = svc.SaveSource(ctx, SourceRequest{
			RequestID: strings.Repeat("d1", 16),
			Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
			YAML:      view.YAML,
		})
		var rej Rejection
		if !errors.As(err, &rej) || rej.Code != CodeSourceNoChanges {
			t.Fatalf("round-trip of 1.0 float returned %v, want Rejection %s", err, CodeSourceNoChanges)
		}

		// Both 10.0 and 1e6 must reject consistently as float values on an integer field.
		var prevCode string
		for i, badVal := range []string{"10.0", "1e6"} {
			_, err = svc.SaveSource(ctx, SourceRequest{
				RequestID: strings.Repeat("d2", 15) + string([]byte{'0' + byte(i)}) + "0",
				Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
				YAML:      "experimental:\n  ratio: 1\nrouting:\n  retry:\n    request-retry: " + badVal + "\n  strategy: round-robin\n",
			})
			if !errors.As(err, &rej) {
				t.Fatalf("integer field with float %s = %v, want Rejection", badVal, err)
			}
			if rej.Code != CodeSourceInvalidYAML && rej.Code != CodeInvalidConfig {
				t.Fatalf("integer field with float %s rejection code = %q", badVal, rej.Code)
			}
			if prevCode != "" && rej.Code != prevCode {
				t.Fatalf("inconsistent float rejection codes: 10.0=%q vs %s=%q", prevCode, badVal, rej.Code)
			}
			prevCode = rej.Code
		}
	})
}

func TestSourceModeProtectsHiddenCredentialedProxyAndRejectsCaseCollidingKeys(t *testing.T) {
	ctx := context.Background()

	t.Run("rejects overwriting stripped credentialed requests.proxy-url leaf", func(t *testing.T) {
		wire := &fakeWire{document: map[string]any{
			"routing": map[string]any{"strategy": "round-robin"},
			"requests": map[string]any{
				"proxy-url":           "http://alice:s3cr3t@proxy.example.com:8080",
				"passthrough-headers": true,
			},
		}}
		svc, _ := managedService(t, wire)
		view, err := svc.ReadSource(ctx)
		if err != nil {
			t.Fatalf("ReadSource: %v", err)
		}
		for i, candidate := range []string{
			"requests:\n  passthrough-headers: true\n  proxy-url: \"\"\nrouting:\n  strategy: round-robin\n",
			"requests:\n  passthrough-headers: true\n  proxy-url: http://plain.proxy.test:8080\nrouting:\n  strategy: round-robin\n",
		} {
			_, err = svc.SaveSource(ctx, SourceRequest{
				RequestID: strings.Repeat("e1", 15) + string([]byte{'0' + byte(i)}) + "a",
				Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
				YAML:      candidate,
			})
			var rej Rejection
			if !errors.As(err, &rej) || rej.Code != CodeSourceProtectedPath {
				t.Fatalf("case %d: SaveSource overwriting hidden credentialed proxy-url = %v, want %s", i, err, CodeSourceProtectedPath)
			}
		}
		if got := wire.document["requests"].(map[string]any)["proxy-url"]; got != "http://alice:s3cr3t@proxy.example.com:8080" {
			t.Fatalf("credentialed requests.proxy-url mutated: %v", got)
		}
	})

	t.Run("rejects case-colliding structural keys while preserving case-sensitive KVMap data keys", func(t *testing.T) {
		wire := &fakeWire{document: map[string]any{
			"routing": map[string]any{"strategy": "round-robin"},
		}}
		svc, _ := managedService(t, wire)
		view, err := svc.ReadSource(ctx)
		if err != nil {
			t.Fatalf("ReadSource: %v", err)
		}
		_, err = svc.SaveSource(ctx, SourceRequest{
			RequestID: strings.Repeat("e2", 16),
			Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
			YAML:      "routing:\n  strategy: round-robin\nRouting:\n  strategy: fill-first\n",
		})
		var rej Rejection
		if !errors.As(err, &rej) || rej.Code != CodeSourceInvalidYAML {
			t.Fatalf("case-colliding structural keys = %v, want %s", err, CodeSourceInvalidYAML)
		}

		op, err := svc.SaveSource(ctx, SourceRequest{
			RequestID: strings.Repeat("e3", 16),
			Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
			YAML:      "oauth:\n  providers:\n    antigravity:\n      sensitive-words-replace:\n        Foo: bar\n        foo: baz\nrouting:\n  strategy: round-robin\n",
		})
		if err != nil || op.State != "persisted" {
			t.Fatalf("case-sensitive KVMap keys should succeed, got (%+v, %v)", op, err)
		}
	})
}

func TestSourceModeRejectsCaseMismatchedKeysAgainstRawDocumentBeforeJournalOrCPAWrite(t *testing.T) {
	ctx := context.Background()
	wire := &fakeWire{
		document: map[string]any{
			"requests": map[string]any{
				"proxy-url":           "http://alice:s3cr3t@proxy.example.com:8080",
				"passthrough-headers": true,
			},
			"server": map[string]any{
				"port": json.Number("8317"),
			},
			"routing": map[string]any{
				"strategy": "round-robin",
			},
			"oauth": map[string]any{
				"providers": map[string]any{
					"antigravity": map[string]any{
						"sensitive-words-replace": map[string]any{
							"foo": "bar",
						},
					},
				},
			},
		},
	}
	svc, store := managedService(t, wire)
	view, err := svc.ReadSource(ctx)
	if err != nil {
		t.Fatalf("ReadSource: %v", err)
	}

	cases := []struct {
		name     string
		yaml     string
		wantCode string
	}{
		{
			name: "Requests collides with raw requests holding credentialed proxy-url",
			yaml: "oauth:\n  providers:\n    antigravity:\n      sensitive-words-replace:\n        foo: bar\nRequests:\n  passthrough-headers: false\nrouting:\n  strategy: round-robin\n",
			wantCode: CodeSourceProtectedPath,
		},
		{
			name: "Server collides with stripped raw server holding only port",
			yaml: "oauth:\n  providers:\n    antigravity:\n      sensitive-words-replace:\n        foo: bar\nrequests:\n  passthrough-headers: true\nrouting:\n  strategy: round-robin\nServer:\n  max-request-body-bytes: 1048576\n",
			wantCode: CodeSourceProtectedPath,
		},
		{
			name: "Routing collides with ordinary raw routing",
			yaml: "oauth:\n  providers:\n    antigravity:\n      sensitive-words-replace:\n        foo: bar\nrequests:\n  passthrough-headers: true\nRouting:\n  strategy: fill-first\n",
			wantCode: CodeSourceInvalidYAML,
		},
		{
			name: "nested Routing.Strategy collides with ordinary raw routing.strategy",
			yaml: "oauth:\n  providers:\n    antigravity:\n      sensitive-words-replace:\n        foo: bar\nrequests:\n  passthrough-headers: true\nrouting:\n  Strategy: fill-first\n",
			wantCode: CodeSourceInvalidYAML,
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, saveErr := svc.SaveSource(ctx, SourceRequest{
				RequestID: strings.Repeat("a7", 15) + string([]byte{'0' + byte(i)}) + "f",
				Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
				YAML:      tc.yaml,
			})
			var rej Rejection
			if !errors.As(saveErr, &rej) || rej.Code != tc.wantCode {
				t.Fatalf("SaveSource(%s) = %v, want Rejection{%s}", tc.name, saveErr, tc.wantCode)
			}
			if len(wire.patches) != 0 || len(wire.puts) != 0 || len(wire.deletes) != 0 {
				t.Fatalf("CPA write count non-zero: patches=%d puts=%d deletes=%d", len(wire.patches), len(wire.puts), len(wire.deletes))
			}
			if entry := store.record.ConfigGroups[ConfigSourceGroupID]; entry != nil && len(entry.Operations) != 0 {
				t.Fatalf("rejected case-mismatch wrote source journal entry: %+v", entry)
			}
		})
	}

	// KVMap data keys remain case-sensitive and unrestricted by existing raw data keys.
	op, err := svc.SaveSource(ctx, SourceRequest{
		RequestID: strings.Repeat("b8", 16),
		Expected:  Expected{Generation: view.Generation, Baseline: view.Baseline},
		YAML:      "oauth:\n  providers:\n    antigravity:\n      sensitive-words-replace:\n        Foo: updated\nrequests:\n  passthrough-headers: true\nrouting:\n  strategy: round-robin\n",
	})
	if err != nil || op.State != "persisted" {
		t.Fatalf("KVMap case-distinct data key save = (%+v, %v), want persisted", op, err)
	}
	kvMap := wire.document["oauth"].(map[string]any)["providers"].(map[string]any)["antigravity"].(map[string]any)["sensitive-words-replace"].(map[string]any)
	if kvMap["Foo"] != "updated" {
		t.Fatalf("expected KVMap data key Foo persisted, got %#v", kvMap)
	}
	if _, exists := kvMap["foo"]; exists {
		t.Fatalf("expected atomic KVMap replacement to remove omitted lowercase foo, got %#v", kvMap)
	}
}
