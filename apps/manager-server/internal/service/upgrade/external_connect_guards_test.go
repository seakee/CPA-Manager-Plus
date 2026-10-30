package upgrade

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

// TestExternalConnectNeverRewindsAConcurrentAdvance proves the recording write is
// stage-guarded.
//
// The step spends three network probes between reading the row and writing it.
// Rebuilding the row from that snapshot would erase anything that advanced while
// the probes ran: the verify domain and stage would be replaced by this step's
// stage, and the refusal would be reported as if the CPA had answered. The hook
// below performs exactly that concurrent advance, at the moment the real code
// path cannot observe it locally.
func TestExternalConnectNeverRewindsAConcurrentAdvance(t *testing.T) {
	target := newExternalTarget(t)
	if got := operationRow(t, target.databasePath()); got.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("the fixture did not start at copy_in: %q", got.Stage)
	}
	target.options.proveModelSurface = func(context.Context, string, string) error {
		db, err := managersqlite.Open(target.databasePath())
		if err != nil {
			t.Fatalf("open for the concurrent advance: %v", err)
		}
		defer func() { _ = db.Close() }()
		if _, err := upgradeoperation.New(db).Advance(context.Background(), model.UpgradeDomainUsageEvents, model.UpgradeStageVerify); err != nil {
			t.Fatalf("concurrent advance: %v", err)
		}
		return nil
	}
	_, err := target.connect(t)
	if !errors.Is(err, ErrExternalNotApplicable) {
		t.Fatalf("err = %v, want the not-applicable refusal", err)
	}
	recorded := operationRow(t, target.databasePath())
	if recorded.Stage != model.UpgradeStageVerify {
		t.Fatalf("the step rewound the stage to %q", recorded.Stage)
	}
	if !recorded.HasDomain(model.UpgradeDomainUsageEvents) {
		t.Fatalf("the step erased a concurrent domain: %v", recorded.DomainsDone)
	}
	if recorded.HasDomain(model.UpgradeDomainCPAConfig) || recorded.HasDomain(model.UpgradeDomainIdentity) {
		t.Fatalf("a refused step marked its domains: %v", recorded.DomainsDone)
	}
	// The row is the authority and the evidence file is not, so the guarded write
	// runs first: a refusal that never recorded the row must leave no evidence
	// behind, or a rerun would read a connection the operation never took.
	if _, found, evidenceErr := readExternalConnectEvidence(target.copied.target); evidenceErr != nil || found {
		t.Fatalf("a refused record left evidence behind: found %t, err %v", found, evidenceErr)
	}
}

// TestExternalConnectRefusesATranslatedV7Document proves "the endpoint answers" is
// not "the target is v8".
//
// A proxied or translated target can advertise a v8 version header and answer
// /v8 with a v7 document. The strict reader is the proof that the configuration
// this Manager would write into is the contract it writes, and its refusal has to
// stop the step instead of being recorded as a connection.
func TestExternalConnectRefusesATranslatedV7Document(t *testing.T) {
	target := newExternalTarget(t)
	target.configErr = cpa.V8InvalidConfig
	_, err := target.connect(t)
	if !errors.Is(err, ErrExternalVersionUnsupported) {
		t.Fatalf("err = %v, want the version refusal", err)
	}
	if strings.Contains(err.Error(), "Bearer") {
		t.Fatalf("the refusal carries a credential: %v", err)
	}
	recorded := operationRow(t, target.databasePath())
	if recorded.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("stage advanced to %q", recorded.Stage)
	}
	if recorded.ErrorCode != string(UpgradeErrorExternalVersionUnsupported) {
		t.Fatalf("recorded error code = %q", recorded.ErrorCode)
	}
	if _, found, evidenceErr := readExternalConnectEvidence(target.copied.target); evidenceErr != nil || found {
		t.Fatalf("a refused document recorded evidence: found %t, err %v", found, evidenceErr)
	}
}

