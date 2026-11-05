import { useEffect, useRef, useState } from 'react'
import { FolderUp, GitBranch, RotateCcw, ShieldAlert, Upload } from 'lucide-react'
import { toast } from 'sonner'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Callout, EmptyState, ErrorNotice, ListSkeleton, LogViewer, Panel, PanelBody, PanelFooter, PanelHeader, PanelList, ProgressSteps, TechnicalDetails, VersionRow, type LogSegment, type LogTone } from '@/components/odyssey'
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

/** Prefills the (technical) port field with the API's default, 8080: the agent sets $PORT for the app. */
const BUILD_PORT = '8080'

/**
 * "Desde una carpeta": pick the app's folder, see what goes up (and that
 * .env files don't), publish. The browser packs it; the cluster builds it.
 * Port and health path are technical details.
 */
export function PublishFolderPanel({ project, env, busy, technical }: { project: string; env: string; busy: boolean; technical: boolean }) {
  const deploy = useDeploySource(project, env)
  const input = useRef<HTMLInputElement>(null)
  const [plan, setPlan] = useState<SourcePlan>()
  const [packing, setPacking] = useState(false)
  const [packError, setPackError] = useState<string>()
  const [port, setPort] = useState(BUILD_PORT)
  const [health, setHealth] = useState('/')
  const problem = plan && planProblem(plan)
  const sending = packing || deploy.isPending

  const pick = (files: FileList | null) => {
    deploy.reset()
    setPackError(undefined)
    setPlan(files && files.length ? planSource(Array.from(files)) : undefined)
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
      { name: plan.name, source, config: { port: Number(port), health_path: health.trim() || '/' } },
      {
        onSuccess: () => {
          toast.success('Publicando…')
          setPlan(undefined)
          if (input.current) input.current.value = ''
        },
      },
    )
  }

  return (
    <Panel>
      <form onSubmit={e => { e.preventDefault(); void publish() }}>
        <PanelHeader icon={FolderUp} title="Publicar desde una carpeta" description="Elige la carpeta de tu app. La construimos y la publicamos por ti." />
        <PanelBody>
          <div className="flex flex-wrap items-center gap-3">
            <Button type="button" variant="outline" onClick={() => input.current?.click()} disabled={sending}>
              <FolderUp />
              {plan ? 'Elegir otra carpeta' : 'Elegir carpeta'}
            </Button>
            <input
              ref={input}
              type="file"
              hidden
              // webkitdirectory picks a whole folder (all current browsers)
              {...{ webkitdirectory: '', directory: '' }}
              multiple
              onChange={e => pick(e.target.files)}
            />
            {plan && (
              <span className="text-sm" aria-live="polite">
                <span className="font-semibold">{plan.name}</span>
                <span className="text-muted-foreground"> · {plan.files.length.toLocaleString('es-PE')} {plan.files.length === 1 ? 'archivo' : 'archivos'} · {formatBytes(plan.size)}</span>
              </span>
            )}
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
        </PanelBody>
        <PanelFooter className="justify-end">
          <Button type="submit" disabled={sending || !plan || !!problem || !port}>
            {packing ? 'Preparando la carpeta…' : deploy.isPending ? 'Subiendo…' : busy ? 'Publicar (reemplaza la que está en curso)' : 'Publicar'}
          </Button>
        </PanelFooter>
      </form>
    </Panel>
  )
}

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
