import type { ChannelData } from '@/components/odyssey'
import type { Deployment } from '@/lib/types'

/**
 * Who asked for a version, and through what (UX v4 §2). Both come from the
 * server: `requested_via` (derived from the request's OAuth client, never set
 * by a caller) and `requested_by_name` (the directory's name right after it
 * started). The console only words them: no channel is ever guessed.
 */

const assistantNames: Record<string, string> = {
  claude_code: 'Claude Code',
  codex: 'Codex',
  cursor: 'Cursor',
  vscode: 'VS Code',
  antigravity: 'Antigravity',
}

/** The channel chip of a `requested_via`. '' (versions before it was recorded) says so instead of guessing. */
export function channelOf(via: string): ChannelData {
  if (via === 'console') return { kind: 'console', label: 'Consola' }
  if (via === 'github') return { kind: 'github', label: 'GitHub' }
  if (via === 'platform') return { kind: 'platform', label: 'Automático' }
  if (via.startsWith('assistant:')) return { kind: 'assistant', label: assistantNames[via.slice('assistant:'.length)] ?? 'Asistente' }
  return { kind: 'unrecorded', label: 'Canal no registrado' }
}

type Requested = Pick<Deployment, 'requested_by' | 'requested_by_name' | 'requested_via'>

/**
 * The person: "ti", their name, "tu equipo" while the name is not known (or
 * a push nobody asked for), "la plataforma" for its own restores. Never an
 * email.
 */
export function requesterOf(d: Requested, me: string): string {
  if (d.requested_via === 'platform' || (d.requested_by === '' && d.requested_via !== 'github')) return 'la plataforma'
  if (d.requested_by !== '' && d.requested_by === me) return 'ti'
  return d.requested_by_name.trim() || 'tu equipo'
}

/** Who and through what, for a version row or an app card. */
export function attribution(d: Requested, me: string): { by: string; channel: ChannelData } {
  return { by: requesterOf(d, me), channel: channelOf(d.requested_via) }
}
