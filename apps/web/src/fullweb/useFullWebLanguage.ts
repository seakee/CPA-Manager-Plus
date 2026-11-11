import { useSyncExternalStore } from 'react';
import { fullWebLanguage, subscribeFullWebLanguage } from '../i18n/fullweb';

// The Full Web text helpers (`bootstrapJourneyText`, `shellText`,
// `freshExternal`, `artifactText`) are plain functions that read the active
// language while rendering, so a language switch only reaches text that is
// already on screen when the component that rendered it re-renders. Calling this
// hook subscribes that component to the Full Web language and makes the switch
// itself the re-render trigger.
export function useFullWebLanguage(): string {
  return useSyncExternalStore(subscribeFullWebLanguage, fullWebLanguage, fullWebLanguage);
}
