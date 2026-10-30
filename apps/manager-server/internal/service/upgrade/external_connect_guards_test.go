package upgrade

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
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
// itself: a real CPA-shaped server that answers /v8/management/config with a v7
// document must be refused, and one with a v8 document must be accepted, so the
// refusal above cannot be satisfied by a seam that refuses everything.
func TestExternalConnectReadsTheConfigurationStrictly(t *testing.T) {
	config := func(version int, port int, authDir string) string {
		return fmt.Sprintf(`{"config-version":%d,"server":{"port":%d},"oauth":{"auth-dir":%q},"access":{"api-keys":["client-key-1"]}}`,
			version, port, authDir)
	}
	for name, testCase := range map[string]struct {
		body string
		want bool
	}{
		"v8 document":          {body: config(8, 8317, "/auth"), want: true},
		"v7 document":          {body: config(7, 8317, "/auth"), want: false},
		"unbounded port":       {body: config(8, 0, "/auth"), want: false},
		"empty auth-dir":       {body: config(8, 8317, ""), want: false},
		"unparsable document":  {body: `{"config-version":`, want: false},
		"no client key at all": {body: `{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"/auth"}}`, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/v8/management/config" {
					writer.WriteHeader(http.StatusNotFound)
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(testCase.body))
			}))
			defer server.Close()
			target := newExternalTarget(t)
			_, err := target.connectWith(t, ExternalConnectOptions{
				TargetRoot:    target.copied.target,
				BaseURL:       server.URL,
				ManagementKey: externalTestKey,
			})
			if testCase.want && err != nil {
				t.Fatalf("a v8 document was refused: %v", err)
			}
			if !testCase.want {
				if !errors.Is(err, ErrExternalVersionUnsupported) && !errors.Is(err, ErrExternalAuthorityUnavailable) {
					t.Fatalf("err = %v, want a refusal", err)
				}
				if recorded := operationRow(t, target.databasePath()); recorded.Stage != model.UpgradeStageCopyIn {
					t.Fatalf("stage advanced to %q", recorded.Stage)
				}
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

// TestExternalConnectIgnoresTheEnvironmentProxy proves a probe carrying a client
// credential never travels through the environment's HTTP proxy.
//
// The assertion is made in a child process, because http.ProxyFromEnvironment
// caches the environment process-wide on first use: an in-process t.Setenv would
// be order-dependent and could silently assert nothing. The child also proves its
// own poison first — a default-transport request must reach the recording proxy —
// so a proxy the runtime ignores cannot look like a pass.
//
// Linux only: this host does not dial 0.0.0.0, and the child has to reach a
// listener on a wildcard address so the same address is both direct and proxied.
func TestExternalConnectIgnoresTheEnvironmentProxy(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("darwin does not dial 0.0.0.0; the environment-proxy assertion runs on the Linux CI matrix")
	}
	if os.Getenv("UPGRADE_PROXY_CHILD") != "1" {
		proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer proxy.Close()
		command := exec.Command(os.Args[0], "-test.run=^TestExternalConnectIgnoresTheEnvironmentProxy$", "-test.count=1")
		command.Env = append(os.Environ(),
			"UPGRADE_PROXY_CHILD=1",
			"HTTP_PROXY="+proxy.URL,
			"http_proxy="+proxy.URL,
			"HTTPS_PROXY="+proxy.URL,
			"https_proxy="+proxy.URL,
			"NO_PROXY=",
			"no_proxy=",
		)
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Run(); err != nil {
			t.Fatalf("the child process failed: %v", err)
		}
		return
	}
	runExternalConnectProxyChild(t)
}

// runExternalConnectProxyChild is the child half of the proxy test.
func runExternalConnectProxyChild(t *testing.T) {
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

	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var managementSurface atomic.Int64
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		managementSurface.Add(1)
		if request.URL.Path != "/v1/models" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[{"id":"direct-model"}]}`))
	}))
	target.Listener = listener
	target.Start()
	defer target.Close()
	address := "http://" + listener.Addr().String()

	// Poison check: the default transport does honour this environment, so a
	// request built like the one this fix replaced would be observed by the proxy.
	response, err := (&http.Client{}).Get(address + "/v1/models")
	if err != nil {
		t.Fatalf("the poison request failed: %v", err)
	}
	_ = response.Body.Close()
	if proxied.Load() == 0 {
		t.Fatal("the environment proxy is not honoured; the assertion cannot fail")
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
	if managementSurface.Load() == 0 {
		t.Fatal("the CPA never served the model surface; the assertion cannot fail")
	}
}
