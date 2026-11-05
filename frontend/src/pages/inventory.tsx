import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Download, Server } from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Callout, ChipGroup, EmptyState, ErrorNotice, ListSkeleton, PageHeader, Panel, PanelFooter, StatusDot, type AppStatusKind, type Tone } from '@/components/odyssey'
import { errorMessage } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import { useClusters } from '@/lib/clusters'
import { accessLabel } from '@/features/apps/access'
import { tierLabel } from '@/features/policy/rules'
import { downloadInventoryCsv, useInventory } from '@/lib/inventory'
import { formatRelative } from '@/lib/format'
import { permissions } from '@/lib/permissions'
import { usePreferences } from '@/lib/preferences'
import type { InventoryFilter, InventoryRow, ProjectTier } from '@/lib/types'
import { ownerText, rowUnavailable, scanText, statusText, toneClass } from '@/features/inventory/inventory'
import { cn } from '@/lib/utils'

const PAGE = 25
const ALL = 'all'

const dot: Record<Tone, AppStatusKind> = { ok: 'live', warn: 'attention', bad: 'attention', info: 'building', ai: 'building', neutral: 'archived' }

/**
 * Inventario (TI): everything the organization runs, one row per app and
 * environment, with the last image scan. Filters narrow it; "Descargar CSV"
 * exports every row that matches (not just the page on screen).
 */
export function InventoryPage() {
  const { can, user } = useAuth()
  const allowed = can(permissions.inventoryRead)
  const { technical } = usePreferences()
  const [tier, setTier] = useState<ProjectTier | typeof ALL>(ALL)
  const [cluster, setCluster] = useState(ALL)
  const [needs, setNeeds] = useState<'all' | 'needs'>('all')
  const [search, setSearch] = useState('')
  const [offset, setOffset] = useState(0)
  const [exporting, setExporting] = useState(false)
  // The cluster filter is only offered to who may read clusters (anyone else would get a 403).
  const clusters = useClusters(undefined, allowed && can(permissions.clustersRead))

  const filter: InventoryFilter = {
    tier: tier === ALL ? undefined : [tier],
    cluster: cluster === ALL ? undefined : cluster,
    needs_attention: needs === 'needs' ? true : undefined,
    search: search.trim() || undefined,
    limit: PAGE,
    offset,
  }
  // Anyone without the permission would get a 403: do not ask.
  const inventory = useInventory(filter, allowed)
  const narrow = <T,>(set: (v: T) => void) => (v: T) => { set(v); setOffset(0) }

  if (!allowed) {
    return (
      <>
        <PageHeader title="Inventario" />
        <EmptyState icon={Server} title="Solo TI puede ver el inventario">Pídele a TI que te comparta lo que necesites.</EmptyState>
      </>
    )
  }

  const rows = inventory.data?.items ?? []
  const total = inventory.data?.page.total ?? 0
  const download = async () => {
    setExporting(true)
    try {
      const { truncated, clustersUnavailable } = await downloadInventoryCsv(filter)
      if (clustersUnavailable) toast.warning('Descargamos el archivo, pero no pudimos leer los clústeres ahora: el filtro por clúster no se pudo aplicar, así que está incompleto.')
      else toast.success(truncated ? 'Descargamos el archivo, pero la organización tiene más apps de las que caben: filtra para ver el resto.' : 'Descargamos el inventario.')
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setExporting(false)
    }
  }
  const clusterItems = Object.fromEntries([[ALL, 'Todos los clústeres'], ...(clusters.data?.items ?? []).map(c => [c.id, c.name])])

  return (
    <>
      <PageHeader
        title="Inventario"
        description={inventory.data ? `${total} ${total === 1 ? 'entorno' : 'entornos'} en ${inventory.data.page.total === 0 ? 'ninguna app' : 'tus apps'}` : ' '}
        actions={
          <Button variant="outline" onClick={download} disabled={exporting}>
            <Download />
            {exporting ? 'Preparando…' : 'Descargar CSV'}
          </Button>
        }
      />

      <div className="flex flex-wrap items-center gap-2">
        <Input type="search" aria-label="Buscar apps" placeholder="Buscar por nombre…" value={search} onChange={e => narrow(setSearch)(e.target.value)} className="w-56" />
        <ChipGroup
          label="Nivel"
          value={tier}
          onChange={narrow(setTier)}
          options={[{ value: ALL, label: 'Todos' }, { value: 'production', label: tierLabel('production') }, { value: 'internal', label: tierLabel('internal') }, { value: 'sandbox', label: tierLabel('sandbox') }]}
        />
        {can(permissions.clustersRead) && (
          <Select value={cluster} items={clusterItems} onValueChange={v => narrow(setCluster)(v ?? ALL)}>
            <SelectTrigger size="sm" className="w-52" aria-label="Clúster"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>Todos los clústeres</SelectItem>
              {(clusters.data?.items ?? []).map(c => <SelectItem key={c.id} value={c.id}>{c.name}</SelectItem>)}
            </SelectContent>
          </Select>
        )}
        <ChipGroup label="Atención" value={needs} onChange={narrow(setNeeds)} options={[{ value: 'all', label: 'Todas' }, { value: 'needs', label: 'Necesitan atención' }]} />
      </div>

      <ErrorNotice error={inventory.error} onRetry={() => inventory.refetch()} />
      {inventory.data?.unavailable.includes('clusters') && (
        <Callout tone="warn" title="No pudimos leer los clústeres ahora.">
          La lista de este clúster puede estar incompleta: que no aparezca nada no quiere decir que no haya apps allí. Vuelve a intentarlo en un momento.
        </Callout>
      )}
      {inventory.data?.truncated && (
        <Callout tone="warn" title="Hay más apps de las que se revisan a la vez.">
          Usa los filtros para acotar la lista; el CSV tiene la misma regla.
        </Callout>
      )}

      {inventory.isPending && inventory.fetchStatus !== 'idle' ? (
        <Panel aria-busy><ListSkeleton rows={4} /></Panel>
      ) : rows.length === 0 && !inventory.error ? (
        inventory.data?.unavailable.includes('clusters')
          ? <EmptyState icon={Server} title="No se pudo comprobar este clúster">Los clústeres no respondieron; no sabemos qué apps corren allí.</EmptyState>
          : <EmptyState icon={Server} title="No hay entornos que coincidan">Prueba con otros filtros.</EmptyState>
      ) : (
        <Panel>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>App</TableHead>
                <TableHead>Nivel</TableHead>
                <TableHead>Estado</TableHead>
                <TableHead>Quién</TableHead>
                <TableHead>Escaneo</TableHead>
                <TableHead>Clúster</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map(r => <Row key={r.environment.id} row={r} me={user.id} technical={technical} />)}
            </TableBody>
          </Table>
          <PanelFooter>
            <span>{total === 0 ? '' : `${offset + 1}–${offset + rows.length} de ${total}`}</span>
            <span className="flex gap-2">
              <Button variant="outline" size="sm" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>Anterior</Button>
              <Button variant="outline" size="sm" disabled={offset + PAGE >= total} onClick={() => setOffset(offset + PAGE)}>Siguiente</Button>
            </span>
          </PanelFooter>
        </Panel>
      )}
    </>
  )
}

