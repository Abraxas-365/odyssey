import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { Lock, Search } from 'lucide-react'
import { Button, ButtonLink } from '@/components/ui/button'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Callout, ErrorNotice } from '@/components/odyssey'
import { useAuth } from '@/lib/auth'
import { useBranches, useGitHubInfo, useInstallations, useRepositories } from '@/lib/github'
import { permissions } from '@/lib/permissions'
import { Technical } from '@/lib/preferences'
import type { GitHubRepository } from '@/lib/types'
import { cn } from '@/lib/utils'

/** What the person picked: a repository, one of its branches and (technical) a folder in it. */
export interface RepoChoice {
  repo?: GitHubRepository
  branch: string
  root: string
}

export const noRepoChoice: RepoChoice = { branch: '', root: '' }

const PAGE = 20
const MAX = 100 // the API clamps larger pages

function useDebounced(value: string, ms = 250) {
  const [v, setV] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms)
    return () => clearTimeout(t)
  }, [value, ms])
  return v
}

export type GitHubReadiness = 'loading' | 'error' | 'disabled' | 'none' | 'ready'

/** Can this person pick a repository? GitHub off, no account connected, or ready. */
export function useGitHubReadiness(enabled = true) {
  const info = useGitHubInfo()
  const on = enabled && !!info.data?.enabled
  const installations = useInstallations(on)
  const error = info.error ?? installations.error
  let state: GitHubReadiness = 'ready'
  if (info.isPending || (on && installations.isPending)) state = 'loading'
  else if (error) state = 'error'
  else if (!info.data?.enabled) state = 'disabled'
  else if (!installations.data?.items.length) state = 'none'
  return { state, error, retry: () => { void info.refetch(); void installations.refetch() } }
}

/** Explains who must connect GitHub: TI, or you if you can (with the way there). */
export function ConnectGitHubNotice({ state }: { state: 'disabled' | 'none' }) {
  const { can } = useAuth()
  const canManage = can(permissions.integrationsManage)
  const title = state === 'disabled' ? 'GitHub todavía no está disponible en tu organización.' : 'Tu organización aún no conectó GitHub.'
  return (
    <Callout
      tone="info"
      title={title}
      action={canManage && <ButtonLink size="sm" variant="outline" render={<Link to="/administracion/github" />}>{state === 'disabled' ? 'Ver cómo' : 'Conectar GitHub'}</ButtonLink>}
    >
      {canManage ? 'Conéctalo en la configuración y vuelve aquí.' : 'Pídele a TI que conecte GitHub y vuelve aquí.'}
    </Callout>
  )
}

/**
 * Pick a repository (searchable), its branch (default: the repository's own)
 * and, in technical mode, the app's folder inside it. Shared by "Crear app"
 * and the app's "Código" panel.
 */
export function RepoPicker({ value, onChange, idPrefix = 'repo' }: { value: RepoChoice; onChange: (v: RepoChoice) => void; idPrefix?: string }) {
  const ready = useGitHubReadiness()
  if (ready.state === 'loading') return <Skeleton className="h-40 w-full" />
  if (ready.state === 'error') return <ErrorNotice error={ready.error} onRetry={ready.retry} />
  if (ready.state === 'disabled' || ready.state === 'none') return <ConnectGitHubNotice state={ready.state} />
  return <Picker value={value} onChange={onChange} idPrefix={idPrefix} />
}

