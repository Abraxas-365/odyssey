import { describe, expect, it } from 'vitest'
import type { CatalogOfferParam, EnvironmentCatalog } from './types'
import { defaultOf, defaultTakenBy, growthOf, markOf, offeredKinds, offeringDraft, offeringForm, offeringProblem, offeringUnchanged, offersOf, paramProblem, startingOffer, teamNames } from './catalog'
import { catalogEntry as entry } from './catalog.fixture'

const size = { name: 'storage_gi', label: 'Tamaño', unit: 'GiB', min: 1, max: 100, default: 1, mutability: 'grows' as const }

const offerParam = (over: Partial<CatalogOfferParam> = {}): CatalogOfferParam => ({ ...size, preapproved_min: 1, preapproved_max: 100, ...over })

describe('catalog offering form', () => {
  it('resolves absent ranges as the server does (absent or 0 = the outer one)', () => {
    const f = offeringForm(entry({}, { limits: { storage_gi: { max: 50 } }, preapproved: { storage_gi: { max: 10 } } }))
    expect(f.limits.storage_gi).toEqual({ min: 1, max: 50 })
    expect(f.preapproved.storage_gi).toEqual({ min: 1, max: 10 })
    expect(offeringForm(entry()).preapproved.storage_gi).toEqual({ min: 1, max: 100 })
    // a pre-approved range wider than the limits is clipped, like catalog.narrow
    expect(offeringForm(entry({}, { limits: { storage_gi: { max: 20 } }, preapproved: { storage_gi: { max: 80 } } })).preapproved.storage_gi).toEqual({ min: 1, max: 20 })
  })

  it('sends only what narrows, and drops defaults outside the tiers', () => {
    const e = entry()
    const f = offeringForm(e)
    expect(offeringDraft(e.params, f)).toEqual({ version: 1, active: true, tiers: ['sandbox', 'internal', 'production'], defaults: ['sandbox', 'internal', 'production'], limits: {}, preapproved: {}, teams: [] })
    const narrowed = { ...f, tiers: ['internal' as const], limits: { storage_gi: { min: 1, max: 50 } }, preapproved: { storage_gi: { min: 1, max: 50 } } }
    expect(offeringDraft(e.params, narrowed)).toEqual({ version: 1, active: true, tiers: ['internal'], defaults: ['internal'], limits: { storage_gi: { max: 50 } }, preapproved: {}, teams: [] })
    expect(offeringDraft(e.params, { ...f, preapproved: { storage_gi: { min: 1, max: 10 } } }).preapproved).toEqual({ storage_gi: { max: 10 } })
    // switched off: no default anywhere
    expect(offeringDraft(e.params, { ...f, active: false }).defaults).toEqual([])
  })

  it('round-trips: an untouched form changes nothing', () => {
    const e = entry({}, { limits: { storage_gi: { max: 50 } }, preapproved: { storage_gi: { max: 10 } } })
    expect(offeringUnchanged(e, offeringForm(e))).toBe(true)
    expect(offeringUnchanged(e, { ...offeringForm(e), active: false })).toBe(false)
  })

  it('says what is wrong in plain words', () => {
    const e = entry()
    const f = offeringForm(e)
    expect(offeringProblem(e.params, f)).toBeUndefined()
    expect(offeringProblem(e.params, { ...f, tiers: [] })).toMatch(/al menos un nivel/)
    expect(offeringProblem(e.params, { ...f, active: false, tiers: [] })).toBeUndefined()
    expect(offeringProblem(e.params, { ...f, limits: { storage_gi: { min: 1, max: 200 } } })).toMatch(/de 1 a 100 GB/)
    expect(offeringProblem(e.params, { ...f, limits: { storage_gi: { min: 30, max: 20 } }, preapproved: { storage_gi: { min: 30, max: 20 } } })).toMatch(/mayor que el máximo/)
    expect(offeringProblem(e.params, { ...f, limits: { storage_gi: { min: 1, max: 20 } }, preapproved: { storage_gi: { min: 1, max: 30 } } })).toMatch(/dentro del límite \(1–20 GB\)/)
    expect(offeringProblem(e.params, { ...f, preapproved: { storage_gi: { min: 1, max: Number.NaN } } })).toMatch(/enteros/)
    for (const t of [offeringProblem(e.params, { ...f, tiers: [] })]) expect(t).not.toMatch(/storage_gi|tier|preapproved/)
  })

  it("finds who already holds a tier's default of the same kind", () => {
    const basic = entry()
    const prod = entry({ recipe: 'postgres-produccion', title: 'PostgreSQL de producción' }, { defaults: [] })
    const redis = entry({ recipe: 'redis-basico', kind: 'redis' })
    expect(defaultTakenBy([basic, prod, redis], 'postgres-produccion', 'production')?.recipe).toBe('postgres-basico')
    expect(defaultTakenBy([basic, prod, redis], 'postgres-basico', 'production')).toBeUndefined()
    expect(defaultTakenBy([entry({}, { active: false }), prod], 'postgres-produccion', 'production')).toBeUndefined()
  })

  it('teams: sent sorted only when chosen; defaults clash only for the same people', () => {
    const e = entry({}, { teams: ['grp_b'] })
    const f = offeringForm(e)
    expect(f.creators).toBe('teams')
    expect(offeringUnchanged(e, f)).toBe(true)
    expect(offeringDraft(e.params, { ...f, teams: ['grp_b', 'grp_a', 'grp_a'] }).teams).toEqual(['grp_a', 'grp_b'])
    // back to everyone: the chosen teams are dropped
    expect(offeringDraft(e.params, { ...f, creators: 'all' }).teams).toEqual([])
    expect(offeringProblem(e.params, { ...f, teams: [] })).toMatch(/al menos un equipo/)

    const everyone = entry()
    const datos = entry({ recipe: 'postgres-datos' }, { teams: ['grp_datos'] })
    const prod = entry({ recipe: 'postgres-produccion' }, { defaults: [] })
    // a team default next to everyone's: no clash; two for overlapping teams: clash
    expect(defaultTakenBy([everyone, prod], 'postgres-produccion', 'production', ['grp_datos'])).toBeUndefined()
    expect(defaultTakenBy([datos, prod], 'postgres-produccion', 'production', ['grp_datos', 'grp_x'])?.recipe).toBe('postgres-datos')
    expect(defaultTakenBy([datos, prod], 'postgres-produccion', 'production', ['grp_ventas'])).toBeUndefined()
    expect(defaultTakenBy([datos, prod], 'postgres-produccion', 'production')).toBeUndefined()
    expect(teamNames(['grp_datos', 'grp_old'], [{ id: 'grp_datos', name: 'Datos', members: 1, directory: false }])).toEqual(['Datos', 'un equipo que ya no existe'])
  })

  it('keeps a default inside the limits', () => {
    expect(defaultOf(entry({}, { limits: { storage_gi: { min: 5 } } }), size)).toBe(5)
    expect(defaultOf(entry(), size)).toBe(1)
  })
})

