import type { AppAI } from '@/lib/types'
import type { StateTone } from './resources'

/** The AI's state in one badge, in plain words ("Activa", "Preparando la llave…"). */
export function aiState(a: Pick<AppAI, 'status'>): { label: string; tone: StateTone; busy: boolean } {
  switch (a.status) {
    case 'enabling':
      return { label: 'Activando…', tone: 'info', busy: true }
    case 'enabled':
      return { label: 'Activa', tone: 'success', busy: false }
    case 'rotating':
      return { label: 'Renovando la llave…', tone: 'info', busy: true }
    case 'revoke_failed':
      return { label: 'Llave por revocar', tone: 'warning', busy: false }
    case 'disabling':
      return { label: 'Desactivando…', tone: 'secondary', busy: true }
    default:
      return { label: 'Sin activar', tone: 'secondary', busy: false }
  }
}

/** What each button may do now: one change at a time, and only while it makes sense. */
export function aiActions(a: Pick<AppAI, 'status' | 'available'>, canWrite: boolean, busy: boolean) {
  const idle = canWrite && !busy && !aiState(a).busy
  return {
    // while a key waits to be revoked the app already runs without the AI: it may turn it on again (a new key)
    enable: idle && a.available && (a.status === 'off' || a.status === 'revoke_failed'),
    retry: idle && a.status === 'revoke_failed',
    rotate: idle && a.status === 'enabled',
    disable: idle && a.status === 'enabled',
  }
}

/** Why the AI cannot be turned on here, for the person using the app (undefined: it can, or it is already on). */
export function aiUnavailable(a: Pick<AppAI, 'status' | 'available' | 'message'>): string | undefined {
  if (a.available || a.status !== 'off') return undefined
  return a.message || 'La IA no está disponible para esta app. Pide a TI que la habilite.'
}

/** The variables the app reads, without values: the key is never shown. */
export const aiVariablesText = (a: Pick<AppAI, 'variables'>) => a.variables.join('  ')
