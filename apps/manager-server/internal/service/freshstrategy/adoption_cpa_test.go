package freshstrategy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

const (
	realCPA8BinarySHA256      = "b682e9e42586263f476888361514f68f89ff4ebf89a5dadba394b850b797ce41"
	realCPA8ArchiveSHA256     = "50ecffb47fdd81c8c5a9825a73a7a905ab66342337e274f39c4276b92d3533f3"
	realCPA8Version           = "v8.0.13"
	legacyTransitionOutputKey = "CPAMP_TEST_FRESH_TRANSITION_OUT"
	legacyTransitionHeadKey   = "CPAMP_TEST_FRESH_TRANSITION_HEAD"
	legacyTransitionTreeKey   = "CPAMP_TEST_FRESH_TRANSITION_TREE"
	evidenceSourceHeadKey     = "CPAMP_TEST_FRESH_TRANSITION_SOURCE_HEAD"
	adoptionResourceOutputKey = "CPAMP_TEST_FRESH_RESOURCE_OUT_DIR"
)

// These controlled-source runs are implementation evidence, not an independent
// acceptance. Only the test constructor injects resource/profile qualification.
//
// The legacy_managed case points Fresh at the real pre-v8 process. The legacy
// v0 safe read observes that process; the existing Supervisor StageDestination
// child operation then creates the Manager-owned exact v8 destination, which is
// the staging-v8 transition itself. No second runtime or artificial v8 source
// stands in for the historical source.
func TestAdoptionExactCPAV8WithSupervisor(t *testing.T) {
	cpaPath, supervisorPath := os.Getenv("CPAMP_TEST_CPA_V8"), os.Getenv("CPAMP_TEST_RUNTIME")
	legacyCPAPath := os.Getenv("CPAMP_TEST_CPA_LEGACY")
	if cpaPath == "" || supervisorPath == "" {
		t.Skip("requires exact CPA candidate and built Supervisor; CI runs explicitly")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Fatal("exact evidence requires linux/amd64")
	}
	data, err := os.ReadFile(cpaPath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != realCPA8BinarySHA256 {
		t.Fatal("candidate executable digest mismatch")
	}
	for _, class := range []bootstrap.AdoptSourceClass{bootstrap.AdoptNetworkExisting, bootstrap.AdoptLegacyManaged} {
		t.Run(string(class), func(t *testing.T) {
			root := t.TempDir()
			sourceRoot := filepath.Join(root, "source")
			targetRoot := filepath.Join(root, "target")
			for _, dir := range []string{sourceRoot, targetRoot, filepath.Join(sourceRoot, "auth")} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			sourceAddr, targetAddr, runtimeAddr := freeTestAddress(t), freeTestAddress(t), freeTestAddress(t)
			sourceURL := "http://" + sourceAddr
			sourceKey := "isolated-source-management-key"
			clientKeys := []string{"isolated-client-one", "isolated-client-two"}
			var source *exec.Cmd
			if class == bootstrap.AdoptLegacyManaged {
				if legacyCPAPath == "" {
					t.Fatal("legacy_managed evidence requires pinned historical CPA")
				}
				legacyBytes, err := os.ReadFile(legacyCPAPath)
				if err != nil {
					t.Fatal(err)
				}
				legacyDigest := sha256.Sum256(legacyBytes)
				if hex.EncodeToString(legacyDigest[:]) != cpa.AcceptedLegacyManagedBinarySHA256 {
					t.Fatal("historical CPA executable does not match the accepted legacy source profile")
				}
				if archive := os.Getenv("CPAMP_TEST_CPA_LEGACY_ARCHIVE_SHA256"); archive != "" && archive != cpa.AcceptedLegacyManagedArchiveSHA256 {
					t.Fatal("historical CPA archive does not match the accepted legacy source profile")
				}
				_, sourcePort, _ := net.SplitHostPort(sourceAddr)
				legacyConfig := map[string]any{"host": "127.0.0.1", "port": json.Number(sourcePort), "auth-dir": filepath.Join(sourceRoot, "auth"), "remote-management": map[string]any{"secret-key": sourceKey, "disable-control-panel": true}, "api-keys": clientKeys, "plugins": map[string]any{"enabled": false}}
				configPath := filepath.Join(sourceRoot, "config.yaml")
				writeTestJSON(t, configPath, legacyConfig)
				source = exec.Command(legacyCPAPath, "-config", configPath)
				source.Dir = sourceRoot
				if err = source.Start(); err != nil {
					t.Fatal(err)
				}
				defer killTestProcess(source)
				waitTest(t, func() bool {
					_, err := cpa.ReadLegacyManagedAdoptionSnapshot(t.Context(), sourceURL, sourceKey)
					return err == nil
				})
				// The real v7 source cannot answer the v8 probe; this adapter is
				// explicit, never a v0 fallback from a failed v8 read.
				if _, err := cpa.ReadV8AdoptionSnapshot(t.Context(), sourceURL, sourceKey); err == nil {
					t.Fatal("historical source unexpectedly answered the v8 management read")
				}
				waitClientModels(t, sourceAddr, clientKeys[0])
			} else {
				_, sourcePort, _ := net.SplitHostPort(sourceAddr)
				sourceConfig := map[string]any{"config-version": 8, "server": map[string]any{"host": "127.0.0.1", "port": json.Number(sourcePort)}, "oauth": map[string]any{"auth-dir": filepath.Join(sourceRoot, "auth")}, "management": map[string]any{"secret-key": sourceKey, "disable-control-panel": true, "disable-auto-update-panel": true}, "access": map[string]any{"api-keys": clientKeys}, "plugins": map[string]any{"enabled": false}}
				configPath := filepath.Join(sourceRoot, "config.yaml")
				writeTestJSON(t, configPath, sourceConfig)
				source = exec.Command(cpaPath, "-config", configPath)
				source.Dir = sourceRoot
				if err = source.Start(); err != nil {
					t.Fatal(err)
				}
				defer killTestProcess(source)
				waitTest(t, func() bool {
					_, err := cpa.ReadV8AdoptionKeys(t.Context(), sourceURL, sourceKey)
					return err == nil
				})
			}
			before := directoryDigest(t, sourceRoot)
			manifest := filepath.Join(targetRoot, "artifact.json")
			writeTestJSON(t, manifest, map[string]any{"schemaVersion": 1, "engine": "cpa", "version": realCPA8Version, "artifactId": "sha256:" + realCPA8BinarySHA256})
			token := "isolated-runtime-transport-token"
			supervisor := exec.Command(supervisorPath)
			supervisor.Dir = targetRoot
			supervisor.Env = append(os.Environ(), "CPAMP_RUNTIME_ADDR="+runtimeAddr, "CPAMP_RUNTIME_IDENTITY=isolated-adopt-runtime", "CPAMP_RUNTIME_TOKEN="+token, "CPAMP_RUNTIME_JOURNAL_PATH="+filepath.Join(targetRoot, "operations.sqlite"), "CPAMP_CPA_EXECUTABLE="+cpaPath, "CPAMP_CPA_ARTIFACT_MANIFEST="+manifest, "CPAMP_RUNTIME_CPA_ADDR="+targetAddr)
			if err = supervisor.Start(); err != nil {
				t.Fatal(err)
			}
			defer killTestProcess(supervisor)
			direct := runtimeservice.NewEmbeddedClient("http://"+runtimeAddr, token)
			var status model.RuntimeObservedStatus
			waitTest(t, func() bool { var err error; status, err = direct.Status(t.Context()); return err == nil })
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				current, err := direct.Status(ctx)
				if err == nil {
					_, _ = direct.Stop(ctx, model.RuntimeMutationRequest{OperationID: "test-destination-cleanup", ExpectedRuntimeIdentity: current.Identity, ExpectedRuntimeGeneration: current.Generation})
				}
			}()
			upstream, _ := url.Parse("http://" + runtimeAddr)
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.Proxy = nil
			defer transport.CloseIdleConnections()
			var lostStage, lostStart atomic.Bool
			var stageRequests, startRequests atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				request := r.Clone(r.Context())
				request.URL.Scheme = upstream.Scheme
				request.URL.Host = upstream.Host
				request.RequestURI = ""
				response, err := transport.RoundTrip(request)
				if err != nil {
					w.WriteHeader(502)
					return
				}
				defer response.Body.Close()
				body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
				if r.URL.Path == "/v1/runtime/operations/stage-destination" && response.StatusCode == 200 {
					stageRequests.Add(1)
				}
				if r.URL.Path == "/v1/runtime/operations/start" && response.StatusCode == 200 {
					startRequests.Add(1)
				}
				lose := r.URL.Path == "/v1/runtime/operations/stage-destination" && !lostStage.Swap(true) || r.URL.Path == "/v1/runtime/operations/start" && !lostStart.Swap(true)
				if lose && response.StatusCode == 200 {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(response.StatusCode)
				_, _ = w.Write(body)
			}))
			defer proxy.Close()
			client := runtimeservice.NewEmbeddedClient(proxy.URL, token)
			protector, _ := security.NewProtector([]byte("isolated-exact-cpa-data-key"))
			q := qualified()
			q.StagingV8TransitionQualified = class == bootstrap.AdoptLegacyManaged
			expectedArtifact := model.RuntimeArtifactID("sha256:" + realCPA8BinarySHA256)
			options := Options{Runtime: client, Protector: protector, Qualification: q, QualifiedArtifactID: expectedArtifact, AdoptionResources: acceptedKeys(), DestinationBaseURL: "http://" + targetAddr}
			request := Request{Strategy: bootstrap.AdoptExisting, BaseURL: sourceURL, ManagementKey: sourceKey, AdoptSourceClass: class, ExpectedRuntimeIdentity: status.Identity, ExpectedRuntimeGeneration: status.Generation, ExpectedArtifactID: options.QualifiedArtifactID}
			dbPath := filepath.Join(root, "manager.sqlite")
			db, err := sqlite.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			admin, _ := security.NewAdminCredential("isolated-cpa-adoption-admin-key", "host")
			identity := bootstrap.FreshIdentity{AdmissionID: "real-admission", InstallationID: "real-installation", Generation: "real-generation", DatabasePath: dbPath, OperationID: "real-adopt-operation"}
			store := freshstore.New(db)
			record, err := store.Create(t.Context(), identity, &admin)
			if err != nil {
				t.Fatal(err)
			}
			staged, err := New(options).Execute(t.Context(), record.Operation, request)
			if err != nil {
				t.Fatal(err)
			}
			record, err = store.Update(t.Context(), identity, record.Operation.Revision, func(r *freshstore.Record) error {
				r.Operation.Strategy = staged.Checkpoint
				r.AdoptionCiphertext = staged.AdoptionCiphertext
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			// Reopen actual SQLite before effects, then reconstruct the adapter for
			// every retry. The frozen encrypted payload survives a Manager restart.
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = sqlite.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			store = freshstore.New(db)
			record, err = store.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			request.AdoptionCiphertext = record.AdoptionCiphertext
			stageCallsBefore, startCallsBefore := stageRequests.Load(), startRequests.Load()
			for i := 0; i < 2; i++ {
				if result, err := New(options).Execute(t.Context(), record.Operation, request); err == nil || result.Checkpoint.Stage == "committed" {
					t.Fatal("lost mutation response committed target")
				}
			}
			if record.Operation.Strategy.Stage != "staged" {
				t.Fatal("durable checkpoint committed before verified readback")
			}
			var committed Result
			waitTest(t, func() bool {
				var err error
				committed, err = New(options).Execute(t.Context(), record.Operation, request)
				return err == nil && committed.Checkpoint.Stage == "committed"
			})
			record, err = store.Update(t.Context(), identity, record.Operation.Revision, func(r *freshstore.Record) error {
				r.Operation.Strategy = committed.Checkpoint
				r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: committed.Connection.BaseURL, ManagementKey: committed.Connection.ManagementKeyCiphertext}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = New(options).Execute(t.Context(), record.Operation, request); err != nil {
				t.Fatal("committed replay", err)
			}
			stageCallsCommitted, startCallsCommitted := stageRequests.Load(), startRequests.Load()
			if stageCallsCommitted == stageCallsBefore || startCallsCommitted == startCallsBefore {
				t.Fatal("lost responses were not actually exercised")
			}
			if _, err = New(options).Execute(t.Context(), record.Operation, request); err != nil || stageRequests.Load() != stageCallsCommitted || startRequests.Load() != startCallsCommitted {
				t.Fatal("committed replay mutated destination or changed child operation IDs")
			}
			// Never alias the preserved source with the Manager-owned destination.
			if committed.Connection.BaseURL != "http://"+targetAddr || committed.Connection.BaseURL == sourceURL {
				t.Fatal("destination aliased the preserved source")
			}
			if before != directoryDigest(t, sourceRoot) {
				t.Fatal("source config or native resource state changed")
			}
			if !lostStage.Load() || !lostStart.Load() || !record.Operation.GatewayPending {
				t.Fatal("lost response/Gateway boundary was not exercised")
			}

			// Decrypt the Manager-owned destination snapshot only to verify its
			// bounds: same Fresh child OperationID, new management key, source
			// client keys, exact artifact.
			plainSnapshot, err := protector.UnprotectString(record.AdoptionCiphertext)
			if err != nil {
				t.Fatal(err)
			}
			var snapshot adoptionSnapshot
			if err = json.Unmarshal([]byte(plainSnapshot), &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.SourceManagementKey != sourceKey || snapshot.DestinationURL != "http://"+targetAddr ||
				snapshot.Destination.OperationID != string(identity.OperationID)+":adopt-destination" ||
				snapshot.Destination.SourceClass != string(class) ||
				snapshot.Destination.ExpectedActiveArtifactID != expectedArtifact ||
				snapshot.Destination.ManagementKey == sourceKey || len(snapshot.Destination.ManagementKey) != 64 {
				t.Fatalf("destination snapshot bounds changed: %+v", snapshot.Destination)
			}
			if !reflect.DeepEqual(snapshot.Destination.APIKeys, clientKeys) {
				t.Fatal("destination did not receive the source client API keys")
			}
			destinationKeys, err := cpa.ReadV8AdoptionKeys(t.Context(), "http://"+targetAddr, snapshot.Destination.ManagementKey)
			if err != nil || !reflect.DeepEqual(destinationKeys, clientKeys) {
				t.Fatalf("destination v8 readback: %v %v", destinationKeys, err)
			}
			if err = cpa.VerifyV8Adoption(t.Context(), "http://"+targetAddr, snapshot.Destination.ManagementKey, clientKeys); err != nil {
				t.Fatalf("destination verification: %v", err)
			}
			finalStatus, err := direct.Status(t.Context())
			if err != nil || finalStatus.ActiveGatewayArtifact == nil || finalStatus.ActiveGatewayArtifact.ArtifactID != expectedArtifact ||
				finalStatus.CPAObservedVersion != realCPA8Version || finalStatus.Identity != status.Identity || finalStatus.Generation != status.Generation {
				t.Fatalf("destination is not the accepted exact v8 target: %+v %v", finalStatus, err)
			}
			// The exact v8 destination still serves the v0 compatibility
			// endpoint. The legacy adapter must reject it by declared source
			// profile even though its body is legacy-shaped.
			directHTTP := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
			v0Request, _ := http.NewRequest(http.MethodGet, "http://"+targetAddr+"/v0/management/config", nil)
			v0Request.Header.Set("Authorization", "Bearer "+snapshot.Destination.ManagementKey)
			v0Response, err := directHTTP.Do(v0Request)
			if err != nil || v0Response.StatusCode != http.StatusOK {
				t.Fatalf("exact v8 destination v0 compatibility endpoint unavailable: %v", err)
			}
			_ = v0Response.Body.Close()
			if _, err := cpa.ReadLegacyManagedAdoptionSnapshot(t.Context(), "http://"+targetAddr, snapshot.Destination.ManagementKey); !errors.Is(err, cpa.LegacySourceProfileUnsupported) {
				t.Fatalf("real v8 v0 endpoint was accepted as the accepted legacy source profile: %v", err)
			}
			directHTTP.CloseIdleConnections()
			// Emit the class-specific client-key resource chain while both the
			// source and the exact v8 destination are still live and verified.
			// It is separate from the legacy transition chain.
			writeAdoptionResourceEvidence(t, class, record, finalStatus, expectedArtifact, sourceURL, sourceKey, clientKeys, snapshot, legacyCPAPath)

			if class == bootstrap.AdoptLegacyManaged {
				// The historical v7 process remains live, byte-preserved and on v0.
				waitClientModels(t, sourceAddr, clientKeys[0])
				legacy, err := cpa.ReadLegacyManagedAdoptionSnapshot(t.Context(), sourceURL, sourceKey)
				if err != nil || !reflect.DeepEqual(legacy.APIKeys, clientKeys) {
					t.Fatalf("legacy source no longer readable or preserved: %v", err)
				}
				directHTTP := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
				response, err := directHTTP.Get(sourceURL + "/v8/management/config")
				if err != nil || response.StatusCode != http.StatusNotFound {
					t.Fatalf("historical source answered v8 management: %v", err)
				}
				_ = response.Body.Close()
				directHTTP.CloseIdleConnections()
				if before != directoryDigest(t, sourceRoot) {
					t.Fatal("legacy source changed during transition")
				}
			}

			if _, err = direct.Stop(t.Context(), model.RuntimeMutationRequest{OperationID: "test-restart-owned-target", ExpectedRuntimeIdentity: status.Identity, ExpectedRuntimeGeneration: status.Generation}); err != nil {
				t.Fatal(err)
			}
			restarted := exec.Command(supervisorPath)
			restarted.Dir = supervisor.Dir
			restarted.Env = supervisor.Env
			killTestProcess(supervisor)
			if err = restarted.Start(); err != nil {
				t.Fatal(err)
			}
			defer killTestProcess(restarted)
			waitTest(t, func() bool {
				current, err := direct.Status(t.Context())
				return err == nil && current.Generation != status.Generation
			})
			if _, err = New(options).Execute(t.Context(), record.Operation, request); err == nil {
				t.Fatal("new Supervisor incarnation accepted the old Runtime generation")
			}
			if before != directoryDigest(t, sourceRoot) {
				t.Fatal("Supervisor restart changed source")
			}
			if class == bootstrap.AdoptLegacyManaged {
				keys, err := cpa.ReadLegacyManagedAdoptionSnapshot(t.Context(), sourceURL, sourceKey)
				if err != nil || !reflect.DeepEqual(keys.APIKeys, clientKeys) {
					t.Fatal("qualified legacy source no longer usable")
				}
				// Emit the sanitized manifest/report/observation evidence only
				// after every transition, preservation and restart-fence
				// assertion passed.
				legacyBytes, err := os.ReadFile(legacyCPAPath)
				if err != nil {
					t.Fatal(err)
				}
				writeLegacyTransitionEvidence(t, record, finalStatus, expectedArtifact, legacyBytes)
			} else {
				keys, err := cpa.ReadV8AdoptionKeys(t.Context(), sourceURL, sourceKey)
				if err != nil || len(keys) != 2 || keys[0] != clientKeys[0] || keys[1] != clientKeys[1] {
					t.Fatal("qualified staging source no longer usable")
				}
			}
			t.Logf("exact artifact=%s class=%s source-preserved=true destination-readback=true destination-verified=true sqlite-reopen=true lost-stage-reply=true lost-start-reply=true runtime-restart-fenced=true production-acceptance=pending", options.QualifiedArtifactID, class)
		})
	}
}

// writeLegacyTransitionEvidence emits the sanitized, content-addressed CI
// evidence only when the workflow asks for it. It is review input, never
// compiled production authority, and it never writes consumer.json.
func writeLegacyTransitionEvidence(t *testing.T, record freshstore.Record, status model.RuntimeObservedStatus, artifact model.RuntimeArtifactID, legacyBinary []byte) {
	t.Helper()
	name := os.Getenv(legacyTransitionOutputKey)
	if name == "" {
		return
	}
	if strings.TrimSpace(name) != name {
		t.Fatal("invalid transition evidence path")
	}
	head, tree := os.Getenv(legacyTransitionHeadKey), os.Getenv(legacyTransitionTreeKey)
	if !validEvidenceDigest(head, 40) || !validEvidenceDigest(tree, 40) {
		t.Fatal("transition evidence requires exact tested commit and tree")
	}
	legacyDigest := sha256.Sum256(legacyBinary)
	if hex.EncodeToString(legacyDigest[:]) != cpa.AcceptedLegacyManagedBinarySHA256 {
		t.Fatal("historical CPA executable does not match the accepted legacy source profile")
	}
	manifestName, manifestData, manifest := embeddedLegacyTransitionManifest(t)
	if manifest.SchemaVersion != 1 || manifest.ContractID != "cpamp-cpa-v8-qualification" ||
		manifest.Candidate.DeclaredSHA256 != realCPA8ArchiveSHA256 || manifest.Candidate.ArtifactID != string(artifact) ||
		manifest.LegacySource.Version != cpa.AcceptedLegacyManagedVersion ||
		manifest.LegacySource.ArchiveSHA256 != cpa.AcceptedLegacyManagedArchiveSHA256 ||
		manifest.LegacySource.BinarySHA256 != cpa.AcceptedLegacyManagedBinarySHA256 ||
		len(manifest.Cases) != 1 || manifest.Cases[0].ID != acceptedLegacyTransitionCaseRef {
		t.Fatalf("checked-in transition manifest does not bind this run: %s", manifestName)
	}
	stagingV8Verified := status.CPAObservedVersion == realCPA8Version && status.ActiveGatewayArtifact != nil && status.ActiveGatewayArtifact.ArtifactID == artifact
	if !stagingV8Verified || !record.Operation.GatewayPending {
		t.Fatal("transition evidence requires a verified exact v8 destination and pending Gateway")
	}
	observationData, reportData, authorityData := legacyTransitionEvidencePayload(t, manifestData, manifest, artifact,
		stagingV8Verified, record.Operation.GatewayPending, head, tree, os.Getenv(evidenceSourceHeadKey))
	directory := filepath.Dir(name)
	if err := os.WriteFile(name, observationData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "fresh-legacy-transition-report.json"), reportData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "fresh-legacy-transition-manifest.json"), manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "fresh-legacy-transition-authority.json"), authorityData, 0o600); err != nil {
		t.Fatal(err)
	}
}

