import { useState, type FormEvent } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { AppWindow, ArrowRight, Bot, LayoutGrid, List, Plus, Sparkles } from 'lucide-react'
import { Button, ButtonLink } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { AppCard, AppList, AppRow, ChipGroup, CreateAppTile, EmptyState, ErrorNotice, Panel, PanelBody, SectionHeader, type AppRowData } from '@/components/odyssey'
import { Skeleton } from '@/components/ui/skeleton'
import { useAuth } from '@/lib/auth'
import { permissions } from '@/lib/permissions'
import { usePreferences } from '@/lib/preferences'
import { useProjects } from '@/lib/projects'
import { useAppStates, useAttention } from '@/lib/inventory'
import { safeAppUrl } from '@/lib/format'
import type { AppState, Project, ProjectStatus } from '@/lib/types'
import { cardAudience, cardStatus, changeLine } from '@/features/apps/app-state'
import { CreateAppDialog } from '@/features/apps/create-app-dialog'
import { AttentionSection, allInOrder } from '@/features/inventory/attention-section'

type View = 'list' | 'grid'
const viewKey = 'odyssey:inicio:view'

/**
 * Inicio (UX v4 §5.2): a small greeting, "Necesita tu atención" only when
 * something real needs it, then the apps as a list a person reads (two
 * lines per app; a grid on request). With no apps yet, "¿Qué quieres crear?"
 * (§5.6).
 */
export function HomePage() {
  const { user, can } = useAuth()
  const { technical } = usePreferences()
  const [search, setSearch] = useState('')
  const [status, setStatus] = useState<ProjectStatus>('active')
  const [view, setViewState] = useState<View>(() => (localStorage.getItem(viewKey) === 'grid' ? 'grid' : 'list'))
  const setView = (v: View) => { localStorage.setItem(viewKey, v); setViewState(v) }
  const [params, setParams] = useSearchParams()
  const creating = params.get('crear') === '1'
  const [idea, setIdea] = useState('')
  const setCreating = (v: boolean) => setParams(v ? { crear: '1' } : {}, { replace: true })
  const projects = useProjects({ status, search: search.trim() || undefined, limit: 50 })
  const attention = useAttention()
  const first = user.name?.split(' ')[0]
  const canWrite = can(permissions.projectsWrite)
  const items = projects.data?.items ?? []
  // How each app stands: what its page would say (never a guessed default).
  // Archived apps need no read: they are archived.
  const ids = items.filter(p => p.status !== 'archived').map(p => p.id)
  const states = useAppStates(ids, can(permissions.deploymentsRead))
  const byId = new Map((states.data?.items ?? []).map(s => [s.app_id, s]))
  const firstTime = projects.isSuccess && items.length === 0 && !search && status === 'active'
  const total = projects.data?.page.total

  const rowOf = (p: Project): AppRowData => {
    const archived = p.status === 'archived'
    const state: AppState | undefined = archived ? undefined : byId.get(p.id)
    // The last states read stay after a failed refresh (the notice says so); an app whose
    // state is not in yet (first load, or a new search still showing the previous one) waits.
    const st = cardStatus(state, archived)
    const { text, channel } = changeLine(state, user.id, p.updated_at)
    return {
      name: p.name,
      status: st?.status,
      statusLabel: st?.label,
      loading: !archived && !state && (states.isLoading || states.isPlaceholderData),
      url: safeAppUrl(state?.url),
      change: text,
      channel,
      audience: cardAudience(state),
      detail: technical ? p.slug : undefined,
    }
  }

  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-muted-foreground">
          <span className="font-heading font-semibold text-foreground">{first ? `Hola, ${first}` : 'Inicio'}</span>
          {total !== undefined && !firstTime && ` · ${total} ${total === 1 ? 'app' : 'apps'}`}
          {allInOrder(attention.data) && !firstTime && ' · todo en orden'}
        </p>
        <div className="flex flex-wrap items-center gap-2">
          <ButtonLink variant="outline" render={<Link to="/asistente" />}>
            <Bot />
            Conectar mi asistente
          </ButtonLink>
          {canWrite && !firstTime && (
            <Button onClick={() => setCreating(true)}>
              <Plus />
              Crear app
            </Button>
          )}
        </div>
      </div>

      <AttentionSection />

      {firstTime && canWrite ? (
        <FirstApp idea={idea} onIdea={setIdea} onContinue={() => setCreating(true)} />
      ) : (
        <>
          <SectionHeader
            title="Apps"
            actions={
              <>
                <Input type="search" aria-label="Buscar apps" placeholder="Buscar por nombre…" value={search} onChange={e => setSearch(e.target.value)} className="w-full sm:w-56" />
                <ChipGroup label="Estado" value={status} onChange={setStatus} options={[{ value: 'active', label: 'Activas' }, { value: 'archived', label: 'Archivadas' }]} />
                <ChipGroup
                  label="Vista"
                  value={view}
                  onChange={setView}
                  options={[
                    { value: 'list', label: <><List className="size-3.5" aria-hidden />Lista</> },
                    { value: 'grid', label: <><LayoutGrid className="size-3.5" aria-hidden />Cuadrícula</> },
                  ]}
                />
              </>
            }
          />

          <ErrorNotice error={projects.error} onRetry={() => projects.refetch()} />
          {states.isError && !projects.error && (
            <p role="status" className="text-sm text-muted-foreground">
              No pudimos leer el estado de las apps ahora.{' '}
              <button type="button" className="underline underline-offset-2" onClick={() => states.refetch()}>Reintentar</button>
            </p>
          )}

          {projects.isPending ? (
            <div className="flex flex-col gap-2" aria-busy>
              {[0, 1, 2].map(i => <Skeleton key={i} className="h-14 rounded-lg" />)}
            </div>
          ) : firstTime ? (
            <EmptyState icon={AppWindow} title="Todavía no hay apps">Cuando alguien de tu equipo cree una, la verás aquí.</EmptyState>
          ) : items.length === 0 ? (
            <EmptyState icon={AppWindow} title="No hay apps que coincidan">Prueba con otro nombre o cambia el filtro.</EmptyState>
          ) : view === 'list' ? (
            <AppList>
              {items.map(p => <AppRow key={p.id} href={`/apps/${p.id}`} linkAs={Link} app={rowOf(p)} />)}
            </AppList>
          ) : (
            <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
              {items.map(p => <AppCard key={p.id} href={`/apps/${p.id}`} linkAs={Link} technical={technical} app={{ ...rowOf(p), description: p.description || undefined }} />)}
              {canWrite && status === 'active' && <CreateAppTile onClick={() => setCreating(true)} />}
            </div>
          )}
        </>
      )}

      <CreateAppDialog open={creating} onOpenChange={v => { setCreating(v); if (!v) setIdea('') }} initialIdea={idea} />
    </>
  )
}

