import { Outlet, useMatch, useNavigate } from 'react-router-dom'
import { Bot } from 'lucide-react'
import { AppShell, CommandPalette, Crumb, NotificationsMenu, UserMenu, useCoreCommands, type NavTab } from '@/components/odyssey'
import { useAuth } from '@/lib/auth'
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
  const commands = [
    ...useCoreCommands(),
    ...(apps.data?.items ?? []).map(p => ({ id: p.id, label: p.name, group: 'Apps', onSelect: () => navigate(`/apps/${p.id}`) })),
  ]

  const tabs: NavTab[] = appId
    ? [{ to: `/apps/${appId}`, label: 'Resumen', end: true }]
    : [{ to: '/', label: 'Inicio', end: true }, { to: '/asistente', label: 'Asistente' }]

  return (
    <AppShell
      team={<Crumb to="/">{auth.organization ? 'Mi equipo' : 'Odyssey'}</Crumb>}
      project={appId && <Crumb to={`/apps/${appId}`}>{project.data?.name ?? '…'}</Crumb>}
      tabs={tabs}
      search={<CommandPalette entries={commands} />}
      notifications={<NotificationsMenu items={[{ id: 'welcome', tone: 'ai', icon: Bot, title: 'Conecta tu asistente para publicar desde Claude Code o Codex', when: 'Sugerencia' }]} />}
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
