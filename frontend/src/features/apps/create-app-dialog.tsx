import { useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { ErrorNotice } from '@/components/odyssey'
import { ApiError } from '@/lib/api'
import { usePreferences } from '@/lib/preferences'
import { slugify, useCreateProject } from '@/lib/projects'

/**
 * Create an app (POST /api/v1/projects). The address (slug) is derived from
 * the name like the API does; only technical mode lets you edit it.
 */
export function CreateAppDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const navigate = useNavigate()
  const { technical } = usePreferences()
  const create = useCreateProject()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [slug, setSlug] = useState<string | null>(null)
  const address = slug ?? slugify(name)
  const fieldError = create.error instanceof ApiError ? create.error.field : undefined

  const reset = () => { setName(''); setDescription(''); setSlug(null); create.reset() }
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const project = await create.mutateAsync({ name: name.trim(), description: description.trim() || undefined, slug: slug ?? undefined }).catch(() => null)
    if (project) { onOpenChange(false); reset(); navigate(`/apps/${project.id}`) }
  }

  return (
    <Dialog open={open} onOpenChange={v => { if (!create.isPending) { onOpenChange(v); if (!v) reset() } }}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Crear una app</DialogTitle>
            <DialogDescription>Ponle un nombre. Después la conectas a tu asistente de IA o a GitHub.</DialogDescription>
          </DialogHeader>
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
            <Button type="button" variant="outline" disabled={create.isPending} onClick={() => onOpenChange(false)}>Cancelar</Button>
            <Button type="submit" disabled={create.isPending || !name.trim()}>
              {create.isPending && <Spinner />}
              {create.isPending ? 'Creando…' : 'Crear app'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
