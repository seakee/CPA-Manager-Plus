package upgrade

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
)

func TestManagedUpgradeRequiresAndPersistsTwoPrefixesAtomically(t *testing.T) {
	fixture := newManagedVerifyFixture(t)
	ctx := context.Background()

	// 1. Verify unconditionally refuses completion before prefixes are configured.
	opts := fixture.verifyOptions()
	opts.ControlBasePath = ""
	opts.InferenceBasePath = ""
	opts.SharedEntry = false
	if _, err := Verify(ctx, opts); err == nil {
		t.Fatal("Verify succeeded without configured Managed prefixes")
	} else if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorVerifyAccessPrefixRequired {
		t.Fatalf("error code = %q (ok=%t), want %s (err=%v)", code, ok, UpgradeErrorVerifyAccessPrefixRequired, err)
	}
	singlePrefixOpts := opts
	singlePrefixOpts.ControlBasePath = "/upgrade-admin"
	if _, err := Verify(ctx, singlePrefixOpts); err == nil {
		t.Fatal("Verify succeeded on Managed target without inferenceBasePath")
	} else if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCode(bootstrap.CodeInferencePrefixRequired) {
		t.Fatalf("single-prefix Managed Verify code = %q (ok=%t), want %s (err=%v)", code, ok, bootstrap.CodeInferencePrefixRequired, err)
	}
	// 2. Managed mode refuses missing inference prefix and unconfirmed shared prefix.
	if _, err := ConfigureAccessRouting(ctx, AccessRoutingOptions{
		TargetRoot:      fixture.target,
		ControlBasePath: "/upgrade-admin",
	}); err == nil {
		t.Fatal("ConfigureAccessRouting accepted Managed config without inferenceBasePath")
	}
	if _, err := ConfigureAccessRouting(ctx, AccessRoutingOptions{
		TargetRoot:        fixture.target,
		ControlBasePath:   "/upgrade-same",
		InferenceBasePath: "/upgrade-same",
		SharedEntry:       false,
	}); err == nil {
		t.Fatal("ConfigureAccessRouting accepted identical prefixes without sharedEntry")
	}

	// 3. Persist valid Managed two-prefix config onto upgrade_operations and recover on ReadRecord.
	cfgRes, err := ConfigureAccessRouting(ctx, AccessRoutingOptions{
		TargetRoot:        fixture.target,
		ControlBasePath:   "/upgrade-admin",
		InferenceBasePath: "/upgrade-api",
		SharedEntry:       false,
	})
	if err != nil {
		t.Fatalf("ConfigureAccessRouting failed: %v", err)
	}
	if cfgRes.TargetMode != model.UpgradeTargetManaged || cfgRes.Operation.RoutingCommitted {
		t.Fatalf("unexpected pre-verify routing result: %+v", cfgRes)
	}

	persisted, found, err := ReadRecord(ctx, fixture.databasePath())
	if err != nil || !found {
		t.Fatalf("ReadRecord: found=%t err=%v", found, err)
	}
	if persisted.ControlBasePath != "/upgrade-admin" || persisted.InferenceBasePath != "/upgrade-api" || persisted.RoutingCommitted || persisted.InferenceCommitted {
		t.Fatalf("unexpected persisted pre-verify row: %+v", persisted)
	}

	// 4. Verify commits Stage=complete and RoutingCommitted=true atomically.
	verRes, err := Verify(ctx, opts)
	if err != nil {
		t.Fatalf("Verify after ConfigureAccessRouting failed: %v", err)
	}
	if !verRes.Complete || !verRes.RoutingCommitted || !verRes.InferenceCommitted || verRes.ControlBasePath != "/upgrade-admin" || verRes.InferenceBasePath != "/upgrade-api" || verRes.ExternalBaseURL != "" {
		t.Fatalf("unexpected VerifyResult: %+v", verRes)
	}
	completedManaged, found, err := ReadRecord(ctx, fixture.databasePath())
	if err != nil || !found {
		t.Fatalf("ReadRecord(Managed complete): found=%t err=%v", found, err)
	}
	if completedManaged.Stage != model.UpgradeStageComplete || !completedManaged.RoutingCommitted || !completedManaged.InferenceCommitted ||
		completedManaged.ControlBasePath != "/upgrade-admin" || completedManaged.InferenceBasePath != "/upgrade-api" {
		t.Fatalf("unexpected persisted completed Managed row: %+v", completedManaged)
	}

	// 5. Committed prefixes are preserved and cannot be overwritten.
	if _, err := ConfigureAccessRouting(ctx, AccessRoutingOptions{
		TargetRoot:        fixture.target,
		ControlBasePath:   "/other-admin",
		InferenceBasePath: "/other-api",
	}); err == nil {
		t.Fatal("ConfigureAccessRouting overwrote committed prefixes")
	} else if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorPrefixesAlreadyCommitted {
		t.Fatalf("overwrite refusal code = %q, want %s", code, UpgradeErrorPrefixesAlreadyCommitted)
	}
}

