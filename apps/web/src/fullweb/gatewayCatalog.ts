import { configurationText as c } from '../i18n/configurationWorkspace';
import { referenceText as r, type ReferenceTextKey } from '../i18n/configurationReference';

// The page catalog is presentation only: which group a leaf is rendered in, what
// it is called and how it is edited. Writability, grouping and current values
// always come from Manager's projection, so this file can never invent an
// editable path.

export type GatewayTabKey =
  | 'common'
  | 'routing'
  | 'network'
  | 'clients'
  | 'upstream'
  | 'payload'
  | 'media';

export const GATEWAY_TABS: GatewayTabKey[] = [
  'common',
  'routing',
  'network',
  'clients',
  'upstream',
  'payload',
  'media',
];

export type UpstreamProviderKey =
  | 'codex'
  | 'claude'
  | 'antigravity'
  | 'devin'
  | 'other'
  | 'oauth';

export const UPSTREAM_PROVIDERS: UpstreamProviderKey[] = [
  'codex',
  'claude',
  'antigravity',
  'devin',
  'other',
  'oauth',
];

export type GatewayFieldKind =
  | 'number'
  | 'bool'
  | 'text'
  | 'duration'
  | 'url'
  | 'select'
  | 'string-list'
  | 'kv-map'
  | 'pattern-list';

export interface GatewayFieldMeta {
  path: string;
  labelKey: ReferenceTextKey;
  helpKey: ReferenceTextKey;
  risk?: boolean;
  /** Search synonyms/aliases (e.g. old YAML keys, shorthand terms). */
  keywords?: string[];
  /** A dependent leaf is edited only while its switch is on. */
  depends?: string;
  options?: { value: string; labelKey: ReferenceTextKey; readOnly?: boolean }[];
}

export interface GatewayGroupMeta {
  id: string;
  tab: 'routing' | 'network' | 'clients' | 'upstream' | 'media';
  provider?: UpstreamProviderKey;
  titleKey: Parameters<typeof c>[0];
  helpKey: Parameters<typeof c>[0];
  collapsedByDefault?: boolean;
  fields: GatewayFieldMeta[];
}

export interface GatewayFamilyMeta {
  family: string;
  step: string;
  path: string;
  labelKey: Parameters<typeof c>[0];
  hintKey: Parameters<typeof c>[0];
  raw: boolean;
  filter: boolean;
  replace: boolean;
}
export interface GatewayOwnerMeta {
  path: string;
  ownerKey: ReferenceTextKey;
  detailKey: Parameters<typeof c>[0];
  target?: string;
  /** The exact paths the owning workspace edits, as the accepted inventory lists them. */
  fields: string[];
}

// The 8-field common view shares one draft with its own group: it is an alias
// view, never a second editor.
export const COMMON_GATEWAY_PATHS: Record<string, true> = {
  'routing.strategy': true,
  'routing.session-affinity': true,
  'routing.force-model-prefix': true,
  'routing.retry.request-retry': true,
  'requests.proxy-url': true,
  'requests.passthrough-headers': true,
  'requests.streaming.keepalive-seconds': true,
  'multimedia.disable-image-generation': true,
};

export function isCommonGatewayPath(path: string): boolean {
  return COMMON_GATEWAY_PATHS[path] === true;
}

function field(
  path: string,
  labelKey: ReferenceTextKey,
  helpKey: ReferenceTextKey,
  extra: Partial<GatewayFieldMeta> = {}
): GatewayFieldMeta {
  return { path, labelKey, helpKey, ...extra };
}

