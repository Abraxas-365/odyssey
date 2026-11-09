import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AuthProvider, type Auth } from '@/lib/auth'
import { catalogEntry as entry } from '@/lib/catalog.fixture'
import { permissions } from '@/lib/permissions'

const api = vi.fn()
vi.mock('@/lib/api', async orig => ({ ...(await orig<typeof import('@/lib/api')>()), api: (...a: unknown[]) => api(...a) }))

import { CatalogPage, offeringSummary, specLine } from './catalog'

const basic = entry({}, { limits: { storage_gi: { max: 50 } }, preapproved: { storage_gi: { max: 10 } } })
const prod = entry(
  { recipe: 'postgres-produccion', title: 'PostgreSQL de producción', description: 'Con réplica. Sin respaldos automáticos todavía.', spec: { postgres: { instances: 2, major: 17, cpu: '1', memory: '2Gi' } } },
  { active: false, tiers: [], defaults: [] },
)
const redis = entry({ recipe: 'redis-basico', kind: 'redis', title: 'Redis básico' })

function mount(can: boolean) {
  const auth: Auth = { can: p => can && p === permissions.catalogManage, user: { id: 'u1' }, logout: () => {}, switchOrganization: () => {} }
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <AuthProvider value={auth}>
        <MemoryRouter>
          <CatalogPage />
        </MemoryRouter>
      </AuthProvider>
    </QueryClientProvider>,
  )
}

describe('CatalogPage', () => {
  beforeEach(() => { api.mockReset() })

  it('says only IT manages the catalog, without asking the API', () => {
    mount(false)
    expect(screen.getByText('Solo TI administra el catálogo')).toBeTruthy()
    expect(api).not.toHaveBeenCalled()
  })

  it('lists each kind with its options, ranges in plain words and what production lacks', async () => {
    api.mockResolvedValue({ items: [basic, prod, redis] })
    mount(true)
    const dbs = await screen.findByRole('list', { name: 'Bases de datos' })
    expect(within(dbs).getByText('PostgreSQL básico')).toBeTruthy()
    expect(within(dbs).getByText(/Tamaño: de 1 a 50 GB; al instante de 1 a 10 GB, más allá con aprobación\./)).toBeTruthy()
    expect(within(dbs).getByText(/Sin respaldos automáticos todavía/)).toBeTruthy()
    expect(within(dbs).getByText('Apagada')).toBeTruthy()
    expect(screen.getByRole('list', { name: 'Cachés' })).toBeTruthy()
    // no ids or units of the API on screen
    expect(document.body.textContent).not.toMatch(/storage_gi|GiB|preapproved|postgres-basico/)
  })

  it('saves a narrower offering as the PUT body, only what narrows', async () => {
    api.mockImplementation((_path: string, init?: { method?: string; body?: unknown }) =>
      init?.method === 'PUT' ? Promise.resolve({ ...basic, offering: { ...basic.offering, ...(init.body as object) } }) : Promise.resolve({ items: [basic, prod, redis] }),
    )
    mount(true)
    await userEvent.click(await screen.findByRole('button', { name: 'Editar PostgreSQL básico' }))
    const save = screen.getByRole('button', { name: 'Guardar' })
    expect(save.hasAttribute('disabled')).toBe(true)
    fireEvent.change(screen.getByLabelText('Al instante hasta'), { target: { value: '20' } })
    await userEvent.click(save)
    await waitFor(() => expect(api.mock.calls.some(c => c[1]?.method === 'PUT')).toBe(true))
    const put = api.mock.calls.find(c => c[1]?.method === 'PUT')!
    expect(put[0]).toBe('/catalog/recipes/postgres-basico/offering')
    expect(put[1].body).toEqual({ version: 1, active: true, tiers: ['sandbox', 'internal', 'production'], defaults: ['sandbox', 'internal', 'production'], limits: { storage_gi: { max: 50 } }, preapproved: { storage_gi: { max: 20 } }, teams: [] })
  })

  it('refuses a pre-approved range outside the limits before sending', async () => {
    api.mockResolvedValue({ items: [basic] })
    mount(true)
    await userEvent.click(await screen.findByRole('button', { name: 'Editar PostgreSQL básico' }))
    fireEvent.change(screen.getByLabelText('Al instante hasta'), { target: { value: '60' } })
    expect(screen.getByText(/dentro del límite \(1–50 GB\)/)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Guardar' }).hasAttribute('disabled')).toBe(true)
  })

  it('cannot make a second default of a kind in one level', async () => {
    api.mockResolvedValue({ items: [basic, { ...prod, offering: { ...prod.offering, active: true, tiers: ['production'] } }] })
    mount(true)
    await userEvent.click(await screen.findByRole('button', { name: 'Editar PostgreSQL de producción' }))
    expect(screen.getAllByText(/ya lo es «PostgreSQL básico»/).length).toBeGreaterThan(0)
  })

  const teams = { available: true, items: [{ id: 'grp_datos', name: 'Datos', members: 4, directory: false }, { id: 'grp_ventas', name: 'Ventas', members: 1, directory: true }] }
  const serve = (items: unknown[], dir: unknown = teams) => api.mockImplementation((path: string, init?: { method?: string; body?: unknown }) =>
    path === '/catalog/teams' ? Promise.resolve(dir)
      : init?.method === 'PUT' ? Promise.resolve({ ...basic, offering: { ...basic.offering, ...(init.body as object) } })
        : Promise.resolve({ items }))

  it('limits an offering to some teams, by name', async () => {
    serve([basic])
    mount(true)
    await userEvent.click(await screen.findByRole('button', { name: 'Editar PostgreSQL básico' }))
    await userEvent.click(screen.getByRole('radio', { name: /Solo estos equipos/ }))
    const save = screen.getByRole('button', { name: 'Guardar' })
    expect(screen.getByText('Elige al menos un equipo, o ofrécela a todos.')).toBeTruthy()
    expect(save.hasAttribute('disabled')).toBe(true)
    const group = screen.getByRole('group', { name: 'Equipos' })
    expect(within(group).getByText('1 persona · del directorio de la empresa')).toBeTruthy()
    await userEvent.click(within(group).getByRole('checkbox', { name: /Datos/ }))
    await userEvent.click(save)
    await waitFor(() => expect(api.mock.calls.some(c => c[1]?.method === 'PUT')).toBe(true))
    expect(api.mock.calls.find(c => c[1]?.method === 'PUT')![1].body.teams).toEqual(['grp_datos'])
    expect(document.body.textContent).not.toMatch(/grp_/)
  })

  it('without the directory: no new teams, the saved ones are kept', async () => {
    serve([basic, redis], { available: false, items: [] })
    mount(true)
    await userEvent.click(await screen.findByRole('button', { name: 'Editar PostgreSQL básico' }))
    const teamsOnly = screen.getByRole('radio', { name: /Solo estos equipos/ })
    expect(teamsOnly.hasAttribute('disabled') || teamsOnly.getAttribute('aria-disabled') === 'true' || teamsOnly.hasAttribute('data-disabled')).toBe(true)
    expect(screen.getByText(/Odicea no puede leer los equipos de tu organización/)).toBeTruthy()
  })

  it('a team default does not clash with everyone’s; the summary names the team', async () => {
    const forDatos = { ...prod, offering: { ...prod.offering, active: true, tiers: ['production' as const], teams: ['grp_datos'] } }
    serve([basic, forDatos])
    mount(true)
    expect(await screen.findByText('Solo la crea el equipo Datos.')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'Editar PostgreSQL de producción' }))
    expect(screen.queryByText(/ya lo es «PostgreSQL básico»/)).toBeNull()
  })
})

