import { Link } from 'react-router-dom'
import { Callout } from '@/components/odyssey'
import { useAuth } from '@/lib/auth'
import { permissions } from '@/lib/permissions'
import { useAppPage } from '@/features/apps/app-page'
import { PublishImagePanel, VersionsPanel } from '@/features/apps/release-panels'
import { versions } from '@/features/apps/versions'

/**
 * App → Historial (UX v4 §5.4): from the deployments, not the audit log:
 * versions, restores and automatic rollbacks, each with its channel. TI sees
 * the company's whole record elsewhere.
 */
export function AppHistoryPage() {
  const { project: p, archived, canRead, canPublish, technical, production, current, deployments, history, publishing } = useAppPage()
  const { can, user } = useAuth()
  if (!canRead || !production) return <Callout tone="info" title="No tienes acceso a las versiones de esta app.">Pide a TI el permiso para verlas.</Callout>
  return (
    <>
      <VersionsPanel
        items={versions(history, current.data, user.id)}
        loading={deployments.isPending}
        error={deployments.error}
        onRetry={() => deployments.refetch()}
        technical={technical}
        project={p.id}
        env={production.id}
        canRestore={can(permissions.deploymentsRollback) && !archived}
      />
      {can(permissions.auditRead) ? (
        <p className="text-xs text-muted-foreground">
          Todo lo que se hizo con esta app — quién, por dónde y cuándo — está en el{' '}
          <Link to={`/registro?app=${encodeURIComponent(p.id)}`} className="text-foreground underline-offset-4 hover:underline">Registro</Link>.
        </p>
      ) : (
        <p className="text-xs text-muted-foreground">TI ve el registro completo de la empresa: quién hizo cada cambio, por dónde y cuándo.</p>
      )}
      {technical && canPublish && <PublishImagePanel project={p.id} env={production.id} busy={publishing} />}
    </>
  )
}
