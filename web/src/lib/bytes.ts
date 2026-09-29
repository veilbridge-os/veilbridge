// Byte counts in one spelling for the whole panel (#56): whole units above
// ten, one decimal below, KB under a megabyte — "0 MB" for a device that
// fetched a few pages would read as "nothing", which is untrue.
export function fmtTraffic(n: (v: number) => string, bytes?: number | null): string {
  const b = bytes ?? 0
  if (b < 1024 * 1024) return `${n(Math.max(0, Math.round(b / 1024)))} KB`
  const mb = b / 1024 / 1024
  if (mb < 1024) return `${n(mb < 10 ? Math.round(mb * 10) / 10 : Math.round(mb))} MB`
  const gb = mb / 1024
  return `${n(gb < 10 ? Math.round(gb * 10) / 10 : Math.round(gb))} GB`
}
