# Private Fresh configuration authority and recovery

This implements the private B0/B1 seam authorized by
[Plan #65](https://github.com/seakee/cpamp-plan/pull/65), against Plan main
`6c217236b62e2c76558108859887ecd15d4a540d`. It adds no B2 editor, public
configuration endpoint, Key replacement/revocation, or production qualification.

The profile is **off by default**. The Host selects
`CPAMP_PRIVATE_CONFIG_AUTHORITY=1` on both Runtime and Manager, using only the
Linux/amd64 `v8-amd64-loopback-proof` image and official CPA **v8.0.13**
(`sha256:b682e9e42586263f476888361514f68f89ff4ebf89a5dadba394b850b797ce41`).
The separate private composition has no updater, artifact selection or adoption
writer. Ordinary Runtime composition and its existing qualification bytes,
`v8-entrypoint.sh` seed equality, Ingress and pre-install seed validators remain
unchanged.

## Host ownership

The trusted launcher creates new, separate Manager `/data` and Runtime
`/runtime` volumes and an internal network. Before the first CPA child, it
prepares the approved Provider seed and a prehashed temporary Management
credential. This avoids CPA's startup plaintext-to-bcrypt file mutation.

The Host mounts root-owned `0600` `/run/cpamp-config/source.json` read-only
into both services. It names source ID, Runtime generation, network namespace,
configuration device/inode, original seed digest and a lease of at most ten
minutes. Initial admission requires a stopped child, that exact seed and a new
authority directory. Existing/missing/corrupt authority records cannot be
silently replaced by re-admission. Every write, reconciliation and spawn checks
the source again.

**Trust boundary:** Docker daemon/root and the serial launcher must maintain
exclusive volume, namespace and credential ownership throughout the lease.
They must not introduce another writer, writable alias, credential holder or
uncoordinated lifecycle action. A permit, a digest or a one-time consumer scan
does not establish continuous exclusivity. The disposable launcher controls
the resources it creates; this is not an arbitrary-host attestation mechanism.
If the Host cannot maintain this condition, writes remain STOP.

CPA's Management credential is held only by the Host and the existing scoped
Manager consumer. Private ports stay loopback-only; no Docker socket or
writable Runtime mount is given to Manager/Ingress. Host-driven negative
fixtures run only in disposable resources. Existing S1 permit, generation,
Provider binding and AuthSweep remain necessary for S1/S2 authorization.

## Operation and recovery

Runtime's root-only `/runtime/supervisor/private-config/record.json` holds one
current operation and the complete last-good configuration. This is recovery
material, including secrets; protect the Runtime volume as configuration data.
It is not a second canonical Key store or a Manager-owned settings source.
The Manager volume does not contain this snapshot.

The private authenticated Runtime route is
`POST /v1/runtime/private-configuration`. It accepts no file path, YAML,
Management credential or arbitrary field. Its actions are:

1. `admit`: persist the Host's initial validated source while CPA is stopped.
2. `observe`: return metadata only, including source, generation, revision,
   raw file digest and `persisted`/`unknown`.
3. `begin`: compare the revision and exact raw digest; durably save the
   original operation/target before permitting one official CPA write.
   Initial-Key and retry share this boundary and the lifecycle executor lock.
   An unresolved operation denies all further reservations and CPA spawns.
4. `commit`: validate the complete before/after YAML, flush the admitted inode,
   then atomically persist the new last-good and terminal receipt. Only the
   requested single initial Key or exact retry scalar may differ.
5. `recover`: after Stop/reap, require the Host to name the original operation
   and the currently damaged raw digest, then restore its trusted last-good to
   the same owned inode. No running, shared, replaced or unknown file is
   blindly overwritten.

Corruption discovered between completed operations also requires an explicit
stopped Host decision anchored to the last operation; recovery intention is
persisted before restoring. Missing/invalid recovery material remains STOP.
An uninstalled source damaged before any operation is reprovisioned in new
disposable resources, rather than guessed or silently re-admitted.

Snapshots/intents use file fsync, rename and directory fsync. Missing, null,
empty, false, zero, ordered lists, unknown fields and secret values are compared
as tagged YAML nodes; duplicate keys, aliases and anchors are rejected. YAML
comments and map formatting/order may change through official serialization.
An independent committed retry allowance survives recovery of a later failed
operation.

CPA remains the normal mutation writer through its official scoped API. Its
whole-file `O_TRUNC` is **not** atomic and provides no client CAS. Runtime's
revision is a local reservation fence, not an invented upstream ETag. Runtime
does not submit PUT. Its only direct file write is explicitly Host-authorized
recovery with the CPA writer stopped.

S1 persists its existing one-shot Manager attempt, reserves Runtime intention,
issues the official Key PUT once and commits the full-file Runtime receipt.
If the response/receipt is lost, the existing S1 observation reconciles the
original deterministic operation; it never repeats PUT. A pre-write or partial
result stays unknown until controlled recovery. A failed recovery or terminal
record save leaves the original unknown operation for another explicit Host
decision.

Official ConfigV8 YAML GET normalizes layout, so it is not used as a raw-byte
durability receipt. Runtime reads the actual admitted file; redacted JSON GET
cannot prove secret/full-document preservation.

After Supervisor restart, Host must renew the same source binding for the new
generation. An unknown write can be reconciled **while stopped** only when the
Host binds its original operation and exact current fingerprint. Valid target
bytes may be committed without replaying PUT; otherwise controlled last-good
recovery is required. CPA can then be explicitly started. This does **not**
rebind the old Manager Fresh checkpoint or S1 permit: old-generation S1/S2
admission remains denied. That consumer recovery is a separate unfinished
boundary.

`persisted` means the owned file and recovery receipt are durable.
`effective` is never inferred from it: the existing short-lived Key AuthSweep
proves only its current generation/window. Retry execution behavior requires
B2-specific acceptance. Timeout, mismatched source, corruption and unresolved
outcome do not become effective claims.

## Reproduction and acceptance

Use Host `htpasswd`, Docker and locally available `python:3.13-alpine`:

```sh
revision=$(git rev-parse HEAD)
docker build --platform linux/amd64 --target v8-amd64-loopback-proof \
  --build-arg VERSION=dev --build-arg SOURCE_COMMIT="$revision" \
  -f Dockerfile.runtime -t cpamp-b1-runtime:local .
docker build --platform linux/amd64 \
  --build-arg VERSION=dev --build-arg SOURCE_COMMIT="$revision" \
  -f Dockerfile.manager-server -t cpamp-b1-manager:local .
docker build --platform linux/amd64 \
  --build-arg VERSION=dev --build-arg SOURCE_COMMIT="$revision" \
  -f Dockerfile.ingress -t cpamp-b1-ingress:local .
python3 bin/ci/fresh-b1-docker-proof.py bin/tmp/b1/unique-s2a --s2a
python3 bin/ci/fresh-b1-docker-proof.py bin/tmp/b1/unique-s2b --s2b-offline
CPAMP_PROOF_IMAGE_PREFIX=cpamp-b1 python3 bin/ci/fresh-s1-docker-proof.py \
  bin/tmp/b1/unique-default-off
```

The extension reuses the unchanged S1/S2 launchers and their existing negative
cases. The Host test driver alone exercises the reserved retry scalar; no
Manager scalar adapter or Web editor is installed. Reports contain sanitized
case results, not credentials or recovery snapshots. Only task-created
resources are cleaned.

Source tests cover simultaneous reservations, source/generation/namespace
drift, foreign inode, hardlink/symlink, invalid mode/lease, damaged/missing
record, invalid last-good, interrupted writes and full-file/secret preservation.
Docker exercises real S1 install, CPA restarts after scalar changes, competing
scalar reservations, Manager restart with an unknown result, partial-file
injection, stopped-writer recovery and Supervisor restart/original-operation
reconciliation. It retains the S1/S2 identity/authentication negatives.

Local acceptance on the implementation delivered with this document:

| Check | Result |
| --- | --- |
| B0/B1 with S2-A | PASS: 9 extension cases; 253 original events |
| B0/B1 with offline S2-B | PASS: 9 extension cases; 150 original events |
| Standalone S1, authority disabled | PASS: 108 original events |
| Manager/Runtime/Ingress Go tests, vet and build | PASS |
| Runtime full race; Manager CPA/S1/Key race | PASS |
| Runtime Linux amd64/arm64 and Windows amd64 compile | PASS |
| Runtime architecture boundary; packaged Full Web build | PASS |

Real Host/network qualification, actual power-loss/storage-error injection and
LIVE Provider/retry execution acceptance are **NOT RUN**; public Beta remains
**HOLD** under [Plan #63](https://github.com/seakee/cpamp-plan/pull/63).
S2-B's per-Provider retry override remains unchanged and cannot qualify global
retry behavior.

B2 can build on this private seam after integrating a scoped Manager scalar
consumer, its admin UI, effect observations and failure handling. Before any
combined Key K/U + config profile, the separately owned
[Plan #64](https://github.com/seakee/cpamp-plan/pull/64) writers must use the same
reservation boundary; this PR does not enable them or change their ownership.
Broader/shared/External configuration sources and automatic cross-generation
S1 recovery remain STOP.
