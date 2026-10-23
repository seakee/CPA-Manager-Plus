import { configurationText as c } from '../i18n/configurationWorkspace';
import { referenceText as r, type ReferenceTextKey } from '../i18n/configurationReference';

// Ownership reference only, never a snapshot of an installation's values.
export interface GatewayFieldMeta {
  path: string;
  label: string;
  risk?: boolean;
}
export interface GatewaySectionMeta {
  id: string;
  tab: 'routing' | 'retry' | 'requests' | 'media' | 'compat';
  title: string;
  description: string;
  fields: GatewayFieldMeta[];
}
export interface GatewayOwnerMeta {
  path: string;
  owner: string;
  description: string;
  target?: string;
}
export const RETRY_COUNT_PATH = 'routing.retry.request-retry';
export const RETRY_INTERVAL_PATH = 'routing.retry.max-retry-interval';
export const COMMON_GATEWAY_PATHS = new Set([
  'routing.strategy',
  'routing.session-affinity',
  'routing.force-model-prefix',
  RETRY_COUNT_PATH,
  RETRY_INTERVAL_PATH,
  'requests.proxy-url',
  'requests.passthrough-headers',
  'requests.streaming.keepalive-seconds',
  'multimedia.disable-image-generation',
]);

export function gatewaySections(): GatewaySectionMeta[] {
  const field = (path: string, label: ReferenceTextKey, risk = false): GatewayFieldMeta => ({
    path,
    label: r(label),
    risk,
  });
  return [
    {
      id: 'routing',
      tab: 'routing',
      title: c('routingTitle'),
      description: c('routingHelp'),
      fields: [
        field('routing.strategy', 'strategy'),
        field('routing.session-affinity', 'affinity'),
        field('routing.session-affinity-ttl', 'ttl'),
        field('routing.session-affinity-subagents', 'subagents'),
        field('routing.force-model-prefix', 'prefix'),
      ],
    },
    {
      id: 'retry',
      tab: 'retry',
      title: c('retryTitle'),
      description: c('retryHelp'),
      fields: [
        field(RETRY_COUNT_PATH, 'count'),
        field('routing.retry.max-retry-credentials', 'credentials'),
        field(RETRY_INTERVAL_PATH, 'interval'),
        field('routing.cooldown.disable-cooling', 'disableCooling'),
        field('routing.cooldown.save-cooldown-status', 'persistCooling'),
        field('routing.cooldown.transient-error-cooldown-seconds', 'transient'),
      ],
    },
    {
      id: 'requests',
      tab: 'requests',
      title: c('requestsTitle'),
      description: c('requestsHelp'),
      fields: [
        field('requests.proxy-url', 'proxy'),
        field('requests.passthrough-headers', 'headers'),
        field('requests.nonstream-keepalive-interval', 'keepalive'),
        field('requests.streaming.keepalive-seconds', 'sse'),
        field('requests.streaming.bootstrap-retries', 'bootstrap'),
      ],
    },
    {
      id: 'media',
      tab: 'media',
      title: c('mediaTitle'),
      description: c('mediaHelp'),
      fields: [
        field('multimedia.disable-image-generation', 'images'),
        field('multimedia.gpt-image-2-base-model', 'imageModel'),
        field('multimedia.video-result-auth-cache-ttl', 'videoTtl'),
      ],
    },
    {
      id: 'compat',
      tab: 'compat',
      title: c('compatTitle'),
      description: c('compatHelp'),
      fields: [
        field('client.codex.enable-apply-patch', 'patch'),
        field('client.codex.optimize-multi-agent-v2', 'multiAgent'),
        field('upstream.codex.response-steering', 'steering', true),
        field('upstream.codex.disable-codex-cloaking', 'codexCloak'),
        field('upstream.claude.model-level-cooling', 'modelCooling'),
        field('upstream.claude.disable-claude-cloak-mode', 'claudeCloak'),
        field('upstream.xai.inject-x-search', 'searchTool'),
      ],
    },
  ];
}

export function gatewayOwners(): GatewayOwnerMeta[] {
  const owner = (
    path: string,
    name: ReferenceTextKey,
    help: ReferenceTextKey,
    target?: string
  ): GatewayOwnerMeta => ({ path, owner: r(name), description: r(help), target });
  return [
    owner('access.api-keys', 'keys', 'keysHelp', '/api-keys'),
    owner('api-keys.*', 'providers', 'providersHelp', '/ai-providers'),
    owner('models.* / oauth.model-alias / oauth.excluded-models', 'models', 'modelsHelp'),
    owner(
      'oauth.auth-dir / oauth.auth-auto-refresh-workers / oauth.settings / oauth.request-scoped-errors',
      'auth',
      'authHelp',
      '/accounts'
    ),
    owner('oauth.providers.codex.live-media-relay.*', 'live', 'liveHelp'),
    owner(
      'oauth.providers.codex.header-defaults.* / oauth.providers.aistudio.ws-auth',
      'providerSpecific',
      'headersHelp'
    ),
    owner(
      'oauth.providers.antigravity.* / oauth.providers.devin.*',
      'providerSpecific',
      'specificHelp'
    ),
    owner('credentials.concurrency.* / credentials.in-flight.*', 'runtime', 'runtimeHelp'),
    owner('observability.logs.* / observability.pprof.*', 'logs', 'logsHelp', '/logs'),
    owner('observability.usage.*', 'usage', 'usageHelp', '/monitoring'),
    owner('server.* / management.*', 'deployment', 'deploymentHelp', '/system'),
    owner('plugins.*', 'plugins', 'pluginsHelp', '/plugins'),
    owner(
      'config-version / quota-exceeded.switch-project / quota-exceeded.switch-preview-model',
      'legacy',
      'legacyHelp'
    ),
  ];
}
