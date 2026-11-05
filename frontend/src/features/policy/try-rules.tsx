import { useState } from 'react'
import { Play } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Callout, ErrorNotice } from '@/components/odyssey'
import { useEvaluatePolicy } from '@/lib/policy'
import type { PolicyAction, PolicyEvaluation, ProjectTier } from '@/lib/types'
import { accessModeOptions, actionOptions, decisionText, defaultScenario, effectInfo, scanOptions, scenarioInput, tierOptions, type Candidate, type Scenario, type ScanScenario } from './rules'

const labelOf = (opts: { value: string; label?: string; title?: string }[]) => Object.fromEntries(opts.map(o => [o.value, o.label ?? o.title ?? o.value]))

/**
 * "Probar": asks the rules IN FORCE what they would answer to a made-up action
 * (POST /policy/evaluate, a dry run: nothing is recorded or stopped). The
 * answer is the decision plus, when `outcome` is given, what one rule did.
 * With a `candidate` (the draft being typed, saved or not) it is tried among
 * the saved rules as if it were enabled.
 */
export function TryRules({ outcome, onResult, candidate, disabled }: { outcome?: (e: PolicyEvaluation) => string; onResult?: (e: PolicyEvaluation) => void; candidate?: Candidate; disabled?: boolean }) {
  const [scenario, setScenario] = useState<Scenario>(defaultScenario)
  const evaluate = useEvaluatePolicy()
  const result = evaluate.data
  const set = <K extends keyof Scenario>(k: K, v: Scenario[K]) => setScenario(s => ({ ...s, [k]: v }))
  const run = () => evaluate.mutate(scenarioInput(scenario, candidate), { onSuccess: onResult })
  const actions = labelOf(actionOptions)
  const tiers = labelOf(tierOptions)
  const modes = labelOf(accessModeOptions)
  const scans = labelOf(scanOptions)

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
        </Callout>
      )}
    </div>
  )
}
