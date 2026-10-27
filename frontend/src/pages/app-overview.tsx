import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { Archive, Bot, Database, ExternalLink, GitBranch, Lock, Pencil } from 'lucide-react'
import { toast } from 'sonner'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
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
} from '@/components/odyssey'
import { useAuth } from '@/lib/auth'
import { formatDateTime, formatRelative } from '@/lib/format'
import { permissions } from '@/lib/permissions'
import { usePreferences } from '@/lib/preferences'
import { useArchiveProject, useProject, useUpdateProject } from '@/lib/projects'
import type { Project } from '@/lib/types'

/** App → Resumen (UX v3 §8). Versions/resources/sharing land with their APIs. */
export function AppOverviewPage() {
  const { id } = useParams()
  const project = useProject(id)
  if (project.isPending) return <Loading />
  if (project.error) return <ErrorNotice error={project.error} onRetry={() => project.refetch()} />
  return <Overview project={project.data} />
}

function Overview({ project: p }: { project: Project }) {
  const { technical } = usePreferences()
  const { can } = useAuth()
  const archived = p.status === 'archived'
  const canWrite = can(permissions.projectsWrite) && !archived
  const [editing, setEditing] = useState(false)
  const [confirmArchive, setConfirmArchive] = useState(false)
  const archive = useArchiveProject(p.id)

  return (
    <>
      <PageHeader
        breadcrumb={<Link to="/" className="hover:text-foreground">Apps</Link>}
        title={p.name}
        status={<AppStatus status={archived ? 'archived' : 'paused'} label={archived ? undefined : 'Sin publicar'} />}
        description={p.description || 'Sin descripción'}
        actions={
          canWrite && (
            <>
              <Button variant="outline" onClick={() => setEditing(true)}>
                <Pencil />
                Editar
              </Button>
              <Button nativeButton={false} render={<Link to="/asistente" />}>
                <Bot />
                Publicar con mi asistente
              </Button>
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

      <div className="grid gap-6 lg:grid-cols-[1.4fr_1fr]">
        <Panel>
          <div className="p-4">
            <AppThumbnail off className="aspect-[16/8]" />
          </div>
          <PanelFooter>
            <span>Aún no tiene una versión publicada.</span>
            <Button variant="outline" size="sm" disabled title="Disponible cuando la app esté publicada">
              <ExternalLink />
              Abrir app
            </Button>
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
              ]}
            />
          </PanelBody>
        </Panel>
      </div>

      <div className="grid gap-4 md:grid-cols-3">
        <EmptyState icon={GitBranch} title="Versiones">Cada vez que publiques aparecerá aquí, y podrás volver a cualquiera.</EmptyState>
        <EmptyState icon={Database} title="Lo que usa">Base de datos, archivos o IA. Tu asistente los pide cuando la app los necesita.</EmptyState>
        <EmptyState icon={Lock} title="Quién puede abrirla">Por ahora, solo tu equipo.</EmptyState>
      </div>

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
