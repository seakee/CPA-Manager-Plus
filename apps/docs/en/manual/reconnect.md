---
title: Self-Service Reconnect
description: Let login owners reconnect broken Claude, Codex, Antigravity, xAI, or Muse logins themselves through a one-time link sent by webhook, without an administrator signing in for them.
---

# Self-Service Reconnect

Some OAuth logins in CPA can only recover through a new sign-in, for example after a refresh token is revoked. Self-service reconnect messages the person who owns the login and gives them a one-time link. They sign in themselves and CPAMP hands the result to CPA. No administrator has to sign in for them.

This feature requires Full Mode (Manager Server). It is off by default.

## How It Works

1. Every check interval, Manager Server reads the CPA auth file list and looks for enabled Claude, Codex, Antigravity, xAI, or Muse logins whose status message reports `unauthorized`, `invalid_grant`, or `invalid grant`.
2. A login must stay broken for 10 minutes before anyone is messaged. Brief failures that recover on their own are ignored.
3. The owner is matched by the login email. CPAMP posts one message to your webhook with a one-time link to `<public URL>/management.html#/reconnect/<token>`.
4. The owner opens the link without signing in to CPAMP:
   - **Claude, Codex, Antigravity**: they click Connect, sign in on the provider's site, then paste the address of the `localhost` page they land on. That page does not load; this is expected.
   - **xAI, Muse**: they approve the request on the provider's site. The page detects the approval by itself.
5. On success, the previous broken credential is dropped. If they signed in with a different account than the one requested, their own link stays open and they are asked to retry with the right account. The other account's login is still useful, so it is kept:
   - If that account was waiting to be reconnected, its request closes as **Reconnected**, its old broken credential is dropped, and its owner gets the all-clear if they had already been messaged.
   - If that account had no login yet, it joins the pool, any open invitation for it closes, and its owner gets a welcome message.
   - The link owner's error message ends with a note saying which of these happened.
6. If the login recovers by itself before the owner acts, CPAMP sends an all-clear message so they know nothing is required.

## Settings

Open **Configuration → Manager Server** and find **Self-Service Reconnect**. Changes are saved with the page's floating **Save** button, together with the other Manager Server settings.

- **Public panel URL**: where people open this panel. Required before enabling. The first time, it is filled in with the address you opened the panel at.
- **Notification webhook URL**: must use `https`. It is stored encrypted and never returned by the API. Leave it blank to keep the saved value.
- **Sender name**: shown in messages.
- **Check interval**: 1–60 minutes.
- **Link lifetime**: 1–72 hours.
- **Reminders**: owners who have not reconnected get a reminder every 1–6 hours, only between the chosen hours in the chosen time zone. Each reminder carries a fresh link and invalidates the previous one.

## Webhook Payload

Each message is one `POST` with a JSON body:

```json
{
  "sendTo": "owner@example.com",
  "body": "<p>HTML message</p>",
  "type": "reconnect",
  "provider": "claude",
  "email": "owner@example.com",
  "link": "https://cpamp.example.com/management.html#/reconnect/…",
  "expires_at": "2026-01-01T12:00:00Z",
  "reason": "unauthorized",
  "followup": false
}
```

`sendTo` and `body` are enough for a Microsoft Teams Power Automate flow or a Slack workflow. The other fields let you route or format messages yourself. `type` is one of `reconnect`, `reconnect_followup` (reminder), `reconnect_resolved` (all-clear), `reconnect_test`, `reconnect_invite`, or `reconnect_welcome` (a login added from someone else's link).

## Sending A Link Manually

Under **Send a link**, pick a login type and an email:

- **Working login**: the person receives a test link, which closes itself if unused.
- **No login of this type**: the person receives an invitation to connect one.
- **Broken login**: the person receives a reconnect request, or nothing if they were already notified.

## Requests Table

The table lists requests from the last 30 days with their status, 20 per page by default:

- **Waiting**: the owner has not reconnected yet.
- **Reconnected**: the owner completed the link.
- **Recovered**: the login recovered without the link.
- **Not used**: an admin-sent link expired unused.

## Security Notes

- Links are random, single-use, and stored only as a hash.
- Public link endpoints are rate limited per IP and never expose internal errors.
- The page can only reconnect the login type and email it was issued for.