describe('what a builder sees when creating', () => {
  it('marks a request as at once, with approval or out of the limits', () => {
    const p = [offerParam({ max: 50, preapproved_max: 10 })]
    expect(markOf('allow', p, { storage_gi: 5 })).toMatchObject({ effect: 'allow', label: 'Al instante' })
    expect(markOf('allow', p, { storage_gi: 20 })).toMatchObject({ effect: 'approval', label: 'Pide aprobación' })
    expect(markOf('allow', p, { storage_gi: 20 }).text).toMatch(/hasta 10 GB/)
    expect(markOf('allow', p, { storage_gi: 60 })).toMatchObject({ effect: 'deny', label: 'Fuera del límite' })
    expect(markOf('allow', p, { storage_gi: Number.NaN }).effect).toBe('deny')
    // IT's rules on the defaults: approval stays approval, deny wins
    expect(markOf('approval', p, { storage_gi: 5 }).effect).toBe('approval')
    expect(markOf('deny', p, { storage_gi: 5 }).effect).toBe('deny')
    // nothing asked: the defaults
    expect(markOf('allow', p, {}).effect).toBe('allow')
    // a default IT pre-approved less than (the offer's verdict is approval
    // only because of it): a value inside the range is instant again
    const above = [offerParam({ max: 50, default: 20, preapproved_max: 10 })]
    expect(markOf('allow', above, {}).effect).toBe('approval')
    expect(markOf('allow', above, { storage_gi: 5 }).effect).toBe('allow')
    for (const m of [markOf('allow', p, { storage_gi: 20 }), markOf('allow', p, { storage_gi: 60 })]) expect(m.text).not.toMatch(/storage_gi|GiB|preapproved/)
  })

  it('checks a value against the limits', () => {
    expect(paramProblem(offerParam({ max: 50 }), 10)).toBeUndefined()
    expect(paramProblem(offerParam({ max: 50 }), 51)).toBe('Tu empresa permite entre 1 y 50 GB.')
    expect(paramProblem(offerParam(), 1.5)).toBeDefined()
  })

  it('starts each kind at its default, or its first option', () => {
    const offer = (recipe: string, kind: 'postgres' | 'redis', isDefault: boolean) => ({ kind, recipe, recipe_version: 1, title: recipe, description: '', default: isDefault, params: [], variables: [], verdict: 'allow' as const, rules: [], rules_verdict: 'allow' as const })
    const c: Pick<EnvironmentCatalog, 'recipes'> = { recipes: [offer('postgres-produccion', 'postgres', false), offer('postgres-basico', 'postgres', true), offer('redis-basico', 'redis', false)] }
    expect(offeredKinds(c)).toEqual(['postgres', 'redis'])
    expect(offersOf(c, 'postgres').map(o => o.recipe)).toEqual(['postgres-produccion', 'postgres-basico'])
    expect(startingOffer(c, 'postgres')?.recipe).toBe('postgres-basico')
    expect(startingOffer(c, 'redis')?.recipe).toBe('redis-basico')
    expect(startingOffer(c, 'files' as never)).toBeUndefined()
  })

  it('grows a resource within its recipe offering, saying when it asks IT', () => {
    const g = growthOf(entry({}, { active: false, limits: { storage_gi: { max: 50 } }, preapproved: { storage_gi: { max: 10 } } }), 1)!
    expect(g.max).toBe(50)
    expect(g.problem(20, 8)).toBeUndefined()
    expect(g.problem(8, 8)).toBeUndefined()
    expect(g.problem(4, 8)).toBe('Solo puede crecer: ahora tiene 8 GB.')
    expect(g.problem(60, 8)).toBe('Tu empresa permite hasta 50 GB.')
    expect(g.problem(Number.NaN, 8)).toBe('Escribe un tamaño en números enteros.')
    expect(g.problem(9.5, 8)).toBe('Escribe un tamaño en números enteros.')
    expect(g.asks(10)).toBe(false)
    expect(g.asks(11)).toBe(true)
    expect(g.hint).toBe('Al instante hasta 10 GB; más, con aprobación de TI.')
    expect(growthOf(entry(), 1)?.hint).toBe('')
    // a floor above 1 is worded as a range
    expect(growthOf(entry({}, { limits: { storage_gi: { min: 5, max: 50 } } }), 1)?.problem(60, 8)).toBe('Tu empresa permite entre 5 y 50 GB.')
    // an immutable size cannot change at all
    const fixed = entry({ params: [{ name: 'storage_gi', label: 'Tamaño', unit: 'GiB', min: 1, max: 100, default: 1, mutability: 'immutable' }] })
    expect(growthOf(fixed, 1)?.problem(9, 8)).toBe('Tamaño: no se puede cambiar después de crear el recurso.')
    // nothing to say: no entry, no size, or a resource pinned to another version
    expect(growthOf(undefined, 1)).toBeUndefined()
    expect(growthOf(entry({ params: [] }), 1)).toBeUndefined()
    expect(growthOf(entry({ version: 2 }), 1)).toBeUndefined()
  })
})
