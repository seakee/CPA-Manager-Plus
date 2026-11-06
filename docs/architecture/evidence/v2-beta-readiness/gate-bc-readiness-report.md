# v2 Beta Gate B / Gate C readiness evidence — 2026-11-06

Outcome: **PASS** for the harness, for the three candidate images' licence
inventory, and for the Fresh New, External, Adopt, Runtime v8 smoke and Panel
Lite acceptances on the already-loaded `8935bce5` candidate images. Two plan
items did not run and are reported as such, not as passes. Every number below
was observed on this host in this run; nothing is inferred unless it is
explicitly labelled `[INFERENCE]`.

This is local-only evidence. It is not a registry readback, not a signed
production receipt and not a Beta/Release, tag, channel or `update-index` write.
No image was built: every run below consumes the candidate images that were
already loaded on the Docker host.

## Source identity

| Item | Value |
| --- | --- |
| Base commit | `8935bce5ea7ce994185ee97acb8a4fdbd9cdd6cc` |
| Worktree branch | `test/v2-beta-readiness-evidence` |
| Working tree | uncommitted PR-3 files (see “Changed files”) |
| Candidate version label | `2.0.0-r1candidate` (image provenance `version`) |
| Platform | `linux/amd64` |

## Candidate images (inspected, never built)

| Role | Reference | Image ID (`docker image inspect`) |
| --- | --- | --- |
| manager | `cpampd-beta-manager:8935bce5` | `sha256:38ade7eae6d09e7c05501eae543992cdbea99e0090e6d656651ed12744706f86` |
| runtime | `cpampd-beta-runtime:8935bce5` | `sha256:8c6a7ed65a2c4cd15c5a65b57e0e296bb519c5f8e3130f87c77e1a3647861797` |
| ingress | `cpampd-beta-ingress:8935bce5` | `sha256:5cfa0720fb17822a3b682fe311b336aafe2ebca7c8a037b89ca1df679a7625fb` |

Every acceptance run recorded these same image IDs (Fresh New through its
`images` field, External and Adopt through `images` and `imageSourceCommit`, the
smoke through `imageIds`, Panel Lite after verifying the runtime image before it
started), so every document below describes one candidate build.

## Host facts

| Item | Value |
| --- | --- |
| Host OS | macOS `darwin 22.6.0`, x64, 8 logical CPUs, 32 GiB RAM |
| Docker server | `linux/amd64 27.3.1` (Docker Desktop VM) |
| Container limits | `--cpus=2 --memory=4g --memory-swap=4g` |
| Docker VM free space at start | `6,351,237,120` bytes (5.92 GiB); the harness floor is 3 GiB |
| Benchmark storage | Docker volume inside the VM (the fixture is prepared on the host and copied into the volume, so measured I/O runs on the VM filesystem, not the macOS shared-folder layer) |

The benchmark host is **Docker Desktop on macOS x64**, not a dedicated SSD Linux
host. Any claim that these numbers represent a production Linux/SSD deployment
is `[INFERENCE]`; the observed numbers are a lower bound for such a host, not a
measurement of it.

## Method

`node bin/ci/readiness-gate-bc.mjs` runs five scenario groups against the loaded
candidate images. It creates one host directory `<work-dir>/cpampd<8 hex>`,
publishes only loopback ports, names every container and volume under the same
prefix, registers each object before creating it, and removes all of them — plus
the host directory — on the normal path, every error path and SIGINT/SIGTERM. A
cleanup that cannot remove what this run created fails the run instead of
reporting a pass.

1. **Licence inventory** — `third-party-inventory.txt` and `provenance.json` are
   read out of each loaded image and checked: inventory `status`, role, the
   provenance schema, the candidate commit, the inventory digest binding, and a
   coverage entry per component.
2. **Store/DB-open failure (`unsupported_derived_format`)** — the candidate's
   `initialize` creates the real schema and data key; the harness completes the
   store the way a finished bootstrap persists it, then writes untrusted text
   into `usage_monitoring_model_format_version`. Admission passes, the real
   `store.Open` fails, and the process must answer the safe diagnostics.
