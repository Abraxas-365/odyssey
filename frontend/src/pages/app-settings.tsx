import { useState, type ReactNode } from 'react'
import { Archive, Pencil } from 'lucide-react'
import { toast } from 'sonner'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { ErrorNotice, KeyValue, Panel, PanelBody, PanelFooter, PanelHeader, Term } from '@/components/odyssey'
import { useAuth } from '@/lib/auth'
import { formatDateTime, formatRelative } from '@/lib/format'
import { permissions } from '@/lib/permissions'
import { useArchiveProject, useUpdateProject } from '@/lib/projects'
import type { Project } from '@/lib/types'
import { useAppPage } from '@/features/apps/app-page'
import { ResourcesPanel } from '@/features/apps/resources-panel'
import { ServicesPanel } from '@/features/apps/services-panel'
import { TierField } from '@/features/apps/tier-field'
import { SecretsPanel, VariablesPanel } from '@/features/apps/variables-panel'

type Row = [ReactNode, ReactNode]

/**
 * App → Configuración: its details (name, address, tier), variables, secrets,
 * storage, and archiving at the end, away from the everyday actions.
 */
export function AppSettingsPage() {
  const { project: p, archived, canRead, canWrite, technical, production, live, publishing } = useAppPage()
  const { can } = useAuth()
  const [editing, setEditing] = useState(false)

  return (
    <>
      {editing ? (
        <EditPanel project={p} onDone={() => setEditing(false)} />
      ) : (
        <Panel>
          <PanelHeader
            title="Detalles"
            actions={canWrite && <Button variant="outline" size="sm" onClick={() => setEditing(true)}><Pencil />Editar</Button>}
          />
          <PanelBody>
            <KeyValue
              items={[
                ['Nombre', p.name],
                ['Para qué sirve', p.description || <span className="text-muted-foreground">Sin descripción</span>],
                [<Term help="El nombre corto que se usa en la dirección de la app. No cambia.">Dirección</Term>, <span className="font-mono">{p.slug}</span>],
                [<Term help="Qué tan importante es la app. Las reglas de TI se apoyan en él: por ejemplo, publicar en producción pide aprobación. Solo TI lo cambia.">Nivel</Term>, <TierField project={p} canChange={can(permissions.projectsTier)} />],
                ['Creada', formatDateTime(p.created_at)],
                ['Último cambio', formatRelative(p.updated_at)],
                ...(technical ? ([['ID', <span className="font-mono text-xs break-all">{p.id}</span>], ['Organización', <span className="font-mono text-xs break-all">{p.organization_id}</span>]] as Row[]) : []),
                ...(technical && live?.release ? ([['Imagen', <span className="font-mono text-xs break-all">{live.release.image}</span>], ['Puerto', live.release.config.port], ['Ruta de salud', <span className="font-mono text-xs">{live.release.config.health_path}</span>]] as Row[]) : []),
              ]}
            />
          </PanelBody>
        </Panel>
      )}

      <ServicesPanel project={p.id} canWrite={canWrite} technical={technical} />
      {canRead && production && <VariablesPanel project={p.id} env={production.id} canWrite={canWrite} busy={publishing} />}
      {canRead && production && <SecretsPanel project={p.id} env={production.id} canWrite={canWrite} busy={publishing} />}
      {canRead && production && <ResourcesPanel project={p.id} env={production.id} canWrite={canWrite} busy={publishing} technical={technical} />}

      {canWrite && !archived && <ArchivePanel project={p} />}
    </>
  )
}

function ArchivePanel({ project: p }: { project: Project }) {
  const archive = useArchiveProject(p.id)
  const [confirm, setConfirm] = useState(false)
  return (
    <Panel className="border-destructive/35">
      <PanelHeader icon={Archive} title="Archivar app" description="La app deja de estar disponible. Su historial se conserva y TI puede consultarlo." actions={<Button variant="destructive" onClick={() => setConfirm(true)}>Archivar</Button>} />
      <AlertDialog open={confirm} onOpenChange={setConfirm}>
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
              onClick={() => archive.mutate(undefined, { onSuccess: () => { setConfirm(false); toast.success('App archivada') } })}
            >
              {archive.isPending ? 'Archivando…' : 'Archivar'}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Panel>
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
