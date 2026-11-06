import type { AuditEntry, AuditPerson, AuditSummary, AuditVerification } from '@/lib/types'
import { formatRelative } from '@/lib/format'
import { channelOf, isAssistant } from '@/features/apps/attribution'

/**
 * The company log's words (UX v4 §5.5). The channel and the name come from
 * the server (`channel`, `actor_name`); the console only words them, never
 * guesses: an entry from before the channel was recorded says so.
 */

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`

/**
 * "Esta semana: 12 publicaciones, 9 con un asistente, 3 desde GitHub · 4
 * personas." Only the channels that happened are named.
 */
export function weekSentence(s: AuditSummary): string {
  if (s.events === 0) return 'Esta semana no hubo actividad.'
  const by = (pick: (ch: string) => boolean) => s.by_channel.filter(c => pick(c.channel)).reduce((n, c) => n + c.count, 0)
  const parts: [number, string, string][] = [
    [by(isAssistant), 'con un asistente', 'con un asistente'],
    [by(ch => ch === 'console'), 'desde la consola', 'desde la consola'],
    [by(ch => ch === 'github'), 'desde GitHub', 'desde GitHub'],
    [by(ch => ch === 'platform'), 'automática', 'automáticas'],
    [by(ch => ch === ''), 'sin canal registrado', 'sin canal registrado'],
  ]
  const detail = parts.filter(([n]) => n > 0).map(([n, one, many]) => plural(n, one, many))
  const publications = s.deployments === 0 ? 'ninguna publicación' : plural(s.deployments, 'publicación', 'publicaciones')
  const people = s.people > 0 ? ` · ${plural(s.people, 'persona', 'personas')}` : ''
  return `Esta semana: ${[publications, ...detail].join(', ')}${people}.`
}

/**
 * Who did it: "Tú", their name, "GitHub" for a push nobody pressed, "La
 * plataforma" for its own actions, "Alguien de tu equipo" while the
 * directory does not know them. Never an id (technical mode shows it).
 */
export function whoOf(e: Pick<AuditEntry, 'actor_id' | 'actor_name' | 'channel'>, me: string): string {
  if (e.actor_id === '') return e.channel === 'github' ? 'GitHub' : 'La plataforma'
  if (e.actor_id === me) return 'Tú'
  return e.actor_name.trim() || 'Alguien de tu equipo'
}

/** The person filter's label: their name, or a short id when the directory does not know them. */
export function personLabel(p: AuditPerson, me: string): string {
  if (p.actor_id === me) return p.name.trim() ? `${p.name.trim()} (tú)` : 'Tú'
  return p.name.trim() || `Sin nombre (${p.actor_id.slice(0, 8)})`
}

/** The app an entry is about, from its payload (what the `project_id` filter matches). */
export function projectOf(e: Pick<AuditEntry, 'payload'>): string | undefined {
  const id = (e.payload as Record<string, unknown>)['project_id']
  return typeof id === 'string' && id !== '' ? id : undefined
}

export { channelOf }

export type SealState =
  | { kind: 'checking' }
  | { kind: 'unknown' }
  | { kind: 'empty' }
  | { kind: 'ok'; text: string }
  | { kind: 'broken'; at: number | null; entries: number }

/** The integrity seal: what the chain's verification answered, and when. */
export function sealOf(v: AuditVerification | undefined, failed: boolean, now = Date.now()): SealState {
  if (!v) return failed ? { kind: 'unknown' } : { kind: 'checking' }
  if (!v.valid) return { kind: 'broken', at: v.broken_at, entries: v.entries }
  if (v.entries === 0) return { kind: 'empty' }
  const when = formatRelative(v.verified_at, now)
  return { kind: 'ok', text: `Registro íntegro · verificado ${when === 'ahora' ? 'ahora' : when}` }
}
