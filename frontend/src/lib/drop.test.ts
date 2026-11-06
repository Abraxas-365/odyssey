import { describe, expect, it } from 'vitest'
import { dropProblems, readDrop, type DropEntry } from './drop'
import { planSource } from './tarball'

const file = (name: string, body = name): DropEntry => ({
  name, isFile: true, isDirectory: false,
  file: ok => ok(new File([body], name)),
})

/** A folder whose reader answers in batches of two, then an empty one. */
function dir(name: string, children: DropEntry[], opened: string[] = []): DropEntry {
  return {
    name, isFile: false, isDirectory: true,
    createReader: () => {
      opened.push(name)
      let at = 0
      return { readEntries: ok => { const batch = children.slice(at, at + 2); at += 2; ok(batch) } }
    },
  }
}

describe('readDrop', () => {
  it('reads one folder recursively, as a picked folder, without opening dependencies', async () => {
    const opened: string[] = []
    const root = dir('visitas', [
      file('package.json', '{}'),
      dir('src', [file('index.js'), file('app.js'), file('util.js')], opened),
      dir('node_modules', [file('never.js')], opened),
      file('.env'),
    ], opened)
    const got = await readDrop([root])
    if (!('files' in got)) throw new Error(got.problem)
    expect(got.files.map(f => f.webkitRelativePath).sort()).toEqual(['visitas/.env', 'visitas/package.json', 'visitas/src/app.js', 'visitas/src/index.js', 'visitas/src/util.js'])
    expect(opened).not.toContain('node_modules')
    // planSource sees what it sees for a picked folder
    const plan = planSource(got.files)
    expect(plan.name).toBe('visitas')
    expect(plan.envFiles).toEqual(['.env'])
    expect(plan.files.map(f => f.path)).toEqual(['package.json', 'src/app.js', 'src/index.js', 'src/util.js'])
  })

  it('refuses loose files, several folders and nothing', async () => {
    expect(await readDrop([file('a.js')])).toEqual({ problem: dropProblems.notFolder })
    expect(await readDrop([dir('a', []), dir('b', [])])).toEqual({ problem: dropProblems.many })
    expect(await readDrop([null])).toEqual({ problem: dropProblems.notFolder })
  })

  it('says so when the browser cannot read it', async () => {
    const broken: DropEntry = { name: 'x', isFile: false, isDirectory: true, createReader: () => ({ readEntries: (_ok, fail) => fail(new Error('denied')) }) }
    expect(await readDrop([broken])).toEqual({ problem: dropProblems.unreadable })
  })
})
