# Private Fresh S1 initial Key installation

This is a maintainer-authorized, disposable **Linux/amd64** experiment for official
CPA **v8.0.13**, scoped by [Plan #57](https://github.com/seakee/cpamp-plan/pull/57)
and product [Draft #1017](https://github.com/seakee/CPA-Manager-Plus/pull/1017).
It grants no public Gateway, release, default Compose, External, Upgrade or S2
capability. All Ingress model requests remain **503**.

The separately authorized [private S2-A consumer](fresh-private-s2a-chat-admission.md)
can now opt into one exact nonstreaming synthetic Chat route in a new disposable
proof. Without that additional opt-in, the S1 Ingress 503 behavior is unchanged.

The separate default-off [B0/B1 profile](fresh-private-config-authority.md)
adds Host-owned source admission, durable full-file receipts and stopped-writer
recovery. Its limited restart behavior does not rebind old S1 generations or
change the standalone S1 profile described here.

## Authority and operation

The existing Manager `gateway_api_key_identities` / `fresh_client_api_keys`
records remain the only Client Key authority. Initial installation consumes the
selected Canonical APIKeyID's existing Protector-encrypted secret internally;
neither the browser nor the mutation journal stores a second secret.

Under the committed Fresh control prefix, authenticated Admin can use:

```text
GET  <control>/api/v2/api-keys/<canonical-id>/initial-install
POST <control>/api/v2/api-keys/<canonical-id>/initial-install  {}
```

The POST accepts only an empty JSON object. Runtime identity, generation,
artifact, host isolation, source ownership, credentials and exclusive-writer
flags are never accepted from this client. The existing Fresh Strategy's stored
target and authenticated Runtime observation must match the host permit's exact
generation. A mismatch returns **409**; no retargeting or recovery is attempted.

The consumer is absent unless the private environment selects the fixed
`CPAMP_PRIVATE_S1_PERMIT=/run/cpamp-s1/permit.json` path and both configured
Runtime/CPA URLs are the private loopback literals. This environment setting alone
grants nothing. Manager must also load a root-owned, regular, non-symlink **0600**
host permit, mounted read-only by the disposable launcher. Its short lease is
checked against real time, authenticated Runtime generation, ready state and the
fixed v8.0.13 artifact. The transport rechecks lease expiry before PUT.

The host launcher is the authority for the facts Manager cannot infer over HTTP.
It creates unpredictable new volumes and an internal Docker network; verifies
the actual binary, live child, mounts, volume consumers, namespace consumers,
empty auth directory, authenticated empty CPA configuration, private-port
refusals and an accessible Ingress positive control; then supplies a fresh
Management credential through a separate read-only permit volume. CPA stores its
hash in the disposable seed. No container has a Docker socket or host data mount.
Among product services, only Manager holds the plaintext credential after host provisioning. The probe
receives credentials per read request on stdin and does not retain them.

**Trust limit:** Docker daemon control, host root and the serial launcher are
trusted. They must not attach another namespace/volume consumer, change CPA
configuration or perform lifecycle mutations during the lease. This is observed
host authority in a controlled experiment, not cryptographic attestation or an
automatic qualification of arbitrary Docker installations. Never hand-author a
permit for an existing or shared environment.

### Profile discriminator: private S1 versus default New Managed

This document describes only the `private` profile. Manager keeps that profile
separate from the default Compose `new_managed` profile with a server-owned
discriminator carried in memory on the CPA transport and permit. The private
profile is admitted only when the fixed loopback URLs and root-owned permit
are present; it expects the loopback seed, `/runtime/gateway/auth`, disabled
remote management and the short-lived host lease described above.

The default profile has no host permit and never treats a caller-provided
`Profile` value as authority. It is selected only from a committed Fresh
`new_managed` checkpoint plus the protected `manager_config_v1` connection and
authenticated Runtime identity/generation/artifact. The Supervisor-owned
destination binds CPA to `0.0.0.0`, uses its destination auth directory,
enables remote management and usage statistics, and starts with an empty
Client Key list. Its protected management secret is not this document's S1
seed and is never rendered by the private S1 UI.

Key/retry operations on a default install derive their short-lived operation
grant from the real Fresh operation ID and step and re-read the destination
before the single CPA write. A missing connection, stale generation/artifact,
non-ready Runtime, changed destination posture or missing client
authentication fails closed. The managed admission predicate is documented in
[fresh-new-installation.md](fresh-new-installation.md); it does not widen this
private S1 proof or turn a private permit into default-install authority.

Before any CPA network mutation, Manager commits one durable
`fresh_s1_initial_key_install_v1` attempt in the existing SQLite settings store.
It contains only Canonical APIKeyID, run ID, generation and time. A unique insert
serializes concurrent consumers. A process crash, cancellation, nonempty source,
lost reply or unknown outcome consumes this attempt permanently. Reopening
Manager, choosing another Key or sending another POST cannot retry it. No reset,
replace, revoke, clear-last-Key or rollback endpoint exists.

The CPA transport is in `service/cpa`, pinned to `127.0.0.1:8317`, without a
proxy or redirects. It accepts only the exact empty private seed, optionally
with the single fixed loopback synthetic provider, then rechecks the empty Key
list and issues at most one PUT of one Key. Readback never grants live effect.
In particular, arbitrary root `api-keys` provider groups cannot pass the seed
guard. `null` safety booleans do not mean `false`.

## Four independent facts

| Fact | Evidence | Meaning / invalidation |
| --- | --- | --- |
| Manager staged | Existing canonical encrypted record | Saved in Manager; makes no CPA claim |
| CPA configured | Fresh authenticated CPA Key-list readback equals that one canonical secret | Configuration projection only; not live authentication or disk durability |
| CPA live effect observed | Three real CPA auth sweeps over at least 500 ms: absent/wrong **401**, valid **200**; Runtime checked before/after | Exact generation and `observedFromMs` / `observedUntilMs`; expires after at most five seconds or host lease expiry |
| Ingress authorization | Always `false` in S1 | Model requests remain **503**, including a valid CPA Key |

GET always makes new read-only observations. No effective result is persisted or
served from a cache. A missing/expired permit, unavailable observation, mismatch,
changed configuration or failed authentication cannot return an effective
claim. General Key metadata changes from `not_applied` to `effect_unknown` after
an attempt; it never advertises effective state. Full Web displays the four
facts, exact generation/window and a timer that retires the temporary observation.
After an unknown POST, its only recovery action is a read-only status check.

## Reproduction

Build only the task's opt-in proof images; do not run default Compose:

```sh
revision=$(git rev-parse HEAD)
docker buildx build --platform linux/amd64 --target v8-amd64-loopback-proof \
  --build-arg VERSION=dev --build-arg SOURCE_COMMIT="$revision" \
  -f Dockerfile.runtime --load -t cpamp-s1-runtime:local .
docker buildx build --platform linux/amd64 \
  --build-arg VERSION=dev --build-arg SOURCE_COMMIT="$revision" \
  -f Dockerfile.manager-server --load -t cpamp-s1-manager:local .
docker buildx build --platform linux/amd64 \
  --build-arg VERSION=dev --build-arg SOURCE_COMMIT="$revision" \
  -f Dockerfile.ingress --load -t cpamp-s1-ingress:local .
python3 bin/ci/fresh-s1-docker-proof.py bin/tmp/s1/new-proof-directory
```

`python:3.13-alpine` must already be available locally. The script never selects
an existing container or volume. It destroys only resources it created. The
official pinned archive SHA256 is
`50ecffb47fdd81c8c5a9825a73a7a905ab66342337e274f39c4276b92d3533f3`;
the executable SHA256 is
`b682e9e42586263f476888361514f68f89ff4ebf89a5dadba394b850b797ce41`.
The official executable is never modified or rebuilt from CPA source.

The script exercises canonical initialize/claim/Admin/routing/Strategy before
installation. A fixed, in-memory HTTP synthetic upstream on loopback records
actual chat-completion arrivals. It checks the official empty-list behavior,
initial installation, no-Key/wrong-Key/valid-Key results, Ingress nonarrival,
disk persistence, Manager reload, duplicate-write denial and Runtime generation
change. It never runs the older replace/clear-last-Key experimental probe.

The namespace gate resolves Docker's `container:<reference>` through Docker
inspection, including names, full IDs, ID prefixes and indirect namespace joins.
Sanitized evidence records each consumer's observed `NetworkMode` and resolved
Runtime ID. An unresolved reference prevents qualification. The explicit negative
fixture creates an untrusted consumer using the Runtime's **name**:

```sh
python3 bin/ci/test_fresh_s1_docker_proof.py
python3 bin/ci/fresh-s1-docker-proof.py bin/tmp/s1/name-join-negative --untrusted-name-join
# Expected: nonzero exit, result STOP, absent permit and empty CPA Key list.
```

Run the positive proof separately with a new output directory and new resources.
Before and after installation, it also sends `stream=true` model requests through
Ingress with absent, wrong and valid Keys; all must return **503** with zero
synthetic upstream arrivals.

During a read-only Full Web refresh, the previous snapshot and generation/window
remain visible with a loading indicator. Its original expiry still retires any
live-effective claim while GET is pending. A failed GET immediately retires that
claim; POST continues to clear the snapshot and consume the UI's one-time action.

Optional `--ui` pauses before installation and exposes a temporary host-loopback
relay to **Ingress only**, for an authorized browser test. `ui.json` contains its
URL. A task-local, in-memory synthetic Admin handoff at `/__s1-test-admin` supports
that test; it is a harness route, never a product endpoint. Create `ui-done` in
that unique output directory after the browser installs the Key. The relay has a
120-second deadline and is then closed. No CPA/control port is published.

## STOP

S1 proves bounded private configuration and CPA authentication only. Remote-host
and routed IPv6 qualification, malicious host/daemon defense, S2 per-request
Ingress authorization, cross-generation recovery, S2/External/Upgrade and any
release remain outside scope. After Runtime restart, CPA stays offline and the
existing Fresh generation mismatch **409** must remain intact. Destroy that
disposable run; do not repair it by clearing its attempt or changing its target.
