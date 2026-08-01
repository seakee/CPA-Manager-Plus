import type { OAuthModelAliasEntry } from '@/types';

export const serializeOAuthAliasDraft = (entries: OAuthModelAliasEntry[]) =>
  JSON.stringify(
    entries
      .map((entry) => ({
        name: entry.name.trim(),
        alias: entry.alias.trim(),
        fork: entry.fork === true,
        forceMapping: entry.forceMapping === true,
      }))
      .filter((entry) => entry.name || entry.alias)
  );

export const isOAuthAliasDraftDirty = (
  current: OAuthModelAliasEntry[],
  initial: OAuthModelAliasEntry[]
) => serializeOAuthAliasDraft(current) !== serializeOAuthAliasDraft(initial);
