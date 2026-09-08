# Backup And Restore

CPAMP keeps request history, configuration, and encrypted credentials on the host. The common mistake is backing up only `usage.sqlite` and missing WAL/SHM files, `data.key`, or secret files in the install directory.

## Required Backup Files

Back up these files as a set:

- `usage.sqlite`
- `usage.sqlite-wal`
- `usage.sqlite-shm`
- `data.key`
- `usage-archives/` when historical archiving has been used

If your deployment directory contains custom configuration files, back them up too. With the one-click installer, also back up `secrets/` and `data/` under the install directory; after a successful import, `secrets/cpa-management-key` is normally gone, but it may remain after a failed upgrade or with `CPAMP_SKIP_EXECUTE=1` for retry. Manual env/secret deployments should back up their matching secret files.

## Why data.key Is Required

CPA connections saved through setup or the panel encrypt the CPA Management Key with `data.key` before saving it to SQLite.

- If only `usage.sqlite` leaks, an attacker cannot directly read the CPA Management Key.
- If both `usage.sqlite` and `data.key` leak, the CPA Management Key can be decrypted.
- If `data.key` is lost, the saved CPA Management Key cannot be recovered. You must save the CPA connection configuration again.

If a CPA connection is managed by manual environment variables or secret files, the CPA Management Key may not be written to SQLite. Back up the related secret files together with the data directory. The installer's env input is migrated into SQLite after success, so do not rely on the one-time input file alone.

Archive files may contain event-level `fail_body` and `raw_json`, so protect them as sensitive data. SQLite, WAL/SHM, `data.key`, and `usage-archives/` must come from the same consistent backup point. With custom `dataDir` or `dbPath` settings, confirm the resolved archive location in the [Manager Server Guide](./manager-server.md); it can be separate from the SQLite directory. Never delete WAL manually or restore only selected archive runs.

The Usage Maintenance “Export usage” action is a complete JSONL snapshot of the current raw `usage_events`. It is independent of `USAGE_QUERY_LIMIT`, streams bounded batches from a stable snapshot boundary, and excludes events written after the export began. It is not a complete CPAMP backup and does not automatically merge raw events already deleted from SQLite back out of `usage-archives/` segments; back up the complete data set above for migration or disaster recovery.

## Docker Backup Example

If you use a named volume, stop the container first, then export through a temporary container:

```bash
docker stop cpa-manager-plus
docker run --rm \
  -v cpa-manager-plus-data:/data:ro \
  -v "$PWD":/backup \
  alpine \
  tar czf /backup/cpa-manager-plus-data.tgz -C /data .
docker start cpa-manager-plus
```

If you use a host directory mount:

```bash
docker stop cpa-manager-plus
cp -a /srv/cpa-manager-plus-data /srv/cpa-manager-plus-data.backup
docker start cpa-manager-plus
```

## Native Package Backup

Stop the process, then copy the data directory:

```bash
cp -a ./data ./data.backup
```

Windows PowerShell:

```powershell
Copy-Item -Recurse .\data .\data.backup
```

## Restore

1. Stop CPAMP.
2. Restore the full data directory.
3. Confirm that `usage.sqlite` and `data.key` come from the same backup.
4. When historical archiving has been used, restore the matching `usage-archives/` directory.
5. If the CPA connection is env/secret-managed, also restore `secrets/` from the install directory.
6. Start CPAMP.
7. Log in and check configuration, monitoring data, Usage Maintenance status, and collector status.

If restore produces decryption errors, first check whether `data.key` matches the SQLite database.

## Restore Raw Request History From A Verified Archive

Prefer a complete restore from SQLite, WAL/SHM, `data.key`, and `usage-archives/` captured at the same point in time. The segment-import procedure below is for recovering archived request history into an isolated environment when the original database is unavailable. It is not a table-level merge procedure for a live production database.

Archive segments are `.jsonl.gz` files, while usage import reads decompressed JSONL. Renaming the file is not sufficient: neither the panel file picker nor the import endpoint transparently decompresses gzip. Recover as follows:

1. Use only a run whose status is `verified` or `completed`. Preserve its manifest and original segments; do not edit the archive files in place.
2. Start an isolated recovery instance with an empty data directory and therefore an empty `usage.sqlite`. Do not import the segments into the source database that still contains the original identity ledger. That database intentionally skips the archived identities, which validates idempotency but does not restore raw rows.
3. Copy the segments to a restricted scratch directory and decompress them in filename sequence. For example:

```bash
mkdir -p ./archive-restore
chmod 700 ./archive-restore
gzip -dc -- "./usage-archives/<run-id>/<segment-name>.jsonl.gz" \
  > "./archive-restore/<segment-name>.jsonl"
chmod 600 "./archive-restore/<segment-name>.jsonl"
```

On Windows, use a trusted gzip tool to produce the same `.jsonl` file. The decompressed file can still contain `fail_body` and `raw_json`, so continue to handle it as sensitive data.

4. Sign in to the isolated recovery instance and import each decompressed `.jsonl` through Request Monitoring in segment-number order. Do not select the `.jsonl.gz` file directly.
5. On an empty recovery instance, each segment's `added` count should equal its manifest `event_count`, and `skipped` should be `0`. The sum across all segments should equal the manifest event total. Then verify Request Monitoring, Usage Analytics, and sampled event fields.
6. Keep the original archive and complete backup until validation is finished. Do not overwrite a live production data directory with the recovery instance's SQLite file, and do not merge tables manually. A complete production rollback must restore the consistent backup set.

If importing the same decompressed segment into the source database reports every event as `skipped`, the identity ledger is correctly preventing archived events from being resurrected. That is an idempotency check, not a failed recovery.

When a browser resumes an import session with an uploaded prefix, it computes the selected file's prefix SHA-256 incrementally and the server compares it with the persisted digest. The selected file must have the same content, not merely the same filename, size, or `lastModified`; a mismatch stops resumable upload and requires a new session. Legacy sessions whose uploaded prefix has no digest are not treated as safe resume targets.

The archive “Abandon task” action is allowed only for `previewed` runs and `failed` runs that have not published any archive segments and have not entered raw deletion. Once any segment has been published (including published `archived`, `verified`, and `failed` runs), or once the run enters `deleting`, partial deletion, or `completed`, cancellation is rejected. Cancelling does not delete raw usage, published archive segments, or identity-ledger entries; a run that has started raw deletion must be resumed or completed.

Note: A complete backup taken strictly before the first raw deletion (consisting of `usage.sqlite`, `usage.sqlite-wal`, `usage.sqlite-shm`, `data.key`, and `usage-archives/`) serves as the recovery boundary if a future upgrade requires complete historical raw events to rebuild derived data. After raw events are deleted, pricing model sets and context-tier thresholds are frozen, and a full historical rebuild may require restoring that pre-deletion backup or using a dedicated migration path provided by that version.

## Reclaim Physical Space After Logical Deletion

Deletion in the Usage Maintenance page removes only archived and verified raw rows. It does not immediately shrink the SQLite file. After completing the stopped backup above, run:

```bash
cpa-manager-plus compact-usage --db-path ./data/usage.sqlite
```

For a Docker named volume, run the offline command through the same image:

```bash
docker compose stop cpa-manager-plus
docker compose run --rm --no-deps cpa-manager-plus \
  compact-usage --db-path /data/usage.sqlite
docker compose up -d cpa-manager-plus
```

Stop every Manager Server connected to the database before running the command. The process-level database lock rejects a running Manager Server, and SQLite exclusive access rejects conflicting transactions. Any recorded maintenance lock or active `archiving`/`verifying`/`deleting` stage also blocks compaction; static `previewed`, `archived`, `verified`, and `failed` runs are allowed. Pending derived-data migrations are preserved exactly and continue after the server restarts; `compact-usage` does not advance, reset, or rewrite their checkpoints. If a lock belongs to a resumable active or failed run, start Manager Server and resume that run before retrying. If a lock remains for an inactive or terminal run, preserve the backup and logs and stop for diagnosis; never delete the lock or WAL manually. Keep the complete backup and reserve temporary free space conservatively equal to at least the current database-file size. After compaction, start the server and verify `/health`, `/status`, Dashboard, Usage Analytics, and Usage Maintenance. After decompressing one archive sample as described above, re-importing it into the source database should remain an idempotent skip, while importing it into an empty isolated recovery instance should add the event.

