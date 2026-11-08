import { describe, expect, it } from 'vitest'
import type { PolicyEvaluation, PolicyRule } from '@/lib/types'
import { catalogEntry } from '@/lib/catalog.fixture'
import { actionOptions, candidateOutcome, candidateRule, catalogNote, decisionText, defaultScenario, draftReady, draftUnchanged, effectInfo, expressionVariables, newRuleDraft, ruleOutcome, ruleSentence, scenarioInput, scenarioRecipe, scenarioRecipes, tierLabel, tierOptions, toDraft } from './rules'

const rule = (patch: Partial<PolicyRule> = {}): PolicyRule => ({
  id: 'r1', name: 'Publicar en producción requiere aprobación de TI', description: '', expression: "action == 'deploy'", effect: 'approval',
  approvers: 'odyssey:approvals:decide', enabled: true, builtin: null, version: 1, created_by: '', updated_by: '', created_at: '', updated_at: '', ...patch,
})

const evaluation = (patch: Partial<PolicyEvaluation> = {}): PolicyEvaluation => ({ effect: 'allow', rules: [], approvers: [], results: [], ...patch })

describe('policy rules mapping', () => {
  it('names effects and tiers in plain Spanish, never by id', () => {
    expect(effectInfo.deny.label).toBe('Bloquea')
    expect(effectInfo.approval.label).toBe('Pide aprobación')
    expect(effectInfo.allow.label).toBe('Permite')
    expect(tierLabel('sandbox')).toBe('Sandbox')
    expect(tierLabel('internal')).toBe('Interna')
    expect(tierLabel('production')).toBe('Producción')
    const shown = [...tierOptions.flatMap(o => [o.title, o.description]), ...actionOptions.map(o => o.label)]
    for (const text of shown) expect(text).not.toMatch(/set_access|connect_resource|delete_resource|enable_ai|\btier\b/)
  })

  it('does not offer set_secret (not enforced yet)', () => {
    expect(actionOptions.map(o => o.value)).not.toContain('set_secret')
    expect(actionOptions.map(o => o.value)).toContain('deploy')
  })

  it('reads a rule as its description, or its name when it has none', () => {
    expect(ruleSentence(rule())).toBe('Publicar en producción requiere aprobación de TI')
    expect(ruleSentence(rule({ description: '  Nada sale a producción sin TI ' }))).toBe('Nada sale a producción sin TI')
  })

  it('turns a rule into the whole PUT body, changing only what is asked', () => {
    const r = rule({ description: 'x', builtin: 'production_deploy_approval' })
    expect(toDraft(r, { enabled: false })).toEqual({ name: r.name, description: 'x', expression: r.expression, effect: 'approval', approvers: r.approvers, enabled: false })
    expect(draftUnchanged(r, toDraft(r))).toBe(true)
    expect(draftUnchanged(r, toDraft(r, { expression: 'false' }))).toBe(false)
    expect(draftUnchanged(r, toDraft(r, { effect: 'deny' }))).toBe(false)
  })

  it('needs a name and a condition to save', () => {
    expect(draftReady(newRuleDraft())).toBe(false)
    expect(draftReady({ ...newRuleDraft(), name: ' Algo ' })).toBe(true)
    expect(draftReady({ ...newRuleDraft(), name: 'Algo', expression: '  ' })).toBe(false)
  })

  it('builds the dry-run input from the example action', () => {
    expect(scenarioInput(defaultScenario)).toEqual({ action: 'deploy', project: { tier: 'production' }, access: { mode: '' }, resource: { kind: '' }, scan: { known: false, checked: false, critical: 0 } })
    expect(scenarioInput({ ...defaultScenario, action: 'set_access', tier: 'sandbox', mode: 'public' })).toMatchObject({ action: 'set_access', project: { tier: 'sandbox' }, access: { mode: 'public' } })
    // resource.kind is what a rule on databases, caches or files reads (it was never sent)
    expect(scenarioInput({ ...defaultScenario, action: 'connect_resource', kind: 'redis' }).resource).toEqual({ kind: 'redis' })
    expect(scenarioInput({ ...defaultScenario, action: 'delete_resource', kind: 'files' }).resource).toEqual({ kind: 'files' })
    expect(scenarioInput({ ...defaultScenario, action: 'enable_ai', kind: 'redis' }).resource).toEqual({ kind: '' })
    // a count means nothing before the scan exists, and never goes negative
    expect(scenarioInput({ ...defaultScenario, critical: 4 }).scan).toEqual({ known: false, checked: false, critical: 0 })
    expect(scenarioInput({ ...defaultScenario, scan: 'done', critical: 4 }).scan).toEqual({ known: true, checked: true, critical: 4 })
    expect(scenarioInput({ ...defaultScenario, scan: 'done', critical: -3 }).scan?.critical).toBe(0)
    expect(scenarioInput({ ...defaultScenario, scan: 'done', critical: Number.NaN }).scan?.critical).toBe(0)
    // a scan that failed was looked at: what production_requires_scan reads
    expect(scenarioInput({ ...defaultScenario, scan: 'failed', critical: 4 }).scan).toEqual({ known: false, checked: true, critical: 0 })
  })

  it('says the decision as a sentence', () => {
    expect(decisionText(evaluation())).toBe('Se permite: ninguna regla lo impide.')
    expect(decisionText(evaluation({ effect: 'deny', rules: [{ id: '1', name: 'A' }] }))).toBe('Se bloquea. Regla: «A».')
    expect(decisionText(evaluation({ effect: 'approval', rules: [{ id: '1', name: 'A' }, { id: '2', name: 'B' }] }))).toBe('Pide aprobación de TI. Reglas: «A», «B».')
  })

  it('says what one rule did in the dry run', () => {
    const e = evaluation({
      effect: 'approval',
      results: [
        { rule: { id: 'm', name: 'M' }, effect: 'approval', enabled: true, matched: true },
        { rule: { id: 'n', name: 'N' }, effect: 'deny', enabled: true, matched: false },
        { rule: { id: 'o', name: 'O' }, effect: 'deny', enabled: false, matched: false },
        { rule: { id: 'x', name: 'X' }, effect: 'deny', enabled: true, matched: false, error: 'boom' },
      ],
    })
    expect(ruleOutcome(e, 'm')).toEqual({ kind: 'matched', text: 'Aplica: pide aprobación.' })
    expect(ruleOutcome(e, 'n').kind).toBe('not_matched')
    expect(ruleOutcome(e, 'o').kind).toBe('disabled')
    expect(ruleOutcome(e, 'x')).toMatchObject({ kind: 'error' })
    expect(ruleOutcome(e, 'zzz').kind).toBe('unknown')
    expect(ruleOutcome(undefined, 'm').kind).toBe('unknown')
    // an erroring rule counts at its own strength
    expect(ruleOutcome(e, 'x').text).toContain('bloquea')
  })

  it('sends the unsaved draft as the dry run rule, and finds its result', () => {
    const draft = { name: ' Mía ', expression: "action == 'deploy'", effect: 'deny' as const, approvers: '' }
    expect(candidateRule({ draft })).toEqual({ name: 'Mía', expression: "action == 'deploy'", effect: 'deny' })
    expect(candidateRule({ draft: { ...draft, name: '', approvers: 'odyssey:x' }, replaces: 'r1' })).toEqual({ expression: "action == 'deploy'", effect: 'deny', approvers: 'odyssey:x', replaces: 'r1' })
    expect(scenarioInput(defaultScenario, { draft }).rule).toMatchObject({ expression: "action == 'deploy'", effect: 'deny' })
    expect(scenarioInput(defaultScenario).rule).toBeUndefined()
    const e = evaluation({
      results: [
        { rule: { id: 'saved', name: 'S' }, effect: 'deny', enabled: true, matched: false },
        { rule: { id: 'throwaway', name: 'Regla de prueba' }, effect: 'deny', enabled: true, matched: true },
      ],
    })
    expect(candidateOutcome(e, ['saved'])).toEqual({ kind: 'matched', text: 'Aplica: bloquea.' })
    expect(candidateOutcome(e, ['saved', 'throwaway']).kind).toBe('unknown')
  })

  it('lists what a condition can read', () => {
    const names = expressionVariables.map(v => v.name)
    for (const n of ['action', 'project.tier', 'env.name', 'access.mode', 'actor.permissions', 'resource.kind', 'resource.recipe', 'resource.params', 'resource.out_of_range', 'scan.known', 'scan.critical', 'scan.checked', 'services', 'changed', 'topology_changed']) expect(names).toContain(n)
  })

  it('tries a create or a change with what the catalog would resolve', () => {
    const recipe = {
      recipe: 'postgres-basico', version: 2, engine: 'builtin' as const, params: [{ name: 'storage_gi', label: 'Tamaño', unit: 'GiB', min: 1, max: 100, default: 1, mutability: 'grows' as const }],
      offering: { recipe: 'postgres-basico', version: 2, active: true, tiers: ['production' as const], defaults: ['production' as const], limits: {}, preapproved: { storage_gi: { max: 10 } }, revision: 1, updated_by: '', created_at: '', updated_at: '' },
    }
    const create = { ...defaultScenario, action: 'create_resource' as const, size: 20 }
    expect(scenarioInput(create, undefined, recipe).resource).toEqual({ kind: 'postgres', recipe: 'postgres-basico', recipe_version: 2, engine: 'builtin', params: { storage_gi: 20 }, out_of_range: ['storage_gi'] })
    expect(scenarioInput({ ...create, size: 5 }, undefined, recipe).resource?.out_of_range).toEqual([])
    expect(catalogNote(scenarioInput(create, undefined, recipe))).toMatch(/pide aprobación aunque ninguna regla/)
    expect(catalogNote(scenarioInput({ ...create, size: 5 }, undefined, recipe))).toBeUndefined()
    // without a recipe (none by default here) only the kind goes
    expect(scenarioInput(create).resource).toEqual({ kind: 'postgres' })
    // other actions never carry a recipe
    expect(scenarioInput({ ...defaultScenario, action: 'connect_resource' }, undefined, recipe).resource).toEqual({ kind: 'postgres' })
    expect(actionOptions.map(o => o.value)).toEqual(expect.arrayContaining(['create_resource', 'change_resource']))
    for (const o of actionOptions) expect(o.label).not.toMatch(/_resource/)
  })

  it("resolves the scenario's recipe as the server picks it", () => {
    const e = (recipe: string, defaults: ('internal' | 'production')[], active = true) =>
      catalogEntry({ recipe }, { active, tiers: ['internal', 'production'], defaults })
    const basic = e('postgres-basico', ['internal'])
    const prod = e('postgres-produccion', ['production'])
    const create = { action: 'create_resource' as const, kind: 'postgres' as const, tier: 'production' as const }
    expect(scenarioRecipe([basic, prod], { ...create, recipe: '' })?.recipe).toBe('postgres-produccion')
    expect(scenarioRecipe([basic, prod], { ...create, recipe: 'postgres-basico' })?.recipe).toBe('postgres-basico')
    expect(scenarioRecipe([e('a', []), e('b', [])], { ...create, recipe: '' })).toBeUndefined()
    // a create only picks what the tier is offered
    const off = e('off', [], false)
    expect(scenarioRecipes([basic, off], create).map(x => x.recipe)).toEqual(['postgres-basico'])
    expect(scenarioRecipes([catalogEntry({}, { tiers: ['internal'] })], create)).toEqual([])
    expect(scenarioRecipe([basic, off], { ...create, recipe: 'off' })).toBeUndefined()
    // a change: what exists keeps changing when IT withdraws its offering
    const change = { ...create, action: 'change_resource' as const }
    expect(scenarioRecipes([basic, off], change).map(x => x.recipe)).toEqual(['postgres-basico', 'off'])
    expect(scenarioRecipe([basic, off], { ...change, recipe: 'off' })?.recipe).toBe('off')
    expect(scenarioRecipe([off], { ...change, recipe: '' })?.recipe).toBe('off')
  })

  it('offers rules on a change of services, in plain words', () => {
    const o = actionOptions.find(a => a.value === 'set_services')
    expect(o?.label).toBe('Cambiar los servicios de una app')
    expect(o?.label).not.toMatch(/set_services/)
  })
})
