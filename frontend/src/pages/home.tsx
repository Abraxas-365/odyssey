import { useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { AppWindow, Bot, Plus } from 'lucide-react'
import { Button, ButtonLink } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { AppCard, ChipGroup, CreateAppTile, EmptyState, ErrorNotice, PageHeader, SectionHeader } from '@/components/odyssey'
import { Skeleton } from '@/components/ui/skeleton'
import { useAuth } from '@/lib/auth'
import { formatRelative } from '@/lib/format'
import { permissions } from '@/lib/permissions'
import { usePreferences } from '@/lib/preferences'
import { useProjects } from '@/lib/projects'
import { useAppStates } from '@/lib/inventory'
import type { ProjectStatus } from '@/lib/types'
import { cardAudience, cardHost, cardStatus } from '@/features/apps/app-state'
import { CreateAppDialog } from '@/features/apps/create-app-dialog'
import { AttentionSection } from '@/features/inventory/attention-section'

/**
 * Inicio. "Necesita tu atención" (UX v3 §3) goes on top, then the apps.
 */
export function HomePage() {
  const { user, can } = useAuth()
  const { technical } = usePreferences()
  const [search, setSearch] = useState('')
  const [status, setStatus] = useState<ProjectStatus>('active')
  const [params, setParams] = useSearchParams()
  const creating = params.get('crear') === '1'
  const setCreating = (v: boolean) => setParams(v ? { crear: '1' } : {}, { replace: true })
  const projects = useProjects({ status, search: search.trim() || undefined, limit: 50 })
  const first = user.name?.split(' ')[0]
  const canWrite = can(permissions.projectsWrite)
  const items = projects.data?.items ?? []
  // How each app stands: what its page would say (never a guessed default).
  // Archived apps need no read: they are archived.
  const ids = items.filter(p => p.status !== 'archived').map(p => p.id)
  const states = useAppStates(ids, can(permissions.deploymentsRead))
  const byId = new Map((states.data?.items ?? []).map(s => [s.app_id, s]))

  return (
    <>
      <PageHeader
        title={first ? `Hola, ${first}` : 'Inicio'}
        description={projects.data ? `${projects.data.page.total} ${projects.data.page.total === 1 ? 'app' : 'apps'}` : ' '}
        actions={
          <>
            <ButtonLink variant="outline" render={<Link to="/asistente" />}>
              <Bot />
              Conectar mi asistente
            </ButtonLink>
            {canWrite && (
              <Button onClick={() => setCreating(true)}>
                <Plus />
                Crear app
              </Button>
            )}
          </>
        }
      />

      <AttentionSection />

      <SectionHeader
        title="Apps"
        actions={
          <>
            <Input type="search" aria-label="Buscar apps" placeholder="Buscar por nombre…" value={search} onChange={e => setSearch(e.target.value)} className="w-56" />
            <ChipGroup label="Estado" value={status} onChange={setStatus} options={[{ value: 'active', label: 'Activas' }, { value: 'archived', label: 'Archivadas' }]} />
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
        <div className="grid gap-4 md:grid-cols-3" aria-busy>
          {[0, 1, 2].map(i => <Skeleton key={i} className="h-[300px] rounded-xl" />)}
        </div>
      ) : items.length === 0 && !search && status === 'active' ? (
        <EmptyState icon={AppWindow} title="Todavía no hay apps" action={canWrite && <Button onClick={() => setCreating(true)}><Plus />Crear tu primera app</Button>}>
          Crea una app y publícala con la cuenta de tu empresa. Sin servidores y sin configurar nada.
        </EmptyState>
      ) : items.length === 0 ? (
        <EmptyState icon={AppWindow} title="No hay apps que coincidan">Prueba con otro nombre o cambia el filtro.</EmptyState>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {items.map(p => {
            const archived = p.status === 'archived'
            const state = byId.get(p.id)
            // The last states read stay after a failed refresh (the notice says so); an app whose
            // state is not in yet (first load, or a new search still showing the previous one) waits.
            const st = cardStatus(state, archived)
            const waiting = !archived && !state && (states.isLoading || states.isPlaceholderData)
            return (
              <AppCard
                key={p.id}
                href={`/apps/${p.id}`}
                linkAs={Link}
                technical={technical}
                app={{
                  name: p.name,
                  description: p.description || (technical ? p.slug : undefined),
                  status: st?.status,
                  statusLabel: st?.label,
                  loading: waiting,
                  updated: formatRelative(p.updated_at),
                  host: archived ? undefined : cardHost(state),
                  audience: archived ? undefined : cardAudience(state),
                }}
              />
            )
          })}
          {canWrite && status === 'active' && <CreateAppTile onClick={() => setCreating(true)} />}
        </div>
      )}

      <CreateAppDialog open={creating} onOpenChange={setCreating} />
    </>
  )
}
