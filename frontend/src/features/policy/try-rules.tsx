import { useState } from 'react'
import { Play } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Callout, ErrorNotice } from '@/components/odyssey'
import { useCatalog } from '@/lib/catalog'
import { useEvaluatePolicy } from '@/lib/policy'
import type { PolicyAction, PolicyEvaluation, ProjectTier, ResourceKind } from '@/lib/types'
import { accessModeOptions, actionOptions, catalogNote, decisionText, defaultScenario, effectInfo, onRecipe, onResource, resourceKindOptions, scanOptions, scenarioInput, scenarioRecipe, scenarioRecipes, tierOptions, type Candidate, type Scenario, type ScanScenario } from './rules'

const labelOf = (opts: { value: string; label?: string; title?: string }[]) => Object.fromEntries(opts.map(o => [o.value, o.label ?? o.title ?? o.value]))

/**
 * "Probar": asks the rules IN FORCE what they would answer to a made-up action
 * (POST /policy/evaluate, a dry run: nothing is recorded or stopped). The
 * answer is the decision plus, when `outcome` is given, what one rule did.
 * With a `candidate` (the draft being typed, saved or not) it is tried among
 * the saved rules as if it were enabled.
 */
export function TryRules({ outcome, onResult, candidate, disabled, initial = defaultScenario }: { outcome?: (e: PolicyEvaluation) => string; onResult?: (e: PolicyEvaluation) => void; candidate?: Candidate; disabled?: boolean; initial?: Scenario }) {
  const [scenario, setScenario] = useState<Scenario>(initial)
  const evaluate = useEvaluatePolicy()
  const catalog = useCatalog()
  const result = evaluate.data
  const [note, setNote] = useState<string>()
  const set = <K extends keyof Scenario>(k: K, v: Scenario[K]) =>
    setScenario(s => ({ ...s, [k]: v, ...(k === 'kind' || k === 'tier' || k === 'action' ? { recipe: '' } : {}) }))
  const entries = catalog.data?.items ?? []
  const recipe = scenarioRecipe(entries, scenario)
  const run = () => {
    const input = scenarioInput(scenario, candidate, recipe)
    evaluate.mutate(input, { onSuccess: e => { setNote(catalogNote(input)); onResult?.(e) } })
  }
  const actions = labelOf(actionOptions)
  const tiers = labelOf(tierOptions)
  const modes = labelOf(accessModeOptions)
  const kinds = labelOf(resourceKindOptions)
  const scans = labelOf(scanOptions)
  const changing = scenario.action === 'change_resource'
  const choices = scenarioRecipes(entries, scenario)
  const fallback = changing ? 'La más probable' : 'La de por defecto'
  const recipes: Record<string, string> = { '': fallback, ...Object.fromEntries(choices.map(e => [e.recipe, e.offering.active ? e.title : `${e.title} (ya no se ofrece)`])) }

  return (
    <div className="flex flex-col gap-3 rounded-lg border bg-muted/30 p-4">
      <div className="text-[13px] font-semibold">Probar con una acción de ejemplo</div>
      <FieldGroup className="gap-3 sm:grid sm:grid-cols-2">
        <Field>
          <FieldLabel>Acción</FieldLabel>
          <Select value={scenario.action} items={actions} onValueChange={v => set('action', (v ?? 'deploy') as PolicyAction)}>
            <SelectTrigger className="w-full" aria-label="Acción"><SelectValue /></SelectTrigger>
            <SelectContent>{actionOptions.map(o => <SelectItem key={o.value} value={o.value}>{o.label}</SelectItem>)}</SelectContent>
          </Select>
        </Field>
        <Field>
          <FieldLabel>Nivel de la app</FieldLabel>
          <Select value={scenario.tier} items={tiers} onValueChange={v => set('tier', (v ?? 'internal') as ProjectTier)}>
            <SelectTrigger className="w-full" aria-label="Nivel de la app"><SelectValue /></SelectTrigger>
            <SelectContent>{tierOptions.map(o => <SelectItem key={o.value} value={o.value}>{o.title}</SelectItem>)}</SelectContent>
          </Select>
        </Field>
        {scenario.action === 'set_access' && (
          <Field>
            <FieldLabel>Quién podrá abrirla</FieldLabel>
            <Select value={scenario.mode} items={modes} onValueChange={v => set('mode', v ?? 'team')}>
              <SelectTrigger className="w-full" aria-label="Quién podrá abrirla"><SelectValue /></SelectTrigger>
              <SelectContent>{accessModeOptions.map(o => <SelectItem key={o.value} value={o.value}>{o.label}</SelectItem>)}</SelectContent>
            </Select>
          </Field>
        )}
        {onResource(scenario.action) && (
          <Field>
            <FieldLabel>{onRecipe(scenario.action) ? 'Qué se crea o agranda' : 'Qué se conecta o borra'}</FieldLabel>
            <Select value={scenario.kind} items={kinds} onValueChange={v => set('kind', (v ?? 'postgres') as ResourceKind)}>
              <SelectTrigger className="w-full" aria-label={onRecipe(scenario.action) ? 'Qué se crea o agranda' : 'Qué se conecta o borra'}><SelectValue /></SelectTrigger>
              <SelectContent>{resourceKindOptions.map(o => <SelectItem key={o.value} value={o.value}>{o.label}</SelectItem>)}</SelectContent>
            </Select>
          </Field>
        )}
        {onRecipe(scenario.action) && (
          <>
            <Field>
              <FieldLabel>Opción del catálogo</FieldLabel>
              <Select value={scenario.recipe} items={recipes} onValueChange={v => set('recipe', v ?? '')}>
                <SelectTrigger className="w-full" aria-label="Opción del catálogo"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="">{fallback}</SelectItem>
                  {choices.map(e => <SelectItem key={e.recipe} value={e.recipe}>{recipes[e.recipe]}</SelectItem>)}
                </SelectContent>
              </Select>
              {!recipe && catalog.data && <FieldDescription>No hay una opción por defecto para este nivel: se prueba sin receta.</FieldDescription>}
            </Field>
            {recipe?.params.some(p => p.name === 'storage_gi') && (
              <Field>
                <FieldLabel htmlFor="try-size">{changing ? 'Tamaño nuevo (GB)' : 'Tamaño (GB)'}</FieldLabel>
                <Input id="try-size" type="number" min={1} value={Number.isNaN(scenario.size) ? '' : scenario.size} onChange={e => set('size', e.target.valueAsNumber)} />
                {changing && <FieldDescription>Se prueba como si cambiara a este tamaño (solo puede crecer, dentro del límite de TI).</FieldDescription>}
              </Field>
            )}
          </>
        )}
        {scenario.action === 'deploy' && (
          <>
            <Field>
              <FieldLabel>Análisis de vulnerabilidades</FieldLabel>
              <Select value={scenario.scan} items={scans} onValueChange={v => set('scan', (v ?? 'none') as ScanScenario)}>
                <SelectTrigger className="w-full" aria-label="Análisis de vulnerabilidades"><SelectValue /></SelectTrigger>
                <SelectContent>{scanOptions.map(o => <SelectItem key={o.value} value={o.value}>{o.label}</SelectItem>)}</SelectContent>
              </Select>
            </Field>
            {scenario.scan === 'done' && (
              <Field>
                <FieldLabel htmlFor="try-critical">Vulnerabilidades críticas</FieldLabel>
                <Input id="try-critical" type="number" min={0} value={scenario.critical} onChange={e => set('critical', Number(e.target.value))} />
              </Field>
            )}
          </>
        )}
      </FieldGroup>
      <div className="flex flex-wrap items-center gap-3">
        <Button type="button" variant="outline" size="sm" disabled={evaluate.isPending || disabled} onClick={run}>
          <Play />
          {evaluate.isPending ? 'Probando…' : 'Probar'}
        </Button>
        <FieldDescription>{candidate ? 'Prueba la condición tal como está escrita, sin guardarla.' : 'No se registra ni se detiene nada.'}</FieldDescription>
      </div>
      <ErrorNotice error={evaluate.error} />
      {result && (
        <Callout tone={effectInfo[result.effect].tone} title={decisionText(result)}>
          {outcome?.(result)}
          {note && <p className={outcome ? 'mt-1' : undefined}>{note}</p>}
        </Callout>
      )}
    </div>
  )
}
