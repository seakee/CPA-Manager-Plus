import { fullWebLanguage } from './fullweb';

const en = {
  title: 'Upgrade to CPAMP v2',
  mode: 'Upgrade',
  verified: 'Administrator identity verified',
  retained: 'Your existing administrator key is retained. No new key is required.',
  blocked: 'Migration is not available for this source yet.',
  boundary:
    'Authentication is complete. Backup, migration, CPA changes and management entry changes have not started.',
  next: 'Further upgrade decisions will become available when a supported migration path is implemented. Keep your existing data and key.',
  verify: 'Check authentication again',
  logout: 'Return to login',
  checking: 'Verifying the existing administrator…',
  checkingShort: 'Verifying identity',
  checkingHelp:
    'Checking the current installation. Upgrade details and actions are unavailable until verification finishes.',
  recoveryLabel: 'Unable to use your original key?',
  recoveryHelp:
    'Keep the existing data and key files. Stop the Manager and recover the administrator key on its deployment host using tools for that installed version. Then return here to verify it. Do not initialize the installation again; online key reset is not available here.',
  footer: 'CPA Manager Plus · Installation and upgrade',
  invalid:
    'The existing Manager administrator key could not be verified. Enter it again. CPA management keys cannot be used here.',
  unavailable:
    'This source cannot be verified safely. Check the stopped installation on its host; do not initialize it again.',
  changed:
    'The source or Manager process has changed. Sign in again to verify the current installation.',
  network: 'Authentication could not be confirmed. Check the connection and sign in again.',
  expired: 'Authentication has expired. Sign in again.',
  placeholder: 'Existing CPAMP administrator key',
  loginHint:
    'Existing installation detected. Verify your original CPAMP administrator key to view the upgrade starting page. Authentication does not start migration or connect to CPA.',
};
const zh: typeof en = {
  title: '升级到 CPAMP v2',
  mode: '升级模式',
  verified: '管理员身份已验证',
  retained: '沿用现有管理员密钥，无需重新设置。',
  blocked: '当前来源尚无可执行的迁移路径。',
  boundary: '身份验证已完成；备份、迁移、CPA 变更和管理入口变更均未开始。',
  next: '支持的迁移路径实现后才能继续升级决策。请保留原有数据和管理员密钥。',
  verify: '重新验证当前身份',
  logout: '返回登录',
  checking: '正在验证原管理员身份…',
  checkingShort: '正在验证身份',
  checkingHelp: '正在核验当前安装，验证完成前不显示升级详情，也不开放升级操作。',
  recoveryLabel: '无法使用原密钥？',
  recoveryHelp:
    '请保留现有数据和密钥文件，停止 Manager 后在部署主机上使用与已安装版本匹配的工具恢复管理员密钥，再回到这里验证。不要重新初始化原安装；此处不提供在线密钥重置。',
  footer: 'CPA Manager Plus · 安装与升级',
  invalid: '无法验证原 Manager 管理员密钥，请重新输入。此处不能使用 CPA 管理密钥。',
  unavailable: '当前来源无法安全验证。请在宿主机停机检查原安装，不要重新初始化。',
  changed: '来源或 Manager 进程已变化，请重新登录以验证当前安装。',
  network: '身份验证结果未确认，请检查连接后重新登录。',
  expired: '身份验证已过期，请重新登录。',
  placeholder: '原 CPAMP 管理员密钥',
  loginHint:
    '检测到现有安装。请验证原 CPAMP 管理员密钥，进入升级起始页；认证不会启动迁移或连接 CPA。',
};
export const upgradeEntryText = (key: keyof typeof en, language = fullWebLanguage()) =>
  (language.startsWith('zh') ? zh : en)[key];