export function gatewayGroups(): GatewayGroupMeta[] {
  return [
    {
      id: 'routing',
      tab: 'routing',
      titleKey: 'routingTitle',
      helpKey: 'routingHelp',
      fields: [
        field('routing.strategy', 'strategy', 'strategyHelp', {
          keywords: ['wrr', 'round-robin', 'fill-first'],
          options: [
            { value: 'round-robin', labelKey: 'strategyRoundRobin' },
            { value: 'weighted-round-robin', labelKey: 'strategyWeighted' },
            { value: 'fill-first', labelKey: 'strategyFillFirst' },
          ],
        }),
        field('routing.session-affinity', 'affinity', 'affinityHelp', {
          keywords: ['sticky', '粘性'],
        }),
        field('routing.session-affinity-ttl', 'ttl', 'ttlHelp', {
          depends: 'routing.session-affinity',
          keywords: ['ttl', 'sticky'],
        }),
        field('routing.session-affinity-subagents', 'subagents', 'subagentsHelp', {
          depends: 'routing.session-affinity',
        }),
        field('routing.session-affinity-rebind-on', 'rebindOn', 'rebindOnHelp', {
          depends: 'routing.session-affinity',
          options: [
            { value: 'any', labelKey: 'rebindAny' },
            { value: 'quota', labelKey: 'rebindQuota' },
            { value: 'never', labelKey: 'rebindNever' },
          ],
        }),
        field('routing.force-model-prefix', 'prefix', 'prefixHelp'),
      ],
    },
    {
      id: 'quota',
      tab: 'routing',
      titleKey: 'quotaTitle',
      helpKey: 'quotaHelp',
      fields: [
        field('quota-exceeded.switch-project', 'switchProject', 'switchProjectHelp', {
          keywords: ['quota-exceeded', 'failover'],
        }),
        field('quota-exceeded.switch-preview-model', 'switchPreviewModel', 'switchPreviewModelHelp', {
          keywords: ['quota-exceeded', 'preview'],
        }),
      ],
    },
    {
      id: 'retry',
      tab: 'routing',
      titleKey: 'retryTitle',
      helpKey: 'retryHelp',
      fields: [
        field('routing.retry.request-retry', 'count', 'countHelp'),
        field('routing.retry.max-retry-credentials', 'credentials', 'credentialsHelp'),
        field('routing.retry.max-retry-interval', 'interval', 'intervalHelp'),
      ],
    },
    {
      id: 'cooldown',
      tab: 'routing',
      titleKey: 'cooldownTitle',
      helpKey: 'cooldownHelp',
      fields: [
        field('routing.cooldown.disable-cooling', 'disableCooling', 'disableCoolingHelp'),
        field('routing.cooldown.save-cooldown-status', 'persistCooling', 'persistCoolingHelp'),
        field('routing.cooldown.transient-error-cooldown-seconds', 'transient', 'transientHelp'),
      ],
    },
    {
      id: 'oauth',
      tab: 'upstream',
      provider: 'oauth',
      titleKey: 'oauthTitle',
      helpKey: 'oauthHelp',
      fields: [
        field('oauth.auth-auto-refresh-workers', 'authRefreshWorkers', 'authRefreshWorkersHelp', {
          keywords: ['workers', 'refresh'],
        }),
      ],
    },
    {
      id: 'request',
      tab: 'network',
      titleKey: 'requestTitle',
      helpKey: 'requestHelp',
      fields: [
        field('requests.proxy-url', 'proxy', 'proxyHelp'),
        field('requests.tls-handshake-timeout-seconds', 'tlsHandshakeTimeout', 'tlsHandshakeTimeoutHelp', {
          keywords: ['utls', 'tls'],
        }),
        field('requests.passthrough-headers', 'headers', 'headersPassHelp'),
        field('requests.nonstream-keepalive-interval', 'keepalive', 'keepaliveHelp'),
      ],
    },
    {
      id: 'streaming',
      tab: 'network',
      titleKey: 'streamingTitle',
      helpKey: 'streamingHelp',
      fields: [
        field('requests.streaming.keepalive-seconds', 'sse', 'sseHelp', { keywords: ['sse'] }),
        field('requests.streaming.bootstrap-retries', 'bootstrap', 'bootstrapHelp'),
        field(
          'requests.streaming.send-keepalive-before-ft',
          'sendKeepaliveBeforeFt',
          'sendKeepaliveBeforeFtHelp',
          { keywords: ['first-token', 'sse'] }
        ),
      ],
    },
    {
      id: 'server',
      tab: 'network',
      titleKey: 'serverTitle',
      helpKey: 'serverHelp',
      collapsedByDefault: true,
      fields: [
        field('server.max-request-body-bytes', 'maxRequestBodyBytes', 'maxRequestBodyBytesHelp'),
        field('server.shutdown-timeout', 'shutdownTimeout', 'shutdownTimeoutHelp'),
        field('server.commercial-mode', 'commercialMode', 'commercialModeHelp', {
          keywords: ['commercial-mode'],
        }),
      ],
    },
    {
      id: 'compression',
      tab: 'network',
      titleKey: 'compressionTitle',
      helpKey: 'compressionHelp',
      collapsedByDefault: true,
      fields: [
        field('server.response-compression.enabled', 'respCompressionEnabled', 'respCompressionEnabledHelp', {
          keywords: ['gzip', 'zstd', 'compress'],
        }),
        field('server.response-compression.min-bytes', 'respCompressionMinBytes', 'respCompressionMinBytesHelp'),
      ],
    },
    {
      id: 'aistudio',
      tab: 'upstream',
      provider: 'other',
      titleKey: 'aistudioTitle',
      helpKey: 'aistudioHelp',
      fields: [
        field('oauth.providers.aistudio.ws-auth', 'aistudioWsAuth', 'aistudioWsAuthHelp', {
          keywords: ['ws-auth', 'websocket'],
        }),
      ],
    },
    {
      id: 'media',
      tab: 'media',
      titleKey: 'mediaTitle',
      helpKey: 'mediaHelp',
      fields: [
        field('multimedia.disable-image-generation', 'images', 'imagesHelp', {
          options: [
            { value: 'false', labelKey: 'imagesOff' },
            { value: 'true', labelKey: 'imagesAll' },
            { value: 'chat', labelKey: 'imagesChat' },
            { value: 'passthrough', labelKey: 'imagesPassthrough', readOnly: true },
          ],
        }),
        field('multimedia.gpt-image-2-base-model', 'imageModel', 'imageModelHelp'),
        field('multimedia.video-result-auth-cache-ttl', 'videoTtl', 'videoTtlHelp'),
      ],
    },
    {
      id: 'live_relay',
      tab: 'media',
      titleKey: 'liveRelayTitle',
      helpKey: 'liveRelayHelp',
      collapsedByDefault: true,
      fields: [
        field('oauth.providers.codex.live-media-relay.enabled', 'liveRelayEnabled', 'liveRelayEnabledHelp', {
          keywords: ['webrtc', 'turn', 'relay'],
        }),
        field('oauth.providers.codex.live-media-relay.max-sessions', 'liveRelayMaxSessions', 'liveRelayMaxSessionsHelp'),
        field(
          'oauth.providers.codex.live-media-relay.disable-private-remote-ips',
          'liveRelayDisablePrivateIps',
          'liveRelayDisablePrivateIpsHelp'
        ),
        field('oauth.providers.codex.live-media-relay.public-ip', 'liveRelayPublicIp', 'liveRelayPublicIpHelp'),
        field('oauth.providers.codex.live-media-relay.udp-port-min', 'liveRelayUdpMin', 'liveRelayUdpMinHelp'),
        field('oauth.providers.codex.live-media-relay.udp-port-max', 'liveRelayUdpMax', 'liveRelayUdpMaxHelp'),
      ],
    },
    {
      id: 'client',
      tab: 'clients',
      titleKey: 'clientTitle',
      helpKey: 'clientHelp',
      fields: [
        field('client.codex.enable-apply-patch', 'patch', 'patchHelp'),
        field('client.codex.optimize-multi-agent-v2', 'multiAgent', 'multiAgentHelp'),
        field('client.codex.web-search-result-cache.enabled', 'webSearchCacheEnabled', 'webSearchCacheEnabledHelp'),
        field('client.codex.web-search-result-cache.ttl', 'webSearchCacheTtl', 'webSearchCacheTtlHelp'),
        field('client.codex.web-search-result-cache.limit', 'webSearchCacheLimit', 'webSearchCacheLimitHelp'),
      ],
    },
    {
      id: 'client_claude',
      tab: 'clients',
      titleKey: 'clientClaudeTitle',
      helpKey: 'clientClaudeHelp',
      fields: [
        field('client.claude.show-codex-commentary', 'showCodexCommentary', 'showCodexCommentaryHelp'),
      ],
    },
    {
      id: 'codex',
      tab: 'upstream',
      provider: 'codex',
      titleKey: 'codexTitle',
      helpKey: 'codexHelp',
      fields: [
        field('upstream.codex.response-steering', 'steering', 'steeringHelp', { risk: true }),
        field('upstream.codex.disable-codex-cloaking', 'codexCloak', 'codexCloakHelp', { keywords: ['cloak'] }),
        field('upstream.codex.stream-bootstrap-buffering', 'buffering', 'bufferingHelp', {
          risk: true,
        }),
        field('upstream.codex.stream-bootstrap-timeout', 'bufferTimeout', 'bufferTimeoutHelp', {
          depends: 'upstream.codex.stream-bootstrap-buffering',
        }),
        field('upstream.codex.orphan-delegation-compatibility', 'orphan', 'orphanHelp'),
        field('upstream.codex.model-level-cooling', 'codexModelCooling', 'codexModelCoolingHelp'),
        field('upstream.codex.upstream-request-compression', 'codexReqCompression', 'codexReqCompressionHelp', {
          keywords: ['zstd', 'compress'],
        }),
        field(
          'upstream.codex.upstream-request-compression-min-bytes',
          'codexReqCompressionMinBytes',
          'codexReqCompressionMinBytesHelp'
        ),
        field('upstream.codex.websocket-compression', 'codexWsCompression', 'codexWsCompressionHelp'),
      ],
    },
    {
      id: 'codex_oauth',
      tab: 'upstream',
      provider: 'codex',
      titleKey: 'codexOauthTitle',
      helpKey: 'codexOauthHelp',
      collapsedByDefault: true,
      fields: [
        field('oauth.providers.codex.signin-with-chatgpt-identifier', 'codexSiwcId', 'codexSiwcIdHelp', {
          keywords: ['siwc'],
        }),
        field('oauth.providers.codex.header-defaults.user-agent', 'codexHeaderUa', 'codexHeaderUaHelp'),
        field('oauth.providers.codex.header-defaults.beta-features', 'codexHeaderBeta', 'codexHeaderBetaHelp'),
      ],
    },
    {
      id: 'claude',
      tab: 'upstream',
      provider: 'claude',
      titleKey: 'claudeTitle',
      helpKey: 'claudeHelp',
      fields: [
        field('upstream.claude.model-level-cooling', 'modelCooling', 'claudeModelCoolingHelp'),
        field('upstream.claude.disable-claude-cloak-mode', 'claudeCloak', 'claudeCloakHelp', { keywords: ['cloak'] }),
        field('upstream.claude.disable-cloaking-model-list', 'rawModelIds', 'rawModelIdsHelp'),
        field('upstream.claude.tool-search', 'claudeToolSearch', 'claudeToolSearchHelp'),
        field('upstream.claude.server-side-fallback', 'claudeServerFallback', 'claudeServerFallbackHelp'),
        field('upstream.claude.session-date-pin-days', 'claudeSessionDatePinDays', 'claudeSessionDatePinDaysHelp'),
      ],
    },
    {
      id: 'headers',
      tab: 'upstream',
      provider: 'claude',
      titleKey: 'headersTitle',
      helpKey: 'headersHelp',
      collapsedByDefault: true,
      fields: [
        field('upstream.claude.header-defaults.user-agent', 'userAgent', 'userAgentHelp'),
        field('upstream.claude.header-defaults.package-version', 'packageVersion', 'packageVersionHelp'),
        field('upstream.claude.header-defaults.runtime-version', 'runtimeVersion', 'runtimeVersionHelp'),
        field('upstream.claude.header-defaults.os', 'os', 'osHelp'),
        field('upstream.claude.header-defaults.arch', 'arch', 'archHelp'),
        field('upstream.claude.header-defaults.timeout', 'headerTimeout', 'headerTimeoutHelp'),
        field('upstream.claude.header-defaults.timezone', 'timezone', 'timezoneHelp'),
        field(
          'upstream.claude.header-defaults.stabilize-device-profile',
          'deviceProfile',
          'deviceProfileHelp'
        ),
        field(
          'upstream.claude.header-defaults.inject-diagnostics',
          'claudeInjectDiagnostics',
          'claudeInjectDiagnosticsHelp'
        ),
        field('upstream.claude.header-defaults.client-request-id', 'claudeClientReqId', 'claudeClientReqIdHelp', {
          options: [
            { value: '', labelKey: 'clientReqIdDefault' },
            { value: 'on', labelKey: 'clientReqIdOn' },
            { value: 'off', labelKey: 'clientReqIdOff' },
            { value: 'passthrough', labelKey: 'clientReqIdPassthrough' },
          ],
        }),
        field(
          'upstream.claude.header-defaults.native-entrypoints',
          'claudeNativeEntrypoints',
          'claudeNativeEntrypointsHelp'
        ),
        field('upstream.claude.header-defaults.anthropic-beta-strip', 'claudeBetaStrip', 'claudeBetaStripHelp'),
        field('upstream.claude.header-defaults.anthropic-beta-append', 'claudeBetaAppend', 'claudeBetaAppendHelp'),
      ],
    },
    {
      id: 'antigravity',
      tab: 'upstream',
      provider: 'antigravity',
      titleKey: 'antigravityTitle',
      helpKey: 'antigravityHelp',
      fields: [
        field('oauth.providers.antigravity.antigravity-credits', 'agCredits', 'agCreditsHelp', {
          keywords: ['credits', 'google one'],
        }),
        field('oauth.providers.antigravity.signature-cache-enabled', 'agSigCache', 'agSigCacheHelp'),
        field('oauth.providers.antigravity.signature-bypass-strict', 'agSigBypassStrict', 'agSigBypassStrictHelp'),
        field('oauth.providers.antigravity.tos-pause-window', 'agTosPause', 'agTosPauseHelp', {
          keywords: ['tos', '403'],
        }),
        field('oauth.providers.antigravity.connection-pool.enabled', 'agPoolEnabled', 'agPoolEnabledHelp'),
        field(
          'oauth.providers.antigravity.connection-pool.idle-conn-timeout',
          'agPoolIdleTimeout',
          'agPoolIdleTimeoutHelp'
        ),
        field(
          'oauth.providers.antigravity.connection-pool.max-idle-conns-per-host',
          'agPoolMaxIdle',
          'agPoolMaxIdleHelp'
        ),
        field('oauth.providers.antigravity.sensitive-words', 'sensitiveWords', 'sensitiveWordsHelp', {
          keywords: ['2119', 'zwsp', 'sensitive'],
        }),
        field(
          'oauth.providers.antigravity.sensitive-words-replace',
          'sensitiveWordsReplace',
          'sensitiveWordsReplaceHelp',
          { keywords: ['2119', 'replace', 'sensitive'] }
        ),
        field(
          'oauth.providers.antigravity.sensitive-patterns-replace',
          'sensitivePatternsReplace',
          'sensitivePatternsReplaceHelp',
          { keywords: ['re2', 'regex', 'sensitive'] }
        ),
      ],
    },
    {
      id: 'devin',
      tab: 'upstream',
      provider: 'devin',
      titleKey: 'devinTitle',
      helpKey: 'devinHelp',
      fields: [
        field('oauth.providers.devin.sensitive-words', 'sensitiveWords', 'sensitiveWordsHelp', {
          keywords: ['sensitive'],
        }),
        field('oauth.providers.devin.sensitive-words-replace', 'sensitiveWordsReplace', 'sensitiveWordsReplaceHelp'),
        field(
          'oauth.providers.devin.sensitive-patterns-replace',
          'sensitivePatternsReplace',
          'sensitivePatternsReplaceHelp'
        ),
      ],
    },
    {
      id: 'xai',
      tab: 'upstream',
      provider: 'other',
      titleKey: 'xaiTitle',
      helpKey: 'xaiHelp',
      fields: [field('upstream.xai.inject-x-search', 'searchTool', 'searchToolHelp')],
    },
  ];
}

