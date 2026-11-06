# Private Fresh global request retry API (A1)

This is the Plan [#67](https://github.com/seakee/cpamp-plan/pull/67) A1 consumer
of [#65](https://github.com/seakee/cpamp-plan/pull/65) and product #1032.
The implementation base is `v2@f03d537674c83e6dbf92d89b6f7df03d1e81439a`;
the Plan contract is `main@129dd94065e7549c811a6e83e3e7d86c82148974`.
A2 Full Web and I0 combined Key/Config writing remain separate tasks.

## Qualification and ownership

Default is **off**. Only a trusted Host-created, continuously exclusive private
Linux/amd64 Embedded Fresh source with official CPA v8.0.55 qualifies. Consume
the existing #1032 root-owned read-only source grant, exact Runtime raw-file
SHA256/revision/generation, S1 permit/installed Key and target identity checks.
No ordinary Docker, External, Upgrade, adoption, public Gateway or release
qualification is granted. See [B0/B1 authority](fresh-private-config-authority.md).

The serial Host owns root/Docker, all task-created volumes, mounts, namespaces
and Management credential holders for the **entire** grant lease. It must never
introduce a second Manager/CPA, writable alias, generic Management access,
valid #1031 K/U step permit or other config writer. Renewing a grant continues
the same obligation. The A1 disposable launcher never invokes K/U or issues a
valid K/U permit; only Manager and the trusted serial test driver hold the
ephemeral Management credential. Test-driver operations are sequential with
the Public API operations, never concurrent writers. Containers receive no
Docker socket. Network peers receive no management credentials or Runtime
data mounts. Only task-created resources are selected and cleaned.

Missing/inconsistent source ownership means **STOP**. Environment switches,
Manager mutexes, a file absence check, a consumer scan and readback do not
attest continuous Host isolation. Runtime only knows `initial-key` and
`request-retry`; it does **not** implement K/U cross-domain exclusion. As an
additional rejection fence, a Config profile disables the independent K/U
consumer, and Config admission rejects a requested K/U profile, a reachable
K/U permit artifact or any K/U journal. This is not I0 or general host attestation.

## Administrator API

All paths below are relative to the verified canonical
`<controlBasePath>/api/v2/`. The existing persisted Administrator Bearer
credential is mandatory, including observation and reconciliation. CPA
Management/Client keys and Bootstrap sessions cannot authorize this API.
Responses use `Cache-Control: no-store`.

| Method/path | Meaning |
| --- | --- |
| `GET gateway/retry/request-retry` | Observed value/presence, source/authority, editable/reason, expected source metadata, persisted and effective states |
| `PUT gateway/retry/request-retry` | One original request, one explicit scalar mutation |
| `GET gateway/retry/operations/<requestId>` | Durable original operation metadata; works without a current Host permit |
| `POST gateway/retry/operations/<requestId>/reconcile` | Empty JSON object; observe/commit original operation only; never PUT/recover/rebind |

Read preserves `presence: missing | null | present | unknown`; `value: 0`
remains distinct from missing/null. It does not materialize a default.
`expected` contains `sourceId`, `generation`, `revision`, `sha256` from Runtime's
actual owned file. The revision is a Runtime reservation fence, not upstream CAS.
A Manager-managed source has no Runtime fence: there `revision` is that field's own
journal revision, so writing another field, or any other Fresh record update, never
invalidates the expectation, while a repeated save of the same field still collides.
An unknown operation or missing journal locks further saves.
Malformed bytes of one field's journal entry remain untouched, lock only that field
and are written back verbatim by a later save of another field; unreadable
container bytes lock Config as a whole. Neither removes the existing Administrator
or unrelated Fresh domain access.

PUT body (substitute the complete `expected` object from a fresh GET):

```json
{
  "requestId": "0123456789abcdef0123456789abcdef",
  "value": 0,
  "expected": {
    "sourceId": "host-created-source-id",
    "generation": "7",
    "revision": 2,
    "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  }
}
```

`requestId` is 32 lowercase hex characters. Only explicit integer JSON values
0–10 are accepted. Null/missing, negative, greater than 10, decimal/exponent,
string, bool, list, object, unknown/case-folded/duplicate keys, trailing JSON,
query parameters and encoded paths are rejected before Runtime `begin`.
There is no clear/inherit, field-path, Provider override, YAML, secret or
client-supplied authority input.

Write/operation responses contain `operation` plus `effective: "unknown"`.
Operation `state` is `unknown`, `persisted`, `recovered` or `failed`; recovered
means the requested value may **not** be the current value. Reload the field.
`failed` is terminal and carries a `code`: `write_rejected` when CPA answers a
definite 4xx, `write_not_applied` when the scalar read back after an unknown
managed write differs from the intent. A failed original is never resent and the
field is writable again under a new request ID.
Replaying an identical request returns its original durable record, even
when unknown; it does not reconcile automatically. Changing value or original
expected metadata under the same ID conflicts. Historical IDs remain consumed.

Errors are bounded codes, without upstream messages or raw response bodies:
400 `invalid_retry_request`, 401 Administrator authentication failure,
404 `operation_not_found`, 405 wrong method, 415 unsupported content type or
encoding, 409 `source_or_operation_conflict`, `private_source_not_authorized`,
`config_key_writer_not_isolated`, `original_operation_unknown`, or
503 `configuration_unavailable`. A definite upstream refusal is reported as the
journaled operation's own terminal state, not as a separate reply code: the
operation records why the original never landed and the field is writable again.
An unknown result retains the original operation ID; query/reconcile it, never
submit a replacement to retry.

## Durable operation and recovery

1. Validate input and current target/Host admission, installed S1 Key and absence
   of K/U history/permit. Compare the expected Runtime source metadata.
2. Persist immutable intent/tombstone with existing Fresh SQLite CAS. This uses
   the existing store/transaction, without a new database or workflow engine.
3. Runtime `begin` durably reserves the original `retry-<requestId>` and
   protected last-good. Recheck Host target, lease, Runtime original operation,
   reserved revision and pre-write raw SHA256.
4. Issue exactly one official
   `PUT /v8/management/config/routing/retry/request-retry`. No v0 or YAML fallback.
5. Runtime `observe`/`commit` validates the **complete** configuration, preserves
   tagged presence/null/zero, Key/Provider/secrets/unknown/plugin opaque data,
   fsyncs the owned inode and persists its receipt. Then record the Manager
   terminal result. CPA still performs the actual whole-file `O_TRUNC` write.

Network timeout, lost response, cancellation, uncertain fsync, pre/post-commit
Manager restart or damaged YAML leave the original intent consumed. Reconcile
only records a matching Runtime terminal receipt or attempts the original
Runtime commit; it never reserves again or sends another CPA PUT. A failed
Manager terminal save can be reconciled from the Runtime receipt after restart.

If target bytes cannot be validated, Host must stop/reap CPA, renew the same
exclusive source lease and bind **the original Runtime operation and exact
current raw SHA256** before controlled Runtime `recover`. No Public API accepts
recovery permission or snapshot bytes. After recovery and an explicit CPA
start under the same admitted generation, reconcile the original Manager record.
Wrong permission, unknown inode/source, damaged/missing journal, invalid recovery
material or ownership drift remain STOP; there is no automatic inverse rollback.

A crash/timeout before Runtime's reservation can be established may leave only
the Manager intent. Observation does not prove a delayed `begin` cannot still
arrive. This API deliberately cannot reset that record or invent a Runtime
recovery receipt; the source remains STOP for trusted Host disposition/new
disposable provisioning. A Supervisor generation change likewise does not
rebind the existing S1 checkpoint: original-operation metadata stays readable,
but new writes and live S1/S2 admission remain denied. Neither limitation is
presented as automatic consumer recovery or production qualification.

## Effect and verification

File readback, HTTP 200 and Runtime persisted receipt never imply `effective`.
The API always reports effect unknown. The disposable S2-A test uses the existing
`s1-synthetic` Provider without credential-level retry override, deterministic
upstream failures and actual arrival counts for global 0 and 1. A controlled
500 in the fixed seed produced **1 and 1 arrivals**, so global retry effect
remains **unknown**. The seed omits `max-retry-interval`; official CPA rejects
a cooldown wait when its maximum is zero. A1 does not change another field,
Provider override or cooling policy to manufacture a positive witness.
S2-B keeps its credential-level retry 0 and
tests original Key/Ingress admission; it cannot qualify global retry behavior.

Run `python3 bin/ci/fresh-a1-docker-proof.py <new-output-dir> --s2a` and
`--s2b-offline` with task-built `cpamp-a1-{manager,runtime,ingress}:local`.
This extends all original B1 restart/corruption/recovery and S1/S2 positive and
negative cases, including unauthenticated peers. Reports contain sanitized
case results and source fingerprints, never Management/Client/Provider secrets,
raw YAML or Runtime recovery snapshots. Unit/race tests additionally exercise
independent Manager consumers, duplicate request IDs, lost replies, invalid
inputs, stale source metadata and no PUT replay.

**LIVE NOT RUN / BETA HOLD.** Independent exact-HEAD acceptance, real trusted
Host/network/storage failure qualification and the remaining Beta gates are
still required. Self-review and offline proof do not replace them.
