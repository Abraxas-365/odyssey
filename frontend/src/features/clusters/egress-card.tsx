import { useState } from 'react'
import { Globe, Save } from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { Callout, ErrorNotice, Loading, OptionCards } from '@/components/odyssey'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { egressStates, internalEntries, parseAllowList, useClusterEgress, useSetClusterEgress } from '@/lib/clusters'
import { formatRelative } from '@/lib/format'
import type { Cluster, ClusterEgress } from '@/lib/types'

type Mode = ClusterEgress['mode']

/**
 * "Salida a internet" (TI, clusters:manage): what the apps of a cluster may
 * reach. docs/egress.md. The state says whether the cluster's agent applied
 * the choice.
 */
export function EgressCard({ cluster }: { cluster: Cluster }) {
  const egress = useClusterEgress(cluster.id)
  if (egress.isPending) return <div className="px-5 py-4"><Loading label="Cargando la salida a internet…" /></div>
  if (!egress.data) return <div className="px-5 py-4"><ErrorNotice error={egress.error} onRetry={() => egress.refetch()} /></div>
  // Keyed by generation: a change made elsewhere resets the draft.
  return <EgressForm key={egress.data.generation} cluster={cluster} egress={egress.data} />
}

function detail(egress: ClusterEgress, connected: boolean): string | undefined {
  switch (egress.state) {
    case 'applied': return egress.acknowledged_at ? `Confirmado ${formatRelative(egress.acknowledged_at)}.` : undefined
    case 'pending': return connected ? 'Debería confirmarse en unos segundos.' : 'El clúster está sin conexión: lo aplica al volver.'
    case 'failed': return egress.error || 'Revisa el agente del clúster.'
    default: return undefined
  }
}

function EgressForm({ cluster, egress }: { cluster: Cluster; egress: ClusterEgress }) {
  const set = useSetClusterEgress()
  const [mode, setMode] = useState<Mode>(egress.mode)
  const [text, setText] = useState(egress.allow.join('\n'))
  const parsed = parseAllowList(text)
  const unchanged = mode === egress.mode && parsed.entries.join('\n') === egress.allow.join('\n')
  const state = egressStates[egress.state]
  const revoked = cluster.status === 'revoked'
  const [confirming, setConfirming] = useState(false)
  // Only what this save adds: an internal entry kept from before was confirmed then.
  const internal = internalEntries(parsed.entries.filter(e => !egress.allow.includes(e)))

  const send = () =>
    set.mutate({ id: cluster.id, body: { mode, allow: parsed.entries } }, {
      onSuccess: () => toast.success('Guardado. El clúster lo aplica en unos segundos.'),
      onSettled: () => setConfirming(false),
    })
  const save = () => (internal.length > 0 ? setConfirming(true) : send())

  return (
    <div className="space-y-4 px-5 py-4" aria-label="Salida a internet">
      <div className="flex items-start gap-3">
        <Globe className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden />
        <div className="min-w-0 flex-1">
          <h3 className="font-semibold">Salida a internet</h3>
          <p className="text-[13px] text-muted-foreground">
            A dónde pueden conectarse las apps de este clúster. Siempre llegan a su base de datos, a su caché y a la entrada con identidad.
          </p>
        </div>
      </div>

      <OptionCards<Mode>
        label="Salida a internet"
        value={mode}
        onChange={setMode}
        options={[
          { value: 'internet', title: 'Internet pública', description: 'Las apps llegan a cualquier sitio público. Las redes internas del clúster y de tu empresa siguen cerradas, salvo las que agregues abajo.' },
          { value: 'restricted', title: 'Solo direcciones permitidas', description: 'Las apps solo llegan a las direcciones de la lista. Sin lista, no salen a ningún lado.' },
        ]}
      />

      <div className="space-y-1.5">
        <label htmlFor={`allow-${cluster.id}`} className="text-sm font-medium">
          {mode === 'restricted' ? 'Direcciones permitidas' : 'Redes internas que sí pueden usar'}
        </label>
        <Textarea
          id={`allow-${cluster.id}`}
          value={text}
          onChange={e => setText(e.target.value)}
          rows={4}
          spellCheck={false}
          className="font-mono text-xs"
          placeholder={'203.0.113.0/24\n10.20.0.0/16'}
          aria-invalid={parsed.problems.length > 0}
          aria-describedby={`allow-help-${cluster.id}`}
        />
        <p id={`allow-help-${cluster.id}`} className="text-xs text-muted-foreground">
          Una por línea: una dirección (203.0.113.7) o una red (10.20.0.0/16). Hasta 100.
        </p>
        {parsed.problems.length > 0 && (
          <ul role="alert" className="space-y-0.5 text-xs text-destructive">
            {parsed.problems.map(p => <li key={p.entry}><span className="font-mono">{p.entry}</span>: {p.problem}</li>)}
          </ul>
        )}
        {parsed.entries.length > 100 && <p role="alert" className="text-xs text-destructive">Son más de 100: quita algunas.</p>}
      </div>

      <ErrorNotice error={set.error} />

      <div className="flex flex-wrap items-center gap-3">
        <Button size="sm" onClick={save} disabled={revoked || set.isPending || unchanged || parsed.problems.length > 0 || parsed.entries.length > 100}>
          <Save />
          {set.isPending ? 'Guardando…' : 'Guardar'}
        </Button>
        {!unchanged && <Button size="sm" variant="ghost" onClick={() => { setMode(egress.mode); setText(egress.allow.join('\n')) }}>Descartar cambios</Button>}
      </div>

      {confirming && (
        <AlertDialog open onOpenChange={v => !v && !set.isPending && setConfirming(false)}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>¿Permitir direcciones internas?</AlertDialogTitle>
              <AlertDialogDescription>
                Esta dirección es interna del clúster o de tu red: las apps podrán llegar a ella. Revisa que sea lo que quieres.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <ul className="space-y-0.5 font-mono text-xs">
              {internal.map(i => <li key={i.entry}>{i.entry}</li>)}
            </ul>
            <AlertDialogFooter>
              <AlertDialogCancel type="button">Cancelar</AlertDialogCancel>
              <AlertDialogAction disabled={set.isPending} onClick={send}>
                {set.isPending ? 'Guardando…' : 'Sí, permitirlas'}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}

      <Callout tone={state.tone} title={state.label}>
        {detail(egress, cluster.connected)}
      </Callout>

      {egress.warnings.length > 0 && (
        <Callout tone="warn" title="Revisa estas direcciones">
          <ul className="list-disc space-y-0.5 pl-4">
            {egress.warnings.map(w => <li key={w}>{w}</li>)}
          </ul>
        </Callout>
      )}
    </div>
  )
}
