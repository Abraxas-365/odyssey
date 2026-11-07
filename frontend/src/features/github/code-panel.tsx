import { useState } from 'react'
import { Code2, GitBranch, RefreshCw, Rocket, Unplug } from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Spinner } from '@/components/ui/spinner'
import { Callout, ErrorNotice, GitLine, ListSkeleton, Panel, PanelBody, PanelFooter, PanelHeader } from '@/components/odyssey'
import { useAuth } from '@/lib/auth'
import { formatRelative } from '@/lib/format'
import { connectBody, pushActive, pushStatusText, useConnectRepository, useDisconnectRepository, useProjectRepository, usePublishNow, usePushes } from '@/lib/github'
import { permissions } from '@/lib/permissions'
import { Technical, usePreferences } from '@/lib/preferences'
import { useServices } from '@/lib/services'
import type { GitHubPush, Project } from '@/lib/types'
import { ProposedParts, saveProposal, useRepoProposal } from '@/features/apps/services-panel'
import { noRepoChoice, RepoPicker, useGitHubReadiness, type RepoChoice } from './repo-picker'

/** What the latest push says, in plain words (the failure's own message when it failed). */
export function pushSummary(push: GitHubPush | undefined): { tone: 'ok' | 'info' | 'bad' | 'neutral'; text: string } | undefined {
  if (!push) return undefined
  switch (push.status) {
    case 'failed': return { tone: 'bad', text: push.error?.message || pushStatusText.failed }
    case 'deployed': return { tone: 'ok', text: pushStatusText.deployed }
    case 'queued':
    case 'importing': return { tone: 'info', text: pushStatusText[push.status] }
    default: return { tone: 'neutral', text: pushStatusText[push.status] }
  }
}

/**
 * App → Resumen → "Código": the app's GitHub repository, the last commit and
 * what happened to it, "Publicar ahora" and "Desconectar". Without a
 * repository it offers to connect one.
 */
export function CodePanel({ project, archived }: { project: Project; archived: boolean }) {
  const { can } = useAuth()
  const repository = useProjectRepository(project.id, can(permissions.deploymentsRead))
  const conn = repository.data
  if (!can(permissions.deploymentsRead)) return null
  if (repository.isPending) return <Panel><PanelHeader icon={Code2} title="Código" /><ListSkeleton rows={2} /></Panel>
  if (repository.error) return <ErrorNotice error={repository.error} onRetry={() => repository.refetch()} />
  if (!conn) return <ConnectPanel project={project} canConnect={can(permissions.projectsWrite) && can(permissions.deploymentsCreate) && !archived} />
  return <ConnectedPanel project={project} repo={conn.repository} branch={conn.branch} root={conn.root} archived={archived} />
}

