export type ConfigMutationLock = {
  isLocked: () => boolean;
  release: () => void;
  tryAcquire: () => boolean;
};

export const createConfigMutationLock = (): ConfigMutationLock => {
  let locked = false;

  return {
    isLocked: () => locked,
    release: () => {
      locked = false;
    },
    tryAcquire: () => {
      if (locked) return false;
      locked = true;
      return true;
    },
  };
};
