# Private Fresh New installation and recovery

This is the bounded Linux/amd64 Fresh New journey on `v2`, using the normal
official CPA v8 Runtime. It uses the existing Host initialization, Bootstrap,
routing and Runtime lifecycle contracts. Ordinary Compose model Gateway stays
Pending. CPA always starts with its installation-generated Client Key. This is
not a public deployment, Provider setup, Upgrade or five-path Beta qualification.

Use a disposable host/project and **new volumes**. Never attach an existing
installation, Agent B's volumes/network, or a production database. Preserve both
the SQLite database and `data.key` when retaining an installation.

## Operator journey

Run these commands from the selected product worktree. Keep the same project
name and environment for every Compose invocation:

```sh
export COMPOSE_PROJECT_NAME=cpamp-fresh-new-local
export CPAMP_PUBLIC_PORT=127.0.0.1:18317
export CPAMP_PUBLIC_ORIGIN=http://cpamp-ingress:18317
docker compose build
docker compose up -d --no-build
```

The image build uses the repository's normal Runtime target and its frozen
official CPA pin. Use a Linux/amd64 Docker engine for this acceptance profile.
Only the Ingress port is published, to host loopback. The explicitly configured
origin above lets Manager verify the same Ingress routes inside this private
Compose deployment; it is not a public-host or TLS qualification.

Before initialization the service waits for Host admission. Discovery is
read-only and does not authorize initialization or create an administrator.
Before Host admission, the bounded Bootstrap UI may show read-only source guidance if the startup inspection listener is active. This UI never grants a Fresh claim, Upgrade/Adopt permission or writer; unknown/blocked sources remain fail-closed.

```sh
docker compose stop cpamp-manager
docker compose run --rm --no-deps cpamp-manager initialize
docker compose up -d --no-build cpamp-manager
```

`initialize` must hold the existing database fence exclusively; do not run it
against a running Manager. It prints one relative Handoff URL. Open that path
and fragment under `http://127.0.0.1:18317` in your browser. Treat the output as a
one-time credential: do not paste it into issues, service logs or evidence.

1. The page removes the fragment before exchanging the claim. A consumed,
   expired or wrong-identity Handoff cannot initialize the installation.
2. Generate an Admin Key on the page, or enter a manually chosen key with
   24–256 UTF-8 bytes, no whitespace/control characters, at least three character
   classes and twelve distinct characters. Save it in a password manager and
   check the saved-key confirmation before submitting.
3. Submit administrator initialization. If the reply is lost, the page reads
   the actual Operation. If that read also fails, writes and key editing stay
   locked; use **同步服务端状态**. Do not refresh or replace the candidate key
   while the result is unknown.
4. Keep `/console` or choose an allowed Console path, then activate it. The
   existing routing contract persists the intent and verifies the real Ingress
   route, hashed assets and installation identity. A failed prepare/read cannot
   authorize a different target. The continue link appears only after committed
   route verification.
5. Follow the link and use the existing CPAMP login page with the saved Admin
   Key. Select **Remember credentials** to reuse the existing browser-local
   credential storage. Refresh revalidates that credential against the canonical
   Fresh operation before restoring the current Console route. Without this
   option, refresh requires another login. Logout or rejected authentication
   clears the saved credential; another deployment prefix cannot reuse it.

After signing in, the installation wizard continues the same durable operation:

