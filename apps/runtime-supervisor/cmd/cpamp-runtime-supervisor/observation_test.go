package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
)

func TestRuntimeObservesRetainedUpdatesAcrossSupervisorRestart(t *testing.T) {
	cfg := loadTestConfig(t, fixedGeneration(41))
	cfg.journalPath = filepath.Join(t.TempDir(), "supervisor", "operations.sqlite")
	cfg.cpaExecutable = filepath.Join(t.TempDir(), "not-started-cpa")
	cfg.cpaAddr = unreadyCPAAddr(t)
	store, err := journal.Open(t.Context(), cfg.journalPath, journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	authority := journal.Authority{RuntimeIdentity: cfg.runtimeIdentity, RuntimeGeneration: 41}
	retained := make(map[string]journal.Operation)
	for _, operationType := range []string{"prepare_update", "activate_update"} {
		for _, state := range []journal.State{journal.StateAccepted, journal.StateRunning, journal.StateSucceeded, journal.StateFailed} {
			id := operationType + "-" + string(state)
			operation, _, err := store.Begin(t.Context(), authority, journal.Intent{
				OperationID: id, OperationType: operationType,
				ExpectedRuntimeIdentity: authority.RuntimeIdentity, ExpectedRuntimeGeneration: 41,
				RequestFingerprint: sha256.Sum256([]byte("runtime." + operationType + "/v1:{targetVersion:7.3.4}")),
			})
			if err != nil {
				t.Fatal(err)
			}
			switch state {
			case journal.StateRunning:
				operation, err = store.MarkRunning(t.Context(), authority.RuntimeIdentity, id)
			case journal.StateSucceeded:
				operation, err = store.Complete(t.Context(), authority.RuntimeIdentity, id, state, "")
			case journal.StateFailed:
				operation, err = store.Complete(t.Context(), authority.RuntimeIdentity, id, state, "staging_failed")
			}
			if err != nil {
				t.Fatal(err)
			}
			if state == journal.StateSucceeded || state == journal.StateFailed {
				operation, err = store.Tombstone(t.Context(), authority.RuntimeIdentity, id)
				if err != nil {
					t.Fatal(err)
				}
			}
			retained[id] = operation
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Rebuild the real Supervisor handler twice from the same identity and
	// journal, with a different opaque authority epoch on restart.
	for _, generation := range []uint64{41, 7} {
		t.Run(fmt.Sprint(generation), func(t *testing.T) {
			cfg.runtimeGeneration = generation
			runtime, err := newRuntimeHandler(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			dsn := (&url.URL{Scheme: "file", Path: cfg.journalPath, RawQuery: "mode=ro"}).String()
			reader, err := sql.Open("sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			reader.SetMaxOpenConns(1)
			snapshot := func() [2]int {
				t.Helper()
				var values [2]int
				if err := reader.QueryRow("pragma data_version").Scan(&values[0]); err != nil {
					t.Fatal(err)
				}
				if err := reader.QueryRow("select count(*) from operations").Scan(&values[1]); err != nil {
					t.Fatal(err)
				}
				return values
			}
			before := snapshot()
			for _, operation := range retained {
				query := url.Values{
					"operationId": {operation.OperationID}, "operationType": {operation.OperationType},
					"expectedRuntimeIdentity": {cfg.runtimeIdentity}, "expectedRuntimeGeneration": {fmt.Sprint(generation)},
					"targetVersion": {"7.3.4"},
				}
				for repeat := 0; repeat < 2; repeat++ {
					request := httptest.NewRequest(http.MethodGet, "/v1/runtime/operations/update?"+query.Encode(), nil)
					request.Header.Set("Authorization", "Bearer "+cfg.token)
					response := httptest.NewRecorder()
					runtime.ServeHTTP(response, request)
					if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
						t.Fatalf("observation response = %d %s", response.Code, response.Body.String())
					}
					var got struct {
						OperationID          string
						OperationType        string
						RuntimeIdentity      string
						RuntimeGeneration    uint64
						State                journal.State
						Error                *struct{ Code, Message string }
						CreatedAt, UpdatedAt time.Time
						CompletedAt          *time.Time
					}
					if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if got.OperationID != operation.OperationID || got.OperationType != operation.OperationType ||
						got.RuntimeIdentity != cfg.runtimeIdentity || got.RuntimeGeneration != 41 || got.State != operation.State ||
						!got.CreatedAt.Equal(operation.CreatedAt) || !got.UpdatedAt.Equal(operation.UpdatedAt) ||
						!reflect.DeepEqual(got.CompletedAt, operation.CompletedAt) {
						t.Fatalf("retained evidence = %+v, want %+v", got, operation)
					}
					if (got.Error != nil) != (operation.State == journal.StateFailed) ||
						got.Error != nil && got.Error.Code != operation.FailureCode {
						t.Fatalf("failure evidence = %+v", got.Error)
					}
					for _, forbidden := range []string{cfg.token, cfg.journalPath, cfg.cpaExecutable, "fingerprint", "tombstone"} {
						if strings.Contains(response.Body.String(), forbidden) {
							t.Fatalf("observation exposed %q", forbidden)
						}
					}
				}
			}
			if after := snapshot(); after != before || after[1] != len(retained) {
				t.Fatalf("observation committed journal changes: before=%v after=%v", before, after)
			}
		})
	}
}
