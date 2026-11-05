import { Building2, Database, Globe, KeyRound, Sparkles, UserCheck, Users, type LucideIcon } from 'lucide-react'
import type { Tone } from '@/components/odyssey/tone'
import type { EnvironmentUses, UsesAttention, UsesAttentionKind } from '@/lib/types'
import { accessLabel } from './access'
import { aiState } from './ai'
import { kindText } from './resources'

/** Where each row leads: the id of the panel below that changes it (see the `id`s in the panels). */
export const anchors = {
  access: 'acceso',
  data: 'datos',
  secrets: 'secretos',
  ai: 'ia',
} as const

export type UsesRowKey = 'access' | 'identity' | 'data' | 'secrets' | 'ai' | 'egress'

export interface UsesRow {
  /** Unique within the card. */
  key: string
  area: UsesRowKey
  icon: LucideIcon
  /** What the row is about: "Acceso", "Datos", "Secretos"… */
  label: string
  /** The state in plain words: "Solo tu equipo", "2 por cargar". */
  text: string
  tone: Tone
  /** Variable names, header names… only with the technical toggle. */
  technical?: string
  /** The id of the panel that changes it, if there is one. */
  anchor?: string
}

const accessIcon = { team: Users, organization: Building2, public: Globe } as const

/** What each Attention kind looks like: the API's message is the text; this is its weight. */
export const attentionTone: Record<UsesAttentionKind, Tone> = {
  secret_missing: 'warn',
  secrets_unavailable: 'warn',
  resource_not_ready: 'info',
  resource_failed: 'bad',
  ai_error: 'warn',
  ai_pending: 'info',
  config_pending: 'info',
  config_failed: 'warn',
  egress_not_applied: 'warn',
}

/** The panel a kind of attention is solved in. */
export const attentionAnchor: Record<UsesAttentionKind, string | undefined> = {
  secret_missing: anchors.secrets,
  secrets_unavailable: undefined, // TI's: the cluster or the vault
  resource_not_ready: anchors.data,
  resource_failed: anchors.data,
  ai_error: anchors.ai,
  ai_pending: anchors.ai,
  config_pending: anchors.access,
  config_failed: anchors.access,
  egress_not_applied: undefined, // TI's, in the cluster
}

/** Said under an alert only IT can solve: we do not notify them yet. */
export const ASK_IT = 'Pide a TI que lo revise.'

/** Worst first, so the card opens with what matters. */
const weight: Record<Tone, number> = { bad: 0, warn: 1, info: 2, ai: 3, ok: 4, neutral: 5 }
export const byWeight = <T extends { tone: Tone }>(items: T[]) => [...items].sort((a, b) => weight[a.tone] - weight[b.tone])

/**
 * The alerts: what needs a person (`owner`, or `it`, which says to ask IT).
 * What is in progress (`wait`) is not an alert: it shows as the state of its row.
 */
export function alertsOf(u: EnvironmentUses): (UsesAttention & { tone: Tone })[] {
  return byWeight(u.attention.filter(a => a.who !== 'wait').map(a => ({ ...a, tone: attentionTone[a.kind] })))
}

/** The sections the API could not read now, in the words the card uses. */
const sectionText = { resources: 'datos', secrets: 'secretos', ai: 'IA', egress: 'salida a internet' } as const
export const unavailableText = (u: EnvironmentUses): string | undefined =>
  u.unavailable.length === 0 ? undefined : `No pudimos leer ahora: ${u.unavailable.map(s => sectionText[s]).join(', ')}. Vuelve a cargar en un momento.`

const NOT_READ = 'No se pudo leer ahora'

const resourceTone = { ready: 'ok', preparing: 'info', failed: 'bad', deleting: 'neutral', unknown: 'neutral' } as const
const resourceText = { ready: 'Lista', preparing: 'Preparando…', failed: 'Falló', deleting: 'Eliminando…', unknown: NOT_READ } as const

const aiTone = (status: NonNullable<EnvironmentUses['ai']>['status']): Tone => {
  const s = aiState({ status })
  return s.tone === 'success' ? 'ok' : s.tone === 'warning' ? 'warn' : s.tone === 'info' ? 'info' : 'neutral'
}