3. **Store/DB-open failure (`corrupted_database_bytes`)** — the SQLite file is
   replaced with non-database bytes; inspection must refuse it into read-only
   discovery, never a partial graph.
4. **Mismatched `data.key`**, in two variants:
   - *secret-free store*: only the key is swapped, which is measured rather than
     asserted;
   - *encrypted settings*: `manager_config_v1` and `setup` are written with the
     original key through the product's own envelope (HKDF-SHA256 + AES-256-GCM,
     AAD `settings-secret`), then `data.key` is replaced with a different key.
     The candidate must fail closed and disclose neither key.
5. **2 vCPU / 4 GB benchmark** — 100,000 synthetic `usage_events` over 120 days
   (12 models, 3 providers, 100 accounts, 10 API keys, 850-byte synthetic
   `raw_json`, `timestamp_ms == created_at_ms` so the day rollups carry the same
   spread). One transaction seeds the store (`2,192 ms`, 145,870,848 bytes), then
   two limited runs are measured: hourly rollup enabled (default) and disabled
   (`USAGE_DASHBOARD_HOURLY_ROLLUP_ENABLED=false`), with `cleanup-derived`
   executed offline between them.

The benchmark store is a **fixture**, not an installation. Its construction is
recorded in the evidence (`fixtureConstruction`): the candidate's own
`initialize` creates the schema and data key, then the harness removes the Fresh
checkpoint rows, writes the bootstrap-state row and retires the Fresh marker, so
the benchmark reads the post-install Manager graph (`/v0/management/...`) without
re-running an installer. It makes no claim about install behaviour.

Latency is the wall-clock time of one HTTP request from the harness to the
container, over five consecutive samples per query. P50/P95 are nearest-rank, so
they are always observed samples. Peak memory is the Manager process's
`/proc/1/status` `VmHWM` (RSS); the cgroup `memory.peak`, which also charges the
page cache, is recorded beside it.

## Results — Gate B1: Store/DB-open safe diagnostics (`<= 10 s`)

| Scenario | Time to first `/health` response | `/health` | `/.well-known/cpamp-bootstrap` | Business route | Log marker | Offline lock |
| --- | --- | --- | --- | --- | --- | --- |
| `unsupported_derived_format` (real `Store.Open` failure) | **164 ms** | `503 {"ok":false}` | `503 status=unavailable` | `404` | `Manager store could not be opened; keeping safe not-ready diagnostics available` | released: `cleanup-derived` exited `0` |
| `corrupted_database_bytes` | **56 ms** | `503 {"ok":false}` | `503 status=unavailable` | `404` | `keeping read-only discovery available` | released: `cleanup-derived` reached the file (`exit 1`, `file is not a database (26)`) without a lock refusal |

Both scenarios are far inside the 10,000 ms budget. No registered secret (admin
key, injected untrusted text, host scratch path, data key) appeared in a response
body or the captured log tail (`leakedSecretCount: 0`), and neither container was
OOM-killed.

The lock probe runs while the diagnostic process is still alive, so it proves
admission was released rather than released-on-exit. In the corrupt-file case the
offline command must fail — the database itself is unreadable — but it reaches
validation instead of refusing on `usage.sqlite.manager.lock`; the harness
records both facts separately.

## Results — Gate B1(b): mismatched `data.key`

| Variant | Observed | Secret leaks |
| --- | --- | --- |
| secret-free store | `/health` **200** after **208 ms** | 0 |
| encrypted `manager_config_v1` + `setup` | **exit code 1** after **338 ms**, `running=false`; log: `bootstrap manager server: decrypt secret: invalid data key or corrupted ciphertext` | 0 |

Mechanism, as the code actually reads: `RequireDataKeyForInspectedConnection`
only checks that `data.key` **exists** — it does not compare contents. With a
mismatched key the process passes that guard and `store.Open`, then
`bootstrapservice.Run` fails while decrypting the persisted secret, and
`main.go` terminates with `log.Fatalf("bootstrap manager server: %v", err)`. The
fail-closed guarantee is the non-zero exit plus the absence of key material in
the log; it is not a 503 diagnostic.

