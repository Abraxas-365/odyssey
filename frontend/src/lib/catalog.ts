import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from './api'
import { releaseKeys } from './releases'
import type { CatalogEntry, CatalogOffer, CatalogOfferingDraft, CatalogOfferParam, CatalogParam, CatalogRange, EnvironmentCatalog, PolicyEffect, ProjectTier, ResourceKind } from './types'

const enc = encodeURIComponent

export const catalogKeys = {
  recipes: ['catalog', 'recipes'] as const,
  env: (project: string, env: string) => [...releaseKeys.env(project, env), 'catalog'] as const,
}

/** Every recipe at its offered version, with the organization's offering (docs/catalog.md). */
export function useCatalog() {
  return useQuery({
    queryKey: catalogKeys.recipes,
    queryFn: ({ signal }) => api<{ items: CatalogEntry[] }>('/catalog/recipes', { signal }),
  })
}

/**
 * IT's edit of an offering (catalog:manage). Every environment's view of the
 * catalog changes with it, so those are refreshed too.
 */
export function useUpdateOffering() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ recipe, body }: { recipe: string; body: CatalogOfferingDraft }) =>
      api<CatalogEntry>(`/catalog/recipes/${enc(recipe)}/offering`, { method: 'PUT', body }),
    onSuccess: entry => {
      qc.setQueryData<{ items: CatalogEntry[] }>(catalogKeys.recipes, old => old && { items: old.items.map(e => (e.recipe === entry.recipe ? entry : e)) })
      return Promise.all([
        qc.invalidateQueries({ queryKey: catalogKeys.recipes }),
        qc.invalidateQueries({ predicate: q => q.queryKey.at(-1) === 'catalog' }),
      ])
    },
  })
}

/** What this environment's app can ask for, and how (GET …/catalog: writes nothing). */
export function useEnvironmentCatalog(project: string, env: string | undefined, enabled = true) {
  return useQuery({
    queryKey: catalogKeys.env(project, env ?? ''),
    queryFn: ({ signal }) => api<EnvironmentCatalog>(`/projects/${enc(project)}/environments/${enc(env!)}/catalog`, { signal }),
    enabled: !!env && enabled,
  })
}

// --- IT's offering editor -------------------------------------------------

/** An offering as the form edits it: every range filled in (no "absent = the schema's"). */
export interface OfferingForm {
  version: number
  active: boolean
  tiers: ProjectTier[]
  defaults: ProjectTier[]
  limits: Record<string, { min: number; max: number }>
  preapproved: Record<string, { min: number; max: number }>
}

/** The server's `narrow` (internal/catalog): 0 is "absent", and an inner range only ever tightens. */
const narrow = (outer: { min: number; max: number }, inner: CatalogRange | undefined) => ({
  min: inner?.min && inner.min > outer.min ? inner.min : outer.min,
  max: inner?.max && inner.max < outer.max ? inner.max : outer.max,
})

/** The form of an entry's offering, its ranges resolved as the server resolves them. */
export function offeringForm(e: Pick<CatalogEntry, 'params' | 'offering'>): OfferingForm {
  const o = e.offering
  const limits: OfferingForm['limits'] = {}
  const preapproved: OfferingForm['preapproved'] = {}
  for (const p of e.params) {
    limits[p.name] = narrow({ min: p.min, max: p.max }, o.limits[p.name])
    preapproved[p.name] = narrow(limits[p.name], o.preapproved[p.name])
  }
  return { version: o.version, active: o.active, tiers: [...o.tiers], defaults: [...o.defaults], limits, preapproved }
}

/**
 * The PUT body. A range equal to its outer one is left out (absent: "all of
 * it"), so the stored offering stays what IT meant. Defaults outside the
 * tiers are dropped (the server refuses them).
 */
export function offeringDraft(params: readonly CatalogParam[], f: OfferingForm): CatalogOfferingDraft {
  const limits: Record<string, CatalogRange> = {}
  const preapproved: Record<string, CatalogRange> = {}
  for (const p of params) {
    const l = f.limits[p.name] ?? { min: p.min, max: p.max }
    const a = f.preapproved[p.name] ?? l
    const lr = { ...(l.min !== p.min ? { min: l.min } : {}), ...(l.max !== p.max ? { max: l.max } : {}) }
    const ar = { ...(a.min !== l.min ? { min: a.min } : {}), ...(a.max !== l.max ? { max: a.max } : {}) }
    if (Object.keys(lr).length) limits[p.name] = lr
    if (Object.keys(ar).length) preapproved[p.name] = ar
  }
  return { version: f.version, active: f.active, tiers: [...f.tiers], defaults: f.active ? f.defaults.filter(t => f.tiers.includes(t)) : [], limits, preapproved }
}

/** The first problem of the form, in plain words (undefined: it can be saved). */
export function offeringProblem(params: readonly CatalogParam[], f: OfferingForm): string | undefined {
  if (f.active && f.tiers.length === 0) return 'Elige al menos un nivel de app donde se ofrece, o apágala.'
  for (const p of params) {
    const l = f.limits[p.name]
    const a = f.preapproved[p.name]
    if (!l || !a) continue
    const unit = p.unit ? ` ${unitLabel(p.unit)}` : ''
    if (![l.min, l.max, a.min, a.max].every(Number.isInteger)) return `${p.label}: usa números enteros.`
    if (l.min < p.min || l.max > p.max) return `${p.label}: el límite va de ${p.min} a ${p.max}${unit}.`
    if (l.min > l.max) return `${p.label}: el mínimo no puede ser mayor que el máximo.`
    if (a.min < l.min || a.max > l.max) return `${p.label}: lo que se crea sin pedir aprobación tiene que estar dentro del límite (${l.min}–${l.max}${unit}).`
    if (a.min > a.max) return `${p.label}: el rango sin aprobación está al revés.`
  }
  return undefined
}

