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
