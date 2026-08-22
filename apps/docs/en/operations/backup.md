# Backup And Restore

CPAMP keeps request history, configuration, and encrypted credentials on the host. The common mistake is backing up only `usage.sqlite` and missing WAL/SHM files, `data.key`, or secret files in the install directory.

## Required Backup Files

Back up these files as a set:

- `usage.sqlite`
- `usage.sqlite-wal`
- `usage.sqlite-shm`
- `data.key`

If your deployment directory contains custom configuration files, back them up too. With the one-click installer, also back up `secrets/` and `data/` under the install directory; after a successful import, `secrets/cpa-management-key` is normally gone, but it may remain after a failed upgrade or with `CPAMP_SKIP_EXECUTE=1` for retry. Manual env/secret deployments should back up their matching secret files.

## Why data.key Is Required

CPA connections saved through setup or the panel encrypt the CPA Management Key with `data.key` before saving it to SQLite.

- If only `usage.sqlite` leaks, an attacker cannot directly read the CPA Management Key.
- If both `usage.sqlite` and `data.key` leak, the CPA Management Key can be decrypted.
- If `data.key` is lost, the saved CPA Management Key cannot be recovered. You must save the CPA connection configuration again.

If a CPA connection is managed by manual environment variables or secret files, the CPA Management Key may not be written to SQLite. Back up the related secret files together with the data directory. The installer's env input is migrated into SQLite after success, so do not rely on the one-time input file alone.

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
4. If the CPA connection is env/secret-managed, also restore `secrets/` from the install directory.
5. Start CPAMP.
6. Log in and check configuration, monitoring data, and collector status.

If restore produces decryption errors, first check whether `data.key` matches the SQLite database.

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

Stop Manager Server before running this command. It encrypts the key into SQLite and never echoes it. Start the new instance, then import the remaining configuration:

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
