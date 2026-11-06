import { Navigate, Outlet } from 'react-router-dom'
import { NavTabs, type NavTab } from '@/components/odyssey'
import { useAuth } from '@/lib/auth'
import { permissions, type Permission } from '@/lib/permissions'

/**
 * Administración (UX v4 §6.1): what IT sets up once — clusters, GitHub, the
 * rules — out of the everyday tabs. Each section shows only to who can use
 * it; the tab itself only when at least one does.
 */
const sections: (NavTab & { permission: Permission })[] = [
  { to: '/administracion/clusteres', label: 'Clústeres', permission: permissions.clustersRead },
  { to: '/administracion/github', label: 'GitHub', permission: permissions.integrationsManage },
  { to: '/administracion/reglas', label: 'Reglas', permission: permissions.policyManage },
]

/** The sections this person may open, in order. */
export function adminSections(can: (p: Permission) => boolean): NavTab[] {
  return sections.filter(s => can(s.permission)).map(({ to, label }) => ({ to, label }))
}

export function AdminLayout() {
  const { can } = useAuth()
  const items = adminSections(can)
  return (
    <>
      {items.length > 1 && (
        <div className="-mt-4 border-b">
          <NavTabs tabs={items} label="Administración" />
        </div>
      )}
      <Outlet />
    </>
  )
}

/** /administracion opens its first section (or Inicio, for who has none). */
export function AdminIndex() {
  const { can } = useAuth()
  return <Navigate to={adminSections(can)[0]?.to ?? '/'} replace />
}
