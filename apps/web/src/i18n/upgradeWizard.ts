import { fullWebLanguage } from './fullweb';

// Every string of the Full Web Upgrade wizard, in the four supported locales.
// The keys are grouped the way the wizard reads them: the four steps, the
// preflight report, the confirmation, the recorded operation and its result.
// The text never promises a write: this surface only reads what the operation
// recorded, so every sentence about a copy, a backup or a rollback describes
// what the operation does on the host, not what this page can start.
const en = {
  flowLabel: 'Upgrade steps',
  stepPreflight: 'Preflight',
  stepConfirm: 'Confirm',
  stepProgress: 'Progress',
  stepResult: 'Result',
  back: 'Back',
  continue: 'Continue',
  bootTitle: 'Reading the recorded operation',
  bootHelp:
    'The wizard reads what this installation already recorded before it offers a preflight. An operation that is already recorded resumes where it stands, and that needs no mounted source.',
  bootReading: 'Reading the recorded operation…',
  bootFailedTitle: 'The recorded operation could not be read',
  bootRetry: 'Read the recorded operation again',

  preflightTitle: 'Read-only preflight',
  preflightHelp:
    'This inspection reads the stopped 1.x source. It copies nothing, writes nothing and grants no permission for a later step.',
  preflightReading: 'Inspecting the source…',
  preflightSourceKind: 'Source kind',
  preflightSourceVersion: 'Source version',
  preflightFingerprint: 'Source fingerprint (abbreviated)',
  preflightChecks: 'Inspection checks',
  preflightUnknown: 'Not reported',
  preflightRetry: 'Run the preflight again',
  preflightPendingTitle: 'Source version declared at copy-in',
  preflightPendingHelp:
    'The read-only preflight cannot declare the 1.x release, and the source does not record it. The CLI copy-in does: pass',
  preflightPendingTail: 'The check stays pending here; it is not a failure.',
  preflightBlockedTitle: 'Preflight blocked the upgrade',
  preflightBlockedHelp:
    'Resolve each named condition on the host, then run the preflight again. Nothing has been copied.',
  preflightReadyTitle: 'The source passed preflight',
  preflightReadyHelp:
    'Every check passed. Nothing has been copied yet; the next step describes what the operation will do.',
  preflightReadyPendingTitle: 'The source passed preflight (pending copy-in confirmation)',
  preflightReadyPendingHelp:
    'No blocking issues were found; pending items must be confirmed at CLI copy-in (--source-version). Nothing has been copied yet; the next step describes what the operation will do.',
  preflightGapsTitle: 'Retained history gaps',
  preflightGapsHelp:
    'Days whose deleted raw events no longer match the retained projection. The copy-in reports them; it never repairs them silently.',
  preflightGapsDays: 'Days with gaps',

  checkSourceOwnership: 'Source has no running writer',
  checkSourceVersion: 'Source version supported',
  checkFingerprint: 'Source fingerprint recorded',
  checkDisk: 'Free space for the copy',
  checkCpaClassification: 'CPA classification',
  checkParity: 'Retained history parity',

  checkPass: 'Passed',
  checkBlocked: 'Blocked',
  checkSkipped: 'Skipped',
  checkPending: 'Pending',
  checkGap: 'Gap',
  checkUnknown: 'Unreported',

  codeUpgradeSourceMissing:
    'The mounted 1.x directory has no usage.sqlite. Check that the whole data directory is mounted read-only, then try again.',
  codeUpgradeSourceBusy:
    'The 1.x source is still locked by a running process. Stop it and try again.',
  codeUpgradeSourceLockUnavailable:
    'The source ownership marker cannot be inspected read-only. Check the mounted 1.x volume; nothing was written.',
  codeUpgradeSourceSymlinked:
    'The 1.x database is reached through a link, not as the file itself. Mount the data directory itself, without linked files, then try again.',
  codeUpgradeSourceUnreadable:
    'The 1.x database cannot be read safely. Check the volume and its permissions on the host.',
  codeUpgradeSourceVersionUnsupported: 'This 1.x version is not supported as an upgrade source.',
  codeUpgradeDiskInsufficient: 'There is not enough free space for the copy.',
  codeUpgradeCredentialTimeout:
    'The source did not answer in time to verify the administrator key. Try again when the host is idle.',
  codeUpgradeMountTimeout:
    'The mounted 1.x volume did not answer in time, so it was never inspected. Try again when the host responds; nothing was read from it or written to it.',
  codeUpgradeLegacyRootMissing:
    'No 1.x data directory is mounted. Mount it read-only and try again.',
  codeUpgradeLegacyRootUnusable: 'The mounted 1.x data directory cannot be used.',
  codeUpgradePreflightTimeout: 'The preflight did not finish in time. Run it again when the host is idle.',
  codeUpgradePreflightFailed: 'The preflight failed before it could report the source.',
  codeUpgradeOperationUnreadable:
    'The operation record cannot be read. Check the Manager database on the host.',
  codeUnknown: 'This condition has no description in this version. Report the exact code.',
  codeUpgradeVerifyNotApplicable:
    'The recorded operation stands where the verification cannot run from. Continue the step it names on the host, then run the verification again.',
  codeUpgradeVerifyV2Running:
    'Another process owns the v2 data root, so nothing was verified. Stop it and run the verification again.',
  codeUpgradeVerifySourceUnavailable:
    'The 1.x source cannot be observed, so the volume the copy-in read cannot be proven unchanged. Mount the directory read-only and run the verification again.',
  codeUpgradeVerifySourceMismatch:
    'The mounted 1.x source is not the volume the recorded copy-in read, or it was written while it was being verified. Nothing was recorded.',
  codeUpgradeVerifyReconciliationInvalid:
    'The recorded reconciliation is not one this build can read, so nothing proves the copied store holds the source usage. Run the reconciliation again.',
  codeUpgradeVerifyReconciliationUnacknowledged:
    'The recorded reconciliation leaves days undecided or unacknowledged, so it was never accepted. Acknowledge them and reconcile again.',
  codeUpgradeVerifyDomainsIncomplete:
    'The recorded operation does not hold every domain this target needs, so the copy is not proven for them.',
  codeUpgradeVerifyBindingMissing:
    'The recorded operation reached the CPA connection step without the target binding, so the CPA it connected cannot be proven.',
  codeUpgradeVerifyBindingDrifted:
    'The CPA this Manager connects to is not the target the connection step recorded. Reconnect the CPA, then verify again.',
  codeUpgradeVerifyConnectionUnproven:
    'The connection the CPA import handed over did not survive: the Manager no longer names that destination with the secret the import used.',
  codeUpgradeVerifyConflict:
    'The recorded operation changed while it was being verified, so nothing was written. Run the verification again.',
  codeUpgradeVerifyFailed: 'The verification could not finish and recorded nothing.',
  codeUpgradeRequestInvalid: 'The verification takes no input; the request was refused.',

  confirmTitle: 'What the upgrade will do',
  confirmHelp:
    'Review these facts before the operation starts. This page only reads state; the copy-in itself starts from the Manager host.',
  confirmSourceKind: 'Source kind',
  confirmSourceVersion: 'Source version',
  confirmReadOnlyTitle: 'The 1.x volume stays read-only',
  confirmReadOnlyHelp:
    'The 1.x data directory is mounted read-only and is only copied out of. Its fingerprint is recorded before and after the copy.',
  confirmBackupTitle: 'A verified backup is taken first',
  confirmBackupHelp:
    'The copy-in is preceded by a backup bundle that is verified before anything is written to the v2 volume.',
  confirmManualTitle: 'Items that may need manual work',
  confirmManualHelp:
    'Anything the operation cannot carry over is listed for you on the result step. Nothing is dropped silently.',
  confirmManualKeys: 'CPA client keys that must be re-created on the v8 target',
  confirmManualCredentials: 'CPA provider and credential sections that must be re-entered',
  confirmManualGateway: 'Gateway configuration the target cannot import automatically',
  confirmManualHistory: 'Usage days whose retained history cannot be reconstructed',
  confirmAck:
    'I understand the 1.x source stays read-only, a verified backup comes first, and the listed items may need manual work.',
  confirmContinue: 'Continue to progress',

  progressTitle: 'Recorded copy-in progress',
  progressHelp: 'This page reads the recorded operation state. It never advances the operation.',
  progressStage: 'Current stage',
  progressDomains: 'Completed domains',
  progressDomainsNone: 'No domain is recorded as done yet.',
  progressManual: 'Recorded manual items',
  progressManualNone: 'No manual item is recorded.',
  progressReading: 'Reading the operation…',
  progressUnrecorded: 'No upgrade operation is recorded yet. Start it from the Manager host.',
  progressWaiting: 'Waiting for the next recorded change…',
  resumeNotice:
    'This installation already recorded an upgrade operation, so this page resumes at its recorded stage. The read-only preflight is required only when no operation is recorded.',
  progressStageNote:
    'The recorded stage is the last position the operation wrote. It is not proof that the work behind it finished.',
  progressStalledTitle: 'The operation status could not be read',
  progressStalledHelp:
    'Three reads in a row failed. The last observation is kept below and may be out of date.',
  progressRetry: 'Read the operation again',

  resultStage: 'Recorded stage',
  resultNext: 'Next step',
  resultSource: 'Recorded source',
  resultCompleteTitle: 'The upgrade is complete',
  resultCompleteHelp:
    'The recorded operation says complete. The v2 Manager now serves its normal surfaces.',
  resultIncompleteTitle: 'The upgrade is not complete',
  resultIncompleteHelp:
    'The recorded operation has not reached complete. Nothing on this page claims that the upgrade finished.',
  resultRolledBackTitle: 'The operation was rolled back to 1.x',
  resultRollbackReason: 'Recorded reason',
  resultRollbackReasonUnknown:
    'The operation recorded no reason. Check the rollback report on the Manager host.',
  resultRollbackRestart:
    'Restart the 1.x stack against the original volume. The v1 data was never written, so it starts as it was before the upgrade; anything recorded in v2 is not written back.',
  resultFailedTitle: 'The operation stopped at a failure',
  resultFailedHelp:
    'The failure is recorded together with the domains that already completed. Reconciliation resumes from there; nothing already done is replayed.',
  resultUnreadableTitle: 'The recorded stage is not one this page knows',
  resultUnreadableHelp:
    'A newer Manager may have written it. The stage is shown exactly as recorded; this page does not guess what it means.',
  resultReconciliationTitle: 'Reconciliation mismatches',
  resultReconciliationHelp:
    'These days cannot be reported as complete. Each one is listed; none is repaired silently.',
  resultReconciliationNone: 'No mismatch day is recorded.',
  resultReconciliationTotal: '{count} mismatch days are recorded.',
  resultReconciliationBound:
    'The newest {shown} of them are listed; the list is a bounded window, not the whole history.',
  resultManualTitle: 'Manual items',
  resultManualNone: 'No manual item is recorded.',
  resultReread: 'Read the operation again',

  verifyTitle: 'Verify and complete the upgrade',
  verifyHelp:
    'The verification re-reads the recorded operation and proves the copied source, the reconciliation and the CPA this installation uses. It records the one verdict the recorded target supports; this page never declares the upgrade finished on its own.',
  verifySourceRequired:
    'The 1.x data directory must stay mounted and stopped: its fingerprint is the proof that the volume the copy-in read was not written.',
  verifyAction: 'Run the verification',
  verifyRetry: 'Run the verification again',
  verifyRunning: 'Verifying the recorded operation…',
  verifyDoneTitle: 'The verification recorded a verdict',
  verifyComplete:
    'The operation is complete. Restart the Manager: the normal product starts and the Upgrade surface closes.',
  verifyAlready: 'This position was already recorded; nothing was written this time.',
  verifyFailedTitle: 'The verification recorded nothing',
  verifyTargetExternal: 'Target: an External CPA, administered by its owner',
  verifyTargetManaged: 'Target: the CPA this Manager owns',
  verifyReasonLabel: 'Recorded reason',
  cpaImportReadyTitle: 'CPA engine is ready for import',
  cpaImportReadyHelp: 'The pinned CPA engine is active. Run the import on the Manager host:',
  stagePreflight: 'Preflight',
  stageCopyIn: 'Copy-in',
  stageReconcile: 'Reconciliation',
  stageCpaImport: 'CPA import',
  stageExternalConnect: 'CPA connection',
  stageVerify: 'Verification',
  stageComplete: 'Complete',
  stageFailed: 'Failed',
  stageRolledBack: 'Rolled back',

  domainSourceCopy: 'Source copy',
  domainUsageEvents: 'Usage events',
  domainRetainedProjection: 'Retained projection',
  domainPricing: 'Pricing',
  domainCoverage: 'Coverage',
  domainIdentity: 'Identity',
  domainCpaConfig: 'CPA configuration',

  errorAuthentication:
    'This session is not authorized to read the upgrade state. Verify your administrator key again.',
  errorNetwork: 'The upgrade service could not be reached. Check the connection and try again.',
  errorResponse: 'The reply could not be verified and was not shown. Try again.',
  errorUnavailable: 'The upgrade service is not available right now.',

  routingTitle: 'Access prefixes',
  routingHelpManaged:
    'Set independent management and inference prefixes for this first v2 upgrade, or explicitly enable shared mode. Defaults are random 16-character prefixes persisted for this operation.',
  routingHelpExternal:
    'Set the CPAMP management prefix for this external-target upgrade. External CPA inference continues using the configured external address.',
  routingPreservedTitle: 'Committed access prefixes preserved',
  routingPreservedHelp:
    'This installation already committed its v2 access prefixes. Subsequent upgrades preserve these routes unchanged.',
  routingControlLabel: 'Management entry prefix',
  routingControlHelp: 'Path beginning with / for the CPAMP Console and management API.',
  routingInferenceLabel: 'Inference entry prefix',
  routingInferenceHelp:
    'Independent path beginning with / for model protocol endpoints. Must not nest with the management prefix.',
  routingExternalInferenceLabel: 'External CPA inference address',
  routingExternalInferenceHelp: 'External mode only sets the management prefix; inference goes directly to the external CPA.',
  routingRandomize: 'Regenerate random',
  routingSharedToggle: 'Use the same prefix for management and inference (shared mode)',
  routingSharedHelp: 'Off by default. Enable explicitly only when both entries intentionally share one path.',
  routingSharedLabel: 'Shared prefix mode',
  routingIndependentLabel: 'Independent dual prefixes',
  routingPreviewTitle: 'Final URL preview',
  routingPreviewConsole: 'Management Console URL',
  routingPreviewApi: 'Management API base',
  routingPreviewInference: 'Inference entry base',
  routingPreviewExternal: 'External inference URL',
  routingSave: 'Save access prefixes',
  routingSaving: 'Saving access prefixes…',
  routingSavedNotice: 'Access prefixes saved to the upgrade record.',
  resultAccessTitle: 'Access and protocol endpoints',
  resultAccessPendingRestart:
    'Access prefixes are saved to the upgrade record, but routes are pending Manager restart and are not active yet.',
  resultAccessActive: 'Access routes are verified and active.',
  codeInvalidAccessPrefix:
    'Enter a normalized path starting with /, without trailing slash, spaces, percent encoding, or dot segments.',
  codeInferencePrefixRequired: 'An inference entry prefix is required for Managed upgrades.',
  codeExternalInferencePrefixForbidden:
    'External mode does not use a local inference prefix or shared mode.',
  codeSharedPrefixMismatch: 'In shared mode, management and inference prefixes must be identical.',
  codeSharedPrefixConfirmationRequired:
    'Management and inference prefixes are equal. Enable shared mode explicitly or use distinct prefixes.',
  codePrefixOverlapForbidden: 'Management and inference prefixes cannot be nested inside each other.',
  codeReservedPrefixCollision:
    'This prefix collides with a reserved system or protocol route. Choose another path.',
  codeUpgradeAccessPrefixRequired:
    'Save valid access prefixes for this target mode before completing upgrade verification.',
  codeUpgradePrefixesAlreadyCommitted:
    'Committed v2 access prefixes are already locked and cannot be overwritten during upgrade.',
  codeUpgradeRestartRequired:
    'Upgrade changes are saved and pending Manager restart before new routes serve traffic.',
  codeRoutingConflict: 'Routing state conflicted with another operation state. Refresh and retry.',
  codeRoutingVerificationFailed: 'Published routes could not be verified. Check host routing and retry.',
  modeBadgeManaged: 'Managed CPA',
  modeBadgeExternal: 'External CPA',
  externalDirectBadge: 'Direct inference base',
  sourceKindLegacyManager: 'CPAMP 1.x Manager',
  sourceKindUpgradedLegacy: 'Copied 1.x source',
  techDetails: 'Raw diagnostic parameters',
  checkSummaryOwnershipPass: 'Idle & read-only',
  checkSummaryVersionPending: 'Pass --source-version {version} at copy-in',
  checkSummaryVersionPass: 'Supported release',
  checkSummaryFingerprintPass: 'Source ~{size}',
  checkSummaryFingerprintPlain: 'SHA-256 recorded',
  checkSummaryDiskPass: 'Needs {required} · {available} free',
  checkSummaryCpaManaged: 'Managed CPA',
  checkSummaryCpaExternal: 'External CPA',
  checkSummaryParityPass: 'Matched, 0 gaps',
  cliGuideTitle: 'Target mode',
  cliGuideHelp: 'Run the 3 steps below on the host in order.',
  cliStepDone: 'Done',
  cliStepCurrent: 'Current',
  cliStepPending: 'Pending',
  cliStep1Title: '1. Backup & copy 1.x data (stop v2 container first)',
  cliStep2Title: '2. Reconcile usage & pricing',
  cliStep3ManagedTitle: '3. Start v2 container & import CPA v8 config',
  cliStep3ExternalTitle: '3. Start v2 container & bind external CPA v8',
  cliCopy: 'Copy',
  cliCopied: 'Copied',
  progressRefreshNow: 'Check status',
  confirmAutoTitle: 'Automatic data & key migration',
  confirmAutoHelp:
    'Usage history, model pricing, client API keys, and supported CPA v8 settings migrate automatically.',
  confirmEdgeToggle: 'Edge cases that require manual review',
  progressCliTitle: 'Host data migration',
  progressCliHelp:
    'Run the 3 commands below on the host in order; this page unlocks access setup automatically when done.',
  progressVerifyTitle: 'Confirm access URLs & complete upgrade',
  progressVerifySubtitle:
    'Host migration is ready. Review your v2 access endpoints below and run final verification.',
  migrationReadySummary: 'Host migration & reconciliation ready ({count} domains · 0 mismatches · 0 manual items)',
  migrationDetailsToggle: 'Details',
  routingCustomizeToggle: 'Customize route prefixes',
  verifyInlineHint: 'Keep the 1.x data directory mounted read-only during verification.',
  copyAction: 'Copy',
  copiedAction: 'Copied',
  resultCompleteSummary: 'Migrated from {source} · 0 reconciliation mismatches · 0 manual items',
  resultAuditToggle: 'Migration audit details',
  migrationReadyWithManual: 'Host migration & reconciliation ready ({count} domains · 0 mismatches)',
  manualDisclosureSummary: '{count} optional/unsupported settings skipped (configure in Console after upgrade)',
  manualReasonNotInScope: 'Outside automatic import scope; configure in Console if needed',
  manualReasonReconfigure: 'Reconfigure in Console after upgrade',
  manualReasonReauth: 'Re-authenticate provider in Console after upgrade',
  manualReasonRemoved: 'Deprecated in target CPA v8',
  slimEngineStepBadge: 'Step 1 · Required first',
  slimEngineTitle: 'Prepare embedded CPA engine',
  slimEngineHelp: 'This slim image has no bundled CPA binary. Prepare the pinned engine here before running upgrade cpa-import below.',
  cliStepWaitEngine: 'Waiting for engine',
};

