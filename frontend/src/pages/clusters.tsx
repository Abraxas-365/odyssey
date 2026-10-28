import { useState } from 'react'
import { ArrowRightLeft, Plus, RefreshCw, Server, Unplug } from 'lucide-react'
import { toast } from 'sonner'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { EmptyState, ErrorNotice, KeyValue, ListSkeleton, PageHeader, Panel, PanelHeader, PanelList, PanelRow, StatusDot, TechnicalDetails, type AppStatusKind } from '@/components/odyssey'
import { useAuth } from '@/lib/auth'
import { appCount, clusterState, moveTargets, useClusters, useIssueToken, useMoveClusterApps, useRevokeCluster, type ClusterState } from '@/lib/clusters'
import { formatDateTime, formatRelative } from '@/lib/format'
import { permissions } from '@/lib/permissions'
import { usePreferences } from '@/lib/preferences'
import type { Cluster } from '@/lib/types'
import { ConnectClusterDialog, type Issued } from '@/features/clusters/connect-cluster-dialog'

/** One word per state (plan 1a-a3 §Consola), on the app status palette. */
const states: Record<ClusterState, { label: string; dot: AppStatusKind }> = {
  connected: { label: 'Conectado', dot: 'live' },
  offline: { label: 'Sin conexión', dot: 'attention' },
  waiting: { label: 'Esperando al agente', dot: 'building' },
  revoked: { label: 'Revocado', dot: 'archived' },
}

function ClusterStatus({ cluster }: { cluster: Cluster }) {
  const s = states[clusterState(cluster)]
  return (
    <span className="inline-flex items-center gap-1.5 text-[13px] whitespace-nowrap">
      <StatusDot status={s.dot} />
      {s.label}
    </span>
  )
}

/**
 * Infraestructura → Clústeres (TI): where the organization's apps run. Each
 * cluster's agent dials out to Odyssey; connecting one shows its install
 * command once.
 */
