import { useEffect, useRef, useState, type DragEvent } from 'react'
import { Link } from 'react-router-dom'
import { ArrowRight, FolderUp, GitBranch, RotateCcw, ShieldAlert, Upload } from 'lucide-react'
import { toast } from 'sonner'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button, ButtonLink } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { Callout, EmptyState, ErrorNotice, ListSkeleton, LogViewer, Panel, PanelBody, PanelFooter, PanelHeader, PanelList, ProgressSteps, TechnicalDetails, VersionRow, type LogSegment, type LogTone } from '@/components/odyssey'
import { dropEntries, readDrop } from '@/lib/drop'
import { formatBytes } from '@/lib/format'
import { useBuildLog, useDeploy, useDeploySource, useRollback } from '@/lib/releases'
import { packSource, planProblem, planSource, SOURCE_LIMITS, type SourcePlan } from '@/lib/tarball'
import type { BuildLogLine, Deployment } from '@/lib/types'
import { progressSteps, waitingForIT, waitingLabel, type VersionItem } from './versions'

/** "Publicando una nueva versión" with its steps (UX v3 §7); the build's log stays one click away. */
export function PublishingPanel({ deployment, technical }: { deployment: Deployment; technical: boolean }) {
  const restoring = deployment.reason !== 'deploy'
  const building = deployment.status === 'building'
  if (waitingForIT(deployment)) {
    return (
      <Panel>
        <PanelHeader
          title={waitingLabel}
          description="Las reglas de TI piden aprobar esta publicación antes de empezar. Tu app sigue funcionando con la versión actual y te avisamos cuando lo decidan."
          meta={technical ? <span className="font-mono text-xs">rev {deployment.revision}</span> : undefined}
        />
        <PanelBody>
          <Callout tone="info" title="Ya le pedimos a TI que lo apruebe.">
            Puedes seguir la solicitud, o cancelarla, en «Mis solicitudes».
          </Callout>
        </PanelBody>
      </Panel>
    )
  }
  return (
    <Panel>
      <PanelHeader
        title={restoring ? 'Volviendo a una versión anterior' : 'Publicando una nueva versión'}
        description="Tu app sigue funcionando con la versión actual hasta que la nueva esté lista."
        meta={technical ? <span className="font-mono text-xs">rev {deployment.revision}</span> : undefined}
      />
      <div aria-live="polite">
        <ProgressSteps steps={progressSteps(deployment)} technical={technical} />
      </div>
      {deployment.build_id && (
        <TechnicalDetails title={building ? 'Ver cómo se construye' : 'Ver cómo se construyó'} defaultOpen={technical}>
          <BuildLog project={deployment.project_id} build={deployment.build_id} />
        </TechnicalDetails>
      )}
    </Panel>
  )
}

const streamTone: Record<BuildLogLine['stream'], LogTone> = { prepare: 'key', build: 'plain', system: 'warn' }

/** The build's log, followed while it runs. */
export function BuildLog({ project, build }: { project: string; build: string }) {
  const log = useBuildLog(project, build)
  const box = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = box.current
    if (el) el.scrollTop = el.scrollHeight
  }, [log.lines.length])
  if (log.error && log.lines.length === 0) return <div className="p-4"><ErrorNotice error={log.error} /></div>
  const lines: LogSegment[][] = log.lines.length
    ? log.lines.map(l => [{ text: l.text, tone: streamTone[l.stream] }])
    : [[{ text: log.finished ? 'El registro está vacío.' : 'Esperando las primeras líneas…', tone: 'time' }]]
  return (
    <div ref={box} className="max-h-80 overflow-auto bg-inset">
      <LogViewer label="Registro de la construcción" lines={lines} className="min-w-fit overflow-visible" />
    </div>
  )
}

