# Monitoring Has No Data

First confirm that you opened CPAMP Full Mode:

```text
http://<cpamp-host>:18317/management.html
```

The Lightweight Panel on CPA `:8317/management.html` does not store request history, so Monitoring is unavailable there by design.

## Check In This Order

1. **Send a real request**: confirm that Codex, Claude Code, or another client actually requested a model through CPA.
2. **Check the CPA connection**: Dashboard should show CPA as connected with no authentication error.
3. **Enable Monitoring**: in Manager Server configuration, confirm that request monitoring is enabled.
4. **Wait for a new request**: Monitoring only shows events collected after it is enabled; expired events cannot be recovered.
5. **Use one Full Mode instance**: do not let multiple Manager Servers read the same CPA request queue.

## Troubleshoot By Symptom

| Symptom                                 | Check first                                                    |
| --------------------------------------- | -------------------------------------------------------------- |
| Dashboard says CPA is disconnected      | CPA URL, CPA Management Key, network, and remote management    |
| CPA is connected but no requests appear | Client base URL, CPA usage publishing, Monitoring switch       |
| Data appears intermittently             | Manager Server restarts, queue retention, duplicate collectors |
| Data stopped after adding a proxy       | Let Manager Server connect directly to CPA `:8317` first       |
| No new data immediately after upgrade   | Send a new request and check the current connection            |

## Most Common Fix

1. Confirm that client base URLs point to CPA, not CPAMP.
2. Enable `usage-statistics-enabled` in CPA configuration.
3. Enable Monitoring in CPAMP and keep automatic collection mode.
4. Let Manager Server reach the CPA API port directly.
5. After restarting, send a new request and refresh Monitoring.

If data is still missing, save CPA logs, Manager Server logs, and System version information for the same time window. Do not share real API keys, Management Keys, or auth files.

::: details Advanced diagnostics: status fields and collection protocols

Open the authenticated Manager Server `/status` endpoint and check:

| Field            | Meaning                                            |
| ---------------- | -------------------------------------------------- |
| `lastConsumedAt` | Last time an event was read from CPA               |
| `lastInsertedAt` | Last time an event was saved to local history      |
| `lastError`      | Most recent network, authentication, or data error |
| `totalInserted`  | Total request events saved                         |

If `lastConsumedAt` is empty, check CPA usage publishing, address, and collection networking. If events are consumed but not inserted, check `lastError` and data directory permissions.

Automatic collection selects an available path. RESP mode must connect directly to the CPA API port; a normal HTTP reverse proxy cannot proxy RESP. The HTTP queue can use an HTTP proxy.

CPA queue retention is intentionally short. Events cannot be recovered after Manager Server stays offline longer than retention. The poll interval must not exceed queue retention.

:::