function Picker({ value, onChange, idPrefix }: { value: RepoChoice; onChange: (v: RepoChoice) => void; idPrefix: string }) {
  const [search, setSearch] = useState('')
  const [limit, setLimit] = useState(PAGE)
  const term = useDebounced(search.trim())
  const repos = useRepositories(term, limit)
  const items = repos.data?.items ?? []
  const total = repos.data?.page.total ?? 0
  const repo = value.repo

  const pick = (r: GitHubRepository) => onChange({ ...value, repo: r, branch: r.default_branch })

  return (
    <FieldGroup>
      <Field>
        <FieldLabel htmlFor={`${idPrefix}-search`}>Repositorio</FieldLabel>
        <div className="relative">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
          <Input
            id={`${idPrefix}-search`}
            className="pl-8"
            value={search}
            onChange={e => { setSearch(e.target.value); setLimit(PAGE) }}
            placeholder="Busca por nombre"
            autoComplete="off"
          />
        </div>
        <ErrorNotice error={repos.error} onRetry={() => repos.refetch()} />
        <div className="max-h-52 overflow-y-auto rounded-md border" aria-busy={repos.isFetching}>
          {repos.isPending ? (
            <div className="flex flex-col gap-2 p-3"><Skeleton className="h-4 w-1/2" /><Skeleton className="h-4 w-2/3" /><Skeleton className="h-4 w-1/3" /></div>
          ) : items.length === 0 ? (
            <p className="px-3 py-4 text-center text-[13px] text-muted-foreground">{term ? 'Ningún repositorio coincide con tu búsqueda.' : 'GitHub no mostró repositorios. Revisa a cuáles dio acceso TI.'}</p>
          ) : (
            <ul role="list" className="divide-y">
              {items.map(r => (
                <li key={`${r.installation_id}:${r.id}`}>
                  <button
                    type="button"
                    aria-pressed={repo?.id === r.id && repo.installation_id === r.installation_id}
                    onClick={() => pick(r)}
                    className={cn(
                      'flex w-full cursor-pointer items-center gap-2 px-3 py-2 text-left text-sm outline-none hover:bg-muted/50 focus-visible:bg-muted/50',
                      repo?.id === r.id && repo.installation_id === r.installation_id && 'bg-muted font-semibold',
                    )}
                  >
                    <span className="min-w-0 flex-1 truncate">{r.full_name}</span>
                    {r.private && (
                      <span className="inline-flex items-center gap-1 text-xs font-normal text-muted-foreground">
                        <Lock className="size-3" aria-hidden />
                        Privado
                      </span>
                    )}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
        {total > items.length && limit < MAX && (
          <Button type="button" variant="ghost" size="sm" className="self-start" onClick={() => setLimit(l => Math.min(l + PAGE, MAX))}>
            Mostrar más
          </Button>
        )}
        {total > items.length && limit >= MAX && <FieldDescription>Hay más repositorios: escribe parte del nombre para encontrarlo.</FieldDescription>}
      </Field>
      {repo && <BranchField repo={repo} value={value} onChange={onChange} idPrefix={idPrefix} />}
    </FieldGroup>
  )
}

function BranchField({ repo, value, onChange, idPrefix }: { repo: GitHubRepository; value: RepoChoice; onChange: (v: RepoChoice) => void; idPrefix: string }) {
  const branches = useBranches(repo.installation_id, repo.id)
  const names = branches.data?.items.map(b => b.name) ?? []
  // The default branch is always offered, even before (or without) the list.
  const options = names.includes(repo.default_branch) ? names : [repo.default_branch, ...names]
  const items = Object.fromEntries(options.map(n => [n, n === repo.default_branch ? `${n} (la principal)` : n]))
  return (
    <>
      <Field>
        <FieldLabel htmlFor={`${idPrefix}-branch`}>Rama</FieldLabel>
        <Select value={value.branch || repo.default_branch} onValueChange={v => onChange({ ...value, branch: (v as string | null) ?? repo.default_branch })} items={items}>
          <SelectTrigger id={`${idPrefix}-branch`} className="w-full"><SelectValue /></SelectTrigger>
          <SelectContent>
            {options.map(n => <SelectItem key={n} value={n}>{items[n]}</SelectItem>)}
          </SelectContent>
        </Select>
        <FieldDescription>Cada cambio que subas a esta rama se publica solo.</FieldDescription>
        {branches.error && <ErrorNotice error={branches.error} onRetry={() => branches.refetch()} />}
      </Field>
      <Technical>
        <Field>
          <FieldLabel htmlFor={`${idPrefix}-root`}>Carpeta raíz <span className="font-normal text-muted-foreground">(opcional)</span></FieldLabel>
          <Input id={`${idPrefix}-root`} className="font-mono" value={value.root} onChange={e => onChange({ ...value, root: e.target.value })} placeholder="apps/web" maxLength={200} autoComplete="off" />
          <FieldDescription>Si la app vive en una subcarpeta del repositorio. Vacío usa la raíz.</FieldDescription>
        </Field>
      </Technical>
    </>
  )
}