const zh: typeof en = {
  flowLabel: '升级步骤',
  stepPreflight: '预检',
  stepConfirm: '确认',
  stepProgress: '进度',
  stepResult: '结果',
  back: '返回',
  continue: '继续',
  bootTitle: '读取已记录的操作',
  bootHelp:
    '向导会先读取本安装已记录的操作，再决定是否提供预检。已记录的操作会从原阶段继续，这不需要挂载来源。',
  bootReading: '正在读取已记录的操作…',
  bootFailedTitle: '无法读取已记录的操作',
  bootRetry: '重新读取已记录的操作',

  preflightTitle: '只读预检',
  preflightHelp: '只读检查已停止的 1.x 数据目录，不修改任何原文件。',
  preflightReading: '正在检查来源…',
  preflightSourceKind: '来源',
  preflightSourceVersion: '版本',
  preflightFingerprint: '来源指纹',
  preflightChecks: '检查项',
  preflightUnknown: '未上报',
  preflightRetry: '重新预检',
  preflightPendingTitle: '来源版本在命令行复制时声明',
  preflightPendingHelp: '只读预检无法声明 1.x 版本，来源本身也不记录该信息。请在命令行复制时传入',
  preflightPendingTail: '此检查在本页保持待定状态，并不是失败。',
  preflightBlockedTitle: '预检阻断了升级',
  preflightBlockedHelp: '请在宿主机上逐项解决下列问题后重新预检。当前尚未复制任何数据。',
  preflightReadyTitle: '预检全部通过',
  preflightReadyHelp: '未发现阻断项，可继续下一步。',
  preflightReadyPendingTitle: '预检通过，可继续升级',
  preflightReadyPendingHelp: '复制阶段会自动代入 --source-version 参数，无需在此手动设置。',
  preflightGapsTitle: '留存历史缺口',
  preflightGapsHelp:
    '这些日期的原始事件已删除，但与留存投影不一致。复制步骤会如实上报，不会静默修补。',
  preflightGapsDays: '存在缺口的日期数',

  checkSourceOwnership: '运行锁检查',
  checkSourceVersion: '来源版本兼容',
  checkFingerprint: '来源数据指纹',
  checkDisk: '可用磁盘空间',
  checkCpaClassification: 'CPA 部署形态',
  checkParity: '历史数据一致性',

  checkPass: '通过',
  checkBlocked: '阻断',
  checkSkipped: '跳过',
  checkPending: '待定',
  checkGap: '缺口',
  checkUnknown: '未上报',

  codeUpgradeSourceMissing:
    '已挂载的 1.x 目录中没有 usage.sqlite。请确认整个数据目录都以只读方式挂载后重试。',
  codeUpgradeSourceBusy: '1.x 来源仍被运行中的进程占用。请先停止它再重试。',
  codeUpgradeSourceLockUnavailable:
    '无法只读检查来源的归属标记。请检查挂载的 1.x 卷；未写入任何内容。',
  codeUpgradeSourceSymlinked:
    '1.x 数据库是通过链接访问的，而不是普通文件。请挂载数据目录本身（不要使用链接文件）后重试。',
  codeUpgradeSourceUnreadable: '无法安全读取 1.x 数据库。请在宿主机上检查卷和权限。',
  codeUpgradeSourceVersionUnsupported: '该 1.x 版本不能作为升级来源。',
  codeUpgradeDiskInsufficient: '复制所需的可用空间不足。',
  codeUpgradeCredentialTimeout: '来源未在限定时间内响应，无法验证管理员密钥。请在宿主机空闲时重试。',
  codeUpgradeMountTimeout:
    '已挂载的 1.x 卷未在限定时间内响应，因此未能检查。请在宿主机可响应时重试；本次未从该卷读取任何内容，也未写入任何内容。',
  codeUpgradeLegacyRootMissing: '未挂载 1.x 数据目录。请只读挂载后重试。',
  codeUpgradeLegacyRootUnusable: '已挂载的 1.x 数据目录不可用。',
  codeUpgradePreflightTimeout: '预检未在限定时间内完成。请在宿主机空闲时重新预检。',
  codeUpgradePreflightFailed: '预检在报告来源之前就失败了。',
  codeUpgradeOperationUnreadable: '无法读取操作记录。请在宿主机上检查 Manager 数据库。',
  codeUnknown: '此版本没有该条件的说明。请上报完整错误码。',
  codeUpgradeVerifyNotApplicable:
    '已记录的操作不处于可执行验证的位置。请在宿主机上完成它指明的步骤，然后重新执行验证。',
  codeUpgradeVerifyV2Running:
    '另一个进程持有 v2 数据根目录，因此没有进行验证。请停止该进程后重新执行验证。',
  codeUpgradeVerifySourceUnavailable:
    '无法观测 1.x 来源，因此无法证明复制时读取的卷未被写入。请以只读方式挂载该目录后重新执行验证。',
  codeUpgradeVerifySourceMismatch:
    '挂载的 1.x 来源不是已记录复制所读取的卷，或在验证过程中被写入。本次没有写入任何内容。',
  codeUpgradeVerifyReconciliationInvalid:
    '已记录的对账结果不是本版本可读取的证据，因此无法证明复制的存储包含来源用量。请重新执行对账。',
  codeUpgradeVerifyReconciliationUnacknowledged:
    '已记录的对账仍存在未定或未确认的日期，因此从未被接受。请先确认这些日期再重新对账。',
  codeUpgradeVerifyDomainsIncomplete:
    '已记录的操作缺少该目标所需的某些数据域，因此这些域未被证明。',
  codeUpgradeVerifyBindingMissing:
    '已记录的操作进入 CPA 连接步骤却没有目标绑定，因此无法证明它连接的 CPA。',
  codeUpgradeVerifyBindingDrifted:
    '本 Manager 当前连接的 CPA 不是连接步骤记录的目标。请重新连接 CPA 后再验证。',
  codeUpgradeVerifyConnectionUnproven:
    'CPA 导入移交的连接未能保持：Manager 不再以导入使用的管理密钥指向该目标。',
  codeUpgradeVerifyConflict: '验证过程中已记录的操作发生变更，因此没有写入任何内容。请重新执行验证。',
  codeUpgradeVerifyFailed: '验证未能完成，也没有记录任何内容。',
  codeUpgradeRequestInvalid: '验证不接受任何输入，该请求已被拒绝。',

  confirmTitle: '升级确认',
  confirmHelp: '开始宿主机迁移前，请确认以下核心规则：',
  confirmSourceKind: '来源',
  confirmSourceVersion: '版本',
  confirmReadOnlyTitle: '原 1.x 数据全程只读',
  confirmReadOnlyHelp: '只读挂载读取，复制前后双重核验指纹，绝不改动原数据卷。',
  confirmBackupTitle: '先校验备份再写入',
  confirmBackupHelp: '导入前先生成完整备份包并校验通过，随后才写入 v2 数据卷。',
  confirmManualTitle: '边缘例外说明',
  confirmManualHelp: '仅当存在以下特殊情况时，才会在结果页列出供人工核对：',
  confirmManualKeys: '格式异常或无法通过校验的个别客户端 API 密钥',
  confirmManualCredentials: '需在目标环境补充授权或重新登录的上游提供商凭证',
  confirmManualGateway: '超出自动导入范围的自定义网关运行参数（如监听端口、TLS、插件）',
  confirmManualHistory: '原 1.x 库中本身存在缺失、无法重建投影的历史日期',
  confirmAck: '我已了解以上升级规则，继续下一步',
  confirmContinue: '继续',

  progressTitle: '升级执行与路由配置',
  progressHelp: '按顺序在宿主机完成数据复制、对账与 CPA 衔接后，即可在本页配置访问入口前缀并完成升级验证。',
  progressStage: '当前阶段',
  progressDomains: '已完成域',
  progressDomainsNone: '尚未记录任何已完成域。',
  progressManual: '已记录的手工项',
  progressManualNone: '尚未记录手工项。',
  progressReading: '正在读取操作状态…',
  progressUnrecorded: '尚未检测到宿主机升级记录，请按下方步骤在 Manager 宿主机依次执行。',
  progressWaiting: '每 10 秒自动检测进度',
  resumeNotice: '已恢复当前升级进度。',
  progressStageNote: '',
  progressStalledTitle: '无法读取操作状态',
  progressStalledHelp: '连续三次读取失败。下方保留最后一次观测结果，可能已过期。',
  progressRetry: '重新读取操作状态',

  resultStage: '记录阶段',
  resultNext: '下一步',
  resultSource: '记录的来源',
  resultCompleteTitle: '升级已完成',
  resultCompleteHelp: '已完成数据与配置迁移，重启 Manager 后即可使用新访问入口。',
  resultIncompleteTitle: '升级尚未完成',
  resultIncompleteHelp: '记录中的操作尚未到达完成。本页面不会声称升级已经结束。',
  resultRolledBackTitle: '操作已回滚到 1.x',
  resultRollbackReason: '记录的原因',
  resultRollbackReasonUnknown: '该操作没有记录原因。请在 Manager 宿主机上查看回滚报告。',
  resultRollbackRestart:
    '请使用原卷重新启动 1.x 栈。v1 数据从未被写入，因此会以升级前的状态启动；v2 中已记录的内容不会回写。',
  resultFailedTitle: '操作在失败处停止',
  resultFailedHelp:
    '失败与已完成的域一起被记录。对账将从那里继续，已完成的部分不会重放。',
  resultUnreadableTitle: '该记录阶段本页面无法识别',
  resultUnreadableHelp: '可能是更新版本的 Manager 写入的。此处原样显示记录值，不猜测其含义。',
  resultReconciliationTitle: '对账差异',
  resultReconciliationHelp: '这些日期不能被报告为完整。逐条列出，且不会静默修补。',
  resultReconciliationNone: '没有记录任何差异日期。',
  resultReconciliationTotal: '共记录 {count} 个不一致日期。',
  resultReconciliationBound: '此处列出其中最新的 {shown} 个；该列表是有界窗口，并不代表全部历史。',
  resultManualTitle: '手工项',
  resultManualNone: '没有记录手工项。',
  resultReread: '刷新状态',

  verifyTitle: '完成升级验证',
  verifyHelp: '核对复制来源、对账结果与目标 CPA 连接状态。',
  verifySourceRequired: '验证期间请保持 1.x 原数据目录处于只读挂载状态。',
  verifyAction: '完成升级验证',
  verifyRetry: '重新执行验证',
  verifyRunning: '正在验证已记录的操作…',
  verifyDoneTitle: '验证已记录结论',
  verifyComplete: '操作已完成。请重启 Manager：常规产品将启动，升级界面关闭。',
  verifyAlready: '该位置此前已记录，本次没有写入任何内容。',
  verifyFailedTitle: '验证未记录任何结论',
  verifyTargetExternal: '目标：连接外部独立运维的 CPA',
  verifyTargetManaged: '目标：内置受管 CPA',
  verifyReasonLabel: '已记录的原因',
  cpaImportReadyTitle: 'CPA 引擎已就绪，可执行导入',
  cpaImportReadyHelp: '固定版本 CPA 引擎已激活。请在 Manager 宿主机上执行导入命令：',
  stagePreflight: '预检',
  stageCopyIn: '复制入库',
  stageReconcile: '对账',
  stageCpaImport: 'CPA 导入',
  stageExternalConnect: 'CPA 连接',
  stageVerify: '验证',
  stageComplete: '完成',
  stageFailed: '失败',
  stageRolledBack: '已回滚',

  domainSourceCopy: '来源复制',
  domainUsageEvents: '用量事件',
  domainRetainedProjection: '留存投影',
  domainPricing: '定价',
  domainCoverage: '覆盖',
  domainIdentity: '身份',
  domainCpaConfig: 'CPA 配置',

  errorAuthentication: '当前会话无权读取升级状态。请重新验证管理员密钥。',
  errorNetwork: '无法连接升级服务，请检查连接后重试。',
  errorResponse: '返回内容无法核验，已不予显示。请重试。',
  errorUnavailable: '升级服务当前不可用。',

  routingTitle: '访问入口前缀',
  routingHelpManaged:
    '请为首次升级至 v2 的托管安装配置管理与推理前缀；默认使用服务端为本操作持久化的 16 位随机前缀，也可自定义或显式勾选共用模式。',
  routingHelpExternal:
    '请为外部 CPA 升级设置 CPAMP 管理入口前缀；模型推理仍直接使用已绑定的外部 CPA 地址。',
  routingPreservedTitle: '已保留原 v2 提交前缀',
  routingPreservedHelp: '当前安装已锁定提交过 v2 访问前缀，后续常规升级将原样保留管理与推理入口。',
  routingControlLabel: '管理入口前缀',
  routingControlHelp: '以 / 开头的规范路径，用于 CPAMP 控制台与管理 API。',
  routingInferenceLabel: '推理入口前缀',
  routingInferenceHelp: '以 / 开头的独立模型协议推理前缀；不得与管理前缀形成嵌套包含关系。',
  routingExternalInferenceLabel: '外部 CPA 推理地址',
  routingExternalInferenceHelp: '仅需设置管理入口前缀；模型推理流量继续直连外部 CPA。',
  routingRandomize: '重新随机生成',
  routingSharedToggle: '管理与推理使用同一前缀（显式共用模式）',
  routingSharedHelp: '默认关闭。仅当需要管理与推理共用同一路径前缀时显式勾选。',
  routingSharedLabel: '共用前缀模式',
  routingIndependentLabel: '独立双前缀',
  routingPreviewTitle: '最终访问地址预览',
  routingPreviewConsole: '管理控制台地址',
  routingPreviewApi: '管理 API 基路径',
  routingPreviewInference: '推理入口地址',
  routingPreviewExternal: '外部推理地址',
  routingSave: '保存前缀配置',
  routingSaving: '正在保存前缀配置…',
  routingSavedNotice: '访问前缀已保存至当前升级记录，刷新页面后仍会恢复。',
  resultAccessTitle: '访问入口与协议端点',
  resultAccessPendingRestart: '访问前缀已保存至升级记录，需重启 Manager 后才正式接管生效（当前尚未激活）。',
  resultAccessActive: '访问路由已验证并生效。',
  codeInvalidAccessPrefix: '请输入以 / 开头的规范路径，不能包含末尾斜杠、空白、百分号编码或点段。',
  codeInferencePrefixRequired: '托管模式升级必须配置推理入口前缀。',
  codeExternalInferencePrefixForbidden: '连接外部 CPA 模式不允许配置本地推理前缀或开启共用模式。',
  codeSharedPrefixMismatch: '开启共用模式时，管理前缀与推理前缀必须完全一致。',
  codeSharedPrefixConfirmationRequired: '管理前缀与推理前缀相同，请显式勾选共用模式或改为不同路径。',
  codePrefixOverlapForbidden: '管理前缀与推理前缀不得互为嵌套包含关系。',
  codeReservedPrefixCollision: '该路径与系统保留路由或协议路径冲突，请更换前缀。',
  codeUpgradeAccessPrefixRequired: '完成升级验证前，请先保存有效的访问入口前缀。',
  codeUpgradePrefixesAlreadyCommitted: '已有 v2 访问前缀已锁定提交，升级时不得覆盖修改。',
  codeUpgradeRestartRequired: '升级配置已保存，重启 Manager 后新入口与协议路由才会生效。',
  codeRoutingConflict: '路由状态与已记录操作冲突，请重新读取状态后再试。',
  codeRoutingVerificationFailed: '新路由核验未通过，请检查宿主机路由状态后重试。',
  modeBadgeManaged: '内置受管 CPA',
  modeBadgeExternal: '连接外部 CPA',
  externalDirectBadge: '同时作为推理直连基址',
  sourceKindLegacyManager: 'CPAMP 1.x 管理端',
  sourceKindUpgradedLegacy: 'CPAMP 1.x',
  techDetails: '查看原始诊断参数',
  checkSummaryOwnershipPass: '只读静止',
  checkSummaryVersionPending: '复制时传入 {version}',
  checkSummaryVersionPass: '版本受支持',
  checkSummaryFingerprintPass: '源数据约 {size}',
  checkSummaryFingerprintPlain: 'SHA-256 已生成',
  checkSummaryDiskPass: '需 {required} · 可用 {available}',
  checkSummaryCpaManaged: '内置受管 CPA',
  checkSummaryCpaExternal: '连接外部 CPA',
  checkSummaryParityPass: '核对一致，无断档',
  cliGuideTitle: '目标运行模式',
  cliGuideHelp: '请在宿主机按顺序执行以下 3 步命令。',
  cliStepDone: '已完成',
  cliStepCurrent: '当前步骤',
  cliStepPending: '待执行',
  cliStep1Title: '1. 停机备份并复制 1.x 数据（先停止当前 v2 容器）',
  cliStep2Title: '2. 执行历史用量与定价对账',
  cliStep3ManagedTitle: '3. 启动 v2 容器并导入内置 CPA 配置',
  cliStep3ExternalTitle: '3. 启动 v2 容器并绑定外部 CPA',
  cliCopy: '复制',
  cliCopied: '已复制',
  progressRefreshNow: '立即检查进度',
  confirmAutoTitle: '常规数据与密钥自动迁移',
  confirmAutoHelp: '历史用量、模型定价、客户端 API Key 及受支持的 CPA v8 配置均自动迁移。',
  confirmEdgeToggle: '查看需人工核对的边缘例外情况',
  progressCliTitle: '宿主机数据迁移',
  progressCliHelp: '请在宿主机按顺序执行以下 3 步命令；完成后本页会自动进入访问地址配置。',
  progressVerifyTitle: '确认访问地址并完成验证',
  progressVerifySubtitle: '宿主机数据迁移已就绪，请核对 v2 访问入口地址后完成验证。',
  migrationReadySummary: '宿主机迁移与对账已就绪（已完成 {count} 项 · 无差异 · 无手工项）',
  migrationDetailsToggle: '查看明细',
  routingCustomizeToggle: '自定义路由前缀',
  verifyInlineHint: '验证期间请保持 1.x 原数据目录处于只读挂载状态。',
  copyAction: '复制',
  copiedAction: '已复制',
  resultCompleteSummary: '已从 {source} 完成迁移 · 对账无差异 · 无手工待办项',
  resultAuditToggle: '查看迁移核验记录',
  migrationReadyWithManual: '宿主机迁移与对账已就绪（已完成 {count} 项 · 无对账差异）',
  manualDisclosureSummary: '{count} 项扩展配置未自动导入（升级后可在控制台按需设置）',
  manualReasonNotInScope: '超出自动导入范围，升级后可在控制台按需配置',
  manualReasonReconfigure: '升级后请在控制台重新配置',
  manualReasonReauth: '升级后请在控制台重新登录授权',
  manualReasonRemoved: '目标 CPA v8 已移除该旧配置项',
  slimEngineStepBadge: '当前步骤 · 请先完成',
  slimEngineTitle: '准备内置 CPA 引擎',
  slimEngineHelp: '当前精简镜像未附带 CPA 二进制文件，请先在此下载或上传官方引擎包，就绪后再执行下方宿主机导入命令。',
  cliStepWaitEngine: '等待引擎就绪',
};

