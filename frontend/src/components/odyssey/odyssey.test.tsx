import { useState } from 'react'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axe from 'axe-core'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { AlertTriangle } from 'lucide-react'
import { TooltipProvider } from '@/components/ui/tooltip'
import {
  AppCard,
  AppList,
  AppRow,
  AppShell,
  AppStatus,
  AttentionItem,
  AttentionList,
  Callout,
  ChipGroup,
  CodeLine,
  EmptyState,
  ErrorNotice,
  GitLine,
  Panel,
  PanelHeader,
  ProgressSteps,
  RequestTracker,
  requestSteps,
  ResourceIcons,
  ResourceRow,
  RuleRow,
  UsageMeter,
  UserMenu,
  VersionRow,
} from '.'
import { ApiError } from '@/lib/api'

const wrap = (ui: React.ReactNode) => render(<MemoryRouter><TooltipProvider>{ui}</TooltipProvider></MemoryRouter>)

async function expectAccessible(container: HTMLElement) {
  const { violations } = await axe.run(container, { rules: { 'color-contrast': { enabled: false }, region: { enabled: false } } })
  expect(violations.map(v => `${v.id}: ${v.nodes.map(n => n.target).join(', ')}`)).toEqual([])
}

describe('status vocabulary', () => {
  it('uses plain Spanish labels', () => {
    wrap(<><AppStatus status="live" /><AppStatus status="building" /><AppStatus status="attention" /></>)
    for (const t of ['En línea', 'Publicando…', 'Necesita atención']) expect(screen.getByText(t)).toBeInTheDocument()
  })
})

describe('AppCard claims only what it was told', () => {
  it('no audience row and no status when unknown', () => {
    wrap(<AppCard app={{ name: 'Visitas', change: 'Actualizada hace 2 horas' }} />)
    expect(screen.queryByLabelText('Quién puede abrirla')).not.toBeInTheDocument()
    expect(screen.queryByText('En pausa')).not.toBeInTheDocument()
    expect(screen.queryByText('Solo tu equipo')).not.toBeInTheDocument()
  })
  it('shows the address and the audience it was given', () => {
    wrap(<AppCard app={{ name: 'Visitas', status: 'live', change: 'Publicaste hace 2 horas', url: 'https://visitas.apps.acme.com/', audience: 'Toda la empresa' }} />)
    expect(screen.getByText('visitas.apps.acme.com')).toBeInTheDocument()
    expect(screen.getByText('Toda la empresa')).toBeInTheDocument()
    expect(screen.getByText('En línea')).toBeInTheDocument()
  })
  it('draws no fake preview of the app', () => {
    const { container } = wrap(<AppCard app={{ name: 'Visitas', status: 'live', change: 'x' }} />)
    expect(container.querySelector('[class*="aspect-"]')).toBeNull()
  })
})