// legacyTransitionEvidencePayload builds the exact sanitized CI bytes without
// touching the filesystem, so regression tests can feed the same generator
// output back into the production loader.
func legacyTransitionEvidencePayload(t *testing.T, manifestData []byte, manifest legacyTransitionManifest, artifact model.RuntimeArtifactID, stagingV8Verified, gatewayPending bool, head, tree, sourceHead string) ([]byte, []byte, []byte) {
	t.Helper()
	observation := map[string]any{
		"schemaVersion":                1,
		"caseRef":                      acceptedLegacyTransitionCaseRef,
		"sourceClass":                  string(bootstrap.AdoptLegacyManaged),
		"legacyVersion":                cpa.AcceptedLegacyManagedVersion,
		"legacyArchiveSha256":          cpa.AcceptedLegacyManagedArchiveSHA256,
		"legacyBinarySha256":           cpa.AcceptedLegacyManagedBinarySHA256,
		"targetVersion":                realCPA8Version,
		"targetArchiveSha256":          realCPA8ArchiveSHA256,
		"targetBinarySha256":           realCPA8BinarySHA256,
		"targetArtifactId":             string(artifact),
		"consumerImplementationSha256": consumerImplementationSHA256(),
		"sourcePreserved":              true,
		"legacyReadVerified":           true,
		"stagingV8Verified":            stagingV8Verified,
		"destinationVerified":          true,
		"gatewayPending":               gatewayPending,
		"sourceHead":                   sourceHead,
		"testedCommit":                 head,
		"testedTree":                   tree,
	}
	observationData := encodeJSON(t, observation)
	observationSHA := sha256Hex(observationData)
	report := map[string]any{
		"schemaVersion":    1,
		"contractId":       manifest.ContractID,
		"manifestRevision": manifest.Revision,
		"manifestSha256":   sha256Hex(manifestData),
		"run":              map[string]any{"id": "cpamp-fresh-legacy-transition-" + head[:12], "runnerCommit": head, "testedTree": tree},
		"artifactObservation": map[string]any{
			"candidateRef":          manifest.Candidate.ID,
			"provenance":            "locally-verified",
			"observedArchiveSha256": realCPA8ArchiveSHA256,
			"observedBinarySha256":  realCPA8BinarySHA256,
		},
		"results": []any{map[string]any{
			"caseRef": acceptedLegacyTransitionCaseRef, "executionState": "completed",
			"assertionOutcome": "pass", "capability": "supported",
			"evidenceRefs": []string{acceptedLegacyTransitionCaseRef},
		}},
		"evidence": []any{map[string]any{"id": acceptedLegacyTransitionCaseRef, "kind": "sanitized-trace", "sha256": observationSHA}},
	}
	reportData := encodeJSON(t, report)
	reportSHA := sha256Hex(reportData)
	authority := map[string]any{
		"schemaVersion": 1,
		"caseRef":       acceptedLegacyTransitionCaseRef,
		"profile":       acceptedLegacyTransitionProfile,
		"manifest":      map[string]any{"file": "fresh-legacy-transition-manifest.json", "sha256": sha256Hex(manifestData)},
		"report":        map[string]any{"file": "fresh-legacy-transition-report.json", "sha256": reportSHA},
		"observation":   map[string]any{"file": "fresh-legacy-transition.json", "sha256": observationSHA},
		"legacyVersion": cpa.AcceptedLegacyManagedVersion, "legacyArchiveSha256": cpa.AcceptedLegacyManagedArchiveSHA256,
		"legacyBinarySha256":   cpa.AcceptedLegacyManagedBinarySHA256,
		"targetArtifactId":     string(artifact),
		"productionAcceptance": "pending",
	}
	return observationData, reportData, encodeJSON(t, authority)
}

