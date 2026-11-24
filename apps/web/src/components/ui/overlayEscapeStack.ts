const openModalStack: symbol[] = [];
const openDrawerStack: symbol[] = [];

const MODAL_LOCK_CLASS = 'modal-open';

export const OVERLAY_FOCUSABLE_SELECTOR = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',');

export const getOverlayFocusableElements = (container: HTMLElement | null): HTMLElement[] => {
  if (!container || typeof container.querySelectorAll !== 'function') return [];
  return Array.from(container.querySelectorAll<HTMLElement>(OVERLAY_FOCUSABLE_SELECTOR)).filter(
    (element) =>
      !(typeof element.hasAttribute === 'function' && element.hasAttribute('disabled')) &&
      element.tabIndex !== -1
  );
};

export const registerOpenModal = (): (() => void) & { token: symbol } => {
  const token = Symbol('modal');
  openModalStack.push(token);
  const unregister = () => {
    const idx = openModalStack.lastIndexOf(token);
    if (idx >= 0) openModalStack.splice(idx, 1);
  };
  unregister.token = token;
  return unregister;
};

export const isTopmostModal = (token: symbol): boolean =>
  openModalStack.length > 0 && openModalStack[openModalStack.length - 1] === token;

export const hasOpenModal = (): boolean => openModalStack.length > 0;

export const registerOpenDrawer = (): (() => void) & { token: symbol } => {
  const token = Symbol('drawer');
  openDrawerStack.push(token);
  const unregister = () => {
    const idx = openDrawerStack.lastIndexOf(token);
    if (idx >= 0) openDrawerStack.splice(idx, 1);
  };
  unregister.token = token;
  return unregister;
};

export const isTopmostDrawer = (token: symbol): boolean =>
  openDrawerStack.length > 0 && openDrawerStack[openDrawerStack.length - 1] === token;

export const hasOpenDrawer = (): boolean => openDrawerStack.length > 0;

let activeModalLockCount = 0;
let activeDrawerLockCount = 0;
let hadModalFixedLock = false;

const scrollLockSnapshot = {
  scrollY: 0,
  contentScrollTop: 0,
  contentEl: null as HTMLElement | null,
  bodyPosition: '',
  bodyTop: '',
  bodyLeft: '',
  bodyRight: '',
  bodyWidth: '',
  bodyOverflow: '',
  htmlOverflow: '',
};

const resolveContentScrollContainer = (): HTMLElement | null => {
  if (typeof document === 'undefined' || typeof document.querySelector !== 'function') return null;
  const contentEl = document.querySelector('.content');
  return contentEl instanceof HTMLElement ? contentEl : null;
};

const applyModalFixedStyles = () => {
  const body = document.body;
  const html = document.documentElement;
  body.classList?.add(MODAL_LOCK_CLASS);
  html.classList?.add(MODAL_LOCK_CLASS);
  body.style.position = 'fixed';
  body.style.top = `-${scrollLockSnapshot.scrollY}px`;
  body.style.left = '0';
  body.style.right = '0';
  body.style.width = '100%';
  body.style.overflow = 'hidden';
  html.style.overflow = 'hidden';
  hadModalFixedLock = true;
};

const releaseModalFixedStylesToDrawer = () => {
  const body = document.body;
  const html = document.documentElement;
  body.classList?.remove(MODAL_LOCK_CLASS);
  html.classList?.remove(MODAL_LOCK_CLASS);
  body.style.position = scrollLockSnapshot.bodyPosition;
  body.style.top = scrollLockSnapshot.bodyTop;
  body.style.left = scrollLockSnapshot.bodyLeft;
  body.style.right = scrollLockSnapshot.bodyRight;
  body.style.width = scrollLockSnapshot.bodyWidth;
  body.style.overflow = 'hidden';
  html.style.overflow = 'hidden';

  if (hadModalFixedLock) {
    if (scrollLockSnapshot.contentEl) {
      scrollLockSnapshot.contentEl.scrollTop = scrollLockSnapshot.contentScrollTop;
    }
    if (typeof window !== 'undefined' && typeof window.scrollTo === 'function') {
      window.scrollTo(0, scrollLockSnapshot.scrollY);
    }
  }
};

