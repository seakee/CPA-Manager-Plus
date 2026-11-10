import type { ManagerConfig } from '@/services/api/usageService';

/**
 * `PUT /usage-service/config` is a whole-config write, not a patch: every
 * collector field, the CPA connection and the Codex inspection schedule are
 * taken from the submitted body, and the External usage service section is
 * dropped when the body does not carry it. Switching request monitoring must
 * therefore write back the config it just read and change only
 * `collector.enabled`; anything the switch does not own has to survive.
 */
export function buildRequestMonitoringConfig(
  current: ManagerConfig,
  enabled: boolean
): ManagerConfig {
  return {
    ...current,
    collector: { ...current.collector, enabled },
  };
}
