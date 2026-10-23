import i18n from 'i18next';

const messages = {
  strategy: ['Scheduling strategy', '调度策略', '排程策略', 'Стратегия распределения'],
  affinity: ['Session affinity', '会话亲和', '工作階段親和', 'Привязка сеанса'],
  ttl: ['Session binding lifetime', '绑定保留时间', '綁定保留時間', 'Срок привязки сеанса'],
  subagents: [
    'Inherit parent session binding',
    '子代理继承会话绑定',
    '子代理繼承工作階段綁定',
    'Наследовать привязку родительского сеанса',
  ],
  prefix: [
    'Require model prefix matching',
    '要求模型前缀匹配',
    '要求模型前綴符合',
    'Требовать совпадения префикса модели',
  ],
  count: [
    'Additional retry rounds',
    '额外重试轮次',
    '額外重試輪次',
    'Дополнительные циклы повторов',
  ],
  credentials: [
    'Maximum credentials per round',
    '每轮最多尝试凭据',
    '每輪最多嘗試憑證',
    'Лимит учётных данных за цикл',
  ],
  interval: [
    'Maximum cooldown wait (seconds)',
    '最大冷却等待（秒）',
    '最大冷卻等待（秒）',
    'Максимальное ожидание охлаждения (сек.)',
  ],
  disableCooling: [
    'Disable global cooldown',
    '禁用全局冷却',
    '停用全域冷卻',
    'Отключить общее охлаждение',
  ],
  persistCooling: [
    'Persist cooldown state',
    '持久化冷却状态',
    '持久化冷卻狀態',
    'Сохранять состояние охлаждения',
  ],
  transient: [
    'Transient error cooldown (seconds)',
    '瞬态错误冷却（秒）',
    '暫態錯誤冷卻（秒）',
    'Охлаждение временных ошибок (сек.)',
  ],
  proxy: ['Global proxy URL', '全局代理地址', '全域代理位址', 'Общий адрес прокси'],
  headers: [
    'Pass filtered response headers',
    '透传筛选后的响应头',
    '傳遞篩選後的回應標頭',
    'Передавать отфильтрованные заголовки ответа',
  ],
  keepalive: [
    'Non-streaming keepalive (seconds)',
    '非流式保活间隔（秒）',
    '非串流保活間隔（秒）',
    'Поддержание нестримингового соединения (сек.)',
  ],
  sse: [
    'SSE keepalive interval (seconds)',
    'SSE 保活间隔（秒）',
    'SSE 保活間隔（秒）',
    'Интервал поддержания SSE (сек.)',
  ],
  bootstrap: [
    'Retries before first byte',
    '首字节前重试次数',
    '首位元組前重試次數',
    'Повторы до первого байта',
  ],
  images: [
    'Image generation restrictions',
    '图片生成限制',
    '圖片生成限制',
    'Ограничения генерации изображений',
  ],
  imageModel: [
    'GPT Image 2 base model',
    'GPT Image 2 基础模型',
    'GPT Image 2 基礎模型',
    'Базовая модель GPT Image 2',
  ],
  videoTtl: [
    'Video result authorization lifetime',
    '视频结果授权保留',
    '影片結果授權保留',
    'Срок авторизации результата видео',
  ],
  patch: [
    'Extend apply_patch declaration',
    '扩展 apply_patch 声明',
    '擴充 apply_patch 宣告',
    'Расширить объявление apply_patch',
  ],
  multiAgent: [
    'Multi-agent v2 compatibility',
    'Multi-agent v2 兼容',
    'Multi-agent v2 相容',
    'Совместимость Multi-agent v2',
  ],
  steering: [
    'Full-duplex response control',
    '全双工响应控制',
    '全雙工回應控制',
    'Полнодуплексное управление ответом',
  ],
  codexCloak: [
    'Disable Codex request cloaking',
    '禁用 Codex 请求伪装',
    '停用 Codex 請求偽裝',
    'Отключить маскировку запросов Codex',
  ],
  modelCooling: [
    'Claude model-level cooldown',
    'Claude 模型级限额冷却',
    'Claude 模型級限額冷卻',
    'Охлаждение Claude на уровне модели',
  ],
  claudeCloak: [
    'Disable Claude request cloaking',
    '禁用 Claude 请求伪装',
    '停用 Claude 請求偽裝',
    'Отключить маскировку запросов Claude',
  ],
  searchTool: [
    'Inject x_search tool',
    '补充 x_search 工具',
    '補充 x_search 工具',
    'Добавлять инструмент x_search',
  ],
  keys: ['API keys', 'API 密钥', 'API 金鑰', 'Ключи API'],
  keysHelp: [
    'Client keys, groups and permissions.',
    '客户端访问密钥、分组与权限。',
    '用戶端存取金鑰、群組與權限。',
    'Клиентские ключи, группы и разрешения.',
  ],
  providers: ['AI providers', 'AI 提供商', 'AI 提供商', 'Провайдеры ИИ'],
  providersHelp: [
    'Provider URLs, credentials, models and overrides.',
    '提供商 URL、凭据、模型与覆盖项。',
    '提供商 URL、憑證、模型與覆寫項目。',
    'Адреса, учётные данные, модели и переопределения провайдеров.',
  ],
  models: ['Model management', '模型管理', '模型管理', 'Управление моделями'],
  modelsHelp: [
    'Model catalog, OAuth aliases and exclusions.',
    '模型目录、OAuth 别名与排除项。',
    '模型目錄、OAuth 別名與排除項目。',
    'Каталог моделей, псевдонимы OAuth и исключения.',
  ],
  auth: [
    'Credentials and credential health',
    '凭据管理 / 凭据健康',
    '憑證管理 / 憑證健康',
    'Учётные данные и их состояние',
  ],
  authHelp: [
    'Credential storage, refresh and request-level errors; auth-dir belongs to deployment.',
    '凭据存放、刷新与请求级错误；auth-dir 属于部署来源。',
    '憑證儲存、重新整理與請求級錯誤；auth-dir 屬於部署來源。',
    'Хранение, обновление и ошибки запросов; auth-dir задаётся развёртыванием.',
  ],
  live: [
    'Provider · Codex Live',
    '提供商 · Codex Live',
    '提供商 · Codex Live',
    'Провайдер · Codex Live',
  ],
  liveHelp: [
    'Media relay, ICE and TURN need dedicated credential operations.',
    '媒体中继、ICE 与 TURN 需要专用凭据操作。',
    '媒體中繼、ICE 與 TURN 需要專用憑證操作。',
    'Медиаретрансляция, ICE и TURN требуют отдельных действий с учётными данными.',
  ],
  providerSpecific: ['Owning provider', '对应提供商', '對應提供商', 'Соответствующий провайдер'],
  headersHelp: [
    'Codex OAuth header fallback and AI Studio WebSocket authentication.',
    'Codex OAuth 头部回退与 AI Studio WebSocket 认证。',
    'Codex OAuth 標頭備援與 AI Studio WebSocket 驗證。',
    'Резервные заголовки Codex OAuth и аутентификация AI Studio WebSocket.',
  ],
  specificHelp: [
    'Provider-specific filtering, pools, signatures and credits.',
    '提供商专属过滤、连接池、签名与 credits 行为。',
    '提供商專屬過濾、連線池、簽章與 credits 行為。',
    'Фильтрация, пулы, подписи и кредиты конкретных провайдеров.',
  ],
  runtime: [
    'Runtime and credential governance',
    '运行状态 / 凭据治理',
    '執行狀態 / 憑證治理',
    'Состояние и управление учётными данными',
  ],
  runtimeHelp: [
    'Runtime-owned concurrency state and read-only revision metadata.',
    '运行实例拥有的并发状态与只读 revision 元数据。',
    '執行個體擁有的並行狀態與唯讀 revision 中繼資料。',
    'Состояние параллелизма процесса и метаданные ревизии только для чтения.',
  ],
  logs: [
    'System logs and diagnostics',
    '系统日志 / 运行状态',
    '系統日誌 / 執行狀態',
    'Системные журналы и диагностика',
  ],
  logsHelp: [
    'Log levels, retention and profiling; debug listeners belong to deployment.',
    '日志级别、保留与性能诊断；调试监听属于部署边界。',
    '日誌層級、保留與效能診斷；偵錯監聽屬於部署邊界。',
    'Уровни журналов, хранение и профилирование; отладочные порты задаются развёртыванием.',
  ],
  usage: [
    'Request monitoring and usage',
    '请求监控 / 用量维护',
    '請求監控 / 用量維護',
    'Мониторинг запросов и использования',
  ],
  usageHelp: [
    'CPA collection and queue retention, not CPAMP system preferences.',
    'CPA 采集与队列保留，不属于 CPAMP 系统偏好。',
    'CPA 採集與佇列保留，不屬於 CPAMP 系統偏好。',
    'Сбор данных CPA и хранение очереди, не настройки CPAMP.',
  ],
  deployment: [
    'Runtime and deployment',
    '运行状态 / 部署信息',
    '執行狀態 / 部署資訊',
    'Состояние и развёртывание',
  ],
  deploymentHelp: [
    'Listeners, TLS, trusted proxies and CPA management protection. The CPAMP entry uses product operations.',
    '监听、TLS、受信代理与 CPA 管理保护；CPAMP 入口使用产品操作。',
    '監聽、TLS、信任代理與 CPA 管理保護；CPAMP 入口使用產品操作。',
    'Порты, TLS, доверенные прокси и защита управления CPA. Адрес CPAMP меняется операциями продукта.',
  ],
  plugins: ['Plugin management', '插件管理', '插件管理', 'Управление плагинами'],
  pluginsHelp: [
    'Plugin sources and per-plugin configuration need dedicated handling of opaque schemas and secrets.',
    '插件来源与各插件配置需要专门处理 opaque schema 与 secret。',
    '插件來源與各插件設定需要專門處理 opaque schema 與 secret。',
    'Источники и настройки плагинов требуют отдельной обработки непрозрачных схем и секретов.',
  ],
  legacy: [
    'Format and legacy compatibility',
    '格式 / 历史兼容',
    '格式 / 歷史相容',
    'Формат и совместимость со старыми версиями',
  ],
  legacyHelp: [
    'config-version is metadata. Legacy quota switches are not new product controls.',
    'config-version 是元数据，旧 quota 开关不能作为新产品开关。',
    'config-version 是中繼資料，舊 quota 開關不能作為新產品開關。',
    'config-version — метаданные. Старые переключатели quota не являются новыми настройками продукта.',
  ],
} satisfies Record<string, readonly [string, string, string, string]>;

export type ReferenceTextKey = keyof typeof messages;
export function referenceText(key: ReferenceTextKey): string {
  const language = i18n.resolvedLanguage || 'zh-CN';
  return messages[key][
    language === 'zh-TW' ? 2 : language === 'ru' ? 3 : language.startsWith('zh') ? 1 : 0
  ];
}