export const acquireOverlayScrollLock = (kind: 'modal' | 'drawer'): (() => void) => {
  if (typeof document === 'undefined') {
    return () => {};
  }

  const prevTotal = activeModalLockCount + activeDrawerLockCount;
  const prevModal = activeModalLockCount;

  if (prevTotal === 0) {
    const body = document.body;
    const html = document.documentElement;
    const contentEl = resolveContentScrollContainer();
    const scrollY =
      (typeof window !== 'undefined' ? window.scrollY || window.pageYOffset : 0) ||
      html.scrollTop ||
      0;

    scrollLockSnapshot.scrollY = scrollY;
    scrollLockSnapshot.contentEl = contentEl;
    scrollLockSnapshot.contentScrollTop = contentEl?.scrollTop ?? 0;
    scrollLockSnapshot.bodyPosition = body.style.position ?? '';
    scrollLockSnapshot.bodyTop = body.style.top ?? '';
    scrollLockSnapshot.bodyLeft = body.style.left ?? '';
    scrollLockSnapshot.bodyRight = body.style.right ?? '';
    scrollLockSnapshot.bodyWidth = body.style.width ?? '';
    scrollLockSnapshot.bodyOverflow = body.style.overflow ?? '';
    scrollLockSnapshot.htmlOverflow = html.style.overflow ?? '';
    hadModalFixedLock = false;
  }

  if (kind === 'modal') {
    activeModalLockCount += 1;
  } else {
    activeDrawerLockCount += 1;
  }

  if (activeModalLockCount > 0) {
    if (prevModal === 0) {
      applyModalFixedStyles();
    }
  } else if (prevTotal === 0) {
    document.body.style.overflow = 'hidden';
    document.documentElement.style.overflow = 'hidden';
  }

  let released = false;
  return () => {
    if (released) return;
    released = true;

    if (kind === 'modal') {
      activeModalLockCount = Math.max(0, activeModalLockCount - 1);
    } else {
      activeDrawerLockCount = Math.max(0, activeDrawerLockCount - 1);
    }

    if (typeof document === 'undefined') {
      if (activeModalLockCount + activeDrawerLockCount === 0) {
        hadModalFixedLock = false;
        scrollLockSnapshot.scrollY = 0;
        scrollLockSnapshot.contentScrollTop = 0;
        scrollLockSnapshot.contentEl = null;
      }
      return;
    }

    const nextTotal = activeModalLockCount + activeDrawerLockCount;
    if (nextTotal === 0) {
      const body = document.body;
      const html = document.documentElement;
      const shouldRestoreScroll = hadModalFixedLock;
      const scrollY = scrollLockSnapshot.scrollY;
      const contentScrollTop = scrollLockSnapshot.contentScrollTop;
      const contentEl = scrollLockSnapshot.contentEl;

      if (shouldRestoreScroll) {
        body.classList?.remove(MODAL_LOCK_CLASS);
        html.classList?.remove(MODAL_LOCK_CLASS);
        body.style.position = scrollLockSnapshot.bodyPosition;
        body.style.top = scrollLockSnapshot.bodyTop;
        body.style.left = scrollLockSnapshot.bodyLeft;
        body.style.right = scrollLockSnapshot.bodyRight;
        body.style.width = scrollLockSnapshot.bodyWidth;
      }
      body.style.overflow = scrollLockSnapshot.bodyOverflow;
      html.style.overflow = scrollLockSnapshot.htmlOverflow;

      if (shouldRestoreScroll) {
        if (contentEl && typeof contentEl.scrollTo === 'function') {
          contentEl.scrollTo({ top: contentScrollTop, left: 0, behavior: 'auto' });
        }
        if (typeof window !== 'undefined' && typeof window.scrollTo === 'function') {
          window.scrollTo({ top: scrollY, left: 0, behavior: 'auto' });
        }
      }

      hadModalFixedLock = false;
      scrollLockSnapshot.scrollY = 0;
      scrollLockSnapshot.contentScrollTop = 0;
      scrollLockSnapshot.contentEl = null;
      return;
    }

    if (kind === 'modal' && activeModalLockCount === 0 && activeDrawerLockCount > 0) {
      releaseModalFixedStylesToDrawer();
    }
  };
};
