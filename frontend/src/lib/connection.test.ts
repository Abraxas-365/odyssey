import { describe, expect, it } from 'vitest'
import type { CatalogKind, CatalogKindVariable } from './types'
import { connectionLine, connectionOf, connectionProblem, connectionRows, givenOf, hostOk, meaningOf, missingStores, recipeIdOf, recipeIdOk, rowProblem, sameConnection, sourcesOf, titleProblem, valueProblem, type Row } from './connection'

const variable = (over: Partial<CatalogKindVariable> & { name: string }): CatalogKindVariable =>
  ({ meaning: over.name.toLowerCase(), secret: false, optional: false, derived: false, format: '', choices: [], together: '', ...over })

// The shape GET /catalog/kinds answers (Go's TestKinds keeps it the registry's).
const postgres: CatalogKind = {
  kind: 'postgres', existing: true, variables: [
    variable({ name: 'DATABASE_URL', secret: true, derived: true }),
    variable({ name: 'PGHOST', meaning: 'el servidor', format: 'host' }),
    variable({ name: 'PGPORT', meaning: 'el puerto', format: 'port' }),
    variable({ name: 'PGDATABASE', meaning: 'la base' }),
    variable({ name: 'PGUSER', meaning: 'el usuario' }),
    variable({ name: 'PGPASSWORD', meaning: 'la contraseña', secret: true }),
    variable({ name: 'PGSSLMODE', optional: true, choices: ['disable', 'require', 'verify-full'] }),
  ],
}
const objects: CatalogKind = {
  kind: 'object-storage', existing: true, variables: [
    variable({ name: 'S3_BUCKET', meaning: 'el bucket' }),
    variable({ name: 'S3_ENDPOINT', optional: true, format: 'url' }),
    variable({ name: 'AWS_ACCESS_KEY_ID', meaning: 'la llave de acceso', secret: true, optional: true, together: 'aws-key' }),
    variable({ name: 'AWS_SECRET_ACCESS_KEY', meaning: 'la llave secreta', secret: true, optional: true, together: 'aws-key' }),
  ],
}
const v = (k: CatalogKind, name: string) => k.variables.find(x => x.name === name)!
const ref = (key: string, property = ''): Row => ({ source: 'store', value: '', store: 'vault', key, property })
const val = (value: string): Row => ({ source: 'value', value, store: 'vault', key: '', property: '' })

const filled = (): Record<string, Row> => ({
  ...connectionRows(postgres, undefined, 'vault'),
  PGHOST: val('db.empresa.com'), PGPORT: val('5432'), PGDATABASE: val('reportes'), PGUSER: ref('rds/reportes', 'username'), PGPASSWORD: ref('rds/reportes', 'password'),
})

