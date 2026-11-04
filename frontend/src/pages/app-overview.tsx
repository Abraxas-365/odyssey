import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { Archive, Bot, Database, ExternalLink, Pencil } from 'lucide-react'
import { toast } from 'sonner'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button, ButtonLink } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import {
  AppStatus,
  AppThumbnail,
  Callout,
  EmptyState,
  ErrorNotice,
  KeyValue,
  Loading,
  PageHeader,
  Panel,
  PanelBody,
  PanelFooter,
  PanelHeader,
  Term,
  TechnicalDetails,
} from '@/components/odyssey'
import { useAuth } from '@/lib/auth'
import { formatDateTime, formatRelative, safeAppUrl } from '@/lib/format'
import { permissions } from '@/lib/permissions'
import { usePreferences } from '@/lib/preferences'
import { useArchiveProject, useProject, useUpdateProject } from '@/lib/projects'
import { useCurrent, useDeployments, useEnvironments } from '@/lib/releases'
import type { Project } from '@/lib/types'
import { CodePanel } from '@/features/github/code-panel'
import { BuildLog, PublishFolderPanel, PublishImagePanel, PublishingPanel, VersionsPanel } from '@/features/apps/release-panels'
import { AccessPanel } from '@/features/apps/access-panel'
import { SecretsPanel, VariablesPanel } from '@/features/apps/variables-panel'
import { appStatus, configReachedApp, failureText, lastProblem, versions } from '@/features/apps/versions'

/** App → Resumen (UX v3 §8): what is online, what is being published, versions. Resources/sharing land with their APIs. */
export function AppOverviewPage() {
  const { id } = useParams()
  const project = useProject(id)
  if (project.isPending) return <Loading />
  if (project.error) return <ErrorNotice error={project.error} onRetry={() => project.refetch()} />
  return <Overview project={project.data} />
}

/** Copies the sentence to say to the assistant, then the link takes them to connect it. */
function copyPublishPrompt(slug: string) {
  navigator.clipboard.writeText(`Publica esta carpeta en la app ${slug} de Odicea`).then(
    () => toast.success('Copiamos lo que debes decirle a tu asistente.'),
    () => toast.error('No se pudo copiar. Dile: “Publica esta carpeta en la app ' + slug + ' de Odicea”.'),
  )
}

