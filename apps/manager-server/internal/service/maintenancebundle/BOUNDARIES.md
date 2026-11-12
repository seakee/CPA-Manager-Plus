# Phase4A-04-3 — Migration Boundary Qualification

Status: **04-3 not independently accepted**; revised implementation awaits
independent acceptance. Phase4A-04 remains
**pending / not accepted**. BAK-05/06/07/08 and AUT-08 remain **blocked / pending**, with no
Phase4A-07 fixture PASS claimed by this slice. Baseline: independently accepted
04-1 and 04-2 / PR #958, merged at `1a9c868ce41718f9f65e71d821e165cfdc21fb00`.

This tracked contract/evidence index supplies reviewable dependency updates.
The invocation checkout's ignored `docs/plan/` and sibling evidence-preparation
worktrees are deliberately untouched; their owners can consume this index after
independent acceptance without treating submission as acceptance.

## Primitive boundary and evidence

| Boundary | What 04-3 verifies | Qualification limit / remaining owner |
| --- | --- | --- |
| Non-cooperative writer | Real external SQL/file writers ignore the held Manager fence; mutations during copying, after matching and immediately before publication are rejected; stale source revalidation fails after the exact child has exited/reaped | Content observations do not prove writer absence. Arbitrary write/revert between observations, or writes after final observation, require actual isolation. External isolation procedure/platform qualification remains **Phase4A**; product stop/drain/fence orchestration belongs to **Phase4-04 backend** |
| Complete sidecar set | DB, WAL, SHM, rollback journal, archives, config/key mutations plus observed optional-file additions/deletions invalidate the baseline; no valid output is published | No snapshot claim for an unisolated mutable file set; manifest is integrity metadata, never authorization |
| Cancellation | Before rename: abort, remove exact recorded staging, preserve source and allow retry. After rename: finish committed publication, no destructive rollback | Storage/permission failure during cleanup can require explicit recovery; product operation cancellation/journals are backend-owned |
| Process crash / restart | Real subprocess Kill + Wait before/after rename; unpublished roots fail Verify; recovery distinguishes absent-target abort from matching complete target commit; retry uses same target; sync failures after rename are explicitly uncertain | Fault injection verifies process/flush ordering. True physical power-loss/storage-stack qualification remains **Phase4A**, including Windows directory-metadata durability limits |
| Rollback journal | Real SQLite interrupted write with spilled uncommitted pages; restricted format/checksum gate, recovery only on copy, all committed rows restored, integrity/foreign-key and matching gates, resealed manifest with pre/post revisions, source unchanged | Unsupported/partial journal formats and super-journals fail closed. Historical exact-source fixtures remain **Phase4A-07**, not synthetic test PASS |
| Restore safety | New absent root only; full manifest/matching validation; no target replacement, source-root overlap or active-root adoption; active source reader/writer continue unchanged; failure/restart tests | This is an offline restore primitive. Live quiesce, reader/writer drain, owned handle lifecycle, target-generation fencing, activation, failure rollback and service restart belong to **Phase4-04 backend** |
| Linux runtime | Bundle/CLI runtime plus race JSON and environment are uploaded by the Manager Server job | Only the actual successful run, tested SHA and artifact count as evidence; no inference from workflow text |
| Windows runtime | Bundle/CLI runtime; real WAL/journal; Windows no-delete-sharing handles block rename/delete/replacement until released; existing destination never replaced; URI paths include spaces, percent, hash and Unicode | Requires successful Windows runtime job. MoveFileEx WRITE_THROUGH is not universal power-loss proof; no portable Windows directory fsync is claimed |
| Source mutation after reap | Mutation subprocesses are waited/reaped before stale-source revalidation; matching bytes and revision are required again before publication | Does not qualify the exact v1.14.4 hop writer, trusted receipt consumer or Manager authority bootstrap. Those remain **03B-2 / Lane B/E** |

## Ownership contract

1. The caller owns canonical, private source, backup and restore placement. All
   external source writers must actually be stopped/isolated, including archive,
   config and key writers; a flag, Manager lock or matching hash is not proof.
2. The source Manager fence remains held through final source revalidation and
   publication. It excludes cooperating Manager writers only; SQLite's own file
   locks cannot fence arbitrary file writes or independent archive/config/key
   assets. This slice intentionally does not acquire writable source SQLite
   connections merely to manufacture a lock claim or trigger source recovery.
