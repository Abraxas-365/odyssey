import { Callout } from '@/components/odyssey'
import { useAppPage } from '@/features/apps/app-page'
import { AccessPanel } from '@/features/apps/access-panel'

/** App → Compartir: who can open it, as the cluster enforces it and as it will be. */
export function AppSharePage() {
  const { project: p, canRead, canWrite, production, current, publishing } = useAppPage()
  if (!canRead || !production) return <Callout tone="info" title="No tienes acceso a esta app.">Pide a TI el permiso para verla.</Callout>
  return <AccessPanel project={p.id} env={production.id} current={current.data} canWrite={canWrite} busy={publishing} />
}
