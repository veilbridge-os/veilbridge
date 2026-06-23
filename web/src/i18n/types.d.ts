// Global vue-i18n typing: makes t() key-checked across the app, so a typo like
// t('routes.addRulee') is a compile error rather than a silent runtime fallback.
import 'vue-i18n'
import type en from './messages/en'

declare module 'vue-i18n' {
  // The English bundle is the canonical message shape.
  export interface DefineLocaleMessage extends Record<string, unknown> {
    nav: (typeof en)['nav']
    login: (typeof en)['login']
    dashboard: (typeof en)['dashboard']
    nodes: (typeof en)['nodes']
    routes: (typeof en)['routes']
  }
}
