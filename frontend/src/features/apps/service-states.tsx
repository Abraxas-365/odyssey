import { Boxes } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { ErrorNotice, Panel, PanelHeader, PanelList, PanelRow } from '@/components/odyssey'
import { useServiceStates } from '@/lib/services'
import type { InventoryRow, Release } from '@/lib/types'
import { isMulti, kindText, stateText, whereText } from './services'

/**
 * Resumen → "Servicios" (MS-6): how each part of the version online stands.
 * Draws nothing for an app of one part (D3: it reads as before) and asks
 * the API nothing then either.
 */
export function ServiceStatesPanel({ project, env, release, technical }: { project: string; env: string; release: Pick<Release, 'services'> | undefined; technical: boolean }) {
  const multi = isMulti(release?.services)
  const states = useServiceStates(project, env, multi)
  if (!multi) return null
  if (states.error) return <ErrorNotice error={states.error} onRetry={() => states.refetch()} />
  const items = states.data?.items ?? release!.services.map(s => ({ name: s.name, kind: s.kind, route: s.route, phase: '' as const, reason: '' as const, message: '', ready_replicas: 0, replicas: s.replicas }))
  return (
    <Panel>
      <PanelHeader icon={Boxes} title="Servicios" description="Las partes de la versión en línea y cómo está cada una." />
      <PanelList aria-label="Estado de cada servicio">
        {items.map(s => {
          const st = stateText(s)
          return (
            <PanelRow key={s.name} className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <span className="font-mono text-[13px] font-medium">{s.name}</span>
              <span className="text-xs text-muted-foreground">{s.kind === 'worker' ? kindText.worker.label : whereText({ ...s, strip_prefix: false })}</span>
              <Badge variant={st.tone} className="ml-auto">{st.label}</Badge>
              {technical && (
                <span className="w-full font-mono text-[11px] text-muted-foreground">
                  {s.phase || 'unknown'}{s.reason ? ` · ${s.reason}` : ''} · {s.ready_replicas}/{s.replicas}{s.message ? ` · ${s.message}` : ''}
                </span>
              )}
            </PanelRow>
          )
        })}
      </PanelList>
    </Panel>
  )
}

/** Inventario: the parts of an app (names only), or nothing for an app of one. */
export function ServiceNames({ services }: { services: InventoryRow['services'] }) {
  if (!isMulti(services)) return null
  return (
    <span className="block text-xs text-muted-foreground" title="Servicios de la versión en línea">
      {services.map(s => s.name).join(' · ')}
    </span>
  )
}
