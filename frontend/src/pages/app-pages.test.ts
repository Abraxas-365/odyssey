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
