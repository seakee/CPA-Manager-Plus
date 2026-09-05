import { beforeEach, describe, expect, it, vi } from 'vitest';

const { mocks } = vi.hoisted(() => ({
  mocks: {
    get: vi.fn(),
    put: vi.fn(),
    patch: vi.fn(),
    delete: vi.fn(),
  },
}));

vi.mock('./client', () => ({
  apiClient: {
    get: mocks.get,
    put: mocks.put,
    patch: mocks.patch,
    delete: mocks.delete,
  },
}));

import { apiKeysApi } from './apiKeys';

beforeEach(() => {
  mocks.get.mockReset();
  mocks.put.mockReset();
  mocks.patch.mockReset();
  mocks.delete.mockReset();
});

describe('apiKeysApi.list response contract', () => {
  it('returns an empty list for an explicit empty kebab-case list', async () => {
    mocks.get.mockResolvedValue({ 'api-keys': [] });

    await expect(apiKeysApi.list()).resolves.toEqual([]);
  });

  it('returns an empty list for an explicit null kebab-case list', async () => {
    mocks.get.mockResolvedValue({ 'api-keys': null });

    await expect(apiKeysApi.list()).resolves.toEqual([]);
  });

  it('returns an empty list for an explicit null camelCase list', async () => {
    mocks.get.mockResolvedValue({ apiKeys: null });

    await expect(apiKeysApi.list()).resolves.toEqual([]);
  });

  it('rejects a response without a recognized API-key field', async () => {
    mocks.get.mockResolvedValue({});

    await expect(apiKeysApi.list()).rejects.toThrow('Invalid API key list response');
  });

  it('rejects a response with an unrelated field', async () => {
    mocks.get.mockResolvedValue({ unexpected: ['sk-a'] });

    await expect(apiKeysApi.list()).rejects.toThrow('Invalid API key list response');
  });

  it.each([
    ['a scalar', { 'api-keys': 'sk-a' }],
    ['an object', { 'api-keys': { a: 'sk-a' } }],
  ])('rejects a response whose API-key field is %s', async (_label, response) => {
    mocks.get.mockResolvedValue(response);

    await expect(apiKeysApi.list()).rejects.toThrow('Invalid API key list response');
  });

  it('keeps the camelCase fallback', async () => {
    mocks.get.mockResolvedValue({ apiKeys: ['fallback'] });

    await expect(apiKeysApi.list()).resolves.toEqual(['fallback']);
  });

  it('prefers the kebab-case field when both fields exist', async () => {
    mocks.get.mockResolvedValue({ 'api-keys': ['canonical'], apiKeys: ['fallback'] });

    await expect(apiKeysApi.list()).resolves.toEqual(['canonical']);
  });

  it('preserves canonical string values exactly', async () => {
    mocks.get.mockResolvedValue({ 'api-keys': ['  sk-a  '] });

    await expect(apiKeysApi.list()).resolves.toEqual(['  sk-a  ']);
  });

  it.each([
    ['a number', ['sk-a', 2]],
    ['null', ['sk-a', null]],
    ['an object', ['sk-a', { bad: true }]],
  ])('rejects an API-key list containing %s elements', async (_label, keys) => {
    mocks.get.mockResolvedValue({ 'api-keys': keys });

    await expect(apiKeysApi.list()).rejects.toThrow('Invalid API key list response');
  });

  it.each([
    ['an array', ['sk-a']],
    ['a scalar', 'sk-a'],
  ])('rejects a top-level %s response', async (_label, response) => {
    mocks.get.mockResolvedValue(response);

    await expect(apiKeysApi.list()).rejects.toThrow('Invalid API key list response');
  });

  it('propagates a transport failure from the canonical API request', async () => {
    const error = new Error('network unavailable');
    mocks.get.mockRejectedValue(error);

    await expect(apiKeysApi.list()).rejects.toBe(error);
  });
});

describe('apiKeysApi value-based mutations', () => {
  it('replaces an API key by value', async () => {
    mocks.patch.mockResolvedValue({});

    await apiKeysApi.replaceValue('old-key', 'new-key');

    expect(mocks.patch).toHaveBeenCalledWith('/api-keys', {
      old: 'old-key',
      new: 'new-key',
    });
  });

  it('deletes an API key by an encoded value', async () => {
    mocks.delete.mockResolvedValue({});

    await apiKeysApi.deleteValue('key/with ?&');

    expect(mocks.delete).toHaveBeenCalledWith('/api-keys?value=key%2Fwith%20%3F%26');
  });
});
