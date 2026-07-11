// LanguageSwitcher: a compact <select> for choosing English / Simplified Chinese.
// Persists via i18next-browser-languagedetector's localStorage cache, so the
// choice survives reloads and cross-tab navigation.

import { useTranslation } from 'react-i18next';
import { SUPPORTED_LANGUAGES } from '../i18n';

const LABEL: Record<(typeof SUPPORTED_LANGUAGES)[number], string> = {
  en: 'English',
  'zh-CN': '简体中文',
};

interface Props {
  className?: string;
  compact?: boolean; // sidebar footer wants a shorter surface
}

export default function LanguageSwitcher({ className, compact = false }: Props) {
  const { i18n, t } = useTranslation();
  const current = (SUPPORTED_LANGUAGES as readonly string[]).includes(i18n.language)
    ? i18n.language
    : 'en';

  return (
    <label className={`flex items-center gap-2 text-xs text-zinc-500 ${className ?? ''}`}>
      {!compact && <span>{t('language.label')}</span>}
      <select
        value={current}
        onChange={(e) => { void i18n.changeLanguage(e.target.value); }}
        className="rounded-md border border-zinc-300/60 bg-white/70 px-1.5 py-0.5 text-xs text-zinc-800 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-1 focus:ring-indigo-500 dark:border-zinc-700 dark:bg-zinc-900/60 dark:text-zinc-100"
        aria-label={t('language.label')}
      >
        {SUPPORTED_LANGUAGES.map((code) => (
          <option key={code} value={code}>{LABEL[code]}</option>
        ))}
      </select>
    </label>
  );
}
