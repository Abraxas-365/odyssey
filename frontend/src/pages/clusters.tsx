import { useState } from 'react'
import { Plus, RefreshCw, Server, Unplug } from 'lucide-react'
import { toast } from 'sonner'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { EmptyState, ErrorNotice, KeyValue, ListSkeleton, PageHeader, Panel, PanelHeader, PanelList, PanelRow, StatusDot, TechnicalDetails, type AppStatusKind } from '@/components/odyssey'
import { useAuth } from '@/lib/auth'
import { clusterState, useClusters, useIssueToken, useRevokeCluster, type ClusterState } from '@/lib/clusters'
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
  const issue = useIssueToken()
  const revoke = useRevokeCluster()
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
                onRevoke={canManage && c.status !== 'revoked' ? () => setRevoking(c) : undefined}
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
              Las apps que ya corren ahí siguen corriendo, pero no podrás publicar versiones nuevas en ellas.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <ErrorNotice error={revoke.error} />
          <AlertDialogFooter>
            <AlertDialogCancel>Cancelar</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={revoke.isPending}
              onClick={() => revoking && revoke.mutate(revoking.id, { onSuccess: c => { setRevoking(undefined); toast.success(`${c.name} quedó revocado`) } })}
            >
              <Unplug />
              {revoke.isPending ? 'Revocando…' : 'Revocar'}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function ClusterRow({ cluster: c, onReconnect, onRevoke, busy }: { cluster: Cluster; onReconnect?: () => void; onRevoke?: () => void; busy?: boolean }) {
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
            {technical && c.agent.version && ` · agente ${c.agent.version}`}
            {technical && caps.kubernetes_version && ` · Kubernetes ${caps.kubernetes_version}`}
          </p>
        </div>
        {(onReconnect || onRevoke) && (
          <div className="ml-auto flex items-center gap-2">
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