const tw: typeof en = {
  flowLabel: '升級步驟',
  stepPreflight: '預檢',
  stepConfirm: '確認',
  stepProgress: '進度',
  stepResult: '結果',
  back: '返回',
  continue: '繼續',
  bootTitle: '讀取已記錄的操作',
  bootHelp:
    '精靈會先讀取本安裝已記錄的操作，再決定是否提供預檢。已記錄的操作會從原階段繼續，這不需要掛載來源。',
  bootReading: '正在讀取已記錄的操作…',
  bootFailedTitle: '無法讀取已記錄的操作',
  bootRetry: '重新讀取已記錄的操作',

  preflightTitle: '唯讀預檢',
  preflightHelp: '唯讀檢查已停止的 1.x 資料目錄，不修改任何原檔案。',
  preflightReading: '正在檢查來源…',
  preflightSourceKind: '來源',
  preflightSourceVersion: '版本',
  preflightFingerprint: '來源指紋',
  preflightChecks: '檢查項目',
  preflightUnknown: '未回報',
  preflightRetry: '重新預檢',
  preflightPendingTitle: '來源版本在命令列複製時宣告',
  preflightPendingHelp: '唯讀預檢無法宣告 1.x 版本，來源本身也不記錄此資訊。請在命令列複製時傳入',
  preflightPendingTail: '此檢查在本頁保持待定狀態，並不是失敗。',
  preflightBlockedTitle: '預檢阻斷了升級',
  preflightBlockedHelp: '請在主機上逐項解決下列問題後重新預檢。目前尚未複製任何資料。',
  preflightReadyTitle: '預檢全部通過',
  preflightReadyHelp: '未發現阻斷項目，可繼續下一步。',
  preflightReadyPendingTitle: '預檢通過，可繼續升級',
  preflightReadyPendingHelp: '複製階段會自動代入 --source-version 參數，無需在此手動設定。',
  preflightGapsTitle: '留存歷史缺口',
  preflightGapsHelp:
    '這些日期的原始事件已刪除，但與留存投影不一致。複製步驟會如實回報，不會靜默修補。',
  preflightGapsDays: '存在缺口的日期數',

  checkSourceOwnership: '執行鎖檢查',
  checkSourceVersion: '來源版本相容',
  checkFingerprint: '來源資料指紋',
  checkDisk: '可用磁碟空間',
  checkCpaClassification: 'CPA 部署形態',
  checkParity: '歷史資料一致性',

  checkPass: '通過',
  checkBlocked: '阻斷',
  checkSkipped: '略過',
  checkPending: '待定',
  checkGap: '缺口',
  checkUnknown: '未回報',

  codeUpgradeSourceMissing:
    '已掛載的 1.x 目錄中沒有 usage.sqlite。請確認整個資料目錄都以唯讀方式掛載後重試。',
  codeUpgradeSourceBusy: '1.x 來源仍被執行中的程序佔用。請先停止它再重試。',
  codeUpgradeSourceLockUnavailable:
    '無法只讀檢查來源的歸屬標記。請檢查掛載的 1.x 卷；未寫入任何內容。',
  codeUpgradeSourceSymlinked:
    '1.x 資料庫是透過連結存取，而不是一般檔案。請掛載資料目錄本身（不要使用連結檔案）後重試。',
  codeUpgradeSourceUnreadable: '無法安全讀取 1.x 資料庫。請在主機上檢查磁碟區與權限。',
  codeUpgradeSourceVersionUnsupported: '此 1.x 版本不能作為升級來源。',
  codeUpgradeDiskInsufficient: '複製所需的可用空間不足。',
  codeUpgradeCredentialTimeout: '來源未在限定時間內回應，無法驗證管理員金鑰。請在主機閒置時重試。',
  codeUpgradeMountTimeout:
    '已掛載的 1.x 磁碟區未在限定時間內回應，因此未能檢查。請在主機可回應時重試；本次未從該磁碟區讀取任何內容，也未寫入任何內容。',
  codeUpgradeLegacyRootMissing: '未掛載 1.x 資料目錄。請以唯讀方式掛載後重試。',
  codeUpgradeLegacyRootUnusable: '已掛載的 1.x 資料目錄不可用。',
  codeUpgradePreflightTimeout: '預檢未在限定時間內完成。請在主機閒置時重新預檢。',
  codeUpgradePreflightFailed: '預檢在回報來源之前就失敗了。',
  codeUpgradeOperationUnreadable: '無法讀取操作記錄。請在主機上檢查 Manager 資料庫。',
  codeUnknown: '此版本沒有該條件的說明。請回報完整錯誤碼。',
  codeUpgradeVerifyNotApplicable:
    '已記錄的操作不在可執行驗證的位置。請在主機上完成它指明的步驟，然後重新執行驗證。',
  codeUpgradeVerifyV2Running:
    '另一個行程持有 v2 資料根目錄，因此沒有進行驗證。請停止該行程後重新執行驗證。',
  codeUpgradeVerifySourceUnavailable:
    '無法觀測 1.x 來源，因此無法證明複製時讀取的磁碟區未被寫入。請以唯讀方式掛載該目錄後重新執行驗證。',
  codeUpgradeVerifySourceMismatch:
    '掛載的 1.x 來源不是已記錄複製所讀取的磁碟區，或在驗證過程中被寫入。本次沒有寫入任何內容。',
  codeUpgradeVerifyReconciliationInvalid:
    '已記錄的對帳結果不是本版本可讀取的證據，因此無法證明複製的儲存包含來源用量。請重新執行對帳。',
  codeUpgradeVerifyReconciliationUnacknowledged:
    '已記錄的對帳仍有未定或未確認的日期，因此從未被接受。請先確認這些日期再重新對帳。',
  codeUpgradeVerifyDomainsIncomplete:
    '已記錄的操作缺少該目標所需的某些資料域，因此這些域未被證明。',
  codeUpgradeVerifyBindingMissing:
    '已記錄的操作進入 CPA 連線步驟卻沒有目標綁定，因此無法證明它連線的 CPA。',
  codeUpgradeVerifyBindingDrifted:
    '本 Manager 目前連線的 CPA 不是連線步驟記錄的目標。請重新連線 CPA 後再驗證。',
  codeUpgradeVerifyConnectionUnproven:
    'CPA 匯入移交的連線未能保持：Manager 不再以匯入使用的管理金鑰指向該目標。',
  codeUpgradeVerifyConflict: '驗證過程中已記錄的操作發生變更，因此沒有寫入任何內容。請重新執行驗證。',
  codeUpgradeVerifyFailed: '驗證未能完成，也沒有記錄任何內容。',
  codeUpgradeRequestInvalid: '驗證不接受任何輸入，該請求已被拒絕。',

  confirmTitle: '升級確認',
  confirmHelp: '開始主機移轉前，請確認以下核心規則：',
  confirmSourceKind: '來源',
  confirmSourceVersion: '版本',
  confirmReadOnlyTitle: '原 1.x 資料全程唯讀',
  confirmReadOnlyHelp: '唯讀掛載讀取，複製前後雙重核驗指紋，絕不改動原資料磁碟區。',
  confirmBackupTitle: '先驗證備份再寫入',
  confirmBackupHelp: '匯入前先產生完整備份包並驗證通過，隨後才寫入 v2 資料磁碟區。',
  confirmManualTitle: '邊緣例外說明',
  confirmManualHelp: '僅當存在以下特殊情況時，才會在結果頁列出供人工核對：',
  confirmManualKeys: '格式異常或無法通過驗證的個別用戶端 API 金鑰',
  confirmManualCredentials: '需在目標環境補充授權或重新登入的上游提供者憑證',
  confirmManualGateway: '超出自動匯入範圍的自訂閘道執行參數（如監聽連接埠、TLS、外掛）',
  confirmManualHistory: '原 1.x 資料庫中本身存在缺失、無法重建投影的歷史日期',
  confirmAck: '我已了解以上升級規則，繼續下一步',
  confirmContinue: '繼續',

  progressTitle: '升級執行與路由設定',
  progressHelp: '依序在主機完成資料複製、對帳與 CPA 銜接後，即可在本頁設定存取入口前綴並完成升級驗證。',
  progressStage: '目前階段',
  progressDomains: '已完成領域',
  progressDomainsNone: '尚未記錄任何已完成領域。',
  progressManual: '已記錄的手動項目',
  progressManualNone: '尚未記錄手動項目。',
  progressReading: '正在讀取操作狀態…',
  progressUnrecorded: '尚未偵測到主機升級記錄，請依下方步驟在 Manager 主機依序執行。',
  progressWaiting: '每 10 秒自動偵測進度',
  resumeNotice: '已恢復目前升級進度。',
  progressStageNote: '',
  progressStalledTitle: '無法讀取操作狀態',
  progressStalledHelp: '連續三次讀取失敗。下方保留最後一次觀測結果，可能已過期。',
  progressRetry: '重新讀取操作狀態',

  resultStage: '記錄階段',
  resultNext: '下一步',
  resultSource: '記錄的來源',
  resultCompleteTitle: '升級已完成',
  resultCompleteHelp: '已完成資料與設定移轉，重新啟動 Manager 後即可使用新存取入口。',
  resultIncompleteTitle: '升級尚未完成',
  resultIncompleteHelp: '記錄中的操作尚未到達完成。本頁面不會聲稱升級已經結束。',
  resultRolledBackTitle: '操作已回滾到 1.x',
  resultRollbackReason: '記錄的原因',
  resultRollbackReasonUnknown: '該操作沒有記錄原因。請在 Manager 主機上查看回滾報告。',
  resultRollbackRestart:
    '請使用原磁碟區重新啟動 1.x 堆疊。v1 資料從未被寫入，因此會以升級前的狀態啟動；v2 中已記錄的內容不會回寫。',
  resultFailedTitle: '操作在失敗處停止',
  resultFailedHelp: '失敗與已完成的領域一起被記錄。對帳將從那裡繼續，已完成的部分不會重放。',
  resultUnreadableTitle: '該記錄階段本頁面無法辨識',
  resultUnreadableHelp: '可能是更新版本的 Manager 寫入的。此處原樣顯示記錄值，不猜測其含義。',
  resultReconciliationTitle: '對帳差異',
  resultReconciliationHelp: '這些日期不能被回報為完整。逐條列出，且不會靜默修補。',
  resultReconciliationNone: '沒有記錄任何差異日期。',
  resultReconciliationTotal: '共記錄 {count} 個不一致日期。',
  resultReconciliationBound: '此處列出其中最新的 {shown} 個；該清單是有界視窗，並不代表全部歷史。',
  resultManualTitle: '手動項目',
  resultManualNone: '沒有記錄手動項目。',
  resultReread: '重新整理狀態',

  verifyTitle: '完成升級驗證',
  verifyHelp: '核對複製來源、對帳結果與目標 CPA 連線狀態。',
  verifySourceRequired: '驗證期間請保持 1.x 原資料目錄處於唯讀掛載狀態。',
  verifyAction: '完成升級驗證',
  verifyRetry: '重新執行驗證',
  verifyRunning: '正在驗證已記錄的操作…',
  verifyDoneTitle: '驗證已記錄結論',
  verifyComplete: '操作已完成。請重新啟動 Manager：一般產品將啟動，升級介面關閉。',
  verifyAlready: '該位置先前已記錄，本次沒有寫入任何內容。',
  verifyFailedTitle: '驗證未記錄任何結論',
  verifyTargetExternal: '目標：連接外部獨立維運的 CPA',
  verifyTargetManaged: '目標：內建受管 CPA',
  verifyReasonLabel: '已記錄的原因',
  cpaImportReadyTitle: 'CPA 引擎已就緒，可執行匯入',
  cpaImportReadyHelp: '固定版本 CPA 引擎已啟用。請在 Manager 主機上執行匯入命令：',
  stagePreflight: '預檢',
  stageCopyIn: '複製寫入',
  stageReconcile: '對帳',
  stageCpaImport: 'CPA 匯入',
  stageExternalConnect: 'CPA 連線',
  stageVerify: '驗證',
  stageComplete: '完成',
  stageFailed: '失敗',
  stageRolledBack: '已回滾',

  domainSourceCopy: '來源複製',
  domainUsageEvents: '用量事件',
  domainRetainedProjection: '留存投影',
  domainPricing: '定價',
  domainCoverage: '覆蓋',
  domainIdentity: '身分',
  domainCpaConfig: 'CPA 設定',

  errorAuthentication: '目前工作階段無權讀取升級狀態。請重新驗證管理員金鑰。',
  errorNetwork: '無法連線升級服務，請檢查連線後重試。',
  errorResponse: '回傳內容無法核驗，已不予顯示。請重試。',
  errorUnavailable: '升級服務目前無法使用。',

  routingTitle: '存取入口前綴',
  routingHelpManaged:
    '請為首次升級至 v2 的託管安裝設定管理與推論前綴；預設使用伺服器為本操作持久化的 16 位元隨機前綴，也可自訂或明確勾選共用模式。',
  routingHelpExternal:
    '請為外部 CPA 升級設定 CPAMP 管理入口前綴；模型推論仍直接使用已綁定的外部 CPA 位址。',
  routingPreservedTitle: '已保留原 v2 提交前綴',
  routingPreservedHelp: '目前安裝已鎖定提交過 v2 存取前綴，後續常規升級將原樣保留管理與推論入口。',
  routingControlLabel: '管理入口前綴',
  routingControlHelp: '以 / 開頭的規範路徑，用於 CPAMP 控制台與管理 API。',
  routingInferenceLabel: '推論入口前綴',
  routingInferenceHelp: '以 / 開頭的獨立模型協定推論前綴；不得與管理前綴形成巢狀包含關係。',
  routingExternalInferenceLabel: '外部 CPA 推論位址',
  routingExternalInferenceHelp: '僅需設定管理入口前綴；模型推論流量繼續直連外部 CPA。',
  routingRandomize: '重新隨機產生',
  routingSharedToggle: '管理與推論使用同一前綴（明確共用模式）',
  routingSharedHelp: '預設關閉。僅當需要管理與推論共用同一路徑前綴時明確勾選。',
  routingSharedLabel: '共用前綴模式',
  routingIndependentLabel: '獨立雙前綴',
  routingPreviewTitle: '最終存取位址預覽',
  routingPreviewConsole: '管理控制台位址',
  routingPreviewApi: '管理 API 基路徑',
  routingPreviewInference: '推論入口位址',
  routingPreviewExternal: '外部推論位址',
  routingSave: '儲存前綴設定',
  routingSaving: '正在儲存前綴設定…',
  routingSavedNotice: '存取前綴已儲存至目前升級記錄，重新整理頁面後仍會恢復。',
  resultAccessTitle: '存取入口與協定端點',
  resultAccessPendingRestart: '存取前綴已儲存至升級記錄，需重新啟動 Manager 後才正式接管生效（目前尚未啟用）。',
  resultAccessActive: '存取路由已驗證並生效。',
  codeInvalidAccessPrefix: '請輸入以 / 開頭的規範路徑，不能包含結尾斜線、空白、百分比編碼或點區段。',
  codeInferencePrefixRequired: '託管模式升級必須設定推論入口前綴。',
  codeExternalInferencePrefixForbidden: '連接外部 CPA 模式不允許設定本機推論前綴或啟用共用模式。',
  codeSharedPrefixMismatch: '啟用共用模式時，管理前綴與推論前綴必須完全一致。',
  codeSharedPrefixConfirmationRequired: '管理前綴與推論前綴相同，請明確勾選共用模式或改為不同路徑。',
  codePrefixOverlapForbidden: '管理前綴與推論前綴不得互為巢狀包含關係。',
  codeReservedPrefixCollision: '此路徑與系統保留路由或協定路徑衝突，請更換前綴。',
  codeUpgradeAccessPrefixRequired: '完成升級驗證前，請先儲存有效的存取入口前綴。',
  codeUpgradePrefixesAlreadyCommitted: '已有 v2 存取前綴已鎖定提交，升級時不得覆寫修改。',
  codeUpgradeRestartRequired: '升級設定已儲存，重新啟動 Manager 後新入口與協定路由才會生效。',
  codeRoutingConflict: '路由狀態與已記錄操作衝突，請重新讀取狀態後再試。',
  codeRoutingVerificationFailed: '新路由核驗未通過，請檢查主機路由狀態後重試。',
  modeBadgeManaged: '內建受管 CPA',
  modeBadgeExternal: '連接外部 CPA',
  externalDirectBadge: '同時作為推論直連基址',
  sourceKindLegacyManager: 'CPAMP 1.x 管理端',
  sourceKindUpgradedLegacy: 'CPAMP 1.x',
  techDetails: '檢視原始診斷參數',
  checkSummaryOwnershipPass: '唯讀靜止',
  checkSummaryVersionPending: '複製時傳入 {version}',
  checkSummaryVersionPass: '版本受支援',
  checkSummaryFingerprintPass: '源資料約 {size}',
  checkSummaryFingerprintPlain: 'SHA-256 已產生',
  checkSummaryDiskPass: '需 {required} · 可用 {available}',
  checkSummaryCpaManaged: '內建受管 CPA',
  checkSummaryCpaExternal: '連接外部 CPA',
  checkSummaryParityPass: '核對一致，無斷檔',
  cliGuideTitle: '目標執行模式',
  cliGuideHelp: '請在主機依序執行以下 3 步指令。',
  cliStepDone: '已完成',
  cliStepCurrent: '目前步驟',
  cliStepPending: '待執行',
  cliStep1Title: '1. 停機備份並複製 1.x 資料（先停止目前 v2 容器）',
  cliStep2Title: '2. 執行歷史用量與定價對帳',
  cliStep3ManagedTitle: '3. 啟動 v2 容器並匯入內建 CPA 設定',
  cliStep3ExternalTitle: '3. 啟動 v2 容器並綁定外部 CPA',
  cliCopy: '複製',
  cliCopied: '已複製',
  progressRefreshNow: '立即檢查進度',
  confirmAutoTitle: '常規資料與金鑰自動移轉',
  confirmAutoHelp: '歷史用量、模型定價、用戶端 API 金鑰及受支援的 CPA v8 設定均自動移轉。',
  confirmEdgeToggle: '檢視需人工核對的邊緣例外情況',
  progressCliTitle: '主機資料移轉',
  progressCliHelp: '請在主機依序執行以下 3 步指令；完成後本頁會自動進入存取位址設定。',
  progressVerifyTitle: '確認存取位址並完成驗證',
  progressVerifySubtitle: '主機資料移轉已就緒，請核對 v2 存取入口位址後完成驗證。',
  migrationReadySummary: '主機移轉與對帳已就緒（已完成 {count} 項 · 無差異 · 無手動項）',
  migrationDetailsToggle: '檢視明細',
  routingCustomizeToggle: '自訂路由前綴',
  verifyInlineHint: '驗證期間請保持 1.x 原資料目錄處於唯讀掛載狀態。',
  copyAction: '複製',
  copiedAction: '已複製',
  resultCompleteSummary: '已從 {source} 完成移轉 · 對帳無差異 · 無手動待辦項',
  resultAuditToggle: '檢視移轉核驗記錄',
  migrationReadyWithManual: '主機移轉與對帳已就緒（已完成 {count} 項 · 無對帳差異）',
  manualDisclosureSummary: '{count} 項擴充設定未自動匯入（升級後可在主控台按需設定）',
  manualReasonNotInScope: '超出自動匯入範圍，升級後可在主控台按需設定',
  manualReasonReconfigure: '升級後請在主控台重新設定',
  manualReasonReauth: '升級後請在主控台重新登入授權',
  manualReasonRemoved: '目標 CPA v8 已移除此舊設定項',
  slimEngineStepBadge: '目前步驟 · 請先完成',
  slimEngineTitle: '準備內建 CPA 引擎',
  slimEngineHelp: '目前精簡映像未附帶 CPA 二進位檔，請先在此下載或上傳官方引擎套件，就緒後再執行下方主機匯入指令。',
  cliStepWaitEngine: '等待引擎就緒',
};

