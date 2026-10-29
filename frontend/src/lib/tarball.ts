/**
 * "Desde una carpeta" (plan 1b): the console packs the folder a person picks
 * into a .tar.gz in the browser. Files are referenced, never copied: the tar
 * is a Blob of headers and the picked File objects, gzipped as a stream by
 * the browser's own CompressionStream.
 *
 * The server checks the archive again (internal/build/archive): only regular
 * files, relative paths, within its limits. These limits only warn early.
 */

/** Limits of internal/build (docs/api.md §9). */
export const SOURCE_LIMITS = { compressed: 100 << 20, unpacked: 500 << 20, files: 20_000 }

/** Folders never uploaded: dependencies, VCS data, caches. Matched at any depth. */
const skippedDirs = new Set(['node_modules', '.git', '.hg', '.svn', '.next', '.nuxt', '.svelte-kit', '.turbo', '.cache', '.venv', 'venv', '__pycache__', '.pytest_cache', '.mypy_cache', '.idea', '.vscode'])
/** Build outputs, skipped at the top of a JavaScript app (they are rebuilt). */
const jsOutputs = new Set(['dist', 'build', 'out', 'coverage'])
const skippedFiles = new Set(['.DS_Store', 'Thumbs.db'])

/** `.env`, `.env.local`, … may hold passwords: never uploaded (variables are configured apart). */
export const isEnvFile = (name: string) => name === '.env' || name.startsWith('.env.')

export interface SourceFile {
  /** Path inside the folder, `/`-separated, without the folder's own name. */
  path: string
  size: number
  body: Blob
}

export interface SourcePlan {
  /** The picked folder's name (names the source). */
  name: string
  files: SourceFile[]
  /** Bytes of the files that go up (before compression). */
  size: number
  /** Files left out (dependencies, caches, outputs). */
  skipped: number
  /** .env files left out. */
  envFiles: string[]
}

/** A picked file: `webkitRelativePath` is "folder/sub/file". */
export interface PickedFile {
  webkitRelativePath: string
  name: string
  size: number
}

/** Decides what of a picked folder goes up. */
export function planSource<F extends Blob & PickedFile>(picked: F[]): SourcePlan {
  const rel = picked.map(f => ({ f, parts: (f.webkitRelativePath || f.name).split('/').filter(Boolean) }))
  const name = rel[0]?.parts.length && rel[0].parts.length > 1 ? rel[0].parts[0] : 'app'
  const inFolder = rel.map(r => ({ f: r.f, parts: r.parts.length > 1 ? r.parts.slice(1) : r.parts }))
  const js = inFolder.some(r => r.parts.length === 1 && r.parts[0] === 'package.json')
  const plan: SourcePlan = { name, files: [], size: 0, skipped: 0, envFiles: [] }
  for (const { f, parts } of inFolder) {
    const file = parts[parts.length - 1]
    const dirs = parts.slice(0, -1)
    if (isEnvFile(file)) {
      plan.envFiles.push(parts.join('/'))
      continue
    }
    if (skippedFiles.has(file) || dirs.some(d => skippedDirs.has(d)) || (js && dirs.length > 0 && jsOutputs.has(dirs[0]))) {
      plan.skipped++
      continue
    }
    plan.files.push({ path: parts.join('/'), size: f.size, body: f })
    plan.size += f.size
  }
  plan.files.sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0))
  return plan
}

/** Why a plan cannot go up, in plain Spanish (undefined: it can). */
export function planProblem(plan: SourcePlan): string | undefined {
  if (plan.files.length === 0) return 'La carpeta no tiene archivos para publicar.'
  if (plan.files.length > SOURCE_LIMITS.files) return `La carpeta tiene más de ${SOURCE_LIMITS.files.toLocaleString('es-PE')} archivos. Elige solo la carpeta de la app.`
  if (plan.size > SOURCE_LIMITS.unpacked) return 'La carpeta pesa más de 500 MB. Elige solo la carpeta de la app, sin videos ni respaldos.'
  return undefined
}

// ---- tar (POSIX ustar + PAX for long or non-ASCII paths) ----

const BLOCK = 512
const enc = new TextEncoder()

function octal(n: number, width: number): string {
  return n.toString(8).padStart(width - 1, '0') + '\0'
}

function header(name: string, size: number, type: '0' | 'x', mtime: number, mode = 0o644): Uint8Array<ArrayBuffer> {
  const h = new Uint8Array(BLOCK)
  const put = (s: string, at: number, len: number) => h.set(enc.encode(s).subarray(0, len), at)
  put(name, 0, 100)
  put(octal(mode, 8), 100, 8)
  put(octal(0, 8), 108, 8) // uid
  put(octal(0, 8), 116, 8) // gid
  put(octal(size, 12), 124, 12)
  put(octal(mtime, 12), 136, 12)
  put('        ', 148, 8) // checksum placeholder
  put(type, 156, 1)
  put('ustar\0', 257, 6)
  put('00', 263, 2)
  let sum = 0
  for (const b of h) sum += b
  put(sum.toString(8).padStart(6, '0') + '\0 ', 148, 8)
  return h
}

const padding = (size: number) => new Uint8Array((BLOCK - (size % BLOCK)) % BLOCK)

/** A PAX record "<len> path=<value>\n", where len counts the whole record. */
function paxRecord(key: string, value: string): Uint8Array<ArrayBuffer> {
  const body = ` ${key}=${value}\n`
  const bodyLen = enc.encode(body).length
  let len = bodyLen + 1
  while (String(len).length + bodyLen !== len) len = String(len).length + bodyLen
  return enc.encode(len + body)
}

const asciiShort = (path: string) => path.length <= 100 && /^[\x20-\x7e]+$/.test(path)

/**
 * Browsers do not say which files are executable, so the usual ones are
 * guessed by name: shell scripts, the Gradle/Maven wrappers and anything in a
 * bin/ folder (a start script would otherwise fail with "permission denied").
 */
export function isExecutable(path: string): boolean {
  const base = path.slice(path.lastIndexOf('/') + 1)
  return /\.(sh|bash)$/.test(base) || base === 'gradlew' || base === 'mvnw' || /(^|\/)bin\/[^/]+$/.test(path)
}

/**
 * The tar of files, as Blob parts (headers + the files themselves + padding
 * + the two zero blocks that end an archive).
 */
export function tarParts(files: SourceFile[], mtime = Math.floor(Date.now() / 1000)): BlobPart[] {
  const parts: BlobPart[] = []
  for (const f of files) {
    let name = f.path
    if (!asciiShort(f.path)) {
      const rec = paxRecord('path', f.path)
      parts.push(header('PaxHeaders/entry', rec.length, 'x', mtime), rec, padding(rec.length))
      name = f.path.replace(/[^\x20-\x7e]/g, '_').slice(-100)
    }
    parts.push(header(name, f.size, '0', mtime, isExecutable(f.path) ? 0o755 : 0o644), f.body, padding(f.size))
  }
  parts.push(new Uint8Array(BLOCK * 2))
  return parts
}

/** The folder as a .tar.gz, ready to upload. */
export async function packSource(plan: SourcePlan): Promise<Blob> {
  const tar = new Blob(tarParts(plan.files))
  const gz = tar.stream().pipeThrough(new CompressionStream('gzip'))
  return new Response(gz).blob()
}
