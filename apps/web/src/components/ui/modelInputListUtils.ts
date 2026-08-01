import type { ModelAlias } from '@/types';

export interface ModelEntry {
  name: string;
  alias: string;
  priority?: number;
  testModel?: string;
  image?: boolean;
  forceMapping?: boolean;
  inputModalities?: string[];
  outputModalities?: string[];
  inputModalitiesDraft?: string;
  outputModalitiesDraft?: string;
  thinking?: Record<string, unknown>;
}

export const modelsToEntries = (models?: ModelAlias[]): ModelEntry[] => {
  if (!Array.isArray(models) || models.length === 0) {
    return [{ name: '', alias: '' }];
  }
  return models.map((model) => ({
    name: model.name || '',
    alias: model.alias || '',
    priority: model.priority,
    testModel: model.testModel,
    image: model.image,
    forceMapping: model.forceMapping,
    inputModalities: model.inputModalities,
    outputModalities: model.outputModalities,
    inputModalitiesDraft: model.inputModalities?.join(', '),
    outputModalitiesDraft: model.outputModalities?.join(', '),
    thinking: model.thinking,
  }));
};

export const entriesToModels = (entries: ModelEntry[]): ModelAlias[] => {
  return entries
    .filter((entry) => entry.name.trim())
    .map((entry) => {
      const model: ModelAlias = { name: entry.name.trim() };
      const alias = entry.alias.trim();
      if (alias && alias !== model.name) {
        model.alias = alias;
      }
      if (entry.priority !== undefined) {
        model.priority = entry.priority;
      }
      if (entry.testModel) {
        model.testModel = entry.testModel;
      }
      if (entry.image !== undefined) {
        model.image = entry.image;
      }
      if (entry.forceMapping !== undefined) {
        model.forceMapping = entry.forceMapping;
      }
      if (entry.inputModalities !== undefined) {
        model.inputModalities = [...entry.inputModalities];
      }
      if (entry.outputModalities !== undefined) {
        model.outputModalities = [...entry.outputModalities];
      }
      if (entry.thinking && typeof entry.thinking === 'object') {
        model.thinking = entry.thinking;
      }
      return model;
    });
};