/** Versiones with "Volver a esta" (UX v3 §8); the Resumen shows the newest few ("Historial reciente") and links to the rest. */
export function VersionsPanel({ items, loading, error, onRetry, technical, project, env, canRestore, recent, more }: {
  items: VersionItem[]
  loading: boolean
  error: unknown
  onRetry: () => void
  technical: boolean
  project: string
  env: string
  canRestore: boolean
  /** Show only this many, with a link to `more`. */
  recent?: number
  more?: string
}) {
  const [target, setTarget] = useState<VersionItem>()
  const title = recent ? 'Historial reciente' : 'Historial'
  if (loading) return <Panel><PanelHeader title={title} /><ListSkeleton /></Panel>
  if (error) return <ErrorNotice error={error} onRetry={onRetry} />
  if (items.length === 0) return <EmptyState icon={GitBranch} title="Historial">Cada vez que publiques aparecerá aquí, y podrás volver a cualquiera.</EmptyState>
  const shown = recent ? items.slice(0, recent) : items
  return (
    <Panel>
      <PanelHeader
        title={recent ? `${title} (${shown.length})` : title}
        description={recent ? undefined : 'Las versiones que llegaron a estar en línea y las que no se publicaron.'}
        actions={more && <ButtonLink variant="ghost" size="sm" render={<Link to={more} />}>Ver todo<ArrowRight /></ButtonLink>}
      />
      <PanelList>
        {shown.map(v => (
          <VersionRow key={v.id} version={v} technical={technical} onRestore={canRestore && v.restorable ? () => setTarget(v) : undefined} />
        ))}
      </PanelList>
      <RestoreDialog project={project} env={env} target={target} onClose={() => setTarget(undefined)} />
    </Panel>
  )
}

