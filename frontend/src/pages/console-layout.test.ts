import { describe, expect, it } from 'vitest'
import { permissions, type Permission } from '@/lib/permissions'
import { adminSections } from './admin-layout'
import { consoleTabs } from './console-layout'

const role = (...held: Permission[]) => (p: Permission) => held.includes(p)
const labels = (tabs: { label: string }[]) => tabs.map(t => t.label)

describe('consoleTabs (UX v4 §6.1)', () => {
  it('a reader or a builder sees everyday work only', () => {
    const lector = role(permissions.projectsRead, permissions.deploymentsRead)
    expect(labels(consoleTabs(lector))).toEqual(['Inicio', 'Bandeja'])
    const builder = role(permissions.projectsRead, permissions.projectsWrite, permissions.deploymentsCreate)
    expect(labels(consoleTabs(builder))).toEqual(['Inicio', 'Bandeja'])
  })

  it('IT gets Inventario, Registro and one Administración tab with its sections', () => {
    const ti = role(...Object.values(permissions))
    expect(labels(consoleTabs(ti, 3))).toEqual(['Inicio', 'Bandeja', 'Inventario', 'Registro', 'Administración'])
    expect(consoleTabs(ti, 3)[1].count).toBe(3)
    expect(labels(adminSections(ti))).toEqual(['Clústeres', 'GitHub', 'Reglas'])
  })

  it('Administración shows only the sections one may open', () => {
    const integrations = role(permissions.integrationsManage)
    expect(labels(adminSections(integrations))).toEqual(['GitHub'])
    expect(labels(consoleTabs(integrations))).toContain('Administración')
  })
})
