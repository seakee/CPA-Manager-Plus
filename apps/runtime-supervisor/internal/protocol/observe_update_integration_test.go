package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

type observationGatedJournal struct {
	*journal.Store
	afterBegin func()
}

func (j *observationGatedJournal) Begin(ctx context.Context, authority journal.Authority, intent journal.Intent) (journal.Operation, bool, error) {
	operation, created, err := j.Store.Begin(ctx, authority, intent)
	if err == nil && created && j.afterBegin != nil {
		j.afterBegin()
	}
	return operation, created, err
}

type observationGatedPreparer struct {
	resolve func(context.Context, string) (runtimeupdate.Release, error)
	stage   func(context.Context, runtimeupdate.Release) (runtimeupdate.Metadata, error)
}

func (p observationGatedPreparer) Resolve(ctx context.Context, version string) (runtimeupdate.Release, error) {
	return p.resolve(ctx, version)
}

func (p observationGatedPreparer) Stage(ctx context.Context, release runtimeupdate.Release) (runtimeupdate.Metadata, error) {
	return p.stage(ctx, release)
}

func TestObserveUpdateOperationAfterHTTPResponseLoss(t *testing.T) {
	for _, afterBegin := range []bool{false, true} {
		name := "before durable Begin"
		if afterBegin {
			name = "after durable Begin"
		}
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			store, err := journal.Open(t.Context(), filepath.Join(directory, "operations.sqlite"), journal.Options{})
			if err != nil {
				t.Fatal(err)
			}
			executable := filepath.Join(directory, "inactive-cpa")
			if err := os.WriteFile(executable, []byte("test artifact; never executed"), 0o600); err != nil {
				t.Fatal(err)
			}
			observer := artifact.NewObserver(executable, "")
			_ = observer.Refresh()
			if observer.Observation() == nil {
				t.Fatal("missing exact artifact identity")
			}
			storeGate := &observationGatedJournal{Store: store}
			executor, err := lifecycle.NewExecutor(journal.Authority{RuntimeIdentity: "runtime-01", RuntimeGeneration: 7},
				storeGate, cpaprocess.NewManager(nil), executable)
			if err != nil {
				t.Fatal(err)
			}
			entered, releaseBegin := make(chan struct{}), make(chan struct{})
			stageEntered, releaseStage := make(chan struct{}), make(chan struct{})
			var beginOnce, stageOnce sync.Once
			unblockBegin := func() { beginOnce.Do(func() { close(releaseBegin) }) }
			unblockStage := func() { stageOnce.Do(func() { close(releaseStage) }) }
			var releaseCalls, stageCalls atomic.Int32
			preparer := observationGatedPreparer{
				resolve: func(ctx context.Context, version string) (runtimeupdate.Release, error) {
					releaseCalls.Add(1)
					if !afterBegin {
						close(entered)
						<-ctx.Done()
						return runtimeupdate.Release{}, ctx.Err()
					}
					return runtimeupdate.Release{Version: version}, nil
				},
				stage: func(context.Context, runtimeupdate.Release) (runtimeupdate.Metadata, error) {
					stageCalls.Add(1)
					close(stageEntered)
					<-releaseStage
					return runtimeupdate.Metadata{Version: "7.3.4"}, nil
				},
			}
			if afterBegin {
				storeGate.afterBegin = func() { close(entered); <-releaseBegin }
			}
			if err := executor.EnablePrepareUpdate(observer, preparer); err != nil {
				t.Fatal(err)
			}
			handler, err := NewHandler(Config{
				RuntimeIdentity: "runtime-01", RuntimeGeneration: 7, Token: testRuntimeToken,
				PrepareUpdate: executor, ObserveUpdate: executor,
			})
			if err != nil {
				t.Fatal(err)
			}
			mutationDone := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == prepareUpdatePath {
					defer close(mutationDone)
				}
				handler.ServeHTTP(w, r)
			}))
			server.Client().Timeout = 2 * time.Second
			defer func() {
				unblockBegin()
				unblockStage()
				server.Close()
				_ = executor.Close()
			}()
			caller, disconnect := context.WithCancel(t.Context())
			defer disconnect()
			body, err := json.Marshal(prepareUpdateRequest{
				OperationID: "manager-op", ExpectedRuntimeIdentity: "runtime-01", ExpectedRuntimeGeneration: 7,
				ExpectedActiveArtifactID: observer.Observation().ArtifactID, TargetVersion: "7.3.4",
			})
			if err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequestWithContext(caller, http.MethodPost, server.URL+prepareUpdatePath, bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer "+testRuntimeToken)
			clientDone := make(chan error, 1)
			go func() {
				response, err := server.Client().Do(request)
				if response != nil {
					response.Body.Close()
				}
				clientDone <- err
			}()
			waitObservationSignal(t, entered)
			disconnect()
			select {
			case err := <-clientDone:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("lost HTTP response = %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("HTTP client did not disconnect")
			}
			observe := func(want journal.State) {
				t.Helper()
				before, beforeErr := store.Get(t.Context(), "runtime-01", "manager-op")
				query, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+validObserveUpdateTarget(), nil)
				if err != nil {
					t.Fatal(err)
				}
				query.Header.Set("Authorization", "Bearer "+testRuntimeToken)
				response, err := server.Client().Do(query)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if response.Header.Get("Cache-Control") != "no-store" {
					t.Fatal("observation response was cacheable")
				}
				if want == "" {
					var envelope errorResponse
					if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
						t.Fatal(err)
					}
					if response.StatusCode != http.StatusNotFound || envelope.Error.Code != "operation_not_found" ||
						!errors.Is(beforeErr, journal.ErrOperationNotFound) {
						t.Fatalf("before-Begin query = %d %+v, %v", response.StatusCode, envelope, beforeErr)
					}
				} else {
					var got observedOperationResponse
					if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
						t.Fatal(err)
					}
					if response.StatusCode != http.StatusOK || got.State != want || beforeErr != nil ||
						!got.CreatedAt.Equal(before.CreatedAt) || !got.UpdatedAt.Equal(before.UpdatedAt) {
						t.Fatalf("retained query = %d %+v, %v", response.StatusCode, got, beforeErr)
					}
				}
				after, afterErr := store.Get(t.Context(), "runtime-01", "manager-op")
				if !reflect.DeepEqual(before, after) || (beforeErr == nil) != (afterErr == nil) {
					t.Fatal("HTTP observation changed durable evidence")
				}
			}
			if !afterBegin {
				waitObservationSignal(t, mutationDone)
				observe("")
				if stageCalls.Load() != 0 {
					t.Fatal("observation started a missing prepare")
				}
			} else {
				observe(journal.StateAccepted)
				unblockBegin()
				waitObservationSignal(t, stageEntered)
				observe(journal.StateRunning)
				unblockStage()
				waitObservationSignal(t, mutationDone)
				observe(journal.StateSucceeded)
				if stageCalls.Load() != 1 {
					t.Fatal("observation repeated staging")
				}
			}
			if releaseCalls.Load() != 1 {
				t.Fatal("observation performed release discovery")
			}
		})
	}
}

func waitObservationSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for operation boundary")
	}
}