1. Choose **New embedded installation**. External remains a Host-authorized
   connection checkpoint and cannot finish installation. The Fresh flow does not
   upgrade existing product data. The `adopt_existing`/`network_existing` service
   path is admitted (see [Adopt `network_existing`](#adopt-network_existing)),
   but the wizard still offers no Adopt action, so it stays unreachable from the
   browser in this lane.
2. Review the Console address and packaged official CPA version, then select
   **Check deployment**. Manager reads the Runtime through its existing
   Host-configured authenticated transport. There is no Runtime JSON input,
   browser-selected target URL or browser-granted Host permission.
3. Review the destination, acknowledge the installation and select **Confirm
   and install** once. The wizard prepares the target, starts CPA through the
   existing idempotent operation, checks readiness and saves the result. Every
   transition reads the persisted operation before advancing.
4. An interrupted or ambiguous reply shows **Result needs confirmation** and
   locks writes. Use **Check original operation**, or refresh, sign in with the
   same Admin Key and **Continue original installation**. A conflicting Runtime
   identity/generation requires Host reconciliation; it is never silently rebound.
5. **Enter Console** opens the original CPAMP layout, toolbar and business
   pages with two-level navigation. Client access contains API keys; System
   management contains the original configuration and update pages. Recoverable
   retry controls appear on the configuration page. Key and retry operations
   retain their pending state when navigating away and returning. Unsupported
   entries are hidden. Existing Key/Config writer permissions remain unchanged:
   a normal deployment cannot enable private writers through navigation.

Installation completion requires a committed Fresh New checkpoint and a real
Runtime observation of **ready**, matching the original identity, generation
and official artifact. It does not open the model Gateway by itself:
`gatewayPending` remains true and Ingress keeps answering 503 until a Client
Key is installed and Manager proves the admission posture in
[Gateway admission](#gateway-admission-ingress). Diagnostics are available in
expandable details instead of the normal user workflow.

## Restart and recovery

```sh
docker compose restart cpamp-manager cpamp-runtime cpamp-ingress
```

Reopen the saved Console path and log in with the same Admin Key. Manager restores
the same operation, completed routing and installation state; the saved setting
and `data.key` survive. Do not rerun `initialize` on a completed installation.
The fixed Bootstrap UI is retired and returns 404 after routing commits.

An independent Supervisor container restart creates a new Runtime generation and
currently leaves CPA offline. Console access still recovers. A replay of the
old installation target must return `409 managed_target_identity_mismatch` and
must not update the saved target. Host-controlled Runtime Start/Restart remains
the existing separate lifecycle operation: read the current Supervisor status
and reconcile its operation journal before any lifecycle retry. This UI does
not introduce automatic restart policy or silently rebind installation identity.

| Failure | Safe next operation |
| --- | --- |
| No Handoff / expired / consumed claim / lost exchange reply | Stop only this Manager, run Host `initialize` against the same owned store, restart Manager and use the new Handoff. Before routing commits, an initialized admin gets a resume session rather than a second initialization. |
| Missing, weak or incorrect Admin Key | Follow the displayed validation; Console rejects a wrong key. No CPA Management Key substitution. Preserve the saved original key after an unknown commit. |
| Unknown admin/Runtime/completion write result | Read server state. Keep the original key/commit identity and target. If state cannot be read, keep writes locked. Never delete volumes to turn uncertainty into Fresh. |
| Routing activation failure | Retry the original persisted intent after checking state; successful public identity verification is required before continuing. A lost commit reply may retire the fixed Bootstrap transport; the exact public identity leaf resolves that case. |
| HTTP 409 / target or generation conflict | STOP that write and let the Host owner reconcile actual state. No automatic rebind, skip or restart. Refresh reads state but does not clear the conflict write lock. |
| Existing database, legacy settings, unknown source or incomplete receipt | Stop Fresh admission and preserve the source. This flow does not migrate, repair or classify it as a new empty installation. |
| Admin Key lost after installation | Stop and use a separately approved administrator recovery procedure; this flow cannot reveal or overwrite the stored credential. |

## Repeatable automated acceptance

Build task-specific image tags from the selected source, without changing Runtime
targets, seeds, networks or another task's state:

```sh
docker build --platform linux/amd64 -f Dockerfile.runtime -t cpamp-fresh-e2e-runtime:local .
docker build --platform linux/amd64 -f Dockerfile.ingress -t cpamp-fresh-e2e-ingress:local .
docker build --platform linux/amd64 -f Dockerfile.manager-server -t cpamp-fresh-e2e-manager:local .
PLAYWRIGHT_MODULE=/absolute/path/to/node_modules/playwright \
PLAYWRIGHT_CHANNEL=chrome \
node bin/ci/fresh-new-installation.mjs \
  cpamp-fresh-e2e-runtime:local cpamp-fresh-e2e-manager:local \
  cpamp-fresh-e2e-ingress:local /tmp/cpamp-fresh-new-unique-result
```

Prerequisites: Docker Compose, local `python:3.13-alpine`, Node, a callable
Playwright module and installed browser. Omit `PLAYWRIGHT_CHANNEL` for an
installed Playwright Chromium. Use a new output directory per run. The script
pins the supplied images by local image ID, creates a random Compose project,
binds a random loopback port and removes only its own project/volumes afterward.
It retains a secret-free JSON report and masked screenshots. It never logs
Handoff or Admin/Runtime tokens. The data-layer step exposes the
installation-generated Client Key through the Key workspace, re-reads it on demand
through `POST api-keys/<APIKeyID>/secret`, and sends one model request with it
inside the disposable installation.

The script asserts `/health` is `503` before Host initialize and `200` with the
Manager service identity after the Fresh store takeover, then drives the whole
setup wizard (administrator key, one installation submission, public route) and
checks the CPA data layer through the `cpamp/` resource bridge: one CPA
management read, the installation key installed (observed, not written) and one
model request that must authenticate that key (non-401 — it proves
authentication, not a real Provider response; real Provider model success is
reported NOT RUN because no Provider fixture exists in the disposable install).
`CPAMP_SMOKE_MODEL_TARGET` selects where that request is sent: the default
`http://cpamp-runtime:8317` reaches CPA on the Compose network, and
`http://cpamp-ingress:18317` reaches it through the public Ingress listener. The
data layer has no tolerance: the committed default New Managed destination is the
installation-derived CPA management authority, so a bridge or install status
other than the expected one fails the run. Before the completion commits, every
tested Gateway path answers the Pending `503`; once the destination is admitted
every one of those paths stops answering the Pending body, and a status other
than Pending means admission was granted. An anonymous direct call to the
destination CPA is `401` from the first second, because the destination was
staged with its installation key. A restarted Runtime reports a new generation, which withdraws
admission until a new completion binds it, so the post-restart Gateway path is
asserted closed again. The managed retry layout check runs when the config
catalog exposes a retry count or interval field, and is reported `NOT RUN` only
when the catalog omits both.

The expiry case explicitly edits only the claim expiry timestamp in this
disposable, stopped Manager fixture; natural ten-minute wall-clock expiry is
reported NOT RUN. Response-loss cases let real server writes complete before
aborting browser delivery. Source tests, real Docker execution and interactive
browser results must be reported separately. See the
[acceptance record](fresh-new-installation-acceptance.md) for actual observed
outcomes and the remaining limits.

## Candidate build, load and smoke

The release lane builds the three `linux/amd64` candidate images once. The OCI
archives stay the verified evidence artifact, and the verifier still runs on
them. The local engine cannot load an OCI layout archive, so the **same** buildx
invocation also exports a docker-format tar per role. That tar is a load
transport for the very same build result — not a second candidate and not a
rebuild.

```sh
export CPAMP_CANDIDATE_BUILDER=cpamp-r1-local
docker buildx create --name "$CPAMP_CANDIDATE_BUILDER" --driver docker-container --bootstrap
CPAMP_CANDIDATE_LOAD=1 bash bin/release/build-embedded-candidate.sh /tmp/cpamp-r1-unique

PLAYWRIGHT_MODULE=/absolute/path/to/node_modules/playwright \
PLAYWRIGHT_CHANNEL=chrome \
node bin/ci/fresh-new-installation.mjs \
  cpamp-r1-runtime:candidate cpamp-r1-manager:candidate cpamp-r1-ingress:candidate \
  /tmp/cpamp-fresh-new-unique-result
```

A container-driver builder is required: the `docker` driver cannot export an OCI
layout, so the builder script refuses it and asks for
`CPAMP_CANDIDATE_BUILDER=<name>` instead of silently producing another format.
`CPAMP_CANDIDATE_LOAD=1` loads each transport archive and asserts the loaded
image ID equals the verified `imageConfigDigest`, plus config and layer blob
equality with the OCI archive. Any mismatch stops the run with no fallback build.
The reference `cpamp-r1-<role>:candidate` comes from the same buildx `--tag`, so
no separate `docker tag` step exists. `load-evidence.json` records the loaded IDs
and digest comparisons next to `candidate-evidence.json`; the transport note is
repeated in both files.

The three images are local-only evidence: they are not signed producer receipts,
not a registry readback and not a Beta/Release qualification. The candidate
inventory still records `NOTICE` as `NOT_VERIFIED` (third-party LICENSE/NOTICE
legal sufficiency is out of scope).
## Default New Managed CPA authority

The default Compose installation uses the Runtime `new_managed` destination.
`freshstrategy.newManaged` generates a random 256-bit management key, stores
the protected destination snapshot in the Fresh CAS checkpoint, stages and
starts the destination, then requires an authenticated CPA v8 config read-back
before committing `manager_config_v1`. The runtime template binds CPA to
`0.0.0.0`, enables remote management and usage statistics, and disables the
control panel.

These are two different credentials with two different audiences, and the
distinction matters when reading any statement about "the key":

- the **management key** authenticates Manager to CPA's remote management API.
  It is never returned to the browser, Runtime journal, operation response or
  logs; Manager keeps only its protected form and unprotects it per call;
- the **installer Client Key** authenticates model traffic to CPA. It is
  returned exactly once, in the reply that commits the destination, and is
  readable later on demand through `POST api-keys/<APIKeyID>/secret`.

The same snapshot carries exactly one installation-generated Client Key, so CPA
never starts with client authentication disabled. `freshkeys.EnsureInitialKey`
mints `sk-` plus 64 `[A-Za-z0-9]` characters once, and the `origin = 'installer'`
column with its partial unique index makes that record singular: a retry, a
resume or a process restart replays the exact key instead of minting a second
one. `freshkeyapply.EnsureInitialInstall` persists the key and the one-shot
initial-install journal before the destination is described; the Supervisor
rejects a `new_managed` stage that carries zero keys, two keys, or a
workspace-shaped key. A missing or unpersistable key blocks the install with
`managed_initial_client_key_unavailable` instead of starting an open CPA.

The Key workspace lists that record with `origin: "installer"` and the default
name `Initial key`; `POST api-keys/<APIKeyID>/secret` is the authenticated
on-demand read behind the copy button, and it refuses a superseded key. The
`initial-install` route observes the already-installed key and writes nothing,
while a later workspace key cannot consume that single journal (409).

#### Where the installer Client Key lives at rest

The installer Client Key is a working credential for model traffic, not a
one-way secret: CPA has to read it on every request, so `access.api-keys` in the
child-owned CPA `config.yaml` holds it in plaintext regardless of what Manager
does. The declared trust model is therefore "the Docker host is trusted; other
processes inside the same container are not", and the key is confined to
exactly these places:

| location | form | access |
| --- | --- | --- |
| Manager SQLite (`freshkeys`, `origin = 'installer'`) | encrypted with the existing protector | Manager process; plaintext only in memory during a call |
| Runtime volume `destination/stage.json` | plaintext | Supervisor-only stage snapshot, mode `0600`, same boundary as the management key |
| Runtime volume CPA `config.yaml` (child-owned) | plaintext `access.api-keys` | the CPA child process, `0600` |
| committing `POST strategy` reply | plaintext `initialClientKey` | the one in-flight install session, `Cache-Control: no-store`, never persisted by the browser |
| `POST api-keys/<APIKeyID>/secret` reply | plaintext | authenticated Admin, `Cache-Control: no-store`, never cached |

The Supervisor stage snapshot is not a new disclosure path: it has always held
`ManagementKey` and the `network_existing`/legacy `APIKeys` in plaintext under
the same `0600` Supervisor-only rule, and the child `config.yaml` must be
plaintext for CPA to work. Manager keeps only the protected form, and the
committed acceptance script (`bin/ci/fresh-new-installation.mjs`) checks the
boundary it can observe from inside the disposable installation rather than
scanning Manager's volume for encodings:

- the raw installer Client Key, like every other secret the run touches, must not
  appear in `docker compose logs` output;
- the Manager volume's persistent identity files and its `settings` digest are
  byte-identical before and after a completed-operation replay, so the replay
  neither rewrites the checkpoint nor rewrites a credential;
- the committing `POST strategy` reply carries the key exactly once with
  `Cache-Control: no-store`, a replay of the committed operation omits both the
  field and the key, `POST api-keys/<APIKeyID>/secret` returns it with the same
  header, and the Key list never carries a plaintext secret.

Anything beyond those checks — for example scanning the Manager volume for the
key's hex and base64 encodings — is out-of-band verification, not CI evidence.

After commit, `freshkeyapply.Service.DefaultInstallCPAAuthorityProven` in
`apps/manager-server/internal/service/freshkeyapply/config_target.go` is the
Manager-internal predicate exposed for Gateway admission. It requires the
committed New Managed connection and an authenticated Runtime status matching
the recorded identity, generation, and artifact. Private attested installs
continue to use their explicit host-permit profile.

### Trust boundary and durable authority

The managed profile is selected by the server-owned Fresh checkpoint, not by a
browser field or an environment variable. A `new_managed` checkpoint is bound
to the authenticated Runtime identity, generation and active CPA artifact. Its
destination identity is `managed:sha256:<sha256(normalized CPA base URL)>`.
The checkpoint's protected snapshot contains the Supervisor operation ID,
expected Runtime facts, source discriminator and randomly generated 256-bit
management secret. The plaintext secret is used only while staging and reading
the destination; it is never serialized to a response, journal, log or browser
state.

The first strategy call only returns this protected snapshot. The caller writes
it through the Fresh CAS before `StageDestination` or `Start` is attempted.
The next call reuses the same child operation ID, verifies the Runtime facts,
stages the `new_managed` destination, starts it and authenticates
`GET /v8/management/config`. A Supervisor `Start` result only publishes the
spawn, so the install consumer's Runtime client first waits for the Supervisor's
own readiness observation of that exact identity and generation (bounded to
20 s); a readiness timeout is not a failed spawn, and it leaves the typed,
resumable `managed_destination_verification_required` blocker instead of an
unconfirmed write. Only after that read succeeds does the CAS store
the protected Manager connection. Completing the Fresh routing/product CAS
also mirrors that connection to `manager_config_v1` and the compatibility
`setup` row; both rows contain the protected envelope and are decrypted only by
Manager-owned repositories.

Key install, revoke, replace and retry configuration use this connection as
the default authority. One profile discriminator decides that ownership:
`freshkeyapply.Service.Targets` compares the committed checkpoint with the
profile the Manager constructed — the managed destination for the default
Compose install, the attested private target for the host-permit profile — and
takes the asking surface as an argument, so each surface keeps the contract it
shipped with instead of a second inline check. Initial install, key
revoke/replace and the private chat consumer all ask it and refuse an adopted,
External or uncommitted checkpoint. The gateway configuration page asks the
same discriminator under its own surface: on a managed install it refuses the
same checkpoints, while on a private host-permit install it keeps the attested
private target scope its page and field journal shipped with, including an
adopted checkpoint that names that target. On an External install the page and
the key surfaces ask the same discriminator under `ProfileExternal`: the target
comes from the External authority, never from a settings read, and only a
committed Connect External checkpoint whose connection identity it can prove is
accepted. `DefaultInstallCPAAuthorityProven` is the
boolean admission predicate at
`apps/manager-server/internal/service/freshkeyapply/config_target.go`; it
requires the exact committed Runtime facts, managed CPA posture and at least
one installed Client Key, so a CPA with client authentication off is not
admitted. The private profile remains separately discriminated and continues
to require the short-lived host permit.

### Connect External management

An External installation has no Manager-owned Runtime, artifact or readiness
fact: the CPA belongs to the user, and this Manager only holds a protected
connection to it. `freshkeyapply.NewExternal` is therefore constructed from a
source that re-reads, on every call, the committed `connect_external` checkpoint
with `ProductComplete` and the protected Manager connection. It decrypts the
management credential and refuses anything but a complete checkpoint. The
credential is accepted by the rule Connect External itself applies at
installation (any printable key up to 4096 characters), not by an exact-length
check, because the user chooses it. The user's CPA is never contacted to resolve
the authority, so an outage there cannot hide the target.

Two durable facts identify it:

- the **identity** is `external:sha256:<digest>` of the normalized committed
  address — the address digest is the only comparable durable fact, because the
  credential is a protected envelope;
- the **generation** is that identity and nothing else. An earlier design mixed
  the digest of the CPA's `config.yaml` into it, which made every External
  operation invalidate itself: the client-key list lives in that document, so
  this Manager's own successful write changed the digest, left the journal at an
  unknown effect, and `BeginOperation` then refused every later operation. The
  configuration digest is a per-step grant CAS instead: it is read immediately
  before each single PUT and compared there, and it never enters the journal.

`Targets` answers `key_install`, `key_operation` and `gateway_config` for it —
that allowlist, so an unknown surface is refused — and refuses chat admission
and gateway admission, which stay with the managed destination. External,
Managed and Private remain mutually exclusive: the discriminator accepts only the
checkpoint of the profile the Manager constructed.

Keys are administered through the CPA v8 API and every claim is read back. The
staged secret is decrypted in-process and compared by membership against the
CPA's own key list — there is no managed template to prove, and the CPA may
carry credentials this Manager never staged. The journal settles from what the
CPA shows: a revoke step has ended when the credential it removes is absent, and
an add step when the credential it introduces is carried. A lost reply that the
read-back can settle, a 401 that proves nothing was written, and an edit the
user made between `Prepare` and `Continue` therefore all reach a terminal state
instead of leaving the journal pending and every later operation refused. A lost
reply is not always one of those: an attempted write that did not land — a 5xx, a
timeout, or a 4xx other than 401 — leaves the effect unknown until an operator
confirms it, exactly as on Managed. A write is only sent while the start
state still holds: the credential it removes is carried, the CPA keeps another
one, and — for a revoke — that keeper is a credential this Manager staged and has
installed. Revoking the only credential is refused with `ErrLastKey`, because
that would leave the user's CPA accepting unauthenticated clients. The External
Apply refuses while a journal entry has not settled, and re-reads the document
digest and the membership immediately before its own PUT, so a credential the
user revoked on their CPA is not knowingly added back. That read is not a
compare-and-swap: a whole-list PUT replaces whatever the CPA holds when it
arrives, so a change that lands between the re-read and the PUT is still
overwritten — the check narrows the window, it does not close it. The gateway
configuration page keeps #1090's External semantics: scalars are best-effort with
a baseline re-read, and rule families stay read-only.

The key list is the Manager's own record plus the presence read back from the
CPA. A CPA that cannot be read leaves that list intact and reports
`presenceUnverified: true`, so an outage hides nothing this Manager knows and
nothing in the list claims an observed effect. A credential the user's CPA
refuses — its own 401, and its 403 to a client it bans or whose remote management
it disables — is `cpa.ErrManagementRejected`, surfaced as
`external_management_key_rejected`; the 403 variant wraps it and adds a `detail`
on the rebind answer, because a ban expires and the same credential may then
work. A committed target the connection no longer names is
`cpa.ErrExternalTargetMoved`, surfaced as `external_target_moved` on every
External surface. Both are typed blockers, not an unavailable proof and not a
retry reason: no write follows either. The difference is the way back — a
refused credential is resolved by rebinding the connection, while a moved target
is **refused** by rebind (`external_rebind_refused`, since the address is never
changed there) and needs a reconnect, which commits its own checkpoint.

#### The CPA version decides what may be written

External management speaks the CPA v8 contract, so every write is preceded by
one authenticated `GET /v0/management/config` that reads the advertised version
(`cpa.ObserveExternalVersion`). The class is `supported` for major 8 and later,
`unsupported` below it, and `unknown` when the header is missing or unparseable.
Both of the latter set `WriteBlocked` and refuse writes with
`cpa.ErrExternalVersionUnsupported`, surfaced as
`external_cpa_version_unsupported`: an unproven version is not a licence to
write. Reads are never gated — the key list, the gateway page and the usage
workspace keep showing what the user's CPA actually serves — and the class is
published beside the observation (`cpaVersion` on `external/usage`,
`versionSupport`/`writeBlocked` on the Runtime status) so the page can explain
the refusal instead of offering a write that would fail.

The gate sits on one point per profile: the External key wire wraps its single
PUT, and the key journal and the gateway saves ask it before they record an
intent, so an unsupported CPA never holds a pending operation it could never
send. The key journal asks it again immediately before each step is marked
`attempted`, and a refusal there leaves that step `unattempted`: a CPA that
changed class between `Prepare` and `Continue` sends no request, and the step can
still be taken once it serves the contract again instead of the journal
answering `ErrApplyUnknown` forever.

The probe travels on the transport the writes use — no proxy, no redirect
followed, one five-second budget — so the hop it judges is the hop a write
reaches. A probe that answers 401 stays the credential blocker, one that answers
403 is the same blocker tagged `ErrManagementClientForbidden`, and one that
cannot reach the CPA stays `ErrProofUnavailable`; none of them is reported as a
version class.

#### Key usage from the user's CPA

The Collector has no host-supplied authority on External: it reads the committed
connection through the same `externalSource` the key surfaces use, so an
uncommitted or foreign connection can never become a collection source. Staged
keys bind their source to the installation identity `external:sha256:<digest>`,
never to a raw address and never to a managed destination — another CPA is
another identity, so a rebind to a different address cannot inherit this
history.

Because the Collector writes the `usage-statistics-enabled` switch on a CPA this
Manager does not own, that write is the one action here that needs the
administrator, not the connection: `POST external/usage` with
`{"requestId","confirmEnableStatistics":true}` is the only path that sets it, and
an unconfirmed start collects nothing rather than defaulting to yes. The write is
proven by reading the switch back; an unproven switch starts no Collector,
because a Collector against a CPA that records nothing would present an empty
window as a live one. The confirmation is consumed by the one start attempt it
authorises: a later replay of the same completion, or a restart, asks again
instead of reusing an approval nobody renewed, and a confirm while collection is
already live is a no-op rather than a restart.

A start is claimed only when it actually happened. `MarkCollectionStart` runs
after a Collector is live — and, on Managed and External, after its first
authenticated management read succeeded, because a live phase alone is not
evidence — and `collectionStarted` reports that latch rather than the absence of
a refusal, so an installation that never attempted a start reports no coverage
instead of a zero. `blockedReason` carries the stable code of the last refused start
(`usage_statistics_disabled`, `usage_collector_unavailable`,
`usage_collection_authority_unavailable`, `external_cpa_authority_unavailable`,
`external_cpa_version_unsupported`, `external_management_key_rejected`), and
`external/usage` reports it beside `statisticsEnabled`, `collector` and
`cpaVersion`.

A refused credential — CPA's 401 to a key it no longer holds, or its 403 to a
banned client — stops collection and surfaces
`external_management_key_rejected`. Both paths are covered: a start attempt meets
it on its own authenticated read, and a Collector already running meets it on
its first refused poll. In the second case the Collector stops itself and reports
back, because a CPA bans an address after five failed authentications, so the
backoff retry loop would have converted the user's rotation into an outage within
about a minute; the reported refusal clears the started latch, so
`collectionStarted` reads false and `blockedReason` carries the code. Nothing
restarts collection until an administrator rebinds the connection: a successful
rebind — a rotation, or a re-verify of the stored key — stops any surviving
Collector and starts a new one that authenticates, so a refused key is never sent
again. The refusal is also recorded durably, and the browser-driven reads
(`external/usage`, the key list, runtime status, the gateway read) answer it as
`external_management_key_rejected` without contacting the CPA at all: those pages
refresh, and five refreshes would otherwise reach the ban on their own. Starting collection on an
unsupported version is refused with `external_cpa_version_unsupported` before the
statistics write, for the same reason the write gate exists.

#### Rebind after a rotated management credential

The user's CPA is theirs to administer, so a rotated management credential is a
fact of their installation rather than an outage. `POST external/rebind` takes
`{"requestId","managementKey"}` (and an optional `baseUrl`, accepted only to
refuse it) and is the only way out of
`external_management_key_rejected`. The order is the safety:

1. the candidate must pass the rule Connect External itself applies;
2. the record revision is read, and it is the revision the write below is
   conditional on — a save, a key write or another rebind that lands during the
   verification therefore conflicts instead of being overwritten;
3. the candidate is proven against the committed address with one authenticated
   read on the write transport that also classifies the version, so nothing is
   persisted for a credential that does not authenticate or that does not serve
   the contract;
4. it is encrypted and written in a transaction that replaces the connection and
   both compatibility mirrors (`manager_config_v1`, `setup`) and bumps the record
   revision.

The way back does not require a new key. The common cause of a rejected
credential is the address ban, which expires, so rebinding with the credential
already stored is a **re-verify** (`status: "reverified"`): the same one
authenticated read runs, the recorded refusal is cleared, and collection restarts
— but nothing is persisted and the record revision is untouched, because no
credential changed. A re-verify the CPA still refuses answers with the same
refusal a rotation would. The practical instruction is therefore: wait out the
CPA ban window, then rebind with the current key (or the new one, if it really was
rotated).

The **address is never changed**: a different address is a different
installation, and reaching it is a reconnect that commits its own checkpoint.
The installation identity — the address digest — therefore survives, and with it
every key-usage binding, so a rebind never mixes one CPA's history into
another's. The Connect External target generation is pinned to `1` by the
contract and is not bumped here; the record revision is the CAS a rebind moves,
and the credential a later write resolves is always the one the record holds at
that moment. A candidate equal to the stored credential is a re-verify, never a
rotation. A rebind to a credential the CPA refuses, to an
unsupported version, to another address, or against a revision that moved during
the verification is refused with its own code
(`external_management_key_rejected`, `external_cpa_version_unsupported`,
`external_rebind_refused`, `external_rebind_stale_generation`). A 403 from the
CPA is the same `external_management_key_rejected` code the reads use, with a
`detail` saying the CPA is refusing this key or client and that a retry after the
ban window may be enough. No reply carries the credential, the address or any
text from the CPA.

### Failed stage or start

The Supervisor journal never resumes a terminal operation, so a stage or start
recorded as `failed` can only ever be replayed. The checkpoint therefore
carries a durable `destinationAttempt`: stage/start operation IDs are derived
from it (`<base>` for attempt zero, `<base>:attempt-<n>` afterwards) and it
advances by exactly one, in the same revision-guarded Fresh CAS, only after the
Supervisor reports a terminal `failed`. An unresolved outcome (transport
error, `accepted`, `running`) keeps the attempt, so a retry replays the
operation that may still be running rather than racing a second one, and two
browser retries at the same revision cannot fork attempts. The legacy
migration uses the same counter, and so does `adopt_existing`: its extra bound
is described in [Retry after a terminal destination
failure](#retry-after-a-terminal-destination-failure).

The Runtime admits a replacement stage only under a new operation ID, only for
the identical destination, and only with terminal evidence that the superseded
attempt never owned a child. Its stage operation must be terminal, and its Start
operation must be absent or terminally failed; an accepted, running or
successful Start, a content change, or a live owned child stays a conflict. A
stage that succeeded always needs its Start terminally failed, because that
attempt may own the child the retry would replant. A stage may publish its live
root and still be recorded as failed, because the final directory sync runs
after the rename; that is why `Start` refuses any destination whose stage
operation is not `succeeded`, and why absence of Start evidence is the only
proof that such a root is free. The superseded directory is renamed to a
content-addressed `.destination-retired-<digest>` sibling, never deleted and
never left as the live destination; a retry that fails to publish restores it,
so a retry can neither orphan nor displace the previous attempt.

### Gateway admission (Ingress)

Ingress keeps every Fresh client Gateway route closed with
`503 gateway pending configuration` until Manager answers the private
`/internal/routing/fresh-authority` read with an admission posture that is
consistent with the installation:

- the Fresh install is committed (`committed: true`) and the reply repeats the
  installation, generation, operation, revision and target that Ingress
  already holds in its routing projection;
- `gatewayAdmission.installationId` and `gatewayAdmission.generation` are the
  same installation generation;
- `gatewayAdmission.admitted` is true, which Manager sets only while
  `DefaultInstallCPAAuthorityProven` is true for the default New Managed
  profile (committed destination, authenticated Runtime status, managed CPA
  posture and at least one installed Client Key).

Manager computes the posture on every read and never persists it; the reply
carries no key, management secret or destination address, and a Manager that
does not send the field (an older Manager) never admits. Ingress caches one
positive answer for 15s and one negative answer for 1s, re-reads Manager after
that TTL, and withdraws a cached positive answer immediately when its routing
projection changes or disappears. A restarted Ingress has no cached answer, so
it stays closed until Manager re-publishes the projection (the existing
`restoreFreshRouting` loop runs every 2s) and admits again. Any Manager error,
timeout, non-200 reply, legacy `204`, unknown field, trailing data or mismatch
keeps the Gateway closed.

Client authentication is enforced from the first second of the destination,
not by reacting to a later key install. Three facts form the safety chain:

1. the destination is staged with exactly one installation-generated Client Key
   (`sk-` plus 64 `[A-Za-z0-9]`), which the Supervisor refuses to drop, so CPA
   v8 starts with client authentication on; admission still requires at least
   one installed key, so Manager never admits a destination whose client set is
   empty;
2. the product cannot empty that set: a managed revoke of the last effective
   key is refused with `ErrLastKey` (HTTP 409 `last_effective_key`), and
   replace installs the successor before the old key is dropped;
3. if the destination's key set or managed posture is changed out of band
   (outside Manager), the predicate returns false on the next read and Ingress
   withdraws admission at that read.

"Revoke every Client Key" is therefore not a reachable product state and does
not itself withdraw admission. Because the destination never starts with an
empty key set, CPA v8 has client authentication on for the whole life of the
installation; an anonymous direct call to the destination is refused with `401`
even before any workspace key exists. The residual window is out-of-band only: a
positive admission answer already held by Ingress stays effective for at most
15 s after the destination is changed outside Manager, and a missing or wrong
client credential is forwarded to CPA, which answers `401`.

The public Ingress listener never proxies CPA management paths:
`/v8/management` is denied at the transport edge, `/v0/management` keeps its
Manager ownership, and the private `/internal/...` namespaces are denied before
either upstream. Legacy deployments keep the existing `204` authority contract
unchanged.

### Adopt `network_existing`

Adoption takes over an already running CPA v8 that the operator points at. It is
admitted on the same Fresh claim the New Managed flow uses, and on nothing else:
the host operator who ran `cpa-manager-plus initialize` is the only holder of
that handoff, so an authenticated `POST strategy` naming
`adopt_existing`/`network_existing` is that operator's own intent and needs no
second Host permit. The service no longer answers 503 for it.

What stays refused is narrower than before, and every refusal happens before any
destination operation or Manager checkpoint mutation, so a refusal has no side
effect. (The zero-Key refusal below is the one exception in ordering: it *is* the
single source `GET`, answered by that read.)

| input | result |
| --- | --- |
| `adopt_existing` with `legacy_managed`, or with no source class | `adoption_source_class_refused` (409); a v7/CPAMP-managed source is Upgrade's path, and adoption never guesses a generation |
| `adopt_existing`/`network_existing` whose source authenticates but carries no client Key | `adoption_source_empty` (409); see below |
| `adopt_existing` with a stale `expectedRevision` | 400, not a retryable conflict |
| any target whose qualified consumer is not accepted in `consumer.json` | fail-closed before any probe |

The zero-Key refusal is a distinct typed error, not a generic read failure.
`cpa.ReadV8AdoptionSnapshot` returns `V8AdoptionSourceEmpty` when the source
answered a well-formed v8 config whose `access.api-keys` is empty, and
`freshstrategy.adopt` maps only that error to `adoption_source_empty`. CPA v8
disables client authentication when its Key list is empty, so adopting such a
source would install a destination reachable by any anonymous Compose peer. A
transport error, an unauthenticated read, a v0-only source or any other malformed
body keeps the ordinary `adoption_source_read_unavailable` code, and the browser
is told the source could not be read rather than that it was empty.

The Manager data volume is mounted only into `cpamp-manager`; the Runtime
container mounts its own volume and never the Manager store. A repo test asserts
that topology against `docker-compose.yml` so the claim anchor, the Fresh CAS and
the Key records cannot silently become reachable from the Runtime child.

Adoption stops at a `committed` checkpoint plus the protected destination
snapshot in this lane; `POST complete` still answers 409 for
`strategy = adopt_existing` (`fresh.go:426`), so adoption does not yet reach
product completion the way New Managed does. Completion arrives in a later
slice. What it already writes is its own `external:sha256:` target identity, and
it keeps `strategy = adopt_existing`; the prefixes are never merged.

#### Migration after the destination is committed

Once the destination is committed and its client keys are verified, the Manager
copies a bounded inventory from the source CPA into it and records one result per
item. The executor is `internal/service/freshadopt`, reached only from
`freshApplication.runAdoptionMigration` (`fresh_adopt_migration.go`), guarded on
`adopt_existing` + `network_existing` + `stage == committed`.

| kind | source | destination | read-back |
| --- | --- | --- | --- |
| credential file | `GET /v8/management/credentials`, `GET /credentials/download?name=` | `POST /v8/management/credentials?name=`, then `PATCH /credentials/status` to restore the enabled state | the destination's listing entry (name, type, disabled) and the downloaded byte digest |
| gateway leaf | one `GET /v8/management/config` | `PATCH /config/<group prefix>`, nested down to the leaf, using the same `cpa.ConfigLeafPaths()` table (39 leaves) the gateway page owns | `GET` that one path |
| rule family | the same single document | `PUT /config/<family path>`, for the 5 `cpa.ConfigRuleFamilyIDs()` families | `GET` that path |
| provider section | the same single document | `PUT /config/<section>`, for the 12 `api-keys.*`/`oauth.*` groups | `GET` that path |

The approved scope names 13 provider ownership groups, but CPA v8.0.13 has no
root `models` section: `internal/config/config_types.go:454-455,537-538,658-659`
declares `models` only inside a `payload` rule list or a provider config, and
`v8AllowedRoots` (`config_v8.go:572`) does not list it. It is absent from the
table rather than guessed; its effective content arrives with the `api-keys.*`
groups, which carry each group's own `models` list.

Three properties are structural rather than conventional:

- **The source is never written.** The source client interface has no write
  method, so no code path in the package can issue a non-GET request to it. A
  test asserts the fake source observed zero writes and kept its own bytes and
  configuration.
- **An unknown result is reconciled by read-back, never by resending.** An upload
  or a configuration write whose reply was lost is decided by what the
  destination holds afterwards: matching bytes or a matching value is a
  successful copy, anything else is a reported failure. Every item already
  recorded — `migrated`, `manual` or `failed` — is skipped on a rerun, because
  resending a failed write is exactly what the checkpoint exists to prevent.
- **Secrets stay in Manager memory.** Credential bytes and provider keys travel
  inside the process only: they are never logged, never written to the record,
  never returned in a result and never listed by an enumerable GET. The durable
  summary carries item identities, outcomes, a bounded reason, a concrete
  instruction and byte digests.

`ManagerOwned` is the single exclusion predicate: `management.*`,
`remote-management.*`, `server.host`, `server.port`, `plugins.*`,
`disable-control-panel`, `oauth.auth-dir` and `access.api-keys` are never copied.
They are the destination's own identity, listener, plugin root, auth directory
and client key list, and the last of those was already written by the verified
`StageDestination`/`VerifyV8Adoption` path. A test asserts that no table path
matches the predicate and that the executor never sends one.

A credential v8 cannot take becomes a `manual` item with a concrete instruction
instead of a failure, and so does a copy whose enabled state or provider
classification the destination did not accept. There are three such cases, and
each one is decided from evidence this side holds:

- a runtime-only/plugin virtual auth has no file to copy;
- a file that is not a JSON object can never satisfy CPA's `buildAuthFromFileData`,
  so it is refused locally and never uploaded — CPA's answer for such a body is
  a `500 {"error":"invalid auth file: ..."}` (`auth_files_crud.go:125-128,482-508`),
  not a 400, which only covers a bad `name` or an unreadable request body;
- an upload CPA answered `500` for, with the file absent on read-back, is CPA's
  verdict on the bytes and is recorded as `manual` without a resend.

The per-item record is `freshstore.Record.AdoptionSummary`, written through the
same revision CAS as every other Fresh write and held to a monotone append-only
shape by the store itself. The executor checkpoints after each item, so the
record is never behind a write that already happened; a refused checkpoint stops
the run at that item, and the item in flight is the only one a resume may repeat.
Manual and failed items are operator work carrying a concrete instruction: they
never fail the adoption, and a source that cannot be read leaves the adoption
committed with an empty summary. A committed replay of `POST strategy` is the
resume path and continues from the durable prefix. Step 4's completion
conditions read this summary; the authority generalization, usage enablement,
model management and the Full Web Adopt flow are not part of this lane.

The per-item summary is the operational record of the copy. It is deliberately
*not* itself a qualification. `freshAdoptionRefRules`
(`internal/domain/bootstrap/adoption.go`) now marks `cpa-auth-files`,
`cpa-config-routing` and `provider-state-api-keys` as qualified, but the decision
still requires per-kind evidence: the class-specific read/replay/verify chain
must name the kind in all three traces, and every authorized kind must be in the
rules' migrated set. A `legacy_managed` source authorizes client keys only, so its
other kinds fall back to exactly the manual action they had before. That record
is the reviewed authority for what the Manager's install-derived authority may
claim, and it is bound to an independent acceptance of exactly those bytes and
that scope; the `client_api_keys_only` limitation therefore remains on the
legacy consumer and is no longer claimed for the network consumer.

**Waiver (2026-10-29).** Acceptance `65eb659a` covered client API keys only.
What this document describes for credentials, gateway configuration and provider
sections was built and evidenced afterwards: those kinds are migrated under a
**maintainer waiver dated 2026-10-29**, backed by real-CPA evidence from the
`linux/amd64` container run and **pending the single pre-Beta independent
re-acceptance**. No new review record was created and the consumer stays
`accepted` under `65eb659a`; read every widened kind here as waived, not as
independently accepted.

The record is also the gate on the copy itself, not only on its label. The
executor classifies every item through `freshadopt.ItemRef` — a gateway leaf or
payload rule family the accepted gateway table names is `cpa-config-routing`,
anything outside that table keeps its describing reference and stays manual —
and it copies an item only when the record's decision for that reference is
`migrate` (`freshadopt.Executor.gated`). Every other item is recorded with that
decision's manual action and is never written, so an inventory that later
widens cannot copy a kind the record still calls manual.

#### Retry after a terminal destination failure

Adoption shares the durable `destinationAttempt` counter with New Managed, and
`retireDestinationAttempt` now retires an adoption attempt on the same terms,
bounded by `bootstrap.MaxAdoptionDestinationRetry = 1`:

- the destination operation ID is derived from the attempt index
  (`<base>` for attempt zero, `<base>:attempt-<n>` afterwards), so a replay of
  the same attempt is byte-identical and stays idempotent, while a retired
  attempt is a genuinely new Supervisor operation;
- an adoption gets its original attempt plus exactly one retry. A second
  terminal `failed` is reported as a plain blocker with no retirement, because it
  carries no new information and the operator must reconcile the target instead
  of looping;
- only terminal Supervisor evidence may advance the counter. A transport error,
  an `accepted`/`running` result, an invalid result or a foreign
  identity/generation is `adoption_destination_stage_unresolved` or
  `..._start_unresolved` and keeps the attempt, so the retry replays the
  operation that may still be running rather than racing a second one;
- a committed adoption is not retryable by construction: both the adapter and
  the retirement CAS refuse a checkpoint that already published its destination
  connection, so a successful attempt can never be superseded;
- a resend after a lost first reply is only accepted with identical parameters.
  A resend that changes the source URL, the management secret, the source class
  or the expected Runtime identity is a conflict and mutates nothing, so a lost
  response cannot be used to swap the source under an in-flight attempt.

`adoption.go` is one of the files behind `consumerImplementationSHA256`, so any
change to this retry or refusal logic invalidates the accepted consumer record
until that consumer implementation digest is recomputed and the class-specific
adoption evidence is re-emitted from a real run. The Runtime destination pin does
not move for this change; only the consumer digest is recomputed. That is why
this lane re-emitted the `network_existing` and `legacy_managed` resource
reports/traces and the legacy-transition report/observation rather than editing a
digest by hand.

Adopt stays unreachable from the web UI in this lane: the wizard's only
actionable strategy is still **New embedded installation**, and the Full Web
Adopt flow is a later step.

### Existing-install migration

On Manager startup, a completed legacy default record with a committed
`new_managed` checkpoint but no destination identity or connection enters one
one-time migration. Manager requires the Runtime to be authenticated and CPA
offline, CAS-persists a protected migration snapshot, then reconciles the
stable `:managed-migration-destination` Supervisor journal through stage,
start and config read-back. A generation/checkpoint mismatch is a conflict and
never silently rebinds. If `/runtime/gateway/auth` is non-empty, the Runtime
returns `managed_gateway_auth_present`; Manager leaves the durable snapshot in
place and fails closed. A completed connection makes subsequent startup passes
no-ops.

### Beta acceptance (D)

After the candidate is re-accepted and the three qualification pins are
updated by the authorized maintainer, D runs the default Compose acceptance in
this document and verifies, in order:

1. Fresh New Managed completes with the destination identity, generation and
   artifact recorded; `manager_config_v1` and `setup` resolve to the same base
   URL and management authority without exposing it.
2. Read the installation key from the completion screen, then create a workspace
   Client Key and send a real model request through the public Ingress listener
   by setting `CPAMP_SMOKE_MODEL_TARGET=http://cpamp-ingress:18317` for the smoke
   run; observe CPA acceptance. Replace the key and observe the old key rejected
   while the new key works; revoking the last effective key must return 409
   `last_effective_key`. Before the installation is admitted the same request
   returns the Pending 503; once admitted, a wrong or missing credential returns
   401 from CPA, because the destination always has at least its installation
   key. A restart of Ingress must not forward before it has reconfirmed
   admission.
3. Read and write each accepted Gateway retry/streaming field, then read it
   back and compare the CPA v8 config. Interrupt one write, query its operation
   and resume it; the journal must not duplicate the PUT.
4. Read `/auth-files` and the resource bridge through `cpamp/`; verify the
   collector/identity consumers resolve the managed base URL and credential
   from the completion mirrors.
5. Repeat after Manager restart. For a legacy fixture, verify migration is
   idempotent and refuses a non-empty bootstrap auth directory or generation
   drift. Capture only secret-free evidence.

Before re-acceptance, any pin-gated step is `NOT RUN`. The two build-bound
pins and the observation bytes they bind were recomputed/re-emitted on
maintainer decision, ahead of independent re-acceptance; writing an acceptance
record is not part of this implementation lane, and the retained independent
review is unchanged.

The adoption subset of that re-acceptance is narrower and already pinned by a
real run: `TestDestinationRealCPAV8` and
`TestAdoptionExactCPAV8WithSupervisor` were re-run in a
`linux/amd64` container against the exact CPA v8.0.13 candidate and the pinned
CPA v7.0.12 historical source, and the emitted
`network_existing`/`legacy_managed` resource chains plus the legacy transition
observation are what `consumer.json` now binds. Re-running those two harnesses
is reproducible: two independent container runs produced byte-identical
emission. Adoption acceptance still needs an independent reviewer; a green run
is not acceptance.