describe('catalog lines', () => {
  it('reads the engine settings and an offering in one line each', () => {
    expect(specLine(prod)).toBe('2 instancias · PostgreSQL 17 · 1 CPU · 2 GB de memoria')
    expect(specLine(basic)).toBeUndefined()
    expect(offeringSummary(prod)).toEqual(['No se ofrece: ninguna app puede crearla.'])
    expect(offeringSummary(entry())).toEqual(['Se ofrece en apps Sandbox · Interna · Producción.', 'Es la opción por defecto en Sandbox · Interna · Producción.', 'Tamaño: de 1 a 100 GB, todo al instante.'])
    // teams: by name; never an id, not even before the names arrive or without the directory
    const forTeams = entry({}, { teams: ['grp_datos', 'grp_old'] })
    expect(offeringSummary(forTeams, [{ id: 'grp_datos', name: 'Datos', members: 1, directory: false }])[1]).toBe('Solo la crean los equipos Datos, un equipo que ya no existe.')
    expect(offeringSummary(forTeams)[1]).toBe('Solo la crean 2 equipos.')
    expect(offeringSummary(entry({}, { teams: ['grp_datos'] }))[1]).toBe('Solo la crea un equipo.')
  })
})

// The kinds' contracts as GET /catalog/kinds answers them (Go's TestKinds keeps the shape the registry's).
const v = (name: string, meaning: string, over: object = {}) => ({ name, meaning, secret: false, optional: false, derived: false, format: '', choices: [], together: '', ...over })
const contracts = {
  items: [
    { kind: 'postgres', existing: true, variables: [
      v('DATABASE_URL', 'la dirección completa de conexión', { secret: true, derived: true }),
      v('PGHOST', 'el servidor', { format: 'host' }), v('PGPORT', 'el puerto', { format: 'port' }), v('PGDATABASE', 'el nombre de la base'),
      v('PGUSER', 'el usuario'), v('PGPASSWORD', 'la contraseña', { secret: true }),
      v('PGSSLMODE', 'cómo se cifra la conexión', { optional: true, choices: ['disable', 'require'] }),
    ] },
    { kind: 'redis', existing: true, variables: [v('REDIS_URL', 'la dirección de conexión', { secret: true })] },
    { kind: 'files', existing: false, variables: [v('FILES_DIR', 'la carpeta')] },
  ],
}
const stores = { items: [{ name: 'vault', clusters: [{ id: 'c1', name: 'Lima' }] }] }
const own = entry(
  { recipe: 'org-base-de-reportes', title: 'Base de reportes', description: '', engine: 'existing', params: [], requires: ['existing'],
    spec: { existing: { values: [
      { variable: 'PGHOST', value: 'db.empresa.com' }, { variable: 'PGPORT', value: '5432' }, { variable: 'PGDATABASE', value: 'reportes' },
      { variable: 'PGUSER', value: 'app' }, { variable: 'PGPASSWORD', ref: { store: 'vault', key: 'rds/reportes', property: 'password' } },
    ] } } },
  { recipe: 'org-base-de-reportes', active: false, tiers: [], defaults: [] },
)