## Results — Gate C: 2 vCPU / 4 GB / 100k-event benchmark

| Metric | Rollup on (default) | Rollup off |
| --- | --- | --- |
| Container start → `/health` 200 | **278 ms** | **388 ms** |
| Derived rollups finished (pricing/monitoring/Codex, coverage `100000`) | **54,493 ms** | already covered (no rebuild) |
| `databaseMaintenance` after rollups | `required=true`, `deferredIndexes=11`, `command=cleanup-derived` | `required=false`, `deferredIndexes=0` (after offline cleanup) |
| Offline `cleanup-derived` (Manager stopped) | — | **2,417 ms**, `preparedIndexes=11` |
| Peak process RSS (`VmHWM`) | **133.61 MiB** | **83.57 MiB** |
| cgroup `memory.peak` (includes page cache) | 1,352.96 MiB | 71.46 MiB |
| OOM killed / restarts | `false` / `0` | `false` / `0` |

Both cold starts are inside the 5,000 ms target, and peak RSS is far below the
1.5 GiB target. The 11 deferred indexes are the plan's offline step: after
`cleanup-derived` the restarted Manager reports `required=false` with no reasons.

### HTTP read latencies (5 samples each; ms)

| Query | Rollup on P50 / P95 | Rollup off P50 / P95 |
| --- | --- | --- |
| Dashboard summary (`/v0/management/dashboard/summary`, today window) | **308.77 / 318.37** | **54.71 / 68.20** |
| Monitoring header snapshots (`?days=30&limit=100`) | **2.44 / 2.65** | **2.31 / 2.59** |
| Monitoring events page (50 rows, 30d) | **114.85 / 118.28** | not sampled (the rollup switch does not serve this path) |
| Usage Analytics Overview, 30d / day | **1,054.76 / 1,086.03** | **1,033.88 / 1,230.64** |
| Usage Analytics Overview, 90d / day | **1,889.97 / 1,921.92** | **2,561.96 / 2,601.17** |
| Usage Analytics hourly, 24h | **279.48 / 377.57** | **7.76 / 10.35** |

The fixture is proven to be exercised by the queries themselves, not only by row
counts: in the rollup-on run the 30d overview reported `total_calls >= 12,500`,
the 90d overview a larger count, the 24h summary `> 0`, the dashboard today
window `> 0`, and the events page exactly 50 rows. The rollup-off run validates
the same queries it samples (all but the events page).

Observed notes, recorded rather than smoothed:

- The 30d/90d overview carries `summary_percentiles`, `summary_comparison`,
  `timeline`, `model_stats`, `channel_share`, `api_key_stats` and
  `anomaly_points` — the Usage Analytics Overview tab's include set — so it is
  the heaviest read here: ~1.0 s (30d) and ~1.9–2.6 s (90d) on two vCPUs.
- With the hourly rollup enabled the 24h hourly read is ~279 ms and the dashboard
  summary ~309 ms; with it disabled the same reads fall back to the raw
  `usage_events`/index path and answer in ~8 ms and ~55 ms. This is the observed
  direction on this fixture; it is not a product recommendation.
- The 90d P50 is higher with the rollup disabled than enabled. The hourly rollup
  does not serve day-granularity queries, so this is sample variance on a
  two-vCPU host, not an effect of the switch.

## Results — candidate licence inventory

| Role | Inventory path | `status` | Digest bound to provenance |
| --- | --- | --- | --- |
| manager | `/usr/local/share/cpamp/release/third-party-inventory.txt` | `VERIFIED_LICENSE_COLLECTION` | yes |
| runtime | same | `VERIFIED_LICENSE_COLLECTION` | yes |
| ingress | same | `VERIFIED_LICENSE_COLLECTION` | yes |

The review named `/usr/local/share/cpamp/release-provenance.json` with a
top-level `thirdPartyInventoryStatus`. **Neither exists in these candidate
images**: the path probe returns "No such file or directory" for all three roles
(recorded as `releaseProvenancePathPresent: false`), and the inventory carries
`status`, not `thirdPartyInventoryStatus`. The harness asserts the observed
contract instead — the inventory status, its role, the provenance schema, the
candidate commit, and that `provenance.json`'s `licenseInventorySHA256` equals
the SHA-256 of the inventory bytes — so the status cannot be read from a
different document than the one the image ships. Substituting the reviewer's
names would have produced a check that always fails or, worse, a vacuous pass.