describe('AppRow (UX v4 §5.2, §5.10)', () => {
  const row = (app: Partial<React.ComponentProps<typeof AppRow>['app']>) =>
    wrap(<AppList><AppRow href="/apps/a1" app={{ name: 'Visitas', change: 'Ana Torres publicó hace 2 horas', ...app }} /></AppList>)
  it('two lines: name, state and address; who, through what and who can open it', () => {
    row({ status: 'live', url: 'https://visitas.apps.acme.com/', channel: { kind: 'assistant', label: 'Claude Code' }, audience: 'Toda la empresa' })
    expect(screen.getByRole('list', { name: 'Apps' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Visitas' })).toHaveAttribute('href', '/apps/a1')
    expect(screen.getByText('En línea')).toBeInTheDocument()
    expect(screen.getByText('visitas.apps.acme.com')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Copiar la dirección' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Abrir la app' })).toHaveAttribute('href', 'https://visitas.apps.acme.com/')
    expect(screen.getByText('Ana Torres publicó hace 2 horas')).toBeInTheDocument()
    expect(screen.getByRole('img', { name: 'Canal: Claude Code' })).toBeInTheDocument()
    expect(screen.getByText('Toda la empresa')).toBeInTheDocument()
  })
  it('claims nothing it was not told', () => {
    row({})
    expect(screen.queryByRole('button', { name: 'Copiar la dirección' })).toBeNull()
    expect(screen.queryByLabelText('Quién puede abrirla')).toBeNull()
    expect(screen.queryByRole('img', { name: /^Canal/ })).toBeNull()
    expect(screen.queryByText('En línea')).toBeNull()
  })
  it('is accessible, with one main link per row', async () => {
    const { container } = row({ status: 'live', url: 'https://visitas.apps.acme.com/', audience: 'Ventas' })
    await expectAccessible(container)
    expect(container.querySelectorAll('a[data-row-link]')).toHaveLength(1)
  })
})

describe('AppShell', () => {
  it('hides a bar with a single tab', () => {
    const { rerender } = wrap(<AppShell team="t" user="u" tabs={[{ to: '/apps/x', label: 'Resumen', end: true }]}>x</AppShell>)
    expect(screen.queryByRole('link', { name: 'Resumen' })).not.toBeInTheDocument()
    rerender(<MemoryRouter><TooltipProvider><AppShell team="t" user="u" tabs={[{ to: '/', label: 'Inicio', end: true }, { to: '/asistente', label: 'Asistente' }]}>x</AppShell></TooltipProvider></MemoryRouter>)
    expect(screen.getByRole('link', { name: 'Asistente' })).toBeInTheDocument()
  })
})

describe('UsageMeter', () => {
  it('turns warning at 85%', () => {
    wrap(<><UsageMeter value={84} label="a" /><UsageMeter value={85} label="b" /></>)
    expect(screen.getByRole('meter', { name: 'a' }).firstElementChild).toHaveClass('bg-success')
    expect(screen.getByRole('meter', { name: 'b' }).firstElementChild).toHaveClass('bg-warning')
  })
  it('clamps', () => {
    wrap(<UsageMeter value={140} />)
    expect(screen.getByRole('meter')).toHaveAttribute('aria-valuenow', '100')
  })
})

describe('RequestTracker', () => {
  it('marks the current step', () => {
    wrap(<RequestTracker steps={requestSteps('Carlos (TI)')} current={1} />)
    expect(screen.getByText('En revisión · Carlos (TI)').closest('li')).toHaveAttribute('aria-current', 'step')
  })
})

describe('technical mode only adds detail', () => {
  const app = { name: 'Visitas', status: 'live' as const, change: 'hace 2 h', audience: 'Ventas', git: { sha: 'a1b2c3d4e5', message: 'fotos' } }
  it('hides git info for builders', () => {
    wrap(<AppCard app={app} />)
    expect(screen.queryByText('a1b2c3d')).not.toBeInTheDocument()
    expect(screen.getByText('Visitas')).toBeInTheDocument()
  })
  it('shows git info in technical mode', () => {
    wrap(<AppCard app={app} technical />)
    expect(screen.getByText('a1b2c3d')).toBeInTheDocument()
    expect(screen.getByText('Visitas')).toBeInTheDocument()
  })
  it('resource rows add the engine only when technical', () => {
    const { rerender } = wrap(<ul><ResourceRow resource={{ kind: 'postgres', detail: '1 GB' }} /></ul>)
    expect(screen.queryByText(/Postgres 17/)).not.toBeInTheDocument()
    rerender(<MemoryRouter><TooltipProvider><ul><ResourceRow technical resource={{ kind: 'postgres', detail: '1 GB' }} /></ul></TooltipProvider></MemoryRouter>)
    expect(screen.getByText(/Postgres 17/)).toBeInTheDocument()
  })
})

describe('interactions', () => {
  it('ChipGroup is a radiogroup', async () => {
    function Chips() {
      const [v, setV] = useState('all')
      return <ChipGroup label="Filtro" value={v} onChange={setV} options={[{ value: 'all', label: 'Todas' }, { value: 'mine', label: 'Mías' }]} />
    }
    wrap(<Chips />)
    await userEvent.click(screen.getByRole('radio', { name: 'Mías' }))
    expect(screen.getByRole('radio', { name: 'Mías' })).toHaveAttribute('aria-checked', 'true')
  })

  it('VersionRow offers restore only for previous versions', async () => {
    const onRestore = vi.fn()
    wrap(<ul>
      <VersionRow version={{ id: '1', kind: 'live', message: 'a', when: 'x', by: 'Ana' }} onRestore={onRestore} />
      <VersionRow version={{ id: '2', kind: 'previous', message: 'b', when: 'x', by: 'Ana' }} onRestore={onRestore} />
    </ul>)
    const buttons = screen.getAllByRole('button', { name: /Volver a esta/ })
    expect(buttons).toHaveLength(1)
    await userEvent.click(buttons[0])
    expect(onRestore).toHaveBeenCalledOnce()
  })

  it('GitLine shows branch, short sha, title and author, and each part is optional', () => {
    const { container, rerender } = render(<GitLine branch="main" sha="a1b2c3d4e5f6" message="Agrega fotos" author="ana-ruiz" />)
    expect(container).toHaveTextContent('main')
    expect(container).toHaveTextContent('a1b2c3d')
    expect(container).not.toHaveTextContent('a1b2c3d4')
    expect(container).toHaveTextContent('Agrega fotos')
    expect(container).toHaveTextContent('ana-ruiz')
    rerender(<GitLine message="Agrega fotos" author="ana-ruiz" />)
    expect(container).not.toHaveTextContent('main')
    expect(container.querySelector('.font-mono')).toBeNull()
  })

  it('VersionRow from GitHub hides branch and sha unless technical', () => {
    const git = { branch: 'main', sha: 'a1b2c3d4e5', author: 'ana-ruiz' }
    const row = (technical: boolean) => <ul><VersionRow technical={technical} version={{ id: '1', kind: 'live', message: 'Agrega fotos', when: 'hace 2 h', by: 'ti', git }} /></ul>
    const { container, rerender } = render(row(false))
    expect(container).toHaveTextContent('ana-ruiz')
    expect(container).not.toHaveTextContent('a1b2c3d')
    rerender(row(true))
    expect(container).toHaveTextContent('a1b2c3d')
    expect(container).toHaveTextContent('main')
  })

  it('VersionRow says who and through what, the channel with icon, text and a label', () => {
    const git = { branch: 'main', sha: 'a1b2c3d4e5', author: 'ana-ruiz' }
    const { container } = render(<ul>
      <VersionRow version={{ id: '1', kind: 'live', message: 'a', when: 'hace 2 h', by: 'Ana Torres', channel: { kind: 'assistant', label: 'Claude Code' } }} />
      <VersionRow version={{ id: '2', kind: 'previous', message: 'b', when: 'ayer', by: 'tu equipo', channel: { kind: 'github', label: 'GitHub' }, git }} />
      <VersionRow version={{ id: '3', kind: 'previous', message: 'c', when: 'el lunes', by: 'ti', channel: { kind: 'unrecorded', label: 'Canal no registrado' } }} />
    </ul>)
    expect(container).toHaveTextContent('hace 2 h · por Ana Torres')
    expect(container).toHaveTextContent('ayer· por tu equipo')
    const chips = screen.getAllByRole('img', { name: /^Canal: / })
    expect(chips.map(c => c.getAttribute('aria-label'))).toEqual(['Canal: Claude Code', 'Canal: GitHub', 'Canal: Canal no registrado'])
    chips.forEach(c => expect(c.querySelector('svg')).not.toBeNull())
  })

  it('AppCard names who changed it last only when told', () => {
    const { container, rerender } = wrap(<AppCard app={{ name: 'Visitas', status: 'live', change: 'Ana Torres publicó hace 2 h', channel: { kind: 'console', label: 'Consola' } }} />)
    expect(container).toHaveTextContent('Ana Torres publicó hace 2 h')
    expect(screen.getByRole('img', { name: 'Canal: Consola' })).toBeInTheDocument()
    rerender(<MemoryRouter><TooltipProvider><AppCard app={{ name: 'Visitas', status: 'live', change: 'Actualizada hace 2 h' }} /></TooltipProvider></MemoryRouter>)
    expect(container).not.toHaveTextContent('Ana')
    expect(screen.queryByRole('img', { name: /^Canal/ })).toBeNull()
  })
  it('locked rules cannot be toggled', () => {
    wrap(<RuleRow checked locked>Las apps <b>públicas</b> necesitan Seguridad</RuleRow>)
    expect(screen.getByRole('switch')).toHaveAttribute('aria-disabled', 'true')
  })

  it('CodeLine copies', async () => {
    const user = userEvent.setup()
    wrap(<CodeLine code="claude mcp add odyssey" />)
    await user.click(screen.getByRole('button', { name: 'Copiar' }))
    expect(await navigator.clipboard.readText()).toBe('claude mcp add odyssey')
    expect(screen.getByRole('button', { name: 'Copiado' })).toBeInTheDocument()
  })
})

describe('errors', () => {
  it('ErrorNotice renders the human message', () => {
    wrap(<ErrorNotice error={new ApiError('x', 422, 'BUSINESS', 'PROJECT_ARCHIVED')} />)
    expect(screen.getByRole('alert')).toBeInTheDocument()
  })
  it('AttentionList hides when empty', () => {
    const { container } = wrap(<AttentionList count={0}>{null}</AttentionList>)
    expect(container).toBeEmptyDOMElement()
  })
})

describe('accessibility (axe)', () => {
  it('composites have no violations', async () => {
    const { container } = wrap(
      <main>
        <AttentionList count={1}>
          <AttentionItem tone="bad" icon={AlertTriangle} title="No se publicó" action={<button>Arreglar</button>}>La app sigue funcionando</AttentionItem>
        </AttentionList>
        <Callout tone="warn" title="Casi lleno.">9 de 10 GB</Callout>
        <ProgressSteps steps={[{ icon: AlertTriangle, title: 'Preparando', description: 'x', state: 'running' }]} />
        <ul><ResourceRow resource={{ kind: 'postgres', detail: '9 GB', usage: 90 }} onOpen={() => {}} /></ul>
        <RuleRow checked onCheckedChange={() => {}}>Vencen a los <b>6 meses</b></RuleRow>
        <CodeLine code="npm i" />
      </main>,
    )
    await expectAccessible(container)
  })

  it('page furniture: user menu, resource icons, panel and 404 headings', async () => {
    const { container } = wrap(
      <main>
        <UserMenu name="Demo TI" technical={false} onTechnicalChange={() => {}} onLogout={() => {}} />
        <ResourceIcons resources={[{ kind: 'postgres', detail: '' }, { kind: 'ai', detail: '', usage: 95 }]} />
        <EmptyState level={1} icon={AlertTriangle} title="No encontramos esta página" />
        <Panel><PanelHeader title="Versiones" /></Panel>
      </main>,
    )
    await expectAccessible(container)
    // Named by sr-only text, not aria-label: an aria-label would contradict the visible initials.
    expect(screen.getByRole('button', { name: 'Cuenta de Demo TI' })).not.toHaveAttribute('aria-label')
    expect(screen.getByRole('heading', { level: 1, name: 'No encontramos esta página' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 2, name: 'Versiones' })).toBeInTheDocument()
    expect(screen.getByRole('img', { name: 'Base de datos' })).toBeInTheDocument()
  })
})