/** "2 por cargar" / "3 cargados" / "Sin secretos" / "Todos en la bóveda de la empresa". */
export function secretsText(s: NonNullable<EnvironmentUses['secrets']>): { text: string; tone: Tone } {
  if (s.unavailable > 0 && s.missing.length === 0) return { text: s.unavailable === 1 ? '1 sin llegar a la app' : `${s.unavailable} sin llegar a la app`, tone: 'warn' }
  if (s.missing.length > 0) return { text: s.missing.length === 1 ? '1 por cargar' : `${s.missing.length} por cargar`, tone: 'warn' }
  if (s.total === 0) return { text: 'Sin secretos', tone: 'neutral' }
  if (s.external === s.total) return { text: s.total === 1 ? '1 en la bóveda de la empresa' : `${s.total} en la bóveda de la empresa`, tone: 'ok' }
  return { text: s.total === 1 ? '1 cargado' : `${s.total} cargados`, tone: 'ok' }
}

/** The internet row: open, limited and confirmed, limited but not confirmed, or no cluster to ask. */
export function egressText(e: NonNullable<EnvironmentUses['egress']>): { text: string; tone: Tone } {
  if (!e.mode) return { text: 'Aún sin clúster', tone: 'neutral' }
  if (e.mode === 'internet') return { text: 'Sin límites', tone: 'ok' }
  if (e.enforced) return { text: 'Solo a lo permitido por TI', tone: 'ok' }
  return { text: 'Limitada, sin confirmar todavía', tone: 'warn' }
}

/**
 * The rows of "Lo que usa esta app", in the order a person asks: who opens it,
 * what it stores, what it needs, what it calls. One row per resource, so a
 * failed database is not hidden behind "Datos".
 */
export function usesRows(u: EnvironmentUses): UsesRow[] {
  const access: UsesRow = {
    key: 'access',
    area: 'access',
    icon: accessIcon[u.access.mode],
    label: 'Acceso',
    text: u.access.failed
      ? `${accessLabel(u.access.mode)} · sin aplicar`
      : u.access.pending
        ? `${accessLabel(u.access.mode)} · aplicándose`
        : accessLabel(u.access.mode),
    tone: u.access.failed ? 'warn' : u.access.pending ? 'info' : 'ok',
    technical: u.access.mode,
    anchor: anchors.access,
  }
  const identity: UsesRow = {
    key: 'identity',
    area: 'identity',
    icon: UserCheck,
    label: 'Identidad',
    text: u.identity.verifiable ? 'La app sabe quién la abre' : 'La app no sabe quién la abre',
    tone: u.identity.verifiable ? 'ok' : 'neutral',
    technical: u.identity.verifiable ? 'Cabecera X-Odicea-Assertion firmada' : 'Sin cabecera de identidad (acceso público)',
    anchor: anchors.access,
  }
  const data: UsesRow[] =
    u.resources.length === 0
      ? [{ key: 'data', area: 'data', icon: Database, label: 'Datos', text: 'Sin base de datos ni archivos', tone: 'neutral', anchor: anchors.data }]
      : u.resources.map(r => ({
          key: `data:${r.name}`,
          area: 'data' as const,
          icon: kindText[r.kind].icon,
          label: `${kindText[r.kind].label} · ${r.name}`,
          text: resourceText[r.status],
          tone: resourceTone[r.status],
          technical: [kindText[r.kind].technical, ...r.variables, r.detail].filter(Boolean).join(' · '),
          anchor: anchors.data,
        }))
  const secrets: UsesRow = {
    key: 'secrets',
    area: 'secrets',
    icon: KeyRound,
    label: 'Secretos',
    ...(u.secrets ? secretsText(u.secrets) : { text: NOT_READ, tone: 'neutral' as const }),
    technical: u.secrets && u.secrets.missing.length > 0 ? `Faltan: ${u.secrets.missing.join(', ')}` : undefined,
    anchor: anchors.secrets,
  }
  const ai: UsesRow = {
    key: 'ai',
    area: 'ai',
    icon: Sparkles,
    label: 'IA',
    ...(u.ai
      ? {
          text: aiState({ status: u.ai.status }).label,
          tone: u.ai.status === 'off' && !u.ai.available ? ('neutral' as const) : aiTone(u.ai.status),
          technical: `Estado: ${u.ai.status}`,
        }
      : { text: NOT_READ, tone: 'neutral' as const }),
    anchor: anchors.ai,
  }
  const egress: UsesRow = {
    key: 'egress',
    area: 'egress',
    icon: Globe,
    label: 'Salida a internet',
    ...(u.egress ? egressText(u.egress) : { text: NOT_READ, tone: 'neutral' as const }),
    technical: u.egress?.mode ? `${u.egress.mode} · ${u.egress.state || 'sin estado'}` : undefined,
  }
  return [access, identity, ...data, secrets, ai, egress]
}