## Docker items from the plan

| Item | Result | Evidence |
| --- | --- | --- |
| `node bin/ci/readiness-gate-bc.mjs` | **PASS**, `verdict: "pass"`, 18/18 checks, exit `0` | `readiness-gate-bc-29cb551d.json` |
| `node bin/ci/fresh-new-installation.mjs` (same three images) | **PASS**, 30 PASS / 0 FAIL / 0 NOT RUN (3 further entries are harness request records, not checks) | `fresh-new-summary.json` |
| `node bin/ci/fresh-external-management.mjs` (same three images) | **PASS**, 38 PASS / 0 FAIL / 2 NOT RUN (v7 CPA refusal needs `CPAMP_D_CPA_LEGACY`; no real Provider) | `fresh-external-summary.json` |
| `node bin/ci/fresh-adopt-network.mjs` (same three images) | **PASS**, 22 PASS / 0 FAIL / 1 NOT RUN (the v7 source refusal needs `CPAMP_D_CPA_LEGACY`; no pinned v7 binary was supplied) | `fresh-adopt-summary.json` |
| `python3 bin/ci/runtime-v8-default-smoke.py` (same three images) | **PASS** (`result: PASS`, CPA `8.0.55`, artifact hash matches the amd64 pin, Runtime runs as UID 10001) | `runtime-v8-smoke-summary.json` |
| `node bin/ci/plugin-isolation.mjs` | **NOT RUN** — it drives an already-running Manager with an admin session and one enabled fixture plugin (`MANAGER_ORIGIN`, `ADMIN_KEY`, `PLUGIN_ID`). None of the candidate images contributes a fixture plugin, and this run starts no such deployment. |
| `node bin/ci/upgrade-docker.mjs` | **NOT RUN** — it builds the Manager binary from the worktree (`go build ./cmd/cpa-manager-plus`) and builds its own 1.x fixture image; `--source-tag` selects that fixture image, not a running candidate. Running it is a build, which this lane forbids. |
| `node bin/ci/panel-lite-cpa-v8.mjs --skip-build --cpa-image cpampd-beta-runtime:8935bce5` | **PASS**, `verdict: "pass"`, 4/4 layout × static-path cases, 0 published ports, isolation boundary proven | `panel-lite-cpa-v8-summary.json` |

The Panel Lite run needed the bundle the panel build produces, so the bundle
was built once from this worktree (`npm --workspace apps/web run build:panel`)
and the harness then ran with `--skip-build`: it still built no image, and the
`--skip-build` it received suppressed only the panel build. The panel itself is
not part of the candidate images — CPA downloads it — so what this run fixes is
the CPA side (the verified `cpampd-beta-runtime:8935bce5` image ID, its
`cli-proxy-api` digest against the `Dockerfile.runtime` pin, and the v8
management API): each case served the built bundle byte-identically before and
after a CPA restart, and with `disable-control-panel: true` the same path was
`404` with the stored bundle untouched and zero release-server requests. No web
source differs from `8935bce5` in this PR, so the bundle is the base commit's.

The Fresh New acceptance reproduced its 7 data-layer checks as PASS on this base.
`fresh-external` and `fresh-adopt` require a clean worktree (they refuse a dirty
one because the report names the HEAD); they were run with this PR's changes
temporarily stashed and nothing else, then the changes were restored. Both
reports record `gitHead 8935bce5…`, the same image IDs, and `sourceCommit` equal
to the candidate commit.

The `runtime-v8-smoke` result is the harness's own `result.json`; it was run one
at a time under the disk guard, and its `notRun` list (public admission, real
provider, External/Upgrade migration, Beta/Release qualification) is part of the
result rather than a gap this document hides.

## Deviations from the plan

