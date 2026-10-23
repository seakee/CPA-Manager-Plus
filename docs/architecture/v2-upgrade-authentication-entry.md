# Existing Manager authentication at the v2 upgrade entry

Delivery slice: existing-source discovery → server-verified remembered credential
or the original login form → authenticated upgrade starting page. Authentication
retains the old administrator key and does not authorize or execute migration.

Product base: `aa23872ac2143f5685b3eb1cf8d6e294974d8a32`.
Coordination/task input: `seakee/cpamp-plan` at
`f23452ff91d18afe047e4d38aa25e0352308e57b`. No shared Plan state is updated.

## Behavior and boundary

- Public discovery still exposes only the existing non-sensitive classification.
  `unsupported_upgrade` / `admin_session` opens the existing login UI through a
  dedicated adapter. Recovery, unknown sources and Fresh retain their own gates.
- Cached credentials must explicitly identify the current origin's root Manager
  endpoint and a Manager runtime or generated-key provenance. Both historical
  `cmp_admin_` and current `cpamp_` prefixes are recognized for old untagged
  formats. Ambiguous bases/keys, other Managers, CPA Panel credentials and local
  logged-in flags cannot silently establish authentication. Manual login accepts
  existing keys without applying Fresh key-strength rules.
- Every attempt verifies the persisted credential with the existing bounded,
  fenced, read-only source inspector. No normal Console login/config loading,
  writable store open, initialization, collector or CPA client is invoked.
- The in-memory observation is revalidated after 30 seconds, on foreground return
  and explicitly by the user, with a 60-second maximum age. Source/credential
  changes and process restart invalidate the old observation. Late responses and
  responses after unmount cannot reactivate it. Failure returns to login.
- The starting page states that migration is unavailable and retains the old key.
  It has no backup, migration, CPA strategy writer, route switch or completion
  action. No `UpgradeAuthorized` or product-completion receipt is manufactured.
- The exact HTTP leaf, error envelope, origin rules and retirement contract are
  recorded in [ADR 0004](adr/0004-v2-web-public-control-surface.md). TLS-terminating
  deployments use the existing trusted `CPAMP_PUBLIC_ORIGIN`; forwarded headers
  alone are not trusted. Source file metadata is a change witness, not a content
  hash, backup proof or permission for any later mutation.

## Verification — 2026-10-23

| Evidence | Result and limits |
| --- | --- |
| Web focused tests | PASS: 20 files, 343 tests under `src/fullweb src/features/login` |
| Full frontend CI checks | PASS locally: `npm run test` (261 web files / 4210 tests; 25 repository files / 744 tests), root type-check/lint, `VERSION=ci` bundle build, demo isolation and packaged read-only HTTP transport |
| Web type-check / affected ESLint | PASS |
| `build:bundle` | PASS: Full Web graph remains bounded; Panel is a single offline HTML file |
| Go focused packages | PASS: legacy preflight, discovery, admin auth, Fresh bootstrap, Upgrade auth, Full Web static handler, HTTP API and Manager startup |
| Race checks | PASS: new authentication, explicit public-origin and startup/source-binding cases |
| Packaged startup transport | PASS: real embedded UI/assets over HTTP |
| Upgrade browser | PASS: 19 checks against the real Manager executable and a real SQLite fixture built from the pinned v1.14.4 schema; no CPA server or mock involved |
| Fresh/Completed browser | PASS: 6 checks using the existing real Manager/store browser harness; CPA safe reads and Ingress are mocks |
| Production deployment / real CPA / real migration | NOT RUN |
| Full Beta / release / joint A+B+C integration | NOT RUN |

The repository dependency-isolation test now recognizes Upgrade's exact original
login and credential-persistence compatibility edges. Both new Upgrade modules
remain in the scanned core graph. An additional assertion limits compatibility
imports and permits only `STORAGE_KEY_AUTH` from shared legacy constants; direct
legacy transport dependencies and CPA endpoint references remain prohibited.

Upgrade browser checks include missing/invalid cache, manual weak old key,
remember/reload/reverification, blocked browser storage, foreign Manager and CPA
Panel rejection, forged login flags, expired observations, Manager restart,
independent fixture source change, English/Chinese, desktop light and 390px dark
mobile. There were no page exceptions, unexpected normal configuration/CPA
requests or horizontal overflow. Before the deliberate source-change test, all
SQLite/config/data-key bytes and directory entries exactly matched the initial
fixture. Go tests additionally cover credential renewal with restored mtime,
credential rotation/corruption, unreadable/missing sources and endpoint retirement.

Fresh regression completes the wizard before ordinary login, enters the original
Console, reloads a completed installation without reinitializing it and confirms
the fixed Upgrade leaf remains retired. This is fixture evidence, not real CPA
or deployment qualification. Ordinary Panel credential compatibility is covered
by HTTP/auth tests; a live CPA Panel browser session was not run.

Task-local evidence is under `bin/tmp/upgrade-auth-entry/` (ignored):
`browser-evidence.json`, `fresh-browser-evidence.json`, test/build logs and
`login-desktop.png`, `upgrade-desktop.png`, `login-mobile-dark.png`,
`upgrade-mobile-dark.png`, `fresh-complete.png`, `completed-login.png`.

### Prototype alignment follow-up

The authenticated entry now uses the accepted installation/upgrade prototype's
header, responsive card geometry, title badges and footer. Language and theme
controls use the original persisted preferences. The original login remains the
authentication gate, with an expandable host-side key recovery explanation;
there is no online reset. Successful entry focuses its heading, and rejected
authentication returns focus to the key field with an associated error.

Renewal keeps the shell mounted and preserves focus. While verification is
pending, the old authenticated badge and observation are withheld and the check
action is disabled. A failed or expired verification returns to login. This does
not add migration choices, unsupported source details or executable upgrade
steps: the current server still reports migration unavailable.

Follow-up validation: 20 focused Web files / 346 tests, 7 transport-isolation
tests, type-check, affected ESLint and bundle build passed. Real Chrome against
a rebuilt Manager and isolated legacy SQLite covered desktop light, 390px dark,
320px layout, English/Chinese, theme changes, recovery help, error/entry focus
and three consecutive renewals per viewport. Responses were real; only their
delivery was held to inspect pending UI. A separately injected network abort
confirmed fail-closed login fallback. Source SQLite/config/data-key bytes were
unchanged. Evidence is in `bin/tmp/upgrade-auth-entry/prototype-fix-20261023/`.
The original production/CPA/migration/joint-integration NOT RUN limits remain;
this UI follow-up does not requalify those workflows.

## Coordinator handoff

Completed: A's authentication entry and honest unsupported starting page.
Remaining: actual backup/migration, supported-source qualification, CPA transition,
entry activation and final upgrade commit. These remain unavailable.

No C-owned Console/Shell files or B/C feature state machines changed. The bounded
Bootstrap static handler now admits the manifest's Upgrade entry and its static
login dependencies, while continuing to reject dynamic Console imports. C must
rebuild shared generated assets from the selected joint commits; generated
`management.html` and Full Web artifacts are not included in this source PR.
