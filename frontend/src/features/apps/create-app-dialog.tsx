import { useEffect, useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { Bot, FolderUp, GitBranch } from 'lucide-react'
import { toast } from 'sonner'
import { Button, ButtonLink } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { AssistantPrompt, ErrorNotice, OptionCards } from '@/components/odyssey'
import { ApiError, errorMessage } from '@/lib/api'
import { connectBody, useConnectRepository } from '@/lib/github'
import { noRepoChoice, RepoPicker, useGitHubReadiness, type RepoChoice } from '@/features/github/repo-picker'
import { usePreferences } from '@/lib/preferences'
import { slugify, useCreateProject } from '@/lib/projects'
import type { Project } from '@/lib/types'
import { buildPrompt, deriveName } from './overview'
import { ProposedParts, saveProposal, useRepoProposal } from './services-panel'

/** How the first version arrives (UX v4 §5.6). */
export type Way = 'assistant' | 'github' | 'folder'

/** The app page opens "Subir una carpeta" when it is reached with this (`/apps/:id?subir=1`). */
export const UPLOAD_PARAM = 'subir'

/** A new app has no parts of its own yet: any proposal of two or more is offered. */
const NEW_APP: never[] = []

/**
 * Create an app (UX v4 §5.6, POST /api/v1/projects): "¿Qué quieres crear?" in
 * free text, the name derived from it (editable; the address follows the name
 * like the API does, editable only in technical mode), then one of three ways
 * in: ask your assistant to build it (the prompt, ready to copy), import a
 * GitHub repository (created and connected, which publishes it), or upload a
 * folder (the app opens on its upload dialog).
 */
export function CreateAppDialog({ open, onOpenChange, initialIdea = '' }: { open: boolean; onOpenChange: (open: boolean) => void; initialIdea?: string }) {
  const navigate = useNavigate()
  const { technical } = usePreferences()
  const create = useCreateProject()
  const [idea, setIdea] = useState('')
  // Inicio's "¿Qué quieres crear?" opens it with what was already written
  useEffect(() => {
    if (open && initialIdea) setIdea(initialIdea)
  }, [open, initialIdea])
  const [named, setNamed] = useState<string | null>(null)
  const [slug, setSlug] = useState<string | null>(null)
  const [way, setWay] = useState<Way>('assistant')
  const [choice, setChoice] = useState<RepoChoice>(noRepoChoice)
  const [created, setCreated] = useState<Project>()
  const connect = useConnectRepository()
  const github = useGitHubReadiness(open && way === 'github')
  const importing = way === 'github'
  const proposal = useRepoProposal(importing ? choice : noRepoChoice, NEW_APP)
  const [split, setSplit] = useState(true)
  const qc = useQueryClient()
  const [saving, setSaving] = useState(false)
  const busy = create.isPending || connect.isPending || saving
  const name = named ?? deriveName(idea)
  const address = slug ?? slugify(name)
  const fieldError = create.error instanceof ApiError ? create.error.field : undefined

  const reset = () => {
    setIdea(''); setNamed(null); setSlug(null); setWay('assistant'); setChoice(noRepoChoice); setCreated(undefined); setSplit(true); create.reset(); connect.reset()
  }
  const close = () => { onOpenChange(false); reset() }
  // Naming the app after the repository saves a step; the name stays editable.
  const choose = (next: RepoChoice) => {
    if (next.repo && named === null && !idea.trim()) setNamed(next.repo.full_name.split('/').pop() ?? '')
    setChoice(next)
  }
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (importing && !choice.repo) return
    const project = await create.mutateAsync({ name: name.trim(), description: idea.trim() || undefined, slug: slug ?? undefined }).catch(() => null)
    if (!project) return
    if (way === 'assistant') {
      setCreated(project) // the dialog now shows what to tell the assistant
      return
    }
    if (importing && choice.repo) {
      // The app exists now: if connecting fails, it opens on its "Código" panel to try again.
      // Its parts are saved first, so the first version already builds each one.
      setSaving(true)
      const parts = split && proposal.rows ? await saveProposal(qc, project.id, proposal.rows, 'La app se creó, pero no la conectamos todavía. ') : true
      setSaving(false)
      if (parts) {
        await connect.mutateAsync({ project: project.id, body: connectBody(choice.repo, choice.branch, choice.root) }).then(
          () => toast.success('Conectamos tu repositorio. Lo estamos publicando…'),
          e => toast.error(`La app se creó, pero no pudimos conectar el repositorio. ${errorMessage(e)}`),
        )
      }
    }
    close()
    navigate(way === 'folder' ? `/apps/${project.id}?${UPLOAD_PARAM}=1` : `/apps/${project.id}`)
  }

  return (
    <Dialog open={open} onOpenChange={v => { if (!busy) { if (v) onOpenChange(true); else close() } }}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        {created ? (
          <AskAssistant project={created} idea={idea} onDone={() => { close(); navigate(`/apps/${created.id}`) }} />
        ) : (
          <form onSubmit={submit} className="flex flex-col gap-5">
            <DialogHeader>
              <DialogTitle>Crear una app</DialogTitle>
              <DialogDescription>Cuéntalo con tus palabras. Después eliges cómo llega el código.</DialogDescription>
            </DialogHeader>
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="app-idea">¿Qué quieres crear?</FieldLabel>
                <Textarea id="app-idea" autoFocus maxLength={2000} value={idea} onChange={e => setIdea(e.target.value)} placeholder="Una app para que los vendedores registren sus visitas con fotos y ubicación." />
                <FieldDescription>TI lo lee si pides compartirla con más gente.</FieldDescription>
              </Field>
              <Field data-invalid={fieldError === 'name' || undefined}>
                <FieldLabel htmlFor="app-name">Nombre</FieldLabel>
                <Input id="app-name" required maxLength={200} value={name} onChange={e => setNamed(e.target.value)} placeholder="Visitas a clientes" aria-invalid={fieldError === 'name' || undefined} />
                {!technical && address && <FieldDescription>Dirección: <span className="font-mono text-foreground">{address}</span></FieldDescription>}
              </Field>
              {technical && (
                <Field data-invalid={fieldError === 'slug' || undefined}>
                  <FieldLabel htmlFor="app-slug">Dirección (slug)</FieldLabel>
                  <Input id="app-slug" className="font-mono" value={address} onChange={e => setSlug(e.target.value.toLowerCase())} pattern="[a-z0-9][a-z0-9\-]{1,38}[a-z0-9]" aria-invalid={fieldError === 'slug' || undefined} />
                  <FieldDescription>3–40 letras minúsculas, números o guiones. No se puede cambiar después.</FieldDescription>
                </Field>
              )}
            </FieldGroup>
            <OptionCards
              label="Cómo llega el código"
              value={way}
              onChange={setWay}
              options={[
                { value: 'assistant', icon: <Bot className="size-4" />, title: 'Pedir a mi asistente', description: 'Claude Code, Codex o Antigravity la construye y la publica.' },
                { value: 'github', icon: <GitBranch className="size-4" />, title: 'Importar de GitHub', description: 'Cada cambio que subas a la rama se publica solo.' },
                { value: 'folder', icon: <FolderUp className="size-4" />, title: 'Subir una carpeta', description: 'La construimos y la publicamos por ti.' },
              ]}
            />
            {importing && <RepoPicker value={choice} onChange={choose} idPrefix="create-repo" />}
            {importing && <ProposedParts proposal={proposal} use={split} onUseChange={setSplit} technical={technical} />}
            <ErrorNotice error={create.error} />
            <DialogFooter>
              <Button type="button" variant="outline" disabled={busy} onClick={close}>Cancelar</Button>
              <Button type="submit" disabled={busy || !name.trim() || (importing && (!choice.repo || github.state !== 'ready' || proposal.loading))}>
                {busy && <Spinner />}
                {create.isPending ? 'Creando…' : saving ? 'Guardando las partes…' : connect.isPending ? 'Conectando…' : importing ? 'Crear e importar' : way === 'folder' ? 'Crear y subir' : 'Crear app'}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

/** The app exists: what to tell the assistant so it builds it and publishes it there. */
function AskAssistant({ project, idea, onDone }: { project: Project; idea: string; onDone: () => void }) {
  return (
    <div className="flex flex-col gap-5">
      <DialogHeader>
        <DialogTitle>Creamos «{project.name}»</DialogTitle>
        <DialogDescription>Abre Claude Code, Codex o Antigravity en una carpeta nueva y pídele:</DialogDescription>
      </DialogHeader>
      <AssistantPrompt
        text={buildPrompt(project.slug, idea)}
        actions={<ButtonLink variant="ghost" render={<Link to="/asistente" />}>¿Aún no lo conectas? Conéctalo</ButtonLink>}
      />
      <p className="text-xs text-muted-foreground">Cuando la publique, la verás en línea en la app. Puedes cambiar de camino cuando quieras.</p>
      <DialogFooter>
        <Button onClick={onDone}>Ir a la app</Button>
      </DialogFooter>
    </div>
  )
}