1. **Plan's expected not-ready body.** The plan quotes
   `{"status":"starting","service":"cpa-manager-plus","ready":false}`. At
   `8935bce5` the pre-ready listener returns `503 {"ok":false}` from
   `bootstrapdiscovery.NotReadyHandler`; the harness asserts the observed body
   and fails closed on anything else.
2. **Mismatched `data.key` is not a `Store.Open` failure.** The secret-free store
   reaches `/health 200`; the encrypted store fails closed at bootstrap
   decryption with exit code 1. Both are recorded above, and the mechanism text
   follows the code (`data_key_guard.go` checks presence only).
3. **Fixture time column.** The plan reuses the latency fixture's synthetic
   distribution. The monitoring/pricing day rollups bucket by `created_at_ms`, so
   the harness writes `created_at_ms == timestamp_ms`; otherwise every window
   query answers zero even though `usage_events` holds the rows.
4. **Benchmark storage.** SQLite on a macOS bind mount is not the target host's
   storage; the prepared fixture is copied into a Docker volume and measured
   there. A bind-mount run is not evidence and is not claimed.
5. **Licence file names.** The images carry `release/third-party-inventory.txt`
   with `status`, not `release-provenance.json` with
   `thirdPartyInventoryStatus`; the harness asserts the observed contract and
   records the absent path.
6. **Go acceptance command name.** The plan's
   `go test -v ./cmd/cpa-manager-plus -run TestMainStoreOpenFailure` matches no
   test at this commit (`testing: warning: no tests to run`, `PASS`, exit `0`).
   The substantive test is `TestStoreOpenFailureKeepsSafeStartupListener`; it
   passes in 0.08 s and asserts the same contract process-wide.
