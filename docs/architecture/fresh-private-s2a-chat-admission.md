# Private Fresh S2-A Chat admission

Implements the maintainer-authorized slice in [Plan #58](https://github.com/seakee/cpamp-plan/pull/58).
Within the disposable S1 Linux/amd64 proof only, the installed Canonical Client
Key can complete nonstreaming synthetic Chat through Ingress. This does not
qualify public Gateway, a normal Docker profile, External, Upgrade or a release.

## Request and authority

Both Manager and Ingress require `CPAMP_PRIVATE_S2A_CHAT=1`. Ingress additionally
requires the exact loopback Manager/CPA origins `http://127.0.0.1:18319` and
`http://127.0.0.1:8317`. Manager still requires the existing S1 permit configuration,
fixed Runtime origin, a committed New Managed Fresh target and its original
generation. Environment flags alone cannot authorize a request.

Ingress accepts only exact `POST /v1/chat/completions`, `application/json`, a
complete Content-Length-framed body of at most 64 KiB and depth 32, top-level
unique `stream:false` and `model:"s1-proof-model"`, and one original Bearer.
Duplicate names (including escaped/Unicode case aliases), invalid UTF-8 or UTF-16,
compression, chunking, upgrades, query strings and ambiguous targets deny 503.
Other Fresh model APIs remain 503. No partially inspected body reaches CPA.

For each request Ingress hashes a new random nonce, the original Bearer and exact
validated bytes. Only the binding and Bearer are sent over trusted loopback to
Manager's `/internal/fresh-chat-admission`. This endpoint is denied locally by
the public classifier in both Fresh and Legacy modes, including encoded and
normalized variants and after Ingress restart. It is absent without private
opt-in and rejects non-loopback peers. There is no public verdict API.

Manager uses the existing S1 journal's Canonical APIKeyID and RunID/generation.
The candidate must equal that active identity's decrypted, hash-validated secret.
Every call rechecks authenticated Runtime identity/generation/ready artifact,
the root-owned S1 Host permit, the current **single** CPA Key list, and real CPA
data-plane authentication: absent/wrong/valid Key must return 401/401/200.
Runtime and CPA configuration are checked again, and the same permit must remain
valid at completion. No S1 five-second UI observation or previous verdict is read.
Unavailable SQLite/Protector/Manager/Runtime/CPA or unknown observations deny.
Generation mismatches preserve 409 internally and in the existing Admin fence;
Ingress returns its generic 503 without exposing the internal result.

The verdict echoes only the request binding and expires at the earlier of the
Host permit or one second from this admission's start, including lock wait.
Ingress validates that binding and deadline, disables connection reuse/replay,
and checks the deadline again in the connection's first `Write`, after dialing.
Only the exact checked body and original Client Bearer go to CPA. Manager or
Management credentials and client routing headers never replace that identity.
CPA performs its own independent authentication.

Expiry before the first CPA byte prevents forwarding. Once bytes were sent,
the operation remains an actual upstream attempt: an ensuing transport failure
is 502, without retry; a CPA response keeps its actual status. The admission
deadline is not an in-flight cancellation lease. Later requests always recheck.

## Disposable verification

Use unique task-owned image tags; no existing CPA, production volume, real Key,
public port or default Compose is selected. Build the three existing Dockerfiles
for Linux/amd64, using the Runtime target `v8-amd64-loopback-proof` and the fixed
official CPA v8.0.55 executable. Do not rebuild or modify CPA from source.

```sh
revision=$(git rev-parse HEAD)
docker buildx build --platform linux/amd64 --target v8-amd64-loopback-proof \
  --build-arg VERSION=dev --build-arg SOURCE_COMMIT="$revision" \
  -f Dockerfile.runtime --load -t cpamp-s2a-runtime:private .
docker buildx build --platform linux/amd64 \
  --build-arg VERSION=dev --build-arg SOURCE_COMMIT="$revision" \
  -f Dockerfile.manager-server --load -t cpamp-s2a-manager:private .
docker buildx build --platform linux/amd64 \
  --build-arg VERSION=dev --build-arg SOURCE_COMMIT="$revision" \
  -f Dockerfile.ingress --load -t cpamp-s2a-ingress:private .
CPAMP_PROOF_IMAGE_PREFIX=cpamp-s2a CPAMP_PROOF_IMAGE_TAG=private \
  python3 bin/ci/fresh-s1-docker-proof.py bin/tmp/s2a/new-main --s2a
CPAMP_PROOF_IMAGE_PREFIX=cpamp-s2a CPAMP_PROOF_IMAGE_TAG=private \
  python3 bin/ci/fresh-s1-docker-proof.py bin/tmp/s2a/new-expiry --s2a --s2a-expiry
```

The existing S1 launcher owns new volumes, isolation, exclusive namespace/source
checks, initial installation and cleanup. The S2-A helper adds only verification:
a trusted loopback relay can delay an **unchanged real Manager verdict**, and a
passive loopback meter counts CPA Chat, total CPA and private Manager arrivals.
The meter has NET_RAW only, no data/credential mounts, uses a kernel port filter
and retains counts only. Any packet loss is STOP. Official CPA stdout is discarded
by Supervisor and cannot establish arrival counts. Synthetic upstream arrivals
are independently counted. The baseline zero-Key direct-CPA positive control
calibrates the counter and demonstrates why Ingress must deny before CPA.

Host root, Docker daemon, the serial launcher and its explicitly inspected
namespace consumers are trusted for this short permit. They must not mutate CPA
configuration or attach unknown consumers during the permit. The relay and meter
are disposable test tooling, not production proxy/authentication services.
The expiry run waits for the original permit to expire with Runtime still ready;
it does not renew a permit, reinstall a Key or change the journal.

Retained evidence and self-review results: [S2-A evidence](evidence/fresh-private-s2a-chat/README.md).
Routed IPv6, external hosts, cross-generation recovery, ordinary deployment,
public Gateway, streaming, multiple installed Keys and Beta/Release remain
**NOT RUN / STOP**. Runtime restart must leave CPA offline and preserve the
existing Admin generation 409; no recovery is attempted.
