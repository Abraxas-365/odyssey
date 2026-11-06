import type { AppStatusKind, ChannelData } from '@/components/odyssey'
import { formatRelative } from '@/lib/format'
import type { AppState } from '@/lib/types'
import { accessLabel } from './access'
import { attribution } from './attribution'
import { waitingLabel } from './versions'

/**
 * What an app's card says, from what the API read (GET /app-states). The same
 * words as the app's own page (`appStatus` in versions.ts). Without a state
 * (still loading, or it could not be read) the card says nothing: never a
 * default presented as a fact.
 */
export function cardStatus(s: Pick<AppState, 'status'> | undefined, archived: boolean): { status: AppStatusKind; label?: string } | undefined {
  if (archived) return { status: 'archived' }
  switch (s?.status) {
    case undefined:
      return undefined
    case 'archived':
      return { status: 'archived' }
    case 'waiting_approval':
      return { status: 'waiting', label: waitingLabel }
    case 'deploying':
      return { status: 'building' }
    case 'online':
      return { status: 'live' }
    case 'failed':
      return { status: 'attention', label: 'No se publicó' }
    case 'not_deployed':
      return { status: 'paused', label: 'Sin publicar' }
  }
}

/**
 * Who can open it, as the cluster enforces it now; a different mode saved and
 * not applied yet is said too. Nothing when the API could not tell (nothing
 * online, or unknown).
 */
export function cardAudience(s: Pick<AppState, 'access'> | undefined): string | undefined {
  if (!s?.access) return undefined
  const now = accessLabel(s.access.mode)
  return s.access.pending ? `${now} · cambio pendiente` : now
}

/** The host to show ("visitas.apps.empresa.com"), or nothing. */
export function cardHost(s: Pick<AppState, 'url'> | undefined): string | undefined {
  if (!s?.url) return undefined
  try {
    return new URL(s.url).host
  } catch {
    return undefined
  }
}

/**
 * The row's second line (UX v4 §5.2): "Ana publicó hace 2 h" and through
 * what, from the latest publication the API read (`last_change`): the verb
 * says how it went. Without it, only when the app last changed; nobody named.
 */
export function changeLine(s: Pick<AppState, 'last_change'> | undefined, me: string, fallbackAt: string, now = Date.now()): { text: string; channel?: ChannelData } {
  const c = s?.last_change
  if (!c) return { text: `Actualizada ${formatRelative(fallbackAt, now)}` }
  const { by, channel } = attribution(c, me)
  const [third, second] = verb(c.reason, c.status)
  const when = formatRelative(c.at, now)
  const text = by === 'ti' ? `${second[0].toUpperCase()}${second.slice(1)} ${when}` : `${by[0].toUpperCase()}${by.slice(1)} ${third} ${when}`
  return { text, channel }
}

type Change = NonNullable<AppState['last_change']>

/** [él/ella, tú]: what was done, as it went. */
function verb(reason: Change['reason'], status: Change['status']): [string, string] {
  switch (status) {
    case 'waiting_approval':
      return ['pidió publicar', 'pediste publicar']
    case 'building':
    case 'pending':
    case 'applying':
    case 'verifying':
      return ['está publicando', 'estás publicando']
    case 'failed':
      return ['intentó publicar', 'intentaste publicar']
    case 'cancelled':
      return ['canceló una publicación', 'cancelaste una publicación']
    case 'ready':
      if (reason === 'rollback') return ['volvió a una versión anterior', 'volviste a una versión anterior']
      if (reason === 'auto_rollback') return ['restauró la versión anterior', 'restauraste la versión anterior']
      if (reason === 'config') return ['aplicó un cambio de configuración', 'aplicaste un cambio de configuración']
      return ['publicó', 'publicaste']
  }
}