const ru: typeof en = {
  flowLabel: 'Шаги обновления',
  stepPreflight: 'Проверка',
  stepConfirm: 'Подтверждение',
  stepProgress: 'Ход',
  stepResult: 'Итог',
  back: 'Назад',
  continue: 'Продолжить',
  bootTitle: 'Чтение записанной операции',
  bootHelp:
    'Мастер сначала читает то, что эта установка уже записала, и только затем предлагает предполётную проверку. Записанная операция продолжается с сохранённого этапа; для этого источник не нужно монтировать.',
  bootReading: 'Чтение записанной операции…',
  bootFailedTitle: 'Не удалось прочитать записанную операцию',
  bootRetry: 'Прочитать записанную операцию ещё раз',

  preflightTitle: 'Проверка только для чтения',
  preflightHelp:
    'Эта проверка читает остановленный источник 1.x. Она ничего не копирует, не записывает и не даёт прав на следующий шаг.',
  preflightReading: 'Проверка источника…',
  preflightSourceKind: 'Тип источника',
  preflightSourceVersion: 'Версия источника',
  preflightFingerprint: 'Отпечаток источника (сокращённо)',
  preflightChecks: 'Проверки',
  preflightUnknown: 'Не сообщается',
  preflightRetry: 'Запустить проверку снова',
  preflightPendingTitle: 'Версия источника объявляется при копировании в CLI',
  preflightPendingHelp:
    'Предварительная проверка только для чтения не может объявить версию 1.x, и сам источник её не хранит. Это делает копирование в CLI: передайте',
  preflightPendingTail: 'Здесь проверка остаётся в ожидании — это не ошибка.',
  preflightBlockedTitle: 'Проверка заблокировала обновление',
  preflightBlockedHelp:
    'Устраните каждое названное условие на хосте и запустите проверку снова. Ничего не скопировано.',
  preflightReadyTitle: 'Источник прошёл проверку',
  preflightReadyHelp:
    'Все проверки пройдены. Данные ещё не скопированы; следующий шаг описывает, что сделает операция.',
  preflightReadyPendingTitle:
    'Источник прошёл проверку (есть пункты для подтверждения при копировании)',
  preflightReadyPendingHelp:
    'Блокирующих проблем не найдено; ожидающие проверки нужно подтвердить при копировании в CLI (--source-version). Данные ещё не скопированы; следующий шаг описывает, что сделает операция.',
  preflightGapsTitle: 'Пробелы в сохранённой истории',
  preflightGapsHelp:
    'Дни, в которых удалённые исходные события больше не совпадают с сохранённой проекцией. Копирование сообщает о них и никогда не исправляет их молча.',
  preflightGapsDays: 'Дней с пробелами',

  checkSourceOwnership: 'Источник не занят работающим процессом',
  checkSourceVersion: 'Версия источника поддерживается',
  checkFingerprint: 'Отпечаток источника записан',
  checkDisk: 'Свободное место для копирования',
  checkCpaClassification: 'Классификация CPA',
  checkParity: 'Согласованность сохранённой истории',

  checkPass: 'Пройдено',
  checkBlocked: 'Заблокировано',
  checkSkipped: 'Пропущено',
  checkPending: 'Ожидает',
  checkGap: 'Пробел',
  checkUnknown: 'Не сообщается',

  codeUpgradeSourceMissing:
    'В смонтированном каталоге 1.x нет usage.sqlite. Проверьте, что каталог данных смонтирован целиком и только для чтения, и повторите.',
  codeUpgradeSourceBusy: 'Источник 1.x всё ещё занят работающим процессом. Остановите его и повторите.',
  codeUpgradeSourceLockUnavailable:
    'Маркер владения источником нельзя проверить только для чтения. Проверьте смонтированный том 1.x; ничего не записано.',
  codeUpgradeSourceSymlinked:
    'База 1.x доступна через ссылку, а не как сам файл. Смонтируйте сам каталог данных, без файлов-ссылок, и повторите.',
  codeUpgradeSourceUnreadable: 'База 1.x не читается безопасно. Проверьте том и его права на хосте.',
  codeUpgradeSourceVersionUnsupported: 'Эта версия 1.x не поддерживается как источник обновления.',
  codeUpgradeDiskInsufficient: 'Недостаточно свободного места для копирования.',
  codeUpgradeCredentialTimeout:
    'Источник не ответил вовремя, поэтому ключ администратора не проверен. Повторите, когда хост свободен.',
  codeUpgradeMountTimeout:
    'Смонтированный том 1.x не ответил вовремя, поэтому проверка не выполнялась. Повторите, когда хост ответит; из тома ничего не читалось и в него ничего не записывалось.',
  codeUpgradeLegacyRootMissing: 'Каталог данных 1.x не смонтирован. Смонтируйте его только для чтения и повторите.',
  codeUpgradeLegacyRootUnusable: 'Смонтированный каталог данных 1.x непригоден.',
  codeUpgradePreflightTimeout: 'Проверка не завершилась вовремя. Повторите её, когда хост свободен.',
  codeUpgradePreflightFailed: 'Проверка завершилась ошибкой, не успев описать источник.',
  codeUpgradeOperationUnreadable: 'Запись операции не читается. Проверьте базу Manager на хосте.',
  codeUnknown: 'В этой версии нет описания этого условия. Передайте точный код.',
  codeUpgradeVerifyNotApplicable:
    'Записанная операция находится там, откуда проверка не может начаться. Выполните названный ею шаг на хосте и запустите проверку снова.',
  codeUpgradeVerifyV2Running:
    'Корневой каталог данных v2 занят другим процессом, поэтому ничего не проверено. Остановите его и запустите проверку снова.',
  codeUpgradeVerifySourceUnavailable:
    'Источник 1.x недоступен для наблюдения, поэтому нельзя доказать, что прочитанный при копировании том не был записан. Смонтируйте каталог только для чтения и повторите проверку.',
  codeUpgradeVerifySourceMismatch:
    'Смонтированный источник 1.x — не тот том, который прочитало записанное копирование, либо в него писали во время проверки. Ничего не записано.',
  codeUpgradeVerifyReconciliationInvalid:
    'Записанная сверка не читается этой сборкой, поэтому ничто не доказывает, что скопированное хранилище содержит данные источника. Выполните сверку снова.',
  codeUpgradeVerifyReconciliationUnacknowledged:
    'В записанной сверке остались нерешённые или неподтверждённые дни, поэтому она не была принята. Подтвердите их и выполните сверку снова.',
  codeUpgradeVerifyDomainsIncomplete:
    'Записанная операция не содержит всех доменов, нужных этой цели, поэтому по ним копия не доказана.',
  codeUpgradeVerifyBindingMissing:
    'Записанная операция дошла до шага подключения CPA без привязки к цели, поэтому CPA, к которому она подключилась, недоказуем.',
  codeUpgradeVerifyBindingDrifted:
    'CPA, к которому подключается этот Manager, — не та цель, которую записал шаг подключения. Подключите CPA заново и повторите проверку.',
  codeUpgradeVerifyConnectionUnproven:
    'Соединение, переданное импортом CPA, не сохранилось: Manager больше не указывает на это назначение с тем секретом, который использовал импорт.',
  codeUpgradeVerifyConflict:
    'Записанная операция изменилась во время проверки, поэтому ничего не записано. Запустите проверку снова.',
  codeUpgradeVerifyFailed: 'Проверка не завершилась и ничего не записала.',
  codeUpgradeRequestInvalid: 'Проверка не принимает входных данных; запрос отклонён.',

  confirmTitle: 'Что сделает обновление',
  confirmHelp:
    'Проверьте эти факты до начала операции. Эта страница только читает состояние; само копирование запускается на хосте Manager.',
  confirmSourceKind: 'Тип источника',
  confirmSourceVersion: 'Версия источника',
  confirmReadOnlyTitle: 'Том 1.x остаётся доступным только для чтения',
  confirmReadOnlyHelp:
    'Каталог данных 1.x смонтирован только для чтения; из него только копируют. Его отпечаток записывается до и после копирования.',
  confirmBackupTitle: 'Сначала делается проверенная резервная копия',
  confirmBackupHelp:
    'Перед копированием создаётся архив резервной копии и проверяется до любой записи в том v2.',
  confirmManualTitle: 'Пункты, которые могут потребовать ручной работы',
  confirmManualHelp:
    'Всё, что операция не сможет перенести, будет перечислено на шаге итога. Ничего не теряется молча.',
  confirmManualKeys: 'Ключи клиентов CPA, которые нужно создать заново на цели v8',
  confirmManualCredentials: 'Разделы провайдеров и учётных данных CPA, которые нужно ввести заново',
  confirmManualGateway: 'Конфигурация шлюза, которую цель не может импортировать автоматически',
  confirmManualHistory: 'Дни использования, сохранённую историю которых нельзя восстановить',
  confirmAck:
    'Я понимаю: источник 1.x остаётся только для чтения, сначала делается проверенная резервная копия, а перечисленные пункты могут потребовать ручной работы.',
  confirmContinue: 'Перейти к ходу выполнения',

  progressTitle: 'Записанный ход копирования',
  progressHelp: 'Эта страница только читает записанное состояние операции и никогда её не продвигает.',
  progressStage: 'Текущий этап',
  progressDomains: 'Завершённые области',
  progressDomainsNone: 'Пока ни одна область не записана как завершённая.',
  progressManual: 'Записанные ручные пункты',
  progressManualNone: 'Ручные пункты не записаны.',
  progressReading: 'Чтение операции…',
  progressUnrecorded: 'Операция обновления ещё не записана. Запустите её на хосте Manager.',
  progressWaiting: 'Ожидание следующего записанного изменения…',
  resumeNotice:
    'Эта установка уже записала операцию обновления, поэтому страница продолжает с записанного этапа. Проверка только для чтения нужна, только когда операций нет.',
  progressStageNote:
    'Записанный этап — последняя позиция, которую записала операция. Это не доказывает, что работа за ним завершена.',
  progressStalledTitle: 'Состояние операции не читается',
  progressStalledHelp:
    'Три чтения подряд завершились ошибкой. Ниже сохранено последнее наблюдение; оно может быть устаревшим.',
  progressRetry: 'Прочитать операцию снова',

  resultStage: 'Записанный этап',
  resultNext: 'Следующий шаг',
  resultSource: 'Записанный источник',
  resultCompleteTitle: 'Обновление завершено',
  resultCompleteHelp: 'Записанная операция сообщает о завершении. v2 Manager теперь обслуживает обычные поверхности.',
  resultIncompleteTitle: 'Обновление не завершено',
  resultIncompleteHelp:
    'Записанная операция не достигла завершения. Эта страница не утверждает, что обновление закончилось.',
  resultRolledBackTitle: 'Операция откатана к 1.x',
  resultRollbackReason: 'Записанная причина',
  resultRollbackReasonUnknown: 'Операция не записала причину. Проверьте отчёт отката на хосте Manager.',
  resultRollbackRestart:
    'Перезапустите стек 1.x с исходным томом. Данные v1 никогда не записывались, поэтому он запустится в состоянии до обновления; записанное в v2 обратно не переносится.',
  resultFailedTitle: 'Операция остановилась на ошибке',
  resultFailedHelp:
    'Ошибка записана вместе с уже завершёнными областями. Сверка продолжится с этого места; сделанное не повторяется.',
  resultUnreadableTitle: 'Записанный этап неизвестен этой странице',
  resultUnreadableHelp:
    'Возможно, его записал более новый Manager. Этап показан точно как записан; страница не угадывает его значение.',
  resultReconciliationTitle: 'Расхождения сверки',
  resultReconciliationHelp:
    'Эти дни нельзя считать полными. Каждый перечислен; ни один не исправляется молча.',
  resultReconciliationNone: 'Расхождения по дням не записаны.',
  resultReconciliationTotal: 'Записано дней с расхождениями: {count}.',
  resultReconciliationBound:
    'Показаны последние {shown} из них; это ограниченное окно, а не вся история.',
  resultManualTitle: 'Ручные пункты',
  resultManualNone: 'Ручные пункты не записаны.',
  resultReread: 'Прочитать операцию снова',

  verifyTitle: 'Проверить и завершить обновление',
  verifyHelp:
    'Проверка заново читает записанную операцию и подтверждает скопированный источник, сверку и CPA, который использует эта установка. Она записывает единственный вывод, который допускает цель; эта страница никогда не объявляет обновление завершённым сама.',
  verifySourceRequired:
    'Каталог данных 1.x должен оставаться смонтированным и остановленным: его отпечаток доказывает, что том, прочитанный при копировании, не был записан.',
  verifyAction: 'Выполнить проверку',
  verifyRetry: 'Выполнить проверку снова',
  verifyRunning: 'Проверка записанной операции…',
  verifyDoneTitle: 'Проверка записала вывод',
  verifyComplete:
    'Операция завершена. Перезапустите Manager: запустится обычный продукт, а интерфейс обновления закроется.',
  verifyAlready: 'Эта позиция уже была записана; на этот раз ничего не записано.',
  verifyFailedTitle: 'Проверка ничего не записала',
  verifyTargetExternal: 'Цель: внешний CPA, которым управляет его владелец',
  verifyTargetManaged: 'Цель: CPA, которым владеет этот Manager',
  verifyReasonLabel: 'Записанная причина',
  cpaImportReadyTitle: 'Движок CPA готов к импорту',
  cpaImportReadyHelp: 'Фиксированный движок CPA активен. Выполните импорт на хосте Manager:',
  stagePreflight: 'Проверка',
  stageCopyIn: 'Копирование',
  stageReconcile: 'Сверка',
  stageCpaImport: 'Импорт CPA',
  stageExternalConnect: 'Подключение CPA',
  stageVerify: 'Проверка результата',
  stageComplete: 'Завершено',
  stageFailed: 'Ошибка',
  stageRolledBack: 'Откат выполнен',

  domainSourceCopy: 'Копия источника',
  domainUsageEvents: 'События использования',
  domainRetainedProjection: 'Сохранённая проекция',
  domainPricing: 'Цены',
  domainCoverage: 'Покрытие',
  domainIdentity: 'Идентичность',
  domainCpaConfig: 'Конфигурация CPA',

  errorAuthentication:
    'У этого сеанса нет прав на чтение состояния обновления. Проверьте ключ администратора ещё раз.',
  errorNetwork: 'Служба обновления недоступна. Проверьте соединение и повторите.',
  errorResponse: 'Ответ не удалось проверить, он не показан. Повторите попытку.',
  errorUnavailable: 'Служба обновления сейчас недоступна.',

  routingTitle: 'Префиксы доступа',
  routingHelpManaged:
    'Настройте независимые префиксы управления и инференса для первого обновления до v2 или явно включите общий режим. По умолчанию используются случайные 16-символьные префиксы, сохранённые в операции.',
  routingHelpExternal:
    'Задайте префикс управления CPAMP для обновления с внешним CPA. Трафик инференса продолжит идти по настроенному внешнему адресу.',
  routingPreservedTitle: 'Зафиксированные префиксы v2 сохранены',
  routingPreservedHelp:
    'Эта установка уже зафиксировала префиксы доступа v2. Последующие обновления сохраняют эти маршруты без изменений.',
  routingControlLabel: 'Префикс входа управления',
  routingControlHelp: 'Канонический путь с /, обслуживающий консоль CPAMP и API управления.',
  routingInferenceLabel: 'Префикс входа инференса',
  routingInferenceHelp:
    'Независимый путь с / для протокольных эндпоинтов моделей. Не может вкладываться в префикс управления.',
  routingExternalInferenceLabel: 'Внешний адрес инференса CPA',
  routingExternalInferenceHelp: 'Настраивается только префикс управления; трафик инференса идёт напрямую на внешний CPA.',
  routingRandomize: 'Сгенерировать новый',
  routingSharedToggle: 'Использовать один префикс для управления и инференса (общий режим)',
  routingSharedHelp: 'По умолчанию выключено. Включайте только при намеренном совмещении на одном префиксе.',
  routingSharedLabel: 'Общий префикс',
  routingIndependentLabel: 'Раздельные префиксы',
  routingPreviewTitle: 'Предпросмотр итоговых URL',
  routingPreviewConsole: 'Адрес консоли управления',
  routingPreviewApi: 'Базовый путь API управления',
  routingPreviewInference: 'Базовый адрес инференса',
  routingPreviewExternal: 'Внешний URL инференса',
  routingSave: 'Сохранить префиксы',
  routingSaving: 'Сохранение префиксов…',
  routingSavedNotice: 'Префиксы доступа сохранены в операции обновления и восстановятся при обновлении страницы.',
  resultAccessTitle: 'Точки доступа и протоколы',
  resultAccessPendingRestart:
    'Префиксы доступа сохранены в операции обновления, но маршруты ожидают перезапуска Manager и ещё не активны.',
  resultAccessActive: 'Маршруты доступа проверены и активны.',
  codeInvalidAccessPrefix:
    'Введите канонический путь, начинающийся с /, без конечного слэша, пробелов, процентного кодирования и сегментов с точками.',
  codeInferencePrefixRequired: 'Для управляемого обновления требуется префикс входа инференса.',
  codeExternalInferencePrefixForbidden:
    'В режиме External нельзя указывать локальный префикс инференса или общий режим.',
  codeSharedPrefixMismatch: 'В общем режиме префиксы управления и инференса должны совпадать.',
  codeSharedPrefixConfirmationRequired:
    'Префиксы управления и инференса совпадают. Явно включите общий режим или выберите разные префиксы.',
  codePrefixOverlapForbidden: 'Префиксы управления и инференса не могут вкладываться друг в друга.',
  codeReservedPrefixCollision:
    'Этот префикс конфликтует с зарезервированным системным или протокольным маршрутом. Выберите другой путь.',
  codeUpgradeAccessPrefixRequired:
    'Перед завершением проверки обновления сохраните корректные префиксы доступа для целевого режима.',
  codeUpgradePrefixesAlreadyCommitted:
    'Зафиксированные префиксы доступа v2 уже заблокированы и не могут быть перезаписаны при обновлении.',
  codeUpgradeRestartRequired:
    'Настройки обновления сохранены; новые маршруты активируются только после перезапуска Manager.',
  codeRoutingConflict: 'Состояние маршрутизации конфликтует с другой операцией. Перечитайте статус и повторите.',
  codeRoutingVerificationFailed: 'Не удалось подтвердить опубликованные маршруты. Проверьте маршрутизацию на хосте и повторите.',
  modeBadgeManaged: 'Управляемый CPA',
  modeBadgeExternal: 'Внешний CPA',
  externalDirectBadge: 'Прямой базовый адрес инференса',
  sourceKindLegacyManager: 'CPAMP 1.x Manager',
  sourceKindUpgradedLegacy: 'CPAMP 1.x',
  techDetails: 'Исходные параметры диагностики',
  checkSummaryOwnershipPass: 'Только чтение, в покое',
  checkSummaryVersionPending: 'Передать {version} при копировании',
  checkSummaryVersionPass: 'Версия поддерживается',
  checkSummaryFingerprintPass: 'Объём ~{size}',
  checkSummaryFingerprintPlain: 'SHA-256 записан',
  checkSummaryDiskPass: 'Нужно {required} · свободно {available}',
  checkSummaryCpaManaged: 'Управляемый CPA',
  checkSummaryCpaExternal: 'Внешний CPA',
  checkSummaryParityPass: 'Совпадает, без пропусков',
  cliGuideTitle: 'Целевой режим',
  cliGuideHelp: 'Выполните 3 команды на хосте по порядку.',
  cliStepDone: 'Готово',
  cliStepCurrent: 'Текущий',
  cliStepPending: 'Ожидает',
  cliStep1Title: '1. Резервная копия и перенос данных 1.x (остановите v2)',
  cliStep2Title: '2. Сверка статистики и цен',
  cliStep3ManagedTitle: '3. Запуск v2 и импорт конфигурации CPA v8',
  cliStep3ExternalTitle: '3. Запуск v2 и привязка внешнего CPA v8',
  cliCopy: 'Копировать',
  cliCopied: 'Скопировано',
  progressRefreshNow: 'Проверить сейчас',
  confirmAutoTitle: 'Автоматический перенос данных и ключей',
  confirmAutoHelp: 'История использования, цены, ключи клиентов и поддерживаемые настройки CPA v8 переносятся автоматически.',
  confirmEdgeToggle: 'Особые случаи для ручной проверки',
  progressCliTitle: 'Перенос данных на хосте',
  progressCliHelp: 'Выполните 3 команды на хосте по порядку; затем откроется настройка адресов.',
  progressVerifyTitle: 'Подтверждение адресов и завершение обновления',
  progressVerifySubtitle: 'Перенос на хосте готов. Проверьте адреса доступа v2 и завершите проверку.',
  migrationReadySummary: 'Перенос и сверка готовы ({count} обл. · 0 расхождений · 0 ручных пунктов)',
  migrationDetailsToggle: 'Подробнее',
  routingCustomizeToggle: 'Настроить префиксы маршрутов',
  verifyInlineHint: 'Оставьте каталог 1.x смонтированным только для чтения на время проверки.',
  copyAction: 'Копировать',
  copiedAction: 'Скопировано',
  resultCompleteSummary: 'Перенесено из {source} · 0 расхождений · 0 ручных пунктов',
  resultAuditToggle: 'Подробности проверки переноса',
  migrationReadyWithManual: 'Перенос и сверка готовы ({count} обл. · 0 расхождений)',
  manualDisclosureSummary: 'Пропущено дополнительных параметров: {count} (настройте в консоли после обновления)',
  manualReasonNotInScope: 'Вне области автоимпорта; настройте в консоли при необходимости',
  manualReasonReconfigure: 'Настройте заново в консоли после обновления',
  manualReasonReauth: 'Повторно авторизуйте провайдера в консоли',
  manualReasonRemoved: 'Удалено в целевой версии CPA v8',
  slimEngineStepBadge: 'Текущий шаг · Выполните сначала',
  slimEngineTitle: 'Подготовка встроенного движка CPA',
  slimEngineHelp: 'Образ slim не содержит бинарный файл CPA. Сначала подготовьте движок здесь, затем выполните команду upgrade cpa-import ниже.',
  cliStepWaitEngine: 'Ожидание движка',
};