function Overview({ project: p }: { project: Project }) {
  const { technical } = usePreferences()
  const { can, user } = useAuth()
  const archived = p.status === 'archived'
  const canWrite = can(permissions.projectsWrite) && !archived
  const canRead = can(permissions.deploymentsRead)
  const [editing, setEditing] = useState(false)
  const [confirmArchive, setConfirmArchive] = useState(false)
  const archive = useArchiveProject(p.id)

  // Phase 1 publishes to production only (there is no notion of another
  // environment in the console yet); staging/previews come with GitHub.
  const environments = useEnvironments(p.id)
  const production = canRead ? environments.data?.items.find(e => e.kind === 'production') : undefined
  const current = useCurrent(p.id, production?.id)
  const publishing = !!current.data?.active
  const deployments = useDeployments(p.id, production?.id, publishing)
  const history = deployments.data?.items ?? []
  const problem = publishing ? undefined : lastProblem(history)
  const status = appStatus(archived, current.data, problem)
  const live = current.data?.release ? current.data : undefined
  const liveUrl = safeAppUrl(live?.url)

  return (
    <>
      <PageHeader
        breadcrumb={<Link to="/" className="hover:text-foreground">Apps</Link>}
        title={p.name}
        status={<AppStatus status={status.status} label={status.label} />}
        description={p.description || 'Sin descripción'}
        actions={
          canWrite && (
            <>
              <Button variant="outline" onClick={() => setEditing(true)}>
                <Pencil />
                Editar
              </Button>
              <ButtonLink render={<Link to="/asistente" />} onClick={() => copyPublishPrompt(p.slug)}>
                <Bot />
                Publicar con mi asistente
              </ButtonLink>
            </>
          )
        }
      />

      {archived && (
        <Callout tone="warn" title="Esta app está archivada.">
          Ya no se puede publicar ni editar. Su historial se conserva.
        </Callout>
      )}

      {editing && <EditPanel project={p} onDone={() => setEditing(false)} />}

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

      <div className="grid gap-6 lg:grid-cols-[1.4fr_1fr]">
        <Panel>
          <div className="p-4">
            <AppThumbnail off={!live} className="aspect-[16/8]" />
          </div>
          <PanelFooter>
            {live ? (
              <span className="min-w-0 truncate">
                En línea {live.deployment?.finished_at && <>desde {formatRelative(live.deployment.finished_at)}</>}
                {technical && <> · <span className="font-mono text-xs">{live.url}</span></>}
              </span>
            ) : (
              <span>Aún no tiene una versión publicada.</span>
            )}
            {live && liveUrl ? (
              <ButtonLink variant="outline" size="sm" href={liveUrl} target="_blank" rel="noreferrer">
                <ExternalLink />
                Abrir app
              </ButtonLink>
            ) : (
              <Button variant="outline" size="sm" disabled title="Disponible cuando la app esté publicada">
                <ExternalLink />
                Abrir app
              </Button>
            )}
          </PanelFooter>
        </Panel>

        <Panel>
          <PanelHeader title="Detalles" />
          <PanelBody>
            <KeyValue
              items={[
                [<Term help="El nombre corto que se usa en la dirección de la app. No cambia.">Dirección</Term>, <span className="font-mono">{p.slug}</span>],
                ['Creada', formatDateTime(p.created_at)],
                ['Último cambio', formatRelative(p.updated_at)],
                ...(technical ? ([['ID', <span className="font-mono text-xs break-all">{p.id}</span>], ['Organización', <span className="font-mono text-xs break-all">{p.organization_id}</span>]] as [React.ReactNode, React.ReactNode][]) : []),
                ...(technical && live?.release ? ([['Imagen', <span className="font-mono text-xs break-all">{live.release.image}</span>], ['Puerto', live.release.config.port], ['Ruta de salud', <span className="font-mono text-xs">{live.release.config.health_path}</span>]] as [React.ReactNode, React.ReactNode][]) : []),
              ]}
            />
          </PanelBody>
        </Panel>
      </div>

      <CodePanel project={p} archived={archived} />

      {production && can(permissions.deploymentsCreate) && !archived && <PublishFolderPanel project={p.id} env={production.id} busy={publishing} technical={technical} />}

      {canRead && production && (
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
      )}
      {environments.error && <ErrorNotice error={environments.error} onRetry={() => environments.refetch()} />}

      {technical && production && can(permissions.deploymentsCreate) && !archived && <PublishImagePanel project={p.id} env={production.id} busy={publishing} />}

      {canRead && production && <AccessPanel project={p.id} env={production.id} current={current.data} canWrite={canWrite} busy={publishing} />}

      {canRead && production && <VariablesPanel project={p.id} env={production.id} canWrite={canWrite} busy={publishing} />}

      {canRead && production && <SecretsPanel project={p.id} env={production.id} canWrite={canWrite} busy={publishing} />}

      <EmptyState icon={Database} title="Lo que usa">Base de datos, archivos o IA. Tu asistente los pide cuando la app los necesita.</EmptyState>

      {canWrite && (
        <Panel className="border-destructive/35">
          <PanelHeader icon={Archive} title="Archivar app" description="La app deja de estar disponible. Su historial se conserva y TI puede consultarlo." actions={<Button variant="destructive" onClick={() => setConfirmArchive(true)}>Archivar</Button>} />
        </Panel>
      )}

      <AlertDialog open={confirmArchive} onOpenChange={setConfirmArchive}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>¿Archivar “{p.name}”?</AlertDialogTitle>
            <AlertDialogDescription>No podrás publicarla ni editarla. Esta acción no se puede deshacer desde aquí.</AlertDialogDescription>
          </AlertDialogHeader>
          <ErrorNotice error={archive.error} />
          <AlertDialogFooter>
            <AlertDialogCancel>Cancelar</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={archive.isPending}
              onClick={() => archive.mutate(undefined, { onSuccess: () => { setConfirmArchive(false); toast.success('App archivada') } })}
            >
              {archive.isPending ? 'Archivando…' : 'Archivar'}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function EditPanel({ project: p, onDone }: { project: Project; onDone: () => void }) {
  const update = useUpdateProject(p.id)
  const [name, setName] = useState(p.name)
  const [description, setDescription] = useState(p.description)
  return (
    <Panel>
      <form
        onSubmit={e => {
          e.preventDefault()
          update.mutate({ name: name.trim(), description: description.trim() }, { onSuccess: () => { toast.success('Cambios guardados'); onDone() } })
        }}
      >
        <PanelHeader title="Editar app" />
        <PanelBody>
          <label className="flex flex-col gap-1.5 text-sm font-medium">
            Nombre
            <Input required maxLength={200} value={name} onChange={e => setName(e.target.value)} />
          </label>
          <label className="flex flex-col gap-1.5 text-sm font-medium">
            Para qué sirve
            <Textarea maxLength={2000} value={description} onChange={e => setDescription(e.target.value)} />
          </label>
          <ErrorNotice error={update.error} />
        </PanelBody>
        <PanelFooter className="justify-end">
          <Button type="button" variant="outline" onClick={onDone}>Cancelar</Button>
          <Button type="submit" disabled={update.isPending || !name.trim()}>{update.isPending ? 'Guardando…' : 'Guardar'}</Button>
        </PanelFooter>
      </form>
    </Panel>
  )
}