// writeAdoptionResourceEvidence emits the class-specific client-key resource
// chain: manifest copy, report and three distinct sanitized traces. It is
// review input, never compiled authority, and never writes consumer.json.
func writeAdoptionResourceEvidence(t *testing.T, class bootstrap.AdoptSourceClass, record freshstore.Record, status model.RuntimeObservedStatus, artifact model.RuntimeArtifactID, sourceURL, sourceKey string, clientKeys []string, snapshot adoptionSnapshot, legacyCPAPath string) {
	t.Helper()
	directory := strings.TrimSpace(os.Getenv(adoptionResourceOutputKey))
	if directory == "" {
		return
	}
	if strings.TrimSpace(directory) != directory {
		t.Fatal("invalid resource evidence path")
	}
	head, tree := os.Getenv(legacyTransitionHeadKey), os.Getenv(legacyTransitionTreeKey)
	if !validEvidenceDigest(head, 40) || !validEvidenceDigest(tree, 40) {
		t.Fatal("resource evidence requires exact tested commit and tree")
	}
	manifestName, manifestData, manifest := embeddedAdoptionResourceManifest(t, class)
	if manifest.Candidate.ArtifactID != string(artifact) || manifest.Candidate.DeclaredSHA256 != realCPA8ArchiveSHA256 ||
		manifest.SourceClass != class || manifest.SchemaVersion != 1 || manifest.ContractID != adoptionResourceContract {
		t.Fatalf("checked-in resource manifest does not bind this run: %s", manifestName)
	}
	switch class {
	case bootstrap.AdoptNetworkExisting:
		if manifest.SourceProfile != adoptionResourceSourceCPAV8 || manifest.LegacySource != nil {
			t.Fatal("network resource manifest source drifted")
		}
	case bootstrap.AdoptLegacyManaged:
		legacyBytes, err := os.ReadFile(legacyCPAPath)
		if err != nil {
			t.Fatal(err)
		}
		legacyDigest := sha256.Sum256(legacyBytes)
		if hex.EncodeToString(legacyDigest[:]) != cpa.AcceptedLegacyManagedBinarySHA256 ||
			manifest.LegacySource == nil || manifest.LegacySource.Version != cpa.AcceptedLegacyManagedVersion ||
			manifest.LegacySource.ArchiveSHA256 != cpa.AcceptedLegacyManagedArchiveSHA256 ||
			manifest.LegacySource.BinarySHA256 != cpa.AcceptedLegacyManagedBinarySHA256 {
			t.Fatal("legacy resource manifest does not bind the pinned source profile")
		}
	}
	var keys []string
	switch class {
	case bootstrap.AdoptNetworkExisting:
		observation, err := cpa.ReadV8AdoptionSnapshot(t.Context(), sourceURL, sourceKey)
		if err != nil {
			t.Fatal(err)
		}
		keys = observation.APIKeys
	case bootstrap.AdoptLegacyManaged:
		observation, err := cpa.ReadLegacyManagedAdoptionSnapshot(t.Context(), sourceURL, sourceKey)
		if err != nil {
			t.Fatal(err)
		}
		keys = observation.APIKeys
	}
	if !slices.Equal(keys, clientKeys) {
		t.Fatal("resource read evidence does not match the observed source keys")
	}
	read := adoptionResourceReadTrace{ReadVerified: true, ClientKeyCount: len(keys)}
	if class == bootstrap.AdoptNetworkExisting {
		read.ManagementGeneration = "v8"
	} else {
		read.LegacyVersion = cpa.AcceptedLegacyManagedVersion
		read.LegacyArchiveSHA256 = cpa.AcceptedLegacyManagedArchiveSHA256
		read.LegacyBinarySHA256 = cpa.AcceptedLegacyManagedBinarySHA256
	}
	if !slices.Equal(snapshot.Destination.APIKeys, clientKeys) || snapshot.Destination.ExpectedActiveArtifactID != artifact ||
		snapshot.Destination.SourceClass != string(class) {
		t.Fatal("resource replay evidence does not match the staged destination")
	}
	replay := adoptionResourceReplayTrace{
		ReplayVerified: true, TargetArtifactID: string(artifact), TargetArchiveSHA256: realCPA8ArchiveSHA256,
		TargetBinarySHA256: realCPA8BinarySHA256, SourcePreserved: true, GatewayPending: record.Operation.GatewayPending,
	}
	destinationKeys, err := cpa.ReadV8AdoptionKeys(t.Context(), snapshot.DestinationURL, snapshot.Destination.ManagementKey)
	if err != nil || !slices.Equal(destinationKeys, clientKeys) {
		t.Fatalf("resource readback evidence unavailable: %v", err)
	}
	if err = cpa.VerifyV8Adoption(t.Context(), snapshot.DestinationURL, snapshot.Destination.ManagementKey, clientKeys); err != nil {
		t.Fatalf("resource effective-auth evidence unavailable: %v", err)
	}
	if status.ActiveGatewayArtifact == nil || status.ActiveGatewayArtifact.ArtifactID != artifact {
		t.Fatal("resource verify evidence requires the exact observed artifact")
	}
	verify := adoptionResourceVerifyTrace{
		ReadbackVerified: true, EffectiveAuthVerified: true, DestinationVerified: true,
		TargetArtifactID: string(artifact), TargetArchiveSHA256: realCPA8ArchiveSHA256,
		TargetBinarySHA256: realCPA8BinarySHA256, GatewayPending: record.Operation.GatewayPending,
	}
	readData, replayData, verifyData, reportData, authorityData := adoptionResourceEvidencePayload(t, manifestData, manifest, class, artifact, read, replay, verify, head, tree)
	short := "network"
	if class == bootstrap.AdoptLegacyManaged {
		short = "legacy"
	}
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("fresh-adoption-resource-"+short+"-read.json", readData)
	write("fresh-adoption-resource-"+short+"-replay.json", replayData)
	write("fresh-adoption-resource-"+short+"-verify.json", verifyData)
	write("fresh-adoption-resource-"+short+"-report.json", reportData)
	write("fresh-adoption-resource-"+short+"-manifest.json", manifestData)
	write("fresh-adoption-resource-"+short+"-authority.json", authorityData)
}

