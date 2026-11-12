import { configurationText as c } from '../i18n/configurationWorkspace';
import { referenceText as r, type ReferenceTextKey } from '../i18n/configurationReference';

// The page catalog is presentation only: which group a leaf is rendered in, what
// it is called and how it is edited. Writability, grouping and current values
// always come from Manager's projection, so this file can never invent an
// editable path.

export type GatewayTabKey =
  | 'overview'
  | 'common'
  | 'routing'
  | 'retry'
  | 'requests'
  | 'media'
  | 'compat'
  | 'owners';

export const GATEWAY_TABS: GatewayTabKey[] = [
  'overview',
  'common',
  'routing',
  'retry',
  'requests',
  'media',
  'compat',
  'owners',
];

export type GatewayFieldKind = 'number' | 'bool' | 'text' | 'duration' | 'url' | 'select';

export interface GatewayFieldMeta {
  path: string;
  labelKey: ReferenceTextKey;
  helpKey: ReferenceTextKey;
  risk?: boolean;
  /** A dependent leaf is edited only while its switch is on. */
  depends?: string;
  /**
   * Select options. `readOnly` marks a value CPA may already persist but which
   * must never be offered as a new draft: `passthrough` postdates the accepted
   * inventory, so it is shown as the current value and never as a choice.
   */
  options?: { value: string; labelKey: ReferenceTextKey; readOnly?: boolean }[];
}

export interface GatewayGroupMeta {
  id: string;
  tab: 'routing' | 'retry' | 'requests' | 'media' | 'compat';
  titleKey: Parameters<typeof c>[0];
  helpKey: Parameters<typeof c>[0];
  fields: GatewayFieldMeta[];
}

export interface GatewayFamilyMeta {
  family: string;
  path: string;
  labelKey: Parameters<typeof c>[0];
  hintKey: Parameters<typeof c>[0];
  raw: boolean;
  filter: boolean;
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
          options: [
            { value: 'round-robin', labelKey: 'strategyRoundRobin' },
            { value: 'weighted-round-robin', labelKey: 'strategyWeighted' },
            { value: 'fill-first', labelKey: 'strategyFillFirst' },
          ],
        }),
        field('routing.session-affinity', 'affinity', 'affinityHelp'),
        field('routing.session-affinity-ttl', 'ttl', 'ttlHelp', {
          depends: 'routing.session-affinity',
        }),
        field('routing.session-affinity-subagents', 'subagents', 'subagentsHelp', {
          depends: 'routing.session-affinity',
        }),
        field('routing.force-model-prefix', 'prefix', 'prefixHelp'),
      ],
    },
    {
      id: 'retry',
      tab: 'retry',
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
      tab: 'retry',
      titleKey: 'cooldownTitle',
      helpKey: 'cooldownHelp',
      fields: [
        field('routing.cooldown.disable-cooling', 'disableCooling', 'disableCoolingHelp'),
        field('routing.cooldown.save-cooldown-status', 'persistCooling', 'persistCoolingHelp'),
        field('routing.cooldown.transient-error-cooldown-seconds', 'transient', 'transientHelp'),
      ],
    },
    {
      id: 'request',
      tab: 'requests',
      titleKey: 'requestTitle',
      helpKey: 'requestHelp',
      fields: [
        field('requests.proxy-url', 'proxy', 'proxyHelp'),
        field('requests.passthrough-headers', 'headers', 'headersPassHelp'),
        field('requests.nonstream-keepalive-interval', 'keepalive', 'keepaliveHelp'),
      ],
    },
    {
      id: 'streaming',
      tab: 'requests',
      titleKey: 'streamingTitle',
      helpKey: 'streamingHelp',
      fields: [
        field('requests.streaming.keepalive-seconds', 'sse', 'sseHelp'),
        field('requests.streaming.bootstrap-retries', 'bootstrap', 'bootstrapHelp'),
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
      id: 'client',
      tab: 'compat',
      titleKey: 'clientTitle',
      helpKey: 'clientHelp',
      fields: [
        field('client.codex.enable-apply-patch', 'patch', 'patchHelp'),
        field('client.codex.optimize-multi-agent-v2', 'multiAgent', 'multiAgentHelp'),
      ],
    },
    {
      id: 'codex',
      tab: 'compat',
      titleKey: 'codexTitle',
      helpKey: 'codexHelp',
      fields: [
        field('upstream.codex.response-steering', 'steering', 'steeringHelp', { risk: true }),
        field('upstream.codex.disable-codex-cloaking', 'codexCloak', 'codexCloakHelp'),
        field('upstream.codex.stream-bootstrap-buffering', 'buffering', 'bufferingHelp', {
          risk: true,
        }),
        field('upstream.codex.stream-bootstrap-timeout', 'bufferTimeout', 'bufferTimeoutHelp', {
          depends: 'upstream.codex.stream-bootstrap-buffering',
        }),
        field('upstream.codex.orphan-delegation-compatibility', 'orphan', 'orphanHelp'),
        field('upstream.codex.model-level-cooling', 'modelCooling', 'codexModelCoolingHelp'),
      ],
    },
    {
      id: 'claude',
      tab: 'compat',
      titleKey: 'claudeTitle',
      helpKey: 'claudeHelp',
      fields: [
        field('upstream.claude.model-level-cooling', 'modelCooling', 'claudeModelCoolingHelp'),
        field('upstream.claude.disable-claude-cloak-mode', 'claudeCloak', 'claudeCloakHelp'),
        field('upstream.claude.disable-cloaking-model-list', 'rawModelIds', 'rawModelIdsHelp'),
      ],
    },
    {
      id: 'headers',
      tab: 'compat',
      titleKey: 'headersTitle',
      helpKey: 'headersHelp',
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
      ],
    },
    {
      id: 'xai',
      tab: 'compat',
      titleKey: 'xaiTitle',
      helpKey: 'xaiHelp',
      fields: [field('upstream.xai.inject-x-search', 'searchTool', 'searchToolHelp')],
    },
  ];
}

