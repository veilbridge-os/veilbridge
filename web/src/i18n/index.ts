// i18n core. Two layers move together under one selected locale:
//  1. vue-i18n — our own UI strings (messages/*.ts).
//  2. Element Plus — its built-in component strings, applied via
//     <el-config-provider :locale> in App.vue.
// The locale list mirrors Keenetic's web UI (13 languages); every one of them
// also exists in Element Plus, so the two layers stay in sync.

import type { Language } from 'element-plus/es/locale'
// Element Plus locale objects (its built-in component strings).
import elDa from 'element-plus/es/locale/lang/da'
import elDe from 'element-plus/es/locale/lang/de'
import elEn from 'element-plus/es/locale/lang/en'
import elEs from 'element-plus/es/locale/lang/es'
import elFi from 'element-plus/es/locale/lang/fi'
import elFr from 'element-plus/es/locale/lang/fr'
import elIt from 'element-plus/es/locale/lang/it'
import elPl from 'element-plus/es/locale/lang/pl'
import elPt from 'element-plus/es/locale/lang/pt'
import elRu from 'element-plus/es/locale/lang/ru'
import elSv from 'element-plus/es/locale/lang/sv'
import elTr from 'element-plus/es/locale/lang/tr'
import elUk from 'element-plus/es/locale/lang/uk'
import { createI18n } from 'vue-i18n'
import type { MessageSchema } from './messages/en'

// Our message bundles.
import en from './messages/en'
import ru from './messages/ru'

// A language the UI offers. epLocale is the matching Element Plus locale object.
export interface LocaleMeta {
  code: string
  // nativeName is shown in the language switcher.
  nativeName: string
  epLocale: Language
}

// The 13 languages, ordered to match Keenetic's switcher. en + ru ship with full
// translations; the rest currently fall back to en for our strings (see messages/)
// while Element Plus components are already localized for all of them.
export const LOCALES: LocaleMeta[] = [
  { code: 'en', nativeName: 'English', epLocale: elEn },
  { code: 'ru', nativeName: 'Русский', epLocale: elRu },
  { code: 'de', nativeName: 'Deutsch', epLocale: elDe },
  { code: 'fr', nativeName: 'Français', epLocale: elFr },
  { code: 'es', nativeName: 'Español', epLocale: elEs },
  { code: 'it', nativeName: 'Italiano', epLocale: elIt },
  { code: 'pt', nativeName: 'Português', epLocale: elPt },
  { code: 'pl', nativeName: 'Polski', epLocale: elPl },
  { code: 'uk', nativeName: 'Українська', epLocale: elUk },
  { code: 'tr', nativeName: 'Türkçe', epLocale: elTr },
  { code: 'sv', nativeName: 'Svenska', epLocale: elSv },
  { code: 'da', nativeName: 'Dansk', epLocale: elDa },
  { code: 'fi', nativeName: 'Suomi', epLocale: elFi },
]

const STORAGE_KEY = 'veilbridge.locale'

function initialLocale(): string {
  const saved = localStorage.getItem(STORAGE_KEY)
  if (saved && LOCALES.some((l) => l.code === saved)) return saved
  // Fall back to the browser language if we support it, else English.
  const nav = navigator.language.slice(0, 2)
  return LOCALES.some((l) => l.code === nav) ? nav : 'en'
}

// Explicit generics select the non-legacy (Composition API) overload, so
// i18n.global.locale is a WritableComputedRef and t() is key-checked.
/**
 * slavicPlural picks the form for languages with three plural categories
 * (Russian, Ukrainian, Polish): one / few / many.
 *
 * vue-i18n's built-in rule is the English one — "1 → first form, everything
 * else → second" — which on a three-form message produces «1 часа» and
 * «0 минута». That is visible on the dashboard, in the uptime line, on every
 * page load: the kind of wrongness a native speaker reads as "machine
 * translated" before reading anything else.
 */
function slavicPlural(count: number, choicesLength: number): number {
  if (choicesLength < 3) return count === 1 ? 0 : 1
  const mod10 = count % 10
  const mod100 = count % 100
  if (mod10 === 1 && mod100 !== 11) return 0
  if (mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14)) return 1
  return 2
}

export const i18n = createI18n<[MessageSchema], string, false>({
  legacy: false,
  locale: initialLocale(),
  fallbackLocale: 'en',
  // Only en/ru are translated so far; others resolve via fallbackLocale.
  messages: { en, ru },
  pluralRules: {
    ru: slavicPlural,
    uk: slavicPlural,
    pl: slavicPlural,
  },
})

// elementLocale returns the Element Plus locale object for the active language,
// for binding to <el-config-provider>.
export function elementLocale(code: string): Language {
  return (LOCALES.find((l) => l.code === code) ?? LOCALES[0]).epLocale
}

// setLocale switches the language and persists the choice.
export function setLocale(code: string) {
  i18n.global.locale.value = code as typeof i18n.global.locale.value
  localStorage.setItem(STORAGE_KEY, code)
}
