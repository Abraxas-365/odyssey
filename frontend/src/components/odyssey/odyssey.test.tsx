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
  AppStatus,
  AttentionItem,
  AttentionList,
  Callout,
  ChipGroup,
  CodeLine,
  ErrorNotice,
  ProgressSteps,
  RequestTracker,
  requestSteps,
  ResourceRow,
  RuleRow,
  UsageMeter,
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
  const app = { name: 'Visitas', status: 'live' as const, updated: 'hace 2 h', audience: 'Ventas', git: { sha: 'a1b2c3d4e5', message: 'fotos' } }
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
})