3. The per-destination publication fence protects cooperating publishers and
   restart recovery. The owner excludes other publishers and case/path aliases
   and does not replace ancestors. Staging belongs exclusively to that operation.
4. Restore targets are new roots. Opening/adopting a restored root is a separate
   owner action after publication reconciliation; active roots are never swapped.
   Product activation must revalidate source/target generation and hold its
   ownership/fencing contract across consumption, rather than trust an earlier
   validation result or a PID alone.
5. Recovery never deletes/repairs original source data, blindly cleans siblings,
   adopts unknown output, grants UpgradeAuthorized, or starts old/runtime servers.

Journal restart reconciliation recognizes a complete `state.next` only after
strict record decoding, a legal transition from `state.json` (or initial intent),
and exact artifact/target/staging checks. Initial, retry-preparing, manifest,
committed and aborted writes have subprocess crash tests after temporary-file
fsync and after replacement but before directory sync. Partial, foreign or
inconsistent intent remains fail closed and preserved for inspection. A target
cannot be recovered as committed while its recorded staging still exists, even
when both copies have the same complete manifest.

## Phase4A-07 Lane C dependency update

The current cpamp-plan/main Lane C gate remains unchanged: execution waits for
independent acceptance of non-cooperative/external writer isolation,
crash/power-loss publication boundaries, rollback-journal recovery and live
restore. Acceptance of these 04-3 primitives alone does not unlock any Lane C
fixture. Every real case also needs its own exact-source generation provenance,
isolated fixture, bundle digests, applicable SEM assertions and durable evidence.

| Case | Primitive supplied for future qualification | Case status |
| --- | --- | --- |
| BAK-05 | Controlled concurrent-writer/stale rejection; observations do not fence arbitrary writers | blocked / pending; full Lane C gate applies |
| BAK-06 | Complete sidecar inventory and final revalidation | blocked / pending; full Lane C gate applies |
| BAK-07 | Cancellation commit point, exact staging cleanup, restart and same-target retry | blocked / pending; full Lane C gate applies |
| BAK-08 | Process-crash/restart and flush-failure branches; Kill tests do not prove physical power-loss durability | blocked / pending; full Lane C gate applies |
| AUT-08 | Post-reap content revalidation for a controlled child and immutable inventory | blocked / pending; full Lane C gate plus Lane B writer and Lane E trusted consumer apply |

Live restore remains a Lane C prerequisite under the current Plan. Its product
ownership orchestration belongs to Phase4-04 backend; this implementation does
not waive that dependency. Splitting offline/new-root execution from live restore
would require a separately reviewed and accepted cpamp-plan change first.

## Verification and evidence location

From `apps/manager-server`:

```sh
go test ./internal/service/maintenancebundle ./internal/command/managerdatabundle -count=1 -timeout 15m
go test -race ./internal/service/maintenancebundle ./internal/command/managerdatabundle ./internal/processlock -count=1 -timeout 20m
go test ./... -count=1 -timeout 20m
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o <task-temp>/manager-linux ./cmd/cpa-manager-plus
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o <task-temp>/manager-windows.exe ./cmd/cpa-manager-plus
git diff --check
```

CI `PR Check / Manager Server bundle race (k/4)` shards the race gate across
four runners and uploads `manager-bundle-linux-shard<k>-<run>-<attempt>` per
shard: `environment.json` (which includes `shard`), `boundary.jsonl` (that
shard's `maintenancebundle` test events) and, on shard 0,
`boundary-other.jsonl` (the `managerdatabundle` and `processlock` events, run in
their own invocation because a shard's `-run` filter would otherwise silence
them). Each shard's `verify` step proves the executed tests are exactly that
shard's assignment, so the four `boundary.jsonl` files together cover the whole
race-enabled catalog. `Manager Server SQLite (Windows)` uploads
`manager-preflight-windows-<run>-<attempt>`:
`environment.json` and `bundle-boundary.jsonl`, alongside pre-existing evidence.
Environment records bind requested source head, actual tested commit, OS/arch,
Go and SQLite driver. Review the runtime test events and failures/skips; artifact
existence alone is insufficient. CI run URLs and observed results belong in the
PR validation record. These artifacts are primitive regression evidence, not
Phase4A-07 exact historical fixture evidence.

No Upgrade API, wizard, #870 change, production ExecuteHop/ValidateHop enablement,
upgrade authority, release, merge or full migration orchestration is included.