7. **Resource prefix and memory probe.** The plan sketches a
   `cpampr<runid>-*` prefix and `docker stats --no-stream` for peak memory. The
   harness names everything `cpampd<runid>-*` (the wave's agreed prefix) and reads
   the Manager process's `VmHWM` plus the cgroup `memory.peak`, because a single
   `docker stats` sample can miss the peak of a ~50 s rollup build.
8. **Clean-worktree harnesses.** `fresh-external-management.mjs` and
   `fresh-adopt-network.mjs` refuse a dirty worktree by design. Their runs used a
   stash of this PR's working-tree changes — nothing else was modified — and
   recorded `gitHead 8935bce5…`; the stash was restored immediately.
9. **Panel Lite and plugin isolation.** `panel-lite-cpa-v8.mjs` ran against the
   candidate runtime image but needed the panel bundle, which this worktree had
   not built; the bundle was built once and the harness ran with `--skip-build`
   so it built no image. `plugin-isolation.mjs` needs a deployment with one
   enabled fixture plugin, which no harness in this lane provides.

## Changed files

- `bin/ci/readiness-gate-bc.mjs` (new; Gate B1/C harness, licence inventory,
  provenance/scenario evidence, self-cleaning workspace)
- `bin/ci/readiness-gate-bc.test.mjs` (new; 14 offline `node:test` cases)
- `docs/architecture/evidence/v2-beta-readiness/gate-bc-readiness-report.md`
  (this report)
- `docs/architecture/evidence/v2-beta-readiness/readiness-gate-bc-29cb551d.json`
  (redacted harness evidence)
- `docs/architecture/evidence/v2-beta-readiness/fresh-new-summary.json`,
  `fresh-external-summary.json`, `fresh-adopt-summary.json`,
  `runtime-v8-smoke-summary.json`, `panel-lite-cpa-v8-summary.json` (redacted
  acceptance summaries)
- `bin/ci/classify-pr-checks.mjs`, `tests/prCheckClassifier.test.mjs`,
  `.github/workflows/pr-check.yml` (Frontend wiring for the new suite)

That is 12 paths: three modified and nine added. Nothing outside this list is
part of PR-3.

## Commands run

```bash
node --test bin/ci/readiness-gate-bc.test.mjs
# 14 tests, 14 pass, 0 fail

node bin/ci/readiness-gate-bc.mjs \
  --output $SCRATCH/evidence \
  --work-dir $SCRATCH/work
# verdict: pass (18/18 checks, exit 0)

PLAYWRIGHT_MODULE=$PLAYWRIGHT \
  node bin/ci/fresh-new-installation.mjs \
    cpampd-beta-runtime:8935bce5 cpampd-beta-manager:8935bce5 \
    cpampd-beta-ingress:8935bce5 \
    $SCRATCH/fresh-new
# PASS

PLAYWRIGHT_MODULE=… node bin/ci/fresh-external-management.mjs \
  cpampd-beta-runtime:8935bce5 cpampd-beta-manager:8935bce5 \
  cpampd-beta-ingress:8935bce5 …/fresh-external
# PASS

PLAYWRIGHT_MODULE=… node bin/ci/fresh-adopt-network.mjs \
  cpampd-beta-runtime:8935bce5 cpampd-beta-manager:8935bce5 \
  cpampd-beta-ingress:8935bce5 …/fresh-adopt
# PASS

python3 bin/ci/runtime-v8-default-smoke.py \
  cpampd-beta-runtime:8935bce5 cpampd-beta-manager:8935bce5 \
  cpampd-beta-ingress:8935bce5 …/runtime-v8-smoke
# PASS

npm --workspace apps/web run build:panel
# dist/panel/management.html, 6,095,525 bytes, single file

node bin/ci/panel-lite-cpa-v8.mjs --skip-build \
  --cpa-image cpampd-beta-runtime:8935bce5 --report $SCRATCH/panel-lite/report.json
# verdict: pass (4/4 cases)

npx vitest run tests/prCheckClassifier.test.mjs tests/ciWorkflowIntegrity.test.mjs
# 2 files, 64 tests, 64 pass

go test -v ./cmd/cpa-manager-plus -run TestMainStoreOpenFailure
# PASS (testing: warning: no tests to run)

go test -v ./cmd/cpa-manager-plus -run TestStoreOpenFailureKeepsSafeStartupListener
# PASS (0.08s)
```

## Open risks

- The benchmark ran on Docker Desktop on macOS x64. Absolute latencies are a
  lower bound for a dedicated Linux/SSD host `[INFERENCE]`; carry forward the
  shape (cold start, memory headroom, heavier 90d overview), not the numbers.
- The two-vCPU host makes tail latencies sensitive to a single scheduling hiccup.
  Repeating the same run on this host moved the 30d overview P50 by several
  percent and the 90d P50 by more, with no change to images, fixture or limits.
  With five samples per query, differences under ~30% are noise: the archived run
  is one sample of that distribution, not a stable constant.
- After derived maintenance the Manager still reports `deferredIndexes=11` until
  the offline `cleanup-derived` runs. For an operator that is a required
  post-upgrade step (`command: cleanup-derived`), and the Manager stays ready and
  serving while it is pending.
- `plugin-isolation.mjs` and `upgrade-docker.mjs` were not exercised on these
  candidate images; the reasons above are capability limits of this lane, not
  passes. `upgrade-docker.mjs` in particular is the one plan item whose subject —
  a 1.x → v2 migration on a real data volume — is not covered by anything in this
  document, and it needs a lane that may build.
- `fresh-external` and `fresh-adopt` were run against a stashed worktree because
  they refuse a dirty one. That records the base commit, not this PR's tree, which
  is what their reports claim.
- No association with the other Scope 4–9 PRs was verified here: this document is
  about the candidate images at `8935bce5`.

## Evidence paths (repo-relative)

- `docs/architecture/evidence/v2-beta-readiness/readiness-gate-bc-29cb551d.json`
- `docs/architecture/evidence/v2-beta-readiness/fresh-new-summary.json`
- `docs/architecture/evidence/v2-beta-readiness/fresh-external-summary.json`
- `docs/architecture/evidence/v2-beta-readiness/fresh-adopt-summary.json`
- `docs/architecture/evidence/v2-beta-readiness/runtime-v8-smoke-summary.json`
- `docs/architecture/evidence/v2-beta-readiness/panel-lite-cpa-v8-summary.json`

The harness also writes a run copy beside its scratch directory; the repository
copy is the versioned one. Each summary is a redacted projection of the full
report the harness wrote to the scratch directory, which is not versioned because
it carries per-page screenshots and container logs.