// TestExternalConnectReadsTheConfigurationStrictly exercises the strict reader
// itself, against a real CPA-shaped server, because the two refusals it can
// produce are not interchangeable.
//
// A document that declares another generation is the CPA saying it is not v8,
// and only that answer may tell the operator to upgrade the CPA. A document that
// cannot be a v8 configuration at all — a proxy's page, a truncated body, an
// impossible port, a key list that is not a list of strings — says nothing about
// the generation, so telling the operator to upgrade a CPA that may already be v8
// sends them after the wrong cause. The table therefore asserts the exact code of
// every refusal and that the two codes never appear together.
//
// The v8 rows are the shapes a real v8 CPA legitimately serves: a default auth
// directory, an empty one, no client Key list and a null one (Go serialises a nil
// slice as null). Refusing those told the operator to upgrade a CPA that was
// already v8.
func TestExternalConnectReadsTheConfigurationStrictly(t *testing.T) {
	const v8Document = `{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"/auth"},"access":{"api-keys":["client-key-1"]}}`
	cases := []struct {
		name        string
		contentType string
		body        string
		want        error // nil means the document is accepted
	}{
		{name: "a v8 document", body: v8Document},
		{
			name: "a v8 document without an auth directory",
			body: `{"config-version":8,"server":{"port":8317},"access":{"api-keys":["client-key-1"]}}`,
		},
		{
			name: "a v8 document with an empty auth directory",
			body: `{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":""},"access":{"api-keys":["client-key-1"]}}`,
		},
		{
			name: "a v8 document without a client key",
			body: `{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"/auth"}}`,
		},
		{
			name: "a v8 document with a null client-key list",
			body: `{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"/auth"},"access":{"api-keys":null}}`,
		},
		{
			name: "a document declaring the previous generation",
			body: `{"config-version":7,"server":{"port":8317},"oauth":{"auth-dir":"/auth"},"access":{"api-keys":["client-key-1"]}}`,
			want: ErrExternalVersionUnsupported,
		},
		{
			name: "a document declaring a later generation",
			body: `{"config-version":9,"server":{"port":8317},"oauth":{"auth-dir":"/auth"},"access":{"api-keys":["client-key-1"]}}`,
			want: ErrExternalVersionUnsupported,
		},
		{
			name: "a document that declares no generation",
			body: `{"server":{"port":8317},"oauth":{"auth-dir":"/auth"},"access":{"api-keys":["client-key-1"]}}`,
			want: ErrExternalConfigDocumentInvalid,
		},
		{
			name: "an unbounded port",
			body: `{"config-version":8,"server":{"port":0},"oauth":{"auth-dir":"/auth"},"access":{"api-keys":["client-key-1"]}}`,
			want: ErrExternalConfigDocumentInvalid,
		},
		{
			name: "a port above the range",
			body: `{"config-version":8,"server":{"port":65536},"oauth":{"auth-dir":"/auth"},"access":{"api-keys":["client-key-1"]}}`,
			want: ErrExternalConfigDocumentInvalid,
		},
		{
			name: "a truncated document",
			body: `{"config-version":`,
			want: ErrExternalConfigDocumentInvalid,
		},
		{
			name:        "a proxy page instead of the document",
			contentType: "text/html",
			body:        `<html><body>gateway</body></html>`,
			want:        ErrExternalConfigDocumentInvalid,
		},
		{
			name: "a client-key list that is not a list of strings",
			body: `{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"/auth"},"access":{"api-keys":[7]}}`,
			want: ErrExternalConfigDocumentInvalid,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			contentType := testCase.contentType
			if contentType == "" {
				contentType = "application/json"
			}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/v8/management/config" {
					writer.WriteHeader(http.StatusNotFound)
					return
				}
				writer.Header().Set("Content-Type", contentType)
				_, _ = writer.Write([]byte(testCase.body))
			}))
			defer server.Close()
			target := newExternalTarget(t)
			_, err := target.connectWith(t, ExternalConnectOptions{
				TargetRoot:    target.copied.target,
				BaseURL:       server.URL,
				ManagementKey: externalTestKey,
			})
			recorded := operationRow(t, target.databasePath())
			if testCase.want == nil {
				if err != nil {
					t.Fatalf("a v8 document was refused: %v", err)
				}
				if recorded.Stage != model.UpgradeStageExternalConnect {
					t.Fatalf("an accepted document left stage %q", recorded.Stage)
				}
				if !hasDomains(recorded.DomainsDone, model.UpgradeDomainCPAConfig, model.UpgradeDomainIdentity) {
					t.Fatalf("an accepted document marked %v", recorded.DomainsDone)
				}
				return
			}
			if !errors.Is(err, testCase.want) {
				t.Fatalf("err = %v, want %v", err, testCase.want)
			}
			// The version stop and the document stop are answers to different
			// questions, so exactly one of them may describe a refusal.
			if errors.Is(err, ErrExternalVersionUnsupported) == errors.Is(err, ErrExternalConfigDocumentInvalid) {
				t.Fatalf("the refusal does not name exactly one of the two document verdicts: %v", err)
			}
			wantCode, _ := UpgradeErrorCodeOf(testCase.want)
			if recorded.ErrorCode != string(wantCode) {
				t.Fatalf("recorded error code = %q, want %q", recorded.ErrorCode, wantCode)
			}
			if recorded.Stage != model.UpgradeStageCopyIn {
				t.Fatalf("a refusal advanced the stage to %q", recorded.Stage)
			}
			if recorded.HasDomain(model.UpgradeDomainCPAConfig) || recorded.HasDomain(model.UpgradeDomainIdentity) {
				t.Fatalf("a refusal marked domains: %v", recorded.DomainsDone)
			}
			if _, found, evidenceErr := readExternalConnectEvidence(target.copied.target); evidenceErr != nil || found {
				t.Fatalf("a refusal recorded evidence: found %t, err %v", found, evidenceErr)
			}
		})
	}
}

