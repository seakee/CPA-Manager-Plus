# OAuth Login

OAuth Login adds or reauthorizes accounts. It answers "how do I get a usable credential saved?" Account enablement, batch maintenance, and status decisions belong in [Accounts](./accounts.md).

## Available Entries

The page shows login methods based on CPA and plugin capabilities. Common entries include:

- Codex OAuth
- Anthropic / Claude OAuth
- Antigravity OAuth
- Kimi OAuth
- xAI OAuth
- Qoder OAuth (requires a CLIProxyAPIPlus build that supports the endpoint)
- iFlow OAuth
- Plugin-provided OAuth
- Vertex credential import

Some entries appear only when the runtime supports them. If an entry is missing, check CPA version, plugin status, and configuration.

## Standard Flow

1. Click the provider login button.
2. Complete login and authorization in the provider page.
3. Wait for the panel to poll auth state.
4. After success, open Accounts and confirm the credential was saved.
5. Send one low-cost request and confirm it in [Monitoring](./monitoring.md).

OAuth success does not guarantee request success. After saving, still check provider configuration, auth-file binding, model rules, and quota state.

## Qoder Device-Code Login

Qoder uses a device-code flow and does not require a pasted browser callback URL:

1. Click **Start Qoder Login**.
2. Open or copy the authorization link shown by the panel.
3. Complete authorization on the Qoder page and keep the panel open while it polls the status.
4. After success, confirm the newly created `qoder` auth file in Accounts.

This entry requires the connected proxy to implement `GET /v0/management/qoder-auth-url` and the auth-status polling endpoint. If the panel reports an unavailable endpoint (HTTP 404), it indicates a proxy-compatibility or start-flow error rather than an account authentication failure. Upgrade to a Qoder OAuth-capable CLIProxyAPIPlus/compatible build, or import the auth file manually.

## Remote Browser Callback

If CPAMP runs on a remote server, the provider may redirect to `http://localhost:...`. In that case:

1. Copy the full callback URL from the browser address bar.
2. Paste it into the callback URL field.
3. Submit it and wait for auth state to update.
4. If the page reports an upgrade or API issue, check CPA version and management API availability.

Paste the full callback URL. Do not manually extract `code` or `state`; that can break state matching.

## Vertex Credential Import

Vertex usually requires a service-account file and a location. After import, check the returned project, email, location, and file name.

If import succeeds but requests fail, check both [AI Providers](./ai-providers.md) for Vertex routing and Accounts for project ID.

## Reauthorize

Reauthorize when:

- Accounts says the credential needs login again.
- Monitoring shows `token_revoked`, `invalidated oauth token`, or a similar summary.
- Codex Inspection recommends reauth.
- The upstream account password, organization, or authorization scope changed.

After reauth, do not immediately delete old files. Confirm the new file works first, then clean up accounts that are clearly invalid.

## Security Notes

Authorization links, callback URLs, and auth files can contain sensitive data. Do not paste full values into public issues or chats. For troubleshooting, describe the stage and share sanitized errors.