function serveOwn(items: unknown[], can: (p: string) => boolean = () => true) {
  api.mockImplementation((path: string, init?: { method?: string; body?: Record<string, unknown> }) => {
    if (path === '/catalog/kinds') return Promise.resolve(contracts)
    if (path === '/catalog/stores') return Promise.resolve(stores)
    if (path === '/catalog/teams') return Promise.resolve({ available: false, items: [] })
    if (init?.method === 'POST' && path === '/catalog/recipes') return Promise.resolve({ ...own, recipe: init.body!.recipe, title: init.body!.title })
    if (init?.method === 'POST') return Promise.resolve(own)
    return Promise.resolve({ items })
  })
  const auth: Auth = { can: p => can(p), user: { id: 'u1' }, logout: () => {}, switchOrganization: () => {} }
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}><AuthProvider value={auth}><MemoryRouter><CatalogPage /></MemoryRouter></AuthProvider></QueryClientProvider>)
}
const posts = () => api.mock.calls.filter(c => c[1]?.method === 'POST')

describe('a resource the company already has (stage 2)', () => {
  beforeEach(() => { api.mockReset() })

  it('registers one: the id comes from the name, a credential only from a store, then IT offers it', async () => {
    serveOwn([basic])
    await userEvent.click(await screen.findByRole('button', { name: /Registrar un recurso que ya existe/ }))
    const dialog = await screen.findByRole('dialog')
    // only the kinds a company's resource can fulfil
    expect(within(dialog).queryByRole('radio', { name: 'Archivos' })).toBeNull()
    expect(within(dialog).getByText('Se arma sola con los datos de abajo.')).toBeTruthy()
    // the password has no "write it" choice
    const password = within(dialog).getByRole('group', { name: 'La contraseña' })
    expect(within(password).queryByRole('radio', { name: 'Escribirlo' })).toBeNull()
    expect(within(dialog).getByText(/Si la salida del clúster está restringida/)).toBeTruthy()

    await userEvent.type(within(dialog).getByLabelText('Nombre'), 'Base de reportes')
    await userEvent.type(within(dialog).getByRole('textbox', { name: 'El servidor' }), '10.0.0.5')
    expect(within(dialog).getByText(/10\.0\.0\.5 es una dirección de una red privada/)).toBeTruthy()
    await userEvent.type(within(dialog).getByRole('textbox', { name: 'El puerto' }), '5432')
    await userEvent.type(within(dialog).getByRole('textbox', { name: 'El nombre de la base' }), 'reportes')
    await userEvent.type(within(dialog).getByRole('textbox', { name: 'El usuario' }), 'app')
    await userEvent.type(within(password).getByLabelText('Llave en el almacén'), 'rds/reportes')
    await userEvent.type(within(password).getByLabelText(/Propiedad/), 'password')
    expect(within(dialog).getByText(/Lo pueden usar las apps de el clúster Lima/)).toBeTruthy()
    await userEvent.click(within(dialog).getByRole('button', { name: 'Registrar' }))

    await waitFor(() => expect(posts()).toHaveLength(1))
    expect(posts()[0][0]).toBe('/catalog/recipes')
    expect(posts()[0][1].body).toEqual({
      recipe: 'org-base-de-reportes', kind: 'postgres', title: 'Base de reportes',
      existing: { values: [
        { variable: 'PGHOST', value: '10.0.0.5' }, { variable: 'PGPORT', value: '5432' }, { variable: 'PGDATABASE', value: 'reportes' },
        { variable: 'PGUSER', value: 'app' }, { variable: 'PGPASSWORD', ref: { store: 'vault', key: 'rds/reportes', property: 'password' } },
      ] },
    })
    // registered switched off: the offering dialog opens to offer it
    expect(await screen.findByRole('switch', { name: /Se ofrece/ })).toBeTruthy()
  })

  it('says what is missing before sending, and nothing is sent', async () => {
    serveOwn([basic])
    await userEvent.click(await screen.findByRole('button', { name: /Registrar un recurso que ya existe/ }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.type(within(dialog).getByRole('textbox', { name: 'El servidor' }), 'localhost')
    expect(within(dialog).getByText(/No vale localhost/)).toBeTruthy()
    await userEvent.click(within(dialog).getByRole('button', { name: 'Registrar' }))
    expect(within(dialog).getByText(/Ponle un nombre/)).toBeTruthy()
    expect(posts()).toHaveLength(0)
  })

  it('another kind starts its own connection; a written piece may come from a store instead', async () => {
    serveOwn([basic])
    await userEvent.click(await screen.findByRole('button', { name: /Registrar un recurso que ya existe/ }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.type(within(dialog).getByRole('textbox', { name: 'El servidor' }), 'db.empresa.com')
    await userEvent.click(within(dialog).getByRole('radio', { name: 'Caché' }))
    expect(within(dialog).queryByRole('textbox', { name: 'El servidor' })).toBeNull()
    expect(within(dialog).getByRole('group', { name: 'La dirección de conexión' })).toBeTruthy()
    await userEvent.click(within(dialog).getByRole('radio', { name: 'Base de datos' }))
    expect((within(dialog).getByRole('textbox', { name: 'El servidor' }) as HTMLInputElement).value).toBe('')
    const user = within(dialog).getByRole('group', { name: 'El usuario' })
    await userEvent.click(within(user).getByRole('radio', { name: 'Del almacén de secretos' }))
    expect(within(user).getByLabelText('Llave en el almacén')).toBeTruthy()
    expect(within(user).queryByRole('textbox', { name: 'El usuario' })).toBeNull()
  })

  it('without the vault permission, a connection that reads a store cannot be sent', async () => {
    serveOwn([basic], p => p !== permissions.secretsExternal)
    await userEvent.click(await screen.findByRole('button', { name: /Registrar un recurso que ya existe/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Tu cuenta no puede usar el almacén de secretos de la empresa.')).toBeTruthy()
    expect(within(dialog).getByRole('button', { name: 'Registrar' }).hasAttribute('disabled')).toBe(true)
  })

  it('lists one with its marking and where it connects; a new version starts from the current', async () => {
    serveOwn([basic, own])
    const dbs = await screen.findByRole('list', { name: 'Bases de datos' })
    expect(within(dbs).getByText('Ya existe en la empresa')).toBeTruthy()
    expect(within(dbs).getByText('Se conecta a db.empresa.com · credenciales del almacén «vault».')).toBeTruthy()
    expect(within(dbs).getByText('No se ofrece: ninguna app puede usarla.')).toBeTruthy()
    // a factory recipe has no new version here
    expect(screen.queryByRole('button', { name: 'Versión nueva de PostgreSQL básico' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Versión nueva de Base de reportes' }))
    const dialog = await screen.findByRole('dialog')
    expect((within(dialog).getByRole('textbox', { name: 'El servidor' }) as HTMLInputElement).value).toBe('db.empresa.com')
    const publish = within(dialog).getByRole('button', { name: 'Publicar versión 2' })
    expect(publish.hasAttribute('disabled')).toBe(true)
    const host = within(dialog).getByRole('textbox', { name: 'El servidor' })
    await userEvent.clear(host)
    await userEvent.type(host, 'db2.empresa.com')
    await userEvent.click(publish)
    await waitFor(() => expect(posts()).toHaveLength(1))
    expect(posts()[0][0]).toBe('/catalog/recipes/org-base-de-reportes/versions')
    expect(posts()[0][1].body.existing.values[0]).toEqual({ variable: 'PGHOST', value: 'db2.empresa.com' })
    expect('recipe' in posts()[0][1].body).toBe(false)
  })
})
