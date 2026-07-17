# Reset Admin Key

Use this page when you have lost the CPAMP Admin Key for Full Docker or native Manager Server mode. It does not reset the CPA Management Key and cannot recover a lost `data.key`.

The reset command modifies the local SQLite database directly. Stop Manager Server before running it so the service does not keep writing to SQLite.

## What The Command Does

`cpa-manager-plus reset-admin-key` replaces `settings.admin_credential_v1` in the Manager Server SQLite database with a new salt and HMAC digest.

- If no key is provided, it generates a `cpamp_...` admin key.
- If a key is provided, only its digest is saved. The command does not echo the provided key.
- The command does not start the HTTP service, collector, or background jobs.
- The command does not need the CPA Management Key or `data.key`.

The alias `reset-admin-password` is also available.

## Before You Run It

1. Stop Manager Server.
2. Back up the full data directory, including `usage.sqlite`, `usage.sqlite-wal`, `usage.sqlite-shm`, and `data.key`.
3. Confirm the command points to the real Manager Server database:
   - Docker default: `/data/usage.sqlite`
   - Native package default: `data/usage.sqlite` next to the binary
   - Custom deployment: the value of `USAGE_DB_PATH`

## Docker Compose

```bash
docker compose -f docker-compose.manager.yml stop cpa-manager-plus
docker compose -f docker-compose.manager.yml run --rm cpa-manager-plus reset-admin-key
docker compose -f docker-compose.manager.yml up -d cpa-manager-plus
```

The command prints the newly generated key once:

```text
CPA Manager Plus admin key reset.
New admin key: cpamp_...
Save this value now. It will not be shown again.
```

### One-Click Installer Docker Deployments

If `install-cpamp.sh` created the Docker deployment, prefer letting the installer stop the service, reset the credential, restart, and verify login:

```bash
curl -fsSLO https://raw.githubusercontent.com/seakee/CPA-Manager-Plus/main/bin/install-cpamp.sh
CPAMP_OPERATION=repair \
CPAMP_INSTALL_DIR="$HOME/cpa-manager-plus" \
bash install-cpamp.sh
```

The repair flow synchronizes the SQLite admin credential with `secrets/cpamp-admin-key` in the install directory, so the file and actual login key remain aligned. It does not delete the Docker data volume, CPA Management Key, or request history.

Non-interactive environments require explicit confirmation:

```bash
curl -fsSLO https://raw.githubusercontent.com/seakee/CPA-Manager-Plus/main/bin/install-cpamp.sh
CPAMP_OPERATION=repair \
CPAMP_INSTALL_DIR="$HOME/cpa-manager-plus" \
CPAMP_NON_INTERACTIVE=1 \
CPAMP_CONFIRM=1 \
bash install-cpamp.sh
```

## Docker Named Volume

```bash
docker stop cpa-manager-plus
docker run --rm \
  -v cpa-manager-plus-data:/data \
  seakee/cpa-manager-plus:latest \
  reset-admin-key
docker start cpa-manager-plus
```

If you use the GitHub Container Registry image, replace `seakee/cpa-manager-plus:latest` with `ghcr.io/seakee/cpa-manager-plus:latest`.

## Provide A Specific Admin Key

Prefer `--admin-key-file` so the key does not enter shell history:

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

## Native Packages

macOS / Linux:

```bash
./cpa-manager-plus reset-admin-key
```

Windows PowerShell:

```powershell
.\cpa-manager-plus.exe reset-admin-key
```

If the SQLite database is not in the default data directory, pass the path explicitly:

```bash
./cpa-manager-plus reset-admin-key --db-path /path/to/usage.sqlite
```

## Troubleshooting

- `SQLite database not found`: the command is not running against the real configured environment. Pass `--db-path`, or mount the correct Docker volume or host directory.
- `is empty` / `does not look like a CPA Manager Plus Manager Server database`: the path points to the wrong file or a newly created empty file.
- `database is locked`: Manager Server or another process is still using SQLite. Stop related processes and retry.
- Login still fails after reset: confirm the panel is accessing the same Manager Server.
- The new generated key was not saved: run the command again while Manager Server is stopped. It will generate another random key.