export type UpgradeWizardKey = keyof typeof en;

export function upgradeWizardText(key: UpgradeWizardKey, language = fullWebLanguage()): string {
  if (language === 'zh-TW') return tw[key];
  if (language.startsWith('zh')) return zh[key];
  if (language === 'ru') return ru[key];
  return en[key];
}

// A check name this build does not know is shown verbatim: the report's own
// vocabulary is evidence, and hiding an unrecognised row would hide a failure.
export function checkNameText(name: string, language = fullWebLanguage()): string {
  switch (name) {
    case 'source_ownership':
      return upgradeWizardText('checkSourceOwnership', language);
    case 'source_version':
      return upgradeWizardText('checkSourceVersion', language);
    case 'fingerprint':
      return upgradeWizardText('checkFingerprint', language);
    case 'disk':
      return upgradeWizardText('checkDisk', language);
    case 'cpa_classification':
      return upgradeWizardText('checkCpaClassification', language);
    case 'parity':
      return upgradeWizardText('checkParity', language);
    default:
      return name;
  }
}

export function checkOutcomeText(status: string, language = fullWebLanguage()): string {
  switch (status) {
    case 'pass':
      return upgradeWizardText('checkPass', language);
    case 'blocked':
      return upgradeWizardText('checkBlocked', language);
    case 'skipped':
      return upgradeWizardText('checkSkipped', language);
    case 'gap':
      return upgradeWizardText('checkGap', language);
    case 'pending':
      return upgradeWizardText('checkPending', language);
    default:
      return upgradeWizardText('checkUnknown', language);
  }
}

