import { describe, expect, it } from 'vitest';
import {
  buildAccountModelRuleDiff,
  buildAccountModelRuleProjection,
  getAccountModelExclusionPriority,
  matchesAccountModelRule,
  setEffectiveAccountModelExactRule,
  resolveAccountModelRuleIdentity,
  setAccountModelExactRule,
} from './accountModelRules';

describe('accountModelRules', () => {
  it('matches exact and wildcard rules case-insensitively', () => {
    expect(matchesAccountModelRule('GPT-5-Codex', 'gpt-5-codex')).toBe(true);
    expect(matchesAccountModelRule('gpt-5-mini', 'gpt-5-*')).toBe(true);
    expect(matchesAccountModelRule('gpt-5-mini', 'gpt-5.*')).toBe(false);
    expect(matchesAccountModelRule('claude-sonnet', '*-mini')).toBe(false);
  });


  it('recognizes the verified CPA precedence boundary without guessing custom builds', () => {
    expect(getAccountModelExclusionPriority('v7.3.4')).toBe('merge');
    expect(getAccountModelExclusionPriority('v8.0.13')).toBe('merge');
    expect(getAccountModelExclusionPriority('v8.0.32')).toBe('merge');
    expect(getAccountModelExclusionPriority('v8.0.33')).toBe('override');
    expect(getAccountModelExclusionPriority('v8.0.40')).toBe('override');
    expect(getAccountModelExclusionPriority('v8.0.40-dev')).toBe('unknown');
    expect(getAccountModelExclusionPriority(null)).toBe('unknown');
  });

  it('shows effective exclusions instead of inactive global matches for newer CPA', () => {
    const args = {
      provider: 'codex',
      runtimeModels: [{ id: 'gpt-5.6-terra' }, { id: 'gpt-6-luna' }],
      modelDefinitions: [],
      credentialRules: ['gpt-6-luna'],
      globalRules: { codex: ['gpt-5.6-terra'] },
    };
    const overridden = buildAccountModelRuleProjection({
      ...args,
      exclusionPriority: 'override',
    });
    expect(overridden.rows.map((row) => [row.id, row.scope])).toEqual([
      ['gpt-5.6-terra', 'available'],
      ['gpt-6-luna', 'credential'],
    ]);
    const merged = buildAccountModelRuleProjection({ ...args, exclusionPriority: 'merge' });
    expect(merged.rows.map((row) => [row.id, row.scope])).toEqual([
      ['gpt-5.6-terra', 'global'],
      ['gpt-6-luna', 'credential'],
    ]);
  });

  it('restores global exclusions when the credential list becomes empty', () => {
    const p = buildAccountModelRuleProjection({
      provider: 'codex',
      runtimeModels: [{ id: 'gpt-5.6-terra' }],
      modelDefinitions: [],
      credentialRules: [],
      globalRules: { codex: ['gpt-5.6-terra'] },
      exclusionPriority: 'override',
    });
    expect(p.rows[0].scope).toBe('global');
  });

  it('does not classify conflicting global-only exclusions when CPA precedence is unknown', () => {
    const p = buildAccountModelRuleProjection({
      provider: 'codex',
      runtimeModels: [{ id: 'gpt-5.6-terra' }, { id: 'gpt-6-luna' }],
      modelDefinitions: [],
      credentialRules: ['gpt-6-luna'],
      globalRules: { codex: ['gpt-5.6-terra'] },
      exclusionPriority: 'unknown',
    });
    expect(p.rows.map((row) => row.scope)).toEqual(['unknown', 'credential']);
  });

  it('copies global exclusions before the first credential-only edit', () => {
    expect(setEffectiveAccountModelExactRule({
      credentialRules: [],
      globalRules: ['gpt-5.6-terra'],
      exclusionPriority: 'override',
      modelId: 'gpt-6-luna',
      excluded: true,
    })).toEqual(['gpt-5.6-terra', 'gpt-6-luna']);
  });

  it('rejects an empty override that would re-enable a globally denied exact model', () => {
    expect(setEffectiveAccountModelExactRule({
      credentialRules: [],
      globalRules: ['gpt-5.6-terra'],
      exclusionPriority: 'override',
      modelId: 'gpt-5.6-terra',
      excluded: false,
    })).toBeNull();
    expect(setEffectiveAccountModelExactRule({
      credentialRules: ['gpt-5.6-terra'],
      globalRules: ['gpt-5.6-terra'],
      exclusionPriority: 'override',
      modelId: 'gpt-5.6-terra',
      excluded: false,
    })).toBeNull();
  });

  it('keeps wildcard exclusions intact when editing a different exact model', () => {
    expect(setEffectiveAccountModelExactRule({
      credentialRules: [],
      globalRules: ['gpt-5.*'],
      exclusionPriority: 'override',
      modelId: 'gpt-6-luna',
      excluded: true,
    })).toEqual(['gpt-5.*', 'gpt-6-luna']);
  });

  it('combines runtime models, excluded definitions, and custom exact rules', () => {
    const projection = buildAccountModelRuleProjection({
      provider: 'codex',
      runtimeModels: [{ id: 'gpt-5-codex', display_name: 'Codex' }],
      modelDefinitions: [
        { id: 'gpt-5-codex', display_name: 'Codex static' },
        { id: 'gpt-5-mini', display_name: 'Mini' },
        { id: 'unrelated-model' },
      ],
      credentialRules: ['gpt-5-mini', 'custom-private-model'],
      globalRules: { codex: ['gpt-5-*'] },
    });

    expect(projection.rows.map((row) => row.id)).toEqual(['gpt-5-codex', 'gpt-5-mini']);
    expect(projection.rows[0]).toMatchObject({
      runtimeAvailable: true,
      scope: 'global',
    });
    expect(projection.rows[1]).toMatchObject({
      runtimeAvailable: false,
      scope: 'both',
      hasCredentialExactRule: true,
    });
    expect(projection.advancedCredentialRules).toEqual(['custom-private-model']);
    expect(projection.advancedGlobalRules).toEqual(['gpt-5-*']);
  });

  it('preserves the provider model id while using a normalized key for matching', () => {
    const projection = buildAccountModelRuleProjection({
      provider: 'codex',
      runtimeModels: [{ id: 'GPT-5-Codex', display_name: 'Codex' }],
      modelDefinitions: [{ id: 'gpt-5-codex', display_name: 'Static Codex' }],
      credentialRules: ['gpt-5-codex'],
      globalRules: {},
    });

    expect(projection.rows).toHaveLength(1);
    expect(projection.rows[0]).toMatchObject({
      id: 'GPT-5-Codex',
      display_name: 'Codex',
      hasCredentialExactRule: true,
      scope: 'credential',
    });
  });

  it('combines Gemini and Gemini CLI global rule aliases', () => {
    const projection = buildAccountModelRuleProjection({
      provider: 'gemini-cli',
      runtimeModels: [{ id: 'gemini-2.5-pro' }, { id: 'gemini-2.5-flash' }],
      modelDefinitions: [],
      credentialRules: [],
      globalRules: {
        gemini: ['gemini-2.5-pro'],
        'gemini-cli': ['gemini-2.5-flash'],
      },
    });

    expect(projection.rows.every((row) => row.scope === 'global')).toBe(true);
    expect(projection.globalRules).toEqual(['gemini-2.5-flash', 'gemini-2.5-pro']);
  });

  it('marks wildcard-only credential exclusions as advanced rules', () => {
    const projection = buildAccountModelRuleProjection({
      provider: 'claude',
      runtimeModels: [],
      modelDefinitions: [{ id: 'claude-sonnet' }, { id: 'claude-haiku' }],
      credentialRules: ['claude-*'],
      globalRules: {},
    });

    expect(projection.rows).toHaveLength(2);
    expect(projection.rows.every((row) => row.hasCredentialWildcardRule)).toBe(true);
    expect(projection.advancedCredentialRules).toEqual(['claude-*']);
  });

  it('keeps unknown global state distinct and projects shared-source exclusions', () => {
    const unknownProjection = buildAccountModelRuleProjection({
      provider: 'codex',
      runtimeModels: [{ id: 'unknown-model' }],
      modelDefinitions: [],
      credentialRules: [],
      globalRules: { codex: ['unknown-model'] },
      globalRulesKnown: false,
    });
    const sharedProjection = buildAccountModelRuleProjection({
      provider: 'codex',
      runtimeModels: [{ id: 'shared-model' }, { id: 'shared-global-model' }],
      modelDefinitions: [],
      credentialRules: ['shared-model', 'shared-global-model'],
      globalRules: { codex: ['shared-global-model'] },
      credentialRulesShared: true,
    });

    expect(unknownProjection.rows).toMatchObject([{ id: 'unknown-model', scope: 'unknown' }]);
    expect(sharedProjection.rows).toMatchObject([
      { id: 'shared-model', scope: 'shared' },
      { id: 'shared-global-model', scope: 'shared-global' },
    ]);
  });

  it('adds and removes only the selected exact rule', () => {
    expect(setAccountModelExactRule(['gpt-*'], 'GPT-5-Codex', true)).toEqual([
      'gpt-*',
      'gpt-5-codex',
    ]);
    expect(setAccountModelExactRule(['gpt-*', 'gpt-5-codex'], 'gpt-5-codex', false)).toEqual([
      'gpt-*',
    ]);
  });

  it('builds added, removed, and unchanged rule previews', () => {
    expect(buildAccountModelRuleDiff('model-a\nmodel-b', 'model-b, model-c')).toEqual({
      added: ['model-c'],
      removed: ['model-a'],
      unchanged: ['model-b'],
    });
  });

  it('resolves prefixed runtime model to canonical rule id using static definitions (Case 1)', () => {
    const projection = buildAccountModelRuleProjection({
      provider: 'codex',
      credentialPrefix: 'haochi',
      runtimeModels: [{ id: 'haochi/gpt-5.5' }],
      modelDefinitions: [{ id: 'gpt-5.5' }],
      credentialRules: [],
      globalRules: {},
    });

    expect(projection.rows).toHaveLength(1);
    expect(projection.rows[0]).toMatchObject({
      id: 'haochi/gpt-5.5',
      ruleModelId: 'gpt-5.5',
      ruleModelIdResolved: true,
      runtimeAvailable: true,
    });
  });

  it('matches canonical exact rule against prefixed runtime model (Case 2)', () => {
    const projection = buildAccountModelRuleProjection({
      provider: 'codex',
      credentialPrefix: 'haochi',
      runtimeModels: [{ id: 'haochi/gpt-5.5' }],
      modelDefinitions: [{ id: 'gpt-5.5' }],
      credentialRules: ['gpt-5.5'],
      globalRules: {},
    });

    expect(projection.rows).toHaveLength(1);
    expect(projection.rows[0]).toMatchObject({
      id: 'haochi/gpt-5.5',
      ruleModelId: 'gpt-5.5',
      scope: 'credential',
      hasCredentialExactRule: true,
    });
  });

  it('does not treat historical prefixed exact rule as matching and moves it to advanced rules (Case 3)', () => {
    const projection = buildAccountModelRuleProjection({
      provider: 'codex',
      credentialPrefix: 'haochi',
      runtimeModels: [{ id: 'haochi/gpt-5.5' }],
      modelDefinitions: [{ id: 'gpt-5.5' }],
      credentialRules: ['haochi/gpt-5.5'],
      globalRules: {},
    });

    expect(projection.rows).toHaveLength(1);
    expect(projection.rows[0]).toMatchObject({
      id: 'haochi/gpt-5.5',
      ruleModelId: 'gpt-5.5',
      scope: 'available',
      hasCredentialExactRule: false,
    });
    expect(projection.advancedCredentialRules).toContain('haochi/gpt-5.5');
  });

  it('matches canonical wildcard against prefixed runtime model (Case 4)', () => {
    const projection = buildAccountModelRuleProjection({
      provider: 'codex',
      credentialPrefix: 'haochi',
      runtimeModels: [{ id: 'haochi/gpt-5-codex' }],
      modelDefinitions: [{ id: 'gpt-5-codex' }],
      credentialRules: ['gpt-5-*'],
      globalRules: {},
    });

    expect(projection.rows).toHaveLength(1);
    expect(projection.rows[0]).toMatchObject({
      id: 'haochi/gpt-5-codex',
      ruleModelId: 'gpt-5-codex',
      scope: 'credential',
      hasCredentialWildcardRule: true,
      hasCredentialExactRule: false,
    });
  });

  it('prioritizes exact definition match over prefix removal for slash model ids (Case 5)', () => {
    const identity = resolveAccountModelRuleIdentity({
      modelId: 'haochi/foo',
      credentialPrefix: 'haochi',
      modelDefinitions: [{ id: 'haochi/foo' }, { id: 'foo' }],
    });

    expect(identity).toEqual({
      ruleModelId: 'haochi/foo',
      ruleModelIdResolved: true,
    });

    const projection = buildAccountModelRuleProjection({
      provider: 'codex',
      credentialPrefix: 'haochi',
      runtimeModels: [{ id: 'haochi/foo' }],
      modelDefinitions: [{ id: 'haochi/foo' }, { id: 'foo' }],
      credentialRules: ['haochi/foo'],
      globalRules: {},
    });

    expect(projection.rows[0]).toMatchObject({
      id: 'haochi/foo',
      ruleModelId: 'haochi/foo',
      ruleModelIdResolved: true,
      hasCredentialExactRule: true,
      scope: 'credential',
    });
  });

  it('does not strip similar prefix without full slash segment (Case 6)', () => {
    const identity = resolveAccountModelRuleIdentity({
      modelId: 'haochi2/gpt-5.5',
      credentialPrefix: 'haochi',
      modelDefinitions: [{ id: 'gpt-5.5' }],
    });

    expect(identity).toEqual({
      ruleModelId: 'haochi2/gpt-5.5',
      ruleModelIdResolved: true,
    });
  });

  it('resolves prefix case-insensitively (Case 7)', () => {
    const identity = resolveAccountModelRuleIdentity({
      modelId: 'haochi/GPT-5.5',
      credentialPrefix: 'HaoChi',
      modelDefinitions: [{ id: 'gpt-5.5' }],
    });

    expect(identity).toEqual({
      ruleModelId: 'gpt-5.5',
      ruleModelIdResolved: true,
    });
  });

  it.each(['haochi', 'haochi/', '/haochi', '/haochi/', '  /haochi/  '])(
    'normalizes equivalent prefix variation "%s" to canonical rule id',
    (credentialPrefix) => {
      const identity = resolveAccountModelRuleIdentity({
        modelId: 'haochi/gpt-5.5',
        credentialPrefix,
        modelDefinitions: [{ id: 'gpt-5.5' }],
      });

      expect(identity).toEqual({
        ruleModelId: 'gpt-5.5',
        ruleModelIdResolved: true,
      });

      const projection = buildAccountModelRuleProjection({
        provider: 'codex',
        credentialPrefix,
        runtimeModels: [{ id: 'haochi/gpt-5.5' }],
        modelDefinitions: [{ id: 'gpt-5.5' }],
        credentialRules: [],
        globalRules: {},
      });

      expect(projection.rows).toHaveLength(1);
      expect(projection.rows[0]).toMatchObject({
        id: 'haochi/gpt-5.5',
        ruleModelId: 'gpt-5.5',
        ruleModelIdResolved: true,
        runtimeAvailable: true,
      });

      const updatedRules = setAccountModelExactRule(
        projection.credentialRules,
        projection.rows[0].ruleModelId,
        true,
        projection.rows[0].equivalentRuntimeModelIds
      );

      expect(updatedRules).toEqual(['gpt-5.5']);
    }
  );

  it('fails closed as unknown when prefixed candidate cannot be verified (Case 8)', () => {
    const projection = buildAccountModelRuleProjection({
      provider: 'codex',
      credentialPrefix: 'haochi',
      runtimeModels: [{ id: 'haochi/private-model' }],
      modelDefinitions: [],
      credentialRules: ['haochi/private-model'],
      globalRules: {},
    });

    expect(projection.rows).toHaveLength(1);
    expect(projection.rows[0]).toMatchObject({
      id: 'haochi/private-model',
      ruleModelId: 'haochi/private-model',
      ruleModelIdResolved: false,
      scope: 'unknown',
      credentialPatterns: [],
      globalPatterns: [],
      hasCredentialExactRule: false,
      hasCredentialWildcardRule: false,
    });
    expect(projection.advancedCredentialRules).toEqual(['haochi/private-model']);
  });

  it('preserves both runtime routes without merging when force-model-prefix is false (Case 9)', () => {
    const projection = buildAccountModelRuleProjection({
      provider: 'codex',
      credentialPrefix: 'haochi',
      runtimeModels: [{ id: 'gpt-5.5' }, { id: 'haochi/gpt-5.5' }],
      modelDefinitions: [{ id: 'gpt-5.5' }],
      credentialRules: ['gpt-5.5'],
      globalRules: {},
    });

    expect(projection.rows).toHaveLength(2);
    expect(projection.rows[0]).toMatchObject({
      id: 'gpt-5.5',
      ruleModelId: 'gpt-5.5',
      ruleModelIdResolved: true,
      scope: 'credential',
      hasCredentialExactRule: true,
      equivalentRuntimeModelIds: ['gpt-5.5', 'haochi/gpt-5.5'],
    });
    expect(projection.rows[1]).toMatchObject({
      id: 'haochi/gpt-5.5',
      ruleModelId: 'gpt-5.5',
      ruleModelIdResolved: true,
      scope: 'credential',
      hasCredentialExactRule: true,
      equivalentRuntimeModelIds: ['gpt-5.5', 'haochi/gpt-5.5'],
    });
  });

  it('cleans up legacy prefixed exact rule when disabling model (Case 10)', () => {
    const result = setAccountModelExactRule(
      ['haochi/gpt-5.5'],
      'gpt-5.5',
      true,
      ['gpt-5.5', 'haochi/gpt-5.5']
    );

    expect(result).toEqual(['gpt-5.5']);
  });

  it('restores exact model by removing canonical and legacy exact rules while keeping wildcards (Case 11)', () => {
    const result = setAccountModelExactRule(
      ['gpt-*', 'gpt-5.5', 'haochi/gpt-5.5'],
      'gpt-5.5',
      false,
      ['gpt-5.5', 'haochi/gpt-5.5']
    );

    expect(result).toEqual(['gpt-*']);
  });
});
