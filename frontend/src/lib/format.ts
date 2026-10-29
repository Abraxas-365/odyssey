// Spanish (Peru) display formatting. Wire values stay ISO-8601 (docs/api.md).
const locale = 'es-PE'

const rtf = new Intl.RelativeTimeFormat(locale, { numeric: 'auto' })
const units: [Intl.RelativeTimeFormatUnit, number][] = [
  ['year', 365 * 24 * 3600],
  ['month', 30 * 24 * 3600],
  ['week', 7 * 24 * 3600],
  ['day', 24 * 3600],
  ['hour', 3600],
  ['minute', 60],
]

/** "hace 3 min", "ayer", "hace 2 semanas". Under a minute → "ahora". */
export function formatRelative(iso: string, now = Date.now()) {
  const seconds = (new Date(iso).getTime() - now) / 1000
  if (Number.isNaN(seconds)) return ''
  for (const [unit, size] of units) {
    if (Math.abs(seconds) >= size) return rtf.format(Math.round(seconds / size), unit)
  }
  return 'ahora'
}

/** "26 oct 2026, 22:31". */
export function formatDateTime(iso: string) {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString(locale, { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' })
}

export function formatMoney(usd: number) {
  return new Intl.NumberFormat(locale, { style: 'currency', currency: 'USD', minimumFractionDigits: 2 }).format(usd)
}

/** "820 B", "4.2 KB", "12.5 MB" (powers of 1024, one decimal under 100). */
export function formatBytes(n: number) {
  const units = ['B', 'KB', 'MB', 'GB']
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  const digits = i === 0 || n >= 100 ? 0 : 1
  return `${n.toLocaleString(locale, { maximumFractionDigits: digits, minimumFractionDigits: 0 })} ${units[i]}`
}

/**
 * An app's address, only if it is safe to link to (absolute http/https). The
 * URL comes from the customer's cluster, outside the platform's trust
 * boundary: anything else (javascript:, data:…) is never put in an href.
 */
export function safeAppUrl(raw: string | null | undefined): string | undefined {
  if (!raw) return undefined
  try {
    const u = new URL(raw)
    return (u.protocol === 'http:' || u.protocol === 'https:') && u.hostname !== '' ? u.href : undefined
  } catch {
    return undefined
  }
}
