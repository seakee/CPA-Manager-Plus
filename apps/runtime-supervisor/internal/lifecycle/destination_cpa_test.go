package lifecycle

import (
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

// Optional real-executable check. CI verifies the exact official archive/binary
// before setting this input. A native source build is only a local smoke test.
func TestDestinationRealCPAV8(t *testing.T) {
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
	defer e.Close()
	e.artifact = observer
	if err = e.EnableDestination(filepath.Join(root, "destination"), address, nil); err != nil {
		t.Fatal(err)
	}
	r := DestinationRequest{OperationID: "real-native:adopt-destination", ExpectedRuntimeIdentity: "real-v8-target", ExpectedRuntimeGeneration: 1, ExpectedActiveArtifactID: observer.Observation().ArtifactID, SourceIdentity: "external:sha256:" + strings.Repeat("a", 64), SourceClass: "network_existing", APIKeys: []string{"disposable-client-one", "disposable-client-two"}, ManagementKey: strings.Repeat("b", 64)}
	if _, err = e.StageDestination(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Start(t.Context(), StartRequest{OperationID: r.OperationID + ":start", ExpectedRuntimeIdentity: r.ExpectedRuntimeIdentity, ExpectedRuntimeGeneration: 1, ExpectedActiveArtifactID: r.ExpectedActiveArtifactID}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		target, err := child.PrepareStop()
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = target.Terminate(ctx)
			target.Release()
		}
	}()
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
	var config struct {
		Access struct {
			Keys []string `json:"api-keys"`
		} `json:"access"`
	}
	if json.Unmarshal(body, &config) != nil || strings.Join(config.Access.Keys, ",") != strings.Join(r.APIKeys, ",") {
		t.Fatal("real destination did not expose replayed native keys")
	}
	for _, key := range append([]string{"", r.ManagementKey}, r.APIKeys...) {
		req, _ := http.NewRequest(http.MethodGet, "http://"+address+"/v1/models", nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		want := http.StatusUnauthorized
		if key == r.APIKeys[0] || key == r.APIKeys[1] {
			want = 200
		}
		if response.StatusCode != want {
			t.Fatalf("real destination auth status=%d want=%d", response.StatusCode, want)
		}
	}
}
