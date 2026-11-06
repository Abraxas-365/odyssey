import { isSkippedDir, type PickedFile } from './tarball'

/**
 * A folder dropped on the page (UX v4 §5.3 "Subir una carpeta"): the browser
 * hands over entries, not files. They are read into the same shape a picked
 * folder has (`webkitRelativePath` = "folder/sub/file"), so `planSource`
 * decides what goes up either way. Folders that never go up (node_modules,
 * .git…) are not even opened: a JavaScript app's dependencies can be
 * hundreds of thousands of files.
 */

/** The parts of the File and Directory Entries API the reader uses (tests fake them). */
export interface DropEntry {
  name: string
  isFile: boolean
  isDirectory: boolean
  file?: (ok: (f: File) => void, fail: (e: unknown) => void) => void
  createReader?: () => { readEntries: (ok: (entries: DropEntry[]) => void, fail: (e: unknown) => void) => void }
}

export type DroppedFile = File & PickedFile

/** What was dropped: one folder, or something else the reader refuses. */
export type DropResult = { files: DroppedFile[] } | { problem: string }

export const dropProblems = {
  notFolder: 'Arrastra la carpeta de tu app, no archivos sueltos.',
  many: 'Arrastra una sola carpeta: la de tu app.',
  unreadable: 'No pudimos leer la carpeta en este navegador. Usa «Elegir carpeta».',
} as const

/** Reads the dropped entries: exactly one folder, recursively. */
export async function readDrop(entries: (DropEntry | null)[]): Promise<DropResult> {
  const real = entries.filter((e): e is DropEntry => !!e)
  if (real.length > 1) return { problem: dropProblems.many }
  const root = real[0]
  if (!root || !root.isDirectory) return { problem: dropProblems.notFolder }
  const files: DroppedFile[] = []
  try {
    await walk(root, root.name, files)
  } catch {
    return { problem: dropProblems.unreadable }
  }
  return { files }
}

async function walk(entry: DropEntry, path: string, out: DroppedFile[]): Promise<void> {
  if (entry.isFile && entry.file) {
    const f = await new Promise<File>((ok, fail) => entry.file!(ok, fail))
    // webkitRelativePath is a read-only getter on File: shadow it on this one
    out.push(Object.defineProperty(f, 'webkitRelativePath', { value: path }) as DroppedFile)
    return
  }
  if (!entry.isDirectory || !entry.createReader) return
  const reader = entry.createReader()
  // readEntries answers in batches (about 100 in Chrome) until an empty one
  for (;;) {
    const batch = await new Promise<DropEntry[]>((ok, fail) => reader.readEntries(ok, fail))
    if (batch.length === 0) return
    for (const child of batch) {
      if (child.isDirectory && isSkippedDir(child.name)) continue
      await walk(child, `${path}/${child.name}`, out)
    }
  }
}

/** The entries of a drop event (`webkitGetAsEntry`, in every current browser). */
export function dropEntries(items: DataTransferItemList): (DropEntry | null)[] {
  return Array.from(items)
    .filter(i => i.kind === 'file')
    .map(i => (i.webkitGetAsEntry?.() ?? null) as DropEntry | null)
}