func TestExternalUpgradeForbidsInferencePrefixAndCommitsControlOnly(t *testing.T) {
	fixture := newExternalVerifyFixture(t)
	ctx := context.Background()

	opts := fixture.verifyOptions()
	opts.ControlBasePath = ""
	opts.InferenceBasePath = ""
	opts.SharedEntry = false

	// Unconditional refusal when no control prefix is configured or passed.
	if _, err := Verify(ctx, opts); err == nil {
		t.Fatal("External Verify succeeded without configured controlBasePath")
	} else if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorVerifyAccessPrefixRequired {
		t.Fatalf("External missing prefix code = %q (ok=%t), want %s", code, ok, UpgradeErrorVerifyAccessPrefixRequired)
	}

	withInferenceOpts := opts
	withInferenceOpts.ControlBasePath = "/ext-admin"
	withInferenceOpts.InferenceBasePath = "/ext-api"
	if _, err := Verify(ctx, withInferenceOpts); err == nil {
		t.Fatal("External Verify accepted managed inferenceBasePath")
	} else if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCode(bootstrap.CodeExternalInferencePrefixForbidden) {
		t.Fatalf("External inference prefix Verify code = %q (ok=%t), want %s", code, ok, bootstrap.CodeExternalInferencePrefixForbidden)
	}

	if _, err := ConfigureAccessRouting(ctx, AccessRoutingOptions{
		TargetRoot:        fixture.target,
		ControlBasePath:   "/ext-admin",
		InferenceBasePath: "/ext-api",
	}); err == nil {
		t.Fatal("External Upgrade accepted managed inferenceBasePath")
	}

	cfgRes, err := ConfigureAccessRouting(ctx, AccessRoutingOptions{
		TargetRoot:      fixture.target,
		ControlBasePath: "/ext-admin",
	})
	if err != nil {
		t.Fatalf("External ConfigureAccessRouting failed: %v", err)
	}
	if cfgRes.ExternalBaseURL == "" {
		t.Fatalf("External ConfigureAccessRouting missing ExternalBaseURL: %+v", cfgRes)
	}

	verRes, err := Verify(ctx, opts)
	if err != nil {
		t.Fatalf("External Verify failed: %v", err)
	}
	if !verRes.Complete || !verRes.RoutingCommitted || verRes.InferenceCommitted || verRes.ControlBasePath != "/ext-admin" || verRes.InferenceBasePath != "" || verRes.ExternalBaseURL != cfgRes.ExternalBaseURL {
		t.Fatalf("unexpected External VerifyResult: %+v", verRes)
	}

	// Completed status replay works without the old 1.x source volume and rejects conflicting prefixes.
	if err := os.RemoveAll(fixture.volume.root); err != nil {
		t.Fatalf("remove 1.x source after completion: %v", err)
	}
	replayRes, err := Verify(ctx, VerifyOptions{TargetRoot: fixture.target})
	if err != nil {
		t.Fatalf("completed Verify replay without LegacyRoot failed: %v", err)
	}
	if !replayRes.Complete || !replayRes.AlreadyVerified || replayRes.ControlBasePath != "/ext-admin" || !replayRes.RoutingCommitted {
		t.Fatalf("unexpected completed replay result: %+v", replayRes)
	}
	if _, err := Verify(ctx, VerifyOptions{TargetRoot: fixture.target, ControlBasePath: "/different-admin"}); err == nil {
		t.Fatal("completed Verify replay accepted conflicting ControlBasePath")
	} else if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorPrefixesAlreadyCommitted {
		t.Fatalf("conflicting replay code = %q (ok=%t), want %s", code, ok, UpgradeErrorPrefixesAlreadyCommitted)
	}
	if _, err := Verify(ctx, VerifyOptions{TargetRoot: fixture.target, ControlBasePath: "/ext-admin", InferenceBasePath: "/ext-api"}); err == nil {
		t.Fatal("completed External Verify replay accepted forbidden InferenceBasePath")
	} else if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorPrefixesAlreadyCommitted {
		t.Fatalf("conflicting External inference code = %q (ok=%t), want %s", code, ok, UpgradeErrorPrefixesAlreadyCommitted)
	}

	completedRow, found, err := ReadRecord(ctx, fixture.databasePath())
	if err != nil || !found {
		t.Fatalf("ReadRecord(External complete): found=%t err=%v", found, err)
	}
	if completedRow.Stage != model.UpgradeStageComplete || !completedRow.RoutingCommitted || completedRow.InferenceCommitted ||
		completedRow.ControlBasePath != "/ext-admin" || completedRow.InferenceBasePath != "" {
		t.Fatalf("unexpected persisted completed External row: %+v", completedRow)
	}
	db, err := managersqlite.Open(fixture.databasePath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	realBinding, hasReal, err := upgradeoperation.New(db).ExternalBinding(ctx)
	if err != nil || !hasReal {
		t.Fatalf("ExternalBinding: has=%t err=%v", hasReal, err)
	}
	// Drift in BaseURL relative to binding.Identity, unsealed ManagementKey, or drifted identity all fail closed.
	driftedURLBinding := realBinding
	driftedURLBinding.BaseURL = "https://drifted-cpa.example:8317"
	if got := FencedExternalBaseURL(completedRow, driftedURLBinding, true); got != "" {
		t.Fatalf("FencedExternalBaseURL accepted BaseURL drifted from binding.Identity: %q", got)
	}
	unsealedKeyBinding := realBinding
	unsealedKeyBinding.ManagementKey = "plaintext-not-protected-envelope"
	if got := FencedExternalBaseURL(completedRow, unsealedKeyBinding, true); got != "" {
		t.Fatalf("FencedExternalBaseURL accepted unsealed ManagementKey envelope: %q", got)
	}
	if got := FencedExternalBaseURL(completedRow, upgradeoperation.ExternalBinding{
		BaseURL:       verRes.ExternalBaseURL,
		Identity:      "drifted-identity",
		Generation:    "drifted-identity",
		ManagementKey: realBinding.ManagementKey,
		ObservedAtMS:  realBinding.ObservedAtMS,
	}, true); got != "" {
		t.Fatalf("FencedExternalBaseURL accepted drifted binding identity: %q", got)
	}
}

func TestPreExistingCompletedUnroutedStoreRefusesEmptyReplayAndRepairsOnceWithoutLegacySource(t *testing.T) {
	ctx := context.Background()

	t.Run("managed-completely-unrouted-and-committed-control-preservation", func(t *testing.T) {
		fixture := newManagedVerifyFixture(t)
		opts := fixture.verifyOptions()
		opts.ControlBasePath = "/initial-ctrl"
		opts.InferenceBasePath = "/initial-api"
		if _, err := Verify(ctx, opts); err != nil {
			t.Fatalf("initial Managed Verify: %v", err)
		}
		if err := os.RemoveAll(fixture.volume.root); err != nil {
			t.Fatalf("remove 1.x source: %v", err)
		}

		// Simulate a pre-existing v2@9060054e completed Managed store before routing columns existed.
		db, err := managersqlite.Open(fixture.databasePath())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `update upgrade_operations set control_base_path = '', inference_base_path = '', shared_entry = 0, routing_committed = 0, inference_committed = 0 where id = 1`); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		_ = db.Close()

		// Empty replay fails closed instead of reporting valid completion without prefixes.
		if _, err := Verify(ctx, VerifyOptions{TargetRoot: fixture.target}); err == nil {
			t.Fatal("Verify accepted pre-existing unrouted complete store without prefixes")
		} else if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorVerifyAccessPrefixRequired {
			t.Fatalf("unrouted complete refusal code = %q (ok=%t), want %s", code, ok, UpgradeErrorVerifyAccessPrefixRequired)
		}

		// One-time repair commits missing prefixes without needing the 1.x source volume.
		repaired, err := Verify(ctx, VerifyOptions{
			TargetRoot:        fixture.target,
			ControlBasePath:   "/repaired-ctrl",
			InferenceBasePath: "/repaired-api",
		})
		if err != nil {
			t.Fatalf("one-time Managed missing-prefix repair failed: %v", err)
		}
		if !repaired.Complete || repaired.AlreadyVerified || !repaired.RoutingCommitted || !repaired.InferenceCommitted ||
			repaired.ControlBasePath != "/repaired-ctrl" || repaired.InferenceBasePath != "/repaired-api" {
			t.Fatalf("unexpected repaired VerifyResult: %+v", repaired)
		}

		// Second attempt with different prefixes is refused; committed prefixes stay immutable.
		if _, err := ConfigureAccessRouting(ctx, AccessRoutingOptions{
			TargetRoot:        fixture.target,
			ControlBasePath:   "/mutated-ctrl",
			InferenceBasePath: "/mutated-api",
		}); err == nil {
			t.Fatal("ConfigureAccessRouting mutated repaired prefixes")
		} else if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorPrefixesAlreadyCommitted {
			t.Fatalf("second repair refusal code = %q, want %s", code, UpgradeErrorPrefixesAlreadyCommitted)
		}

		// Simulate Managed store with committed ControlBasePath and missing InferenceBasePath:
		// ControlBasePath must be preserved and cannot be changed during inference repair.
		db2, err := managersqlite.Open(fixture.databasePath())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db2.ExecContext(ctx, `update upgrade_operations set inference_base_path = '', inference_committed = 0 where id = 1`); err != nil {
			_ = db2.Close()
			t.Fatal(err)
		}
		_ = db2.Close()

		if _, err := ConfigureAccessRouting(ctx, AccessRoutingOptions{
			TargetRoot:        fixture.target,
			ControlBasePath:   "/attempt-overwrite-ctrl",
			InferenceBasePath: "/repaired-api-v2",
		}); err == nil {
			t.Fatal("ConfigureAccessRouting allowed overwriting committed ControlBasePath during inference repair")
		} else if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorPrefixesAlreadyCommitted {
			t.Fatalf("overwrite committed control code = %q, want %s", code, UpgradeErrorPrefixesAlreadyCommitted)
		}

		infRepaired, err := ConfigureAccessRouting(ctx, AccessRoutingOptions{
			TargetRoot:        fixture.target,
			InferenceBasePath: "/repaired-api-v2",
		})
		if err != nil {
			t.Fatalf("ConfigureAccessRouting preserving committed ControlBasePath failed: %v", err)
		}
		if infRepaired.Operation.ControlBasePath != "/repaired-ctrl" || infRepaired.Operation.InferenceBasePath != "/repaired-api-v2" || !infRepaired.Operation.InferenceCommitted {
			t.Fatalf("unexpected inference-repaired row: %+v", infRepaired.Operation)
		}
	})
}

