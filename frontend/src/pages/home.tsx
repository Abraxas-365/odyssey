import { useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { AppWindow, Bot, Plus } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { AppCard, ChipGroup, CreateAppTile, EmptyState, ErrorNotice, PageHeader, SectionHeader } from '@/components/odyssey'
import { Skeleton } from '@/components/ui/skeleton'
import { useAuth } from '@/lib/auth'
import { formatRelative } from '@/lib/format'
import { permissions } from '@/lib/permissions'
import { usePreferences } from '@/lib/preferences'
import { useProjects } from '@/lib/projects'
import type { ProjectStatus } from '@/lib/types'
import { CreateAppDialog } from '@/features/apps/create-app-dialog'

/**
 * Inicio. UX v3 §3 puts "Necesita tu atención" on top; it is wired when the
 * API exposes builds/requests. Today it lists apps (projects).
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

  return (
    <>
      <PageHeader
        title={first ? `Hola, ${first}` : 'Inicio'}
        description={projects.data ? `${projects.data.page.total} ${projects.data.page.total === 1 ? 'app' : 'apps'}` : ' '}
        actions={
          <>
            <Button variant="outline" nativeButton={false} render={<Link to="/asistente" />}>
              <Bot />
              Conectar mi asistente
            </Button>
            {canWrite && (
              <Button onClick={() => setCreating(true)}>
                <Plus />
                Crear app
              </Button>
            )}
          </>
        }
      />

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
          {items.map(p => (
            <AppCard
              key={p.id}
              href={`/apps/${p.id}`}
              linkAs={Link}
              technical={technical}
              app={{
                name: p.name,
                description: p.description || (technical ? p.slug : undefined),
                // TODO(api): real status once deployments exist.
                status: p.status === 'archived' ? 'archived' : 'paused',
                statusLabel: p.status === 'archived' ? undefined : 'Sin publicar',
                updated: formatRelative(p.updated_at),
                audience: 'Solo tu equipo',
              }}
            />
          ))}
          {canWrite && status === 'active' && <CreateAppTile onClick={() => setCreating(true)} />}
        </div>
      )}

      <CreateAppDialog open={creating} onOpenChange={setCreating} />
    </>
  )
}
