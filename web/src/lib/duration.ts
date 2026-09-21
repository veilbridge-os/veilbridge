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

  /** fmtDuration renders seconds as up to two units, largest first.
   *
   * A zero smaller unit is dropped instead of printed: a lease of exactly
   * twelve hours read "12 hours 0 minutes" on the local-network screen, and
   * an hour of uptime read "1 hour 0 minutes" on the dashboard. The zero
   * carries no information and costs the line its plainness. */
  function fmtDuration(sec?: number): string {
    if (!sec) return '—'
    const d = Math.floor(sec / 86400)
    const h = Math.floor((sec % 86400) / 3600)
    const m = Math.floor((sec % 3600) / 60)
    const days = t('time.days', { n: d }, d)
    const hours = t('time.hours', { n: h }, h)
    const minutes = t('time.minutes', { n: m }, m)
    if (d > 0) return h > 0 ? `${days} ${hours}` : days
    if (h > 0) return m > 0 ? `${hours} ${minutes}` : hours
    return minutes
  }

  /** fmtAgo renders "how long ago" in a single unit: the question it answers
   * is whether something happened recently, and a second unit adds precision
   * nobody acts on. Negative input is clamped rather than rendered — it means
   * the two numbers it was derived from came from different readings. */
  function fmtAgo(sec: number): string {
    const s = Math.max(0, Math.floor(sec))
    if (s < 60) return t('time.secondsAgo', { n: s }, s)
    const mins = Math.floor(s / 60)
    if (mins < 60) return t('time.minutesAgo', { n: mins }, mins)
    const hours = Math.floor(mins / 60)
    return t('time.hoursAgo', { n: hours }, hours)
  }

  return { fmtDuration, fmtAgo }
}
