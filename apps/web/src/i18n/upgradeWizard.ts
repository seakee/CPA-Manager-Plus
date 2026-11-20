import { fullWebLanguage } from './fullweb';

// Upgrade copy is shared by manual recovery and the script-prepared execution UI.
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
  preflightPendingTitle: 'Source evidence needs review',
  preflightPendingHelp: 'The source release was not recorded. Resume the upgrade script or follow the recovery guide; this page does not declare a release.',
  preflightPendingTail: 'The check stays pending here; it is not a failure.',
  preflightBlockedTitle: 'Preflight blocked the upgrade',
  preflightBlockedHelp:
    'Resolve each named condition on the host, then run the preflight again. Nothing has been copied.',
  preflightReadyTitle: 'The source passed preflight',
  preflightReadyHelp:
    'Every check passed. Nothing has been copied yet; the next step describes what the operation will do.',
  preflightReadyPendingTitle: 'The source passed preflight (pending copy-in confirmation)',
  preflightReadyPendingHelp:
    'No blocking issues were found, but pending source evidence must be resolved by the preparation flow before migration.',
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
  codeUpgradeDiskProbeFailed:
    'Available disk space for the copy could not be measured. Check the mounted target volume and its permissions on the host, then try again.',
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
  codeUpgradeOperationInvalid:
    'The recorded upgrade operation row is incomplete or contradictory. Restore from backup or inspect the database state before continuing.',
  codeUpgradeAuthorityUnavailable:
    'The canonical v2 upgrade authority could not be verified right now. Wait for the restarted service to finish loading or inspect the Manager runtime.',
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
  progressHelp: 'Continue the next recorded step below. Progress comes from the operation and its executor, not from accepted requests.',
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
    'The verification gate passed. Activate v2 below if the access routes are not ready yet.',
  verifyAlready: 'This position was already recorded; nothing was written this time.',
  verifyFailedTitle: 'The verification recorded nothing',
  verifyTargetExternal: 'Target: an External CPA, administered by its owner',
  verifyTargetManaged: 'Target: the CPA this Manager owns',
  verifyReasonLabel: 'Recorded reason',
  cpaImportReadyTitle: 'CPA engine is ready for import',
  cpaImportReadyHelp: 'The pinned CPA engine is active. Continue with CPA import when the executor is available.',
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
    'Enter a canonical path starting with /, without trailing slash, spaces, percent encoding, or dot segments.',
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
  codeUpgradeExecutionBusy: 'Another upgrade execution step is already running. Wait for it to finish and refresh status.',
  codeUpgradeExecutionConflict: 'This execution action conflicts with the recorded upgrade stage or source state.',
  codeUpgradeExecutionRequired: 'The upgrade executor is required for this step but is unavailable here.',
  codeUpgradeExecutionFailed: 'The background upgrade step failed before reaching the next stage.',
  codeUpgradeWorkerUnavailable: 'The upgrade executor is temporarily unreachable. Keep this page open and read status again before switching to recovery.',
  codeUpgradeActivationNotReady: 'Verification has not completed yet. Finish prefix configuration and verification before activating v2.',
  codeUpgradeActivationTimeout: 'The final canonical access routes were not confirmed within two minutes after activation.',
  codeUpgradeRestartDisabled: 'Automatic container restart is disabled or unavailable from the Web UI. Restart the container from its host or Docker runtime.',
  codeUpgradeRestartFailed: 'The Manager failed to trigger a controlled container restart. Check the runtime supervisor or restart the container manually.',
  codeUpgradeBootstrapRetired: 'The temporary bootstrap entry has been retired. Read status again to verify the canonical v2 route.',
  codeUpgradeTargetOwnershipFailed: 'The target volume ownership check or repair failed before restart. Fix directory permissions and try again.',
  codeUpgradePreflightBlocked: 'Preflight checks blocked automatic execution. Review the preflight blockers before continuing.',
  codeUpgradeStoreLocked: 'The upgrade operation store is locked by another process. Wait and try again.',
  codeUpgradeTargetLocked: 'The v2 target database is locked by another process.',
  codeUpgradeTargetModified: 'The copied v2 target files changed unexpectedly outside the recorded operation.',
  codeUpgradeSourceChanged: 'The 1.x source files changed since the operation started. Reopen recovery with the current source.',
  codeUpgradeTargetConflict: 'The target directory already contains conflicting v2 state.',
  codeUpgradeCopyInV2Running: 'Stop the running v2 Manager before copying or modifying this operation.',
  codeUpgradeSnapshotFailed: 'Creating the rollback snapshot failed. Original 1.x data was left unchanged.',
  codeUpgradeStoreSchemaOutdated: 'The target database schema version does not match this v2 binary.',
  codeUpgradeReconcileMismatch: 'Retained history reconciliation found date mismatches or parity gaps that require explicit confirmation.',
  codeUpgradeReconcileTargetIncomplete: 'Copied target tables are incomplete for reconciliation.',
  codeUpgradeReconcileNotCopiedIn: 'Reconciliation cannot run before source copy-in has completed.',
  codeUpgradeReconcileStageClosed: 'Reconciliation is no longer open at the current upgrade stage.',
  codeUpgradeReconcileRecordIncomplete: 'The recorded upgrade operation is missing required reconciliation metadata.',
  codeUpgradeReconcileNothingCompared: 'No usage history tables were available to compare during reconciliation.',
  codeUpgradeCpaSourceUnavailable: 'The 1.x CPA source configuration tree is unreadable or missing.',
  codeUpgradeCpaSourceMissing: 'No readable 1.x CPA source files were found at the configured legacy root.',
  codeUpgradeCpaConfigInvalid: 'The 1.x CPA configuration could not be parsed safely.',
  codeUpgradeCpaVersionRequired: 'A valid CPA v8.x version is required before Managed import can proceed.',
  codeUpgradeCpaVersionUnsupported: 'The detected or pinned CPA version is not supported by this upgrade.',
  codeUpgradeCpaClientKeysMissing: 'Client API keys could not be resolved from the source installation.',
  codeUpgradeCpaPluginChoiceRequired: 'Choose explicitly whether imported CPA plugins should be enabled or kept disabled.',
  codeUpgradeCpaStageConflict: 'Managed CPA import cannot run at the current upgrade stage.',
  codeUpgradeCpaDestinationUnready: 'The Managed CPA runtime directory or pinned binary is not ready yet.',
  codeUpgradeCpaImportUnproven: 'Managed CPA import wrote configuration, but the managed runtime connection could not be verified.',
  codeUpgradeCpaImportUnavailable: 'Managed CPA import is unavailable in this container environment.',
  codeUpgradeCpaSourceLocked: 'The 1.x CPA source tree is still locked by a live writer.',
  codeUpgradeCpaSourceRootMismatch: 'The source root does not match the recorded upgrade operation.',
  codeUpgradeCpaDestinationRequestChanged: 'The Managed CPA import options changed relative to the recorded operation.',
  codeUpgradeCpaImportTargetForeign: 'Managed CPA import refuses to bind to an unrelated external CPA instance.',
  codeUpgradeCpaImportTargetUnreachable: 'The Managed CPA endpoint did not respond during verification.',
  codeUpgradeCpaConnectionConflict: 'The CPA connection settings conflict with the recorded target mode.',
  codeUpgradeCpaDestinationKeyRequired: 'A destination CPA management key is required before verifying Managed import.',
  codeUpgradeExternalConnectionRequired: 'No usable external CPA connection was found in the source. Enter both an external CPA base URL and management key.',
  codeExternalCpaVersionUnsupported: 'The External CPA instance reported an unsupported version; CPA v8.x is required.',
  codeExternalCpaConfigDocumentInvalid: 'The External CPA configuration response is invalid or incomplete.',
  codeExternalManagementKeyRejected: 'The External CPA management key was rejected by the remote endpoint.',
  codeExternalTargetMoved: 'The External CPA endpoint redirected or moved to a different location.',
  codeExternalCpaAuthorityUnavailable: 'The External CPA endpoint is currently unreachable from this Manager.',
  codeUpgradeExternalConnectNotApplicable: 'External CPA connection is not applicable at the current upgrade stage.',
  codeUpgradeExternalConnectV2Running: 'Stop the running v2 Manager before modifying the External CPA connection.',
  codeExternalTargetUnavailable: 'The configured External CPA endpoint is unreachable.',
  codeUpgradeExternalConnectConflict:
    'The External CPA connection binding or management key conflicted with a concurrent update. Re-check the External CPA target and try again.',
  codeUpgradeSourceAlreadyV2:
    'The mounted source database already contains v2 tables and cannot be upgraded as a 1.x source.',
  codeUpgradeTargetLayoutMismatch:
    'The target v2 directory layout or data-key configuration does not match where the upgrade reads and writes copied state.',
  codeUpgradeSourceVersionUndeclared:
    'The 1.x source release version could not be recovered from the database and was not declared.',
  codeUpgradeExecutionCanceled:
    'The running upgrade step was interrupted or canceled before completion. Read the current status and try again.',
  codeUpgradeV2Running:
    'A v2 Manager instance is already running against the target data directory. Stop it before continuing the upgrade.',
  codeUpgradeStoreUnavailable:
    'The upgrade operation store cannot be opened right now. Check the Manager data directory and permissions on the host.',
  codeUpgradeVerificationFailed:
    'Upgrade verification did not pass. Review the verification requirements and try again.',
  codeDatabaseUnavailable:
    'The Manager database is temporarily unavailable. Check the host volume and try again.',
  codeUpgradeMigrateNotCopiedIn:
    'Migration cannot proceed before source copy-in has completed.',
  modeBadgeManaged: 'Managed CPA',
  modeBadgeExternal: 'External CPA',
  externalDirectBadge: 'Direct inference base',
  sourceKindLegacyManager: 'CPAMP 1.x Manager',
  sourceKindUpgradedLegacy: 'Copied 1.x source',
  techDetails: 'Raw diagnostic parameters',
  checkSummaryOwnershipPass: 'Idle & read-only',
  checkSummaryVersionPass: 'Supported release',
  checkSummaryFingerprintPass: 'Source ~{size}',
  checkSummaryFingerprintPlain: 'SHA-256 recorded',
  checkSummaryDiskPass: 'Needs {required} · {available} free',
  checkSummaryCpaManaged: 'Managed CPA',
  checkSummaryCpaExternal: 'External CPA',
  checkSummaryParityPass: 'Matched, 0 gaps',
  progressRefreshNow: 'Check status',
  confirmAutoTitle: 'Automatic data & key migration',
  confirmAutoHelp:
    'Usage history, model pricing, client API keys, and supported CPA v8 settings migrate automatically.',
  confirmEdgeToggle: 'Edge cases that require manual review',
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
  slimEngineHelp: 'This slim image has no bundled CPA binary. Prepare the pinned engine here before importing the source CPA configuration.',
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
  preflightPendingTitle: '来源证据需要核对',
  preflightPendingHelp: '来源没有记录版本。请继续升级脚本或按恢复指南处理；本页不会代为声明来源版本。',
  preflightPendingTail: '此检查在本页保持待定状态，并不是失败。',
  preflightBlockedTitle: '预检阻断了升级',
  preflightBlockedHelp: '请在宿主机上逐项解决下列问题后重新预检。当前尚未复制任何数据。',
  preflightReadyTitle: '预检全部通过',
  preflightReadyHelp: '未发现阻断项，可继续下一步。',
  preflightReadyPendingTitle: '预检通过，可继续升级',
  preflightReadyPendingHelp: '未发现阻断项，但迁移前仍需由准备流程处理待定的来源证据。',
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
  codeUpgradeDiskProbeFailed: '无法检测复制所需的可用磁盘空间。请在宿主机上检查目标卷挂载与权限后重试。',
  codeUpgradeCredentialTimeout: '来源未在限定时间内响应，无法验证管理员密钥。请在宿主机空闲时重试。',
  codeUpgradeMountTimeout:
    '已挂载的 1.x 卷未在限定时间内响应，因此未能检查。请在宿主机可响应时重试；本次未从该卷读取任何内容，也未写入任何内容。',
  codeUpgradeLegacyRootMissing: '未挂载 1.x 数据目录。请只读挂载后重试。',
  codeUpgradeLegacyRootUnusable: '已挂载的 1.x 数据目录不可用。',
  codeUpgradePreflightTimeout: '预检未在限定时间内完成。请在宿主机空闲时重新预检。',
  codeUpgradePreflightFailed: '预检在报告来源之前就失败了。',
  codeUpgradeOperationUnreadable: '无法读取操作记录。请在宿主机上检查 Manager 数据库。',
  codeUpgradeOperationInvalid: '已记录的升级操作记录不完整或存在自相矛盾。继续前请核查数据库状态或从备份恢复。',
  codeUpgradeAuthorityUnavailable: '当前无法验证规范 v2 升级路由权限。请等待重启后的服务完成加载，或检查 Manager 运行状态。',
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
  progressHelp: '请依据记录继续下方步骤；进度由操作记录和执行器状态决定，受理请求不代表完成。',
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
  resultCompleteHelp: '记录中的升级操作已完成，v2 Manager 现已提供正常访问入口。',
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
  verifyComplete: '最终验证已通过。若访问路由尚未就绪，请在下方激活 v2。',
  verifyAlready: '该位置此前已记录，本次没有写入任何内容。',
  verifyFailedTitle: '验证未记录任何结论',
  verifyTargetExternal: '目标：连接外部独立运维的 CPA',
  verifyTargetManaged: '目标：内置受管 CPA',
  verifyReasonLabel: '已记录的原因',
  cpaImportReadyTitle: 'CPA 引擎已就绪，可执行导入',
  cpaImportReadyHelp: '固定版本 CPA 引擎已激活；执行器可用时可继续导入 CPA 配置。',
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
  codeUpgradeExecutionBusy: '已有升级执行步骤正在运行，请等待其完成并刷新状态。',
  codeUpgradeExecutionConflict: '当前执行动作与已记录的升级阶段或来源状态冲突。',
  codeUpgradeExecutionRequired: '此步骤需要升级执行器，但当前环境不可用。',
  codeUpgradeExecutionFailed: '后台升级步骤未能顺利推进至下一阶段。',
  codeUpgradeWorkerUnavailable: '升级执行器暂时不可达。请先保持本页并重新读取状态，再决定是否切回脚本恢复。',
  codeUpgradeActivationNotReady: '升级验证尚未完成，请先保存访问前缀并完成验证后再激活 v2。',
  codeUpgradeActivationTimeout: '激活后两分钟内未确认最终规范访问路由。',
  codeUpgradeRestartDisabled: 'Web 端已禁用或不支持自动重启容器，请通过宿主机或 Docker 运行时手动重启容器。',
  codeUpgradeRestartFailed: 'Manager 请求受控重启容器失败，请检查运行时守护进程或手动重启容器。',
  codeUpgradeBootstrapRetired: '临时引导入口已停用。请重新读取以检查规范 v2 路由状态。',
  codeUpgradeTargetOwnershipFailed: '重启前目标卷属主校验或修复失败，请检查目录权限后重试。',
  codeUpgradePreflightBlocked: '只读预检存在阻断项，无法自动执行升级步骤。',
  codeUpgradeStoreLocked: '升级状态库正被其他进程锁定，请稍后重试。',
  codeUpgradeTargetLocked: 'v2 目标数据库正被其他进程锁定。',
  codeUpgradeTargetModified: '已复制的 v2 目标文件在记录之外发生了非预期改动。',
  codeUpgradeSourceChanged: '1.x 来源文件在操作开始后发生变化，请以最新来源重新检查或恢复。',
  codeUpgradeTargetConflict: '目标目录已存在冲突的 v2 状态。',
  codeUpgradeCopyInV2Running: '请先停止正在运行的 v2 Manager，再复制或修改该操作。',
  codeUpgradeSnapshotFailed: '创建回滚快照失败，1.x 原始数据未作任何更改。',
  codeUpgradeStoreSchemaOutdated: '目标数据库表结构版本与当前 v2 二进制不匹配。',
  codeUpgradeReconcileMismatch: '留存历史对账发现日期差异或一致性缺口，需要逐项明确确认。',
  codeUpgradeReconcileTargetIncomplete: '已复制的目标数据表不完整，无法执行对账。',
  codeUpgradeReconcileNotCopiedIn: '尚未完成来源数据复制（copy-in），无法执行对账。',
  codeUpgradeReconcileStageClosed: '当前升级阶段已关闭对账入口。',
  codeUpgradeReconcileRecordIncomplete: '已记录的升级操作缺少对账所需元数据。',
  codeUpgradeReconcileNothingCompared: '未发现可供比对的历史用量表。',
  codeUpgradeCpaSourceUnavailable: '1.x 来源 CPA 配置目录不可读或缺失。',
  codeUpgradeCpaSourceMissing: '在配置的旧版根目录中未找到可读的 1.x CPA 源文件。',
  codeUpgradeCpaConfigInvalid: '1.x CPA 配置文件无法安全解析。',
  codeUpgradeCpaVersionRequired: '导入内置 Managed CPA 前需要有效的 CPA v8.x 版本。',
  codeUpgradeCpaVersionUnsupported: '检测到或指定的 CPA 版本不受本次升级支持。',
  codeUpgradeCpaClientKeysMissing: '无法从来源安装中解析出 Client API Keys。',
  codeUpgradeCpaPluginChoiceRequired: '必须明确选择启用或关闭导入的 CPA 插件。',
  codeUpgradeCpaStageConflict: '当前升级阶段不允许执行 Managed CPA 导入。',
  codeUpgradeCpaDestinationUnready: 'Managed CPA 运行时目录或固定版本二进制尚未就绪。',
  codeUpgradeCpaImportUnproven: 'Managed CPA 配置已写入，但未能验证本地托管运行时连通性。',
  codeUpgradeCpaImportUnavailable: '当前容器环境不支持内置 Managed CPA 导入。',
  codeUpgradeCpaSourceLocked: '1.x CPA 源目录仍被旧写入进程锁定。',
  codeUpgradeCpaSourceRootMismatch: '来源根目录与已记录的升级操作不一致。',
  codeUpgradeCpaDestinationRequestChanged: '本次 Managed CPA 导入参数与已记录操作不一致。',
  codeUpgradeCpaImportTargetForeign: 'Managed CPA 导入拒绝绑定到非本机托管的外部 CPA 实例。',
  codeUpgradeCpaImportTargetUnreachable: '验证期间无法连接到本地 Managed CPA 端点。',
  codeUpgradeCpaConnectionConflict: 'CPA 连接设置与已记录的目标模式冲突。',
  codeUpgradeCpaDestinationKeyRequired: '验证 Managed CPA 导入前需要目标 CPA 管理密钥。',
  codeUpgradeExternalConnectionRequired: '来源中未找到可用的外部 CPA 连接。请同时输入外部 CPA 基础地址与管理密钥。',
  codeExternalCpaVersionUnsupported: '外部 CPA 实例版本不受支持，需使用 CPA v8.x。',
  codeExternalCpaConfigDocumentInvalid: '外部 CPA 返回的配置响应无效或不完整。',
  codeExternalManagementKeyRejected: '外部 CPA 端点拒绝了提供的管理密钥。',
  codeExternalTargetMoved: '外部 CPA 端点发生了重定向或地址变更。',
  codeExternalCpaAuthorityUnavailable: '当前 Manager 无法访问该外部 CPA 端点。',
  codeUpgradeExternalConnectNotApplicable: '当前升级阶段不允许连接外部 CPA。',
  codeUpgradeExternalConnectV2Running: '请先停止正在运行的 v2 Manager，再修改外部 CPA 连接。',
  codeExternalTargetUnavailable: '已配置的外部 CPA 端点当前不可达。',
  codeUpgradeExternalConnectConflict: '外部 CPA 连接绑定或管理密钥与并发更新发生冲突。请重新核对外部 CPA 目标后再试。',
  codeUpgradeSourceAlreadyV2: '挂载的来源数据库已包含 v2 数据表，不能作为 1.x 来源执行升级。',
  codeUpgradeTargetLayoutMismatch: '目标 v2 目录布局或数据密钥配置与升级服务读写复制状态的位置不匹配。',
  codeUpgradeSourceVersionUndeclared: '无法从来源数据库中识别 1.x 版本号，且尚未声明来源版本。',
  codeUpgradeExecutionCanceled: '正在执行的升级步骤在完成前被中断或取消。请读取当前状态后重试。',
  codeUpgradeV2Running: '目标数据目录上已有正在运行的 v2 Manager 实例。请先停止该进程再继续升级。',
  codeUpgradeStoreUnavailable: '当前无法打开升级操作状态库。请在宿主机上检查 Manager 数据目录与权限。',
  codeUpgradeVerificationFailed: '升级核验未通过。请核对验证条件后重试。',
  codeDatabaseUnavailable: 'Manager 数据库当前不可用，请检查宿主机存储卷后重试。',
  codeUpgradeMigrateNotCopiedIn: '尚未完成来源数据复制（copy-in），无法继续执行迁移步骤。',
  modeBadgeManaged: '内置受管 CPA',
  modeBadgeExternal: '连接外部 CPA',
  externalDirectBadge: '同时作为推理直连基址',
  sourceKindLegacyManager: 'CPAMP 1.x 管理端',
  sourceKindUpgradedLegacy: 'CPAMP 1.x',
  techDetails: '查看原始诊断参数',
  checkSummaryOwnershipPass: '只读静止',
  checkSummaryVersionPass: '版本受支持',
  checkSummaryFingerprintPass: '源数据约 {size}',
  checkSummaryFingerprintPlain: 'SHA-256 已生成',
  checkSummaryDiskPass: '需 {required} · 可用 {available}',
  checkSummaryCpaManaged: '内置受管 CPA',
  checkSummaryCpaExternal: '连接外部 CPA',
  checkSummaryParityPass: '核对一致，无断档',
  progressRefreshNow: '立即检查进度',
  confirmAutoTitle: '常规数据与密钥自动迁移',
  confirmAutoHelp: '历史用量、模型定价、客户端 API Key 及受支持的 CPA v8 配置均自动迁移。',
  confirmEdgeToggle: '查看需人工核对的边缘例外情况',
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
  slimEngineHelp: '当前精简镜像未附带 CPA 二进制文件，请先在此准备固定版本引擎，再导入来源 CPA 配置。',
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
  preflightPendingTitle: '來源證據需要核對',
  preflightPendingHelp: '來源沒有記錄版本。請繼續升級指令碼或依復原指南處理；本頁不會代為宣告來源版本。',
  preflightPendingTail: '此檢查在本頁保持待定狀態，並不是失敗。',
  preflightBlockedTitle: '預檢阻斷了升級',
  preflightBlockedHelp: '請在主機上逐項解決下列問題後重新預檢。目前尚未複製任何資料。',
  preflightReadyTitle: '預檢全部通過',
  preflightReadyHelp: '未發現阻斷項目，可繼續下一步。',
  preflightReadyPendingTitle: '預檢通過，可繼續升級',
  preflightReadyPendingHelp: '未發現阻斷項目，但移轉前仍需由準備流程處理待定的來源證據。',
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
  codeUpgradeDiskProbeFailed: '無法偵測複製所需的可用磁碟空間。請在主機上檢查目標磁碟區掛載與權限後重試。',
  codeUpgradeCredentialTimeout: '來源未在限定時間內回應，無法驗證管理員金鑰。請在主機閒置時重試。',
  codeUpgradeMountTimeout:
    '已掛載的 1.x 磁碟區未在限定時間內回應，因此未能檢查。請在主機可回應時重試；本次未從該磁碟區讀取任何內容，也未寫入任何內容。',
  codeUpgradeLegacyRootMissing: '未掛載 1.x 資料目錄。請以唯讀方式掛載後重試。',
  codeUpgradeLegacyRootUnusable: '已掛載的 1.x 資料目錄不可用。',
  codeUpgradePreflightTimeout: '預檢未在限定時間內完成。請在主機閒置時重新預檢。',
  codeUpgradePreflightFailed: '預檢在回報來源之前就失敗了。',
  codeUpgradeOperationUnreadable: '無法讀取操作記錄。請在主機上檢查 Manager 資料庫。',
  codeUpgradeOperationInvalid: '已記錄的升級操作紀錄不完整或存在自相矛盾。繼續前請核查資料庫狀態或從備份復原。',
  codeUpgradeAuthorityUnavailable: '目前無法驗證規範 v2 升級路由權限。請等待重啟後的服務完成載入，或檢查 Manager 執行狀態。',
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
  progressHelp: '請依據記錄繼續下方步驟；進度由操作記錄及執行器狀態決定，受理請求不代表完成。',
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
  resultCompleteHelp: '記錄中的升級操作已完成，v2 Manager 現已提供正常存取入口。',
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
  verifyComplete: '最終驗證已通過。若存取路由尚未就緒，請在下方啟用 v2。',
  verifyAlready: '該位置先前已記錄，本次沒有寫入任何內容。',
  verifyFailedTitle: '驗證未記錄任何結論',
  verifyTargetExternal: '目標：連接外部獨立維運的 CPA',
  verifyTargetManaged: '目標：內建受管 CPA',
  verifyReasonLabel: '已記錄的原因',
  cpaImportReadyTitle: 'CPA 引擎已就緒，可執行匯入',
  cpaImportReadyHelp: '固定版本 CPA 引擎已啟用；執行器可用時可繼續匯入 CPA 設定。',
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
  codeUpgradeExecutionBusy: '已有升級執行步驟正在執行，請等待其完成並重新整理狀態。',
  codeUpgradeExecutionConflict: '目前執行動作與已記錄的升級階段或來源狀態衝突。',
  codeUpgradeExecutionRequired: '此步驟需要升級執行器，但目前環境無法使用。',
  codeUpgradeExecutionFailed: '背景升級步驟未能順利推進至下一階段。',
  codeUpgradeWorkerUnavailable: '升級執行器暫時無法連線。請先保留本頁並重新讀取狀態，再決定是否切回指令碼復原。',
  codeUpgradeActivationNotReady: '升級驗證尚未完成，請先儲存存取前綴並完成驗證後再啟用 v2。',
  codeUpgradeActivationTimeout: '啟用後兩分鐘內未確認最終規範存取路由。',
  codeUpgradeRestartDisabled: 'Web 端已停用或不支援自動重新啟動容器，請透過主機或 Docker 執行環境手動重啟容器。',
  codeUpgradeRestartFailed: 'Manager 請求受控重新啟動容器失敗，請檢查執行環境監控程序或手動重啟容器。',
  codeUpgradeBootstrapRetired: '暫時引導入口已停用。請重新讀取以檢查規範 v2 路由狀態。',
  codeUpgradeTargetOwnershipFailed: '重新啟動前目標卷擁有者檢查或修復失敗，請檢查目錄權限後重試。',
  codeUpgradePreflightBlocked: '唯讀預檢存在阻斷項目，無法自動執行升級步驟。',
  codeUpgradeStoreLocked: '升級狀態庫正被其他程序鎖定，請稍後重試。',
  codeUpgradeTargetLocked: 'v2 目標資料庫正被其他程序鎖定。',
  codeUpgradeTargetModified: '已複製的 v2 目標檔案在記錄之外發生非預期變更。',
  codeUpgradeSourceChanged: '1.x 來源檔案在操作開始後有所變更，請以最新來源重新檢查或復原。',
  codeUpgradeTargetConflict: '目標目錄已存在衝突的 v2 狀態。',
  codeUpgradeCopyInV2Running: '請先停止正在執行的 v2 Manager，再複製或修改該操作。',
  codeUpgradeSnapshotFailed: '建立回滾快照失敗，1.x 原始資料未作任何更動。',
  codeUpgradeStoreSchemaOutdated: '目標資料庫結構版本與目前 v2 二進位檔不相符。',
  codeUpgradeReconcileMismatch: '留存歷史對帳發現日期差異或一致性缺口，需要逐項明確確認。',
  codeUpgradeReconcileTargetIncomplete: '已複製的目標資料表不完整，無法執行對帳。',
  codeUpgradeReconcileNotCopiedIn: '尚未完成來源資料複製（copy-in），無法執行對帳。',
  codeUpgradeReconcileStageClosed: '目前升級階段已關閉對帳入口。',
  codeUpgradeReconcileRecordIncomplete: '已記錄的升級操作缺少對帳所需中繼資料。',
  codeUpgradeReconcileNothingCompared: '未發現可供比對的歷史用量表。',
  codeUpgradeCpaSourceUnavailable: '1.x 來源 CPA 設定目錄無法讀取或遺失。',
  codeUpgradeCpaSourceMissing: '在設定的舊版根目錄中未找到可讀取的 1.x CPA 來源檔案。',
  codeUpgradeCpaConfigInvalid: '1.x CPA 設定檔案無法安全解析。',
  codeUpgradeCpaVersionRequired: '匯入內建 Managed CPA 前需要有效的 CPA v8.x 版本。',
  codeUpgradeCpaVersionUnsupported: '偵測到或指定的 CPA 版本不受本次升級支援。',
  codeUpgradeCpaClientKeysMissing: '無法從來源安裝中解析出 Client API Keys。',
  codeUpgradeCpaPluginChoiceRequired: '必須明確選擇啟用或停用匯入的 CPA 外掛。',
  codeUpgradeCpaStageConflict: '目前升級階段不允許執行 Managed CPA 匯入。',
  codeUpgradeCpaDestinationUnready: 'Managed CPA 執行環境目錄或固定版本二進位檔尚未就緒。',
  codeUpgradeCpaImportUnproven: 'Managed CPA 設定已寫入，但未能驗證本機託管執行環境連線能力。',
  codeUpgradeCpaImportUnavailable: '目前容器環境不支援內建 Managed CPA 匯入。',
  codeUpgradeCpaSourceLocked: '1.x CPA 來源目錄仍被舊寫入程序鎖定。',
  codeUpgradeCpaSourceRootMismatch: '來源根目錄與已記錄的升級操作不一致。',
  codeUpgradeCpaDestinationRequestChanged: '本次 Managed CPA 匯入參數與已記錄操作不一致。',
  codeUpgradeCpaImportTargetForeign: 'Managed CPA 匯入拒絕繫結至非本機託管的外部 CPA 執行個體。',
  codeUpgradeCpaImportTargetUnreachable: '驗證期間無法連線至本機 Managed CPA 端點。',
  codeUpgradeCpaConnectionConflict: 'CPA 連線設定與已記錄的目標模式衝突。',
  codeUpgradeCpaDestinationKeyRequired: '驗證 Managed CPA 匯入前需要目標 CPA 管理金鑰。',
  codeUpgradeExternalConnectionRequired: '來源中未找到可用的外部 CPA 連線。請同時輸入外部 CPA 基礎網址與管理金鑰。',
  codeExternalCpaVersionUnsupported: '外部 CPA 執行個體版本不受支援，需使用 CPA v8.x。',
  codeExternalCpaConfigDocumentInvalid: '外部 CPA 傳回的設定回應無效或不完整。',
  codeExternalManagementKeyRejected: '外部 CPA 端點拒絕了提供的管理金鑰。',
  codeExternalTargetMoved: '外部 CPA 端點發生重導向或位址變更。',
  codeExternalCpaAuthorityUnavailable: '目前 Manager 無法存取該外部 CPA 端點。',
  codeUpgradeExternalConnectNotApplicable: '目前升級階段不允許連線外部 CPA。',
  codeUpgradeExternalConnectV2Running: '請先停止正在執行的 v2 Manager，再修改外部 CPA 連線。',
  codeExternalTargetUnavailable: '已設定的外部 CPA 端點目前無法連線。',
  codeUpgradeExternalConnectConflict: '外部 CPA 連線繫結或管理金鑰與並行更新發生衝突。請重新核對外部 CPA 目標後再試。',
  codeUpgradeSourceAlreadyV2: '掛載的來源資料庫已包含 v2 資料表，不能作為 1.x 來源執行升級。',
  codeUpgradeTargetLayoutMismatch: '目標 v2 目錄配置或資料金鑰設定與升級服務讀寫複製狀態的位置不相符。',
  codeUpgradeSourceVersionUndeclared: '無法從來源資料庫中識別 1.x 版本號，且尚未宣告來源版本。',
  codeUpgradeExecutionCanceled: '正在執行的升級步驟在完成前被中斷或取消。請讀取目前狀態後重試。',
  codeUpgradeV2Running: '目標資料目錄上已有正在執行的 v2 Manager 執行個體。請先停止該程序再繼續升級。',
  codeUpgradeStoreUnavailable: '目前無法開啟升級操作狀態庫。請在主機上檢查 Manager 資料目錄與權限。',
  codeUpgradeVerificationFailed: '升級核驗未通過。請核對驗證條件後重試。',
  codeDatabaseUnavailable: 'Manager 資料庫目前無法使用，請檢查主機儲存磁碟區後重試。',
  codeUpgradeMigrateNotCopiedIn: '尚未完成來源資料複製（copy-in），無法繼續執行移轉步驟。',
  modeBadgeManaged: '內建受管 CPA',
  modeBadgeExternal: '連接外部 CPA',
  externalDirectBadge: '同時作為推論直連基址',
  sourceKindLegacyManager: 'CPAMP 1.x 管理端',
  sourceKindUpgradedLegacy: 'CPAMP 1.x',
  techDetails: '檢視原始診斷參數',
  checkSummaryOwnershipPass: '唯讀靜止',
  checkSummaryVersionPass: '版本受支援',
  checkSummaryFingerprintPass: '源資料約 {size}',
  checkSummaryFingerprintPlain: 'SHA-256 已產生',
  checkSummaryDiskPass: '需 {required} · 可用 {available}',
  checkSummaryCpaManaged: '內建受管 CPA',
  checkSummaryCpaExternal: '連接外部 CPA',
  checkSummaryParityPass: '核對一致，無斷檔',
  progressRefreshNow: '立即檢查進度',
  confirmAutoTitle: '常規資料與金鑰自動移轉',
  confirmAutoHelp: '歷史用量、模型定價、用戶端 API 金鑰及受支援的 CPA v8 設定均自動移轉。',
  confirmEdgeToggle: '檢視需人工核對的邊緣例外情況',
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
  slimEngineHelp: '目前精簡映像未附帶 CPA 二進位檔，請先在此準備固定版本引擎，再匯入來源 CPA 設定。',
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
  preflightPendingTitle: 'Требуется проверка данных источника',
  preflightPendingHelp: 'Версия источника не записана. Продолжите скрипт обновления или следуйте руководству восстановления; страница не объявляет версию.',
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
    'Блокирующих проблем нет, но до переноса подготовка должна разрешить ожидающие данные источника.',
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
  codeUpgradeDiskProbeFailed:
    'Не удалось определить свободное место на диске для копирования. Проверьте смонтированный целевой том и его права на хосте, затем повторите.',
  codeUpgradeCredentialTimeout:
    'Источник не ответил вовремя, поэтому ключ администратора не проверен. Повторите, когда хост свободен.',
  codeUpgradeMountTimeout:
    'Смонтированный том 1.x не ответил вовремя, поэтому проверка не выполнялась. Повторите, когда хост ответит; из тома ничего не читалось и в него ничего не записывалось.',
  codeUpgradeLegacyRootMissing: 'Каталог данных 1.x не смонтирован. Смонтируйте его только для чтения и повторите.',
  codeUpgradeLegacyRootUnusable: 'Смонтированный каталог данных 1.x непригоден.',
  codeUpgradePreflightTimeout: 'Проверка не завершилась вовремя. Повторите её, когда хост свободен.',
  codeUpgradePreflightFailed: 'Проверка завершилась ошибкой, не успев описать источник.',
  codeUpgradeOperationUnreadable: 'Запись операции не читается. Проверьте базу Manager на хосте.',
  codeUpgradeOperationInvalid: 'Записанная строка операции обновления неполна или противоречива. Проверьте состояние базы данных или восстановите из резервной копии перед продолжением.',
  codeUpgradeAuthorityUnavailable: 'Не удалось подтвердить полномочия канонического маршрута обновления v2. Дождитесь завершения запуска службы или проверьте состояние Manager.',
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
  progressHelp: 'Продолжите следующий записанный шаг ниже. Прогресс определяется операцией и исполнителем, а не приёмом запроса.',
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
    'Итоговая проверка пройдена. Активируйте v2 ниже, если маршруты доступа ещё не готовы.',
  verifyAlready: 'Эта позиция уже была записана; на этот раз ничего не записано.',
  verifyFailedTitle: 'Проверка ничего не записала',
  verifyTargetExternal: 'Цель: внешний CPA, которым управляет его владелец',
  verifyTargetManaged: 'Цель: CPA, которым владеет этот Manager',
  verifyReasonLabel: 'Записанная причина',
  cpaImportReadyTitle: 'Движок CPA готов к импорту',
  cpaImportReadyHelp: 'Фиксированный движок CPA активен. Продолжите импорт, когда исполнитель доступен.',
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
  codeUpgradeExecutionBusy: 'Другой шаг обновления уже выполняется. Дождитесь его завершения и обновите статус.',
  codeUpgradeExecutionConflict: 'Это действие конфликтует с записанным этапом обновления или состоянием источника.',
  codeUpgradeExecutionRequired: 'Для этого шага нужен исполнитель обновления, но здесь он недоступен.',
  codeUpgradeExecutionFailed: 'Фоновый шаг обновления завершился ошибкой до перехода на следующий этап.',
  codeUpgradeWorkerUnavailable: 'Исполнитель обновления временно недоступен. Не закрывайте страницу и прочитайте статус снова перед возвратом к восстановлению.',
  codeUpgradeActivationNotReady: 'Проверка ещё не завершена. Сохраните префиксы и завершите проверку перед активацией v2.',
  codeUpgradeActivationTimeout: 'Итоговые канонические маршруты не подтверждены в течение двух минут после активации.',
  codeUpgradeRestartDisabled: 'Автоматический перезапуск контейнера отключён или недоступен из веб-интерфейса. Перезапустите контейнер со стороны хоста или Docker.',
  codeUpgradeRestartFailed: 'Не удалось выполнить управляемый перезапуск контейнера. Проверьте супервизор или перезапустите контейнер вручную.',
  codeUpgradeBootstrapRetired: 'Временный bootstrap-вход выведен из эксплуатации. Прочитайте статус снова для проверки канонического маршрута v2.',
  codeUpgradeTargetOwnershipFailed: 'Проверка или исправление прав доступа целевого тома перед перезапуском завершились ошибкой.',
  codeUpgradePreflightBlocked: 'Предварительная проверка заблокировала автоматическое выполнение.',
  codeUpgradeStoreLocked: 'Хранилище операций обновления заблокировано другим процессом. Повторите позже.',
  codeUpgradeTargetLocked: 'Целевая база данных v2 заблокирована другим процессом.',
  codeUpgradeTargetModified: 'Скопированные файлы v2 неожиданно изменились вне записанной операции.',
  codeUpgradeSourceChanged: 'Исходные файлы 1.x изменились после запуска операции. Откройте восстановление для актуального источника.',
  codeUpgradeTargetConflict: 'В целевом каталоге уже есть конфликтующее состояние v2.',
  codeUpgradeCopyInV2Running: 'Остановите запущенный v2 Manager перед копированием или изменением этой операции.',
  codeUpgradeSnapshotFailed: 'Не удалось создать снимок для отката. Исходные данные 1.x не изменены.',
  codeUpgradeStoreSchemaOutdated: 'Версия схемы целевой базы данных не совпадает с этим бинарным файлом v2.',
  codeUpgradeReconcileMismatch: 'Сверка сохранённой истории выявила расхождения по датам или целостности, требующие явного подтверждения.',
  codeUpgradeReconcileTargetIncomplete: 'Скопированные таблицы назначения неполны для сверки.',
  codeUpgradeReconcileNotCopiedIn: 'Сверка невозможна до завершения копирования источника (copy-in).',
  codeUpgradeReconcileStageClosed: 'Сверка больше не доступна на текущем этапе обновления.',
  codeUpgradeReconcileRecordIncomplete: 'В записанной операции не хватает метаданных для сверки.',
  codeUpgradeReconcileNothingCompared: 'Не найдено таблиц истории использования для сравнения при сверке.',
  codeUpgradeCpaSourceUnavailable: 'Каталог конфигурации исходного 1.x CPA недоступен или отсутствует.',
  codeUpgradeCpaSourceMissing: 'Читаемые файлы 1.x CPA не найдены в указанном каталоге legacy.',
  codeUpgradeCpaConfigInvalid: 'Конфигурацию 1.x CPA не удалось безопасно разобрать.',
  codeUpgradeCpaVersionRequired: 'Перед импортом Managed требуется действующая версия CPA v8.x.',
  codeUpgradeCpaVersionUnsupported: 'Обнаруженная или указанная версия CPA не поддерживается этим обновлением.',
  codeUpgradeCpaClientKeysMissing: 'Не удалось извлечь клиентские API-ключи из исходной установки.',
  codeUpgradeCpaPluginChoiceRequired: 'Явно выберите, следует ли включить или оставить выключенными импортируемые плагины CPA.',
  codeUpgradeCpaStageConflict: 'Импорт Managed CPA недопустим на текущем этапе обновления.',
  codeUpgradeCpaDestinationUnready: 'Каталог среды Managed CPA или закреплённый бинарный файл ещё не готовы.',
  codeUpgradeCpaImportUnproven: 'Конфигурация Managed CPA записана, но соединение с локальной средой подтвердить не удалось.',
  codeUpgradeCpaImportUnavailable: 'Импорт Managed CPA недоступен в этой среде контейнера.',
  codeUpgradeCpaSourceLocked: 'Исходный каталог 1.x CPA всё ещё заблокирован активным процессом записи.',
  codeUpgradeCpaSourceRootMismatch: 'Корневой путь источника не совпадает с записанной операцией обновления.',
  codeUpgradeCpaDestinationRequestChanged: 'Параметры импорта Managed CPA изменились относительно записанной операции.',
  codeUpgradeCpaImportTargetForeign: 'Импорт Managed CPA отказывается привязываться к стороннему внешнему CPA.',
  codeUpgradeCpaImportTargetUnreachable: 'Локальная точка доступа Managed CPA не ответила при проверке.',
  codeUpgradeCpaConnectionConflict: 'Параметры подключения CPA конфликтуют с выбранным целевым режимом.',
  codeUpgradeCpaDestinationKeyRequired: 'Для проверки импорта Managed CPA требуется ключ управления назначения.',
  codeUpgradeExternalConnectionRequired: 'В источнике не найдено пригодное подключение External CPA. Введите базовый URL и ключ управления External CPA.',
  codeExternalCpaVersionUnsupported: 'External CPA сообщил неподдерживаемую версию; требуется CPA v8.x.',
  codeExternalCpaConfigDocumentInvalid: 'Ответ конфигурации External CPA некорректен или неполон.',
  codeExternalManagementKeyRejected: 'Удалённый External CPA отклонил предоставленный ключ управления.',
  codeExternalTargetMoved: 'Адрес External CPA перенаправлен или перемещён.',
  codeExternalCpaAuthorityUnavailable: 'Точка доступа External CPA сейчас недоступна для Manager.',
  codeUpgradeExternalConnectNotApplicable: 'Подключение External CPA неприменимо на текущем этапе обновления.',
  codeUpgradeExternalConnectV2Running: 'Остановите работающий v2 Manager перед изменением подключения External CPA.',
  codeExternalTargetUnavailable: 'Настроенный внешний CPA сейчас недоступен.',
  codeUpgradeExternalConnectConflict:
    'Привязка или ключ управления External CPA конфликтуют с параллельным изменением. Проверьте параметры External CPA и повторите попытку.',
  codeUpgradeSourceAlreadyV2:
    'Смонтированная исходная база уже содержит таблицы v2 и не может обновляться как источник 1.x.',
  codeUpgradeTargetLayoutMismatch:
    'Структура целевого каталога v2 или конфигурация ключа данных не совпадает с путями чтения и записи состояния обновления.',
  codeUpgradeSourceVersionUndeclared:
    'Версию выпуска источника 1.x не удалось определить по базе данных, и она не была указана явно.',
  codeUpgradeExecutionCanceled:
    'Выполнявшийся шаг обновления был прерван или отменён до завершения. Проверьте текущий статус и повторите попытку.',
  codeUpgradeV2Running:
    'Экземпляр v2 Manager уже запущен в целевом каталоге данных. Остановите его перед продолжением обновления.',
  codeUpgradeStoreUnavailable:
    'Хранилище операций обновления сейчас недоступно. Проверьте каталог данных Manager и права доступа на хосте.',
  codeUpgradeVerificationFailed:
    'Проверка обновления не пройдена. Проверьте условия верификации и повторите попытку.',
  codeDatabaseUnavailable:
    'База данных Manager временно недоступна. Проверьте том на хосте и повторите попытку.',
  codeUpgradeMigrateNotCopiedIn:
    'Продолжение миграции невозможно до завершения копирования источника (copy-in).',
  modeBadgeManaged: 'Управляемый CPA',
  modeBadgeExternal: 'Внешний CPA',
  externalDirectBadge: 'Прямой базовый адрес инференса',
  sourceKindLegacyManager: 'CPAMP 1.x Manager',
  sourceKindUpgradedLegacy: 'CPAMP 1.x',
  techDetails: 'Исходные параметры диагностики',
  checkSummaryOwnershipPass: 'Только чтение, в покое',
  checkSummaryVersionPass: 'Версия поддерживается',
  checkSummaryFingerprintPass: 'Объём ~{size}',
  checkSummaryFingerprintPlain: 'SHA-256 записан',
  checkSummaryDiskPass: 'Нужно {required} · свободно {available}',
  checkSummaryCpaManaged: 'Управляемый CPA',
  checkSummaryCpaExternal: 'Внешний CPA',
  checkSummaryParityPass: 'Совпадает, без пропусков',
  progressRefreshNow: 'Проверить сейчас',
  confirmAutoTitle: 'Автоматический перенос данных и ключей',
  confirmAutoHelp: 'История использования, цены, ключи клиентов и поддерживаемые настройки CPA v8 переносятся автоматически.',
  confirmEdgeToggle: 'Особые случаи для ручной проверки',
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
  slimEngineHelp: 'Образ slim не содержит бинарный файл CPA. Подготовьте фиксированный движок здесь до импорта конфигурации источника CPA.',
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
    case 'upgrade_disk_probe_failed':
      return upgradeWizardText('codeUpgradeDiskProbeFailed', language);
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
    case 'upgrade_operation_invalid':
      return upgradeWizardText('codeUpgradeOperationInvalid', language);
    case 'upgrade_authority_unavailable':
      return upgradeWizardText('codeUpgradeAuthorityUnavailable', language);
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
    case 'upgrade_insufficient_disk_space':
      return upgradeWizardText('codeUpgradeDiskInsufficient', language);
    case 'upgrade_execution_busy':
      return upgradeWizardText('codeUpgradeExecutionBusy', language);
    case 'upgrade_execution_conflict':
      return upgradeWizardText('codeUpgradeExecutionConflict', language);
    case 'upgrade_execution_required':
      return upgradeWizardText('codeUpgradeExecutionRequired', language);
    case 'upgrade_execution_failed':
      return upgradeWizardText('codeUpgradeExecutionFailed', language);
    case 'upgrade_worker_unavailable':
      return upgradeWizardText('codeUpgradeWorkerUnavailable', language);
    case 'upgrade_activation_not_ready':
      return upgradeWizardText('codeUpgradeActivationNotReady', language);
    case 'upgrade_activation_timeout':
      return upgradeWizardText('codeUpgradeActivationTimeout', language);
    case 'upgrade_restart_disabled':
      return upgradeWizardText('codeUpgradeRestartDisabled', language);
    case 'upgrade_restart_failed':
      return upgradeWizardText('codeUpgradeRestartFailed', language);
    case 'upgrade_bootstrap_retired':
      return upgradeWizardText('codeUpgradeBootstrapRetired', language);
    case 'upgrade_target_ownership_failed':
      return upgradeWizardText('codeUpgradeTargetOwnershipFailed', language);
    case 'upgrade_preflight_blocked':
      return upgradeWizardText('codeUpgradePreflightBlocked', language);
    case 'upgrade_store_locked':
      return upgradeWizardText('codeUpgradeStoreLocked', language);
    case 'upgrade_target_locked':
      return upgradeWizardText('codeUpgradeTargetLocked', language);
    case 'upgrade_target_modified':
      return upgradeWizardText('codeUpgradeTargetModified', language);
    case 'upgrade_source_changed':
      return upgradeWizardText('codeUpgradeSourceChanged', language);
    case 'upgrade_target_conflict':
      return upgradeWizardText('codeUpgradeTargetConflict', language);
    case 'upgrade_copy_in_v2_running':
      return upgradeWizardText('codeUpgradeCopyInV2Running', language);
    case 'upgrade_snapshot_failed':
      return upgradeWizardText('codeUpgradeSnapshotFailed', language);
    case 'upgrade_store_schema_outdated':
      return upgradeWizardText('codeUpgradeStoreSchemaOutdated', language);
    case 'upgrade_reconcile_mismatch':
      return upgradeWizardText('codeUpgradeReconcileMismatch', language);
    case 'upgrade_reconcile_target_incomplete':
      return upgradeWizardText('codeUpgradeReconcileTargetIncomplete', language);
    case 'upgrade_reconcile_not_copied_in':
      return upgradeWizardText('codeUpgradeReconcileNotCopiedIn', language);
    case 'upgrade_reconcile_stage_closed':
      return upgradeWizardText('codeUpgradeReconcileStageClosed', language);
    case 'upgrade_reconcile_record_incomplete':
      return upgradeWizardText('codeUpgradeReconcileRecordIncomplete', language);
    case 'upgrade_reconcile_nothing_compared':
      return upgradeWizardText('codeUpgradeReconcileNothingCompared', language);
    case 'upgrade_cpa_source_unavailable':
      return upgradeWizardText('codeUpgradeCpaSourceUnavailable', language);
    case 'upgrade_cpa_source_missing':
      return upgradeWizardText('codeUpgradeCpaSourceMissing', language);
    case 'upgrade_cpa_config_invalid':
      return upgradeWizardText('codeUpgradeCpaConfigInvalid', language);
    case 'upgrade_cpa_version_required':
      return upgradeWizardText('codeUpgradeCpaVersionRequired', language);
    case 'upgrade_cpa_version_unsupported':
      return upgradeWizardText('codeUpgradeCpaVersionUnsupported', language);
    case 'upgrade_cpa_client_keys_missing':
      return upgradeWizardText('codeUpgradeCpaClientKeysMissing', language);
    case 'upgrade_cpa_plugin_choice_required':
      return upgradeWizardText('codeUpgradeCpaPluginChoiceRequired', language);
    case 'upgrade_cpa_stage_conflict':
      return upgradeWizardText('codeUpgradeCpaStageConflict', language);
    case 'upgrade_cpa_destination_unready':
      return upgradeWizardText('codeUpgradeCpaDestinationUnready', language);
    case 'upgrade_cpa_import_unproven':
      return upgradeWizardText('codeUpgradeCpaImportUnproven', language);
    case 'upgrade_cpa_import_unavailable':
      return upgradeWizardText('codeUpgradeCpaImportUnavailable', language);
    case 'upgrade_cpa_source_locked':
      return upgradeWizardText('codeUpgradeCpaSourceLocked', language);
    case 'upgrade_cpa_source_root_mismatch':
      return upgradeWizardText('codeUpgradeCpaSourceRootMismatch', language);
    case 'upgrade_cpa_destination_request_changed':
      return upgradeWizardText('codeUpgradeCpaDestinationRequestChanged', language);
    case 'upgrade_cpa_import_target_foreign':
      return upgradeWizardText('codeUpgradeCpaImportTargetForeign', language);
    case 'upgrade_cpa_import_target_unreachable':
      return upgradeWizardText('codeUpgradeCpaImportTargetUnreachable', language);
    case 'upgrade_cpa_connection_conflict':
      return upgradeWizardText('codeUpgradeCpaConnectionConflict', language);
    case 'upgrade_cpa_destination_key_required':
      return upgradeWizardText('codeUpgradeCpaDestinationKeyRequired', language);
    case 'upgrade_external_connection_required':
      return upgradeWizardText('codeUpgradeExternalConnectionRequired', language);
    case 'external_cpa_version_unsupported':
      return upgradeWizardText('codeExternalCpaVersionUnsupported', language);
    case 'external_cpa_config_document_invalid':
      return upgradeWizardText('codeExternalCpaConfigDocumentInvalid', language);
    case 'external_management_key_rejected':
    case 'external_management_rejected':
      return upgradeWizardText('codeExternalManagementKeyRejected', language);
    case 'external_target_moved':
      return upgradeWizardText('codeExternalTargetMoved', language);
    case 'external_cpa_authority_unavailable':
      return upgradeWizardText('codeExternalCpaAuthorityUnavailable', language);
    case 'upgrade_external_connect_not_applicable':
      return upgradeWizardText('codeUpgradeExternalConnectNotApplicable', language);
    case 'upgrade_external_connect_v2_running':
      return upgradeWizardText('codeUpgradeExternalConnectV2Running', language);
    case 'external_target_unavailable':
      return upgradeWizardText('codeExternalTargetUnavailable', language);
    case 'upgrade_external_connect_conflict':
      return upgradeWizardText('codeUpgradeExternalConnectConflict', language);
    case 'upgrade_source_already_v2':
      return upgradeWizardText('codeUpgradeSourceAlreadyV2', language);
    case 'upgrade_target_layout_mismatch':
      return upgradeWizardText('codeUpgradeTargetLayoutMismatch', language);
    case 'upgrade_source_version_undeclared':
      return upgradeWizardText('codeUpgradeSourceVersionUndeclared', language);
    case 'upgrade_execution_canceled':
      return upgradeWizardText('codeUpgradeExecutionCanceled', language);
    case 'upgrade_v2_running':
      return upgradeWizardText('codeUpgradeV2Running', language);
    case 'upgrade_store_unavailable':
      return upgradeWizardText('codeUpgradeStoreUnavailable', language);
    case 'upgrade_verification_failed':
    case 'upgrade_verify_incomplete':
      return upgradeWizardText('codeUpgradeVerificationFailed', language);
    case 'database_unavailable':
      return upgradeWizardText('codeDatabaseUnavailable', language);
    case 'upgrade_migrate_not_copied_in':
      return upgradeWizardText('codeUpgradeMigrateNotCopiedIn', language);
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

