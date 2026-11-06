package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
)

// startRealDestination stages r on an explicitly supplied disposable CPA v8
// executable and returns its loopback address once management answers.
func startRealDestination(t *testing.T, mutate func(*DestinationRequest)) (DestinationRequest, string, []byte) {
	t.Helper()
	executable := os.Getenv("CPAMP_TEST_CPA_V8")
	if executable == "" {
		t.Skip("requires an explicitly supplied disposable CPA v8 executable")
	}
	root := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	observer := artifact.NewObserver(executable, "")
	_ = observer.Refresh()
	if observer.Observation() == nil {
		t.Fatal("executable observation unavailable")
	}
	store, err := journal.Open(t.Context(), filepath.Join(root, "operations.sqlite"), journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	child := cpaprocess.NewManager(nil)
	e, err := NewExecutor(journal.Authority{RuntimeIdentity: "real-v8-target", RuntimeGeneration: 1}, store, child, executable)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	e.artifact = observer
	if err = e.EnableDestination(filepath.Join(root, "destination"), filepath.Join(root, "gateway", "auth"), address, nil); err != nil {
		t.Fatal(err)
	}
	r := DestinationRequest{OperationID: "real-native:adopt-destination", ExpectedRuntimeIdentity: "real-v8-target", ExpectedRuntimeGeneration: 1, ExpectedActiveArtifactID: observer.Observation().ArtifactID, SourceIdentity: "external:sha256:" + strings.Repeat("a", 64), SourceClass: "network_existing", APIKeys: []string{"disposable-client-one", "disposable-client-two"}, ManagementKey: strings.Repeat("b", 64)}
	mutate(&r)
	if _, err = e.StageDestination(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Start(t.Context(), StartRequest{OperationID: r.OperationID + ":start", ExpectedRuntimeIdentity: r.ExpectedRuntimeIdentity, ExpectedRuntimeGeneration: 1, ExpectedActiveArtifactID: r.ExpectedActiveArtifactID}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		target, err := child.PrepareStop()
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = target.Terminate(ctx)
			target.Release()
		}
	})
	client := &http.Client{Timeout: time.Second}
	var body []byte
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, "http://"+address+"/v8/management/config", nil)
		req.Header.Set("Authorization", "Bearer "+r.ManagementKey)
		response, err := client.Do(req)
		if err == nil {
			body, _ = io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return r, address, body
}

func realStatus(t *testing.T, method, url, key string, payload []byte) int {
	t.Helper()
	req, _ := http.NewRequest(method, url, bytes.NewReader(payload))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	return response.StatusCode
}

// Optional real-executable check. CI verifies the exact official archive/binary
// before setting this input. A native source build is only a local smoke test.
func TestDestinationRealCPAV8(t *testing.T) {
	r, address, body := startRealDestination(t, func(*DestinationRequest) {})
	var config struct {
		Access struct {
			Keys []string `json:"api-keys"`
		} `json:"access"`
	}
	if json.Unmarshal(body, &config) != nil || strings.Join(config.Access.Keys, ",") != strings.Join(r.APIKeys, ",") {
		t.Fatal("real destination did not expose replayed native keys")
	}
	for _, key := range append([]string{"", r.ManagementKey}, r.APIKeys...) {
		want := http.StatusUnauthorized
		if key == r.APIKeys[0] || key == r.APIKeys[1] {
			want = 200
		}
		if status := realStatus(t, http.MethodGet, "http://"+address+"/v1/models", key, nil); status != want {
			t.Fatalf("real destination auth status=%d want=%d", status, want)
		}
	}
}

// The default New Managed destination starts with exactly the Client Key the
// Manager generated and persisted before staging. CPA v8 disables client auth
// when the list is empty, so an anonymous Compose peer must be refused from the
// first second and only that key may authenticate.
func TestDestinationRealCPAV8NewManaged(t *testing.T) {
	initial := "sk-" + strings.Repeat("a", 64)
	r, address, body := startRealDestination(t, func(r *DestinationRequest) {
		r.OperationID, r.SourceClass, r.SourceIdentity, r.APIKeys = "real-native:managed-destination", SourceClassNewManaged, "managed:sha256:"+strings.Repeat("c", 64), []string{initial}
	})
	assertRealManagedDestinationPosture(t, body, r, initial, true)
	base := "http://" + address
	if status := realStatus(t, http.MethodGet, base+"/v8/management/config", strings.Repeat("d", 64), nil); status != http.StatusUnauthorized {
		t.Fatalf("foreign management key status=%d", status)
	}
	// Real CPA access middleware: the install key authenticates, every other
	// credential (none, a foreign Client Key, the management key) is refused.
	for _, sample := range []struct {
		key  string
		want int
	}{{"", http.StatusUnauthorized}, {"cpamp_sk_" + strings.Repeat("e", 64), http.StatusUnauthorized},
		{r.ManagementKey, http.StatusUnauthorized}, {initial, http.StatusOK}} {
		if status := realStatus(t, http.MethodGet, base+"/v1/models", sample.key, nil); status != sample.want {
			t.Fatalf("real New Managed client auth key=%q status=%d want=%d", sample.key, status, sample.want)
		}
	}
}

func assertRealManagedDestinationPosture(t *testing.T, body []byte, r DestinationRequest, initial string, wantAllowRemote bool) {
	t.Helper()
	var config struct {
		Access struct {
			Keys []string `json:"api-keys"`
		} `json:"access"`
		Management struct {
			AllowRemote         bool   `json:"allow-remote"`
			SecretKey           string `json:"secret-key"`
			DisableControlPanel bool   `json:"disable-control-panel"`
		} `json:"management"`
		Observability struct {
			Usage struct {
				Enabled bool `json:"usage-statistics-enabled"`
			} `json:"usage"`
		} `json:"observability"`
	}
	if json.Unmarshal(body, &config) != nil || len(config.Access.Keys) != 1 || config.Access.Keys[0] != initial || config.Management.AllowRemote != wantAllowRemote ||
		!config.Management.DisableControlPanel || !config.Observability.Usage.Enabled || !strings.HasPrefix(config.Management.SecretKey, "$2") || strings.Contains(string(body), r.ManagementKey) {
		t.Fatalf("real New Managed destination posture: %s", body)
	}
}