## Move Manager Configuration Without Request History

If the old `usage.sqlite` is large and request history is no longer needed, start the replacement instance with an empty data directory and use the existing Manager configuration API to move the non-sensitive CPA URL, collector, Codex inspection, and External Usage Service settings. This does not copy `usage_events`, rollups, inspection run history, model prices, API Key aliases, or account-processing policy, and it does not export the CPA Management Key.

Export while the old instance is still reachable:

```bash
export OLD_CPAMP_URL='http://old-host:18317'
export OLD_CPAMP_ADMIN_KEY='cpamp_...'

curl -fsS \
  -H "Authorization: Bearer ${OLD_CPAMP_ADMIN_KEY}" \
  "${OLD_CPAMP_URL}/usage-service/config" \
  | jq '{config: .config}' \
  > manager-config.json
chmod 600 manager-config.json
```

The new `manager-config.json` does not contain the CPA Management Key; still treat the configuration file as sensitive and do not commit or attach it to an issue. An export from an older version may contain the plaintext key, so handle it as a secret and delete it after migration.

Stop the old instance and prepare an empty data directory for the replacement. While Manager Server is not running, provide the CPA Management Key with the offline command:

```bash
cpa-manager-plus store-cpa-connection \
  --cpa-base-url 'http://cpa:8317' \
  --management-key-file '/secure/cpa-management-key' \
  --db-path './data/usage.sqlite' \
  --data-key-path './data/data.key'
```

Stop Manager Server before running this command. It encrypts the key into SQLite and never echoes it.

Connection records follow these authority rules: a complete `manager_config_v1` is authoritative; if it coexists with stale or conflicting legacy `setup` data, startup and import keep the manager connection and canonicalize setup without repair. If manager data is partial and legacy setup is complete and compatible with its existing fields, setup completes manager. The command above refuses the write and explains the repair path only when no complete authority exists and partial records conflict, or when the persisted state cannot be resolved. After confirming this explicit connection is correct, append `--repair-conflict` to repair explicitly:

```bash
cpa-manager-plus store-cpa-connection \
  --repair-conflict \
  --cpa-base-url 'http://cpa:8317' \
  --management-key-file '/secure/cpa-management-key' \
  --db-path './data/usage.sqlite' \
  --data-key-path './data/data.key'
```

`--repair-conflict` exists only for persisted state the resolver cannot trust: `manager_config_v1`/`setup` rows that conflict with each other, or authority-less partial rows that conflict with the request. It writes the connection you explicitly provide into both `manager_config_v1` and the legacy `setup` mirror in a single transaction (the key stays encrypted at rest) while preserving collector settings and other data. A complete and consistent stored connection still requires exactly matching input; repair never rebinds silently. After repairing, start normally and the connection-storage migration completes as usual. Start the new instance, then import the remaining configuration:

```bash
export NEW_CPAMP_URL='http://new-host:18317'
export NEW_CPAMP_ADMIN_KEY='cpamp_...'

curl -fsS \
  -X PUT \
  -H "Authorization: Bearer ${NEW_CPAMP_ADMIN_KEY}" \
  -H 'Content-Type: application/json' \
  --data-binary @manager-config.json \
  "${NEW_CPAMP_URL}/usage-service/config"
```

The import validates the CPA Management API. After it succeeds, verify collector status and the related settings, then securely delete the exported file and temporary key file.

If the old connection is managed through environment variables or secret files, the API reports `source` as `env` and an API import cannot override the connection fields. Use the offline command above to write the CPA connection into the new SQLite database, or enter it again during setup. Administrator credentials are also outside the Manager configuration export; the new instance uses its newly generated or explicitly configured `CPA_MANAGER_ADMIN_KEY`.