/** "¿Volver a esta versión?": the confirmation before a rollback. */
export function RestoreDialog({ project, env, target, onClose }: { project: string; env: string; target: VersionItem | undefined; onClose: () => void }) {
  const rollback = useRollback(project, env)
  return (
    <AlertDialog open={!!target} onOpenChange={open => { if (!open) { onClose(); rollback.reset() } }}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>¿Volver a “{target?.message}”?</AlertDialogTitle>
          <AlertDialogDescription>
            La publicamos de nuevo y, cuando esté lista, reemplaza a la actual. Si algo falla, la actual sigue en línea.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <ErrorNotice error={rollback.error} />
        <AlertDialogFooter>
          <AlertDialogCancel>Cancelar</AlertDialogCancel>
          <AlertDialogAction
            disabled={rollback.isPending}
            onClick={() => target && rollback.mutate(target.id, { onSuccess: () => { onClose(); toast.success('Volviendo a esa versión…') } })}
          >
            <RotateCcw />
            {rollback.isPending ? 'Iniciando…' : 'Volver a esta'}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

/** Prefills the (technical) port field with the API's default, 8080: the agent sets $PORT for the app. */
const BUILD_PORT = '8080'

/**
 * "Subir una carpeta" (UX v4 §5.3): a dialog where the app's folder is
 * dropped or picked, with what goes up (and that .env files don't), and an
 * optional "Qué cambió" that names the version. The browser packs it; the
 * cluster builds it. Port and health path are technical details.
 */
export function UploadFolderDialog({ project, env, open, onOpenChange, busy, technical, first }: {
  project: string
  env: string
  open: boolean
  onOpenChange: (open: boolean) => void
  busy: boolean
  technical: boolean
  /** Nothing online yet: there is nothing to say "what changed" about. */
  first: boolean
}) {
  const deploy = useDeploySource(project, env)
  const input = useRef<HTMLInputElement>(null)
  const [plan, setPlan] = useState<SourcePlan>()
  const [packing, setPacking] = useState(false)
  const [reading, setReading] = useState(false)
  const [over, setOver] = useState(false)
  const [packError, setPackError] = useState<string>()
  const [message, setMessage] = useState('')
  const [port, setPort] = useState(BUILD_PORT)
  const [health, setHealth] = useState('/')
  const problem = plan && planProblem(plan)
  const sending = packing || deploy.isPending

  const reset = () => { setPlan(undefined); setPackError(undefined); setMessage(''); setOver(false); deploy.reset(); if (input.current) input.current.value = '' }
  const close = (next: boolean) => { if (sending) return; onOpenChange(next); if (!next) reset() }

  const pick = (files: File[]) => {
    deploy.reset()
    setPackError(undefined)
    setPlan(files.length ? planSource(files) : undefined)
  }
  const drop = async (e: DragEvent) => {
    e.preventDefault()
    setOver(false)
    if (sending) return
    setReading(true)
    const got = await readDrop(dropEntries(e.dataTransfer.items))
    setReading(false)
    if ('problem' in got) { setPlan(undefined); setPackError(got.problem); return }
    pick(got.files)
  }

  const publish = async () => {
    if (!plan || problem) return
    setPacking(true)
    setPackError(undefined)
    let source: Blob
    try {
      source = await packSource(plan)
    } catch {
      setPackError('No pudimos preparar la carpeta en este navegador. Prueba con Chrome, Edge o Firefox actualizados.')
      return
    } finally {
      setPacking(false)
    }
    if (source.size > SOURCE_LIMITS.compressed) {
      setPackError('La carpeta comprimida pesa más de 100 MB. Elige solo la carpeta de la app, sin videos ni respaldos.')
      return
    }
    deploy.mutate(
      { name: plan.name, source, config: { port: Number(port), health_path: health.trim() || '/', message: message.trim() || undefined } },
      { onSuccess: () => { toast.success('Publicando…'); onOpenChange(false); reset() } },
    )
  }

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <form onSubmit={e => { e.preventDefault(); void publish() }} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Subir una carpeta</DialogTitle>
            <DialogDescription>La construimos y la publicamos por ti. {first ? '' : 'La versión actual sigue en línea hasta que la nueva esté lista.'}</DialogDescription>
          </DialogHeader>
          <div
            data-over={over || undefined}
            onDragOver={e => { e.preventDefault(); setOver(true) }}
            onDragLeave={() => setOver(false)}
            onDrop={e => void drop(e)}
            className="flex flex-col items-center gap-3 rounded-xl border border-dashed bg-muted/30 px-4 py-6 text-center transition-colors data-over:border-primary data-over:bg-primary/5"
          >
            <FolderUp className="size-6 text-muted-foreground" aria-hidden />
            {plan ? (
              <span className="text-sm" aria-live="polite">
                <span className="font-semibold">{plan.name}</span>
                <span className="text-muted-foreground"> · {plan.files.length.toLocaleString('es-PE')} {plan.files.length === 1 ? 'archivo' : 'archivos'} · {formatBytes(plan.size)}</span>
              </span>
            ) : (
              <span className="text-sm text-muted-foreground" aria-live="polite">{reading ? 'Leyendo la carpeta…' : 'Arrastra aquí la carpeta de tu app'}</span>
            )}
            <Button type="button" variant="outline" size="sm" onClick={() => input.current?.click()} disabled={sending || reading}>
              {plan ? 'Elegir otra carpeta' : 'Elegir carpeta'}
            </Button>
            <input
              ref={input}
              type="file"
              hidden
              // webkitdirectory picks a whole folder (all current browsers)
              {...{ webkitdirectory: '', directory: '' }}
              multiple
              onChange={e => pick(Array.from(e.target.files ?? []))}
            />
          </div>
          {plan && plan.envFiles.length > 0 && (
            <Callout tone="warn" icon={ShieldAlert} title="No subimos tus archivos .env.">
              Pueden tener contraseñas. Las variables de tu app se configuran aparte{technical && <> ({plan.envFiles.join(', ')})</>}.
            </Callout>
          )}
          {plan && technical && plan.skipped > 0 && (
            <p className="text-xs text-muted-foreground">
              {plan.skipped === 1 ? 'Se omite 1 archivo que se vuelve' : `Se omiten ${plan.skipped.toLocaleString('es-PE')} archivos que se vuelven`} a generar al construir (node_modules, .git, dist, cachés…).
            </p>
          )}
          {problem && <Callout tone="bad">{problem}</Callout>}
          {!first && (
            <Field>
              <FieldLabel htmlFor="upload-message">Qué cambió <span className="font-normal text-muted-foreground">(opcional)</span></FieldLabel>
              <Input id="upload-message" maxLength={MAX_MESSAGE} value={message} onChange={e => setMessage(e.target.value)} placeholder="Agrega el filtro por vendedor" />
              <FieldDescription>Así se llamará esta versión en el historial.</FieldDescription>
            </Field>
          )}
          {technical && (
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
          )}
          {packError && <Callout tone="bad">{packError}</Callout>}
          <ErrorNotice error={deploy.error} />
          <DialogFooter>
            <Button type="button" variant="outline" disabled={sending} onClick={() => close(false)}>Cancelar</Button>
            <Button type="submit" disabled={sending || reading || !plan || !!problem || !port}>
              {sending && <Spinner />}
              {packing ? 'Preparando la carpeta…' : deploy.isPending ? 'Subiendo…' : busy ? 'Publicar (reemplaza la que está en curso)' : 'Publicar'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** The API's limit on a version's "Qué cambió" (release.MaxMessage). */
const MAX_MESSAGE = 200

const digestImage = /^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]+)?(?:\/[a-z0-9]+(?:[._-][a-z0-9]+)*)+@sha256:[a-f0-9]{64}$/

/**
 * Technical mode only: publishes an already-built image (pinned by digest).
 * Builders publish from a folder or through their assistant.
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
        <PanelHeader icon={Upload} title="Publicar imagen" description="Una imagen ya construida, fijada por digest." />
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