/** A parameter's value when not asked for: the schema's, kept inside the offering's limits (the server's DefaultOf). */
export function defaultOf(e: Pick<CatalogEntry, 'offering'>, p: CatalogParam): number {
  const b = narrow({ min: p.min, max: p.max }, e.offering.limits[p.name])
  return Math.min(Math.max(p.default, b.min), b.max)
}

/** Whether saving would change nothing. */
export const offeringUnchanged = (e: Pick<CatalogEntry, 'params' | 'offering'>, f: OfferingForm) =>
  JSON.stringify(offeringDraft(e.params, offeringForm(e))) === JSON.stringify(offeringDraft(e.params, f))

/** The other offering of the same kind that is already the default in a tier (what the server answers 409 for). */
export function defaultTakenBy(entries: readonly CatalogEntry[], recipe: string, tier: ProjectTier): CatalogEntry | undefined {
  const me = entries.find(e => e.recipe === recipe)
  return entries.find(e => e.recipe !== recipe && e.kind === me?.kind && e.offering.active && e.offering.tiers.includes(tier) && e.offering.defaults.includes(tier))
}

/** "GiB" reads as GB in the console (as the rest of it says). */
export const unitLabel = (unit: string) => (unit === 'GiB' ? 'GB' : unit)

// --- What a builder sees when creating ------------------------------------

/** How a value would be created, in plain words: the mark next to the button. */
export type Mark = { effect: PolicyEffect; label: string; text: string }

/**
 * What creating with `values` gets: `verdict` is IT's rules on the defaults
 * (the offer's `rules_verdict`, without the catalog's own floor), made
 * stricter by a value outside the pre-approved range (the catalog then
 * always asks IT) or outside the limits (refused). The final word is the
 * server's when it is sent; this is only what to expect.
 */
export function markOf(verdict: PolicyEffect, params: readonly CatalogOfferParam[], values: Record<string, number>): Mark {
  if (verdict === 'deny') return { effect: 'deny', label: 'No disponible', text: 'Las reglas de TI no lo permiten en esta app.' }
  for (const p of params) {
    const v = values[p.name] ?? p.default
    if (!Number.isInteger(v) || v < p.min || v > p.max) return { effect: 'deny', label: 'Fuera del límite', text: `${p.label}: tu empresa permite entre ${p.min} y ${p.max}${p.unit ? ` ${unitLabel(p.unit)}` : ''}.` }
  }
  const outside = params.filter(p => {
    const v = values[p.name] ?? p.default
    return v < p.preapproved_min || v > p.preapproved_max
  })
  if (outside.length > 0) {
    const p = outside[0]
    const u = p.unit ? ` ${unitLabel(p.unit)}` : ''
    const range = p.preapproved_min > p.min ? `de ${p.preapproved_min} a ${p.preapproved_max}${u}` : `hasta ${p.preapproved_max}${u}`
    return { effect: 'approval', label: 'Pide aprobación', text: `Fuera de lo que TI dejó crear al instante (${p.label.toLowerCase()} ${range}). Se crea cuando TI lo apruebe.` }
  }
  if (verdict === 'approval') return { effect: 'approval', label: 'Pide aprobación', text: 'Las reglas de TI piden aprobarlo en esta app. Se crea cuando TI lo apruebe.' }
  return { effect: 'allow', label: 'Al instante', text: 'Se crea enseguida, sin esperar a TI.' }
}

/** Every parameter at its offered default. */
export const defaultValues = (params: readonly CatalogOfferParam[]) => Object.fromEntries(params.map(p => [p.name, p.default]))

/** The kinds something can be created of here, in the catalog's order. */
export const offeredKinds = (c: Pick<EnvironmentCatalog, 'recipes'>): ResourceKind[] => [...new Set(c.recipes.map(r => r.kind))]

/** The recipes of a kind here, the default first (the server's order). */
export const offersOf = (c: Pick<EnvironmentCatalog, 'recipes'>, kind: ResourceKind): CatalogOffer[] => c.recipes.filter(r => r.kind === kind)

/** What a kind starts at: its default, else its first recipe (several and none the default: the person picks). */
export const startingOffer = (c: Pick<EnvironmentCatalog, 'recipes'>, kind: ResourceKind): CatalogOffer | undefined => {
  const of = offersOf(c, kind)
  return of.find(r => r.default) ?? of[0]
}

/** A parameter's value problem, in plain words (undefined: fine). */
export function paramProblem(p: CatalogOfferParam, v: number): string | undefined {
  if (!Number.isInteger(v) || v < p.min || v > p.max) return `Tu empresa permite entre ${p.min} y ${p.max}${p.unit ? ` ${unitLabel(p.unit)}` : ''}.`
  return undefined
}

/** What can happen to a parameter later, for the field's hint. */
export const mutabilityHint: Record<CatalogOfferParam['mutability'], string> = {
  grows: 'Después solo puede crecer.',
  immutable: 'No se puede cambiar después.',
  mutable: 'Se puede cambiar después.',
}
