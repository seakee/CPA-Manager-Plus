# Backup And Restore

CPAMP keeps request history, configuration, and encrypted credentials on the host. The common mistake is backing up only `usage.sqlite` and missing WAL/SHM files, `data.key`, or secret files in the install directory.

## Required Backup Files

Back up these files as a set:

- `usage.sqlite`
- `usage.sqlite-wal`
- `usage.sqlite-shm`
- `data.key`
- `usage-archives/` when historical archiving has been used

If your deployment directory contains custom configuration files, back them up too. With the one-click installer, also back up `secrets/` under the install directory; full installation and env/secret-managed connections store the CPA Management Key in `secrets/cpa-management-key`.

## Why data.key Is Required

CPA connections saved through setup or the panel encrypt the CPA Management Key with `data.key` before saving it to SQLite.

- If only `usage.sqlite` leaks, an attacker cannot directly read the CPA Management Key.
- If both `usage.sqlite` and `data.key` leak, the CPA Management Key can be decrypted.
- If `data.key` is lost, the saved CPA Management Key cannot be recovered. You must save the CPA connection configuration again.

If the CPA connection is managed by environment variables or secret files, the CPA Management Key is not written to SQLite. Back up the related secret files together with the data directory.

Archive files may contain event-level `fail_body` and `raw_json`, so protect them as sensitive data. SQLite, WAL/SHM, `data.key`, and `usage-archives/` must come from the same consistent backup point. With custom `dataDir` or `dbPath` settings, confirm the resolved archive location in the [Manager Server Guide](./manager-server.md); it can be separate from the SQLite directory. Never delete WAL manually or restore only selected archive runs.

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

If the old `usage.sqlite` is large and request history is no longer needed, start the replacement instance with an empty data directory and use the existing Manager configuration API to move the CPA connection, collector, Codex inspection, and External Usage Service settings. This does not copy `usage_events`, rollups, inspection run history, model prices, API Key aliases, or account-processing policy.

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

`manager-config.json` may contain the CPA Management Key in plaintext. Treat it as a secret, do not commit it, and do not attach it to an issue.

Stop the old instance and start the new instance with an empty data directory. Record the new administrator key generated during first startup, then import the configuration:

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

The import validates the CPA Management API. After it succeeds, verify collector status and the related settings, then securely delete the exported file.

If the connection is managed through environment variables or secret files, the API reports `source` as `env` and an import cannot override the connection fields. Move `CPA_UPSTREAM_URL`, `CPA_MANAGEMENT_KEY`, or the matching secret files through the deployment environment instead. Administrator credentials are also outside the Manager configuration export; the new instance uses its newly generated or explicitly configured `CPA_MANAGER_ADMIN_KEY`.