const executionEn = {
  recoveryTitle: 'Upgrade executor unavailable',
  recoveryHelp: 'Keep the original installation and backup. For Docker, resume the v2 upgrade script with the same operation and reopen its Upgrade URL. Do not initialize or repeat the migration. Native and manually prepared installations must use their deployment-specific upgrade or recovery guide; this page cannot run host commands.',
  running: 'The executor is working. Waiting for the recorded result…',
  actionFailed: 'The requested step could not be confirmed',
  conflict: 'Another action is running or the recorded operation changed. Read the current status before trying again; no duplicate action was started.',
  executionStatusFailedTitle: 'Executor reported a step failure',
  resultPendingRestartHelp: 'Verification is recorded. v2 routes remain pending until controlled container restart completes and the canonical surface responds.',
  externalOverridePairRequired: 'Enter both the external CPA base URL and management key together, or clear override to reuse the copied connection.',
  reconcileTitle: 'Review retained history and reconcile',
  reconcileHelp: 'Reconciliation preserves the copied history. Review each listed date explicitly; acknowledging a gap does not reconstruct deleted events.',
  acknowledgeDay: 'I accept the recorded history gap on {day}.',
  acknowledgeParity: 'I accept the disclosed retained-history parity gaps without claiming missing events were recovered.',
  truncatedDays: 'Only a window of mismatched dates is shown below. Confirm below if you accept all recorded mismatch dates without claiming missing events were recovered.',
  acknowledgeAllTruncated: 'I accept all {total} recorded mismatched dates, including dates beyond the displayed window.',
  reconcileAction: 'Reconcile the copied data',
  targetTitle: 'Choose the CPA destination',
  targetHelp: 'Import the accessible source CPA into the built-in Managed runtime, or explicitly connect to an independently operated External CPA. Unsupported settings remain listed for review.',
  managedRecommended: 'Managed CPA · Recommended',
  externalAlternate: 'External CPA · Alternate',
  managedUnavailable: 'The executor has no readable source CPA tree. Managed import is unavailable. Resume script preparation to provide the actual source, or explicitly choose an independently reachable External CPA.',
  pluginsLabel: 'CPA plugins',
  pluginsChoose: 'Choose explicitly',
  pluginsDisabled: 'Keep plugins disabled (recommended)',
  pluginsEnabled: 'Enable plugins',
  pluginsHelp: 'Plugins may run privileged capabilities. Enable them only if you intend to trust and use the imported plugin configuration.',
  importAction: 'Import into Managed CPA',
  externalReuse: 'External CPA must already be independently reachable. The script may have stopped the original CPA for Managed import; this page cannot start that old container. Reuse the copied connection only if its CPA is still running.',
  externalOverride: 'Use a different External CPA connection',
  externalUrl: 'External CPA base URL',
  externalKey: 'External CPA management key',
  externalAction: 'Connect External CPA',
  activateTitle: 'Activate verified v2',
  activateHelp: 'Verification is recorded. Activation requests a controlled container restart, then checks the committed access routes. The stopped original installation and backup remain available for recovery.',
  activateAction: 'Activate v2 and restart',
  activationWaiting: 'Waiting for v2 to restart and confirm the final access routes…',
  activationTimeout: 'The final access routes were not confirmed within two minutes. Read status again or resume the same script operation for recovery. No completion was inferred from the restart request.',
  openConsole: 'Open v2 Console',
  resultCanonicalGatewayPendingHelp: 'The v2 Management Console is active at its canonical path, while the model gateway is still starting or reloading. You can open the Console now; inference routes will become active once gateway readiness completes.',
  resultCanonicalExternalPendingHelp: 'The v2 Management Console is active at its canonical path, while the configured External CPA target is currently unavailable. Open the Console to inspect or rebind the External CPA connection.',
  gatewayWaiting: 'Canonical v2 console is ready; waiting for the model gateway to finish starting…',
  activateBootstrapRetiredHelp: 'The temporary bootstrap route is no longer active. Check the canonical v2 access path below rather than re-submitting bootstrap activation.',
  activateBootstrapRetiredNote: 'Read the status again to verify the canonical v2 route once the restarted service finishes coming up.',
};
const executionZh: typeof executionEn = {
  recoveryTitle: '升级执行器不可用',
  recoveryHelp: '请保留原安装和备份。Docker 部署请使用同一操作重新运行 v2 升级脚本，再打开脚本提供的升级地址；不要重新初始化或重复迁移。Native 与手动准备的安装请按对应部署的升级或恢复指南处理，本页不能执行宿主机命令。',
  running: '执行器正在处理，请等待实际记录的结果…',
  actionFailed: '无法确认本次操作结果',
  conflict: '已有操作执行中，或记录已变化。请读取最新状态后再决定是否重试；没有重复启动操作。',
  executionStatusFailedTitle: '后台执行步骤未完成',
  resultPendingRestartHelp: '验证结论已记录。需完成受控容器重启并由规范路径响应后，v2 访问入口才会正式生效。',
  externalOverridePairRequired: '请同时填写外部 CPA 基础地址与管理密钥，或取消覆盖以复用已复制的连接。',
  reconcileTitle: '核对留存历史并执行对账',
  reconcileHelp: '对账保留已复制的历史。请逐一核对列出的日期；接受缺口不代表已恢复删除的事件。',
  acknowledgeDay: '我接受 {day} 已记录的历史缺口。',
  acknowledgeParity: '我接受已披露的留存历史一致性缺口，不将缺失事件视为已恢复。',
  truncatedDays: '下方仅展示部分存在差异的日期窗口。若确认接受全部已记录差异日期（不代表已恢复缺失事件），请在下方勾选确认。',
  acknowledgeAllTruncated: '我接受全部 {total} 个已记录的差异日期（包含当前窗口未列出的日期）。',
  reconcileAction: '执行复制数据对账',
  targetTitle: '选择 CPA 目标',
  targetHelp: '将可读取的来源 CPA 导入内置 Managed 运行时，或明确选择独立运维的 External CPA。不支持的配置仍会列出供核对。',
  managedRecommended: '内置 Managed CPA · 推荐',
  externalAlternate: '外部 External CPA · 备选',
  managedUnavailable: '执行器无法读取来源 CPA 配置目录，当前不能导入 Managed。请重新准备升级脚本提供真实来源，或明确选择独立可达的 External CPA。',
  pluginsLabel: 'CPA 插件',
  pluginsChoose: '请明确选择',
  pluginsDisabled: '保持插件关闭（推荐）',
  pluginsEnabled: '启用插件',
  pluginsHelp: '插件可能运行高权限能力。仅在确认信任并需要使用导入的插件配置时启用。',
  importAction: '导入内置 Managed CPA',
  externalReuse: '外部 CPA 必须已独立运行且可访问。脚本可能已为 Managed 导入停止原 CPA，本页无法启动旧容器；仅在原 CPA 仍运行时重用已复制的连接。',
  externalOverride: '改用其他外部 CPA 连接',
  externalUrl: '外部 CPA 基础地址',
  externalKey: '外部 CPA 管理密钥',
  externalAction: '连接 External CPA',
  activateTitle: '激活已验证的 v2',
  activateHelp: '验证结论已记录。激活将请求受控重启容器，再检查已提交的访问路由；已停止的原安装和备份仍保留用于恢复。',
  activateAction: '激活 v2 并重启',
  activationWaiting: '正在等待 v2 重启并确认最终访问路由…',
  activationTimeout: '两分钟内未确认最终访问路由。请重新读取状态，或使用同一脚本操作继续恢复；重启请求不会被当作已完成。',
  openConsole: '打开 v2 控制台',
  resultCanonicalGatewayPendingHelp: 'v2 管理控制台已在规范路径生效，但模型网关仍在启动或重载中。现在即可打开控制台；待网关就绪后，模型推理入口将随之激活。',
  resultCanonicalExternalPendingHelp: 'v2 管理控制台已在规范路径生效，但所配置的外部 CPA 目标当前不可用。可打开控制台检查或重新绑定外部 CPA 连接。',
  gatewayWaiting: '规范 v2 控制台已就绪，正在等待模型网关完成启动…',
  activateBootstrapRetiredHelp: '临时引导入口已停用，不应再次提交引导激活；请使用下方重新读取功能检查规范 v2 访问路径。',
  activateBootstrapRetiredNote: '请待重启服务就绪后点击“重新读取”，直接探测规范 v2 路由状态。',
};
const executionTw: typeof executionEn = {
  recoveryTitle: '升級執行器不可用',
  recoveryHelp: '請保留原安裝及備份。Docker 部署請以同一操作重新執行 v2 升級指令碼，再開啟指令碼提供的升級網址；不要重新初始化或重複移轉。Native 及手動準備的安裝請依對應部署的升級或復原指南處理，本頁不能執行主機指令。',
  running: '執行器正在處理，請等待實際記錄的結果…',
  actionFailed: '無法確認本次操作結果',
  conflict: '已有操作執行中，或記錄已變更。請讀取最新狀態後再決定是否重試；沒有重複啟動操作。',
  executionStatusFailedTitle: '背景執行步驟未完成',
  resultPendingRestartHelp: '驗證結論已記錄。需完成受控容器重新啟動並由規範路徑回應後，v2 存取入口才會正式生效。',
  externalOverridePairRequired: '請同時填寫外部 CPA 基礎網址與管理金鑰，或取消覆寫以重複使用已複製的連線。',
  reconcileTitle: '核對留存歷史並執行對帳',
  reconcileHelp: '對帳保留已複製的歷史。請逐一核對列出的日期；接受缺口不代表已復原刪除的事件。',
  acknowledgeDay: '我接受 {day} 已記錄的歷史缺口。',
  acknowledgeParity: '我接受已揭露的留存歷史一致性缺口，不將缺失事件視為已復原。',
  truncatedDays: '下方僅顯示部分存在差異的日期視窗。若確認接受全部已記錄差異日期（不代表已復原缺失事件），請於下方勾選確認。',
  acknowledgeAllTruncated: '我接受全部 {total} 個已記錄的差異日期（包含目前視窗未列出的日期）。',
  reconcileAction: '執行複製資料對帳',
  targetTitle: '選擇 CPA 目標',
  targetHelp: '將可讀取的來源 CPA 匯入內建 Managed 執行環境，或明確選擇獨立維運的 External CPA。不支援的設定仍會列出供核對。',
  managedRecommended: '內建 Managed CPA · 建議',
  externalAlternate: '外部 External CPA · 替代',
  managedUnavailable: '執行器無法讀取來源 CPA 設定目錄，目前不能匯入 Managed。請重新準備升級指令碼提供真實來源，或明確選擇獨立可達的 External CPA。',
  pluginsLabel: 'CPA 外掛',
  pluginsChoose: '請明確選擇',
  pluginsDisabled: '保持外掛關閉（建議）',
  pluginsEnabled: '啟用外掛',
  pluginsHelp: '外掛可能執行高權限能力。僅在確認信任且需要使用匯入的外掛設定時啟用。',
  importAction: '匯入內建 Managed CPA',
  externalReuse: '外部 CPA 必須已獨立執行且可存取。指令碼可能已為 Managed 匯入停止原 CPA，本頁無法啟動舊容器；僅在原 CPA 仍執行時重用已複製的連線。',
  externalOverride: '改用其他外部 CPA 連線',
  externalUrl: '外部 CPA 基礎網址',
  externalKey: '外部 CPA 管理金鑰',
  externalAction: '連線 External CPA',
  activateTitle: '啟用已驗證的 v2',
  activateHelp: '驗證結論已記錄。啟用將請求受控重新啟動容器，再檢查已提交的存取路由；已停止的原安裝及備份仍保留用於復原。',
  activateAction: '啟用 v2 並重新啟動',
  activationWaiting: '正在等待 v2 重新啟動並確認最終存取路由…',
  activationTimeout: '兩分鐘內未確認最終存取路由。請重新讀取狀態，或使用同一指令碼操作繼續復原；重新啟動請求不會被視為已完成。',
  openConsole: '開啟 v2 主控台',
  resultCanonicalGatewayPendingHelp: 'v2 管理主控台已在規範路徑生效，但模型閘道仍在啟動或重載中。現在即可開啟主控台；待閘道就緒後，模型推論入口將隨之啟用。',
  resultCanonicalExternalPendingHelp: 'v2 管理主控台已在規範路徑生效，但所設定的外部 CPA 目標目前無法使用。可開啟主控台檢查或重新綁定外部 CPA 連線。',
  gatewayWaiting: '規範 v2 主控台已就緒，正在等待模型閘道完成啟動…',
  activateBootstrapRetiredHelp: '暫時引導入口已停用，不應再次提交引導啟用；請使用下方重新讀取功能檢查規範 v2 存取路徑。',
  activateBootstrapRetiredNote: '請待重啟服務就緒後點擊「重新讀取」，直接探測規範 v2 路由狀態。',
};
const executionRu: typeof executionEn = {
  recoveryTitle: 'Исполнитель обновления недоступен',
  recoveryHelp: 'Сохраните исходную установку и резервную копию. Для Docker повторно запустите скрипт v2 с той же операцией и откройте его URL обновления. Не инициализируйте установку и не повторяйте перенос. Для Native и ручных установок следуйте руководству соответствующего развёртывания; страница не выполняет команды хоста.',
  running: 'Исполнитель работает. Ожидается записанный результат…',
  actionFailed: 'Результат действия не подтверждён',
  conflict: 'Другое действие выполняется или запись операции изменилась. Прочитайте текущий статус перед повтором; повторное действие не запущено.',
  executionStatusFailedTitle: 'Исполнитель сообщил об ошибке шага',
  resultPendingRestartHelp: 'Проверка записана. Маршруты v2 станут доступны только после управляемого перезапуска контейнера и ответа канонической поверхности.',
  externalOverridePairRequired: 'Укажите базовый URL и ключ управления External CPA вместе или отключите переопределение, чтобы использовать скопированное подключение.',
  reconcileTitle: 'Проверка сохранённой истории и сверка',
  reconcileHelp: 'Сверка сохраняет скопированную историю. Подтвердите каждую указанную дату; принятие пробела не восстанавливает удалённые события.',
  acknowledgeDay: 'Я принимаю записанный пробел истории за {day}.',
  acknowledgeParity: 'Я принимаю раскрытые расхождения сохранённой истории без утверждения о восстановлении отсутствующих событий.',
  truncatedDays: 'Ниже показана только часть дат с расхождениями. Подтвердите ниже, если принимаете все записанные даты расхождений без утверждения о восстановлении отсутствующих событий.',
  acknowledgeAllTruncated: 'Я принимаю все {total} записанных дат расхождений, включая даты за пределами показанного окна.',
  reconcileAction: 'Сверить скопированные данные',
  targetTitle: 'Выбор назначения CPA',
  targetHelp: 'Импортируйте доступный источник CPA во встроенный Managed или явно подключите независимо обслуживаемый External CPA. Неподдерживаемые параметры остаются в списке для проверки.',
  managedRecommended: 'Managed CPA · Рекомендуется',
  externalAlternate: 'External CPA · Альтернатива',
  managedUnavailable: 'Исполнитель не видит читаемого каталога исходного CPA. Импорт Managed недоступен. Продолжите подготовку скриптом с реальным источником или явно выберите независимо доступный External CPA.',
  pluginsLabel: 'Плагины CPA',
  pluginsChoose: 'Выберите явно',
  pluginsDisabled: 'Оставить плагины выключенными (рекомендуется)',
  pluginsEnabled: 'Включить плагины',
  pluginsHelp: 'Плагины могут выполнять привилегированные функции. Включайте их только при доверии к импортируемой конфигурации и намерении использовать её.',
  importAction: 'Импортировать в Managed CPA',
  externalReuse: 'External CPA должен уже работать независимо и быть доступен. Скрипт мог остановить исходный CPA для импорта Managed; страница не запускает старый контейнер. Используйте скопированное подключение только если его CPA ещё работает.',
  externalOverride: 'Использовать другое подключение External CPA',
  externalUrl: 'Базовый URL External CPA',
  externalKey: 'Ключ управления External CPA',
  externalAction: 'Подключить External CPA',
  activateTitle: 'Активация проверенного v2',
  activateHelp: 'Проверка записана. Активация запрашивает управляемый перезапуск контейнера, затем проверяет зафиксированные маршруты. Остановленная исходная установка и резервная копия сохраняются для восстановления.',
  activateAction: 'Активировать v2 и перезапустить',
  activationWaiting: 'Ожидание перезапуска v2 и подтверждения итоговых маршрутов доступа…',
  activationTimeout: 'Итоговые маршруты не подтверждены за две минуты. Прочитайте статус снова или восстановите ту же операцию скриптом. Запрос перезапуска не считается завершением.',
  openConsole: 'Открыть консоль v2',
  resultCanonicalGatewayPendingHelp: 'Панель управления v2 уже активна по каноническому адресу, но шлюз моделей ещё запускается или перезагружается. Вы уже можете открыть консоль; маршруты инференса станут доступны после готовности шлюза.',
  resultCanonicalExternalPendingHelp: 'Панель управления v2 уже активна по каноническому адресу, но настроенный внешний CPA сейчас недоступен. Откройте консоль для проверки или перепривязки внешнего CPA.',
  gatewayWaiting: 'Каноническая консоль v2 готова; ожидание завершения запуска шлюза моделей…',
  activateBootstrapRetiredHelp: 'Временный bootstrap-маршрут больше не активен. Проверьте канонический путь v2 повторным чтением вместо повторной отправки bootstrap-активации.',
  activateBootstrapRetiredNote: 'Нажмите повторное чтение, чтобы проверить канонический маршрут v2 после завершения запуска службы.',
};
export function upgradeExecutionText(key: keyof typeof executionEn, language = fullWebLanguage()): string {
  if (language === 'zh-TW') return executionTw[key];
  if (language.startsWith('zh')) return executionZh[key];
  if (language === 'ru') return executionRu[key];
  return executionEn[key];
}
