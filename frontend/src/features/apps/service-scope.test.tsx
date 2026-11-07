import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { EnvironmentSettings, ResourceList } from '@/lib/types'

const api = vi.fn()
vi.mock('@/lib/api', async orig => ({
  ...(await orig<typeof import('@/lib/api')>()),
  api: (...a: unknown[]) => api(...a),
}))
vi.mock('@/lib/auth', () => ({ useAuth: () => ({ can: () => true }) }))

import { ResourcesPanel } from './resources-panel'
import { ScopeBadge, ScopePicker } from './service-scope'
import { SecretsPanel, VariablesPanel } from './variables-panel'

const web = { name: 'web', kind: 'web' as const }
const apiSvc = { name: 'api', kind: 'web' as const }
const jobs = { name: 'jobs', kind: 'worker' as const }

function settings(over: Partial<EnvironmentSettings> = {}): EnvironmentSettings {
  return {
    revision: 3,
    access: { mode: 'public' },
    env: { TZ: 'UTC', LOG: 'info' },
    service_env: { api: { LOG: 'debug' } },
    resources: [],
    secrets: [],
    ai: { services: [] },
    secret_key: { cluster_id: 'c', app: 'a', key_id: 'k', public_key: 'AA==' },
    external_secrets: false,
    secret_stores: [],
    secrets_cluster_revoked: false,
    access_login_available: true,
    access_protection_available: true,
    scoped_settings: true,
    ...over,
  } as unknown as EnvironmentSettings
}

/** Routes the mocked API by path; `parts` is the app's services. */
function serve(parts: unknown[], s: EnvironmentSettings, resources: ResourceList = { items: [], available_kinds: ['postgres', 'files'] }) {
  api.mockImplementation((path: string) => {
    if (path === '/projects/p/services') return Promise.resolve({ items: parts })
    if (path.endsWith('/settings')) return Promise.resolve(s)
    if (path.endsWith('/resources')) return Promise.resolve(resources)
    if (path.endsWith('/ai')) return Promise.resolve({ status: 'off', available: true, enabled: false, services: [], scoped_settings: true, variables: [], base_url: '' })
    return Promise.resolve({ settings: s, deployment: null, deployment_id: null })
  })
}

function mount(ui: React.ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}><MemoryRouter>{ui}</MemoryRouter></QueryClientProvider>)
}
const writes = (method: string) => api.mock.calls.filter(c => (c[1] as { method?: string } | undefined)?.method === method)

describe('ScopePicker (MS-6b)', () => {
  it('draws nothing for an app of one part', () => {
    const { container } = render(<ScopePicker value={[]} onChange={() => {}} services={[web]} scoped />)
    expect(container.innerHTML).toBe('')
  })

  it('offers the whole app or some parts, and reports the choice', async () => {
    const onChange = vi.fn()
    render(<ScopePicker value={[]} onChange={onChange} services={[web, apiSvc, jobs]} scoped />)
    expect(screen.getByRole('radio', { name: 'Toda la app' }).getAttribute('aria-checked')).toBe('true')
    expect(screen.queryByRole('checkbox')).toBeNull()
    await userEvent.click(screen.getByRole('radio', { name: 'Solo algunas partes' }))
    expect(onChange).toHaveBeenCalledWith(['web'])
  })

  it('lists each part with a checkbox once some are chosen, keeping at least one', async () => {
    const onChange = vi.fn()
    render(<ScopePicker value={['api']} onChange={onChange} services={[web, apiSvc, jobs]} scoped />)
    expect(screen.getAllByRole('checkbox')).toHaveLength(3)
    await userEvent.click(screen.getByRole('checkbox', { name: 'jobs' }))
    expect(onChange).toHaveBeenCalledWith(['api', 'jobs'])
    // the only one ticked cannot be unticked: "Toda la app" is the way back
    expect(screen.getByRole('checkbox', { name: 'api' }).hasAttribute('disabled') || screen.getByRole('checkbox', { name: 'api' }).getAttribute('aria-disabled') === 'true').toBe(true)
  })

  it('says a cluster that cannot scope gives it to the whole app only', () => {
    render(<ScopePicker value={[]} onChange={() => {}} services={[web, apiSvc]} scoped={false} />)
    expect(screen.queryByRole('radio')).toBeNull()
    expect(screen.getByText(/solo puede dárselo a toda la app/)).toBeTruthy()
  })

  it('a files volume picks exactly one part', async () => {
    const onChange = vi.fn()
    render(<ScopePicker value={[]} onChange={onChange} services={[web, apiSvc]} scoped exactlyOne />)
    expect(screen.queryByRole('radio', { name: 'Toda la app' })).toBeNull()
    expect(screen.getByText(/solo una puede/)).toBeTruthy()
    await userEvent.click(screen.getByRole('checkbox', { name: 'api' }))
    expect(onChange).toHaveBeenCalledWith(['api'])
  })

  it('the badge shows only for a scoped row of an app of several parts', () => {
    const a = render(<ScopeBadge scope={['api', 'jobs']} services={[web, apiSvc, jobs]} />)
    expect(a.container.textContent).toBe('Solo: api, jobs')
    expect(render(<ScopeBadge scope={[]} services={[web, apiSvc]} />).container.innerHTML).toBe('')
    expect(render(<ScopeBadge scope={['web']} services={[web]} />).container.innerHTML).toBe('')
  })
})

