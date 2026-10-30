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

1. Choose **New embedded installation**, **Adopt existing CPA** or **Connect to
   external CPA**. New Managed is the recommended path. Adopt reads the
   `adopt_existing`/`network_existing` source the operator names and copies its
   credentials, gateway configuration, provider sections and client keys into the
   CPA this Manager runs (see [Adopt `network_existing`](#adopt-network_existing)).
   External remains a Host-authorized connection checkpoint and cannot finish
   installation. The Fresh flow does not upgrade existing product data.
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

### Legacy bridge writes on External

The Foundation-era Console still speaks the original CPAMP routes through the
`cpamp/` bridge (`fresh_resources.go`), and on External that bridge would be a
second, unproven write path onto a CPA this Manager does not own. It is therefore
cut over on the committed `connect_external` profile, at one point: the whole
legacy router is wrapped, so the `cpamp/` alias and the plugin management proxy
are covered by the same gate.

The gate is scoped to the request that actually presents the saved key. The path
does not say that: the same `/v0/management/...` prefix is proxied with the saved
key for an Admin caller and with the caller's own header for a caller-auth plugin
request, and `/v1/models` forwards the caller's own header. The proxy therefore
marks the request at the moment it attaches the saved key
(`SavedKeyMark`/`NoteSavedManagementKey`), and only a marked request is recorded
when the CPA answers 401 or 403. Admin authorization runs before the gate, so a
caller-auth or unauthenticated request never takes the credential lock and a 401
on one is never filed as a refusal of the saved key. The plugin resource proxy is
excluded from the gate entirely, because it accepts unauthenticated sandbox
fetches and cannot authenticate before it runs.

- Writes Fresh already owns are refused locally with `409` and
  `{"code":"external_legacy_write_replaced"}`, and nothing reaches the CPA:
  non-`GET` `v0/management/api-keys` (owned by `api-keys`), `PUT`
  `v0/management/config.yaml` (owned by `gateway/config`), and non-`GET`
  `usage-service/config` (owned by the connection, the rebind and
  `external/usage`). On External the compatibility `setup` row is a mirror of the
  committed connection, so editing it through the legacy route would desync the
  two.
- Once the refusal is on record, every remaining legacy request that would
  present the saved management credential answers `409`
  `external_management_key_rejected` locally, any method — including the plugin
  management proxy, which presents it too. This is the same blocker the typed
  reads use, and the reason it is a local answer is the CPA ban window: five
  failed authentications ban the Manager's address, so a bridge that kept trying
  would convert a rotation into an outage.
- Read-only `GET`s stay open, subject to that refusal gate, and the Manager-local
  families (`status`, `dashboard`, `usage-service`, `usage`,
  `model-prices`, `monitoring`, `api-key-aliases`) stay readable without it: they
  dispatch before the CPA proxy and never carry the credential.
- The refusal is learned from the CPA as well as recorded before it: if a legacy
  request that presents the saved credential is answered `401`, the bridge
  records that refusal and answers the typed blocker instead of passing the
  `401` through. The browser reads a `401` from this namespace as its own Admin
  session having expired, so forwarding it would clear the session and return the
  operator to the login form before any page could name the cause. This is the
  first refusal after an out-of-band rotation, which is exactly when the blocker
  has to appear.
- Those requests are serialized, one at a time, and the refusal is re-read under
  that lock. A console page opens six of them at once, and CPA bans an address
  after five failed authentications: without the serialization a single page load
  trips the ban, and the blocker could never be cleared, because the rebind that
  would clear it is refused by the ban it caused. The lock is bounded: it is
  released the moment the answer's status is known (`WriteHeader`, and on every
  error or panic path), so one slow body cannot stall the rest of the console, and
  a waiter whose own request was cancelled gives up instead of queueing — nothing
  is ever forwarded for a caller that stopped waiting. A request whose status is an
  upstream provider's rather than the CPA's is exempt from the console-read budget
  (`v0/management/api-call`, and the provider token exchange of
  `v0/management/oauth-callback`): a non-streaming completion can take far longer
  than 30 s to send its headers, so the lock is held until that status arrives or
  the caller cancels, which is the only bound the proxy itself puts on those paths.
  The read grant is taken under the lock *before* the request leaves, so a slow 401
  sent with the replaced key is filed against the envelope that was sent and never
  against a rebound key.
- The local refusal deletes the entity headers the proxy copied from CPA
  (`Content-Length`, `Content-Type`, `Content-Encoding`, `Transfer-Encoding`, and
  the rest of the framing/representation set) before writing its own JSON; behind a
  real server those headers truncated or emptied it.
- A CPA 403 is the same `external_management_key_rejected` blocker with the `detail`
  the rebind route uses for a ban, and a `store.Load` failure fails closed with
  `503 external_cpa_authority_unavailable` and no CPA request. The External gateway
  save, the key install and the usage confirm also answer the recorded refusal
  locally, so a rotation costs at most one failed authentication before the gate
  answers everything.
- The console's own model read (`/v1/models`) is **not** gated and is **not** a
  presentation of the saved key: it forwards the caller's own header, so its 401 is
  the caller's, not the installation's.
- New Managed and Adopt keep their existing bridge exactly as it was; the gate
  reads the committed strategy and hands the request straight through.

The reconciling `POST gateway/config/operations/<id>/reconcile` is gated the same
way, for the same reason: it asks the CPA for the source record, so it presents
the stored credential, and it is polled while a step settles. Its answer is the
typed blocker, and no reconcile is sent. The plain inspection answers from the
journal alone and is therefore not gated.

### External states in the web UI

Nothing on the page guesses a cause, so every typed code the Manager emits is a
state with its own instruction and its own action
(`apps/web/src/fullweb/externalState.ts`):

| Code | Surface | Way out |
| --- | --- | --- |
| `external_management_key_rejected` | banner on Keys and Gateway | **Rebind management key** |
| `external_target_moved` | banner, no write action | start a new External connection |
| `external_cpa_version_unsupported` | banner with the version and its support class; reads stay | run a v8 or later CPA |

`external_legacy_write_replaced` is a raw wire answer of the bridge, not a page
state: no reachable page performs a write the bridge owns, so it has no banner.
| `external_rebind_refused` / `_stale_generation` / `_authority_unavailable` | inside the Rebind dialog | none, fix the input, or reload and retry |

An unrecognised code is not a state: the page keeps its own contract rather than
inventing a cause from a status. The runtime status page shows the observed CPA
version, its support class and `writeBlocked`, read-only.

The **Rebind** dialog takes a management key only (a password field, never
persisted or logged) and posts `{requestId, managementKey}`; there is no address
field because a different address is refused by design. Success reports
`rebound` or `reverified` distinctly and refetches, and the request ID survives a
failed attempt so a retry is a replay rather than a second write.

**Usage** shows statistics, the collector as a localized phase (never the raw wire
value), whether collection ever started, and the CPA version. `writeBlocked=false`
reads "writes allowed", not "writes verified": the Manager has permission, not
proof. A failed confirmation keeps the panel and offers a retry that replays the
same request. A server `detail` is never the sentence a page speaks: it is offered
behind a localized "Details" disclosure. A collector that never started reads "not started", never a
zero: a window that never opened is not coverage. Enabling statistics is a
two-step gate — opening the confirmation writes nothing, and only an explicit
confirm sends `{requestId, confirmEnableStatistics: true}`.

### D acceptance: `bin/ci/fresh-adopt-network.mjs`

```sh
revision=$(git rev-parse HEAD)
docker buildx build --builder "$BUILDER" --platform linux/amd64 --load \
  --label "org.opencontainers.image.revision=$revision" \
  --target v8-amd64-candidate -f Dockerfile.runtime -t cpamp-d-runtime:local .
docker buildx build --builder "$BUILDER" --platform linux/amd64 --load \
  --label "org.opencontainers.image.revision=$revision" \
  -f Dockerfile.manager-server -t cpamp-d-manager:local .
docker buildx build --builder "$BUILDER" --platform linux/amd64 --load \
  --label "org.opencontainers.image.revision=$revision" \
  -f Dockerfile.ingress -t cpamp-d-ingress:local .

node bin/ci/fresh-adopt-network.mjs \
  cpamp-d-runtime:local cpamp-d-manager:local cpamp-d-ingress:local bin/tmp/d-adopt
```

It is the browser journey, not an API reconstruction. A real CPA v8.0.13 runs as
its own container holding 40 credential files, one gateway config with the seeded
routing, provider and client-key sections, one credential whose item name is past
the record's limit (recorded `manual` with an instruction, never uploaded) and one
`.json` file the source cannot classify (present on the host, absent from the
source's own v8 credential listing, so never an item); the destination is the CPA
the project's own Compose Runtime stages, because an adoption needs a
Manager-owned destination. The script claims the installation, chooses Adopt,
enters the source and confirms the inventory in Chromium.

It proves, in order:

1. the four pre-commit negatives each answer their own code and commit nothing —
   a wrong management key (`adoption_source_read_unavailable`), a source with no
   client key (`adoption_source_empty`), a source that is the destination
   (`adoption_destination_identity_conflict`), and, when `CPAMP_D_CPA_LEGACY`
   names the pinned v7 binary, a v7 source;
2. the inventory screen separates what copies from what needs manual work, and the
   commit is impossible before it is confirmed;
3. the migration runs behind a TCP forwarder this script controls. It refuses every
   new connection once the run has recorded its first item, so the remaining items
   fail before writing anything and are recorded retryable — a deterministic fault
   rather than a race;
4. the interrupted screen does not call the adoption complete: a settled run with
   retryable items still on the source shows the settled state and names the count.
   Completion then answers `adopt_migration_retryable`, and the screen offers the
   acknowledgement;
5. removing the control file and pressing **Resume** continues from the durable
   prefix and finishes the run. The resumed run is read from the summary the page
   receives *after* the click, so no assertion can be satisfied by the interrupted
   run's own summary, and its rendered per-outcome numbers are reconciled against
   the route's `counts`;
6. the destination — read from the CPA the Supervisor staged, not from the Manager's
   own report — carries exactly the source client keys, the seeded gateway values
   and the migrated credential bytes (and neither the unparseable file nor the
   credential the record cannot name), admits a client key through Ingress
   `/v1/models` while refusing an unknown one, records a collection window the
   usage workspace reports as still catching up (`projection_catching_up`, so the
   check asserts that a window exists and not that usage is proven), reports a
   `runtime/status` body carrying what its page renders — mode, status, the
   Manager's own state and the gateway observation of that same read — and leaves
   the source's config bytes and credential list unchanged. The page's own
   progress reads are checked as well: a read that never left, never answered or
   never parsed fails the run, because a page that stopped polling and a read
   that failed are not the same observation.

Screenshots cover the source form, the inventory, the retryable result, the
retryable completion and the finished installation. The report goes to
`bin/tmp/d-adopt/report.json`, is written 0600, and records PASS/FAIL/NOT RUN for
every check — including the checks a failure prevented from running, which are
recorded as NOT RUN with the check that ended the run — plus `gitHead`, the image
revisions and the failure logs. Its check registry is self-checking: a check the
run recorded but never declared, or a declared check it never executed, fails the
report, so a name cannot drift out of the NOT RUN backfill unnoticed. The harness
is the External one — secrets in a sibling directory outside the artifact
directory, `assertNoSecret` over the unredacted report, the run log and every
container log before teardown, a dirty tree refused, and a `finally` that removes
only this project.

### D acceptance: `bin/ci/fresh-external-management.mjs`

```sh
# Every image must carry the commit it was built from: the run refuses an
# unlabelled image, because its checks would then be credited to source nobody
# can name.
revision=$(git rev-parse HEAD)
docker buildx build --builder "$BUILDER" --platform linux/amd64 --load \
  --label "org.opencontainers.image.revision=$revision" \
  --target v8-amd64-candidate -f Dockerfile.runtime -t cpamp-d-runtime:local .
docker buildx build --builder "$BUILDER" --platform linux/amd64 --load \
  --label "org.opencontainers.image.revision=$revision" \
  -f Dockerfile.manager-server -t cpamp-d-manager:local .
docker buildx build --builder "$BUILDER" --platform linux/amd64 --load \
  --label "org.opencontainers.image.revision=$revision" \
  -f Dockerfile.ingress -t cpamp-d-ingress:local .

node bin/ci/fresh-external-management.mjs \
  cpamp-d-runtime:local cpamp-d-manager:local cpamp-d-ingress:local OUT
```

It runs the External journey against a real CPA v8.0.13 container built from the
runtime candidate's pinned binary, on a private network, with the manager-only
image and Ingress beside it. Ingress is not scaffolding: it is the public
listener and the routing control plane, so a Manager without it cannot activate
a control base path and the run stops before the External checks. Playwright is
not a repository dependency — point `PLAYWRIGHT_MODULE` at a `playwright-core`
install and `PLAYWRIGHT_EXECUTABLE` at a Chromium binary, or the screenshot pass
is recorded as not run.

It is the acceptance for everything above:

1. claim, connect External, complete;
2. the Key lifecycle — list read back from the CPA, add, rename, replace,
   revoke, and the refusal to revoke the last credential the CPA carries;
3. the gateway: the projection, one scalar saved and read back from the CPA, then
   an out-of-band edit on the CPA and a save with the stale digest, which must
   answer `source_or_operation_conflict`;
4. the usage confirm, including the confirm-less `400`; then one `api-keys/<id>/usage`
   read ("no usage attribution while the key has no binding") taken while the
   Manager is warm, because the probe that follows restarts it and a restart
   rebinds the keys it finds. That read observes the unbound state of the staged
   key: the coverage reason is `binding_missing`, no totals are published, and no
   window is claimed complete or partial, because a zero is never made to stand in
   for "not measured". A transient catching-up or starting projection is polled
   past rather than reported. Then, with the Manager stopped so that nothing
   drains the CPA's queue, one model request with an installed Client Key and an
   out-of-band `usage-queue` read that is empty both before and after it — the
   check is named "usage queue empty at probe time", because that observation is
   all it makes. The Manager is restarted after the probe. CPA v8.0.13 credits
   usage only to an inference call that reached a Provider, so a Client-Key figure
   for a real call is an explicit `NOT RUN` ("Key usage attribution of a real call
   (no Provider fixture)") in a disposable CPA with no Provider fixture;
5. one Client Key proven directly against the CPA;
6. **rotation**: the operator changes the CPA's management key out of band and
   restarts it, after which a Keys write, a gateway save and a usage confirm each
   answer `external_management_key_rejected`, repeated reads answer locally, and
   the legacy `api-keys` `PUT` answers `external_legacy_write_replaced`. Failed
   authentications are counted by parsing the CPA log under one documented rule: a
   line counts only when it names the management surface (`/v0/management/` or
   `/v8/management/`, a management key or the secret), is an authentication
   refusal (401/unauthorized/invalid key), and — when it carries a peer address —
   names the Manager's container. A rotation produces two discovery refusals at
   most — the gated console read, and the Collector's own in-flight poll, which
   stops itself on the answer instead of retrying — so the baseline is taken once
   the Manager's own log shows the Collector stopped and the count has been quiet. The four post-rotation writes are measured against it: not one
   of them may add a management 401, and the repeated reads that follow must not
   move the count at all. The whole rotation-to-rebind window must stay below five,
   and the browser pass, which rotates the CPA a second time, is counted the same
   way;
7. rebind with the new key recovers everything, and the same key reports
   `reverified`;
8. screenshots of the Keys blocker, the rebind dialog, the usage confirmation and
   the runtime version row;
9. the v7 negative: with `CPAMP_D_CPA_LEGACY` naming a pinned v7.0.12 binary, the
   committed address is answered by that CPA instead. A completed installation
   refuses every **CONNECT** before it asks the CPA anything, so the CONNECT is
   made from a second, not-yet-completed Manager install with its own volume and
   listener (`manager-v7`), driven entirely through the bootstrap namespace. It
   must answer 409 with `external_cpa_version_unsupported`, `owner_upgrade_required`
   or `management_safe_read_unavailable` — never an authentication refusal, which
   would mean the wrong credential was presented — and the second install must
   still carry no strategy and no stage afterwards. The rebind write on the
   committed install is refused with `external_cpa_version_unsupported`, and a
   read is never answered with the version code. v7 keeps its management key, port
   and auth directory at the document root, so it needs its own config file rather
   than the v8 one.

`report.json` (mode `0600`) carries per-check `PASS` / `FAIL` / `NOT RUN`, and it
names its provenance: `gitHead` and `testedSourceCommit` are both `HEAD`, the run
refuses a dirty worktree, and every image's
`org.opencontainers.image.revision` label must name that same commit — an
unlabelled image is a failure, not an unknown. The v7 negative is `NOT RUN`
unless `CPAMP_D_CPA_LEGACY` names a pinned v7.0.12 binary.

Both CPA configs and the v7 auth directory live in a sibling
`bin/tmp/d-external-secrets-<random>` directory, never inside the artifact
directory, and that whole directory is deleted in `finally`, because a redacted
copy of a live CPA config is still a replayable one. Both management keys, the
seed and staged Client Keys, the claim and session tokens and the Admin Key are
generated in the run and asserted absent — before any redaction — from the
unredacted report object, from everything the run printed, and from `docker logs`
of every container of this run, read before any container is removed. A passing
check never prints an observed value raw, and a failing one prints it redacted.
A hit fails the run (`result: FAIL`, non-zero exit) instead of being redacted
away. Cleanup removes this run's containers, network, volumes and secrets
directory only.

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

A committed adoption carries its own `external:sha256:` target identity and keeps
`strategy = adopt_existing`; the prefixes are never merged. From that point it
behaves like a New Managed installation: the Keys page, gateway configuration
writes, gateway admission, Key usage, runtime status and operations all answer
through the same install-derived authority (see *The Adopt installation
authority* below).

That authority covers exactly two source classes: `new_managed`, and
`adopt_existing` with source class `network_existing`. `legacy_managed` is
Upgrade's source class — a destination this Fresh lane never staged, read or
verified — so it is granted no install-derived authority at all; the same
predicate refuses it on the Keys page, admission, usage and the update channel.
An `adopt_existing` checkpoint with no source class is refused the same way.

#### The Adopt installation authority

One predicate decides which strategies this Manager owns a destination for
(`freshkeyapply.ManagerOwnedStrategy`): New Managed and Adopt. Connect External
is the user's own CPA and is never answered by it — it keeps its own connection,
its own key routes and its own completion path.

The authority is derived, never cached (`cmd/…/fresh_managed.go`): the committed
checkpoint and the protected Manager connection must still name the normalized
`CPAUpstreamURL`, the checkpoint's `DestinationConnectionIdentity` must be the
identity its strategy derives — `managed:sha256:` for New Managed,
`external:sha256:` for Adopt — and the decrypted credential must be the
64-character install-derived management key. A mismatch fails closed instead of
rebinding.

**Destination posture.** The authenticated read of `/v8/management/config` with
that credential is the real proof. New Managed proves the Supervisor's exact
template; Adopt proves its own rules, because an adopted destination legitimately
carries the source's content:

| rule | New Managed | Adopt |
| --- | --- | --- |
| client keys | 1–2 canonical Manager key shapes | 1–128 unique printable source keys |
| root sections | exactly the seven template roots | a subset of CPA v8's own vocabulary (`config-version`, `api-keys`, `plugins`, `quota-exceeded`, `client`, `server`, `management`, `access`, `credentials`, `routing`, `requests`, `oauth`, `upstream`, `multimedia`, `observability`), no exact count |
| required roots | the template's | the template's: `config-version`, `server`, `management`, `access`, `oauth`, `plugins`, `observability` |
| `observability.usage.usage-statistics-enabled` | `true` | `true` |
| `plugins.enabled` | either (the operator's own switch) | either (the operator's own switch) |
| management digest | bcrypt | bcrypt |

Both postures keep the same Supervisor-owned facts: `server.host` 0.0.0.0 with
port 8317, `management.allow-remote` on with the control and update panels off,
the destination `auth-dir`, exactly one `access.api-keys` leaf, the closed
`plugins` key set, and the Supervisor plugin root. The plugin switch itself is
the operator's on both strategies — the plugin page opens for an adopted
installation too, so a saved value can never invalidate a proof — and the
recorded opt-in only drives the first start. The posture is
selected from the committed strategy (`cpa.PostureFor`) through a selector
installed on the transport, so a strategy the selector does not own — Connect
External, or an unset one — resolves to a refusing posture rather than the
loosest one. `KeySnapshot`, `WriteKeySet`, `InstallOne`, the derived key grant,
the target proof and `DefaultInstallCPAAuthorityProven` all read that posture, so
gateway admission follows the same answer.

**Usage statistics** are on for both adopted classes
(`lifecycle/destination.go`, `managedSourceClass`): the switch is a template fact
of the installation this Supervisor staged, not the operator's original CPA
posture, and Key usage and the Key workspace read the destination's own
aggregation. A source CPA that is not staged here is never rebuilt, so its
posture is untouched.

**Plugin opt-in** is accepted for Adopt and persisted on the checkpoint and in
the protected destination snapshot, exactly as for New Managed, where it drives
the template the Supervisor writes at first start. A *strategy replay* that
presents the other value is refused with `adopt_plugin_optin_conflict`, because
that is a different staged destination; the switch the destination carries
afterwards is the operator's and is not part of the posture. Connect External
still refuses the opt-in (`fresh_strategy_plugin_scope_conflict`): that plugin
root is the user's own process, not this installation's.

**Completion** (`POST complete`, `cmd/…/fresh_adopt_migration.go`) is decided
while `adoptMigrationMu` is held — the same mutex `startAdoptionMigration` takes
— so a migration run can never start between the conditions and the CAS:

1. a copy is not running → 409 `adopt_migration_running`;
2. the installation is still `committed`;
3. the connection decrypts and an authenticated read proves the Adopt posture →
   503 `adopt_destination_unproven`;
4. the destination carries at least one client key (part of the same read);
5. the run recorded `Finished` with every planned item present → 409
   `adopt_migration_incomplete`;
6. no item is retryable, unless the body carries `{"acceptRetryable": true}` →
   409 `adopt_migration_retryable` with `{"retryable": n}`.

Only then does the CAS set `ProductComplete`, still under the lock. The lock is
released before the reply is written and before the idempotent usage-collection
startup, which samples the destination over the network: a migration run must not
wait on that sample to take the same mutex. Manual items never block completion:
a manual outcome is the operator's own work on the destination, and the run
settled it.

`Planned` is the plan the run fixed after its preflight and before its first
write: what the record already held plus every item the run was going to settle.
`Finished` is written by the migration in its final checkpoint only when the item
list reached that plan — it is never inferred from the list, because a run
stopped at an item leaves a list that looks exactly like a complete one. A stop
therefore leaves `Finished` false with `len(Items) < Planned`, which is what the
completion refuses.

**The update channel stays New Managed only** (`cmd/…/fresh_updates.go`). The
Fresh version-update wiring that replaces a Supervisor-staged artifact is Later,
and until it exists there is no update path this lane may offer an adopted
installation; nothing about the operator's own CPA is the reason.


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
resume path and continues from the durable prefix; a replay that was already
waiting for the migration lock when a completion took it starts nothing, because
the start re-reads the record under that lock and refuses a completed or
re-staged installation. Step 4's completion conditions read this summary, and
the Full Web Adopt flow of Step 5 reads it through `GET installation/adoption`
(below). Model management is not part of this lane.

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

The record is also the gate on the copy itself, not only on its label, and the
record and the gate are computed from one resolver:
`internal/service/cpa/adoption_ref.go`. That file answers both questions and is
pinned by the consumer implementation digest, because it decides what an adoption
copies.

- `AdoptionItemRef(path)` is the reference of one item. A gateway leaf or payload
  rule family the accepted gateway table names is `cpa-config-routing`;
  `api-keys.*` groups and the copyable `oauth.*` children (`model-alias`,
  `excluded-models`, `settings`, `request-scoped-errors` — provider
  configuration, not the auth directory) are `provider-state-api-keys`; a
  credential file is `cpa-auth-files`; any other section — a bare root name, a
  provider group outside the copy plan, a made-up name — is `cpa-config-unknown`
  and stays manual, and never borrows a root's verdict.
- `AdoptionRootRefs(root)` is the *set* of references items under one
  discovered root carry, derived from `AdoptionItemRef` over the enumerable item
  universe — never restated, and never a reference no item produces. `oauth`
  therefore contributes both `cpa-auth-files` (a credential file is discovered in
  the source's auth directory) and `provider-state-api-keys` (its copyable
  children). `requests`, `multimedia` and `upstream` are not named sections: the
  v8 reader reports that gateway configuration under
  `unknown_cpa_native_resource`, which contributes `cpa-config-routing` for the
  paths the gateway table names and `cpa-config-unknown` for the paths it does
  not. `client` is two gateway leaves and contributes routing configuration
  only — the copy plan has no client section, so nothing there is provider state.
  A root with no enumerable item at all (`access`, `server`, `management`,
  `logging`, `usage`, `plugins`, `observability`, `providers`) contributes its
  one declared fallback, because the operator may still have to redo it by hand.
- `AdoptionItemUniverse()` also carries the source keys under a named root that
  no copyable item covers, so they cannot vanish from the record: today
  `oauth.auth-auto-refresh-workers` and the whole `oauth.providers` runtime
  block, which is why `oauth` contributes `cpa-config-unknown` in addition to
  its auth-file and provider-state references. `oauth.auth-dir` is not one of
  them — it is the directory credential files are discovered in, so
  `credential:<name>` carries it.

Discovery records one decision per reference in each reported root's set, so the
record holds a decision for every item the executor can classify under that root.
The executor then copies an item only when that item's reference is `migrate`
(`freshadopt.Executor.gated`); every other item is recorded with its reference's
manual action and is never written. Because the two sides read the same resolver,
the record cannot tell the operator to redo an item the executor copies, or
authorize one it never copies.

The resolver's own bytes are pinned with the safe-read implementation, and so is
`AdoptionUniverseDigest` — the digest of every (item, reference) pair the
classifier produces. `AdoptionItemRef` decides routing membership from the gateway
page table (`config_schema.go`), which the gateway page edits often, so folding
the universe into the pin makes a new gateway leaf, a moved rule family or a
reclassified item invalidate an acceptance, without subjecting every gateway-page
edit to a whole-file hash.

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

#### The Adopt read route and the Full Web flow

The per-item summary was durable but readable only by the completion decision, so
the browser could learn whether an adoption had finished and nothing else. Step 5
adds one read: `GET installation/adoption`, in the same `canonicalAPI` switch as
`GET operation`, allowed for the Fresh session exactly as `GET
installation/runtime` is — it is the progress read of the session that committed
the adoption.

It answers `{items[], planned, finished, running, retryable, counts}`, where each
item carries `item`, `kind`, `outcome`, `reason`, `instruction` and the store's own
`retryable` rule, `running` is the live single-flight flag rather than a recorded
value, and `counts` tallies the four outcomes. The reply omits the record's source
and destination digests: they are the Manager's evidence, and dropping them keeps
the route a statement about progress. A staged adoption, a non-adoption and an
unreadable record answer `404 {"code":"adopt_summary_unavailable"}` rather than an
empty list that reads like a finished run. The session branch is bounded by
`ProductComplete`: the claim that committed the adoption reads its own progress
until the installation completes, and afterwards the route is an Admin resource
and the same session is answered `401`.

Both wizards render one shared `FreshAdoptFlow`:

- the Adopt tile in the setup wizard and in the Console's guided Runtime page is a
  real choice, and an `adopt_existing` checkpoint renders the flow instead of an
  empty card;
- the source stage asks for the address, the management key (a password input held
  in component memory, never persisted or logged) and the plugin opt-in, and
  states the scope: credentials, gateway configuration, provider sections and
  client keys migrate; usage history, logs and plugins do not; the source is never
  written;
- the first `POST strategy` answers `staged` with `adoptionResources` and
  `adoptionDecisions` — PascalCase fields the pinned domain type has no JSON tags
  for, read as they are — and the flow groups them into what copies and what needs
  manual work. Nothing is copied until the operator confirms, and the second POST
  sends the identical body;
- the committed run is polled every two seconds with a bounded backoff until the
  record says `finished` and not `running`, then the per-item results are grouped
  by their own outcome — migrated, manual, failed and `absent_at_source` — so the
  number beside "Migrated" is `counts.migrated` and a resource the source never had
  is never counted as a copy. Each reason code has a localized sentence and the
  server's English `instruction` stays behind a "details" disclosure;
- the progress read is bounded: three consecutive failures stop the loop and show
  a typed state with an explicit retry, so a refused or lost route cannot leave
  "reading the migration record" spinning forever;
- the success headline is a claim about the installation, so it requires the run to
  be settled, no retryable item to remain, and the installation itself to be
  complete. A settled run missing any of the three shows the settled state and
  names the retryable or unprocessed items instead;
- a run that stopped, a page reloaded mid-run, or a finished run that never
  processed its own plan resumes from the durable prefix with the same `baseUrl`,
  key and `pluginsEnabled`. The key is not kept anywhere, so the resume form asks
  for it again and pins the recorded plugin choice. Completion is refused over an
  unprocessed plan, because the server cannot accept it either;
- the completion's coded refusals each have their own next action:
  `adopt_migration_running` keeps polling, `adopt_migration_incomplete` offers
  Resume, `adopt_migration_retryable` shows the count plus an explicit
  "finish anyway" checkbox that sends `acceptRetryable`, and
  `adopt_destination_unproven` names the specific failure. The acknowledgement is
  bound to the retryable set the operator was shown: a new run, or a new set, asks
  again instead of carrying the old answer into a decision nobody made.

The strategy body and both refusal envelopes are read by both carriers: the
bootstrap transport parses `Code` and `Unsupported` for a blocker and lowercase
`code` plus `retryable` for a completion, and the canonical transport forwards
only the bounded Adopt blocker vocabulary. Every parser has a test built on real
server bodies.

`FreshManagedRuntime` also sends `pluginsEnabled` for New Managed: the console
path never did, so an operator who installed from the Console could not opt into
the plugin runtime at all.

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