describe('the connection of a resource the company already has', () => {
  it('asks only for what IT gives, each the ways the contract allows', () => {
    expect(givenOf(postgres).map(x => x.name)).not.toContain('DATABASE_URL')
    expect(sourcesOf(v(postgres, 'PGPASSWORD'))).toEqual(['store'])
    expect(sourcesOf(v(postgres, 'PGHOST'))).toEqual(['value', 'store'])
    expect(sourcesOf(v(postgres, 'PGSSLMODE'))).toEqual(['value', 'none'])
    expect(sourcesOf(v(objects, 'AWS_ACCESS_KEY_ID'))).toEqual(['store', 'none'])
  })

  it('starts each row on its first way, an optional one unused, a new reference on the given store', () => {
    const rows = connectionRows(postgres, undefined, 'vault')
    expect(Object.keys(rows)).toEqual(['PGHOST', 'PGPORT', 'PGDATABASE', 'PGUSER', 'PGPASSWORD', 'PGSSLMODE'])
    expect(rows.PGHOST.source).toBe('value')
    expect(rows.PGPASSWORD).toEqual(ref(''))
    expect(rows.PGSSLMODE.source).toBe('none')
  })

  it('a new version starts from the published connection', () => {
    const rows = connectionRows(postgres, { values: [{ variable: 'PGHOST', value: 'db.empresa.com' }, { variable: 'PGPASSWORD', ref: { store: 'aws', key: 'k', property: 'p' } }] }, 'vault')
    expect(rows.PGHOST).toEqual(val('db.empresa.com'))
    expect(rows.PGPASSWORD).toEqual({ source: 'store', value: '', store: 'aws', key: 'k', property: 'p' })
    expect(rows.PGSSLMODE.source).toBe('none')
  })

  it('sends the contract order, trimmed, without unused or derived variables', () => {
    const rows = { ...filled(), PGHOST: val(' db.empresa.com '), PGPASSWORD: ref(' rds/reportes ', ' ') }
    expect(connectionOf(postgres, rows).values).toEqual([
      { variable: 'PGHOST', value: 'db.empresa.com' },
      { variable: 'PGPORT', value: '5432' },
      { variable: 'PGDATABASE', value: 'reportes' },
      { variable: 'PGUSER', ref: { store: 'vault', key: 'rds/reportes', property: 'username' } },
      { variable: 'PGPASSWORD', ref: { store: 'vault', key: 'rds/reportes' } },
    ])
  })

  it('a complete connection has no problem; each missing piece names its variable', () => {
    expect(connectionProblem(postgres, filled())).toBeUndefined()
    expect(connectionProblem(postgres, { ...filled(), PGHOST: val('') })).toEqual({ variable: 'PGHOST', text: 'Escribe el servidor.' })
    expect(connectionProblem(postgres, { ...filled(), PGPASSWORD: val('hunter2') })?.variable).toBe('PGPASSWORD')
    expect(connectionProblem(postgres, { ...filled(), PGPASSWORD: ref('') })?.text).toMatch(/llave/)
    expect(connectionProblem(postgres, { ...filled(), PGUSER: { ...ref('k'), store: '' } })?.text).toMatch(/almacén/)
    expect(connectionProblem(postgres, { ...filled(), PGHOST: { ...val(''), source: 'none' } })?.variable).toBe('PGHOST')
  })

  it('a variable with choices takes only one of them', () => {
    expect(rowProblem(v(postgres, 'PGSSLMODE'), val('require'))).toBeUndefined()
    expect(rowProblem(v(postgres, 'PGSSLMODE'), val('maybe'))).toMatch(/disable, require, verify-full/)
    expect(rowProblem(v(postgres, 'PGSSLMODE'), ref('k'))).toBeDefined()
  })

  it('optional variables that go together: both or neither', () => {
    const base = { ...connectionRows(objects, undefined, 'vault'), S3_BUCKET: val('reportes') }
    expect(connectionProblem(objects, base)).toBeUndefined()
    expect(connectionProblem(objects, { ...base, AWS_ACCESS_KEY_ID: ref('aws/id') })).toEqual({ variable: 'AWS_SECRET_ACCESS_KEY', text: 'La llave de acceso y la llave secreta van juntas: usa las dos o ninguna.' })
    expect(connectionProblem(objects, { ...base, AWS_ACCESS_KEY_ID: ref('aws/id'), AWS_SECRET_ACCESS_KEY: ref('aws/secret') })).toBeUndefined()
  })

  it('checks formats like the server', () => {
    const host = variable({ name: 'H', format: 'host' })
    for (const ok of ['db.empresa.com', '10.0.0.5', 'rds.us-east-1.amazonaws.com', 'fd00::1']) expect(valueProblem(host, ok), ok).toBeUndefined()
    for (const bad of ['localhost', 'postgres', '127.0.0.1', '169.254.169.254', 'db.svc', 'pg.ns.svc.cluster.local', 'a.localhost', '::1', 'fe80::1', 'metadata.google.internal', 'x.2130706433', 'db empresa.com'])
      expect(valueProblem(host, bad), bad).toBeDefined()
    expect(hostOk('0.0.0.0')).toBe(false)
    const port = variable({ name: 'P', format: 'port' })
    expect(valueProblem(port, '5432')).toBeUndefined()
    for (const bad of ['0', '65536', '05432', 'x']) expect(valueProblem(port, bad), bad).toBeDefined()
    const url = variable({ name: 'U', format: 'url' })
    expect(valueProblem(url, 'https://s3.empresa.com')).toBeUndefined()
    for (const bad of ['ftp://s3.empresa.com', 'https://a:b@s3.empresa.com', 'https://s3.empresa.com/?x=1', 'https://localhost', 'nope'])
      expect(valueProblem(url, bad), bad).toBeDefined()
    expect(valueProblem(variable({ name: 'T' }), 'x'.repeat(256))).toBeDefined()
    // bytes, like Go's len: 200 accented letters are 400 bytes
    expect(valueProblem(variable({ name: 'T' }), 'á'.repeat(200))).toBeDefined()
    expect(valueProblem(variable({ name: 'T' }), 'a b')).toBeUndefined()
    expect(valueProblem(variable({ name: 'T' }), 'a\u00a0b')).toBeDefined()
    // IPv6 as net.ParseIP reads it: malformed, or an IPv4 inside that is refused
    for (const bad of ['::ffff:127.0.0.1', '::ffff:169.254.169.254', '1:2:3', 'fe80::1', 'ff02::1', '0:0:0:0:0:0:0:1', '1::2::3', 'g::1'])
      expect(hostOk(bad), bad).toBe(false)
    for (const ok of ['2001:db8::1', '::ffff:10.0.0.5', 'fd12:3456::1']) expect(hostOk(ok), ok).toBe(true)
  })

  it('splits a meaning from the hint in parentheses, and says it as a sentence', () => {
    expect(meaningOf({ meaning: 'la llave de acceso (vacía: la identidad de la app)' })).toEqual({ what: 'la llave de acceso', hint: 'vacía: la identidad de la app' })
    expect(meaningOf({ meaning: 'el servidor' })).toEqual({ what: 'el servidor' })
    const endpoint = variable({ name: 'E', meaning: 'la dirección del servicio (vacía: la de AWS)', optional: true, format: 'url' })
    expect(valueProblem(endpoint, '')).toBe('Escribe la dirección del servicio, o elige «No usar».')
    expect(valueProblem(variable({ name: 'C', meaning: 'el cifrado', choices: ['a'] }), '')).toBe('Elige el cifrado.')
  })

  it('a connection is the same whatever the order of its variables', () => {
    const a = { values: [{ variable: 'PGHOST', value: 'db' }, { variable: 'PGPASSWORD', ref: { store: 'vault', key: 'k' } }] }
    const b = { values: [{ variable: 'PGPASSWORD', ref: { store: 'vault', key: 'k', property: '' } }, { variable: 'PGHOST', value: 'db' }] }
    expect(sameConnection(a, b)).toBe(true)
    expect(sameConnection(a, { values: [a.values[0]] })).toBe(false)
    expect(sameConnection({ values: [] }, undefined)).toBe(true)
  })

  it('checks a reference\'s key and property charsets', () => {
    const pw = v(postgres, 'PGPASSWORD')
    expect(rowProblem(pw, ref('rds/reportes:prod@v1', 'pass_word-1.x'))).toBeUndefined()
    expect(rowProblem(pw, ref('rds reportes'))).toMatch(/llave/)
    expect(rowProblem(pw, ref('-rds'))).toMatch(/llave/)
    expect(rowProblem(pw, ref('rds', 'a/b'))).toMatch(/propiedad/)
  })

  it('names the stores no cluster announces any more', () => {
    expect(missingStores(filled(), [{ name: 'vault' }])).toEqual([])
    expect(missingStores({ ...filled(), PGUSER: { ...ref('k'), store: 'old' } }, [{ name: 'vault' }])).toEqual(['old'])
  })
})