describe('VariablesPanel by part (MS-6b)', () => {
  beforeEach(() => { api.mockReset() })

  it('an app of one part has no layer selector and sends no services', async () => {
    serve([web], settings())
    mount(<VariablesPanel project="p" env="e" canWrite busy={false} />)
    await screen.findByDisplayValue('UTC')
    expect(screen.queryByRole('radio', { name: 'Toda la app' })).toBeNull()
    fireEvent.change(screen.getByLabelText('Valor de TZ'), { target: { value: 'UTC-5' } })
    await userEvent.click(screen.getByRole('button', { name: 'Guardar' }))
    await waitFor(() => expect(writes('PATCH')).toHaveLength(1))
    expect(writes('PATCH')[0][1]).toMatchObject({ body: { set: { TZ: 'UTC-5' }, unset: [] } })
    expect('services' in (writes('PATCH')[0][1] as { body: object }).body).toBe(false)
  })

  it("edits one part's own layer and sends services", async () => {
    serve([web, apiSvc, jobs], settings())
    mount(<VariablesPanel project="p" env="e" canWrite busy={false} />)
    await screen.findByDisplayValue('UTC')
    // the whole app's list notes the part that overrides LOG
    expect(screen.getByText(/api tiene su propio valor/)).toBeTruthy()
    await userEvent.click(screen.getByRole('radio', { name: 'api' }))
    expect(await screen.findByDisplayValue('debug')).toBeTruthy()
    expect(screen.queryByDisplayValue('UTC')).toBeNull()
    fireEvent.change(screen.getByLabelText('Valor de LOG'), { target: { value: 'trace' } })
    await userEvent.click(screen.getByRole('button', { name: 'Guardar' }))
    await waitFor(() => expect(writes('PATCH')).toHaveLength(1))
    expect(writes('PATCH')[0][0]).toBe('/projects/p/environments/e/env')
    expect(writes('PATCH')[0][1]).toMatchObject({ body: { set: { LOG: 'trace' }, unset: [], services: ['api'] } })
  })

  it('shows what the API warns about after saving', async () => {
    serve([web, apiSvc], settings())
    api.mockImplementation((path: string, init?: { method?: string }) => {
      if (init?.method === 'PATCH') return Promise.resolve({ settings: settings(), deployment: null, warnings: ['LOG de api ahora gana sobre el de toda la app.'] })
      if (path === '/projects/p/services') return Promise.resolve({ items: [web, apiSvc] })
      return Promise.resolve(settings())
    })
    mount(<VariablesPanel project="p" env="e" canWrite busy={false} />)
    await screen.findByDisplayValue('UTC')
    fireEvent.change(screen.getByLabelText('Valor de TZ'), { target: { value: 'x' } })
    await userEvent.click(screen.getByRole('button', { name: 'Guardar' }))
    expect(await screen.findByText(/ahora gana sobre/)).toBeTruthy()
  })

  it('a cluster that cannot scope keeps the whole app only', async () => {
    serve([web, apiSvc], settings({ scoped_settings: false }))
    mount(<VariablesPanel project="p" env="e" canWrite busy={false} />)
    await screen.findByDisplayValue('UTC')
    expect(screen.queryByRole('radio', { name: 'api' })).toBeNull()
    expect(screen.getByText(/solo puede dárselo a toda la app/)).toBeTruthy()
  })
})

