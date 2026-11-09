import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

// UX v4 §5.3 rules (architecture tests for the console's app pages).
const src = resolve(__dirname, '..')
const files = (dir: string): string[] =>
  readdirSync(dir).flatMap(n => {
    const p = join(dir, n)
    return statSync(p).isDirectory() ? files(p) : /\.tsx?$/.test(n) && !/\.test\.tsx?$/.test(n) && n !== 'openapi.gen.ts' ? [p] : []
  })
const rel = (f: string) => relative(src, f)
const offenders = (re: RegExp, allowed: string[]) =>
  files(src).map(rel).filter(f => !allowed.includes(f)).filter(f => re.test(readFileSync(join(src, f), 'utf8')))

describe('UX v4 §5.3: app pages', () => {
  // A version has one name everywhere (commit title → requester's words →
  // "Subida desde una carpeta"): only versionMessage decides it.
  it('only versions.ts names a version from its commit or message', () => {
    expect(offenders(/\.source\??\.git\??\.title|\bd\.message\b|deployment\??\.message\b/, ['features/apps/versions.ts'])).toEqual([])
  })

  // Every tab of an app reads what AppLayout loaded (useAppPage); a tab that
  // fetched the current release or the deployments again would poll twice
  // and could disagree with the header.
  it('app tabs read the layout, not the release API', () => {
    const tabs = ['pages/app-overview.tsx', 'pages/app-history.tsx', 'pages/app-share.tsx', 'pages/app-settings.tsx']
    for (const t of tabs) {
      const body = readFileSync(join(src, t), 'utf8')
      expect(body, t).toMatch(/useAppPage\(\)/)
      expect(body, t).not.toMatch(/\buse(Current|Deployments|Environments|Project)\(/)
    }
  })

  // The folder picker lives in the "Subir una carpeta" dialog, reached from
  // the contextual action; no page carries a permanent upload panel again.
  it('the folder picker is only in the upload dialog', () => {
    expect(offenders(/webkitdirectory/, ['features/apps/release-panels.tsx'])).toEqual([])
    expect(readFileSync(join(src, 'features/apps/release-panels.tsx'), 'utf8')).not.toMatch(/PublishFolderPanel/)
  })

  // Archiving is in Configuración, away from the everyday actions.
  it('archiving is offered only in Configuración', () => {
    expect(offenders(/useArchiveProject\(/, ['pages/app-settings.tsx'])).toEqual([])
  })
})

describe('UX v4 §5.2: Inicio', () => {
  // A drawing that is not the app is not shown as if it were (the fake thumbnail is gone).
  it('no page draws a fake preview of an app', () => {
    expect(offenders(/AppThumbnail|thumbnail\s*[:=]/, [])).toEqual([])
  })

  // Inicio reads as a list a person reads: rows of two lines, never a column table.
  it('Inicio lists apps with AppList/AppRow, not a table', () => {
    const body = readFileSync(join(src, 'pages/home.tsx'), 'utf8')
    expect(body).toMatch(/<AppList\b/)
    expect(body).toMatch(/<AppRow\b/)
    expect(body).not.toMatch(/@\/components\/ui\/table/)
  })

  // One main link per row: the stretched link is the row's, and only the app list draws it.
  it('only the app row and card stretch a link over the row', () => {
    expect(offenders(/data-row-link/, ['components/odyssey/app-card.tsx'])).toEqual([])
  })

  // "Necesita tu atención" shows what the API said; the console invents no item
  // (e.g. "No conectaste tu asistente" belongs to the header chip, UX v4 §5.7).
  it('attention items come only from the API', () => {
    expect(offenders(/<AttentionItem\s/, ['features/inventory/attention-section.tsx', 'pages/design-system.tsx', 'components/odyssey/attention.tsx'])).toEqual([])
  })
})

describe('UX v4 §6.1: the assistant', () => {
  // "Conectado" has one source, GET /me, read in one place: no surface can
  // guess it (from a deployment's channel, a preference, a dismissed tip) or
  // disagree with another one.
  it('only features/assistant/state.ts reads whether an assistant is connected', () => {
    expect(offenders(/assistant\??\.connected\b/, ['features/assistant/state.ts', 'pages/assistant.tsx'])).toEqual([])
    expect(offenders(/['"]\/me['"]|\/me\/assistants/, ['lib/me.ts'])).toEqual([])
  })

  // The assistant is the header chip, not a tab; and "connect your assistant"
  // is not a notification nor an attention item.
  it('the assistant is not a tab nor a notification', () => {
    const layout = readFileSync(join(src, 'pages/console-layout.tsx'), 'utf8')
    expect(layout).not.toMatch(/label:\s*'Asistente'/)
    expect(layout).toMatch(/<AssistantChip\b/)
    expect(offenders(/Conecta tu asistente para/, [])).toEqual([])
  })

  // Administración is one tab with its sections; the old addresses only redirect.
  it('admin pages live under /administracion', () => {
    expect(offenders(/['"`]\/(configuracion\/(github|reglas)|infraestructura\/clusteres)\b/, [])).toEqual([])
  })
})

describe('UX v4 §5.5: the company log', () => {
  // The channel is the server's (`channel`, `requested_via`); its words and
  // the "assistant:" prefix are spelled in one place, so a chip, a filter and
  // the weekly sentence cannot disagree, and no surface guesses a channel
  // from a raw OAuth client.
  it('only attribution.ts spells the channel vocabulary', () => {
    expect(offenders(/['"`]assistant:/, ['features/apps/attribution.ts'])).toEqual([])
    expect(offenders(/\be\.client\b|entry\??\.client\b|\.client\s*===/, [])).toEqual([])
  })

  // Every read of /audit is in lib/audit.ts (one place asks only with the permission).
  it('only lib/audit.ts calls the audit API', () => {
    expect(offenders(/['"`]\/audit\b/, ['lib/audit.ts'])).toEqual([])
  })

  // The integrity seal reads /audit/verify, never a constant: "íntegro" only from sealOf.
  it('only sealOf says the log is intact', () => {
    expect(offenders(/['"`>]Registro íntegro/, ['features/audit/audit.ts', 'pages/design-system.tsx'])).toEqual([])
  })
})

describe('MS-6: services (D3)', () => {
  // An app of one service reads exactly as before: the word "servicio"
  // reaches the screen only through the services files, and each component
  // among them asks isMulti before saying it. Comments don't count.
  const word = /\b[Ss]ervicios?\b/
  const code = (body: string) => body.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:'"`])\/\/.*$/gm, '$1')
  const says = (body: string) => word.test(code(body))
  const allowed = [
    'features/apps/services.ts',
    'features/apps/services-panel.tsx',
    'features/apps/service-states.tsx',
    'features/apps/service-scope.tsx',
    // IT's own vocabulary (rules on set_services), not the builder's
    'features/policy/rules.ts',
    // "Servicio externo" is an external key (a secret), another thing
    'components/odyssey/resources.tsx',
    // "Un servicio del que dependemos": an outside provider (EXTERNAL errors)
    'lib/api.ts',
  ]

  it('the rule catches violations', () => {
    expect(says(`<p>Este servicio falló</p>`)).toBe(true)
    expect(says(`toast('Servicios guardados')`)).toBe(true)
    expect(says(`// los servicios de la app\nconst x = 1`)).toBe(false)
    expect(says(`/** servicios */ const y = 2`)).toBe(false)
    expect(says(`const a = 'x' // y\nconst b = 'servicio'`)).toBe(true)
  })

  it('only the services files say "servicio"', () => {
    const offending = files(src).map(rel).filter(f => !allowed.includes(f)).filter(f => says(readFileSync(join(src, f), 'utf8')))
    expect(offending).toEqual([])
  })

  it('every services component asks isMulti before talking about services', () => {
    for (const f of allowed.filter(f => f.startsWith('features/apps/') && f.endsWith('.tsx'))) {
      const body = readFileSync(join(src, f), 'utf8')
      expect(body, f).toMatch(/\bisMulti\(/)
    }
  })
})

describe('UX v4 §5.9: visual', () => {
  const monoTitle = /<h[1-6][^>]*font-mono|font-heading[^"'`]*font-mono|font-mono[^"'`]*font-heading/
  const monoFigure = /font-mono[^"'`]*text-(stat|title|display|section)\b|text-(stat|title|display|section)\b[^"'`]*font-mono/
  const boxedList = /divide-y[^"'`]*\bborder\b|\bborder\b[^"'`]*divide-y/

  it('the rules catch violations', () => {
    expect(monoTitle.test('<h2 className="font-mono text-sm">')).toBe(true)
    expect(monoFigure.test('className="font-mono text-stat font-semibold"')).toBe(true)
    expect(boxedList.test('className="flex flex-col divide-y rounded-lg border"')).toBe(true)
    expect(monoTitle.test('<span className="font-mono">{slug}</span>')).toBe(false)
    expect(boxedList.test('className="flex flex-col divide-y"')).toBe(false)
  })

  // Inter for titles and text; JetBrains Mono only for code, URLs, slugs and SHAs.
  it('the heading face is the body face', () => {
    const css = readFileSync(join(src, 'index.css'), 'utf8')
    expect(css).toMatch(/--font-heading:\s*var\(--font-body-family\)/)
  })

  it('no title or figure is set in mono', () => {
    expect(offenders(monoTitle, [])).toEqual([])
    expect(offenders(monoFigure, [])).toEqual([])
  })

  // Lists inside a panel sit flush and use separators; a bordered box inside
  // a panel (the "panel in a panel" look) is not built in pages or features.
  it('pages and features do not box their lists', () => {
    const scoped = (f: string) => f.startsWith('pages/') || f.startsWith('features/')
    expect(files(src).map(rel).filter(scoped).filter(f => boxedList.test(readFileSync(join(src, f), 'utf8')))).toEqual([])
  })
})

describe('stage 2: resources the company already has', () => {
  // The kinds' contracts (which variables, which are credentials, their
  // formats and choices) live in pkg/resourcekind and reach the console
  // through GET /catalog/kinds. A copy here would drift from the server's
  // checks: no source file names a variable of a contract. The four main
  // ones (DATABASE_URL, REDIS_URL, S3_BUCKET, FILES_DIR) may appear only as
  // illustrations in the design system's components and gallery.
  const contractVariable = /\b(DATABASE_URL|PGHOST|PGPORT|PGDATABASE|PGUSER|PGPASSWORD|PGSSLMODE|REDIS_URL|FILES_DIR|S3_BUCKET|S3_ENDPOINT|S3_REGION|AWS_ACCESS_KEY_ID|AWS_SECRET_ACCESS_KEY)\b/
  const illustrations = ['components/odyssey/resources.tsx', 'pages/design-system.tsx']
  // A credential is only ever a reference to a secret store of the company's
  // clusters: only lib/connection.ts builds a connection (any `ref` key or
  // shorthand), and it never writes a secret variable as a value.
  const storeRef = /\bref\s*:|[{,]\s*ref\s*[,}]/

  it('the rules catch violations', () => {
    expect(contractVariable.test("if (v.name === 'PGPASSWORD')")).toBe(true)
    expect(contractVariable.test("{ name: 'REDIS_URL', secret: true }")).toBe(true)
    expect(storeRef.test('values.push({ variable: v, ref: { store: s, key } })')).toBe(true)
    expect(storeRef.test('values.push({ variable: v, ref: { key, store } })')).toBe(true)
    expect(storeRef.test('values.push({ variable, ref })')).toBe(true)
    expect(storeRef.test('const reference = 1; prefix: x')).toBe(false)
  })

  it('the console reads the contracts from the API, never a copy', () => {
    expect(offenders(contractVariable, illustrations)).toEqual([])
  })

  it('only lib/connection.ts builds a connection', () => {
    expect(offenders(storeRef, ['lib/connection.ts'])).toEqual([])
  })
})
