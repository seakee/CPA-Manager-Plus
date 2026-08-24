# Reset the Manager Server Admin Key

This guide resets the CPA Manager Plus Manager Server admin key used by the full Docker / native Manager Server-hosted panel. It does not reset the CPA Management Key, and it does not recover a lost `data.key`.

The reset command edits the local SQLite database. Stop Manager Server first so SQLite is not being written while the credential changes.

## What the Command Does

`cpa-manager-plus reset-admin-key` replaces `settings.admin_credential_v1` in the Manager Server SQLite database with a new salted HMAC credential.

- If no key is provided, it generates a long random `cpamp_...` key using the same generator as first startup.
- If a key is provided, the command stores only its digest and does not print the key back.
- The command does not start the HTTP server, collector, or background workers.
- The command does not need the CPA Management Key or `data.key`.

The alias `reset-admin-password` is also accepted for users who think of the admin key as a password.

## Before You Start

1. Stop Manager Server.
2. Back up the full data directory, including `usage.sqlite`, `usage.sqlite-wal`, and `usage.sqlite-shm` when those files exist.
3. Make sure you are pointing at the real Manager Server database:
   - Docker default: `/data/usage.sqlite`
   - Native default: `data/usage.sqlite` next to the binary
   - Custom deployments: the `USAGE_DB_PATH` value or the local path in `USAGE_DB_URL`

## Docker Compose

From the directory that contains your compose file:

```bash
docker compose -f docker-compose.manager.yml stop cpa-manager-plus
docker compose -f docker-compose.manager.yml run --rm cpa-manager-plus reset-admin-key
docker compose -f docker-compose.manager.yml up -d cpa-manager-plus
```

The command prints a generated key once:

```text
CPA Manager Plus admin key reset.
New admin key: cpamp_...
Save this value now. It will not be shown again.
```

Use that key on the Manager Server login page after restart.

## Docker Named Volume

If you run the container manually with the default named volume:

```bash
docker stop cpa-manager-plus
docker run --rm \
  -v cpa-manager-plus-data:/data \
  seakee/cpa-manager-plus:latest \
  reset-admin-key
docker start cpa-manager-plus
```

If you use the GitHub Container Registry image, replace `seakee/cpa-manager-plus:latest` with `ghcr.io/seakee/cpa-manager-plus:latest`. A manual `docker run` for a URL deployment must also pass the original `USAGE_DB_URL`; Compose is preferred because it inherits the service environment.

## Docker Host Directory Mount

If your container maps a host directory to `/data`, mount the same directory in the reset container:

```bash
docker stop cpa-manager-plus
cp -a /srv/cpa-manager-plus-data /srv/cpa-manager-plus-data.backup
docker run --rm \
  -v /srv/cpa-manager-plus-data:/data \
  seakee/cpa-manager-plus:latest \
  reset-admin-key
docker start cpa-manager-plus
```

## Provide a Specific Admin Key

Prefer `--admin-key-file` so the key is not stored in shell history:

```bash
printf '%s\n' 'replace-with-a-long-random-admin-key' > /srv/new-cpamp-admin-key.txt
docker stop cpa-manager-plus
docker run --rm \
  -v cpa-manager-plus-data:/data \
  -v /srv/new-cpamp-admin-key.txt:/run/secrets/new_admin_key:ro \
  seakee/cpa-manager-plus:latest \
  reset-admin-key --admin-key-file /run/secrets/new_admin_key
docker start cpa-manager-plus
```

`--admin-key` is available for controlled environments, but it may be recorded by shell history or process auditing:

```bash
cpa-manager-plus reset-admin-key --admin-key 'replace-with-a-long-random-admin-key'
```

## Native Packages

Stop the native process first. Then run the command from the extracted package directory:

macOS / Linux:

```bash
./cpa-manager-plus reset-admin-key
```

Windows PowerShell:

```powershell
.\cpa-manager-plus.exe reset-admin-key
```

If the deployment uses `USAGE_DB_URL`, run in the same configured environment and omit `--db-path` so the URL parameters are preserved. Passing `--db-path` selects the default SQLite settings.

If a path-only SQLite database is not in the default package data directory, pass it explicitly:

macOS / Linux:

```bash
./cpa-manager-plus reset-admin-key --db-path /path/to/usage.sqlite
```

Windows PowerShell:

```powershell
.\cpa-manager-plus.exe reset-admin-key --db-path C:\path\to\usage.sqlite
```

Then restart the native process and log in with the new admin key.

## Troubleshooting

- **`SQLite database not found`**: you are not running the command in the same configured environment as Manager Server. URL deployments should pass the original `USAGE_DB_URL` and omit `--db-path`; path deployments may pass `--db-path`. Also mount the correct Docker volume/host directory.
- **`is empty` / `does not look like a CPA Manager Plus Manager Server database`**: the path points to the wrong file or to a newly created empty file. Find the real `usage.sqlite` from the Manager Server data directory.
- **`database is locked`**: Manager Server or another process is still using SQLite. Stop it and rerun the command.
- **Login still fails**: confirm the panel is using the same Manager Server whose database was reset. For Docker, verify the volume name or host mount path.
- **Generated key was not saved**: rerun the command while Manager Server is stopped. A new random key will be generated.

## Security Notes

- Treat the generated `cpamp_...` value as a secret.
- Rotate any stored deployment secret if the old admin key may have leaked.
- This reset only changes Manager Server authentication. CPA credentials and encrypted CPA Manager Plus configuration are unchanged.
