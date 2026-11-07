import type { PolicyAction, PolicyDraft, PolicyEffect, PolicyEvaluation, PolicyInput, PolicyRule, ProjectTier, ResourceKind } from '@/lib/types'
import type { Tone } from '@/components/odyssey'

/** IT's rules in plain words (UX v3 §11): effects, tiers and actions are never shown by their ids. */
export const effectInfo: Record<PolicyEffect, { label: string; tone: Tone }> = {
  deny: { label: 'Bloquea', tone: 'bad' },
  approval: { label: 'Pide aprobación', tone: 'warn' },
  allow: { label: 'Permite', tone: 'ok' },
}

export const effectOptions: { value: PolicyEffect; title: string; description: string }[] = [
  { value: 'approval', title: 'Pide aprobación', description: 'La acción espera a que alguien de TI la apruebe.' },
  { value: 'deny', title: 'Bloquea', description: 'La acción no se puede hacer mientras la regla aplique.' },
  { value: 'allow', title: 'Permite', description: 'Deja pasar la acción, salvo que otra regla la bloquee o pida aprobación.' },
]

export const tierOptions: { value: ProjectTier; title: string; description: string }[] = [
  { value: 'sandbox', title: 'Sandbox', description: 'Para probar. Las reglas suelen ser las más estrictas con lo que se publica hacia afuera.' },
  { value: 'internal', title: 'Interna', description: 'Herramienta de la empresa. Es el nivel de toda app nueva.' },
  { value: 'production', title: 'Producción', description: 'Atiende a clientes o procesos críticos. Publicar necesita aprobación de TI.' },
]

export const tierLabel = (t: ProjectTier) => tierOptions.find(o => o.value === t)?.title ?? t

/** What a rule can be about (set_secret is not enforced yet, so it is not offered). */
export const actionOptions: { value: PolicyAction; label: string }[] = [
  { value: 'deploy', label: 'Publicar una versión' },
  { value: 'set_access', label: 'Cambiar quién puede abrirla' },
  { value: 'connect_resource', label: 'Conectar una base de datos o archivos' },
  { value: 'delete_resource', label: 'Borrar una base de datos o archivos' },
  { value: 'enable_ai', label: 'Activar la IA' },
  { value: 'rollback', label: 'Volver a una versión anterior' },
  { value: 'set_services', label: 'Cambiar los servicios de una app' },
]

export const accessModeOptions: { value: string; label: string }[] = [
  { value: 'team', label: 'Solo tu equipo' },
  { value: 'organization', label: 'Toda la empresa' },
  { value: 'public', label: 'Cualquiera con el enlace' },
]

/** What `resource.kind` can be, for the actions on a database, cache or files. */
export const resourceKindOptions: { value: ResourceKind; label: string }[] = [
  { value: 'postgres', label: 'Base de datos' },
  { value: 'redis', label: 'Caché' },
  { value: 'files', label: 'Archivos' },
]

/** The actions whose input carries `resource.kind`. */
export const onResource = (a: PolicyAction) => a === 'connect_resource' || a === 'delete_resource'

export const actionLabel = (a: PolicyAction) => actionOptions.find(o => o.value === a)?.label ?? a

/** The sentence that names a rule: what IT wrote, or its name while no description exists. */
export const ruleSentence = (r: Pick<PolicyRule, 'name' | 'description'>) => r.description.trim() || r.name

/** A rule as the PUT body: the whole rule with something changed (PUT replaces). */
export function toDraft(r: PolicyRule, patch: Partial<PolicyDraft> = {}): PolicyDraft {
  return { name: r.name, description: r.description, expression: r.expression, effect: r.effect, approvers: r.approvers, enabled: r.enabled, ...patch }
}

/** The starting point of a new rule: the most common question IT asks. */
export const newRuleDraft = (): PolicyDraft => ({
  name: '',
  description: '',
  expression: "action == 'deploy' && project.tier == 'production'",
  effect: 'approval',
  enabled: true,
})

/** Whether the draft can be sent (the server validates the expression itself). */
export const draftReady = (d: Pick<PolicyDraft, 'name' | 'expression'>) => d.name.trim() !== '' && d.expression.trim() !== ''

/** True when saving would change nothing. */
export function draftUnchanged(r: PolicyRule, d: PolicyDraft) {
  return r.name === d.name && r.description === (d.description ?? '') && r.expression === d.expression && r.effect === d.effect && r.approvers === (d.approvers ?? r.approvers)
}

/**
 * The scan as the rules will see it in the example: `none` is the deployment's
 * first judgment (nothing built yet: `scan.checked` false), `failed` is the
 * second one with no usable scan (it failed, never came or the cluster has
 * none: `scan.checked` true, `scan.known` false) and `done` has the numbers.
 */
export type ScanScenario = 'none' | 'failed' | 'done'

/** The form of "Probar": a made-up action, as the rules will see it. */
export interface Scenario {
  action: PolicyAction
  tier: ProjectTier
  mode: string
  kind: ResourceKind
  scan: ScanScenario
  critical: number
}

export const defaultScenario: Scenario = { action: 'deploy', tier: 'production', mode: 'team', kind: 'postgres', scan: 'none', critical: 0 }

export const scanOptions: { value: ScanScenario; label: string }[] = [
  { value: 'none', label: 'Todavía no se analiza (antes de construir)' },
  { value: 'failed', label: 'No se pudo analizar' },
  { value: 'done', label: 'Ya se analizó' },
]

