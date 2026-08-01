export const hasCaseInsensitiveAliasConflict = (
  aliases: Iterable<string>,
  candidate: string,
  ignoredAlias?: string
) => {
  const normalized = candidate.trim().toLocaleLowerCase();
  let ignored = ignoredAlias?.trim().toLocaleLowerCase();
  return Array.from(aliases).some((alias) => {
    const value = alias.trim().toLocaleLowerCase();
    if (ignored && value === ignored) {
      ignored = undefined;
      return false;
    }
    return value === normalized;
  });
};
