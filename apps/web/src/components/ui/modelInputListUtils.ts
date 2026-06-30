import type { ModelAlias } from '@/types';

export interface ModelEntry {
  name: string;
  alias: string;
  forceMapping?: boolean;
  priority?: number;
  testModel?: string;
  image?: boolean;
  thinking?: Record<string, unknown>;
}

export const modelsToEntries = (models?: ModelAlias[]): ModelEntry[] => {
  if (!Array.isArray(models) || models.length === 0) {
    return [{ name: '', alias: '' }];
  }
  return models.map((model) => ({
    name: model.name || '',
    alias: model.alias || '',
    forceMapping: model.forceMapping,
    priority: model.priority,
    testModel: model.testModel,
    image: model.image,
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
      if (entry.forceMapping !== undefined) {
        model.forceMapping = entry.forceMapping;
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
      if (entry.thinking && typeof entry.thinking === 'object') {
        model.thinking = entry.thinking;
      }
      return model;
    });
};
