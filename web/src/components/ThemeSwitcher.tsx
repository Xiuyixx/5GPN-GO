// ThemeSwitcher: cyclic button for system → light → dark → system.
//
// The store is the source of truth; this component just fires setTheme
// and reads back the current pick. Sun/moon/auto glyphs are inline SVG
// so the panel stays icon-library-free.

import { useTranslation } from 'react-i18next';
import { useThemeStore, type Theme } from '../stores/theme';

const NEXT: Record<Theme, Theme> = {
  system: 'light',
  light: 'dark',
  dark: 'system',
};

interface Props {
  className?: string;
  compact?: boolean; // sidebar footer wants no text
}

export default function ThemeSwitcher({ className, compact = false }: Props) {
  const { t } = useTranslation();
  const theme = useThemeStore((s) => s.theme);
  const setTheme = useThemeStore((s) => s.setTheme);

  const labelKey = `theme.${theme}` as const;
  const next = NEXT[theme];
  const ariaLabel = t('theme.switchTo', {
    defaultValue: 'Switch theme (next: {{next}})',
    next: t(`theme.${next}` as const),
  });

  return (
    <button
      type="button"
      onClick={() => setTheme(next)}
      aria-label={ariaLabel}
      title={ariaLabel}
      className={
        'inline-flex items-center gap-1.5 rounded-md border border-zinc-300/60 bg-white/70 px-2 py-1 text-xs text-zinc-700 shadow-sm transition hover:border-indigo-500 hover:text-indigo-600 dark:border-zinc-700 dark:bg-zinc-900/60 dark:text-zinc-200 dark:hover:border-indigo-400 dark:hover:text-indigo-300 ' +
        (className ?? '')
      }
    >
      <ThemeGlyph theme={theme} />
      {!compact && <span>{t(labelKey)}</span>}
    </button>
  );
}

function ThemeGlyph({ theme }: { theme: Theme }) {
  const cls = 'h-3.5 w-3.5';
  if (theme === 'light') {
    return (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" className={cls} aria-hidden>
        <circle cx="12" cy="12" r="4" />
        <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41" strokeLinecap="round" />
      </svg>
    );
  }
  if (theme === 'dark') {
    return (
      <svg viewBox="0 0 24 24" fill="currentColor" className={cls} aria-hidden>
        <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" />
      </svg>
    );
  }
  // system: half-sun / half-moon
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" className={cls} aria-hidden>
      <circle cx="12" cy="12" r="4" />
      <path d="M12 8a4 4 0 0 0 0 8z" fill="currentColor" stroke="none" />
      <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41" strokeLinecap="round" />
    </svg>
  );
}
