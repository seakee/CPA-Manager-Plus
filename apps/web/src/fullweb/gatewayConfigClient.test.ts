import { describe, expect, it } from 'vitest';
import {
  allowsDefaultEdit,
  boolDraft,
  boolState,
  draftEquals,
  draftFor,
  draftIssue,
  editFor,
  isInheritable,
  limitIssue,
  newGatewayRequestId,
  parseGatewayOperationResponse,
  parseGatewayView,
  type GatewayFieldView,
  type GatewayProfileLimits,
} from './gatewayConfigClient';

function field(overrides: Partial<GatewayFieldView> = {}): GatewayFieldView {
  return {
    path: 'routing.retry.request-retry',
    type: 'int',
    kind: 'number',
    group: 'retry',
    presence: 'present',
    value: 3,
    editable: true,
    ...overrides,
  };
}

describe('gateway configuration client', () => {
  it('accepts the Manager projection and keeps presence apart from value', () => {
    const view = parseGatewayView({
      capability: { mode: 'managed', readable: true, writable: true, rulesWritable: true },
      generation: '7',
      groups: [
        {
          id: 'retry',
          prefix: 'routing.retry',
          paths: ['routing.retry.request-retry'],
          baseline: 'a'.repeat(64),
          writable: true,
          persisted: 'unknown',
          effective: 'unknown',
          operation: { requestId: 'b'.repeat(32), group: 'retry', target: 'group', state: 'persisted' },
        },
      ],
      fields: [
        { path: 'routing.strategy', type: 'string', kind: 'select', group: 'routing', presence: 'present', value: 'round-robin', editable: true },
        { path: 'routing.session-affinity', type: 'bool', kind: 'bool', group: 'routing', presence: 'null', value: null, editable: true },
        { path: 'requests.proxy-url', type: 'string', kind: 'url', group: 'request', presence: 'missing', value: null, editable: true },
      ],
      families: [
        { family: 'filter', path: 'requests.payload.filter', rules: [], baseline: 'c'.repeat(64), writable: true, readOnly: false },
        { family: 'default', path: 'requests.payload.default', rules: null, baseline: 'd'.repeat(64), writable: true, readOnly: false },
      ],
      groupsInvalid: false,
    });
    expect(view.generation).toBe('7');
    expect(view.fields[1].presence).toBe('null');
    expect(view.fields[2].presence).toBe('missing');
    expect(view.groups[0].operation?.state).toBe('persisted');
    expect(view.families[1].rules).toBeNull();
  });

  it('rejects a malformed projection instead of rendering it', () => {
    for (const broken of [
      null,
      {},
      { capability: { mode: 'managed' }, fields: [], groups: [], families: [] },
      {
        capability: { mode: 'nonsense', readable: true, writable: true, rulesWritable: true },
        fields: [],
        groups: [],
        families: [],
      },
      {
        capability: { mode: 'managed', readable: true, writable: true, rulesWritable: true },
        fields: [{ path: 'x', type: 'int', kind: 'nonsense', group: 'g', presence: 'present', value: 1, editable: true }],
        groups: [],
        families: [],
      },
    ]) {
      expect(() => parseGatewayView(broken)).toThrow();
    }
    expect(() => parseGatewayOperationResponse({ effective: 'immediate' })).toThrow();
  });

  it('issues 32 hexadecimal request IDs and never repeats one it already issued', () => {
    const issued = new Set<string>();
    for (let index = 0; index < 32; index += 1) {
      const id = newGatewayRequestId(issued);
      expect(id).toMatch(/^[0-9a-f]{32}$/);
    }
    expect(issued.size).toBe(32);
  });

  it('compares a draft against the server observation', () => {
    expect(draftEquals(field(), draftFor(field()))).toBe(true);
    expect(draftEquals(field(), { action: 'set', value: 4 })).toBe(false);
    expect(draftEquals(field(), { action: 'default', value: '' })).toBe(false);
    expect(draftEquals(field({ presence: 'missing', value: null }), { action: 'set', value: '' })).toBe(true);
    expect(draftEquals(field({ presence: 'missing', value: null }), { action: 'default', value: '' })).toBe(true);
    expect(draftEquals(field({ type: 'bool', kind: 'bool', value: false }), { action: 'set', value: true })).toBe(false);
    expect(draftEquals(field({ type: 'bool', kind: 'bool', value: false }), { action: 'set', value: false })).toBe(true);
  });

  it('keeps an unset inheritable switch apart from an explicit off', () => {
    // routing.session-affinity-subagents is a *bool that CPA defaults to true:
    // an absent key, an explicit false and an explicit true are three states.
    const unset = field({ type: 'bool', kind: 'bool', presence: 'missing', value: null, inherit: true });
    expect(isInheritable(unset)).toBe(true);
    expect(boolState(draftFor(unset))).toBe('inherit');
    // Showing "off" for the unset leaf was the bug: it hid CPA's default.
    expect(draftEquals(unset, draftFor(unset))).toBe(true);
    expect(draftEquals(unset, boolDraft('off'))).toBe(false);
    expect(editFor(unset, boolDraft('off'))).toEqual({
      path: 'routing.retry.request-retry',
      action: 'set',
      value: false,
    });
    expect(boolState(boolDraft('on'))).toBe('on');
    expect(boolState(boolDraft('inherit'))).toBe('inherit');
    // An explicit null resolves to the same CPA default, so it is not a change
    // either — but an explicit value still is.
    const nulled = field({ ...unset, presence: 'null' });
    expect(draftEquals(nulled, boolDraft('inherit'))).toBe(true);
    expect(draftEquals(nulled, boolDraft('off'))).toBe(false);
    // A plain bool has no third state and keeps its two-state control.
    expect(isInheritable(field({ type: 'bool', kind: 'bool', value: false }))).toBe(false);
  });

  it('reports the inheritable default so the page can label it', () => {
    const view = parseGatewayView({
      capability: { mode: 'managed', readable: true, writable: true, rulesWritable: true },
      generation: '7',
      groups: [],
      fields: [
        // A key whose value is false is still a stated default.
        { path: 'routing.session-affinity-subagents', type: 'bool', kind: 'bool', group: 'routing', presence: 'missing', value: null, editable: true, inherit: false },
        { path: 'routing.session-affinity', type: 'bool', kind: 'bool', group: 'routing', presence: 'present', value: true, editable: true },
      ],
      families: [],
      groupsInvalid: false,
    });
    expect(view.fields[0].inherit).toBe(false);
    expect('inherit' in view.fields[0]).toBe(true);
    // A leaf the Manager did not mark carries no key at all, not a false one.
    expect('inherit' in view.fields[1]).toBe(false);
  });

  it('parses the profile save shape and treats absence as no limit', () => {
    const base = {
      capability: { mode: 'private', readable: true, writable: true, rulesWritable: false },
      generation: '7',
      groups: [],
      fields: [],
      families: [],
      groupsInvalid: false,
    };
    const limited = parseGatewayView({ ...base, profile_limits: { single_edit: true, use_default: false } });
    expect(limited.profileLimits).toEqual({ singleEdit: true, useDefault: false });
    expect(limitIssue(limited.profileLimits, 1)).toBeNull();
    expect(limitIssue(limited.profileLimits, 2)).toBe('single_edit');
    expect(allowsDefaultEdit(limited.profileLimits)).toBe(false);
    // A Manager that states nothing must not be read as "one edit only".
    const plain = parseGatewayView(base);
    expect(plain.profileLimits).toBeUndefined();
    expect(limitIssue(plain.profileLimits, 5)).toBeNull();
    expect(allowsDefaultEdit(plain.profileLimits)).toBe(true);
    // A half-stated shape is not a limit either: guessing here would refuse
    // a save the Manager actually accepts.
    const half: GatewayProfileLimits | undefined = parseGatewayView({
      ...base,
      profile_limits: { single_edit: true },
    }).profileLimits;
    expect(half).toBeUndefined();
  });

  it('renders the edit a dirty draft contributes, normalizing only the shape', () => {
    expect(editFor(field(), { action: 'set', value: '11' })).toEqual({
      path: 'routing.retry.request-retry',
      action: 'set',
      value: 11,
    });
    expect(editFor(field(), { action: 'default', value: '' })).toEqual({
      path: 'routing.retry.request-retry',
      action: 'default',
    });
    // Shape only: CPAMP never restates a CPA bound.
    expect(draftIssue(field(), { action: 'set', value: '11' })).toBeNull();
    expect(draftIssue(field(), { action: 'set', value: '' })).toBe('required');
    expect(draftIssue(field(), { action: 'set', value: '1.5' })).toBe('integer');
    expect(draftIssue(field(), { action: 'default', value: '' })).toBeNull();
    expect(
      draftIssue(field({ type: 'string', kind: 'url', value: '' }), { action: 'set', value: ' ' })
    ).toBe('required');
    expect(
      draftIssue(
        field({ type: 'kv-map', path: 'routing.replace-model', value: {} }),
        { action: 'set', value: { '': 'replacement' } }
      )
    ).toBe('keyRequired');
    expect(
      draftIssue(
        field({ type: 'kv-map', path: 'routing.replace-model', value: {} }),
        { action: 'set', value: { alpha: 'one', ' alpha ': 'two' } }
      )
    ).toBe('duplicate');
  });
});
