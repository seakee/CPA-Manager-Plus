# Releases

## v2 Beta Prerelease Channel And Assets

v2 Beta is a **Prerelease**, not Stable. `releases/latest`, automatic 1.x panel updates, and the default installer remain on **1.x Stable**. Select the matching Beta from [All Releases](https://github.com/seakee/CPA-Manager-Plus/releases) only after the maintainer actually publishes it; this page does not assert publication or authorize production migration. Do not use floating Beta tags or replace Embedded Full Web with the Beta single-file panel.

One v2 release pairs:

- Three images: `seakee/cpa-manager-plus:<tag>@sha256:<digest>`, `seakee/cpamp-ingress:<tag>@sha256:<digest>`, and `seakee/cpamp-runtime:<tag>@sha256:<digest>`, with official CPA pinned to `v8.0.55`.
- Prerelease attachments `docker-compose.v2-embedded.yml` and `embedded-release-pairing.json`, matching checksums and release notes. Never mix releases or replace only one role.
- linux/amd64 Native: `cpa-manager-plus-native-v2_<ver>_linux_amd64.tar.gz`. No v2 macOS, Windows, or linux/arm64 native assets; see [Native Deployment](../deployment/native.md).
- Independent CPA lightweight panel `management.html`; see [Lightweight Panel](../deployment/cpa-panel.md) for Stable/Beta download tracks.

**The release contract shipped with Beta, not existing builds' current update capability:** the v2 Beta `release-info.json` contract uses `schema_version: 2`, `migration_required: true`, `minimum_direct_upgrade_version: v1.14.10`, and `minimum_cpa_version: v8.0.55`. The update index is **`update-index.json`**, retaining outer `schema_version: 1`; its v2 `channels.beta` entry uses `schema_version: 2` and permits Beta Prereleases only. Baseline `8935bce5` does not implement v2 publication or that schema-2 Beta entry. PR #1137 introduces the producer contract, currently limited to explicitly opted-in `DRY_RUN=true` rehearsals; real publication/index writes remain behind a maintainer gate. This does not assert a published Beta or a changed live index.

**In-app updates do not offer v2:** `v1.14.10` Stable checks do not select v2 Beta. Manually selecting beta still rejects schema-2 ReleaseInfo, so no available v2 update is shown. Current v2 builds likewise do not offer v2 targets through in-app updates; the producer contract does not add v2 consumer support. Installation or later updates require reading the matching release notes and explicitly selecting one asset group, not automatic image replacement. For 1.x migration, follow [Offline Copy-In, Reconciliation, And Rollback](../operations/update.md), backing up `data.key`, SQLite WAL/SHM, and `usage-archives/`.

## Current Licensing And Inspectable Files

**The current repository root `LICENSE` is MIT**, and images copy that file. Official CPA's MIT LICENSE is distributed separately; third-party dependencies retain their own terms. Future licensing proposals are not effective licenses. CPAMP's MIT license does not imply that every bundled dependency uses MIT. Inspect the actual distributed files and matching license texts.

### Embedded Images

| File | Meaning |
| --- | --- |
| `/usr/local/share/licenses/cpamp/LICENSE` | CPAMP project license in all three image roles |
| `/usr/local/share/licenses/cli-proxy-api/LICENSE` | Official bundled CPA license in Runtime, not duplicated in the other two roles |
| `/usr/local/share/cpamp/release/third-party-inventory.txt` | Aggregate license collection inventory containing JSON, despite the `.txt` suffix |
| `/usr/local/share/licenses/<component>/inventory.json` | Role-specific component inventory and original LICENSE/NOTICE file digests |
| `/usr/local/share/cpamp/provenance.json` | Byte-bound version, source, role/platform, binary, and third-party inventory record |

Manager components are `manager`, `web`, and `base-image`; Ingress uses `ingress` and `base-image`; Runtime uses `runtime-supervisor` and `base-image`. Go/Web collect original texts. Alpine coverage is installed APK license metadata and available packaged notices; Debian uses installed packages' copyright files. `VERIFIED_LICENSE_COLLECTION` is not legal approval and does not turn metadata-only coverage into complete original-text coverage.

Inspect the installed matching release Compose (public build data, not runtime secrets):

```bash
docker compose -f docker-compose.v2-embedded.yml exec -T cpamp-manager \
  cat /usr/local/share/licenses/cpamp/LICENSE
docker compose -f docker-compose.v2-embedded.yml exec -T cpamp-runtime \
  cat /usr/local/share/licenses/cli-proxy-api/LICENSE
docker compose -f docker-compose.v2-embedded.yml exec -T cpamp-manager \
  cat /usr/local/share/cpamp/release/third-party-inventory.txt
docker compose -f docker-compose.v2-embedded.yml exec -T cpamp-manager \
  cat /usr/local/share/licenses/manager/inventory.json
docker compose -f docker-compose.v2-embedded.yml exec -T cpamp-manager \
  cat /usr/local/share/cpamp/provenance.json
docker compose -f docker-compose.v2-embedded.yml exec -T cpamp-ingress \
  cat /usr/local/share/cpamp/provenance.json
docker compose -f docker-compose.v2-embedded.yml exec -T cpamp-runtime \
  cat /usr/local/share/cpamp/provenance.json
```

Compare version, `sourceCommit`, platform/role with the pairing record, and inspect individual notices using inventory paths/digests. Provenance is an **unsigned build-source claim**, not protected-producer proof, a signature, or a legal conclusion. Checksums prove correspondence with a record; you must still trust the release source supplying it.

### linux/amd64 Native

From the extracted package root:

```bash
cat LICENSE
cat share/licenses/cli-proxy-api/LICENSE
cat share/cpamp/runtime/cpa-artifact.json
```

The current Native package carries CPAMP's root LICENSE and the CPA LICENSE above. Image component inventories/Provenance are not evidence for the Native package: it currently supplies neither `THIRD_PARTY_NOTICES/` nor independent Native Provenance. Container paths cannot establish Native dependency-notice verification.

## 1.x Stable: Release Entry


When reviewing a new version, focus on three things: what changed, whether anything is incompatible, and whether the upgrade requires extra backup or configuration work.

## View The Latest Version

- [Latest GitHub Release](https://github.com/seakee/CPA-Manager-Plus/releases/latest)
- [All Releases](https://github.com/seakee/CPA-Manager-Plus/releases)

Each Release lists major features, fixes, downloads, and any required upgrade notes.

## Before Upgrading

1. Read the target version's Upgrade Notes.
2. Confirm whether you use Lightweight Panel, Docker Full Mode, or a native package.
3. For Full Mode, back up SQLite, `data.key`, and secrets in the install directory.
4. Follow the matching steps in [Upgrade CPAMP](../operations/update.md).

Lightweight Panel is normally updated by CPA when it checks for a new `management.html`. If the old interface remains, see [Lightweight Panel Updates And Cache](../deployment/cpa-panel.md#updates-and-cache).

## If Something Goes Wrong

- Cannot log in after upgrade: [Reset Admin Key](../operations/reset-admin-key.md)
- Monitoring is empty after upgrade: [Monitoring Has No Data](../troubleshooting/request-monitoring.md)
- Unsure where data files are stored: [Configuration And Data Directory](../operations/configuration.md)
