# D1: isolated CPA v8 Runtime bundle evidence

**GO for review of this candidate; STOP for public Beta/release.** Tested on
2026-10-19, from product base `c993695b4b9ebe903b21d17f77e4d685d0d66ee3`.
Only packaging, a separate bootstrap seed and this focused smoke changed.
The default Docker target/Compose still use v7.3.3; v8 requires the explicit
`v8-amd64-candidate` target. No image was pushed, nor tag/release created.

> **Recorded run, not the current pin.** Everything below — the pinned source
> commit, the archive/executable identity table, the emitted manifest and the
> observed headers — is the recorded v8.0.13 bundle run and is kept as evidence
> of that run. The current pin is v8.0.55 with
> archive `4cd59325…` / executable `e48c85c7…`, documented in
> `bin/ci/runtime-v8-default-smoke.md`; current single-image invocation is
> `python3 bin/ci/runtime-v8-bundle-smoke.py IMAGE NEW_EVIDENCE_DIRECTORY`.

## Pairing and historical applicability

Inputs: merged Plan #40/#41/#42, First Beta and Release Transition Epics,
Lane D original report/notes, and the [#41 independent correction](https://github.com/seakee/cpamp-plan/pull/41#issuecomment-6257318061).
Pinned CPA source: `router-for-me/CLIProxyAPI@d7914afdedca7af95ee974a42453dc49fc1388ce`.
It has no ETag/If-Match/412 contract. No newer fork is used.

The downloaded [run 39123313023 artifact](https://github.com/seakee/CPA-Manager-Plus/actions/runs/39123313023/artifacts/12135595032)
ZIP SHA256 is `11f5732bde05d03015537de4c7fdccb94664cfd72c424a4f99524e41aa20cfd9`.
Its environment/manifest/logs bind the exact archive/executable below, product
base/tree `5ea19c20eb71215a9b5c084ef5a41037c157da7b`, and consumer digest
`4ead43972543c565ca147b248c166229ec83ee90a8db86267fc6d1f896e8247d`.
The checked-in authority retains four independent bounded acceptances:

| Consumer | Applicable evidence, retained limits |
| --- | --- |
| New Managed | Exact v8 artifact, private Runtime v1 start; Gateway pending |
| External | Authenticated v8 direct safe-read only; no remote binary attestation or lifecycle ownership |
| Adopt network | Safe-read, client-key-only stage/replay/verify; source preserved, Gateway pending |
| Adopt legacy | Exact v7.0.12 source profile to exact v8 destination; separate transition evidence; source preserved, Gateway pending |

The historical run executes `TestDestinationRealCPAV8` and both branches of
`TestAdoptionExactCPAV8WithSupervisor`. The destination config is v8:
`server`, `management`, `access.api-keys`, `oauth.auth-dir`, `observability`,
plugins disabled. Source and destination processes are invoked with `-config`;
the standalone Supervisor uses private `/v1/runtime` operations. Tests use temp
directories without the Docker entrypoint/10001 identity. Thus **binary/schema/
bounded consumer applicability is confirmed; Docker seed, permissions, health
and restart require the separate execution below**. Nothing grants full CPA or
deployment qualification. The consumer and destination implementation pins
remain unchanged and their focused pin/authority tests pass.

Old root `host/port`, `remote-management`, `auth-dir`, `api-keys`, and logging/
usage flags become `server`, `management`, `oauth`, `access`, `observability`.
Root provider `api-keys` is not v8 client authentication. The candidate seed
contains no provider credentials, disables plugins/panel/usage publishing and
keeps empty management secret (management disabled) until destination staging.

## Actual identities

All hashes below are SHA256. Image IDs are local image config IDs, **not registry
platform manifest digests**; RepoDigests is empty. BuildKit metadata preserves
base-image resolutions. No published image or complete deployment set exists.

| Object | Actual identity |
| --- | --- |
| Official `CLIProxyAPI_8.0.13_linux_amd64.tar.gz` | `50ecffb47fdd81c8c5a9825a73a7a905ab66342337e274f39c4276b92d3533f3` |
| Executable, including live `/proc/<pid>/exe` | `b682e9e42586263f476888361514f68f89ff4ebf89a5dadba394b850b797ce41` |
| Supervisor executable | `8f74e3e44533d69d9b4b5f5a6bbf2b734b6f783cb742228e5276ad1190b7c2a3` |
| v8 seed and initial effective `/runtime/gateway/config.yaml` | `d958425c5f71a0e116290a4f0828c367d54905a40ba3ee922f707c4dad5e9eda` |
| Runtime image ID | `sha256:7ab27b9634821fe4de0b7cf9f02175571ecb07d459a1cf8ff9a079f660ccfb71` |
| Unmodified Ingress image ID | `sha256:4d5659fb17579f8950de3d89526d771f0b2d4685d9e946c0c9fd1445a0727766` |
| Probe Python image ID | `sha256:050f6d1f4e5f89af62e03f6fcd1c06be2a401339b3694adc4c0a2850d9d8b1e7` |
| Staged effective config after restart (disposable keys/hash salt) | `08159bfcf56428f11070b46dc09fbcd9826dfa6f640c9a3b8ac32b6d68c190a4` |

The immutable root-owned `0444` manifest is exactly:

```json
{"schemaVersion":1,"engine":"cpa","version":"8.0.13","artifactId":"sha256:b682e9e42586263f476888361514f68f89ff4ebf89a5dadba394b850b797ce41"}
```

Host: macOS 13.6.3 amd64, Docker Desktop 4.36.0 / Engine 27.3.1,
Linux VM `6.10.14-linuxkit`, linux/amd64. Supervisor build uses Go 1.24 Alpine;
test binaries were cross-compiled with Go 1.26.1, CGO disabled and task-local
GOCACHE, then executed in a disposable `golang:1.26-bookworm` container with
`--network none`. No race suite was needed for unchanged lifecycle code.

## Commands and results

Task scratch: `/tmp/cpamp-d1-Fkf0rI`. These are the recorded build/smoke commands,
all exit **0** except the deliberate unsupported-platform negative (exit **1**).
The 2026-10-19 run predates the fail-closed identity guard, so the commands below
now carry the required `--build-arg VERSION=dev --build-arg
SOURCE_COMMIT="$revision"`; those arguments do not change the packaged bytes.
`TARGETOS`/`TARGETARCH=linux/amd64` remain automatic and the explicit targets are
as shown. Downloads and both hardcoded checks ran (not cached) in the first v8
build: archive and `/out/cli-proxy-api` each report `OK`.

```sh
revision=$(git rev-parse HEAD)
docker buildx build --platform linux/amd64 --build-arg VERSION=dev --build-arg SOURCE_COMMIT="$revision" --target v8-amd64-candidate -f Dockerfile.runtime --load -t cpamp-d1-runtime:local --metadata-file /tmp/cpamp-d1-Fkf0rI/build-metadata.json --progress plain .
docker buildx build --platform linux/amd64 --build-arg VERSION=dev --build-arg SOURCE_COMMIT="$revision" -f Dockerfile.ingress --load -t cpamp-d1-ingress:local --metadata-file /tmp/cpamp-d1-Fkf0rI/ingress-metadata.json --progress plain .
python3 bin/ci/runtime-v8-bundle-smoke.py cpamp-d1-runtime:local cpamp-d1-ingress:local /tmp/cpamp-d1-Fkf0rI/smoke-final
# Arch rejection stops inside cpa-v8-download, before any identity stage.
docker buildx build --platform linux/arm64 --target cpa-v8-download -f Dockerfile.runtime --progress plain .
docker buildx build --platform linux/amd64 --build-arg VERSION=dev --build-arg SOURCE_COMMIT="$revision" -f Dockerfile.runtime --load -t cpamp-d1-runtime-v7:local --progress plain .
docker run --rm --network none --entrypoint sh cpamp-d1-runtime-v7:local -ec 'cat /usr/local/share/cpamp/runtime/cpa-artifact.json; sha256sum /usr/local/bin/cli-proxy-api /usr/local/share/cpamp/runtime/config.seed.yaml'
git diff --check
```

Final smoke `commands.json` stores every Docker/probe command, exit status and
HTTP status; request credential values are omitted. A bounded post-restart
connection-refused probe (exit 1) is retained before successful readiness;
all required assertions pass. Early harness attempts exposed internal-network
port publishing and cross-UID `/proc` restrictions; the final test uses a
private-network probe and reads the live executable as its own UID, without
adding ptrace capabilities or changing the Runtime image. All task containers
and networks were removed; logs and images remain local.

| Assertion | Final observed result |
| --- | --- |
| Bootstrap startup | Supervisor offline → typed Start → ready; `HEAD /healthz` 200 |
| Command/cwd | Bootstrap `/usr/local/bin/cli-proxy-api`, cwd `/runtime/gateway`; reads copied seed |
| Bootstrap management/data | v8 and v0 management config 404; no-key `/v1/models` 200 |
| Ingress boundary | With unknown Manager/Fresh authority, `/v1/models` 503 Gateway pending before and after stage |
| Stage API | Exact artifact-fenced stage-destination plus same-operation replay succeed |
| Destination command | `cli-proxy-api -config /runtime/supervisor/destination/config.yaml` |
| Management read | Authenticated `/v8/management/config` 200, X-CPA-VERSION 8.0.13, config-version 8; expected host/port/auth-dir/client keys |
| Credential separation | Missing management credential 401; absent or management-key-as-client 401; synthetic client key 200 |
| Permissions | CPA UID/GID 10001; gateway 0700/config 0600; manifest root:root 0444; Supervisor root:10001 0710; transport token root:root 0600; CPA cannot read token/stage secret or write manifest/Supervisor dir |
| Restart | Child Restart and Docker container restart preserve target/config/keys; old runtime generation Start 409, current Start succeeds |
| Default v7 preservation | Built default remains v7.3.3 executable `f1eb518a12c7fd157e1f7f829a7bdb908bfff88ae29b2205c29bfc79516862f2`, old seed `3ee04ec69028c252e35a1a6a96519e71218c651e56421009c7d4a7012e577e57` |

The extracted image CPA/Supervisor were also used in these existing test
binaries (all exit 0, no skips), not in place of the live-bundle smoke:

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOCACHE=/tmp/cpamp-d1-Fkf0rI/gocache go -C apps/manager-server test -c -o /tmp/cpamp-d1-Fkf0rI/tools/fresh.test ./internal/service/freshstrategy
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOCACHE=/tmp/cpamp-d1-Fkf0rI/gocache go -C apps/runtime-supervisor test -c -o /tmp/cpamp-d1-Fkf0rI/tools/destination.test ./internal/lifecycle
docker run --rm --network none --platform linux/amd64 -v /tmp/cpamp-d1-Fkf0rI:/evidence -v /Users/seakee/WorkSpace/Worktree/CPA-Manager-Plus/feat/v2-beta-d1-runtime-v8-bundle:/src:ro -w /src/apps/manager-server/internal/service/freshstrategy -e CPAMP_TEST_CPA_V8=/evidence/tools/cli-proxy-api -e CPAMP_TEST_CPA_LEGACY=/evidence/tools/cpa-v7 -e CPAMP_TEST_RUNTIME=/evidence/tools/cpamp-runtime-supervisor -e CPAMP_TEST_CPA_LEGACY_ARCHIVE_SHA256=8afc9ed4c89a50a5ffce5d122a9bb93442c5b8ff3d8c6f07b7a274c719109b74 -e CPAMP_TEST_FRESH_RESOURCE_OUT_DIR=/evidence -e CPAMP_TEST_FRESH_TRANSITION_OUT=/evidence/fresh-legacy-transition.json -e CPAMP_TEST_FRESH_TRANSITION_HEAD=c993695b4b9ebe903b21d17f77e4d685d0d66ee3 -e CPAMP_TEST_FRESH_TRANSITION_TREE=5ea19c20eb71215a9b5c084ef5a41037c157da7b -e CPAMP_TEST_FRESH_TRANSITION_SOURCE_HEAD=c993695b4b9ebe903b21d17f77e4d685d0d66ee3 golang:1.26-bookworm /evidence/tools/fresh.test -test.v -test.count=1 '-test.run=^(TestAdoptionExactCPAV8WithSupervisor|TestProductionRecordContainsOnlyIndependentlyReviewedConsumers|TestRuntimeDestinationImplementationPinMatchesExecutionChain|TestManagedRequiresDurableExactIntentAndReusesTypedRuntime|TestExternalStoresOnlyEncryptedManagementSecretAndKeepsGatewayPending)$'
docker run --rm --network none --platform linux/amd64 -v /tmp/cpamp-d1-Fkf0rI/tools:/tools:ro -e CPAMP_TEST_CPA_V8=/tools/cli-proxy-api golang:1.26-bookworm /tools/destination.test -test.v -test.count=1 '-test.run=^TestDestinationRealCPAV8$'
```

The test report HEAD fields identify the unchanged consumer source base;
packaging was the uncommitted D1 diff during execution. NewManaged/External
unit tests use their existing controlled adapters; real executable adoption
tests exercise both network and legacy source branches, source preservation,
replay/verification, SQLite reopen, lost replies and generation fencing.
They are not whole-profile deployment acceptance. The isolated legacy source
uses official v7.0.12 archive `8afc9ed4c89a50a5ffce5d122a9bb93442c5b8ff3d8c6f07b7a274c719109b74`,
verified executable `1eb108b85ad554b92bab9f5a85af433d7c2e1b951bb192b639d73605d4593d1a`.

Raw evidence SHA256 (under the task scratch above):

```text
cbaef92f9cc70150427a3c7a50191862eacd83c063c2ffd0fe91092d928a6e1d  build.log
71959ab7ba406451ffc69cbb307d4a3736e6ed717da6060f07ebd4641e0c3466  build-metadata.json
a687a7ffbd121d14450caba3f7c72f9f19e27d917bfbd9e585bdc41b935b27c2  smoke-final/commands.json
cf16ec1bb4665144d6b477cdae1421b3270d449c78ab5e1129c49c2bbfca12c8  smoke-final/result.json
9bb5acff6fa7fca3fd5ddcdb8e76246722f30a94137dfd4a7fba85dadd15fe05  fresh-smoke.log
571cf029c24abbd6f79514d0a5ddda94c9d3d2feb4b08ce26f0bd7e7e1ad978f  destination-smoke.log
```

## Open gates and complexity

- **CONFIRMED:** exact v8 bundle build, manifest/seed/live executable, isolated
  start/stage/auth/health/permissions/restart and bounded Fresh checks above.
- **INFERRED:** this closes the packaging disconnect and enables subsequent
  single-profile integration; it grants no production support.
- **P0 / OPEN:** empty client keys allow unauthenticated bootstrap data access.
  There are no provider credentials and no published ports/mounts in this
  smoke. C1 last-key mutation safety and Beta entry security remain separate;
  this test does not reproduce last-key deletion or prove C1 completion.
- **P1 / UNKNOWN:** complete Manager/Full Web/Ingress deployment pairing,
  external writable/topology qualification, production recovery/readiness and
  independently accepted candidate support. Ephemeral stage config identity
  is run-specific; no production profile was substituted.
- **NOT RUN / STOP:** five supported Fresh/Upgrade journeys, real Upgrade,
  OAuth/provider traffic, arm64 v8, full qualification/race/old v7 E2E suites,
  Readiness resource budget, license/NOTICES approval, production UI, release
  channel/installer dry-run and image/tag/Release publication. Existing gates
  and five-path scope are unchanged.
- **Overdesign / overdefense / over-abstraction / scope expansion:** none.
  One explicit candidate, one v8 seed, current manifest/entrypoint/identity and
  lifecycle API; a disposable stdlib smoke only. No version matrix, downloader
  platform, shared API/DB/Web changes, installer or release redesign.
