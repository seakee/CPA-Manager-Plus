package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
)

func destinationFixture(t *testing.T) (*prepareFixture, DestinationRequest, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("production destination staging is Linux-only; Windows retains ordinary lifecycle")
	}
	data := []byte("exact CPA destination executable for test")
	digest := sha256.Sum256(data)
	id := artifact.ID("sha256:" + hex.EncodeToString(digest[:]))
	f := newPrepareFixture(t, id)
	executable := filepath.Join(filepath.Dir(f.path), "cpa")
	if err := os.WriteFile(executable, data, 0o700); err != nil {
		t.Fatal(err)
	}
	f.starter.executable = executable
	root := filepath.Join(filepath.Dir(f.path), "destination")
	if err := f.starter.EnableDestination(root, gatewayAuthFixture(f), "127.0.0.1:8317", nil); err != nil {
		t.Fatal(err)
	}
	r := DestinationRequest{OperationID: "fresh-operation:adopt-destination", ExpectedRuntimeIdentity: "runtime-01", ExpectedRuntimeGeneration: 41, ExpectedActiveArtifactID: id, SourceIdentity: "external:sha256:" + strings.Repeat("b", 64), SourceClass: "network_existing", APIKeys: []string{"test-client-secret"}, ManagementKey: strings.Repeat("c", 64)}
	return f, r, root
}

func gatewayAuthFixture(f *prepareFixture) string {
	return filepath.Join(filepath.Dir(f.path), "gateway", "auth")
}

// initialClientKeyFixture is the Manager-generated install key the default
// destination must carry: "sk-" + 64 [A-Za-z0-9].
func initialClientKeyFixture() string {
	return "sk-" + strings.Repeat("aB3", 21) + "Z"
}

func newManagedRequest(r DestinationRequest) DestinationRequest {
	r.OperationID = "fresh-operation:managed-destination"
	r.SourceClass = SourceClassNewManaged
	r.SourceIdentity = "managed:sha256:" + strings.Repeat("a", 64)
	r.APIKeys = []string{initialClientKeyFixture()}
	return r
}

func TestDestinationNewManagedValidationIsClassBound(t *testing.T) {
	_, r, _ := destinationFixture(t)
	managed := newManagedRequest(r)
	if managed.Validate() != nil {
		t.Fatal("new_managed destination rejected")
	}
	for name, change := range map[string]func(*DestinationRequest){
		"absent keys": func(r *DestinationRequest) { r.APIKeys = []string{} },
		"nil keys":    func(r *DestinationRequest) { r.APIKeys = nil },
		"two keys": func(r *DestinationRequest) {
			r.APIKeys = []string{initialClientKeyFixture(), initialClientKeyFixture()}
		},
		"workspace key":     func(r *DestinationRequest) { r.APIKeys = []string{"cpamp_sk_" + strings.Repeat("a", 64)} },
		"external identity": func(r *DestinationRequest) { r.SourceIdentity = "external:sha256:" + strings.Repeat("a", 64) },
		"short identity":    func(r *DestinationRequest) { r.SourceIdentity = "managed:sha256:" + strings.Repeat("a", 63) },
		"non-hex identity":  func(r *DestinationRequest) { r.SourceIdentity = "managed:sha256:" + strings.Repeat("z", 64) },
		"unknown class":     func(r *DestinationRequest) { r.SourceClass = "new_external" },
	} {
		request := managed
		change(&request)
		if request.Validate() == nil {
			t.Fatal("accepted", name)
		}
	}
	adopt := r
	adopt.SourceIdentity = managed.SourceIdentity
	if adopt.Validate() == nil {
		t.Fatal("adoption accepted a managed identity")
	}
}

