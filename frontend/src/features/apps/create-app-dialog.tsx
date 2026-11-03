import { useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { ChipGroup, ErrorNotice } from '@/components/odyssey'
import { ApiError, errorMessage } from '@/lib/api'
import { connectBody, useConnectRepository } from '@/lib/github'
import { noRepoChoice, RepoPicker, useGitHubReadiness, type RepoChoice } from '@/features/github/repo-picker'
import { usePreferences } from '@/lib/preferences'
import { slugify, useCreateProject } from '@/lib/projects'

type Source = 'blank' | 'github'

/**
 * Create an app (POST /api/v1/projects). The address (slug) is derived from
 * the name like the API does; only technical mode lets you edit it. A second
 * path imports a GitHub repository: the app is created and then connected to
 * the chosen branch (PUT /projects/:id/repository), which publishes it.
 */
export function CreateAppDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const navigate = useNavigate()
  const { technical } = usePreferences()
  const create = useCreateProject()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [slug, setSlug] = useState<string | null>(null)
  const [source, setSource] = useState<Source>('blank')
  const [choice, setChoice] = useState<RepoChoice>(noRepoChoice)
  const connect = useConnectRepository()
  const github = useGitHubReadiness(open && source === 'github')
  const importing = source === 'github'
  const busy = create.isPending || connect.isPending
  const address = slug ?? slugify(name)
  const fieldError = create.error instanceof ApiError ? create.error.field : undefined

  const reset = () => { setName(''); setDescription(''); setSlug(null); setSource('blank'); setChoice(noRepoChoice); create.reset(); connect.reset() }
  // Naming the app after the repository saves a step; the name stays editable.
  const choose = (next: RepoChoice) => {
    if (next.repo && !name.trim()) setName(next.repo.full_name.split('/').pop() ?? '')
    setChoice(next)
  }
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (importing && !choice.repo) return
    const project = await create.mutateAsync({ name: name.trim(), description: description.trim() || undefined, slug: slug ?? undefined }).catch(() => null)
    if (!project) return
    if (importing && choice.repo) {
      // The app exists now: if connecting fails, it opens on its "Código" panel to try again.
      await connect.mutateAsync({ project: project.id, body: connectBody(choice.repo, choice.branch, choice.root) }).then(
        () => toast.success('Conectamos tu repositorio. Lo estamos publicando…'),
        e => toast.error(`La app se creó, pero no pudimos conectar el repositorio. ${errorMessage(e)}`),
      )
    }
    onOpenChange(false)
    reset()
    navigate(`/apps/${project.id}`)
  }

  return (
    <Dialog open={open} onOpenChange={v => { if (!busy) { onOpenChange(v); if (!v) reset() } }}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Crear una app</DialogTitle>
            <DialogDescription>{importing ? 'Elige el código que ya tienes en GitHub. Cada cambio que subas se publica solo.' : 'Ponle un nombre. Después la conectas a tu asistente de IA o a GitHub.'}</DialogDescription>
          </DialogHeader>
          <ChipGroup
            label="Cómo empezar"
            value={source}
            onChange={setSource}
            options={[{ value: 'blank', label: 'Empezar de cero' }, { value: 'github', label: 'Importar de GitHub' }]}
          />
          {importing && <RepoPicker value={choice} onChange={choose} idPrefix="create-repo" />}
          <FieldGroup>
            <Field data-invalid={fieldError === 'name' || undefined}>
              <FieldLabel htmlFor="app-name">Nombre</FieldLabel>
              <Input id="app-name" autoFocus required maxLength={200} value={name} onChange={e => setName(e.target.value)} placeholder="Visitas a clientes" aria-invalid={fieldError === 'name' || undefined} />
              {!technical && address && <FieldDescription>Dirección: <span className="font-mono text-foreground">{address}</span></FieldDescription>}
            </Field>
            {technical && (
              <Field data-invalid={fieldError === 'slug' || undefined}>
                <FieldLabel htmlFor="app-slug">Dirección (slug)</FieldLabel>
                <Input id="app-slug" className="font-mono" value={address} onChange={e => setSlug(e.target.value.toLowerCase())} pattern="[a-z0-9][a-z0-9\-]{1,38}[a-z0-9]" aria-invalid={fieldError === 'slug' || undefined} />
                <FieldDescription>3–40 letras minúsculas, números o guiones. No se puede cambiar después.</FieldDescription>
              </Field>
            )}
            <Field>
              <FieldLabel htmlFor="app-description">Para qué sirve <span className="font-normal text-muted-foreground">(opcional)</span></FieldLabel>
              <Textarea id="app-description" maxLength={2000} value={description} onChange={e => setDescription(e.target.value)} placeholder="Los vendedores registran sus visitas con fotos y ubicación." />
              <FieldDescription>TI la pide para compartir la app con más gente.</FieldDescription>
            </Field>
          </FieldGroup>
          <ErrorNotice error={create.error} />
          <DialogFooter>
            <Button type="button" variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>Cancelar</Button>
            <Button type="submit" disabled={busy || !name.trim() || (importing && (!choice.repo || github.state !== 'ready'))}>
              {busy && <Spinner />}
              {create.isPending ? 'Creando…' : connect.isPending ? 'Conectando…' : importing ? 'Crear e importar' : 'Crear app'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
