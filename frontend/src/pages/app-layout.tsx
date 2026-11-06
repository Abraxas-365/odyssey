import { useState } from 'react'
import { Link, Outlet, useParams } from 'react-router-dom'
import { Bot, FolderUp, GitBranch, Server } from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { AppStatus, AppUrl, Callout, ErrorNotice, Loading, PageHeader } from '@/components/odyssey'
import { useAuth } from '@/lib/auth'
import { safeAppUrl } from '@/lib/format'
import { usePublishNow, usePushes, useProjectRepository } from '@/lib/github'
import { useAppStates } from '@/lib/inventory'
import { permissions } from '@/lib/permissions'
import { usePreferences } from '@/lib/preferences'
import { useProject } from '@/lib/projects'
import { useCurrent, useDeployments, useEnvironments } from '@/lib/releases'
import type { Project } from '@/lib/types'
import type { AppPage } from '@/features/apps/app-page'
import { assistantPrompt, hasUnpublishedCommit, primaryAction } from '@/features/apps/overview'
import { AssistantDialog } from '@/features/apps/publish-paths'
import { UploadFolderDialog } from '@/features/apps/release-panels'
import { appStatus, lastProblem } from '@/features/apps/versions'

/**
 * An app (UX v4 §5.1/§5.3): its name, state, address and where it runs, the
 * one primary action its state calls for, then the tab (Resumen · Historial
 * · Compartir · Configuración) through the outlet, which shares what is read
 * here (features/apps/app-page.ts).
 */
export function AppLayout() {
  const { id } = useParams()
  const project = useProject(id)
  if (project.isPending) return <Loading />
  if (project.error) return <ErrorNotice error={project.error} onRetry={() => project.refetch()} />
  return <AppFrame project={project.data} />
}

function AppFrame({ project: p }: { project: Project }) {
  const { technical } = usePreferences()
  const { can } = useAuth()
  const archived = p.status === 'archived'
  const canRead = can(permissions.deploymentsRead)
  const canWrite = can(permissions.projectsWrite) && !archived
  const canPublish = can(permissions.deploymentsCreate) && !archived

  const environments = useEnvironments(p.id)
  const production = canRead ? environments.data?.items.find(e => e.kind === 'production') : undefined
  const current = useCurrent(p.id, production?.id)
  const publishing = !!current.data?.active
  const deployments = useDeployments(p.id, production?.id, publishing)
  const history = deployments.data?.items ?? []
  const problem = publishing ? undefined : lastProblem(history)
  const status = appStatus(archived, current.data, problem)
  const live = current.data?.release ? current.data : undefined
  const url = safeAppUrl(live?.url)
  // "Corre en ‹clúster›": the same read the app list makes
  const state = useAppStates([p.id], canRead && can(permissions.projectsRead)).data?.items.find(s => s.app_id === p.id)

  const repository = useProjectRepository(p.id, canRead)
  const connected = !!repository.data
  const pushes = usePushes(p.id, connected)
  const publishNow = usePublishNow(p.id)
  const unpublished = connected && !publishNow.isPending && hasUnpublishedCommit(pushes.data?.items[0], current.data)
  const action = primaryAction({ archived, canPublish: canPublish && !!production, publishing, live: !!live, unpublishedCommit: unpublished })

  const [upload, setUpload] = useState(false)
  const [ask, setAsk] = useState(false)
  const prompt = assistantPrompt(p.slug, { live: !!live, github: connected })

  const page: AppPage = {
    project: p, archived, canWrite, canRead, canPublish, technical, production,
    current, live, publishing, deployments, history, problem, connected,
    uploadFolder: () => setUpload(true),
  }

  return (
    <>
      <PageHeader
        breadcrumb={<Link to="/" className="hover:text-foreground">Apps</Link>}
        title={p.name}
        status={<AppStatus status={status.status} label={status.label} />}
        description={
          <span className="flex flex-wrap items-center gap-x-4 gap-y-1">
            {url ? <AppUrl url={url} className="text-foreground" /> : <span>{p.description || 'Aún no tiene una versión publicada.'}</span>}
            {state?.cluster && (
              <span className="inline-flex items-center gap-1.5 text-[13px]">
                <Server className="size-3.5" aria-hidden />
                Corre en <span className="font-medium text-foreground">{state.cluster.name}</span>
              </span>
            )}
          </span>
        }
        actions={
          <>
            {(action === 'assistant' || action === 'github') && (
              <Button variant="outline" onClick={() => setUpload(true)}>
                <FolderUp />
                Subir una carpeta
              </Button>
            )}
            {action === 'github' && (
              <Button disabled={publishNow.isPending} onClick={() => publishNow.mutate(undefined, { onSuccess: () => toast.success('Publicando el último cambio…') })}>
                {publishNow.isPending ? <Spinner /> : <GitBranch />}
                Publicar ahora desde GitHub
              </Button>
            )}
            {action === 'assistant' && (
              <Button onClick={() => setAsk(true)}>
                <Bot />
                Pedir a mi asistente
              </Button>
            )}
          </>
        }
      />
      <ErrorNotice error={publishNow.error} />

      {archived && (
        <Callout tone="warn" title="Esta app está archivada.">
          Ya no se puede publicar ni editar. Su historial se conserva.
        </Callout>
      )}
      {environments.error && <ErrorNotice error={environments.error} onRetry={() => environments.refetch()} />}

      <Outlet context={page} />

      {production && canPublish && (
        <UploadFolderDialog project={p.id} env={production.id} open={upload} onOpenChange={setUpload} busy={publishing} technical={technical} first={!live} />
      )}
      <AssistantDialog open={ask} onOpenChange={setAsk} prompt={prompt} />
    </>
  )
}
