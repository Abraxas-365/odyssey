import { Outlet, useMatch, useNavigate } from 'react-router-dom'
import { Bot, ShieldCheck } from 'lucide-react'
import { AppShell, CommandPalette, Crumb, NotificationsMenu, UserMenu, useCoreCommands, type NavTab } from '@/components/odyssey'
import { requesterName } from '@/features/approvals/approvals'
import { inboxFilter, useApprovals } from '@/lib/approvals'
import { formatRelative } from '@/lib/format'
import { useAuth } from '@/lib/auth'
import { permissions } from '@/lib/permissions'
import { PreferencesProvider, usePreferences } from '@/lib/preferences'
import { useProject, useProjects } from '@/lib/projects'

export function ConsoleLayout() {
  const { user } = useAuth()
  return (
    <PreferencesProvider user={user.id}>
      <Chrome />
    </PreferencesProvider>
  )
}

/**
 * Tab sets follow the mockup (UX v3 §2/§8). Only routes that exist are shown;
 * the rest (Datos, Actividad, Equipo, Versiones…) appear with their APIs.
 */
function Chrome() {
  const auth = useAuth()
  const { technical, setTechnical } = usePreferences()
  const appMatch = useMatch('/apps/:id/*')
  const appId = appMatch?.params.id
  const project = useProject(appId)
  const apps = useProjects({ status: 'active', limit: 20 })
  const navigate = useNavigate()
  // The bell counts what this person may decide; people who cannot decide never ask (it would only be empty).
  const decider = auth.can(permissions.approvalsDecide)
  const inbox = useApprovals(inboxFilter, decider)
  const pendingCount = decider ? (inbox.data?.page.total ?? 0) : 0
  const waiting = decider ? (inbox.data?.items ?? []) : []
  const commands = [
    ...useCoreCommands(),
    ...(auth.can(permissions.clustersRead) ? [{ id: 'clusters', label: 'Clústeres', group: 'Infraestructura', onSelect: () => navigate('/infraestructura/clusteres') }] : []),
    ...(auth.can(permissions.inventoryRead) ? [{ id: 'inventory', label: 'Inventario', group: 'Infraestructura', onSelect: () => navigate('/inventario') }] : []),
    ...(apps.data?.items ?? []).map(p => ({ id: p.id, label: p.name, group: 'Apps', onSelect: () => navigate(`/apps/${p.id}`) })),
  ]

  const tabs: NavTab[] = appId
    ? [{ to: `/apps/${appId}`, label: 'Resumen', end: true }]
    : [
        { to: '/', label: 'Inicio', end: true },
        { to: '/asistente', label: 'Asistente' },
        { to: '/bandeja', label: 'Bandeja', count: decider ? pendingCount : undefined },
        ...(auth.can(permissions.inventoryRead) ? [{ to: '/inventario', label: 'Inventario' }] : []),
        ...(auth.can(permissions.clustersRead) ? [{ to: '/infraestructura/clusteres', label: 'Infraestructura' }] : []),
        ...(auth.can(permissions.integrationsManage) ? [{ to: '/configuracion/github', label: 'Configuración' }] : []),
        ...(auth.can(permissions.policyManage) ? [{ to: '/configuracion/reglas', label: 'Reglas de TI' }] : []),
      ]

  return (
    <AppShell
      team={<Crumb to="/">{auth.organization ? 'Mi equipo' : 'Odicea'}</Crumb>}
      project={appId && <Crumb to={`/apps/${appId}`}>{project.data?.name ?? '…'}</Crumb>}
      tabs={tabs}
      search={<CommandPalette entries={commands} />}
      notifications={
        <NotificationsMenu
          count={pendingCount}
          items={[
            ...waiting.map(a => ({ id: a.id, tone: 'info' as const, icon: ShieldCheck, title: `${requesterName(a)} pidió: ${a.summary}`, when: formatRelative(a.created_at), unread: true, onSelect: () => navigate('/bandeja') })),
            { id: 'welcome', tone: 'ai', icon: Bot, title: 'Conecta tu asistente para publicar desde Claude Code o Codex', when: 'Sugerencia' },
          ]}
          onSeeAll={decider ? () => navigate('/bandeja') : undefined}
          seeAllLabel="Abrir la Bandeja"
        />
      }
      user={
        <UserMenu
          name={auth.user.name ?? auth.user.email ?? 'Mi cuenta'}
          email={auth.user.email}
          technical={technical}
          onTechnicalChange={setTechnical}
          onSwitchOrganization={auth.switchOrganization}
          onLogout={auth.logout}
        />
      }
    >
      <Outlet />
    </AppShell>
  )
}
