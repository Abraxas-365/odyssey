import { AlertTriangle, BellRing, Hourglass, Server, ShieldAlert, XCircle, type LucideIcon } from 'lucide-react'
import type { Tone } from '@/components/odyssey/tone'
import { formatRelative } from '@/lib/format'
import type { AttentionItem, AttentionKind, AttentionSource, Build, InventoryRow, InventoryScan, InventoryStatus } from '@/lib/types'

/**
 * Plain-language view of the inventory and of "Necesita tu atención" (UX v3
 * §3): the sentence comes from the API (it already says who and what); the
 * console picks the icon, the tone and the one action.
 */

export const kindView: Record<AttentionKind, { icon: LucideIcon; action: string }> = {
  approval_pending: { icon: Hourglass, action: 'Decidir' },
  app_vulnerable: { icon: ShieldAlert, action: 'Ver la app' },
  cluster_disconnected: { icon: Server, action: 'Ver clústeres' },
  cluster_policy: { icon: Server, action: 'Ver clústeres' },
  deployment_failed: { icon: XCircle, action: 'Ver la app' },
  app_attention: { icon: AlertTriangle, action: 'Resolver' },
  request_pending: { icon: Hourglass, action: 'Ver solicitud' },
  request_rejected: { icon: XCircle, action: 'Ver solicitud' },
  request_failed: { icon: AlertTriangle, action: 'Ver solicitud' },
}

export const severityTone: Record<AttentionItem['severity'], Tone> = { critical: 'bad', warning: 'warn', info: 'info' }

/** Text colour per tone, for a phrase that carries its meaning in words as well. */
export const toneClass: Record<Tone, string> = { ok: 'text-success', warn: 'text-warning', bad: 'text-destructive', info: 'text-info', ai: 'text-ai', neutral: 'text-muted-foreground' }

/** What TI watches over and what is the person's own: two lists, TI's first. */
export function splitScopes(items: AttentionItem[]): { it: AttentionItem[]; mine: AttentionItem[] } {
  return { it: items.filter(i => i.scope === 'it'), mine: items.filter(i => i.scope === 'mine') }
}

const sourceName: Record<AttentionSource, string> = {
  approvals: 'las aprobaciones pendientes',
  apps: 'las apps',
  clusters: 'los clústeres',
  requests: 'tus solicitudes',
}

/** A source that could not be read: the list is incomplete, never "todo en orden". */
export function unavailableText(sources: AttentionSource[]): string {
  const names = sources.map(s => sourceName[s] ?? s)
  if (names.length === 0) return ''
  const list = names.length === 1 ? names[0] : `${names.slice(0, -1).join(', ')} y ${names[names.length - 1]}`
  return `No pudimos revisar ${list} ahora, así que esta lista puede estar incompleta.`
}

export const attentionIcon = BellRing

export const statusText: Record<InventoryStatus, { label: string; tone: Tone }> = {
  not_deployed: { label: 'Sin publicar', tone: 'neutral' },
  deploying: { label: 'Publicando', tone: 'info' },
  online: { label: 'En línea', tone: 'ok' },
  failed: { label: 'No se publicó', tone: 'bad' },
}

/** The scan in one phrase, with the tone that carries its meaning. */
export function scanText(s: InventoryScan): { label: string; tone: Tone; detail?: string } {
  switch (s.state) {
    case 'none':
      return { label: 'Sin escaneo', tone: 'neutral', detail: 'Una imagen publicada tal cual no se escanea.' }
    case 'pending':
      return { label: 'Escaneando…', tone: 'info' }
    case 'unknown':
      return { label: 'No se pudo escanear', tone: 'warn', detail: s.error || undefined }
    case 'clean':
      return { label: 'Sin vulnerabilidades graves', tone: 'ok' }
    case 'vulnerable': {
      const parts = []
      if (s.critical) parts.push(`${s.critical} ${s.critical === 1 ? 'crítica' : 'críticas'}`)
      if (s.high) parts.push(`${s.high} ${s.high === 1 ? 'alta' : 'altas'}`)
      return { label: parts.join(', ') || 'Con vulnerabilidades', tone: s.critical ? 'bad' : 'warn' }
    }
  }
}

/** "Hace 2 horas" for the scan, when it has a date. */
export const scannedAgo = (s: InventoryScan) => (s.scanned_at ? formatRelative(s.scanned_at) : '')

/**
 * The scan of the version online, as the app page shows it. `build` is the
 * online deployment's build: null for an image published as it was (never
 * scanned); `scan` null is a scan still on its way.
 */
export function buildScanText(build: Pick<Build, 'status' | 'scan'> | null | undefined, loading = false): { label: string; tone: Tone; detail?: string } | undefined {
  if (loading) return undefined
  if (!build) return { label: 'Sin escaneo', tone: 'neutral', detail: 'Una imagen publicada tal cual no se escanea.' }
  const s = build.scan
  if (!s) return { label: 'Escaneando…', tone: 'info' }
  return scanText(
    s.error
      ? { state: 'unknown', critical: 0, high: 0, error: s.error, scanned_at: s.scanned_at }
      : { state: s.critical || s.high ? 'vulnerable' : 'clean', critical: s.critical, high: s.high, scanned_at: s.scanned_at },
  )
}

const unavailableNames: Record<string, string> = {
  scan: 'el escaneo',
  cluster: 'el clúster',
  uses: 'lo que usa',
  resources: 'los recursos',
  secrets: 'los secretos',
  ai: 'la IA',
  egress: 'la salida a internet',
}

/** What could not be read for one row: shown instead of a guess. */
export function rowUnavailable(r: Pick<InventoryRow, 'unavailable'>): string {
  return r.unavailable.map(u => unavailableNames[u] ?? u).join(', ')
}

/** Who created the app: the viewer, or a teammate (the console has no directory of people). */
export const ownerText = (owner: string, me: string) => (owner === me ? 'Tú' : 'Alguien de tu equipo')
