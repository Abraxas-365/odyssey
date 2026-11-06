import { Outlet, useMatch, useNavigate } from 'react-router-dom'
import { ShieldCheck, Sparkles } from 'lucide-react'
import { AssistantChip, CommandPalette, Crumb, AppShell, NotificationsMenu, UserMenu, useCoreCommands, type NavTab } from '@/components/odyssey'
import { DropdownMenuItem, DropdownMenuSeparator } from '@/components/ui/dropdown-menu'
import { requesterName } from '@/features/approvals/approvals'
import { assistantState, chipKind, chipLabel, offered } from '@/features/assistant/state'
import { inboxFilter, useApprovals } from '@/lib/approvals'
import { useAssistant } from '@/lib/assistant'
import { formatRelative } from '@/lib/format'
import { useAuth } from '@/lib/auth'
import { knownMe, useMe } from '@/lib/me'
import { permissions } from '@/lib/permissions'
import { PreferencesProvider, usePreferences } from '@/lib/preferences'
import { useProject, useProjects } from '@/lib/projects'
import { adminSections } from './admin-layout'

/**
 * An app's tabs (UX v4 §5.1): Resumen · Historial · Compartir ·
 * Configuración. Who cannot read deployments has no history nor sharing to
 * see: Resumen and Configuración only.
 */
export function appTabs(id: string, canRead: boolean): NavTab[] {
  const base = `/apps/${id}`
  const resumen = { to: base, label: 'Resumen', end: true }
  if (!canRead) return [resumen, { to: `${base}/configuracion`, label: 'Configuración' }]
  return [resumen, { to: `${base}/historial`, label: 'Historial' }, { to: `${base}/compartir`, label: 'Compartir' }, { to: `${base}/configuracion`, label: 'Configuración' }]
}

/**
 * The console's tabs (UX v4 §6.1): everyday work — Inicio, Bandeja,
 * Inventario for who reads it — and Administración (clusters, GitHub,
 * rules) only for who can open one of its sections. The assistant is not a
 * tab: it is the header chip.
 */
export function consoleTabs(can: Parameters<typeof adminSections>[0], pending?: number): NavTab[] {
  return [
    { to: '/', label: 'Inicio', end: true },
    { to: '/bandeja', label: 'Bandeja', count: pending },
    ...(can(permissions.inventoryRead) ? [{ to: '/inventario', label: 'Inventario' }] : []),
    ...(adminSections(can).length > 0 ? [{ to: '/administracion', label: 'Administración' }] : []),
  ]
}

export function ConsoleLayout() {
  const { user } = useAuth()
  return (
    <PreferencesProvider user={user.id}>
      <Chrome />
    </PreferencesProvider>
  )
}

function Chrome() {
  const auth = useAuth()
  const { technical, setTechnical } = usePreferences()
  const appMatch = useMatch('/apps/:id/*')
  const appId = appMatch?.params.id
  const project = useProject(appId)
  const apps = useProjects({ status: 'active', limit: 20 })
  const me = useMe()
  const assistant = useAssistant()
  const navigate = useNavigate()
  // The bell counts what this person may decide; people who cannot decide never ask (it would only be empty).
  const decider = auth.can(permissions.approvalsDecide)
  const inbox = useApprovals(inboxFilter, decider)
  const pendingCount = decider ? (inbox.data?.page.total ?? 0) : 0
  const waiting = decider ? (inbox.data?.items ?? []) : []
  const commands = [
    ...useCoreCommands(),
    ...(auth.can(permissions.inventoryRead) ? [{ id: 'inventory', label: 'Inventario', group: 'Ir a', onSelect: () => navigate('/inventario') }] : []),
    ...adminSections(auth.can).map(s => ({ id: s.to, label: s.label, group: 'Administración', onSelect: () => navigate(s.to) })),
    ...(apps.data?.items ?? []).map(p => ({ id: p.id, label: p.name, group: 'Apps', onSelect: () => navigate(`/apps/${p.id}`) })),
  ]

  const tabs: NavTab[] = appId ? appTabs(appId, auth.can(permissions.deploymentsRead)) : consoleTabs(auth.can, decider ? pendingCount : undefined)
  // Nothing to say while GET /assistant has not answered: the chip appears once it is known whether one is offered.
  const state = assistantState(knownMe(me), offered(assistant.data))
  const chip = assistant.data ? chipLabel(state) : undefined

  return (
    <AppShell
      team={<Crumb to="/">{auth.organization ? 'Mi equipo' : 'Odicea'}</Crumb>}
      project={appId && <Crumb to={`/apps/${appId}`}>{project.data?.name ?? '…'}</Crumb>}
      tabs={tabs}
      search={<CommandPalette entries={commands} />}
      assistant={chip && <AssistantChip label={chip} kind={chipKind(state)} />}
      notifications={
        <NotificationsMenu
          count={pendingCount}
          items={waiting.map(a => ({ id: a.id, tone: 'info' as const, icon: ShieldCheck, title: `${requesterName(a)} pidió: ${a.summary}`, when: formatRelative(a.created_at), unread: true, onSelect: () => navigate('/bandeja') }))}
          onSeeAll={decider ? () => navigate('/bandeja') : undefined}
          seeAllLabel="Abrir la Bandeja"
        />
      }
      user={
        <UserMenu
          name={auth.user.name ?? auth.user.email ?? 'Mi cuenta'}
          email={auth.user.email}
          role={me.data?.role || undefined}
          technical={technical}
          onTechnicalChange={setTechnical}
          onSwitchOrganization={auth.switchOrganization}
          onLogout={auth.logout}
        >
          {state.kind !== 'off' && assistant.data && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={() => navigate('/asistente')}><Sparkles />Tu asistente</DropdownMenuItem>
            </>
          )}
        </UserMenu>
      }
    >
      <Outlet />
    </AppShell>
  )
}