export function gatewayFamilies(): GatewayFamilyMeta[] {
  return [
    { family: 'default', path: 'requests.payload.default', labelKey: 'familyDefault', hintKey: 'familyDefaultHelp', raw: false, filter: false },
    { family: 'default-raw', path: 'requests.payload.default-raw', labelKey: 'familyDefaultRaw', hintKey: 'familyDefaultRawHelp', raw: true, filter: false },
    { family: 'override', path: 'requests.payload.override', labelKey: 'familyOverride', hintKey: 'familyOverrideHelp', raw: false, filter: false },
    { family: 'override-raw', path: 'requests.payload.override-raw', labelKey: 'familyOverrideRaw', hintKey: 'familyOverrideRawHelp', raw: true, filter: false },
    { family: 'filter', path: 'requests.payload.filter', labelKey: 'familyFilter', hintKey: 'familyFilterHelp', raw: false, filter: true },
  ];
}

export function gatewayOwners(): GatewayOwnerMeta[] {
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
    owner('models.* / oauth.model-alias / oauth.excluded-models', 'models', 'ownerDetailModels', [
      'models',
      'oauth.model-alias',
      'oauth.excluded-models',
    ]),
    owner(
      'oauth.auth-dir / oauth.auth-auto-refresh-workers / oauth.settings / oauth.request-scoped-errors',
      'auth',
      'ownerDetailAuth',
      ['oauth.auth-dir', 'oauth.auth-auto-refresh-workers', 'oauth.settings', 'oauth.request-scoped-errors'],
      '/accounts'
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
    owner('observability.usage.*', 'usage', 'ownerDetailUsage', [
      'observability.usage.usage-statistics-enabled',
      'observability.usage.redis-usage-queue-retention-seconds',
    ], '/monitoring'),
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
    ], '/system'),
    owner('plugins.*', 'plugins', 'ownerDetailPlugins', [
      'plugins.enabled',
      'plugins.dir',
      'plugins.store-sources',
      'plugins.store-auth',
      'plugins.auth-revision',
      'plugins.configs.<plugin>.enabled',
      'plugins.configs.<plugin>.priority',
      'plugins.configs.<plugin>.*',
    ], '/plugins'),
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
