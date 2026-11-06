# CPAMP v2 — Release candidate identity and Licensing/NOTICE delta (2026-10-20)

**Snapshot:** product [v2@f278dd2d6e718b393a93fa23b7a99acc87bb83a6](https://github.com/seakee/CPA-Manager-Plus/commit/f278dd2d6e718b393a93fa23b7a99acc87bb83a6), Plan [main@3ea555c20d544d384ad7f1a292a984f0391d0a82](https://github.com/seakee/cpamp-plan/commit/3ea555c20d544d384ad7f1a292a984f0391d0a82). No S2-A Manager/Ingress code, CPA fork, default Compose, installer, release workflow or license text is changed.

**Disposition: SOURCE DELTA VERIFIED / LOCAL IMAGE PAIRING NOT RUN / RELEASE + LICENSING STOP.** This is a present-HEAD incremental source/evidence check, not a replacement for [historical Lane D](https://github.com/seakee/cpamp-plan/blob/3ea555c20d544d384ad7f1a292a984f0391d0a82/active/evidence/v2-beta-lanes-20261019/lane-d/lane-d-release-readiness.md).

## B — Actual Runtime v8 candidate identity

| Component | Exact current-source evidence | Limit |
| --- | --- | --- |
| Runtime | [Dockerfile.runtime](../../../Dockerfile.runtime) has opt-in `v8-amd64-candidate` and `v8-amd64-loopback-proof` targets, pinned official CPA **v8.0.13** linux/amd64. Archive SHA256: `50ecffb47fdd81c8c5a9825a73a7a905ab66342337e274f39c4276b92d3533f3`; executable SHA256: `b682e9e42586263f476888361514f68f89ff4ebf89a5dadba394b850b797ce41`. It creates `cpa-artifact.json`. Default final target `runtime` is still **CPA v7.3.3**, not v8. | New candidate images' actual bytes, Runtime Supervisor digest and OCI image manifest digest unverified. |
| Manager / Full Web | [Dockerfile.manager-server](../../../Dockerfile.manager-server) compiles and embeds Full Web; the build chain executes `TestPackagedFullWebDist`. `SOURCE_COMMIT` defaults to `unknown` if not supplied. | Embedded complete Full Web asset-set SHA, its exact API pairing, final image/source provenance unverified. |
| Ingress | [Dockerfile.ingress](../../../Dockerfile.ingress) builds an independent Go ingress binary. No shared source-commit label or cross-component pairing contract in its recipe. | Actual source/image attribution and final digest unverified. |
| S1 private proof | [docker-compose.fresh-auth-proof.yml](../../../docker-compose.fresh-auth-proof.yml) opts into fixed v8 and namespace isolation. [#1017](https://github.com/seakee/CPA-Manager-Plus/pull/1017) contains accepted **developer-run** S1 Docker evidence; independent review checked source/evidence, not a fresh Docker replay. | S1 proof is not a general/v8 Beta deployment or public data-plane admission qualification. |
| Default Compose | [docker-compose.yml](../../../docker-compose.yml) uses moving `:latest` names for three images, and default runtime builds v7. | No immutable reviewed v8 triple-image deployment set. |
| Release producer | [.github/workflows/release.yml](../../../.github/workflows/release.yml) builds/publishes **Manager** image only, using `Dockerfile.manager-server`. Its GitHub Release assets contain Manager/native/Panel, not a multi-image v8 bundle. | No Runtime+Ingress+Manager/Full Web manifest, registry/installer/Readiness/release proof. |

### Minimal, safe, executable local audit

The [read-only inventory helper](../../../bin/ci/check-v2-private-candidate.py) inspects **three already-built** linux/amd64 images. It creates and removes only new **stopped** containers to extract the exact Manager, Ingress, Supervisor and CPA binaries plus CPA artifact JSON; it does not start services or alter user volumes. It compares the bundled CPA executable to the fixed official v8.0.13 SHA, and rejects a version/artifact mismatch.

```bash
set -euo pipefail
source_sha="$(git rev-parse HEAD)"
docker buildx build --platform linux/amd64 --target v8-amd64-loopback-proof \
  --build-arg VERSION=dev --build-arg SOURCE_COMMIT="$source_sha" \
  -f Dockerfile.runtime --load -t cpamp-audit-v8-runtime:local .
docker buildx build --platform linux/amd64 \
  --build-arg VERSION=dev --build-arg SOURCE_COMMIT="$source_sha" \
  -f Dockerfile.manager-server --load -t cpamp-audit-v8-manager:local .
docker buildx build --platform linux/amd64 \
  --build-arg VERSION=dev --build-arg SOURCE_COMMIT="$source_sha" \
  -f Dockerfile.ingress --load -t cpamp-audit-v8-ingress:local .
python3 bin/ci/test-check-v2-private-candidate.py
python3 bin/ci/check-v2-private-candidate.py --source-commit "$source_sha" \
  --manager-image cpamp-audit-v8-manager:local \
  --ingress-image cpamp-audit-v8-ingress:local \
  --runtime-image cpamp-audit-v8-runtime:local \
  --output bin/tmp/v2-private-image-inventory.json
```

**Output is explicitly `PARTIAL_BINARY_IDENTITY_ONLY_NOT_RELEASE_QUALIFIED`.** Docker image config IDs are **not** OCI manifest/index digests. Observed registry RepoDigests do not by themselves prove this exact local source-to-image relationship; the script's `--source-commit` is a recorded assertion, not attestation. The script cannot measure the entire embedded Full Web asset set or prove that the triple-image set ran together. It does not validate licensing, Docker network security, CPA authentication or public release. Run live/Readiness proof separately only under its original approved safety boundaries.

**Next Runtime/Release proof owner:** execute the tool on a uniquely built local candidate with an actual Docker host, preserve output JSON and exact binary/image bytes, measure OCI manifest digest and source/Full Web/API pairing using the existing Release owner, then validate private startup/Readiness/rollback and license packaging under applicable permissions. Do not change default v7, publish images, add a supported tag or claim Beta from this evidence.

## C — Licensing/NOTICE source inventory

| Subject | Verified source fact | Current gap and responsible owner |
| --- | --- | --- |
| CPAMP repository | [Root LICENSE](../../../LICENSE) is MIT, Copyright 2026 Seakee. | [Approved v2 policy](https://github.com/seakee/cpamp-plan/blob/3ea555c20d544d384ad7f1a292a984f0391d0a82/active/policy-v2-licensing-and-commercial-boundary.zh-CN.md) targets CPAMP-owned BSL 1.1 with AGPL-3.0 Change License and a four-year deadline. **Policy accepted ≠ license implementation**; exact Additional Use Grant, Change Date, contributor rights and professional legal review remain pending. Historic 1.x MIT grants are not revoked. |
| Embedded official CPA | [CLIProxyAPI v8.0.13 LICENSE](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.13/LICENSE) is **MIT**, with separate upstream copyright. Current Runtime Dockerfile copies the extracted official binary and artifact manifest, **not an explicit upstream LICENSE file**. | Need the CPA MIT notice/credits in actual bundled products; never relicense upstream CPA as CPAMP BSL. |
| Native Manager | [bin/release/package-native.sh](../../../bin/release/package-native.sh) explicitly copies the current CPAMP root LICENSE into every native Manager archive. | Source recipe PASS; actual archive bytes and third-party notices NOT RUN. Native v2 is not a supported Beta scope. |
| Manager, Ingress, Runtime Docker images | None of the three final-stage Dockerfiles explicitly COPYs a CPAMP LICENSE, CPA LICENSE or collected third-party NOTICE. | **Source recipe gap only**; do not infer that an actual image contains no license files at all. Inspect final image layers and delivered texts after authorized packaging changes. |
| Dependencies/assets | Go includes `modernc.org/sqlite` and other locked dependencies; npm bundles Web assets; images use Alpine/Debian base packages. | Exact Go/npm licenses, actual base-image package notices, fonts/assets and redistribution terms require a locked inventory and final-image inspection. No guessed classifications or SBOM claim. |
| Docs disclosure | [VitePress config](../../../apps/docs/.vitepress/config.ts) currently says “Released under the MIT License.” | Matches present repo license; must be coordinated with README/CONTRIBUTING and release notices **when** approved precise BSL text is ready, not changed prematurely. |
| Release packaging | Existing public workflow emits Manager image and native/Panel Release assets, not a multi-image v8 set. | Original Licensing and Release Gates are still **NO-GO**. |

**Minimal Licensing owner continuation:** (1) exact source/dependency/asset/CPA notice inventory including actual candidate image bytes; (2) maintainer/professional review of precise new BSL1.1 grant, per-version change date and contribution rights; (3) one separately approved LICENSE/NOTICE/README/CONTRIBUTING/packaging slice with source, Docker, Native and Web proof. Do not embed a runtime license server or put licensing work into the S2-A private authorization path.

## Verification and limitations

- Safe offline tests: `python3 bin/ci/test-check-v2-private-candidate.py` and `python3 -m py_compile bin/ci/check-v2-private-candidate.py`.
- NOT RUN here: Docker daemon, real downloaded CPA binary, image config/manifest digests, image NOTICE bytes, a shared running bundle, Full Web assets extraction, upstream delivery/registry availability, release/channel/installer/readiness, legal/compliance acceptance.
- Three focused self-reviews: (1) current exact source refs and previous accepted evidence retained; (2) no secret/host/runtime mutation and no upgraded security/release claim; (3) small isolated script and this report, no generic compliance/release platform, no Plan duplication or S2-A file overlap.
- Preserve S2-A Codex code ownership, default v7, Gateway restrictions, Config r3 B1 STOP, External/Upgrade and public Beta/Release STOP.
