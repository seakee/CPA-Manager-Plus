import { describe, expect, it } from 'vitest';
import { classifyModels } from './models';

describe('classifyModels', () => {
  it('classifies devin/* models into Devin group without altering model names', () => {
    const input = [
      { name: 'devin/gpt-5' },
      { name: 'devin/claude-sonnet' },
      { name: 'devin/gemini-3-8-flash' },
      { name: 'devin/grok-4-6' },
      { name: 'devin/deepseek-v4-1-flash' },

      { name: 'gpt-5' },
      { name: 'claude-sonnet' },
      { name: 'gemini-3-8-flash' },
      { name: 'grok-4-6' },
      { name: 'deepseek-v4-1-flash' },
    ];

    const groups = classifyModels(input);

    const devinGroup = groups.find((g) => g.id === 'devin');
    expect(devinGroup).toBeDefined();
    expect(devinGroup?.items.map((m) => m.name)).toEqual([
      'devin/gpt-5',
      'devin/claude-sonnet',
      'devin/gemini-3-8-flash',
      'devin/grok-4-6',
      'devin/deepseek-v4-1-flash',
    ]);

    const gptGroup = groups.find((g) => g.id === 'gpt');
    expect(gptGroup).toBeDefined();
    expect(gptGroup?.items.map((m) => m.name)).toEqual(['gpt-5']);

    const claudeGroup = groups.find((g) => g.id === 'claude');
    expect(claudeGroup).toBeDefined();
    expect(claudeGroup?.items.map((m) => m.name)).toEqual(['claude-sonnet']);

    const geminiGroup = groups.find((g) => g.id === 'gemini');
    expect(geminiGroup).toBeDefined();
    expect(geminiGroup?.items.map((m) => m.name)).toEqual(['gemini-3-8-flash']);

    const grokGroup = groups.find((g) => g.id === 'grok');
    expect(grokGroup).toBeDefined();
    expect(grokGroup?.items.map((m) => m.name)).toEqual(['grok-4-6']);

    const deepseekGroup = groups.find((g) => g.id === 'deepseek');
    expect(deepseekGroup).toBeDefined();
    expect(deepseekGroup?.items.map((m) => m.name)).toEqual(['deepseek-v4-1-flash']);

    devinGroup?.items.forEach((item) => {
      expect(item.name.startsWith('devin/')).toBe(true);
    });
  });

  it('keeps devin namespace check strictly on name and does not classify by alias alone', () => {
    const input = [
      { name: 'custom-model', alias: 'devin/something' },
    ];
    const groups = classifyModels(input);
    const devinGroup = groups.find((g) => g.id === 'devin');
    expect(devinGroup).toBeUndefined();
  });
});
