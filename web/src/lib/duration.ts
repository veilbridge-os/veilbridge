// Durations, pluralised by the locale rather than glued together from a
// number and a noun.
//
// It lives here instead of in a view because two screens now show "how long
// has this been up", and a second copy of these rules would drift from the
// first — invisibly, because the drift only shows in a locale nobody on the
// team reads. Interpolating a bare number into "{h} hours" already reads as
// "1 hours" in English, and Russian changes the noun with the count.
import { useI18n } from 'vue-i18n'

export function useDuration() {
  const { t } = useI18n()

  /** fmtDuration renders seconds as up to two units, largest first. */
  function fmtDuration(sec?: number): string {
    if (!sec) return '—'
    const d = Math.floor(sec / 86400)
    const h = Math.floor((sec % 86400) / 3600)
    const m = Math.floor((sec % 3600) / 60)
    if (d > 0) return `${t('time.days', { n: d }, d)} ${t('time.hours', { n: h }, h)}`
    if (h > 0) return `${t('time.hours', { n: h }, h)} ${t('time.minutes', { n: m }, m)}`
    return t('time.minutes', { n: m }, m)
  }

  return { fmtDuration }
}