// TestExternalConnectRecordsOneCanonicalIdentity proves the three spellings of one
// CPA are one target.
//
// The identity is a digest, so a capitalized host, an explicit default port or a
// trailing slash would otherwise read as a different CPA: a rerun after the
// operator retyped the address would be a moved target, and the key bindings of
// the installation would name three addresses. The recorded evidence carries the
// one canonical spelling, and a rerun through another spelling is the same
// connection.
func TestExternalConnectRecordsOneCanonicalIdentity(t *testing.T) {
	target := newExternalTarget(t)
	target.options.BaseURL = "HTTPS://CPA.Example:443/"
	first, err := target.connect(t)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	evidence, found, err := readExternalConnectEvidence(target.copied.target)
	if err != nil || !found {
		t.Fatalf("evidence = %+v, found %t, err %v", evidence, found, err)
	}
	if evidence.BaseURL != "https://cpa.example" {
		t.Fatalf("recorded address = %q, want the canonical spelling", evidence.BaseURL)
	}
	second, err := newExternalTargetAt(t, target, "https://CPA.example:443/")
	if err != nil {
		t.Fatalf("connect through another spelling: %v", err)
	}
	if !second.AlreadyComplete || second.Generation != first.Generation {
		t.Fatalf("another spelling of the same CPA was a new connection: %+v vs %+v", second, first)
	}
	// A different CPA behind the same scheme stays a different target.
	if _, err := newExternalTargetAt(t, target, "https://other.example"); !errors.Is(err, ErrExternalTargetMoved) {
		t.Fatalf("a different CPA refused with %v", err)
	}
}