func TestDestinationNewManagedStagesProductizedTemplateOnlyOverEmptyGatewayAuth(t *testing.T) {
	f, r, root := destinationFixture(t)
	r = newManagedRequest(r)
	auth := gatewayAuthFixture(f)
	if err := os.MkdirAll(auth, 0o700); err != nil {
		t.Fatal(err)
	}
	login := filepath.Join(auth, "provider.json")
	if err := os.WriteFile(login, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.starter.StageDestination(t.Context(), r); !errors.Is(err, ErrGatewayAuthPresent) {
		t.Fatal("orphaned bootstrap provider logins", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) || f.child.starts != 0 {
		t.Fatal("refused stage had effects")
	}
	if err := os.Remove(login); err != nil {
		t.Fatal(err)
	}
	staged, err := f.starter.StageDestination(t.Context(), r)
	if err != nil || staged.State != journal.StateSucceeded {
		t.Fatal("empty bootstrap auth refused", err)
	}
	// A provider login appearing later never invalidates the durable stage.
	if err = os.WriteFile(login, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if replay, err := f.starter.StageDestination(t.Context(), r); err != nil || !reflect.DeepEqual(replay, staged) {
		t.Fatal("stage replay", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Server struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		} `json:"server"`
		Management struct {
			AllowRemote         bool   `json:"allow-remote"`
			SecretKey           string `json:"secret-key"`
			DisableControlPanel bool   `json:"disable-control-panel"`
		} `json:"management"`
		Access struct {
			Keys []string `json:"api-keys"`
		} `json:"access"`
		OAuth struct {
			AuthDir string `json:"auth-dir"`
		} `json:"oauth"`
		Observability struct {
			Usage struct {
				Enabled bool `json:"usage-statistics-enabled"`
			} `json:"usage"`
		} `json:"observability"`
		Plugins struct {
			Enabled bool   `json:"enabled"`
			Dir     string `json:"dir"`
		} `json:"plugins"`
	}
	// The plugin root is the Supervisor's, beside the destination the publish
	// swaps: the destination directory is replaced whole, so a root inside it
	// would lose every installed plugin on the first re-stage.
	pluginRoot := filepath.Join(filepath.Dir(root), "plugins")
	if json.Unmarshal(data, &config) != nil || config.Server.Port != 8317 || !config.Management.AllowRemote || config.Management.SecretKey != r.ManagementKey ||
		!config.Management.DisableControlPanel || !reflect.DeepEqual(config.Access.Keys, r.APIKeys) || config.OAuth.AuthDir != filepath.Join(root, "auth") ||
		!config.Observability.Usage.Enabled || config.Plugins.Enabled || config.Plugins.Dir != pluginRoot {
		t.Fatalf("New Managed template: %s", data)
	}
	// The immutable stage snapshot is Supervisor-private and holds exactly the
	// one Client Key the Manager persisted before staging.
	stage, err := os.ReadFile(filepath.Join(root, "stage.json"))
	var snapshot DestinationRequest
	if err != nil || json.Unmarshal(stage, &snapshot) != nil || !reflect.DeepEqual(snapshot.APIKeys, r.APIKeys) {
		t.Fatalf("stage snapshot keys: %s (%v)", stage, err)
	}
	// The operation journal is secret-free, including the install key.
	journalBytes, err := os.ReadFile(f.path)
	if err != nil || strings.Contains(string(journalBytes), r.APIKeys[0]) {
		t.Fatal("install client key entered the operation journal")
	}
	info, err := os.Lstat(pluginRoot)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("plugin root: %v %v", info, err)
	}
	// An installed plugin survives the next publish unchanged. The destination is
	// replaced whole, so only a root outside it can keep the plugin.
	installed := filepath.Join(pluginRoot, "acme.plugin")
	if err = os.WriteFile(installed, []byte("plugin"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The template decision is immutable: the Fresh checkpoint is what the
	// Supervisor was given, so a later attempt cannot silently flip it. Changing
	// the switch is the management authority's job, not a re-stage.
	//
	// The assertion must be about the opt-in and nothing else. A supersede is
	// refused for several reasons, so "an error came back" would also be true if
	// the field were ignored entirely. What discriminates is which refusal: a
	// flipped value makes the request differ from the stored one, which is an
	// operation-ID conflict; a field the comparison ignored would leave the
	// requests equal and fall through to the state conflict instead.
	flipped := newManagedRequest(r)
	flipped.OperationID = "fresh-operation:managed-destination-optin"
	flipped.PluginsEnabled = true
	if _, err = f.starter.StageDestination(t.Context(), flipped); !errors.Is(err, journal.ErrOperationIDConflict) {
		t.Fatalf("a flipped opt-in was not refused as a destination change: %v", err)
	}
	// The refusal above is only meaningful if a request that differs in nothing
	// but its operation ID would be admitted to the state gate instead, so the
	// comparison really does read the opt-in.
	unchanged := newManagedRequest(r)
	unchanged.OperationID = "fresh-operation:managed-destination-second"
	if _, err = f.starter.StageDestination(t.Context(), unchanged); errors.Is(err, journal.ErrOperationIDConflict) {
		t.Fatalf("an unchanged re-stage was refused as a destination change: %v", err)
	}
	if data, err = os.ReadFile(installed); err != nil || string(data) != "plugin" {
		t.Fatalf("installed plugin lost across a refused publish: %q %v", data, err)
	}
	if data, err = os.ReadFile(filepath.Join(root, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(data, &config) != nil || config.Plugins.Enabled {
		t.Fatalf("refused stage rewrote the destination: %s", data)
	}
}

// TestDestinationNewManagedPublishesThePersistedPluginOptIn proves the opt-in is
// carried by the staged destination rather than a caller default, so a Runtime
// restart that replays the same snapshot keeps plugins enabled.
func TestDestinationNewManagedPublishesThePersistedPluginOptIn(t *testing.T) {
	f, r, root := destinationFixture(t)
	enabled := newManagedRequest(r)
	enabled.PluginsEnabled = true
	if _, err := f.starter.StageDestination(t.Context(), enabled); err != nil {
		t.Fatal(err)
	}
	var config struct {
		Plugins struct {
			Enabled bool   `json:"enabled"`
			Dir     string `json:"dir"`
		} `json:"plugins"`
	}
	pluginRoot := filepath.Join(filepath.Dir(root), "plugins")
	read := func() {
		data, err := os.ReadFile(filepath.Join(root, "config.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal(data, &config) != nil || !config.Plugins.Enabled || config.Plugins.Dir != pluginRoot {
			t.Fatalf("plugin template: %s", data)
		}
	}
	read()
	// A restarted Supervisor loads the same snapshot and replays the recorded
	// operation instead of deriving a fresh default.
	if replay, err := f.starter.StageDestination(t.Context(), enabled); err != nil || replay.State != journal.StateSucceeded {
		t.Fatal("opt-in replay", err)
	}
	read()
}

func TestDestinationNewManagedRejectsUntrustedGatewayAuth(t *testing.T) {
	f, r, root := destinationFixture(t)
	auth := gatewayAuthFixture(f)
	if err := os.MkdirAll(filepath.Dir(auth), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(auth, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.starter.StageDestination(t.Context(), newManagedRequest(r)); !errors.Is(err, ErrPersistenceUnavailable) {
		t.Fatal("untrusted bootstrap auth accepted", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("refused stage created destination")
	}
	// Adoption destinations keep their existing contract.
	if _, err := f.starter.StageDestination(t.Context(), r); err != nil {
		t.Fatal(err)
	}
}

func TestDestinationStageReplayReopenAndEverySpawnRetainOwnedConfiguration(t *testing.T) {
	f, r, root := destinationFixture(t)
	staged, err := f.starter.StageDestination(t.Context(), r)
	if err != nil || staged.State != journal.StateSucceeded || f.child.starts != 0 {
		t.Fatalf("stage: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(root, "stage.json"))
	if err != nil {
		t.Fatal(err)
	}
	configBefore, err := os.ReadFile(filepath.Join(root, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// An adopted destination is staged by this Supervisor, so it carries the
	// installation's template facts: usage statistics are on, which is what the
	// Key workspace reads back.
	var adopted struct {
		Observability struct {
			Usage struct {
				Enabled bool `json:"usage-statistics-enabled"`
			} `json:"usage"`
		} `json:"observability"`
	}
	if json.Unmarshal(configBefore, &adopted) != nil || !adopted.Observability.Usage.Enabled {
		t.Fatal("adoption destination does not record usage statistics")
	}
	// A lost successful reply replays without replacing files or spawning.
	if op, err := f.starter.StageDestination(t.Context(), r); err != nil || !reflect.DeepEqual(op, staged) {
		t.Fatal("stage replay lost durable operation", err)
	}
	if err = f.starter.Close(); err != nil {
		t.Fatal(err)
	}
	journalBytes, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(journalBytes), r.APIKeys[0]) || strings.Contains(string(journalBytes), r.ManagementKey) {
		t.Fatal("resource secret entered operation journal")
	}
	store, err := journal.Open(t.Context(), f.path, journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewExecutor(journal.Authority{RuntimeIdentity: "runtime-01", RuntimeGeneration: 41}, store, f.child, f.starter.executable)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.artifact = f.observer
	if err = e.EnableDestination(root, gatewayAuthFixture(f), "127.0.0.1:8317", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = e.StageDestination(t.Context(), r); err != nil {
		t.Fatal("reopened stage", err)
	}
	f.child.onStart = func(_ context.Context, spec cpaprocess.StartSpec) error {
		if !reflect.DeepEqual(spec.Args, []string{"-config", filepath.Join(root, "config.yaml")}) {
			t.Error("destination configuration lost on spawn")
		}
		return nil
	}
	start := StartRequest{OperationID: r.OperationID + ":start", ExpectedRuntimeIdentity: r.ExpectedRuntimeIdentity, ExpectedRuntimeGeneration: r.ExpectedRuntimeGeneration, ExpectedActiveArtifactID: r.ExpectedActiveArtifactID}
	if _, err = e.Start(t.Context(), start); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Start(t.Context(), start); err != nil || f.child.starts != 1 {
		t.Fatal("duplicate start spawned again", err)
	}
	if _, err = e.Restart(t.Context(), RestartRequest{OperationID: "later-restart", ExpectedRuntimeIdentity: r.ExpectedRuntimeIdentity, ExpectedRuntimeGeneration: r.ExpectedRuntimeGeneration}); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Stop(t.Context(), StopRequest{OperationID: "later-stop", ExpectedRuntimeIdentity: r.ExpectedRuntimeIdentity, ExpectedRuntimeGeneration: r.ExpectedRuntimeGeneration}); err != nil {
		t.Fatal(err)
	}
	start.OperationID = "later-start"
	if _, err = e.Start(t.Context(), start); err != nil || f.child.starts != 3 {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "stage.json"))
	configAfter, _ := os.ReadFile(filepath.Join(root, "config.yaml"))
	if string(before) != string(after) || string(configBefore) != string(configAfter) {
		t.Fatal("replay rewrote resource state")
	}
	if err = os.WriteFile(f.starter.executable, []byte("replacement"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err = e.startProcess(t.Context(), f.starter.executable); !errors.Is(err, ErrActiveArtifactMismatch) {
		t.Fatal("replacement binary spawned", err)
	}
}

func TestDestinationRejectsConflictsBeforeSourceOrChildEffects(t *testing.T) {
	f, r, root := destinationFixture(t)
	for _, change := range []func(*DestinationRequest){func(r *DestinationRequest) { r.ExpectedRuntimeGeneration++ }, func(r *DestinationRequest) { r.ExpectedRuntimeIdentity = "foreign" }, func(r *DestinationRequest) { r.ExpectedActiveArtifactID = activeArtifactB }} {
		request := r
		change(&request)
		if _, err := f.starter.StageDestination(t.Context(), request); err == nil {
			t.Fatal("unfenced stage accepted")
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatal("conflict created destination")
		}
	}
	if _, err := f.starter.StageDestination(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*DestinationRequest){func(r *DestinationRequest) { r.SourceIdentity = "external:sha256:" + strings.Repeat("d", 64) }, func(r *DestinationRequest) { r.SourceClass = "legacy_managed" }, func(r *DestinationRequest) { r.APIKeys = []string{"different-secret"} }, func(r *DestinationRequest) { r.ManagementKey = strings.Repeat("e", 64) }} {
		request := r
		change(&request)
		if _, err := f.starter.StageDestination(t.Context(), request); !errors.Is(err, journal.ErrOperationIDConflict) {
			t.Fatal("conflicting durable stage replay", err)
		}
	}
	// A new operation ID for the identical destination is no longer a different
	// destination; it is a retry that this attempt has no terminal failure
	// evidence for, so it is rejected as a state conflict instead.
	retried := r
	retried.OperationID = "other-operation"
	if _, err := f.starter.StageDestination(t.Context(), retried); !errors.Is(err, journal.ErrOperationStateConflict) {
		t.Fatal("retry without terminal failure evidence", err)
	}
	if staged, err := f.starter.destination.load(); err != nil || staged == nil || staged.OperationID != r.OperationID {
		t.Fatal("rejected retry replaced the live destination", err)
	}
	start := startRequest("different-artifact-start")
	start.ExpectedActiveArtifactID = activeArtifactB
	if _, err := f.starter.Start(t.Context(), start); !errors.Is(err, ErrActiveArtifactMismatch) {
		t.Fatal("different artifact started staged destination", err)
	}
	if f.child.starts != 0 || f.child.stops != 0 {
		t.Fatal("stage touched lifecycle")
	}
}

func TestDestinationPartialPersistenceNeverResumesUncertainStage(t *testing.T) {
	for _, failure := range []string{"running", "complete"} {
		t.Run(failure, func(t *testing.T) {
			f, r, _ := destinationFixture(t)
			f.journal.failAt = failure
			if _, err := f.starter.StageDestination(t.Context(), r); err == nil {
				t.Fatal("persistence failure succeeded")
			}
			f.journal.failAt = ""
			op, err := f.starter.StageDestination(t.Context(), r)
			if err != nil || op.State == journal.StateSucceeded || f.child.starts != 0 {
				t.Fatal("uncertain stage was resumed or committed", err)
			}
		})
	}
}
