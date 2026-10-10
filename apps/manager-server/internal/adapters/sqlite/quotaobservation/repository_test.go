package quotaobservation

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/resourcepolicy"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/quotaobservation"
	sqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestReadSnapshotPreventsMixedProjectionAndPolicy(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "snapshot.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seed := []string{
		`insert into usage_events(id,event_hash,timestamp_ms,timestamp,model,created_at_ms)
		 values (1,'source',100,'2026-01-01T00:00:00Z','model',1)`,
		`insert into gateway_api_key_identities(id,revision,lifecycle,created_at_ms,updated_at_ms)
		 values ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',1,'active',1,1)`,
		`insert into gateway_quota_policies(id,revision,state,enforcement,action,created_at_ms,updated_at_ms)
		 values ('cccccccccccccccccccccccccccccccc',1,'active','observed','notify',1,1)`,
		`insert into gateway_quota_policy_rules(policy_id,metric,limit_value,window_kind,duration_ms,timezone)
		 values ('cccccccccccccccccccccccccccccccc','request',2,'rolling',100,'')`,
		`insert into gateway_api_key_policy_bindings(api_key_id,policy_id,revision,enabled,created_at_ms,updated_at_ms)
		 values ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','cccccccccccccccccccccccccccccccc',1,1,1,1)`,
		`insert into gateway_usage_identity_projection_v1
		 (usage_event_id,event_hash,evidence_timestamp_ms,api_key_state,api_key_id,credential_state,schema_version,projected_at_ms)
		 values (1,'source',100,'mapped','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','unknown',1,1)`,
		`update gateway_usage_identity_projection_state set status='ready',last_processed_event_id=1,binding_revision=0`,
	}
	for _, statement := range seed {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	repo := New(db)
	writerStarted := make(chan struct{})
	writerDone := make(chan error, 1)
	err = repo.WithSnapshot(ctx, func(v ports.View) error {
		if _, err := v.Source(ctx, 1); err != nil {
			return err
		} // fixes the SQLite snapshot
		go func() {
			close(writerStarted)
			writerDone <- changeConfigAndProjection(ctx, db)
		}()
		<-writerStarted
		state, err := v.State(ctx)
		if err != nil {
			return err
		}
		projection, err := v.Projection(ctx, 1)
		if err != nil {
			return err
		}
		binding, err := v.Binding(ctx, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		if err != nil {
			return err
		}
		policy, err := v.Policy(ctx, resourcepolicy.PolicyID("cccccccccccccccccccccccccccccccc"))
		if err != nil {
			return err
		}
		if state.BindingRevision != 0 || projection.APIKeyID == nil ||
			*projection.APIKeyID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ||
			binding.Revision != 1 || policy.Revision != 1 || policy.Rules[0].LimitValue != 2 {
			t.Errorf("mixed snapshot: state=%+v projection=%+v binding=%+v policy=%+v", state, projection, binding, policy)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	err = repo.WithSnapshot(ctx, func(v ports.View) error {
		state, err := v.State(ctx)
		if err != nil {
			return err
		}
		policy, err := v.Policy(ctx, resourcepolicy.PolicyID("cccccccccccccccccccccccccccccccc"))
		if err != nil {
			return err
		}
		if state.BindingRevision != 1 || policy.Revision != 2 || policy.Rules[0].LimitValue != 3 {
			t.Errorf("new snapshot not visible: state=%+v policy=%+v", state, policy)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func changeConfigAndProjection(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`update gateway_usage_identity_projection_state set binding_revision=1`,
		`update gateway_usage_identity_projection_v1 set api_key_state='unknown',api_key_id=null where usage_event_id=1`,
		`update gateway_quota_policies set revision=2,updated_at_ms=2`,
		`update gateway_quota_policy_rules set limit_value=3`,
		`update gateway_api_key_policy_bindings set revision=2,updated_at_ms=2`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func TestObserveRejectsMissingRawWithinCanonicalWindow(t *testing.T) {
	for _, tt := range []struct {
		name                  string
		missingID, missingAt  int64
		missingKey            any
		missingState          string
		highWater, start, end int64
		wantError             bool
		wantCount             int64
	}{
		{"deleted within window", 1, 100, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "mapped", 2, 1, 201, true, 0},
		{"different key", 1, 100, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "mapped", 2, 1, 201, false, 1},
		{"unmapped", 1, 100, nil, "unknown", 2, 1, 201, false, 1},
		{"before window", 1, 100, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "mapped", 2, 150, 201, false, 1},
		{"exclusive end", 1, 100, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "mapped", 2, 1, 100, false, 0},
		{"beyond high water", 3, 150, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "mapped", 2, 1, 201, false, 1},
		{"after source time", 1, 250, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "mapped", 2, 1, 201, false, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, err := sqlite.Open(filepath.Join(t.TempDir(), "archive-window.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, statement := range []string{
				`insert into usage_events(id,event_hash,timestamp_ms,timestamp,model,total_tokens,failed,created_at_ms)
				 values (2,'live',200,'2026-01-01T00:00:00Z','model',5,0,1)`,
				`insert into gateway_usage_identity_projection_v1
				 (usage_event_id,event_hash,evidence_timestamp_ms,api_key_state,api_key_id,credential_state,schema_version,projected_at_ms)
				 values (2,'live',200,'mapped','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','unknown',1,1)`,
			} {
				if _, err := db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`insert into gateway_usage_identity_projection_v1
				(usage_event_id,event_hash,evidence_timestamp_ms,api_key_state,api_key_id,credential_state,schema_version,projected_at_ms)
				values (?,'archived',?,?,?,'unknown',1,1)`, tt.missingID, tt.missingAt, tt.missingState, tt.missingKey); err != nil {
				t.Fatal(err)
			}
			for _, metric := range []resourcepolicy.Metric{resourcepolicy.MetricRequest, resourcepolicy.MetricToken} {
				err := New(db).WithSnapshot(context.Background(), func(v ports.View) error {
					observation, err := v.Observe(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", metric, tt.highWater, tt.start, tt.end)
					if tt.wantError {
						if !errors.Is(err, ErrSourceIncomplete) {
							t.Fatalf("%s incomplete historical source: %+v err=%v", metric, observation, err)
						}
						return nil
					}
					if err != nil {
						return err
					}
					if observation.Count != tt.wantCount || metric == resourcepolicy.MetricToken &&
						(observation.TokenSum == nil || *observation.TokenSum != tt.wantCount*5) {
						t.Fatalf("%s unaffected observation: %+v", metric, observation)
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
