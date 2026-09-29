// An internet schedule in the panel's words (#54): "weekdays 22:00–07:00".
// The device sends a schedule as data (days, from, to) and, in the apply bar,
// as one canonical line "mon,tue 22:00-07:00"; both are said here, so the row
// on the screen and the row in the apply bar cannot disagree.
import type { Composer } from 'vue-i18n'

export const WEEKDAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'] as const

// days may come as null from the generated schema (a Go slice); read as none.
export type Schedule = { days: string[] | null; from: string; to: string }

type T = Composer['t']

export function daysWords(t: T, days: string[] | null): string {
  const set = new Set(days ?? [])
  if (set.size === 7) return t('dev.days.every')
  if (set.size === 5 && ['mon', 'tue', 'wed', 'thu', 'fri'].every((d) => set.has(d))) {
    return t('dev.days.work')
  }
  if (set.size === 2 && set.has('sat') && set.has('sun')) return t('dev.days.weekend')
  return WEEKDAYS.filter((d) => set.has(d))
    .map((d) => t(`dev.days.short.${d}`))
    .join(', ')
}

export function scheduleWords(t: T, s: Schedule): string {
  return `${daysWords(t, s.days)} ${s.from}–${s.to}`
}

/** parseWords reads the apply bar's canonical line back into a schedule. */
export function parseWords(line: string): Schedule | null {
  const m = /^([a-z,]+) (\d\d:\d\d)-(\d\d:\d\d)$/.exec(line.trim())
  if (!m) return null
  return { days: m[1].split(','), from: m[2], to: m[3] }
}

/** overnight: the window runs into the next morning. */
export function overnight(s: { from: string; to: string }): boolean {
  return s.to !== '00:00' && s.to < s.from
}
