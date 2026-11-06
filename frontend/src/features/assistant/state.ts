import { formatRelative } from '@/lib/format'
import type { Assistant, Me } from '@/lib/types'

/**
 * What the console says about the person's AI assistant (UX v4 §6.1). The
 * only place that reads `/me.assistant.connected`: every surface (header
 * chip, Inicio, the assistant page) asks here, so none can disagree or
 * guess. `unknown` is "we could not ask" (IAMKit did not answer), never
 * "not connected".
 */
export type AssistantState =
  | { kind: 'off' }
  | { kind: 'unknown' }
  | { kind: 'none' }
  | { kind: 'connected'; name: string; others: number; lastUsed?: string; readOnly: boolean }

/**
 * `offered` is whether this installation offers an assistant to connect
 * (GET /assistant: MCP served and some client listed; see `offered`). Not
 * offered and nothing connected: there is nothing to say. Not offered but
 * one connected: it is still connected (and can be disconnected). `me`
 * undefined while loading or failed: unknown.
 */
export function assistantState(me: Me | undefined, offered: boolean | undefined, now = Date.now()): AssistantState {
  const a = me?.assistant
  if (offered === false && !a?.connected.length) return { kind: 'off' }
  if (!me || !a) return { kind: 'unknown' }
  const [last, ...rest] = a.connected
  if (!last) return { kind: 'none' }
  return {
    kind: 'connected',
    name: last.name,
    others: rest.length,
    lastUsed: last.last_used_at ? formatRelative(last.last_used_at, now) : undefined,
    readOnly: readOnly(me.permissions),
  }
}

/** Whether there is an assistant to connect here: MCP served and some client offered (undefined until GET /assistant answers). */
export function offered(a: Pick<Assistant, 'available' | 'clients'> | undefined): boolean | undefined {
  return a && a.available && a.clients.length > 0
}

/** Only reading permissions: what the assistant does, it does reading. */
export function readOnly(permissions: string[]): boolean {
  return permissions.length > 0 && permissions.every(p => p.endsWith(':read'))
}

/** How the chip looks: a call to action only when we know none is connected. */
export function chipKind(s: AssistantState): 'connect' | 'unknown' | 'connected' {
  return s.kind === 'connected' ? 'connected' : s.kind === 'none' ? 'connect' : 'unknown'
}

/** The chip's words: "Conecta tu asistente", "Claude Code · hace 5 min · solo lectura". */
export function chipLabel(s: AssistantState): string | undefined {
  switch (s.kind) {
    case 'off':
      return undefined
    case 'unknown':
      return 'Tu asistente'
    case 'none':
      return 'Conecta tu asistente'
    case 'connected':
      return [s.name + (s.others ? ` +${s.others}` : ''), s.lastUsed, s.readOnly ? 'solo lectura' : undefined].filter(Boolean).join(' · ')
  }
}
