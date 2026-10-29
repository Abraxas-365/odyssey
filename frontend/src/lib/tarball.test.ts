// @vitest-environment node
// Node's Blob, CompressionStream and Response are the browser's (jsdom lacks
// Blob.stream); the archive is checked by the system's tar, as a third party.
import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import { isEnvFile, packSource, planProblem, planSource, SOURCE_LIMITS } from './tarball'

const file = (path: string, body = `contents of ${path}`) => Object.assign(new Blob([body]), { webkitRelativePath: path, name: path.split('/').pop()! })

describe('planSource', () => {
  it('takes the folder, leaves out dependencies, caches, outputs and every .env', () => {
    const plan = planSource([
      file('visitas/package.json', '{}'),
      file('visitas/src/index.js'),
      file('visitas/.env'),
      file('visitas/config/.env.production'),
      file('visitas/node_modules/react/index.js'),
      file('visitas/.git/HEAD'),
      file('visitas/dist/bundle.js'),
      file('visitas/src/dist/keep.js'), // only top-level outputs are skipped
      file('visitas/.DS_Store'),
    ])
    expect(plan.name).toBe('visitas')
    expect(plan.files.map(f => f.path)).toEqual(['package.json', 'src/dist/keep.js', 'src/index.js'])
    expect(plan.envFiles).toEqual(['.env', 'config/.env.production'])
    expect(plan.skipped).toBe(4)
    expect(plan.size).toBe(plan.files.reduce((n, f) => n + f.size, 0))
  })

  it('keeps dist and build when the app is not JavaScript (they may be source)', () => {
    const plan = planSource([file('api/go.mod'), file('api/build/gen.go')])
    expect(plan.files.map(f => f.path)).toEqual(['build/gen.go', 'go.mod'])
  })

  it('explains why a folder cannot go up', () => {
    expect(planProblem(planSource([file('x/.env')]))).toMatch(/no tiene archivos/)
    const big = planSource([file('x/a')])
    big.size = SOURCE_LIMITS.unpacked + 1
    expect(planProblem(big)).toMatch(/500 MB/)
    expect(planProblem(planSource([file('x/a')]))).toBeUndefined()
  })

  it('recognizes .env files', () => {
    expect(['.env', '.env.local', '.env.production'].every(isEnvFile)).toBe(true)
    expect(['env', '.envrc', 'x.env', 'environment.ts'].some(isEnvFile)).toBe(false)
  })
})

describe('packSource', () => {
  let dir: string | undefined
  afterEach(() => {
    if (dir) rmSync(dir, { recursive: true, force: true })
    dir = undefined
  })

  it('makes a .tar.gz that tar extracts byte for byte, long and non-ASCII paths included', async () => {
    const long = `visitas/${'carpeta-muy-larga/'.repeat(8)}archivo.txt` // > 100 bytes: PAX
    const plan = planSource([
      file('visitas/package.json', '{"scripts":{"start":"node index.js"}}'),
      file('visitas/empty.txt', ''),
      file('visitas/señal/año.txt', 'ñandú'),
      file('visitas/block.bin', 'x'.repeat(512)), // exactly one block: no padding
      file(long, 'deep'),
    ])
    const gz = await packSource(plan)
    dir = mkdtempSync(join(tmpdir(), 'odyssey-tar-'))
    const archive = join(dir, 'source.tar.gz')
    writeFileSync(archive, Buffer.from(await gz.arrayBuffer()))
    const out = join(dir, 'out')
    execFileSync('mkdir', [out])
    execFileSync('tar', ['-xzf', archive, '-C', out])
    for (const f of plan.files) {
      expect(readFileSync(join(out, f.path), 'utf8')).toBe(await f.body.text())
    }
    // macOS's tar prints names decomposed (NFD): compare them normalized
    const listed = execFileSync('tar', ['-tzf', archive], { encoding: 'utf8' }).trim().split('\n').map(s => s.normalize('NFC')).sort()
    expect(listed).toEqual(plan.files.map(f => f.path.normalize('NFC')).sort())
  })

  it('marks the usual executables as such (browsers do not say)', async () => {
    const plan = planSource([file('app/start.sh', '#!/bin/sh\n'), file('app/gradlew'), file('app/bin/serve'), file('app/main.go')])
    const gz = await packSource(plan)
    dir = mkdtempSync(join(tmpdir(), 'odyssey-tar-'))
    const archive = join(dir, 'source.tar.gz')
    writeFileSync(archive, Buffer.from(await gz.arrayBuffer()))
    const modes = Object.fromEntries(execFileSync('tar', ['-tvzf', archive], { encoding: 'utf8' }).trim().split('\n')
      .map(l => [l.trim().split(/\s+/).pop()!, l.slice(0, 10)]))
    expect(modes).toEqual({ 'start.sh': '-rwxr-xr-x', gradlew: '-rwxr-xr-x', 'bin/serve': '-rwxr-xr-x', 'main.go': '-rw-r--r--' })
  })
})
