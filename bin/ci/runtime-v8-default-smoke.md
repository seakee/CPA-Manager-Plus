# Normal v2 Runtime: official CPA v8

This product slice implements [Plan #60](https://github.com/seakee/cpamp-plan/pull/60)
against product v2 base 9af6e1faae5b1a24c0ade509354fec1ae3a03a5b.
It changes the normal final Docker target, not a release channel or Gateway admission.

## Build and isolation contract

A build without --target (or with --target runtime) selects the official
router-for-me/CLIProxyAPI v8.0.55 pin, tag commit
e6e5708e677adb2463d45accdd24bc9fd028ddd8. Both archive and extracted executable
SHA256 are mandatory. There are no version/hash override arguments or v7 fallback.

| Platform | Official archive SHA256 | Executable / manifest artifactId suffix |
| --- | --- | --- |
| linux/amd64 | 4cd593251db85e50c820f42e6ea5cecdd05f902201d3dbdb025671d4f306a123 | e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7 |
| linux/arm64 | 62c136d7c6ee03cedebaad4be3e7fb9bcf14d095c96597fd92e68554c2a785c5 | 4a5b43cc3700d36c72b7c1953e9b11eca0888c13b789ea2f99471117504a7d5d |

The official Release API asset digests were checked live on 2026-11-05.
The arm64 archive was independently downloaded and hashed before pinning its
extracted executable. The existing v8 download stage is reused for both platforms.
Other architectures fail with an explicit no-v7-fallback error.

The normal v8 seed is config.v8.default.seed.yaml, SHA256
7d2f07c54bb904b6c6691d72a334dddcb6cdf7ad3bd8f346cd71281c1bd2e574.
It uses the v8 server/management/access/oauth/observability structure, keeps
client/provider credentials empty, and binds CPA to 127.0.0.1:8317.
The ordinary Compose services remain separate network namespaces. Therefore
neither Ingress, Manager nor another peer on the tested Compose network can
reach CPA directly. Routed IPv6 and remote-host probes are NOT RUN.
Supervisor can still identify/start/stop/restart the child and check local health.
Ingress remains Gateway Pending. This deliberately does not enable a usable
public Gateway or a general Manager-to-CPA management path.

Upstream CPA still permits anonymous model traffic when its Client Key list is
empty. Loopback isolation encloses that native behavior; this is not an upstream
authentication fix. No key-count-only authorization is introduced. An operator
joining Runtime's network namespace, mounting its state or controlling Docker
is trusted host authority, outside the untrusted peer boundary. Changing the
seed to a network-wide listener or publishing 8317 voids this evidence.

The v8-amd64-candidate and v8-amd64-loopback-proof targets retain their separate
seeds, entrypoint, private scope and amd64 restriction. S1/S2-A/S2-B Manager,
Ingress, provider configuration and permit implementation are not changed.

## Existing data and rollback

Only the normal target adds a bootstrap preflight before the existing entrypoint.
It initializes an empty Runtime root with a root-owned 0444 identity tied to the
packaged seed hash. On reopen, both that identity and the byte-exact bootstrap
config must still match. Existing v7 data, a nonempty root without that identity,
unknown v8-looking configuration, altered seed configuration, symlinked root /
Gateway directory / config / marker, or invalid identity ownership stop startup.
The shared entrypoint's UID, secret, directory,
journal, selection and lifecycle checks still apply afterward.

Rejection happens before the shared entrypoint creates/chowns persistent state
or generates transport credentials. No user data is migrated, overwritten,
normalized or silently adopted. A partial/unknown bootstrap also stops; recovery
is an explicit host task. This is a bounded bootstrap identity, not a migration
format or general configuration/version-management service.

Preserve existing volumes and their exact original images/backups. Rejected v7
volumes can continue with their original v7 image. Never point a v7 image at newly
initialized v8 data, and never delete a volume merely to make an upgrade pass.
External/Upgrade migration and production rollback qualification remain separate.

## Reproduce

Use unique local tags and new disposable evidence directories. No image push.

~~~sh
revision=$(git rev-parse HEAD)
docker buildx build --platform linux/amd64 --build-arg VERSION=dev --build-arg SOURCE_COMMIT="$revision" \
  -f Dockerfile.runtime --load -t cpamp-default-v8:task-amd64 .
docker buildx build --platform linux/amd64 --build-arg VERSION=dev --build-arg SOURCE_COMMIT="$revision" \
  -f Dockerfile.manager-server --load -t cpamp-default-v8-manager:task .
docker buildx build --platform linux/amd64 --build-arg VERSION=dev --build-arg SOURCE_COMMIT="$revision" \
  -f Dockerfile.ingress --load -t cpamp-default-v8-ingress:task .
docker pull python:3.13-alpine
python3 bin/ci/runtime-v8-default-smoke.py \
  cpamp-default-v8:task-amd64 cpamp-default-v8-manager:task cpamp-default-v8-ingress:task \
  bin/tmp/runtime-default-v8/NEW_RUN
~~~

The smoke uses the normal Compose file, exact local image IDs, a unique project,
new named volumes, a task-owned network and a dynamically assigned host-loopback
Ingress port. It never mounts production data or contacts a real Provider.
The companion peer has no state/secret mounts. Only the trusted health probe
joins Runtime's network namespace. Cleanup targets only task-created objects.

Historical Runtime12/14/17/18/19 suites explicitly select
bin/ci/runtime-v7-compatibility.compose.yml and the v7-compatibility target.
Their v7.3.3/v7 update assumptions remain historical checks, not default-v8
qualification. The compatibility image has its own test tag; it does not replace
the normal v8 image. CI separately builds the normal multiarch images, runs the
normal v8 smoke, tests unsupported-architecture rejection, then runs these
explicit historical suites. Scope selection includes the new test files.

## Observed results — 2026-10-20

These observations were recorded against the previous v8.0.13 pin; the rerun on
the v8.0.55 pin has not been performed. Local Docker Desktop Engine 27.3.1 on
Linux/amd64 6.10.14-linuxkit.

| Check | Result |
| --- | --- |
| Default linux/amd64 build without --target | PASS |
| Default image binary, live /proc executable, manifest v8.0.13 and v8 seed | PASS |
| Manifest root:root 0444; child UID/GID 10001; CPA cannot read token/write Supervisor or marker | PASS |
| New-volume Supervisor identity, offline -> start -> ready, CPA healthz | PASS |
| No/wrong Key via Ingress on models/chat/responses/messages/v1beta models | PASS: all 503 Pending |
| Peer on actual Compose network -> CPA 8317, no/wrong Key | PASS: connection refused |
| Child restart, Runtime restart, changed generation, stale-generation mutation | PASS: Pending retained, stale mutation 409 |
| CPA unavailable, Manager unavailable, Ingress restart | PASS: Pending retained |
| Host-loopback published endpoint, no Key | PASS: 503 |
| Existing v7 and unmarked v8-looking volumes | PASS: exit 1; file bytes, owner and mode unchanged |
| Marked volume with edited bootstrap config | PASS: exit 1; edited bytes unchanged |
| Unsupported linux/riscv64 download target | PASS: explicit rejection, exit 1 |
| v8 amd64 private bundle smoke | PASS, including installed-Key native auth and destination restart |
| Private S1 Docker proof | PASS_PRIVATE_S1_GATEWAY_STOP |
| Private S2-A Docker proof | PASS_PRIVATE_S2A_CHAT |
| v7-compatibility target build and binary/manifest inspection | PASS: v7.3.3, explicit target only |
| Runtime Supervisor and Ingress go test, go test -race, go vet, go build ./... | PASS |
| Manager go test ./... | PASS |
| Manager go test -race ./... | FAIL: unchanged maintenancebundle package exceeded Go's default 10m timeout; other packages PASS, no data race reported |
| Timed-out test alone: go test -race -run '^TestRestoreArchiveDeletionEvidence$' ./internal/service/maintenancebundle | PASS: 61.451s; does not replace the full package result |
| Manager go vet, go build ./... | PASS |
| CI classifier Vitest | PASS: 29 tests |
| Existing candidate-identity offline regression | PASS: 5 tests |
| Compose validation, shell/Python syntax, git diff --check | PASS |
| arm64 CPA download stage, archive/executable checks, emitted manifest | PASS |
| Complete default arm64 Docker image | FAIL (infrastructure): local registry mirror returned invalid metadata for debian:bookworm-slim |
| arm64 container startup/health/security, native arm64 host | NOT RUN |
| Full historical Runtime12/14/17/18/19 local E2E | NOT RUN; configured as explicit v7 CI regressions |
| Real provider, External/Upgrade, public Gateway, release/installer/Beta | NOT RUN / STOP |

Normal amd64 local image config ID:
sha256:9f6fd0fba422ec496fcc2a243392b94d0de6492b7b60468dbaa680c27fde35cd.
This is an image config ID, not an OCI manifest digest. No registry RepoDigest
exists because nothing was published. The downloaded arm64 manifest is
schemaVersion 1 / engine cpa / version 8.0.13 and matches its actual binary hash.
The Dockerfile supports arm64 packaging from verified bytes; a complete arm64
runtime is not locally qualified. Keep that UNKNOWN visible to independent review.

Task-owned raw evidence is under bin/tmp/runtime-default-v8/:
build-amd64-final.log, default-amd64-final/result.json,
private-candidate/result.json, private-s1/evidence.json,
private-s2a/evidence.json, build-arm64-artifact.log, arm64-artifact/out/,
build-arm64-retry2.log, build-unsupported.log, and module test/race/vet/build logs.
It is deliberately not committed. The older D1 smoke Markdown is historical
evidence of the previous opt-in state, not the current default contract.

## Three self-review passes

1. Function/build: checked final target selection, both immutable pins, manifest
   identity, v8 seed, architecture errors and all direct v7 test assumptions.
   Fixed Docker's inherited-CMD reset after overriding ENTRYPOINT; the ordinary
   Compose smoke caught the missing Supervisor startup. Retested the final image.
2. Security/compatibility: fixed the exposed no-Key bootstrap by using a separate
   normal loopback seed; added the default-only existing-volume rejection before
   state mutation. Verified Pending under restart/failure/generation change,
   peer isolation and rejected-volume preservation. The smoke's first Manager-
   outage probe timeout was shorter than Ingress's existing 5-second authority
   timeout; increased only the probe timeout to observe the actual 503 result.
3. Regression/complexity: kept private targets and source owners intact, made
   old lifecycle suites explicitly select their historical v7 baseline, and
   added CI scope coverage. No CPA fork, downloader framework, generic config
   platform, S2-B implementation, permission relaxation or release refactor.

No confirmed P0 remains in the tested normal amd64 bootstrap boundary.
The broad Manager race run timed out in TestRestoreArchiveDeletionEvidence
after the unchanged maintenancebundle package consumed 600 seconds. Ordinary
Manager tests passed (that package: 141.792s); the relevant Fresh/Key/Runtime
race packages passed. No Manager Go source/dependency was changed. This timeout
is an unresolved broad-check limitation, not a claimed successful full race run.
The existing CI bundle-boundary race job already uses a 20-minute timeout;
this slice does not change that policy or the package to conceal the timeout.
The single test that was running at timeout passed a focused race recheck in
61.451s. The full package was not rerun with an extended timeout.
The upstream empty-Key P0 remains outside this isolation; public Gateway/Beta
must not be inferred from these results. arm64 complete-image/live qualification
is UNKNOWN and must be resolved or explicitly constrained before any arm64
support/deployment approval. Independent exact-HEAD review remains required.
