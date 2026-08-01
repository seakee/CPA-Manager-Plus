# Releases

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