// adoptionResourceEvidencePayload builds the exact sanitized resource evidence
// bytes without filesystem access, so tests can feed the generator output back
// into the production loader.
func adoptionResourceEvidencePayload(t *testing.T, manifestData []byte, manifest adoptionResourceManifest, class bootstrap.AdoptSourceClass, artifact model.RuntimeArtifactID, read adoptionResourceReadTrace, replay adoptionResourceReplayTrace, verify adoptionResourceVerifyTrace, head, tree string) ([]byte, []byte, []byte, []byte, []byte) {
	t.Helper()
	short := "network"
	if class == bootstrap.AdoptLegacyManaged {
		short = "legacy"
	}
	runID := "cpamp-fresh-adoption-resource-" + short + "-" + head[:12]
	implementation := consumerImplementationSHA256()
	read.SchemaVersion, read.RunID, read.CaseRef, read.SourceClass, read.ConsumerImplementationSHA256 = 1, runID, manifest.Cases[0].ID, string(class), implementation
	replay.SchemaVersion, replay.RunID, replay.CaseRef, replay.SourceClass, replay.ConsumerImplementationSHA256 = 1, runID, manifest.Cases[1].ID, string(class), implementation
	verify.SchemaVersion, verify.RunID, verify.CaseRef, verify.SourceClass, verify.ConsumerImplementationSHA256 = 1, runID, manifest.Cases[2].ID, string(class), implementation
	readData := encodeJSON(t, read)
	replayData := encodeJSON(t, replay)
	verifyData := encodeJSON(t, verify)
	report := evidenceReport{
		SchemaVersion: 1, ContractID: manifest.ContractID, ManifestRevision: manifest.Revision,
		ManifestSHA256: sha256Hex(manifestData),
		Run:            evidenceRun{ID: runID, RunnerCommit: head, TestedTree: tree},
		ArtifactObservation: evidenceArtifactObservation{
			CandidateRef: manifest.Candidate.ID, Provenance: "locally-verified",
			ObservedArchiveSHA256: realCPA8ArchiveSHA256, ObservedBinarySHA256: realCPA8BinarySHA256,
		},
		Results: []evidenceResult{
			{CaseRef: read.CaseRef, ExecutionState: "completed", AssertionOutcome: "pass", Capability: "supported", EvidenceRefs: []string{read.CaseRef}},
			{CaseRef: replay.CaseRef, ExecutionState: "completed", AssertionOutcome: "pass", Capability: "supported", EvidenceRefs: []string{replay.CaseRef}},
			{CaseRef: verify.CaseRef, ExecutionState: "completed", AssertionOutcome: "pass", Capability: "supported", EvidenceRefs: []string{verify.CaseRef}},
		},
		Evidence: []evidenceTraceRef{
			{ID: read.CaseRef, Kind: "sanitized-trace", SHA256: sha256Hex(readData)},
			{ID: replay.CaseRef, Kind: "sanitized-trace", SHA256: sha256Hex(replayData)},
			{ID: verify.CaseRef, Kind: "sanitized-trace", SHA256: sha256Hex(verifyData)},
		},
	}
	reportData := encodeJSON(t, report)
	reportSHA := sha256Hex(reportData)
	authority := map[string]any{
		"schemaVersion": 1, "profile": adoptionResourceProfile, "sourceClass": string(class),
		"caseRefs": []string{read.CaseRef, replay.CaseRef, verify.CaseRef},
		"manifest": map[string]any{"file": "fresh-adoption-resource-" + short + "-manifest.json", "sha256": sha256Hex(manifestData)},
		"report":   map[string]any{"file": "fresh-adoption-resource-" + short + "-report.json", "sha256": reportSHA},
		"traces": map[string]any{
			"read":   map[string]any{"file": "fresh-adoption-resource-" + short + "-read.json", "sha256": sha256Hex(readData)},
			"replay": map[string]any{"file": "fresh-adoption-resource-" + short + "-replay.json", "sha256": sha256Hex(replayData)},
			"verify": map[string]any{"file": "fresh-adoption-resource-" + short + "-verify.json", "sha256": sha256Hex(verifyData)},
		},
		"targetArtifactId": string(artifact), "legacyVersion": cpa.AcceptedLegacyManagedVersion,
		"legacyArchiveSha256":  cpa.AcceptedLegacyManagedArchiveSHA256,
		"legacyBinarySha256":   cpa.AcceptedLegacyManagedBinarySHA256,
		"productionAcceptance": "pending",
	}
	return readData, replayData, verifyData, reportData, encodeJSON(t, authority)
}

func validEvidenceDigest(value string, length int) bool {
	if len(value) != length {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func freeTestAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	_ = l.Close()
	return address
}
func writeTestJSON(t *testing.T, name string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
func killTestProcess(command *exec.Cmd) {
	if command.Process != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
	}
}
func waitClientModels(t *testing.T, address, key string) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	waitTest(t, func() bool {
		request, _ := http.NewRequest(http.MethodGet, "http://"+address+"/v1/models", nil)
		request.Header.Set("Authorization", "Bearer "+key)
		response, err := client.Do(request)
		if err != nil {
			return false
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		return response.StatusCode == http.StatusOK
	})
}

func waitTest(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("controlled CPA/Runtime observation did not become available")
}
func directoryDigest(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, _ := filepath.Rel(root, name)
		_, _ = io.WriteString(h, relative)
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		_, _ = h.Write(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}