function Row({ row: r, me, technical }: { row: InventoryRow; me: string; technical: boolean }) {
  const status = statusText[r.status]
  const scan = scanText(r.scan)
  const missing = rowUnavailable(r)
  return (
    <TableRow>
      <TableCell>
        <Link to={`/apps/${r.app.id}`} className="font-medium hover:underline">{r.app.name}</Link>
        <div className="text-xs text-muted-foreground">
          {r.environment.kind === 'production' ? 'Producción' : r.environment.slug}
          {r.access && <> · {accessLabel(r.access)}</>}
          {technical && <span className="font-mono"> · {r.app.slug}</span>}
        </div>
        {r.needs_attention && <div className="mt-1 text-xs font-medium text-warning">Necesita atención</div>}
        {missing && <div className="mt-1 text-xs text-muted-foreground">No pudimos leer {missing} ahora.</div>}
      </TableCell>
      <TableCell>{tierLabel(r.app.tier)}</TableCell>
      <TableCell>
        <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
          <StatusDot status={dot[status.tone]} />
          {status.label}
        </span>
        {r.online?.since && <div className="text-xs text-muted-foreground">desde {formatRelative(r.online.since)}</div>}
      </TableCell>
      <TableCell>{ownerText(r.owner, me)}</TableCell>
      <TableCell>
        <span className={cn('whitespace-nowrap', toneClass[scan.tone])}>{scan.label}</span>
        {scan.detail && <div className="max-w-[220px] text-xs text-muted-foreground">{scan.detail}</div>}
        {r.scan.scanned_at && <div className="text-xs text-muted-foreground">{formatRelative(r.scan.scanned_at)}</div>}
      </TableCell>
      <TableCell>{r.cluster?.name ?? <span className="text-muted-foreground">—</span>}</TableCell>
    </TableRow>
  )
}
