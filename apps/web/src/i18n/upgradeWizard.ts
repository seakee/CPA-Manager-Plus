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
  preflightHelp:
    '该检查只读取已停止的 1.x 来源，不复制、不写入，也不为后续步骤授予任何权限。',
  preflightReading: '正在检查来源…',
  preflightSourceKind: '来源类型',
  preflightSourceVersion: '来源版本',
  preflightFingerprint: '来源指纹（缩写）',
  preflightChecks: '检查项',
  preflightUnknown: '未上报',
  preflightRetry: '重新预检',
  preflightPendingTitle: '来源版本在命令行复制时声明',
  preflightPendingHelp: '只读预检无法声明 1.x 版本，来源本身也不记录该信息。请在命令行复制时传入',
  preflightPendingTail: '此检查在本页保持待定状态，并不是失败。',
  preflightBlockedTitle: '预检阻断了升级',
  preflightBlockedHelp: '请在宿主机上逐项解决下列问题后重新预检。当前尚未复制任何数据。',
  preflightReadyTitle: '来源已通过预检',
  preflightReadyHelp: '所有检查均通过。尚未复制任何数据；下一步说明该操作将要做什么。',
  preflightReadyPendingTitle: '来源已通过预检（含复制阶段待确认项）',
  preflightReadyPendingHelp:
    '未发现阻断项，待定检查项需在命令行复制阶段通过 --source-version 声明确认。尚未复制任何数据；下一步说明该操作将要做什么。',
  preflightGapsTitle: '留存历史缺口',
  preflightGapsHelp:
    '这些日期的原始事件已删除，但与留存投影不一致。复制步骤会如实上报，不会静默修补。',
  preflightGapsDays: '存在缺口的日期数',

  checkSourceOwnership: '来源没有运行中的写入者',
  checkSourceVersion: '来源版本受支持',
  checkFingerprint: '来源指纹已记录',
  checkDisk: '复制所需可用空间',
  checkCpaClassification: 'CPA 分类',
  checkParity: '留存历史一致性',

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

  confirmTitle: '升级将做什么',
  confirmHelp: '开始前请确认以下事实。本页面只读取状态；复制步骤本身由 Manager 宿主机启动。',
  confirmSourceKind: '来源类型',
  confirmSourceVersion: '来源版本',
  confirmReadOnlyTitle: '1.x 卷保持只读',
  confirmReadOnlyHelp: '1.x 数据目录以只读方式挂载，只被复制出去，不会被写入。复制前后都会记录指纹。',
  confirmBackupTitle: '先做已验证的备份',
  confirmBackupHelp: '复制开始前会先生成备份包，并在向 v2 卷写入任何内容之前完成校验。',
  confirmManualTitle: '可能需要手工处理的项',
  confirmManualHelp: '操作无法自动承接的内容会在结果步骤中逐条列出，不会静默丢弃。',
  confirmManualKeys: '需要在 v8 目标上重新创建的 CPA 客户端 Key',
  confirmManualCredentials: '需要重新填写的 CPA 提供商与凭证段',
  confirmManualGateway: '目标无法自动导入的网关配置',
  confirmManualHistory: '留存历史无法重建的用量日期',
  confirmAck: '我明白：1.x 来源保持只读，先做已验证的备份，上述项目可能需要手工处理。',
  confirmContinue: '继续到进度',

  progressTitle: '已记录的复制进度',
  progressHelp: '本页面只读取操作记录的状态，不会推进该操作。',
  progressStage: '当前阶段',
  progressDomains: '已完成域',
  progressDomainsNone: '尚未记录任何已完成域。',
  progressManual: '已记录的手工项',
  progressManualNone: '尚未记录手工项。',
  progressReading: '正在读取操作状态…',
  progressUnrecorded: '尚未记录升级操作。请在 Manager 宿主机上启动它。',
  progressWaiting: '正在等待下一次记录到的变化…',
  resumeNotice: '本安装已记录升级操作，本页从记录的阶段继续。只有在没有任何操作记录时才需要只读预检。',
  progressStageNote: '记录到的阶段是该操作最后写入的位置，并不证明其背后的工作已经完成。',
  progressStalledTitle: '无法读取操作状态',
  progressStalledHelp: '连续三次读取失败。下方保留最后一次观测结果，可能已过期。',
  progressRetry: '重新读取操作状态',

  resultStage: '记录阶段',
  resultNext: '下一步',
  resultSource: '记录的来源',
  resultCompleteTitle: '升级已完成',
  resultCompleteHelp: '记录中的操作状态为完成。v2 Manager 现在提供常规界面。',
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
  resultReread: '重新读取操作状态',

  verifyTitle: '验证并完成升级',
  verifyHelp:
    '验证会重新读取已记录的操作，核对复制来源、对账结果以及本安装实际使用的 CPA，并记录该目标唯一支持的结论。本页面不会自行宣布升级已完成。',
  verifySourceRequired:
    '1.x 数据目录必须保持已挂载且已停止：它的指纹是“复制读取的卷未被写入”的证明。',
  verifyAction: '执行验证',
  verifyRetry: '再次执行验证',
  verifyRunning: '正在验证已记录的操作…',
  verifyDoneTitle: '验证已记录结论',
  verifyComplete: '操作已完成。请重启 Manager：常规产品将启动，升级界面关闭。',
  verifyAlready: '该位置此前已记录，本次没有写入任何内容。',
  verifyFailedTitle: '验证未记录任何结论',
  verifyTargetExternal: '目标：由所有者管理的 External CPA',
  verifyTargetManaged: '目标：由本 Manager 拥有的 CPA',
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
  preflightHelp:
    '此檢查只讀取已停止的 1.x 來源，不複製、不寫入，也不為後續步驟授予任何權限。',
  preflightReading: '正在檢查來源…',
  preflightSourceKind: '來源類型',
  preflightSourceVersion: '來源版本',
  preflightFingerprint: '來源指紋（縮寫）',
  preflightChecks: '檢查項目',
  preflightUnknown: '未回報',
  preflightRetry: '重新預檢',
  preflightPendingTitle: '來源版本在命令列複製時宣告',
  preflightPendingHelp: '唯讀預檢無法宣告 1.x 版本，來源本身也不記錄此資訊。請在命令列複製時傳入',
  preflightPendingTail: '此檢查在本頁保持待定狀態，並不是失敗。',
  preflightBlockedTitle: '預檢阻斷了升級',
  preflightBlockedHelp: '請在主機上逐項解決下列問題後重新預檢。目前尚未複製任何資料。',
  preflightReadyTitle: '來源已通過預檢',
  preflightReadyHelp: '所有檢查均通過。尚未複製任何資料；下一步說明此操作將要做什麼。',
  preflightReadyPendingTitle: '來源已通過預檢（含複製階段待確認項目）',
  preflightReadyPendingHelp:
    '未發現阻斷項目，待定檢查項目需在命令列複製階段透過 --source-version 宣告確認。尚未複製任何資料；下一步說明此操作將要做什麼。',
  preflightGapsTitle: '留存歷史缺口',
  preflightGapsHelp:
    '這些日期的原始事件已刪除，但與留存投影不一致。複製步驟會如實回報，不會靜默修補。',
  preflightGapsDays: '存在缺口的日期數',

  checkSourceOwnership: '來源沒有執行中的寫入者',
  checkSourceVersion: '來源版本受支援',
  checkFingerprint: '來源指紋已記錄',
  checkDisk: '複製所需可用空間',
  checkCpaClassification: 'CPA 分類',
  checkParity: '留存歷史一致性',

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

  confirmTitle: '升級將做什麼',
  confirmHelp: '開始前請確認以下事實。本頁面只讀取狀態；複製步驟本身由 Manager 主機啟動。',
  confirmSourceKind: '來源類型',
  confirmSourceVersion: '來源版本',
  confirmReadOnlyTitle: '1.x 磁碟區保持唯讀',
  confirmReadOnlyHelp: '1.x 資料目錄以唯讀方式掛載，只被複製出去，不會被寫入。複製前後都會記錄指紋。',
  confirmBackupTitle: '先做已驗證的備份',
  confirmBackupHelp: '複製開始前會先產生備份包，並在向 v2 磁碟區寫入任何內容之前完成驗證。',
  confirmManualTitle: '可能需要手動處理的項目',
  confirmManualHelp: '操作無法自動承接的內容會在結果步驟中逐條列出，不會靜默捨棄。',
  confirmManualKeys: '需要在 v8 目標上重新建立的 CPA 用戶端 Key',
  confirmManualCredentials: '需要重新填寫的 CPA 提供者與憑證區段',
  confirmManualGateway: '目標無法自動匯入的閘道設定',
  confirmManualHistory: '留存歷史無法重建的用量日期',
  confirmAck: '我明白：1.x 來源保持唯讀，先做已驗證的備份，上述項目可能需要手動處理。',
  confirmContinue: '繼續到進度',

  progressTitle: '已記錄的複製進度',
  progressHelp: '本頁面只讀取操作記錄的狀態，不會推進該操作。',
  progressStage: '目前階段',
  progressDomains: '已完成領域',
  progressDomainsNone: '尚未記錄任何已完成領域。',
  progressManual: '已記錄的手動項目',
  progressManualNone: '尚未記錄手動項目。',
  progressReading: '正在讀取操作狀態…',
  progressUnrecorded: '尚未記錄升級操作。請在 Manager 主機上啟動它。',
  progressWaiting: '正在等待下一次記錄到的變化…',
  resumeNotice: '本安裝已記錄升級操作，本頁從記錄的階段繼續。只有在沒有任何操作記錄時才需要唯讀預檢。',
  progressStageNote: '記錄到的階段是該操作最後寫入的位置，並不證明其背後的工作已經完成。',
  progressStalledTitle: '無法讀取操作狀態',
  progressStalledHelp: '連續三次讀取失敗。下方保留最後一次觀測結果，可能已過期。',
  progressRetry: '重新讀取操作狀態',

  resultStage: '記錄階段',
  resultNext: '下一步',
  resultSource: '記錄的來源',
  resultCompleteTitle: '升級已完成',
  resultCompleteHelp: '記錄中的操作狀態為完成。v2 Manager 現在提供一般介面。',
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
  resultReread: '重新讀取操作狀態',

  verifyTitle: '驗證並完成升級',
  verifyHelp:
    '驗證會重新讀取已記錄的操作，核對複製來源、對帳結果以及本安裝實際使用的 CPA，並記錄該目標唯一支援的結論。本頁面不會自行宣布升級已完成。',
  verifySourceRequired:
    '1.x 資料目錄必須保持已掛載且已停止：它的指紋是「複製讀取的磁碟區未被寫入」的證明。',
  verifyAction: '執行驗證',
  verifyRetry: '再次執行驗證',
  verifyRunning: '正在驗證已記錄的操作…',
  verifyDoneTitle: '驗證已記錄結論',
  verifyComplete: '操作已完成。請重新啟動 Manager：一般產品將啟動，升級介面關閉。',
  verifyAlready: '該位置先前已記錄，本次沒有寫入任何內容。',
  verifyFailedTitle: '驗證未記錄任何結論',
  verifyTargetExternal: '目標：由擁有者管理的 External CPA',
  verifyTargetManaged: '目標：由本 Manager 擁有的 CPA',
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