// Every blocking code the read routes can return, named in the operator's
// language. A code this build cannot name still reaches the operator as text;
// `codeUnknown` asks for the exact value instead of inventing a meaning.
export function blockedCodeText(code: string, language = fullWebLanguage()): string {
  switch (code) {
    case 'upgrade_source_missing':
      return upgradeWizardText('codeUpgradeSourceMissing', language);
    case 'upgrade_source_busy':
      return upgradeWizardText('codeUpgradeSourceBusy', language);
    case 'upgrade_source_lock_unavailable':
      return upgradeWizardText('codeUpgradeSourceLockUnavailable', language);
    case 'upgrade_source_symlinked':
      return upgradeWizardText('codeUpgradeSourceSymlinked', language);
    case 'upgrade_source_unreadable':
      return upgradeWizardText('codeUpgradeSourceUnreadable', language);
    case 'upgrade_source_version_unsupported':
      return upgradeWizardText('codeUpgradeSourceVersionUnsupported', language);
    case 'upgrade_disk_insufficient':
      return upgradeWizardText('codeUpgradeDiskInsufficient', language);
    case 'upgrade_credential_timeout':
      return upgradeWizardText('codeUpgradeCredentialTimeout', language);
    case 'upgrade_mount_timeout':
      return upgradeWizardText('codeUpgradeMountTimeout', language);
    case 'upgrade_legacy_root_missing':
      return upgradeWizardText('codeUpgradeLegacyRootMissing', language);
    case 'upgrade_legacy_root_unusable':
      return upgradeWizardText('codeUpgradeLegacyRootUnusable', language);
    case 'upgrade_preflight_timeout':
      return upgradeWizardText('codeUpgradePreflightTimeout', language);
    case 'upgrade_preflight_failed':
      return upgradeWizardText('codeUpgradePreflightFailed', language);
    case 'upgrade_operation_unreadable':
      return upgradeWizardText('codeUpgradeOperationUnreadable', language);
    case 'upgrade_verify_not_applicable':
      return upgradeWizardText('codeUpgradeVerifyNotApplicable', language);
    case 'upgrade_verify_v2_running':
      return upgradeWizardText('codeUpgradeVerifyV2Running', language);
    case 'upgrade_verify_source_unavailable':
      return upgradeWizardText('codeUpgradeVerifySourceUnavailable', language);
    case 'upgrade_verify_source_mismatch':
      return upgradeWizardText('codeUpgradeVerifySourceMismatch', language);
    case 'upgrade_verify_reconciliation_invalid':
      return upgradeWizardText('codeUpgradeVerifyReconciliationInvalid', language);
    case 'upgrade_verify_reconciliation_unacknowledged':
      return upgradeWizardText('codeUpgradeVerifyReconciliationUnacknowledged', language);
    case 'upgrade_verify_domains_incomplete':
      return upgradeWizardText('codeUpgradeVerifyDomainsIncomplete', language);
    case 'upgrade_verify_binding_missing':
      return upgradeWizardText('codeUpgradeVerifyBindingMissing', language);
    case 'upgrade_verify_binding_drifted':
      return upgradeWizardText('codeUpgradeVerifyBindingDrifted', language);
    case 'upgrade_verify_connection_unproven':
      return upgradeWizardText('codeUpgradeVerifyConnectionUnproven', language);
    case 'upgrade_verify_conflict':
      return upgradeWizardText('codeUpgradeVerifyConflict', language);
    case 'upgrade_verify_failed':
      return upgradeWizardText('codeUpgradeVerifyFailed', language);
    case 'upgrade_request_invalid':
      return upgradeWizardText('codeUpgradeRequestInvalid', language);
    case 'invalid_access_prefix':
      return upgradeWizardText('codeInvalidAccessPrefix', language);
    case 'inference_prefix_required':
      return upgradeWizardText('codeInferencePrefixRequired', language);
    case 'external_inference_prefix_forbidden':
      return upgradeWizardText('codeExternalInferencePrefixForbidden', language);
    case 'shared_prefix_mismatch':
      return upgradeWizardText('codeSharedPrefixMismatch', language);
    case 'shared_prefix_confirmation_required':
      return upgradeWizardText('codeSharedPrefixConfirmationRequired', language);
    case 'prefix_overlap_forbidden':
      return upgradeWizardText('codePrefixOverlapForbidden', language);
    case 'reserved_prefix_collision':
      return upgradeWizardText('codeReservedPrefixCollision', language);
    case 'upgrade_access_prefix_required':
      return upgradeWizardText('codeUpgradeAccessPrefixRequired', language);
    case 'upgrade_prefixes_already_committed':
      return upgradeWizardText('codeUpgradePrefixesAlreadyCommitted', language);
    case 'upgrade_restart_required':
      return upgradeWizardText('codeUpgradeRestartRequired', language);
    case 'routing_conflict':
      return upgradeWizardText('codeRoutingConflict', language);
    case 'routing_verification_failed':
      return upgradeWizardText('codeRoutingVerificationFailed', language);
    default:
      return upgradeWizardText('codeUnknown', language);
  }
}