/**
 * The first time (UX v4 §5.6, Pedro): no list to show, one question. What is
 * written goes on to the create dialog, which derives the name from it.
 */
function FirstApp({ idea, onIdea, onContinue }: { idea: string; onIdea: (v: string) => void; onContinue: () => void }) {
  const submit = (e: FormEvent) => { e.preventDefault(); onContinue() }
  return (
    <Panel>
      <PanelBody>
        <form onSubmit={submit} className="mx-auto flex max-w-xl flex-col gap-4 py-6 text-center">
          <Sparkles className="mx-auto size-6 text-ai" aria-hidden />
          <div>
            <h1 className="font-heading text-title font-bold">¿Qué quieres crear?</h1>
            <p className="mt-1 text-muted-foreground">Cuéntalo con tus palabras. Tu asistente de IA la construye y Odicea la publica con la cuenta de tu empresa.</p>
          </div>
          <label htmlFor="first-idea" className="sr-only">¿Qué quieres crear?</label>
          <Textarea id="first-idea" value={idea} onChange={e => onIdea(e.target.value)} maxLength={2000} placeholder="Una app para que los vendedores registren sus visitas con fotos y ubicación." className="min-h-24 text-left" />
          <div className="flex flex-wrap items-center justify-center gap-2">
            <Button type="submit" size="lg">
              Continuar
              <ArrowRight />
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">Después eliges cómo llega el código: tu asistente, un repositorio de GitHub o una carpeta.</p>
        </form>
      </PanelBody>
    </Panel>
  )
}