export function ClustersPage() {
  const { can } = useAuth()
  const canManage = can(permissions.clustersManage)
  const clusters = useClusters()
  const [connecting, setConnecting] = useState(false)
  const [issued, setIssued] = useState<Issued>()
  const [revoking, setRevoking] = useState<Cluster>()
  const [moving, setMoving] = useState<Cluster>()
  const [moveTo, setMoveTo] = useState('')
  const issue = useIssueToken()
  const revoke = useRevokeCluster()
  const move = useMoveClusterApps()
  const items = clusters.data?.items ?? []
  const live = items.filter(c => c.status !== 'revoked')

  const reconnect = (c: Cluster) =>
    issue.mutate(c.id, {
      onSuccess: token => setIssued({ cluster: c.id, name: c.name, token, reconnect: c.status === 'active', serial: c.certificate.serial }),
      onError: () => toast.error('No se pudo generar un código nuevo. Intenta de nuevo.'),
    })

  return (
    <>
      <PageHeader
        title="Clústeres"
        breadcrumb="Infraestructura"
        description="Donde corren las apps de tu organización. Cada clúster se conecta a Odyssey por su cuenta, sin abrir puertos."
        actions={canManage && live.length > 0 && <Button onClick={() => setConnecting(true)}><Plus />Conectar clúster</Button>}
      />

      <ErrorNotice error={clusters.error} onRetry={() => clusters.refetch()} />

      {clusters.isPending ? (
        <Panel><ListSkeleton /></Panel>
      ) : items.length === 0 && !clusters.error ? (
        <EmptyState
          icon={Server}
          title="Todavía no hay clústeres"
          action={canManage && <Button onClick={() => setConnecting(true)}><Plus />Conectar un clúster</Button>}
        >
          Conecta un clúster de Kubernetes de tu empresa para que las apps se publiquen ahí. {!canManage && 'Pídeselo a quien administra la infraestructura.'}
        </EmptyState>
      ) : items.length > 0 && (
        <Panel>
          <PanelHeader title={`${live.length} ${live.length === 1 ? 'clúster' : 'clústeres'}`} description="El primero que se conecta recibe las apps nuevas." />
          <PanelList aria-label="Clústeres">
            {items.map(c => (
              <ClusterRow
                key={c.id}
                cluster={c}
                onReconnect={canManage && c.status !== 'revoked' ? () => reconnect(c) : undefined}
                onRevoke={canManage && c.status !== 'revoked' ? () => { setMoveTo(''); setRevoking(c) } : undefined}
                onMove={canManage && c.status === 'revoked' && c.apps > 0 ? () => { setMoveTo(''); setMoving(c) } : undefined}
                busy={issue.isPending && issue.variables === c.id}
              />
            ))}
          </PanelList>
        </Panel>
      )}

      <ConnectClusterDialog open={connecting} onOpenChange={setConnecting} />
      <ConnectClusterDialog open={!!issued} onOpenChange={open => { if (!open) { setIssued(undefined); issue.reset() } }} issued={issued} />

      <AlertDialog open={!!revoking} onOpenChange={open => { if (!open) { setRevoking(undefined); revoke.reset() } }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>¿Revocar {revoking?.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              Se desconecta para siempre: su agente deja de recibir cambios y no podrás volver a conectarlo (tendrías que registrarlo de nuevo).
            </AlertDialogDescription>
          </AlertDialogHeader>
          {revoking && revoking.apps > 0 && (
            <AppsChoice cluster={revoking} targets={moveTargets(revoking, items)} value={moveTo} onChange={setMoveTo} revoking />
          )}
          <ErrorNotice error={revoke.error} />
          <AlertDialogFooter>
            <AlertDialogCancel>Cancelar</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={revoke.isPending}
              onClick={() => revoking && revoke.mutate({ id: revoking.id, moveTo: moveTo || undefined }, {
                onSuccess: c => {
                  const to = items.find(t => t.id === moveTo)
                  setRevoking(undefined)
                  toast.success(to ? `${c.name} quedó revocado y sus apps van a ${to.name}` : `${c.name} quedó revocado`)
                },
              })}
            >
              <Unplug />
              {revoke.isPending ? 'Revocando…' : moveTo ? 'Revocar y mover' : 'Revocar'}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={!!moving} onOpenChange={open => { if (!open) { setMoving(undefined); move.reset() } }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Mover las apps de {moving?.name}</AlertDialogTitle>
            <AlertDialogDescription>
              Este clúster está revocado: sus apps siguen corriendo ahí, pero no reciben versiones nuevas.
            </AlertDialogDescription>
          </AlertDialogHeader>
          {moving && <AppsChoice cluster={moving} targets={moveTargets(moving, items)} value={moveTo} onChange={setMoveTo} />}
          <ErrorNotice error={move.error} />
          <AlertDialogFooter>
            <AlertDialogCancel>Cancelar</AlertDialogCancel>
            <AlertDialogAction
              disabled={move.isPending || !moveTo}
              onClick={() => moving && moveTo && move.mutate({ id: moving.id, to: moveTo }, {
                onSuccess: m => {
                  const to = items.find(t => t.id === moveTo)
                  setMoving(undefined)
                  toast.success(m.apps === 0 ? 'No quedaban apps por mover'
                    : `${appCount(m.apps)} ${m.apps === 1 ? 'va' : 'van'} a ${to?.name ?? 'el otro clúster'}`)
                },
              })}
            >
              <ArrowRightLeft />
              {move.isPending ? 'Moviendo…' : 'Mover'}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

/**
 * What happens to the apps of a cluster: TI decides (nothing moves on its
 * own: the other cluster may be on another network or far from the data).
 */
function AppsChoice({ cluster, targets, value, onChange, revoking }: {
  cluster: Cluster; targets: Cluster[]; value: string; onChange: (id: string) => void; revoking?: boolean
}) {
  const n = appCount(cluster.apps)
  const one = cluster.apps === 1
  if (targets.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        {n} {one ? 'corre ahí y seguirá' : 'corren ahí y seguirán'} corriendo, sin versiones nuevas.
        {one ? ' Para moverla' : ' Para moverlas'}, conecta otro clúster{revoking ? (one ? ' y muévela después desde esta lista' : ' y muévelas después desde esta lista') : ''}.
      </p>
    )
  }
  const items: Record<string, string> = Object.fromEntries(targets.map(t => [t.id, `Mover a ${t.name}`]))
  if (revoking) items[''] = one ? 'Dejarla donde está' : 'Dejarlas donde están'
  return (
    <div className="space-y-2">
      <p className="text-sm">
        <span className="font-semibold">{n}</span> {one ? 'corre ahí. Al moverla, el otro clúster la publica' : 'corren ahí. Al moverlas, el otro clúster las publica'} desde cero;
        revisa que llegue a los mismos datos y redes.
      </p>
      <Select value={value} onValueChange={v => onChange((v as string | null) ?? '')} items={items}>
        <SelectTrigger className="w-full" aria-label="Qué hacer con las apps"><SelectValue placeholder="Elige a dónde moverlas" /></SelectTrigger>
        <SelectContent>
          {revoking && <SelectItem value="">{items['']}</SelectItem>}
          {targets.map(t => <SelectItem key={t.id} value={t.id}>Mover a {t.name}</SelectItem>)}
        </SelectContent>
      </Select>
    </div>
  )
}

function ClusterRow({ cluster: c, onReconnect, onRevoke, onMove, busy }: {
  cluster: Cluster; onReconnect?: () => void; onRevoke?: () => void; onMove?: () => void; busy?: boolean
}) {
  const { technical } = usePreferences()
  const state = clusterState(c)
  const seen = c.last_seen_at ? `Última conexión ${formatRelative(c.last_seen_at)}` : 'Nunca se conectó'
  const caps = c.agent.capabilities
  return (
    <PanelRow className="flex-col items-stretch gap-0 p-0">
      <div className="flex flex-wrap items-center gap-3.5 px-5 py-3.5">
        <Server className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <div className="min-w-48 flex-1">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <span className="font-semibold">{c.name}</span>
            <ClusterStatus cluster={c} />
          </div>
          <p className="text-xs text-muted-foreground">
            {state === 'revoked' && c.revoked_at ? `Revocado ${formatRelative(c.revoked_at)}`
              : state === 'waiting' ? (c.token_expires_at ? `El código vence ${formatRelative(c.token_expires_at)}` : 'Sin código vigente: genera uno nuevo')
              : seen}
            {c.apps > 0 && ` · ${appCount(c.apps)}${state === 'revoked' ? ' sin versiones nuevas' : ''}`}
            {technical && c.agent.version && ` · agente ${c.agent.version}`}
            {technical && caps.kubernetes_version && ` · Kubernetes ${caps.kubernetes_version}`}
          </p>
        </div>
        {(onReconnect || onRevoke || onMove) && (
          <div className="ml-auto flex items-center gap-2">
            {onMove && (
              <Button variant="outline" size="sm" onClick={onMove}>
                <ArrowRightLeft />
                Mover apps
              </Button>
            )}
            {onReconnect && (
              <Button variant="outline" size="sm" disabled={busy} onClick={onReconnect}>
                <RefreshCw />
                {state === 'waiting' ? 'Nuevo código' : 'Reconectar'}
              </Button>
            )}
            {onRevoke && <Button variant="ghost" size="sm" onClick={onRevoke}>Revocar</Button>}
          </div>
        )}
      </div>
      {technical && c.enrolled_at && (
        <div className="border-t">
          <TechnicalDetails title="Detalles técnicos">
            <div className="px-5 py-4">
              <KeyValue
                items={[
                  ['ID', <span className="font-mono text-xs">{c.id}</span>],
                  ['Kubernetes', caps.kubernetes_version || '—'],
                  ['APIs', caps.apis.length ? caps.apis.join(', ') : '—'],
                  ['Ingress classes', caps.ingress_classes.join(', ') || '—'],
                  ['Storage classes', caps.storage_classes.join(', ') || '—'],
                  ['Certificado', c.certificate.serial ? <span className="font-mono text-xs break-all">{c.certificate.serial}</span> : '—'],
                  ['Vence', c.certificate.expires_at ? formatDateTime(c.certificate.expires_at) : '—'],
                  ['Conectado desde', c.enrolled_at ? formatDateTime(c.enrolled_at) : '—'],
                ]}
              />
            </div>
          </TechnicalDetails>
        </div>
      )}
    </PanelRow>
  )
}
