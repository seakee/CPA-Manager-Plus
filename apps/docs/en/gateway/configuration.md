# Prepare The CPA Gateway

Check this page only when deploying Full Mode manually or when Monitoring never receives data. Users who installed “CPA + CPAMP” with the installer can usually skip it.

## Two Required Settings

### 1. Allow CPAMP To Manage CPA

```yaml
remote-management:
  secret-key: 'replace-with-a-long-random-management-key'
  allow-remote: true
```

- `secret-key` is the CPA Management Key.
- When CPAMP and CPA do not run in the same process, confirm that `allow-remote` permits communication.
- Do not expose the CPA management port and key directly to an untrusted network.

### 2. Publish Request Data For Full Mode

```yaml
usage-statistics-enabled: true
redis-usage-queue-retention-seconds: 60
```

Lightweight Panel does not need request collection. Full Mode needs these settings for request history and Usage Analytics. Keep the default 60-second retention unless collection is frequently interrupted.

## Confirm That CPA Is Ready

1. CPA starts without configuration errors.
2. CPAMP connects with the CPA URL and CPA Management Key.
3. Monitoring shows an event after a real request passes through CPA.

If the connection works but request data is missing, see [Monitoring Has No Data](../troubleshooting/request-monitoring.md).

## Where To Make Daily Changes

| Task                                 | Recommended page                            |
| ------------------------------------ | ------------------------------------------- |
| Add providers, models, or API keys   | [AI Providers](../manual/ai-providers.md)   |
| Log in, import, or disable accounts  | [Auth Files](../manual/auth-files.md)       |
| Complete an OAuth login              | [OAuth Login](../manual/oauth.md)           |
| Change CPA or CPAMP runtime settings | [Configuration](../manual/configuration.md) |

::: details Advanced: auth storage and configuration ownership

Keep auth files in persistent storage. Keep filenames or `auth_index` stable so historical requests and account state remain linked. Prefer disabling accounts in the panel instead of deleting files directly.

CPA can use local files or supported external storage. Confirm backup, permissions, and rollback behavior before choosing one.

CPA stores providers, auth files, OAuth, client API keys, logs, plugins, and routing rules. CPAMP Full Mode stores the CPA connection, request history, prices, aliases, inspection, and automation state.

:::