export function gatewayFamilies(): GatewayFamilyMeta[] {
  return [
    { family: 'default', step: '01', path: 'requests.payload.default', labelKey: 'familyDefault', hintKey: 'familyDefaultHelp', raw: false, filter: false, replace: false },
    { family: 'default-raw', step: '02', path: 'requests.payload.default-raw', labelKey: 'familyDefaultRaw', hintKey: 'familyDefaultRawHelp', raw: true, filter: false, replace: false },
    { family: 'override', step: '03', path: 'requests.payload.override', labelKey: 'familyOverride', hintKey: 'familyOverrideHelp', raw: false, filter: false, replace: false },
    { family: 'override-raw', step: '04', path: 'requests.payload.override-raw', labelKey: 'familyOverrideRaw', hintKey: 'familyOverrideRawHelp', raw: true, filter: false, replace: false },
    { family: 'replace', step: '05', path: 'requests.payload.replace', labelKey: 'familyReplace', hintKey: 'familyReplaceHelp', raw: false, filter: false, replace: true },
    { family: 'filter', step: '06', path: 'requests.payload.filter', labelKey: 'familyFilter', hintKey: 'familyFilterHelp', raw: false, filter: true, replace: false },
  ];
}

export function gatewayOwners(mode?: string): GatewayOwnerMeta[] {
  const owner = (
    path: string,
    ownerKey: ReferenceTextKey,
    detailKey: Parameters<typeof c>[0],
    fields: string[],
    target?: string
  ): GatewayOwnerMeta => ({ path, ownerKey, detailKey, fields, target });
  return [
    owner('access.api-keys', 'keys', 'ownerDetailKeys', ['access.api-keys'], '/api-keys'),
    owner(
      'api-keys.*',
      'providers',
      'ownerDetailProviders',
      [
        'api-keys.<provider>[].api-key',
        'api-keys.<provider>[].base-url',
        'api-keys.<provider>[].prefix',
        'api-keys.<provider>[].priority',
        'api-keys.<provider>[].weight',
        'api-keys.<provider>[].proxy-url',
        'api-keys.<provider>[].headers',
        'api-keys.<provider>[].models',
        'api-keys.<provider>[].excluded-models',
      ],
      '/ai-providers'
    ),
    owner('models.*', 'models', 'ownerDetailModels', ['models']),
    owner(
      'oauth.model-alias / oauth.excluded-models',
      'auth',
      'ownerDetailOauthModels',
      ['oauth.model-alias', 'oauth.excluded-models'],
      '/accounts'
    ),
    owner(
      'oauth.auth-dir / oauth.auth-auto-refresh-workers / oauth.settings / oauth.request-scoped-errors',
      'auth',
      'ownerDetailAuth',
      ['oauth.auth-dir', 'oauth.auth-auto-refresh-workers', 'oauth.settings', 'oauth.request-scoped-errors']
    ),
    owner('oauth.providers.codex.live-media-relay.*', 'live', 'ownerDetailLive', [
      'oauth.providers.codex.live-media-relay.enabled',
      'oauth.providers.codex.live-media-relay.max-sessions',
      'oauth.providers.codex.live-media-relay.disable-private-remote-ips',
      'oauth.providers.codex.live-media-relay.public-ip',
      'oauth.providers.codex.live-media-relay.udp-port-min',
      'oauth.providers.codex.live-media-relay.udp-port-max',
      'oauth.providers.codex.live-media-relay.ice-servers[].urls',
      'oauth.providers.codex.live-media-relay.ice-servers[].username',
      'oauth.providers.codex.live-media-relay.ice-servers[].credential',
    ]),
    owner(
      'oauth.providers.codex.header-defaults.* / oauth.providers.aistudio.ws-auth',
      'providerSpecific',
      'ownerDetailProviderHeaders',
      [
        'oauth.providers.codex.header-defaults.user-agent',
        'oauth.providers.codex.header-defaults.beta-features',
        'oauth.providers.aistudio.ws-auth',
      ]
    ),
    owner('oauth.providers.antigravity.* / oauth.providers.devin.*', 'providerSpecific', 'ownerDetailSpecific', [
      'oauth.providers.antigravity.sensitive-words',
      'oauth.providers.antigravity.signature-cache-enabled',
      'oauth.providers.antigravity.signature-bypass-strict',
      'oauth.providers.antigravity.antigravity-credits',
      'oauth.providers.antigravity.connection-pool.enabled',
      'oauth.providers.antigravity.connection-pool.idle-conn-timeout',
      'oauth.providers.antigravity.connection-pool.max-idle-conns-per-host',
      'oauth.providers.devin.sensitive-words',
    ]),
    owner('credentials.concurrency.* / credentials.in-flight.*', 'runtime', 'ownerDetailRuntime', [
      'credentials.concurrency',
      'credentials.in-flight',
    ]),
    owner('observability.logs.*', 'logs', 'ownerDetailLogs', [
      'observability.logs.debug',
      'observability.logs.logging-to-file',
      'observability.logs.logs-max-total-size-mb',
      'observability.logs.error-logs-max-files',
      'observability.logs.request-log',
    ], '/logs'),
    owner('observability.pprof.*', 'deployment', 'ownerDetailPprof', [
      'observability.pprof.enable',
      'observability.pprof.addr',
    ]),
    owner(
      'observability.usage.usage-statistics-enabled',
      'usage',
      'ownerDetailUsageStats',
      ['observability.usage.usage-statistics-enabled'],
      mode === 'external' ? '/api-keys' : undefined
    ),
    owner(
      'observability.usage.redis-usage-queue-retention-seconds',
      'usage',
      'ownerDetailUsageRetention',
      ['observability.usage.redis-usage-queue-retention-seconds']
    ),
    owner('server.* / management.*', 'deployment', 'ownerDetailDeployment', [
      'server.host',
      'server.port',
      'server.trusted-proxies',
      'server.tls.enable',
      'server.tls.cert',
      'server.tls.key',
      'server.commercial-mode',
      'server.discovery.enabled',
      'server.discovery.service-name',
      'server.discovery.service-type',
      'server.discovery.subtypes',
      'server.discovery.interfaces.include',
      'server.discovery.interfaces.exclude',
      'server.discovery.auth-required',
      'server.discovery.advertise-management',
      'management.allow-remote',
      'management.secret-key',
      'management.disable-control-panel',
      'management.disable-auto-update-panel',
      'management.panel-github-repository',
      'management.base-url',
    ]),
    owner(
      'plugins.enabled / plugins.configs.*',
      'plugins',
      'ownerDetailPlugins',
      [
        'plugins.enabled',
        'plugins.configs.<plugin>.enabled',
        'plugins.configs.<plugin>.priority',
        'plugins.configs.<plugin>.*',
      ],
      '/plugins'
    ),
    owner(
      'plugins.dir / plugins.store-sources / plugins.store-auth / plugins.auth-revision',
      'plugins',
      'ownerDetailPluginsHost',
      [
        'plugins.dir',
        'plugins.store-sources',
        'plugins.store-auth',
        'plugins.auth-revision',
      ]
    ),
    owner(
      'config-version / quota-exceeded.switch-project / quota-exceeded.switch-preview-model',
      'legacy',
      'ownerDetailLegacy',
      ['config-version', 'quota-exceeded.switch-project', 'quota-exceeded.switch-preview-model']
    ),
  ];
}

/** The field metadata of one path, so a Manager projection never guesses a control. */
export function gatewayFieldMeta(groups: GatewayGroupMeta[], path: string): GatewayFieldMeta | undefined {
  for (const group of groups) {
    const found = group.fields.find((item) => item.path === path);
    if (found) return found;
  }
  return undefined;
}

export { r as gatewayReferenceText };
