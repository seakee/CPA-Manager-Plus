# Private H1: unified Key / Config raw-source authority

This is an opt-in **implementation candidate**, not a deployment qualification.
Baseline: product `c5c5d852983b5354b4798ed457b522c978dc37c0`, Plan
`a7ec29fed446a4d76fd27e03c2c62a4ee2969eba` (#64/#65/#67/#73), official
CPA v8.0.55 (`e6e5708e677adb2463d45accdd24bc9fd028ddd8`). CPA is unchanged.
An integrated Draft PR does not authorize a live write or a Beta release.

## Owners and deployment fence

| Owner | Sole authority |
| --- | --- |
| Runtime `privateconfig` | Admitted inode/raw SHA, generation/revision, one durable parent, at most two step receipts, original last-good bytes |
| Manager Key | Canonical IDs/encrypted secrets, keeper/target selection, immutable business terminal, real authentication sweeps |
| Manager Config | Original request ID/value, existing Fresh SQLite journal, persisted vs effective distinction |
| Shared Manager adapter | One bounded handoff checkpoint in the **existing** SQLite `settings`, referencing business terminals, receipts and release anchor |
| Trusted Host operator | Continuous Docker/root ownership of volumes, bind aliases, namespaces, credentials and all reachable senders; exact shutdown before recovery |
| Official CPA | The only **normal configuration writer**, through the existing exact Key-list PUT and retry-scalar PUT |

The private Linux/amd64 loopback composition remains mandatory. Manager requires
`CPAMP_PRIVATE_H1=1`, `CPAMP_PRIVATE_CONFIG_AUTHORITY=1`,
`CPAMP_PRIVATE_KEY_OPERATIONS=1` and the existing exact S1 permit/addresses.
Incorrect H1 flag combinations disable these consumers. Standalone profile
fences remain; an H1 record cannot fall back to standalone transport. Public API
and browser requests cannot mint Host grants or supply a different source,
credential, URL, YAML document or recovery payload.

## Fresh-source takeover, not migration

Host sets `bootstrapRun` when admitting a **new empty task source**, before any
CPA child starts. S1 keeps its original one-shot install. Only the exact v1
revision-3 initial-key persisted record, same generation/raw bytes, successful
live Key observation, durable Manager initial-consumption checkpoint and an
exact Host `activate` grant permit v2 takeover. Unknown, drift, missing provenance
or an old deployment cannot be retrofitted. No second initial-key grant exists.

Legacy v1 records remain readable by the new loader. v2 uses `version:2` and an
explicit `h1` record; the old strict decoder/version check rejects it. **Do not
downgrade a v2 source to the old binary.** Preserve its volumes and receipts.
Old-generation Key parents never resume, including after file recovery.

## Protocol and durable order

One raw source has zero or one active parent:

```text
FREE -> HELD -> TERMINAL_CONSUMED -> RELEASED/FREE
          |               |
          +-- unknown -----+  (no TTL-driven release or new parent)
```

Key replacement uses `k:<requestId>` and `:0:add`, `:1:revoke`; direct revoke
uses `k:<requestId>:0:revoke`. Config uses `c:<requestId>:0:request-retry`.
Replacement holds the **same parent** after add persists, across Manager death,
and until revoke is observed and the business terminal is durable. Runtime
metadata revisions are consumed from responses, not guessed as `old+1`.

1. Host issues the exact `claim` delegation (request ID/intent, source,
   generation, original raw SHA and revision). Runtime persists claim first.
2. Manager saves its held checkpoint and original business intent. Failure
   after claim leaves an **orphan held parent**, never a reusable grant.
3. Manager saves step attempted **before** Runtime begin and before any PUT.
4. Runtime persists the stable step ID/before-image/transition digest, unknown.
5. Manager rechecks target, exact Host grant and Runtime reservation, then sends
   **one** bounded CPA PUT. Key needs a separate fresh v2 Host permit per step.
6. Only a complete HTTP 200 becomes durable Manager HTTP-success evidence. A
   transport failure or failure saving that evidence stays unknown. No resend.
7. Runtime validates the complete YAML semantic tree, fsyncs the admitted inode
   and durably commits the step receipt. A receipt explicitly binds parent,
   step, source, generation, before SHA/revision, scope, transition digest and
   after SHA/revision. It is not a live Key or effective B2 acknowledgement.
8. The original domain saves its immutable business terminal. Key additionally
   requires keeper acceptance/target rejection and the existing live sweeps.
9. Adapter durably saves terminal digest/receipt references, then Runtime
   consumes that terminal and persists `terminal-consumed`.
10. Runtime releases the exact parent and retains one bounded release anchor;
    Manager confirms the anchor in SQLite. Only then is either domain writable.

Config's exact claim also delegates its single step. This lets Host authorize
the **original** request ID/value before the existing Public PUT; there is no
new Public prepare API. The launcher uses the existing GET `expected` fields,
issues the Host-only file, and submits that exact request. Wrong ID/value does
not inherit the delegation. Per-step or exact-Config grants can finish their
own terminal; a Host `reconcile` grant can authorize original-operation
bookkeeping after expiration, without authorizing another CPA PUT.

## Interruption and cross-domain acceptance

| Cut / condition | Disposition |
| --- | --- |
| Claim response lost or business journal insert fails | Runtime parent remains held; no automatic orphan release or new operation |
| Attempted before begin / begin response lost / PUT response lost | Original operation unknown; no repeat PUT, no other-domain admission |
| Runtime commit response lost | Observe original persisted step/receipt, never repeat PUT |
| Config terminal SQLite save fails | Keep original HTTP evidence and Runtime receipt/parent; reconcile only original terminal |
| Manager terminal durable, consume/release response lost | Replay only idempotent bookkeeping using original terminal digest and receipts |
| Delayed release after another parent starts | Reject; cannot clear the new owner |
| Manager restarts between add and revoke | Original parent and add receipt survive; new step needs a new exact Host permit |
| Host grant expires while PUT is in flight | Expiration is not cancellation; keep parent/unknown, stop new admission |
| Runtime/launcher disappears, source/gen changes or termination cannot be proven | STOP; no restart, handoff, restore or generation-based Key continuation |

Key→B2 and B2→Key require the same source/generation, no Runtime parent, matching
Manager release anchor and terminal journals in both domains. Raw persisted add
alone is insufficient. Runtime startup also rejects any active parent, unknown,
old generation or missing exact `start` grant. TTL never releases ownership.

H1 KeySnapshot brackets CPA semantic reads with Runtime observations; its hash
is **Runtime raw SHA**, never the CPA YAML GET serialization hash. The old GET
fingerprint stays only in the fenced standalone path. Only the exactly committed
B2 integer is projected out before unchanged S1/Provider seed checks; missing,
null, different scalar, extra routing/root or Provider drift is rejected.
Key add only appends keeper; revoke deletes only target from either two-Key
ordering, leaving keeper. Full-tree comparisons protect other keys, Provider,
Management secret and opaque/plugin semantics. Unknown/unrepresentable data
fails closed, not silently normalized away. B2 effective remains `unknown`.

## Host H1 control and recovery boundary

`bin/ci/fresh-s1-docker-proof.py --h1` is the existing serial S1 owner with a
small H1 adapter (`fresh_h1_proof.py`). It is off by default and additionally
requires `CPAMP_PROOF_H1_WRITE_APPROVED=plan67-private`. **That acknowledgement
is not authorization**: run only after a trusted operator explicitly authorizes
a dedicated disposable experiment and verifies its complete resource graph.
Do not invoke the driver against any existing deployment or production source.

The driver creates new Runtime/source, token, Manager, S1-permit and H1-grant
volumes and an internal network; no private port is published. It prehashes the
Management secret before first CPA start. Manager receives no source mount or
Docker socket; grant/credential volumes are role-specific and read-only to
consumers. Audits resolve full Docker IDs, namespace consumers, named volumes
and bind ancestors/descendants, reject unknown consumers, unexpected mounts,
privilege, shared host namespaces, published ports or automatic restart.
These audits detect errors: **they do not prove continuous exclusion**. The
trusted root/Docker operator must prevent non-cooperative writer entry for the
entire operation window. The passive meter and probes remain trusted ephemeral
fixtures, not additional credential recipients/configuration writers.

On normal/error exit the H1 driver first stops original sender containers by
full IDs and verifies wait/state/PID=0. A fresh control-only probe requests the
existing typed Runtime Stop (`lifecycle/stop.go` → `cpaprocess.Terminate`, exact
child Kill+Wait). If Supervisor is unavailable, trusted Host stops/kills/waits
the **exact Runtime container**, verifying its process containment is ended.
The driver never restores/restarts afterward. It retains containers, volumes,
journals, source and receipts, and writes a nonsecret `h1-owner.json` manifest.
No automatic cleanup may erase an unconsumed receipt.

SIGKILL cannot execute `finally`: original resources may remain alive. Admission
expires, ownership stays durable/unknown, and no other launcher is allowed to
adopt them. Before any controlled recovery the trusted operator must use that
exact manifest, terminate original Manager/probes/senders and exact Runtime/CPA,
prove stop/reap plus resource exclusion, and explicitly authorize the original
source/gen/operation/current damaged SHA. Runtime's stopped-only recovery may
restore original last-good bytes but **keeps the parent held** and does not
declare Key success. An unproven sender/CPA, unknown manifest, missing credentials
or generation drift stays STOP. Disposal is a separate operator action after
evidence/receipt retention; no blanket Docker cleanup is part of H1.

## Verification and remaining acceptance

Offline tests cover Runtime CAS/receipt/record compatibility, strict tree
mutation, both Key orders/last-Key, same-value Config, bidirectional handoffs,
Manager restart between steps, lost bookkeeping replies, orphan claim, SQLite
HTTP-evidence/terminal failure, strict projection, Host alias/namespace/resource
rejection and exact stop/kill/wait. Adapter transport models are explicitly
**not official CPA or Docker evidence**. Existing Key/Config/Ingress/Chat/S1 and
Provider regression tests remain in their respective suites. Required CI adds
these offline Host tests and the focused Manager race paths.

Still **NOT RUN / HOST HOLD** for this implementation batch: official combined
CPA/Docker, launcher SIGKILL, in-flight PUT past grant expiry, Supervisor loss,
old-sender lateness, true O_TRUNC partial write/recovery and continuous Host
exclusive control. The driver includes the combined happy sequence; failure
scenarios require separately authorized disposable experiments, not fabricated
PASS entries. Docker Desktop evidence never qualifies native Linux.

Before Beta under Plan #63: independently review these mechanisms, run dedicated
combined/failure experiments with exact image/source/command/exit evidence,
validate native Linux/amd64 Host containment and continuous resource ownership,
then perform real Provider/network/effect, Key/AuthSweep/Ingress and Config→Key/
Chat acceptance. Existing staged gates and release pairing remain unchanged.

No CPA fork/API change, general YAML editor, second normal writer, second Key
database, background transaction worker, watchdog or generic orchestration/RBAC
platform is introduced. Remaining unknowns are deployment evidence, not reasons
to remove validation or claim automatic recovery.

### Local validation ledger (2026-10-21)

Local toolchain: Go 1.26.1 darwin/amd64, Python 3.13. CI retains its existing
Linux/Go 1.24 qualification; local results do not substitute for it.

| Directory | Actual command | Result |
| --- | --- | --- |
| `apps/manager-server` | `go test ./...` | PASS, exit 0; focused regressions repeated after final edits |
| `apps/manager-server` | `go test -race ./internal/service/cpa ./internal/service/freshkeyapply ./internal/service/freshconfig ./internal/repository/freshstore ./internal/repository/freshkeys ./cmd/cpa-manager-plus` | PASS, exit 0 |
| `apps/manager-server` | `go test -race ./internal/service/cpa -run H1 -count=1 -v` | PASS, exit 0; no cached H1 results |
| `apps/manager-server` | `go vet ./...` | PASS, exit 0 |
| `apps/runtime-supervisor` | `go test ./...` | PASS, exit 0 |
| `apps/runtime-supervisor` | `go test -race ./...` | PASS, exit 0 |
| `apps/runtime-supervisor` | `go vet ./...` | PASS, exit 0 |
| `apps/ingress` | `go test -race ./...` | PASS, exit 0 |
| root | `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s bin/ci -p 'test_fresh*_proof.py' -v` | PASS, 4 offline tests, exit 0 |
| root | `git diff --check` and changed-path classifier CLI | PASS, exit 0 |

Intermediate negative results were not counted as PASS: the added continuous
B2→Key→B2 test found a real checkpoint before-image alias/CAS failure; it was
fixed and the test passed under race. A later journal-validation edit briefly
failed compilation due to an extra brace; it was corrected and the affected
suites passed. Classifier test discovery initially used a nonexistent filename
and the CLI does not implement `--help`; neither invocation was a test result.
`node --test tests/prCheckClassifier.test.mjs` could not start because the clean
worktree has no installed `vitest` (exit 1); frontend dependencies were not
installed or changed, and the existing Required CI owns that verification.

No real CPA process/config write, disposable Docker experiment, Host failure
injection, frontend browser check or native Linux qualification was performed
by these local commands. Review the Draft PR's exact-SHA CI separately.