function ConnectedPanel({ project, repo, branch, root, archived }: { project: Project; repo: string; branch: string; root: string; archived: boolean }) {
  const { can } = useAuth()
  const pushes = usePushes(project.id)
  const publish = usePublishNow(project.id)
  const disconnect = useDisconnectRepository(project.id)
  const [confirm, setConfirm] = useState(false)
  const last = pushes.data?.items[0]
  const summary = pushSummary(last)
  const busy = pushActive(last?.status) || publish.isPending

  return (
    <Panel>
      <PanelHeader icon={Code2} title="Código" description="Cada cambio que subas a GitHub se publica solo." />
      <PanelBody>
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
          <GitBranch className="size-4 shrink-0" aria-hidden />
          <a className="font-semibold underline-offset-4 hover:underline" href={`https://github.com/${repo}`} target="_blank" rel="noreferrer">{repo}</a>
          <span className="text-muted-foreground">· rama <span className="font-mono">{branch}</span></span>
          <Technical>{root && <span className="text-muted-foreground">· carpeta <span className="font-mono">{root}</span></span>}</Technical>
        </div>
        {last && (
          <div className="flex flex-col gap-1">
            <GitLine branch={last.git.branch} sha={last.git.commit} message={last.git.title} author={last.git.author} />
            <span className="text-xs text-muted-foreground">{formatRelative(last.created_at)}</span>
          </div>
        )}
        {pushes.error && <ErrorNotice error={pushes.error} onRetry={() => pushes.refetch()} />}
        {last?.status === 'failed' ? (
          <Callout tone="bad" title="No se pudo publicar el último cambio.">
            {summary?.text}
            <Technical>{last.error?.detail && <span className="mt-1.5 block font-mono text-xs break-words">{last.error.detail}</span>}</Technical>
          </Callout>
        ) : (
          summary && (
            <p className="flex items-center gap-2 text-[13px]" aria-live="polite">
              {pushActive(last?.status) && <Spinner className="size-3.5" />}
              {summary.text}
            </p>
          )
        )}
        {!last && !pushes.isPending && <p className="text-[13px] text-muted-foreground">Todavía no hay cambios publicados desde GitHub.</p>}
        <ErrorNotice error={publish.error} />
      </PanelBody>
      {!archived && (can(permissions.deploymentsCreate) || can(permissions.projectsWrite)) && (
        <PanelFooter>
          <span>{busy ? 'Publicando el último cambio…' : 'Publica el último cambio de la rama sin esperar uno nuevo.'}</span>
          <span className="flex items-center gap-2">
            {can(permissions.projectsWrite) && (
              <Button variant="ghost" size="sm" onClick={() => setConfirm(true)}>
                <Unplug />
                Desconectar
              </Button>
            )}
            {can(permissions.deploymentsCreate) && (
              <Button
                variant="ghost"
                size="sm"
                disabled={busy}
                title="Construye todo otra vez, aunque el código no haya cambiado: por ejemplo, para tomar una versión nueva de la imagen base."
                onClick={() => publish.mutate({ rebuild: true }, { onSuccess: () => toast.success('Construyendo todo otra vez y publicando…') })}
              >
                <RefreshCw />
                Reconstruir todo
              </Button>
            )}
            {can(permissions.deploymentsCreate) && (
              <Button size="sm" disabled={busy} onClick={() => publish.mutate(undefined, { onSuccess: () => toast.success('Publicando el último cambio…') })}>
                <Rocket />
                Publicar ahora
              </Button>
            )}
          </span>
        </PanelFooter>
      )}
      <AlertDialog open={confirm} onOpenChange={open => { if (!open) { setConfirm(false); disconnect.reset() } }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>¿Desconectar {repo}?</AlertDialogTitle>
            <AlertDialogDescription>
              Dejamos de publicar los cambios de GitHub. La versión que está en línea y el historial se quedan como están; puedes volver a conectarlo cuando quieras.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <ErrorNotice error={disconnect.error} />
          <AlertDialogFooter>
            <AlertDialogCancel>Cancelar</AlertDialogCancel>
            <AlertDialogAction disabled={disconnect.isPending} onClick={() => disconnect.mutate(undefined, { onSuccess: () => { setConfirm(false); toast.success('Repositorio desconectado') } })}>
              {disconnect.isPending ? 'Desconectando…' : 'Desconectar'}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Panel>
  )
}

function ConnectPanel({ project, canConnect }: { project: Project; canConnect: boolean }) {
  const [open, setOpen] = useState(false)

  if (!canConnect) {
    return (
      <Panel>
        <PanelHeader icon={Code2} title="Código" />
        <PanelBody><p className="text-[13px] text-muted-foreground">Esta app no está conectada a un repositorio de GitHub.</p></PanelBody>
      </Panel>
    )
  }
  return (
    <Panel>
      <PanelHeader icon={Code2} title="Código" description="Si tu código está en GitHub, cada cambio se publica solo." actions={<Button variant="outline" size="sm" onClick={() => setOpen(true)}>Conectar un repositorio</Button>} />
      <ConnectRepoDialog project={project} open={open} onOpenChange={setOpen} />
    </Panel>
  )
}

/** "Conectar un repositorio": pick the repository and branch; connecting publishes it. */
export function ConnectRepoDialog({ project, open, onOpenChange }: { project: Project; open: boolean; onOpenChange: (open: boolean) => void }) {
  const [choice, setChoice] = useState<RepoChoice>(noRepoChoice)
  const connect = useConnectRepository()
  const github = useGitHubReadiness(open)
  const { technical } = usePreferences()
  const services = useServices(open ? project.id : undefined)
  const proposal = useRepoProposal(choice, services.data?.items)
  const [split, setSplit] = useState(true)
  const [saving, setSaving] = useState(false)
  const qc = useQueryClient()
  const pending = connect.isPending || saving
  const close = (next: boolean) => {
    if (pending) return
    onOpenChange(next)
    if (!next) { setChoice(noRepoChoice); setSplit(true); connect.reset() }
  }
  const submit = async () => {
    if (!choice.repo) return
    // the parts first, so the first version already builds each one
    if (split && proposal.rows) {
      setSaving(true)
      const saved = await saveProposal(qc, project.id, proposal.rows)
      setSaving(false)
      if (!saved) return
    }
    connect.mutate(
      { project: project.id, body: connectBody(choice.repo, choice.branch, choice.root) },
      { onSuccess: () => { toast.success('Conectamos tu repositorio. Lo estamos publicando…'); close(false) } },
    )
  }
  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <form onSubmit={e => { e.preventDefault(); void submit() }} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Conectar un repositorio</DialogTitle>
            <DialogDescription>Elige el repositorio y la rama. Cada cambio que subas a esa rama se publica solo.</DialogDescription>
          </DialogHeader>
          <RepoPicker value={choice} onChange={setChoice} idPrefix="code-repo" />
          <ProposedParts proposal={proposal} use={split} onUseChange={setSplit} technical={technical} />
          <ErrorNotice error={connect.error} />
          <DialogFooter>
            <Button type="button" variant="outline" disabled={pending} onClick={() => close(false)}>Cancelar</Button>
            <Button type="submit" disabled={!choice.repo || github.state !== 'ready' || pending || proposal.loading}>
              {pending && <Spinner />}
              {saving ? 'Guardando las partes…' : connect.isPending ? 'Conectando…' : 'Conectar y publicar'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