describe('an organization recipe', () => {
  it('takes its id from the title, never typed', () => {
    expect(recipeIdOf('Base de reportes (RDS)', [])).toBe('org-base-de-reportes-rds')
    expect(recipeIdOf('Caché de sesión', [])).toBe('org-cache-de-sesion')
    expect(recipeIdOf('  ', [])).toBe('org-recurso')
    expect(recipeIdOf('Reportes', ['org-reportes'])).toBe('org-reportes-2')
    expect(recipeIdOf('Reportes', ['org-reportes', 'org-reportes-2'])).toBe('org-reportes-3')
    const long = recipeIdOf('a'.repeat(30) + ' ' + 'b'.repeat(30), [])
    expect(long.length).toBeLessThanOrEqual(48)
    expect(recipeIdOk(long)).toBe(true)
    const taken = recipeIdOf('a'.repeat(60), ['org-' + 'a'.repeat(44)])
    expect(taken).toMatch(/-2$/)
    expect(recipeIdOk(taken)).toBe(true)
  })

  it('checks the title and description bounds', () => {
    expect(titleProblem('Reportes', '')).toBeUndefined()
    expect(titleProblem(' ', '')).toBeDefined()
    expect(titleProblem('x'.repeat(81), '')).toBeDefined()
    expect(titleProblem('x', 'y'.repeat(501))).toBeDefined()
  })

  it('says where a published connection points, in one line', () => {
    const spec = { existing: { values: [{ variable: 'PGPORT', value: '5432' }, { variable: 'PGHOST', value: 'db.empresa.com' }, { variable: 'PGPASSWORD', ref: { store: 'vault', key: 'k' } }] } }
    expect(connectionLine(postgres, { spec })).toBe('Se conecta a db.empresa.com · credenciales del almacén «vault».')
    expect(connectionLine(postgres, { spec: {} })).toBeUndefined()
  })
})
