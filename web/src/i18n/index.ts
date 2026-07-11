// i18n bootstrap for the panel.
//
// - i18next + react-i18next: standard translation surface (`t('key')`, `useTranslation`).
// - i18next-browser-languagedetector: picks up localStorage first, then navigator.language,
//   so the user's explicit choice always wins.
// - `<html lang>` is updated whenever the language changes so the browser + screen readers
//   see the right locale.
//
// Adding a new key: put it in ALL locales below (`web/src/locales/en.json` and
// `web/src/locales/zh-CN.json`). Missing keys fall back to `en` and log a dev-only warning.

import i18n from 'i18next';
import LanguageDetector from 'i18next-browser-languagedetector';
import { initReactI18next } from 'react-i18next';

import en from '../locales/en.json';
import zhCN from '../locales/zh-CN.json';

export const SUPPORTED_LANGUAGES = ['en', 'zh-CN'] as const;
export type SupportedLanguage = (typeof SUPPORTED_LANGUAGES)[number];

const STORAGE_KEY = '5gpn-lang';

i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: {
      en: { translation: en },
      'zh-CN': { translation: zhCN },
    },
    fallbackLng: 'en',
    supportedLngs: SUPPORTED_LANGUAGES as unknown as string[],
    // `zh` from navigator.language → treat as zh-CN; anything else unknown → en.
    load: 'currentOnly',
    nonExplicitSupportedLngs: true,
    interpolation: { escapeValue: false }, // React already escapes
    detection: {
      order: ['localStorage', 'navigator'],
      lookupLocalStorage: STORAGE_KEY,
      caches: ['localStorage'],
    },
    returnNull: false,
  });

// Keep <html lang> in sync so accessibility tooling picks the right locale.
function syncHtmlLang(lng: string) {
  const doc = typeof document !== 'undefined' ? document : null;
  if (doc?.documentElement) {
    doc.documentElement.setAttribute('lang', lng);
  }
}
syncHtmlLang(i18n.language);
i18n.on('languageChanged', syncHtmlLang);

export default i18n;
