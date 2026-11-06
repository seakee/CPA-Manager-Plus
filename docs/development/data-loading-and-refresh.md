# Data Loading & Refresh Interaction

This is the default interaction contract for CPAMP v2 and all later pages and
features. v1 applies the same rules when closing existing refresh defects.
Use the current React state/hooks and existing request guards; this contract
does not require a query framework or a global loading-state abstraction.

## Initial load

When the current data scope has no successful snapshot, a skeleton, spinner,
or empty loading state is appropriate. Page-level loading is reserved for page
bootstrap. A successful empty response is a loaded snapshot, too; do not use
array length alone to decide whether a region has ever loaded.

## Revalidation

**Refresh is not initial loading.** With last-known-good data in the current
scope, keep that content mounted and visible during manual refresh, polling,
tab revalidation, and reconciliation after a mutation:

1. Keep the previous snapshot visible.
2. Fetch in the background, using a busy refresh button or small inline state.
3. Replace the snapshot on success, including a successful empty result.
4. On failure, retain the previous snapshot and show nonblocking error feedback.

Do not clear data at request start, replace an entire region with its initial
spinner, or unmount charts, forms, tables, or plugin frames merely to refresh.
Avoid additional layout space for refresh indicators.

## Mutations and operations

Localize busy state to the operation, button, or affected controls. Keep
necessary write locks and confirmation flows. After server success, patch
local state where reliable and revalidate without clearing the visible region;
otherwise retain the current snapshot during reconciliation. Do not default
to a full-page reload. Complex optimistic updates and rollback are not required.
Invalidate reads that started before a successful mutation so an older response
cannot undo its local result. A credential reset/re-authentication may invalidate
old evidence; never restore invalid evidence just to keep content visible.

## Failure

Refresh failures must not turn a loaded page into a blank, empty, or first-load
error screen. Keep stale data with inline feedback or the existing notification
mechanism. A bootstrap failure can use the initial error/retry state. Security
or capability rejection (for example revoked plugin access) can remove content
that is no longer safe or valid to display.

Distinguish an unknown bootstrap value from a known zero or empty result when
reporting health. If a retained form needs a verified read baseline to save,
make it read-only after a failed reload and restore editing after a successful
retry. Do not allow edits that cannot be saved. If a write succeeds but its
reconciliation fails, report both outcomes and prevent further writes that
depend on the stale baseline until recovery.

## View state

Data refresh must preserve the current tab, search, filters, grouping,
expanded/collapsed state, selection, scroll position, and drawer navigation.
Reconcile selections only when their underlying items actually disappear.
Log viewers follow new content only when already following the bottom; a user
reading earlier lines must not be moved to the end by refresh.

## Scope changes

Account, provider, credential revision, server, connection, authorization, and
request parameters can define a new scope. Identify the full scope before
retaining data. Clear or hide the old snapshot before displaying a new scope;
initial loading is allowed here. Never display one account's quota, another
provider's models, or the previous connection's data under the new identity.
Ordinary refresh must not change the scope key or remount the page.
Bind global destructive confirmations to the scope that opened them. Leaving
that scope must invalidate their callbacks, including when the user returns
to the same connection later.

## Concurrency

Reuse AbortController, request IDs, existing generations, and scope guards.
Only the current request may publish data, errors, or completion state. Cleanup
must invalidate outstanding requests. Check scope after awaited work; checking
only before dispatch is insufficient. Test rapid refresh, out-of-order replies,
account/tab/connection changes, and reads overlapping mutations. A stale
request's finally block must not unlock a newer operation.
Capability or authorization loss must invalidate captured operations with a
monotonic generation. Restoring access permits new actions; it must not resume
an older multi-stage operation or revive an old confirmation.

## Verification

Behavior tests must cover initial loading; visible stale content while a
refresh is pending; replacement on success; stale content plus feedback on
failure; successful empty snapshots; scope invalidation; and late replies.
Each repaired entrance needs coverage. Assert visible content and operation
behavior, not just CSS classes. Verify desktop/mobile layout and relevant
theme/locale variants for visible changes. Quota and model caches must retain
their existing credential and connection isolation rules.