func TestExternalRoutingAddressFenceUsesCanonicalSettingsSchema(t *testing.T) {
	const current = "http://127.0.0.1:8317"
	const changed = "http://127.0.0.1:8318"
	for _, tc := range []struct {
		name, managerURL, managerKey, setupURL string
		want                                   bool
	}{
		{"manager-authority-ignores-leftover-setup", current, "stored-ciphertext", changed, true},
		{"manager-drift-refused", changed, "stored-ciphertext", current, false},
		{"legacy-setup-drift-refused", "", "", changed, false},
		{"partial-manager-url-drift-refused", changed, "", current, false},
		{"matching-legacy-setup", "", "", current, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := managersqlite.Open(filepath.Join(t.TempDir(), "settings.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			manager, err := json.Marshal(model.ManagerConfig{CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: tc.managerURL, ManagementKey: tc.managerKey}})
			if err != nil {
				t.Fatal(err)
			}
			setup, err := json.Marshal(model.Setup{CPAUpstreamURL: tc.setupURL, ManagementKey: "stored-ciphertext"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`insert into settings (key, value, updated_at_ms) values ('manager_config_v1', ?, 1), ('setup', ?, 1) on conflict(key) do update set value=excluded.value, updated_at_ms=excluded.updated_at_ms`, string(manager), string(setup)); err != nil {
				t.Fatal(err)
			}
			if got := storedSettingsConnectionAddressesMatch(t.Context(), db, current); got != tc.want {
				t.Fatalf("stored External URL fence = %t, want %t", got, tc.want)
			}
			if got := storedSettingsConnectionAddressesMatch(t.Context(), db, changed); tc.want && got {
				t.Fatal("one settings authority accepted two different External URLs")
			}
		})
	}
}

func TestCompletedVerifyReplayRejectsWhitespaceWhileTransactionOpen(t *testing.T) {
	for _, mode := range []string{"managed", "external", "shared"} {
		t.Run(mode, func(t *testing.T) {
			factory := newManagedVerifyFixture
			if mode == "external" {
				factory = newExternalVerifyFixture
			}
			fixture := factory(t)
			opts := fixture.verifyOptions()
			if mode == "shared" {
				opts.InferenceBasePath = opts.ControlBasePath
				opts.SharedEntry = true
			}
			if _, err := Verify(t.Context(), opts); err != nil {
				t.Fatal(err)
			}
			db, err := managersqlite.Open(fixture.databasePath())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			repo := upgradeoperation.New(db)
			row, _, err := repo.Get(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			binding, hasBinding, err := repo.ExternalBinding(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, input := range []VerifyOptions{{TargetRoot: fixture.target}, opts, {TargetRoot: fixture.target, ControlBasePath: "   "}, {TargetRoot: fixture.target, InferenceBasePath: "   "}} {
				wantConflict := input.ControlBasePath == "   " || input.InferenceBasePath == "   "
				_, err := Verify(t.Context(), input)
				if wantConflict {
					if code, _ := UpgradeErrorCodeOf(err); code != UpgradeErrorPrefixesAlreadyCommitted {
						t.Fatalf("public completed replay code=%q err=%v", code, err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				result, err := completedVerifyReplayResultForStore(ctx, db, fixture.paths, row, binding, hasBinding, input)
				_ = tx.Rollback()
				cancel()
				if wantConflict {
					if code, _ := UpgradeErrorCodeOf(err); code != UpgradeErrorPrefixesAlreadyCommitted {
						t.Fatalf("in-transaction replay code=%q err=%v", code, err)
					}
				} else if err != nil || !result.Complete || !result.AlreadyVerified {
					t.Fatalf("in-transaction completed replay=%+v err=%v", result, err)
				}
			}
		})
	}
}

func TestCompletedManagedStagedInferenceEmptyReplayAndPartialRepair(t *testing.T) {
	fixture := newManagedVerifyFixture(t)
	opts := fixture.verifyOptions()
	if _, err := Verify(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	db, err := managersqlite.Open(fixture.databasePath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`update upgrade_operations set inference_committed=0 where id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(t.Context(), VerifyOptions{TargetRoot: fixture.target}); err == nil {
		t.Fatal("empty replay accepted staged, uncommitted inference")
	} else if code, _ := UpgradeErrorCodeOf(err); code != UpgradeErrorVerifyAccessPrefixRequired {
		t.Fatalf("empty staged replay code=%q err=%v", code, err)
	}
	if _, err := Verify(t.Context(), VerifyOptions{TargetRoot: fixture.target, ControlBasePath: opts.ControlBasePath}); err == nil {
		t.Fatal("Managed repair accepted missing inference")
	} else if code, _ := UpgradeErrorCodeOf(err); code != UpgradeErrorCode(bootstrap.CodeInferencePrefixRequired) {
		t.Fatalf("partial repair code=%q err=%v", code, err)
	}
	result, err := Verify(t.Context(), opts)
	if err != nil || !result.InferenceCommitted || result.InferenceBasePath != opts.InferenceBasePath {
		t.Fatalf("staged inference repair=%+v err=%v", result, err)
	}
}
