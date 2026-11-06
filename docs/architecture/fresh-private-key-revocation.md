# Private Canonical Key revocation and necessary replacement

Implements the maintainer-approved [Plan #64](https://github.com/seakee/cpamp-plan/pull/64)
slice on official CPA **v8.0.55**, Linux/amd64, disposable Fresh Embedded only.
The existing Full Web Key page prepares and continues a canonical-ID operation.
This is default-off private functionality. Real Provider, target deployment network
and manual acceptance remain **LIVE NOT RUN / BETA HOLD** under Plan #63.

## User operations

- **Revoke:** select the target and a different already effective Canonical
  APIKeyID. Manager verifies the two-Key CPA configuration and live authentication
  before removing the target. A staged-only keeper and the last effective Key
  are refused before a CPA write.
- **Necessary replacement:** first create and save an independent replacement
  through the existing Key creation action. Select old and replacement in the
  same page. One durable operation performs `[old] → [old,replacement] →
  [replacement]`. The second step requires observed replacement authentication
  and a new Host grant. Ingress refuses traffic during the unresolved operation.

The revoked identity becomes `superseded`; its encrypted record, name, ID and
history remain. Replacement has its own ID. List and detail show retained revoked
history, without offering initial installation again. A committed result records
what was observed; it is not a persistent live-auth verdict. Final keeper traffic
still uses the existing S2 per-request configuration/auth/generation fences.
Already in-flight requests may finish.

## Authority and isolation

`CPAMP_PRIVATE_KEY_OPERATIONS=1` only opts the existing bounded S1 wiring into
these operations. It grants no write authority. Default Compose, External,
Upgrade, real user volumes and public Gateway are outside this feature.
No CPA/internal port is opened by the product changes.

The trusted Host must own every reachable config, Key, file/watcher and reload
writer throughout the operation, including Management credentials and every
consumer of its source volumes or network namespace. A Manager mutex or Plan
merge cannot establish this. The disposable proof launcher verifies unique new
volumes, internal network, no published ports, known namespace/mount consumers,
official executable SHA256, ready Runtime generation and saved source bytes.
Host root, Docker daemon and the serial launcher remain trusted; they must not
attach another writer while a grant is live.

Each write needs a separate root-owned regular non-symlink **0600** file at
`/run/cpamp-s1/key-operation.json`, mounted read-only in Manager:

```json
{
  "operationId": "<original operation ID>",
  "step": "add or revoke",
  "runId": "<original S1 run>",
  "generation": "<same ready Runtime generation>",
  "sourceSHA256": "<SHA256 of current saved config.yaml bytes>",
  "issuedAtMs": 0,
  "expiresAtMs": 0
}
```

Example timestamps are placeholders, not a valid permit. Grants last at most
60 seconds and must postdate operation creation and this Manager startup.
S1's provisioning permit is still required but cannot authorize either KU step.
Each step revalidates source, actual effective Keys, generation and grant before
claiming intent, then authority again immediately before its single PUT.
Official v8.0.55 serves an `ETag` on this endpoint and honours `If-Match` when it
is supplied (a stale precondition answers 412 `precondition_failed`); this
Manager sends no `If-Match`, so the source hash remains a Host ownership binding,
**not** upstream compare-and-swap.

## Journal and failure recovery

The existing settings table stores `fresh_key_operation_v1:<id>` and its latest
pointer. There is no new schema, secret store or task queue. Each step moves from
`unattempted` through durable `attempted` to read-only `observed`; `returned`
records whether transport returned, including errors. An attempted step's
result stays unknown until current CPA configuration and three spaced live
positive/negative auth sweeps agree. The final identity transition and operation
commit share one SQLite transaction. The original S1 journal is unchanged.

Timeout, disconnect, 409, asynchronous reload and Manager restart require GET
reconciliation of the original operation. An attempted unknown step is never
resent, even if no CPA change can be seen. No new operation, S1 reinstallation
or inverse write can bypass that journal. Only an original **unattempted** next
step can continue after reconciliation and a fresh per-step Host grant. Changed
generation/source/writer or unavailable evidence remains UNKNOWN/STOP. Disable
the opt-in to stop operation/admission; preserve CPA's nonempty configuration,
all journals, canonical history and `data.key`. Never roll back by restoring the
old client Key automatically.

Authenticated Admin endpoints under the existing control prefix:

```text
POST api/v2/api-keys/<target-id>/revocation
     {"requestId":"<operation-id>","keeperId":"<canonical-id>","mode":"revoke|replace"}
GET  api/v2/api-keys/operations/latest
GET  api/v2/api-keys/operations/<operation-id>
POST api/v2/api-keys/operations/<operation-id>/continue  {}
```

Responses separate persisted step/outcome (`pending`, `unknown`, `committed`),
fresh bounded `runtimeState` and the next unattempted step. Rejections use 4xx;
unavailable/unknown observations use 503. Neither HTTP 200 from CPA nor config
readback alone means effective. The UI never persists credentials or journals in
browser storage and recovers latest on login/reload.

## Reproduction and acceptance limits

Build the three private images as in the [S1 instructions](fresh-private-s1-key-install.md),
using a task-specific `cpamp-ku` prefix/tag. Then, only with explicit approval of
the disposable private writer scope:

```sh
CPAMP_PROOF_IMAGE_PREFIX=cpamp-ku CPAMP_PROOF_IMAGE_TAG=local \
CPAMP_PROOF_KEY_WRITE_APPROVED=plan64-private \
python3 bin/ci/fresh-s1-docker-proof.py bin/tmp/ku-unique-api --ku

CPAMP_PROOF_IMAGE_PREFIX=cpamp-ku CPAMP_PROOF_IMAGE_TAG=local \
CPAMP_PROOF_KEY_WRITE_APPROVED=plan64-private CPAMP_PROOF_KU_BROWSER=1 \
PLAYWRIGHT_MODULE=/absolute/path/to/playwright PLAYWRIGHT_CHANNEL=chrome \
python3 bin/ci/fresh-s1-docker-proof.py bin/tmp/ku-unique-browser --ku
```

The acknowledgment variable is not authorization. The browser harness relays
only task Ingress to Host loopback, keeps synthetic credentials in memory and
destroys its server and all task containers/volumes/network. It is not a product
management endpoint. Offline success uses a deterministic synthetic Provider;
it does not prove real Provider access, normal deployment, arm64 or Beta readiness.
Retained developer observations and verification are linked in
[the evidence index](evidence/fresh-private-key-revocation/README.md).