// An unknown stage is shown verbatim by the caller; this returns '' so the
// caller never renders a guessed label.
export function stageText(stage: string, language = fullWebLanguage()): string {
  switch (stage) {
    case 'preflight':
      return upgradeWizardText('stagePreflight', language);
    case 'copy_in':
      return upgradeWizardText('stageCopyIn', language);
    case 'reconcile':
      return upgradeWizardText('stageReconcile', language);
    case 'cpa_import':
      return upgradeWizardText('stageCpaImport', language);
    case 'external_connect':
      return upgradeWizardText('stageExternalConnect', language);
    case 'verify':
      return upgradeWizardText('stageVerify', language);
    case 'complete':
      return upgradeWizardText('stageComplete', language);
    case 'failed':
      return upgradeWizardText('stageFailed', language);
    case 'rolled_back':
      return upgradeWizardText('stageRolledBack', language);
    default:
      return '';
  }
}

export function domainText(domain: string, language = fullWebLanguage()): string {
  switch (domain) {
    case 'source_copy':
      return upgradeWizardText('domainSourceCopy', language);
    case 'usage_events':
      return upgradeWizardText('domainUsageEvents', language);
    case 'retained_projection':
      return upgradeWizardText('domainRetainedProjection', language);
    case 'pricing':
      return upgradeWizardText('domainPricing', language);
    case 'coverage':
      return upgradeWizardText('domainCoverage', language);
    case 'identity':
      return upgradeWizardText('domainIdentity', language);
    case 'cpa_config':
      return upgradeWizardText('domainCpaConfig', language);
    default:
      return domain;
  }
}

export function nextActionText(action: string, language = fullWebLanguage()): string {
  return action ? stageText(action, language) : '';
}

// The reconciliation list travels as a bounded window with its own total. These
// two sentences keep the recorded total and the shown length separate, so a cut
// list can never read as the whole history.
export function reconciliationTotalText(total: number, language = fullWebLanguage()): string {
  return upgradeWizardText('resultReconciliationTotal', language).replace('{count}', String(total));
}

export function reconciliationBoundText(shown: number, language = fullWebLanguage()): string {
  return upgradeWizardText('resultReconciliationBound', language).replace('{shown}', String(shown));
}
