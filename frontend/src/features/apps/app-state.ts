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
 * The card's "hace 2 h · Ana" and channel, from the latest publication the
 * API read (`last_change`); without it the card keeps the project's own date
 * and names nobody.
 */
export function cardChange(s: Pick<AppState, 'last_change'> | undefined, me: string, now = Date.now()): { updated?: string; changedBy?: string; channel?: ChannelData } {
  const c = s?.last_change
  if (!c) return {}
  const { by, channel } = attribution(c, me)
  return { updated: formatRelative(c.at, now), changedBy: by, channel }
}
