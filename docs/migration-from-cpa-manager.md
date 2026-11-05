# Migration from CPA-Manager to CPA Manager Plus

This guide is for users migrating from the old `seakee/cpa-manager` / CPA-Manager project to `seakee/cpa-manager-plus`. The goal is to preserve historical usage events, model prices, API key aliases, and saved CPA connection settings.

## What Changed

- Docker image: `seakee/cpa-manager` -> `seakee/cpa-manager-plus`.
- Native binary/package: `cpa-manager` -> `cpa-manager-plus`.
- Backend source layout: old `usage-service` -> `apps/manager-server`. Compatible HTTP endpoints under `/usage-service/*` are still kept.
- Full Docker login now uses the Manager Server admin key `cpamp_...`, not the CPA Management Key.
- CPA Management Key is encrypted with `/data/data.key` before it is stored in SQLite.
- Legacy `settings.setup` is migrated to `settings.manager_config_v1` and kept as compatibility data.

## Before You Start

1. Check CPA version: `v7.1.0+` is recommended; HTTP usage queue support requires at least `v6.10.8+`.
2. Identify the old Manager Server data location:
   - Docker volume is commonly `cpa-manager-data`.
   - Host directory mounts usually map to container `/data`.
   - Native packages default to `data/usage.sqlite` next to the binary.
3. Stop the old container or process so SQLite WAL files are no longer being written.
4. Back up the full old data directory, not only a single database file. Keep at least:
   - `usage.sqlite`
   - `usage.sqlite-wal`
   - `usage.sqlite-shm`
5. Decide the admin-key strategy. During migration, prefer setting `CPA_MANAGER_ADMIN_KEY` or `CPA_MANAGER_ADMIN_KEY_FILE` explicitly instead of relying on the first startup log.

## Docker Volume Migration

Old compose files often looked like:

```yaml
services:
  cpa-manager:
    image: seakee/cpa-manager:latest
    volumes:
      - cpa-manager-data:/data

volumes:
  cpa-manager-data:
```

Mount the old volume into Plus:

```yaml
services:
  cpa-manager-plus:
    image: seakee/cpa-manager-plus:latest
    restart: unless-stopped
    ports:
      - "18317:18317"
    environment:
      HTTP_ADDR: "0.0.0.0:18317"
      USAGE_DB_PATH: "/data/usage.sqlite"
      CPA_MANAGER_DATA_KEY_PATH: "/data/data.key"
      CPA_MANAGER_ADMIN_KEY: "replace-with-a-long-random-admin-key"
      USAGE_COLLECTOR_MODE: "auto"
    volumes:
      - cpa-manager-data:/data

volumes:
  cpa-manager-data:
    external: true
```

The sample Plus compose creates `cpa-manager-plus-data` by default. If you use that new volume directly, the panel will look like a fresh installation and old data will not appear.

## Host Directory Migration

If the old container used a host directory:

```bash
docker run -d \
  --name cpa-manager \
  -p 18317:18317 \
  -v /srv/cpa-manager-data:/data \
  seakee/cpa-manager:latest
```

Migrate with:

```bash
docker stop cpa-manager
cp -a /srv/cpa-manager-data /srv/cpa-manager-data.backup

docker run -d \
  --name cpa-manager-plus \
  --restart unless-stopped \
  -p 18317:18317 \
  -v /srv/cpa-manager-data:/data \
  -e CPA_MANAGER_ADMIN_KEY='replace-with-a-long-random-admin-key' \
  seakee/cpa-manager-plus:latest
```

Then open `http://<host>:18317/management.html` and log in with the admin key.

## Native Package Migration

1. Stop the old `cpa-manager` process.
2. Back up the old program directory, especially `data/usage.sqlite*`.
3. Extract `cpa-manager-plus_<version>_<os>_<arch>`.
4. Copy the old `data` directory into the new package directory, or set `USAGE_DATA_DIR` / `USAGE_DB_PATH` to the old data path.
5. Prefer setting the admin key on first startup:

```bash
CPA_MANAGER_ADMIN_KEY='replace-with-a-long-random-admin-key' ./cpa-manager-plus
```

If it is not set, the service generates a `cpamp_...` value and prints it only once in the first startup log.

## Validate the First Startup

1. Check startup logs:
   - If `CPA_MANAGER_ADMIN_KEY` was not set, save `CPA Manager Plus admin key generated: cpamp_...`.
   - Ensure there are no `decrypt secret`, `open sqlite`, or `bootstrap manager server` errors.
2. Open **Configuration -> CPA Manager Plus Configuration**.
3. Verify the bound CPA URL, request monitoring, collection mode, and polling interval.
4. Open the dashboard or monitoring page and confirm historical data is visible.
5. Request `/status` and check collector state, `lastConsumedAt`, `lastInsertedAt`, and `lastError`.
6. Back up the migrated `/data`; from now on it must include the new `data.key`.