// TestExternalConnectIgnoresTheEnvironmentProxy proves the model-surface probe —
// the one External read that carries a client credential — never travels through
// the environment's HTTP proxy.
//
// The assertion runs in a child process for two reasons. http.ProxyFromEnvironment
// caches the environment process-wide on its first use, so an in-process t.Setenv
// would be order-dependent and could silently assert nothing. And the child has to
// own both the recording proxy and the target, because the proxy URL it exports
// must name a listener in the process that reads the environment; a parent that
// exports its own proxy while the child counts a different one asserts nothing at
// all.
//
// The child proves its own poison first: an ordinary default-transport request
// must be seen by the recording proxy. Only then is the same address probed by the
// step, and only then does "no request reached the proxy" mean anything.
//
// The target listens on 0.0.0.0:0 so one address is both direct and proxied:
// net/http never proxies "localhost" or a loopback literal, but it does proxy a
// wildcard host, and this host and the Linux CI both dial 0.0.0.0 to the loopback.
// A platform that stopped doing either fails the poison assertion below instead of
// skipping, so the premise is never assumed silently.
func TestExternalConnectIgnoresTheEnvironmentProxy(t *testing.T) {
	if os.Getenv("UPGRADE_PROXY_CHILD") == "1" {
		runExternalConnectProxyChild(t)
		return
	}
	// The child inherits this process's environment, including this marker; it
	// must not carry a proxy, because the child exports its own after it starts
	// so the URL it names is a listener the child itself counts.
	t.Setenv("UPGRADE_PROXY_CHILD", "1")
	command := exec.Command(os.Args[0], "-test.run=^TestExternalConnectIgnoresTheEnvironmentProxy$", "-test.count=1")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		t.Fatalf("the child process failed: %v", err)
	}
}

// runExternalConnectProxyChild is the child half of the proxy test: it owns the
// proxy and the target, poisons its own environment, and asserts both directions
// in one process.
func runExternalConnectProxyChild(t *testing.T) {
	t.Helper()
	var proxied atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "" {
			t.Errorf("a request carrying a credential reached the environment proxy: %s %s", request.Method, request.URL)
		}
		proxied.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[{"id":"proxy-model"}]}`))
	}))
	defer proxy.Close()

	// The real v8 surface, with no seam in front of it: the step's own version,
	// configuration and key reads run against this listener. It is bound to the
	// IPv4 wildcard explicitly so the address it reports is 0.0.0.0 and not the
	// dual-stack [::], which keeps the proxied and the direct spelling one value.
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen on the wildcard address: %v", err)
	}
	var modelRequests atomic.Int64
	var modelCredential atomic.Value
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v0/management/config":
			writer.Header().Set("X-CPA-Version", "v8.0.13")
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{}`))
		case "/v8/management/config":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"/auth"},"access":{"api-keys":["client-key-1"]}}`))
		case "/v8/management/config/access/api-keys":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`["client-key-1"]`))
		case "/v8/management/config.yaml":
			writer.Header().Set("Content-Type", "application/yaml")
			_, _ = writer.Write([]byte("server:\n  port: 8317\n"))
		case "/v1/models":
			modelRequests.Add(1)
			modelCredential.Store(request.Header.Get("Authorization"))
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"data":[{"id":"direct-model"}]}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	target.Listener = listener
	target.Start()
	defer target.Close()
	address := "http://" + listener.Addr().String()

	// The environment is read here and nowhere earlier, so the cache the default
	// transport keeps is populated with this proxy and not with the host's.
	for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(name, proxy.URL)
	}
	for _, name := range []string{"NO_PROXY", "no_proxy"} {
		t.Setenv(name, "")
	}

	// Poison check: a request built the way the fix replaced would carry the
	// client key, and this one must land on the proxy for the assertion below to
	// be able to fail.
	response, err := (&http.Client{}).Get(address + "/v1/models")
	if err != nil {
		t.Fatalf("the poison request failed: %v", err)
	}
	_ = response.Body.Close()
	if proxied.Load() == 0 {
		t.Fatalf("the environment proxy %s is not honoured through %s; the assertion cannot fail", proxy.URL, address)
	}
	before := proxied.Load()

	copied := newCopiedVolume(t)
	if _, err := ExternalConnect(context.Background(), ExternalConnectOptions{
		TargetRoot:    copied.target,
		BaseURL:       address,
		ManagementKey: externalTestKey,
	}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if after := proxied.Load(); after != before {
		t.Fatalf("the External connect sent %d request(s) through the environment proxy", after-before)
	}
	if modelRequests.Load() == 0 {
		t.Fatal("the CPA never served the model surface; the assertion cannot fail")
	}
	// The probe carries the client key, so "the request arrived" is the weaker
	// half of the fact: the credential itself has to have stayed on the direct
	// hop.
	if got, _ := modelCredential.Load().(string); got != "Bearer client-key-1" {
		t.Fatalf("the CPA served the model surface without the client credential: %q", got)
	}
}
