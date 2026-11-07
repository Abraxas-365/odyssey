import { useState } from 'react'
import { Link } from 'react-router-dom'
import { ArrowRight, RotateCcw } from 'lucide-react'
import { Button, ButtonLink } from '@/components/ui/button'
import { Callout, ErrorNotice, Panel, PanelFooter, PanelHeader, PanelList, TechnicalDetails, VersionRow } from '@/components/odyssey'
import { useAuth } from '@/lib/auth'
import { formatRelative } from '@/lib/format'
import { permissions } from '@/lib/permissions'
import { useBuild } from '@/lib/releases'
import { CodePanel } from '@/features/github/code-panel'
import { useAppPage } from '@/features/apps/app-page'
import { FirstVersion } from '@/features/apps/publish-paths'
import { PublishingPanel, BuildLog, RestoreDialog, VersionsPanel } from '@/features/apps/release-panels'
import { ServiceStatesPanel } from '@/features/apps/service-states'
import { UsesCard } from '@/features/apps/uses-card'
import { configReachedApp, failureText, liveVersion, previousVersion, versions, type VersionItem } from '@/features/apps/versions'
import { buildScanText, toneClass as scanTone } from '@/features/inventory/inventory'

/**
 * App → Resumen (UX v4 §5.3): the hero is what is online now, with "Volver a
 * la anterior"; while publishing, the progress comes first and the version
 * online stays visible; a failure is told by its consequence. With nothing
 * online, the three ways to publish. Then how it is published, what it uses
 * and the recent history.
 */
export function AppOverviewPage() {
  const page = useAppPage()
  const { project: p, archived, canRead, canPublish, technical, production, current, live, publishing, deployments, history, problem, connected } = page
  const { can, user } = useAuth()
  const items = versions(history, current.data, user.id)
  const canRestore = can(permissions.deploymentsRollback) && !archived

  if (!canRead) {
    return <Callout tone="info" title="No tienes acceso a las versiones de esta app.">Pide a TI el permiso para verlas.</Callout>
  }
  return (
    <>
      {current.error && <ErrorNotice error={current.error} onRetry={() => current.refetch()} />}
      {current.data?.active && <PublishingPanel deployment={current.data.active} technical={technical} />}
      {problem && (
        <Callout tone={live ? 'warn' : 'bad'} title={problem.reason === 'config' ? (configReachedApp(problem) ? 'El cambio de quién puede abrirla llegó a la app, pero no terminó de aplicarse.' : 'El cambio de quién puede abrirla no se aplicó.') : live ? 'Tu app sigue funcionando con la versión anterior.' : 'La nueva versión no se publicó.'}>
          {failureText(problem)} {problem.reason === 'config' ? (configReachedApp(problem) ? 'Por seguridad la app queda con el nuevo acceso; no se vuelve a abrir sola. Publica una versión nueva para recuperarla.' : 'La app sigue como estaba.') : live ? 'Nadie se vio afectado.' : 'Revisa los cambios y vuelve a intentarlo.'}
          {technical && problem.error?.detail && <span className="mt-1.5 block font-mono text-xs break-words">{problem.error.detail}</span>}
        </Callout>
      )}
      {problem?.build_id && problem.step === 'build' && (
        <Panel>
          <TechnicalDetails title="Ver cómo se construyó" defaultOpen={technical}>
            <BuildLog project={p.id} build={problem.build_id} />
          </TechnicalDetails>
        </Panel>
      )}

      {live && production ? (
        <LiveHero item={liveVersion(current.data, user.id)} previous={previousVersion(items)} publishing={publishing} canRestore={canRestore} />
      ) : (
        !publishing && !connected && current.isSuccess && canPublish && production && (
          <FirstVersion project={p} canConnect={can(permissions.projectsWrite)} onUpload={page.uploadFolder} />
        )
      )}

      {(live || connected || !canPublish) && <CodePanel project={p} archived={archived} />}

      {production && <ServiceStatesPanel project={p.id} env={production.id} release={live?.release ?? undefined} technical={technical} />}

      {production && <UsesCard project={p.id} env={production.id} technical={technical} />}

      {production && (
        <VersionsPanel
          items={items}
          loading={deployments.isPending}
          error={deployments.error}
          onRetry={() => deployments.refetch()}
          technical={technical}
          project={p.id}
          env={production.id}
          canRestore={canRestore}
          recent={3}
          more={items.length > 3 ? 'historial' : undefined}
        />
      )}
    </>
  )
}

/** "En línea ahora": the live version, who published it and through what, its scan, and the way back. */
function LiveHero({ item, previous, publishing, canRestore }: { item: VersionItem | undefined; previous: VersionItem | undefined; publishing: boolean; canRestore: boolean }) {
  const { project: p, production, live, technical } = useAppPage()
  const [target, setTarget] = useState<VersionItem>()
  const buildId = live?.deployment?.build_id
  const build = useBuild(p.id, buildId)
  const scan = buildScanText(buildId ? build.data : null, !!buildId && build.isPending)
  if (!item || !production) return null
  return (
    <Panel className="border-success/40">
      <PanelHeader
        title={publishing ? 'Sigue en línea mientras tanto' : 'En línea ahora'}
        meta={live?.deployment?.finished_at && <span>desde {formatRelative(live.deployment.finished_at)}</span>}
      />
      <PanelList>
        <VersionRow version={item} technical={technical} />
      </PanelList>
      <PanelFooter>
        <span className="flex flex-wrap items-center gap-2">
          {canRestore && previous && !publishing && (
            <Button variant="outline" size="sm" onClick={() => setTarget(previous)}>
              <RotateCcw />
              Volver a la anterior
            </Button>
          )}
          {scan && (
            <span className="text-xs">
              Seguridad: <span className={scanTone[scan.tone]}>{scan.label}</span>
            </span>
          )}
        </span>
        <ButtonLink variant="ghost" size="sm" render={<Link to="historial" />}>
          Ver historial
          <ArrowRight />
        </ButtonLink>
      </PanelFooter>
      <RestoreDialog project={p.id} env={production.id} target={target} onClose={() => setTarget(undefined)} />
    </Panel>
  )
}
