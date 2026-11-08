import type { ComponentType, SVGProps } from 'react';
import {
  IconCheck,
  IconChevronRight,
  IconCircleHelp,
  IconClock,
  IconCopy,
  IconDownload,
  IconEye,
  IconInfo,
  IconKey,
  IconLayoutDashboard,
  IconMoon,
  IconPlus,
  IconRefreshCw,
  IconSearch,
  IconSettings,
  IconShieldCheck,
  IconSun,
  IconTriangleAlert,
  IconX,
  type IconProps,
} from '../components/ui/icons';

const sharedIconComponents = {
  dashboard: IconLayoutDashboard,
  key: IconKey,
  settings: IconSettings,
  installation: IconDownload,
  help: IconCircleHelp,
  close: IconX,
  chevron: IconChevronRight,
  sun: IconSun,
  moon: IconMoon,
  refresh: IconRefreshCw,
  check: IconCheck,
  alert: IconTriangleAlert,
  shield: IconShieldCheck,
  clock: IconClock,
  copy: IconCopy,
  eye: IconEye,
  plus: IconPlus,
  search: IconSearch,
  chevronRight: IconChevronRight,
  info: IconInfo,
} as const satisfies Record<string, ComponentType<IconProps>>;

const localPaths = {
  link: 'M10 13a5 5 0 0 0 7 .1l3-3a5 5 0 0 0-7-7l-2 2 M14 11a5 5 0 0 0-7-.1l-3 3a5 5 0 0 0 7 7l2-2',
  gateway: 'M4 4h16v6H4z M4 14h16v6H4z M7 7h.01 M7 17h.01 M12 10v4',
  fold: 'M3 4h18v16H3z M9 4v16 M16 9l-3 3 3 3',
  menu: 'M4 6h16 M4 12h16 M4 18h16',
  logout: 'M9 4H4v16h5 M13 7l5 5-5 5 M8 12h13',
  globe: 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18 M3 12h18 M12 3c4 5 4 13 0 18-4-5-4-13 0-18Z',
  arrowRight: 'M4 12h16 M14 6l6 6-6 6',
  server: 'M4 3h16v7H4z M4 14h16v7H4z M8 6.5h.01 M8 17.5h.01 M12 6.5h5 M12 17.5h5',
  arrowLeft: 'M20 12H4 M10 6l-6 6 6 6',
  sparkle:
    'M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8L12 3 M5 17v4 M3 19h4 M19 3v4 M17 5h4',
} as const;

export type FullWebIconName = keyof typeof sharedIconComponents | keyof typeof localPaths;

export function FullWebIcon({
  name,
  size = 18,
  className = '',
  ...props
}: SVGProps<SVGSVGElement> & { name: FullWebIconName; size?: number }) {
  const mergedClassName = className ? `fullweb-icon ${className}` : 'fullweb-icon';
  if (name in sharedIconComponents) {
    const SharedIcon = sharedIconComponents[name as keyof typeof sharedIconComponents];
    return (
      <SharedIcon
        size={size}
        className={mergedClassName}
        strokeWidth={1.6}
        {...props}
      />
    );
  }
  return (
    <svg
      className={mergedClassName}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.6}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
      {...props}
    >
      <path d={localPaths[name as keyof typeof localPaths]} />
    </svg>
  );
}
