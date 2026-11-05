import type { Tone } from '@/components/odyssey'
import { formatRelative } from '@/lib/format'
import type { Approval, ApprovalStatus } from '@/lib/types'

/**
 * Plain-language view of IT's approval requests (UX v3 §10-11, docs/approvals.md):
 * one sentence, who asks, why, which rule, when, until when. Codes and ids
 * (`set_access`, rule ids, permissions) never reach the screen.
 */

export type Tab = 'pending' | 'decided'

/** The statuses each tab asks the API for. */
export const tabStatuses: Record<Tab, ApprovalStatus[]> = {
  pending: ['pending'],
  decided: ['approved', 'rejected', 'expired', 'cancelled'],
}

export const statusInfo: Record<ApprovalStatus, { label: string; tone: Tone }> = {
  pending: { label: 'Esperando a TI', tone: 'info' },
  approved: { label: 'Aprobada', tone: 'ok' },
  rejected: { label: 'Rechazada', tone: 'bad' },
  expired: { label: 'Venció', tone: 'neutral' },
  cancelled: { label: 'Cancelada', tone: 'neutral' },
}

/** Who asked: the viewer, a teammate, or the platform (a GitHub push). */
export function requesterName(a: Pick<Approval, 'mine' | 'requested_by' | 'requested_system'>): string {
  if (a.mine) return 'Tú'
  if (a.requested_system) {
    const login = a.requested_system.startsWith('github:') ? a.requested_system.slice('github:'.length) : ''
    return login ? `Un cambio en GitHub de ${login}` : 'Un cambio en GitHub'
  }
  return 'Alguien de tu equipo'
}

/** The rules that asked, in words: «A» y «B». */
export function ruleNames(a: Pick<Approval, 'rules'>): string {
  const names = a.rules.map(r => `«${r.name}»`)
  if (names.length <= 1) return names.join('')
  return `${names.slice(0, -1).join(', ')} y ${names[names.length - 1]}`
}

/** "Vence en 6 días", "Venció hace 2 horas"; nothing once it was decided. */
export function expiryText(a: Pick<Approval, 'status' | 'expires_at'>, now = Date.now()): string {
  if (a.status !== 'pending') return ''
  const left = new Date(a.expires_at).getTime() - now
  if (Number.isNaN(left)) return ''
  if (left <= 0) return 'Vence en un momento'
  return `Vence ${formatRelative(a.expires_at, now)}`
}

/** How it ended, for the decided list: "Aprobada por TI · hace 2 horas". */
export function outcomeText(a: Pick<Approval, 'status' | 'decided_at' | 'expires_at' | 'mine'>, now = Date.now()): string {
  switch (a.status) {
    case 'pending':
      return ''
    case 'approved':
      return `Aprobada por TI · ${formatRelative(a.decided_at ?? a.expires_at, now)}`
    case 'rejected':
      return `Rechazada por TI · ${formatRelative(a.decided_at ?? a.expires_at, now)}`
    case 'expired':
      return `Nadie la decidió a tiempo · ${formatRelative(a.expires_at, now)}`
    case 'cancelled':
      return `${a.mine ? 'La cancelaste' : 'La canceló quien la pidió'} · ${formatRelative(a.decided_at ?? a.expires_at, now)}`
  }
}

/** The requester can take back their own pending request. */
export const canCancel = (a: Pick<Approval, 'mine' | 'status'>) => a.mine && a.status === 'pending'

/** Approver actions: the API already says whether this viewer may decide now. */
export const canDecide = (a: Pick<Approval, 'can_decide' | 'status'>) => a.can_decide && a.status === 'pending'

/** An approved request whose action could not run: the person asks again. */
export const needsAnotherTry = (a: Pick<Approval, 'status' | 'execution_error'>) => a.status === 'approved' && !!a.execution_error

/** The bell's text: "3 solicitudes esperan a TI". Nothing at zero. */
export function bellText(count: number): string {
  if (count <= 0) return 'No hay solicitudes esperando tu decisión.'
  return count === 1 ? '1 solicitud espera tu decisión' : `${count} solicitudes esperan tu decisión`
}

/** The label the bell and the tab show: a number up to 9, then "9+". */
export const countLabel = (count: number) => (count > 9 ? '9+' : String(count))