## Data Key Backup Rules

Plus encrypts the CPA Management Key before storing it in SQLite. The default data key is `/data/data.key`.

- If only `usage.sqlite` leaks, the CPA Management Key is not directly readable.
- If both `usage.sqlite` and `data.key` leak, the CPA Management Key can be decrypted.
- If `data.key` is lost, encrypted CPA Management Key values cannot be recovered; save the CPA connection again.

Backups must include both SQLite files and `data.key`. Do not upload `data.key` in public troubleshooting material.

## Lost Admin Key Recovery

Admin-key reset instructions are maintained separately. Stop Manager Server, back up `/data`, and follow [Reset the Manager Server Admin Key](reset-admin-key.md).

## Managed CPA Import

`cpa-manager-plus upgrade cpa-import` stages, binds and connects the v2 destination for a stopped 1.x stack's managed CPA state, and copies the state the source can prove into it. The copy runs under the compiled adoption authority this build carries: the accepted `CPA8-STAGE-r1` staging transition and the accepted `CPA8-ADOPT-RESOURCE-r1` destination replay chain, both pinned to the same CPA v8 artifact. Provider credential files (`cpa-auth-files`), the gateway routing configuration (`cpa-config-routing`) and the provider runtime state (`provider-state-api-keys`) are written into the destination and read back. A build whose compiled record does not prove both consumers refuses with `upgrade_cpa_import_unproven` before the destination is staged, so an unproven build writes nothing.

The rules that matter when you plan the move:

- It runs after reconciliation. The recorded operation must be at `reconcile` (or at its own `cpa_import` stage without the `cpa_config` domain, which is the retry point); anything else is refused with `upgrade_cpa_stage_conflict` before the source is read. A refusal while staging the destination leaves the recorded position untouched.
- Stop the 1.x Manager and the managed CPA, then stop Manager Server: the command holds the v2 data-root process lock and refuses with `upgrade_store_locked` (exit 1) while another process holds it. It also takes the 1.x source's ownership marker through a read-only descriptor — the same exclusive fence the copy-in took, so a source preserved on a read-only mount still proves exclusivity — and holds it for the whole run; another holder, or a source with no marker, refuses with `upgrade_cpa_source_locked` before the CPA directory is read.
- **Residual limit of that marker.** The marker proves the 1.x Manager is stopped. It does not prove the managed CPA container is stopped: the CPA never holds this marker, and the command cannot see a running container. Stop the `cli-proxy-api` service (`docker compose stop cli-proxy-api`) before importing.
- It reads the `--legacy` directory only if that directory is the one the recorded `copy-in` step read; a different tree is refused with `upgrade_cpa_source_root_mismatch` before anything is read.
- The destination's own paths are never copied: its management secret, listener, ports, plugin root, auth directory and client key list stay the destination's.
- The source release is the exact release `--cpa-version` names, or the `cli-proxy-api` image tag the stack declares. A 1.x install writes `image: ${CPA_IMAGE}` in `compose.yaml`, so the reader resolves that one variable from the `.env` file beside it; no other variable in `.env` is parsed, retained or reported. A moving tag, a digest (including `repo:v7.3.4@sha256:...`), or a reference that does not resolve names no release, and the import refuses with `upgrade_cpa_version_required` instead of guessing. Both inputs are declarations: the command never runs or inspects the source image, so a mistaken `--cpa-version` is not caught by comparing it with the running CPA.
- The destination attempt is immutable. When the step stages the destination itself it records the generated management secret (encrypted with `/data/data.key`), the complete request the Supervisor publishes — expected Runtime identity, generation and active artifact, source identity and class, the ordered client key list, the plugin choice — the digest of the source credential set, and the operation ID, all before staging. A retry compares today's inputs with that record before changing anything, and a changed source, credential set, plugin choice, client key set or secret — or a complete attempt recorded by an earlier build without a credential snapshot — is refused with `upgrade_cpa_destination_request_changed` while the recorded attempt stays as it was (recover from a pre-snapshot attempt with `cpa-manager-plus upgrade rollback` and re-run). The destination URL is the exception, because the Supervisor's request does not carry it: it is recorded only once a run proved it is the staged destination, and from then on it is as immutable as the rest.
- The destination is bound before anything else addresses it. Every staging arm ends in an authenticated read of the client key list with the secret this run staged; a CPA that does not answer with both is refused with `upgrade_cpa_import_target_foreign` and receives neither configuration nor credentials. A URL where nothing answers is a different outcome: the refusal is `upgrade_cpa_import_target_unreachable`, and it says nothing was listening, because the destination may still be starting, or the URL may name a 1.x CPA that is already stopped — the CLI defaults to `CPA_UPSTREAM_URL`, which usually still names the 1.x CPA. Do not start that CPA to make this URL answer: a started 1.x CPA rejects an absent and an unknown credential, so it passes the unauthenticated probe and fails the proof, which is the refusal this message describes.
- When the step completes its work, the destination URL and the same management secret are stored in the Manager's own CPA connection (`manager_config_v1` and its legacy setup mirror), so the Console can reach the destination after a restart. Unrelated Manager settings are preserved. A Manager that already holds a connection to a different CPA is refused with `upgrade_cpa_connection_conflict` instead of being repointed; rebind the stored connection explicitly (`store-cpa-connection --repair-conflict`) while Manager Server is stopped and re-run, and the recorded destination secret is reused.
- The destination attempt row is the evidence the completion gate re-reads. It carries the address a run proved, the source identity and Supervisor authority the destination was staged for, the server fingerprint (the destination's build version combined with the digest of the v8 configuration body), and the sealed management secret. `upgrade verify` re-reads it, confirms the live Manager-owned Runtime reports `ready` on the recorded Runtime identity and accepted active gateway artifact, re-proves the live CPA with the secret the Manager stores, and only then completes the operation. A store whose destination evidence, live Runtime authority or sealed connection no longer matches is refused instead of admitted.
- The `cpa_config` domain is recorded only after the executor settled every item: it is the claim that the destination holds the copied state. A failed or partial copy leaves the operation at `cpa_import` without the domain, which is the retry point. A recorded import is replayed, not re-copied: it requires the Manager-owned Runtime to report `ready` on the accepted artifact and the recorded Runtime identity, re-proves the recorded destination with the secret the Manager stores, and refreshes the recorded fingerprint from the configuration that destination serves now — the recovery the verification instruction asks for after a manual item was applied. A Runtime that is `offline`, `unknown` or `starting`, or that reports an unqualified artifact or identity, fails closed with `upgrade_cpa_import_unproven` before making any destination request, without refreshing the recorded fingerprint or changing the operation row; when the Runtime is `ready` and qualified but the destination endpoint is unreachable, replay attempts the unauthenticated listener probe and fails closed with `upgrade_cpa_import_target_unreachable` without sending the management secret, refreshing the recorded fingerprint, or changing the operation row.
- Every item the adoption rules do not mark migrate is a manual item with that rule's own action, never a silent drop: for example `credential:gemini-user.json` (`gemini_credential_unsupported`, because CPA v8 never lists a gemini credential), `config:codex.identity-confuse` and `section:ampcode` (`removed_upstream`), and `section:something-new` (`not_in_import_scope`). Items the reader classifies as manual keep their specific reason, such as a symbolic link (`credential_not_regular`), a file above the 1 MiB upload bound (`credential_too_large`) or an unreadable entry (`credential_unreadable`); copy or re-create those yourself.
- A source declaring `config-version: 8` accepts only a declared v8 release; its client keys are read from `access.api-keys`, and an `api-keys.<family>` the table does not map is reported instead of dropped.

This step does not promote installation-derived authority: it creates no Fresh installation record, strategy checkpoint or qualification. The existing consumers deliberately refuse an upgrade-derived (`legacy_managed`) source, so the destination is managed as a connection, not as a New Managed installation. The copy itself, however, is no longer deferred: it runs under the compiled qualification this build carries.

**Stated limit: `codex-header-defaults`.** CPA v8 still loads the section (`codex-header-defaults` loads as `oauth.providers.codex.header-defaults`), but the v2 destination exposes no adoptable `upstream.codex.header-defaults` leaf, so the copy does not include it. The operation row reports it as the manual item `section:codex-header-defaults`; re-create those header defaults in the destination CPA configuration if you still need them. The sibling `claude-header-defaults.*` leaves do have destination paths and are part of the copied set.

## Rollback

Stop Plus before rollback. The old CPA-Manager can still read the main usage tables and legacy `settings.setup`, but it does not understand Plus admin credentials, bootstrap state, or the data key. Prefer rolling back to the backup taken before migration.

## FAQ

- Old data is missing: the Plus container is probably mounting a new empty `cpa-manager-plus-data` volume instead of the old `cpa-manager-data`.
- Login returns 401: Manager Server endpoints require the Manager Server admin key. The CPA Management Key only logs in to CPA panel mode.
- Monitoring is empty: enable CPA usage publishing, match `USAGE_COLLECTOR_MODE` to your network path, and make sure only one Manager Server consumes usage from the CPA instance.
- Decryption fails: confirm `/data/data.key` was not lost or replaced.