export function scenarioInput(s: Scenario, candidate?: Candidate): PolicyInput {
  const input: PolicyInput = {
    action: s.action,
    project: { tier: s.tier },
    access: { mode: s.action === 'set_access' ? s.mode : '' },
    resource: { kind: onResource(s.action) ? s.kind : '' },
    scan: {
      known: s.scan === 'done',
      checked: s.scan !== 'none',
      critical: s.scan === 'done' ? Math.max(0, Math.trunc(s.critical) || 0) : 0,
    },
  }
  if (candidate) input.rule = candidateRule(candidate)
  return input
}

/** An unsaved rule to try (the dialog's draft); `replaces` is the saved rule it edits. */
export interface Candidate {
  draft: Pick<PolicyDraft, 'name' | 'expression' | 'effect' | 'approvers'>
  replaces?: string
}

/** The draft as the dry run's `rule`: the server names a nameless one and defaults the approvers. */
export function candidateRule({ draft, replaces }: Candidate): NonNullable<PolicyInput['rule']> {
  const rule: NonNullable<PolicyInput['rule']> = { expression: draft.expression, effect: draft.effect }
  if (draft.name.trim()) rule.name = draft.name.trim()
  if (draft.approvers) rule.approvers = draft.approvers
  if (replaces) rule.replaces = replaces
  return rule
}

/** The variables a condition can read (docs/policy.md "The input"): shown under the editor. */
export const expressionVariables: { name: string; description: string }[] = [
  { name: 'action', description: "lo que se intenta: 'deploy', 'set_access', 'connect_resource', 'delete_resource', 'enable_ai', 'rollback' o 'set_services'" },
  { name: 'project.tier', description: "el nivel de la app: 'sandbox', 'internal' o 'production'" },
  { name: 'project.slug', description: 'el nombre corto de la app' },
  { name: 'env.name', description: 'el entorno' },
  { name: 'access.mode', description: "quién podrá abrirla, solo al cambiar el acceso: 'team', 'organization' o 'public'" },
  { name: 'actor.id', description: 'quién lo hace (una publicación por push de GitHub aparece como «github:usuario»)' },
  { name: 'actor.permissions', description: 'lista de permisos de quien lo hace; vacía en las publicaciones por push' },
  { name: 'resource.kind', description: "el tipo de dato, al conectar o borrar: 'postgres', 'redis' o 'files'" },
  { name: 'scan.known', description: 'si hay un análisis de vulnerabilidades hecho de esta imagen (false mientras no lo hay o si falló)' },
  { name: 'scan.critical', description: 'vulnerabilidades críticas (también scan.high); úsalo junto con scan.known' },
  { name: 'scan.checked', description: 'si ya se revisó el análisis: false al pedir la publicación (antes de construir), true después, haya salido bien o no. «scan.checked && !scan.known» es una imagen que no se pudo analizar' },
  { name: 'services', description: "los servicios que trae la versión (o la lista nueva al cambiar los servicios), cada uno con name, kind ('web' o 'worker'), route y public. Ej.: services.exists(s, s.kind == 'worker')" },
  { name: 'changed', description: 'los nombres de los servicios que cambian frente a lo que está en línea (los que se agregan, quitan o actualizan)' },
  { name: 'topology_changed', description: 'si cambian los servicios mismos: uno nuevo, uno que se va, o uno que cambia de carpeta, ruta o tipo' },
]

/** What the dry-run answers, as one sentence. */
export function decisionText(e: Pick<PolicyEvaluation, 'effect' | 'rules'>): string {
  const names = e.rules.map(r => `«${r.name}»`).join(', ')
  switch (e.effect) {
    case 'deny': return `Se bloquea. ${e.rules.length > 1 ? 'Reglas' : 'Regla'}: ${names}.`
    case 'approval': return `Pide aprobación de TI. ${e.rules.length > 1 ? 'Reglas' : 'Regla'}: ${names}.`
    default: return 'Se permite: ninguna regla lo impide.'
  }
}

export type Outcome = 'matched' | 'not_matched' | 'disabled' | 'error' | 'unknown'

/** What one rule did in the dry-run. */
export function ruleOutcome(e: PolicyEvaluation | undefined, ruleId: string): { kind: Outcome; text: string } {
  return outcomeOf(e?.results.find(x => x.rule.id === ruleId))
}

/**
 * What the UNSAVED rule did: the result whose rule is not among the saved ones
 * (the dry run gives the candidate an id of its own).
 */
export function candidateOutcome(e: PolicyEvaluation | undefined, savedIds: string[]): { kind: Outcome; text: string } {
  return outcomeOf(e?.results.find(x => !savedIds.includes(x.rule.id)))
}

function outcomeOf(r: PolicyEvaluation['results'][number] | undefined): { kind: Outcome; text: string } {
  if (!r) return { kind: 'unknown', text: '' }
  if (r.error) return { kind: 'error', text: `Esta regla no se pudo evaluar${failedClosed[r.effect]}` }
  if (!r.enabled) return { kind: 'disabled', text: 'Está apagada: no se tuvo en cuenta.' }
  return r.matched
    ? { kind: 'matched', text: `Aplica: ${effectInfo[r.effect].label.toLowerCase()}.` }
    : { kind: 'not_matched', text: 'No aplica a esta acción.' }
}

/** A rule that cannot be evaluated counts at its own strength (docs/policy.md); an allow is ignored. */
const failedClosed: Record<PolicyEffect, string> = {
  deny: ', así que bloquea por precaución.',
  approval: ', así que pide aprobación por precaución.',
  allow: ', así que se ignora.',
}
