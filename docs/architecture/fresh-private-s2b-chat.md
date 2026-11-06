# Private S2-B single-model nonstreaming Chat

**Draft / LIVE NOT RUN / HOLD. This is not S2-B PASS or merge readiness.**

Authority: [approved S2-B decision](https://github.com/seakee/cpamp-plan/blob/main/active/decision-v2-s2b-private-real-model-chat-20261020.zh-CN.md) and merged [Plan #62](https://github.com/seakee/cpamp-plan/pull/62). The maintainer deferred real-environment acceptance until the first Beta preparation. This changes the execution schedule; the existing Plan's real-LIVE-before-merge gate remains in force until formally changed. This product PR does not edit Plan.

## Product boundary

One fresh, disposable Linux/amd64 Embedded environment, one installed Canonical Client Key, one Host-fixed `openai-compatibility` Provider group, one HTTPS `/v1` target, one exact model and `POST /v1/chat/completions` with explicit `stream:false`. There is no default real Provider/model/target. There is no client-controlled BaseURL, Provider editor, model discovery, second Key writer, streaming or CPA fork.

Manager and Ingress require `CPAMP_PRIVATE_S2B_CHAT=1`; both S2-A and S2-B enabled together deny. Ingress additionally reads the single `CPAMP_PRIVATE_S2B_MODEL` at process startup and retains the existing pinned loopback Manager/CPA origins. S2-A's separate opt-in and fixed `s1-proof-model` remain unchanged.

The trusted Host prepares CPA's exact initial seed **before its first start**. It contains the original seven S1 sections plus exactly one `api-keys.openai-compatibility` group with exactly `name`, `base-url`, `request-retry:0`, `keys` and `models`. There must be one nonempty Host-injected credential and one model whose name and alias equal the locked model. No headers, proxy settings, plugins, extra routing fields, extra Providers/models/credentials or initial Client Keys are accepted. CPA's management projection adds its credential `auth_index`; an unknown index format is refused.

The existing root-owned `0600`, read-only-mounted `/run/cpamp-s1/permit.json` carries this additional nonsecret `s2b` identity:

```json
{
  "provider": "HOST_FIXED_PROVIDER_NAME",
  "model": "HOST_FIXED_MODEL_NAME",
  "target": "https://HOST_FIXED_PROVIDER_HOST/v1",
  "credentialSHA256": "SHA256_OF_THE_HOST_INJECTED_CREDENTIAL"
}
```

This illustrative object is not an executable fixture. Never put credentials in Git, environment/command arguments, URLs, CI, logs or evidence. Provider and Client credentials are separate: Client `access.api-keys` starts empty and is populated only by the existing S1 `InstallOne` operation. The real fixture is explicitly checked and then the original `emptyProofConfig` validates the remaining seed; S1/S2-A acceptance is not broadened. A S2-B permit cannot authorize blank/synthetic seed fallback.

The existing one-shot installation journal adds only an optional fixture identity hash, without a schema migration or another ledger. Requests and status observations must match that installation binding across Manager restart; a new permit cannot retarget the installed Key to a different fixture. Historical S1/S2-A journals retain their empty binding.

Every request still checks Canonical identity/active secret, installed journal, current Host lease, authenticated Runtime generation/artifact and current CPA single-Key state. S2-B also reads and exactly validates the current Provider projection before and after the existing absent/wrong/valid `/v1/models` AuthSweep. This sweep is local authentication, not a paid model call. Ingress checks the Manager verdict's Host-owned model against its startup projection before any CPA write, preventing conflicting profile/model configuration from forwarding. It retains all existing method/path/JSON bounds, duplicate/Unicode checks, original Bearer/body binding, fresh random nonce, single-use short verdict and post-dial/pre-first-byte expiry checks. No old observation or verdict is an authorization cache. A sent request or uncertain result is never retried.

## Network and live gate

**Controlled real-provider egress is NOT RUN / HOLD and is not supplied or qualified by the offline launcher.** This PR leaves Runtime, Manager and Ingress on the existing Docker internal network with no published ports. The offline TLS server binds only namespace loopback. These observations prove only that offline topology; they do not qualify any future real outbound path.

Before LIVE, the trusted Host must first prepare and independently verify a temporary, fixed-destination-only outbound arrangement. The intended minimal candidate is a Host-owned byte-forwarding tool with exactly one frozen Provider IP:443 destination, preserving end-to-end Provider TLS verification. Runtime/Ingress stay on their internal network; only the disposable tool may reach its external network. Default-deny input/output/forward rules must prevent other destinations and any new inbound access. This is a candidate to implement and qualify with the actual Host and target, not a delivered/verified topology or a CPAMP proxy service.

LIVE must separately record allowed-target TLS identity/reachability, denied other destinations, denied untrusted CPA `8317`, Runtime `9081`, private Manager verdict/control ports, the precise Ingress entry boundary, namespace/volume ownership and unchanged generation/permit. Do not publish CPA/control ports, remove `--internal`, attach Runtime/Ingress to an ordinary bridge, or reuse S2-A/offline network results as evidence for that new topology. If any condition is unknown, stop before sending paid Chat bytes.

No real Provider, real model, approved public HTTPS target, real credential, external egress or independently confirmed Provider response was supplied/exercised in this work. Their source/fixture preparation, controlled network implementation and full real-process negative matrix remain required before the Plan gate can pass. Never retry a paid attempt with an unknown outcome.

## Offline reproduction

Use unique task image tags; build `Dockerfile.runtime` with `--platform linux/amd64 --target v8-amd64-loopback-proof`, plus the unchanged Manager/Ingress Dockerfiles. Every image needs the fail-closed identity arguments `--build-arg VERSION=dev --build-arg SOURCE_COMMIT="$(git rev-parse HEAD)"`, which also populate the OCI revision label the proof checks. This embeds the unmodified official CPA v8.0.13 binary from:

```text
https://github.com/router-for-me/CLIProxyAPI/releases/download/v8.0.13/CLIProxyAPI_8.0.13_linux_amd64.tar.gz
archive SHA256: 50ecffb47fdd81c8c5a9825a73a7a905ab66342337e274f39c4276b92d3533f3
binary SHA256:  b682e9e42586263f476888361514f68f89ff4ebf89a5dadba394b850b797ce41
```

The proof uses locally built CPAMP images around that official binary; it is not an upstream-published CPA image or release provenance.

```sh
CPAMP_PROOF_IMAGE_PREFIX=cpamp-s2b CPAMP_PROOF_IMAGE_TAG=offline-20261020 \
  python3 bin/ci/fresh-s1-docker-proof.py bin/tmp/s2b-run --s2b-offline
CPAMP_PROOF_IMAGE_PREFIX=cpamp-s2b CPAMP_PROOF_IMAGE_TAG=offline-20261020 \
  python3 bin/ci/fresh-s1-docker-proof.py bin/tmp/s2b-expiry --s2b-offline --s2b-offline-expiry
```

The launcher creates fresh task-owned volumes/containers/internal network, supplies a random **fake** upstream credential through stdin before CPA starts, and installs a new local CA only in that disposable Runtime. The `.invalid` TLS stub checks integration without adding any product TLS bypass. No real Provider input/live mode exists in this offline launcher. All task containers, volumes and networks are removed on completion. It never targets existing user data or clears/replaces the installed last Key.

The existing passive packet meter independently counts CPA Chat arrivals without retaining packet/header/body/secret data. The TLS stub separately counts only path/model/stream metadata. Denials require CPA Chat arrivals zero; a stub 503 after forwarding requires exactly one CPA arrival and one stub arrival. A local HTTP 200 is labelled **offline stub only**, never real-model evidence.

## Delivery and STOP

See [sanitized verification and self-review record](evidence/fresh-private-s2b-chat/README.md). Agent A's Fresh Bootstrap pages, Console UI, installation wizard, normal Compose, Runtime source/seed and packaging were not edited.

Public Gateway, ordinary Docker qualification, External/Adopt/Upgrade, Config B1, general Key K/U, streaming/multiple models, five-path Beta, Plugin Authorization, Release/License/NOTICE and release publication remain STOP. #1002 remains PAUSED / DO NOT MERGE. Do not merge this Draft before the current Plan gate, independent latest-HEAD acceptance and Required CI are satisfied.
