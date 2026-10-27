import { useState } from 'react'
import { GitBranch, RotateCcw, Upload } from 'lucide-react'
import { toast } from 'sonner'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { EmptyState, ErrorNotice, ListSkeleton, Panel, PanelBody, PanelFooter, PanelHeader, PanelList, ProgressSteps, VersionRow } from '@/components/odyssey'
import { useDeploy, useRollback } from '@/lib/releases'
import type { Deployment } from '@/lib/types'
import { progressSteps, type VersionItem } from './versions'

/** "Publicando una nueva versión" with its three steps (UX v3 §7). */
export function PublishingPanel({ deployment, technical }: { deployment: Deployment; technical: boolean }) {
  const restoring = deployment.reason !== 'deploy'
  return (
    <Panel aria-live="polite">
      <PanelHeader
        title={restoring ? 'Volviendo a una versión anterior' : 'Publicando una nueva versión'}
        description="Tu app sigue funcionando con la versión actual hasta que la nueva esté lista."
        meta={technical ? <span className="font-mono text-xs">rev {deployment.revision}</span> : undefined}
      />
      <ProgressSteps steps={progressSteps(deployment)} technical={technical} />
    </Panel>
  )
}

/** Versiones with "Volver a esta" (UX v3 §8). */
export function VersionsPanel({ items, loading, error, onRetry, technical, project, env, canRestore }: {
  items: VersionItem[]
  loading: boolean
  error: unknown
  onRetry: () => void
  technical: boolean
  project: string
  env: string
  canRestore: boolean
}) {
  const rollback = useRollback(project, env)
  const [target, setTarget] = useState<VersionItem>()
  if (loading) return <Panel><PanelHeader title="Versiones" /><ListSkeleton /></Panel>
  if (error) return <ErrorNotice error={error} onRetry={onRetry} />
  if (items.length === 0) return <EmptyState icon={GitBranch} title="Versiones">Cada vez que publiques aparecerá aquí, y podrás volver a cualquiera.</EmptyState>
  return (
    <Panel>
      <PanelHeader title="Versiones" description="Las que llegaron a estar en línea y las que no se publicaron." />
      <PanelList>
        {items.map(v => (
          <VersionRow key={v.id} version={v} technical={technical} onRestore={canRestore && v.restorable ? () => setTarget(v) : undefined} />
        ))}
      </PanelList>
      <AlertDialog open={!!target} onOpenChange={open => { if (!open) { setTarget(undefined); rollback.reset() } }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>¿Volver a esta versión?</AlertDialogTitle>
            <AlertDialogDescription>
              La publicamos de nuevo y, cuando esté lista, reemplaza a la actual. Si algo falla, la actual sigue en línea.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <ErrorNotice error={rollback.error} />
          <AlertDialogFooter>
            <AlertDialogCancel>Cancelar</AlertDialogCancel>
            <AlertDialogAction
              disabled={rollback.isPending}
              onClick={() => target && rollback.mutate(target.id, { onSuccess: () => { setTarget(undefined); toast.success('Volviendo a esa versión…') } })}
            >
              <RotateCcw />
              {rollback.isPending ? 'Iniciando…' : 'Volver a esta'}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Panel>
  )
}

const digestImage = /^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]+)?(?:\/[a-z0-9]+(?:[._-][a-z0-9]+)*)+@sha256:[a-f0-9]{64}$/

/**
 * Technical mode only, temporary: publishes an already-built image until
 * builds from GitHub exist (phase 1b/1c). Builders publish through their
 * assistant.
 */
export function PublishImagePanel({ project, env, busy }: { project: string; env: string; busy: boolean }) {
  const deploy = useDeploy(project, env)
  const [image, setImage] = useState('')
  const [port, setPort] = useState('8080')
  const [health, setHealth] = useState('/')
  const validImage = digestImage.test(image.trim())
  return (
    <Panel>
      <form
        onSubmit={e => {
          e.preventDefault()
          deploy.mutate(
            { image: image.trim(), port: Number(port), health_path: health.trim() || '/' },
            { onSuccess: () => { toast.success('Publicando…'); setImage('') } },
          )
        }}
      >
        <PanelHeader icon={Upload} title="Publicar imagen" description="Una imagen ya construida, fijada por digest. Temporal hasta que lleguen las publicaciones desde GitHub." />
        <PanelBody>
          <label className="flex flex-col gap-1.5 text-sm font-medium">
            Imagen
            <Input
              required
              className="font-mono text-xs"
              placeholder="ghcr.io/inedge/app@sha256:…"
              value={image}
              onChange={e => setImage(e.target.value)}
              aria-invalid={image !== '' && !validImage}
              aria-describedby="publish-image-help"
            />
            <span id="publish-image-help" className="text-xs font-normal text-muted-foreground">registro/repositorio@sha256:&lt;64 hex&gt;. Las etiquetas como :latest no se aceptan.</span>
          </label>
          <div className="grid gap-4 sm:grid-cols-2">
            <label className="flex flex-col gap-1.5 text-sm font-medium">
              Puerto
              <Input required type="number" min={1} max={65535} value={port} onChange={e => setPort(e.target.value)} />
            </label>
            <label className="flex flex-col gap-1.5 text-sm font-medium">
              Ruta de salud
              <Input className="font-mono text-xs" value={health} onChange={e => setHealth(e.target.value)} />
            </label>
          </div>
          <ErrorNotice error={deploy.error} />
        </PanelBody>
        <PanelFooter className="justify-end">
          <Button type="submit" disabled={deploy.isPending || !validImage || !port}>
            {deploy.isPending ? 'Enviando…' : busy ? 'Publicar (reemplaza la que está en curso)' : 'Publicar'}
          </Button>
        </PanelFooter>
      </form>
    </Panel>
  )
}