describe('SecretsPanel by part (MS-6b)', () => {
  beforeEach(() => { api.mockReset() })

  const withSecret = settings({ secrets: [{ name: 'STRIPE_KEY', source: 'managed', state: 'loaded', next_version: 2, services: ['api'] }] as EnvironmentSettings['secrets'] })

  it('shows who reads each secret and changes it without loading the value', async () => {
    serve([web, apiSvc, jobs], withSecret)
    mount(<SecretsPanel project="p" env="e" canWrite busy={false} />)
    expect(await screen.findByText('Solo: api')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'Cambiar quién lo lee' }))
    await userEvent.click(await screen.findByRole('checkbox', { name: 'jobs' }))
    await userEvent.click(screen.getByRole('button', { name: 'Guardar' }))
    await waitFor(() => expect(writes('PUT')).toHaveLength(1))
    expect(writes('PUT')[0][0]).toBe('/projects/p/environments/e/secrets/STRIPE_KEY/services')
    expect(writes('PUT')[0][1]).toMatchObject({ body: { services: ['api', 'jobs'] } })
  })

  it('an app of one part offers no scope for a secret', async () => {
    serve([web], settings({ secrets: [{ name: 'STRIPE_KEY', source: 'managed', state: 'loaded', next_version: 2 }] as EnvironmentSettings['secrets'] }))
    mount(<SecretsPanel project="p" env="e" canWrite busy={false} />)
    await screen.findByText('STRIPE_KEY')
    expect(screen.queryByRole('button', { name: 'Cambiar quién lo lee' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: /Agregar secreto/ }))
    expect(screen.queryByRole('radio', { name: 'Toda la app' })).toBeNull()
  })

  it('a new secret carries the parts chosen', async () => {
    serve([web, apiSvc], settings())
    mount(<SecretsPanel project="p" env="e" canWrite busy={false} />)
    await userEvent.click(await screen.findByRole('button', { name: /Agregar secreto/ }))
    fireEvent.change(await screen.findByLabelText('Nombre'), { target: { value: 'DB_PASS' } })
    await userEvent.click(screen.getByRole('radio', { name: 'Solo algunas partes' }))
    await userEvent.click(screen.getByRole('checkbox', { name: 'api' }))
    await userEvent.click(screen.getByRole('checkbox', { name: 'web' }))
    await userEvent.click(screen.getByRole('button', { name: /Siguiente/ }))
    await waitFor(() => expect(writes('PUT')).toHaveLength(1))
    expect(writes('PUT')[0][0]).toBe('/projects/p/environments/e/secrets/DB_PASS')
    expect(writes('PUT')[0][1]).toMatchObject({ body: { source: 'managed', services: ['api'] } })
  })
})

describe('ResourcesPanel by part (MS-6b)', () => {
  beforeEach(() => { api.mockReset() })

  const res = (kind: 'postgres' | 'files', over = {}) => ({
    id: 'r1', name: kind === 'files' ? 'docs' : 'main-db', kind, storage_gi: 1, status: 'active', phase: 'ready', connected: false, prefix: '', services: [], variables: [], ...over,
  })

  it('connecting in an app of several parts asks who gets it first', async () => {
    serve([web, apiSvc], settings(), { items: [res('postgres')], available_kinds: ['postgres', 'files'] } as unknown as ResourceList)
    mount(<ResourcesPanel project="p" env="e" canWrite busy={false} technical={false} />)
    await userEvent.click(await screen.findByRole('button', { name: 'Conectar' }))
    expect(writes('PUT')).toHaveLength(0)
    await userEvent.click(await screen.findByRole('radio', { name: 'Solo algunas partes' }))
    await userEvent.click(screen.getByRole('checkbox', { name: 'api' }))
    await userEvent.click(screen.getAllByRole('button', { name: 'Conectar' }).at(-1)!)
    await waitFor(() => expect(writes('PUT')).toHaveLength(1))
    expect(writes('PUT')[0][0]).toBe('/projects/p/environments/e/resources/main-db/connection')
    // the picker starts with the first part ticked (web); ticking api adds it
    expect(writes('PUT')[0][1]).toMatchObject({ body: { services: ['api', 'web'] } })
  })

  it('connecting in an app of one part stays one click, with no services', async () => {
    serve([web], settings(), { items: [res('postgres')], available_kinds: ['postgres'] } as unknown as ResourceList)
    mount(<ResourcesPanel project="p" env="e" canWrite busy={false} technical={false} />)
    await userEvent.click(await screen.findByRole('button', { name: 'Conectar' }))
    await waitFor(() => expect(writes('PUT')).toHaveLength(1))
    expect(writes('PUT')[0][1]).toMatchObject({ body: {} })
  })

  it('a files volume of several parts cannot connect without exactly one part', async () => {
    serve([web, apiSvc], settings(), { items: [res('files')], available_kinds: ['files'] } as unknown as ResourceList)
    mount(<ResourcesPanel project="p" env="e" canWrite busy={false} technical={false} />)
    await userEvent.click(await screen.findByRole('button', { name: 'Conectar' }))
    expect(await screen.findByText(/solo una puede/)).toBeTruthy()
    const confirm = screen.getAllByRole('button', { name: 'Conectar' }).at(-1)!
    expect(confirm.hasAttribute('disabled')).toBe(true)
    await userEvent.click(screen.getByRole('checkbox', { name: 'api' }))
    await userEvent.click(screen.getAllByRole('button', { name: 'Conectar' }).at(-1)!)
    await waitFor(() => expect(writes('PUT')).toHaveLength(1))
    expect(writes('PUT')[0][1]).toMatchObject({ body: { services: ['api'] } })
  })

  it('shows who gets a connected resource', async () => {
    serve([web, apiSvc], settings(), { items: [res('postgres', { connected: true, services: ['api'] })], available_kinds: ['postgres'] } as unknown as ResourceList)
    mount(<ResourcesPanel project="p" env="e" canWrite busy={false} technical={false} />)
    expect(await screen.findByText('Solo: api')).toBeTruthy()
  })
})
