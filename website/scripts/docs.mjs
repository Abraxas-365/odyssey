import { readdirSync, readFileSync, existsSync, statSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { visit } from 'unist-util-visit'

export const root = fileURLToPath(new URL('../../', import.meta.url))
export const docsRoot = path.join(root, 'docs')
export const repository = 'https://github.com/Abraxas-365/odyssey'
// Public URL; SITE_BASE is '/odyssey' on GitHub Pages and '/' locally.
export const site = process.env.SITE_URL ?? 'https://abraxas-365.github.io'
export const base = `/${(process.env.SITE_BASE ?? '').replace(/^\/+|\/+$/g, '')}/`.replace('//', '/')

export function docId(file) {
  return `docs/${file.replace(/\\/g, '/').replace(/\.md$/, '').replace(/(^|\/)(index|README)$/, '')}`.replace(/\/$/, '')
}

// One curated sidebar entry; the label defaults to the page's own title.
export function page(source, label) {
  const file = path.join(docsRoot, `${source}.md`)
  return { label: label ?? metadata(file).title, slug: docId(`${source}.md`) }
}

export function group(label, items, collapsed = true) {
  return { label, collapsed, items }
}

// Every published Markdown document, as slugs, to prove the curated sidebar leaves none out.
export function allSlugs() {
  return readdirSync(docsRoot, { recursive: true })
    .filter(file => file.endsWith('.md'))
    .map(file => docId(file))
}

export function sidebarSlugs(items) {
  return items.flatMap(item => item.items ? sidebarSlugs(item.items) : [item.slug])
}

export function metadata(file) {
  const source = readFileSync(file, 'utf8')
  const title = source.match(/^# (.+)$/m)?.[1]
  if (!title) throw new Error(`Missing document title: ${file}`)
  const paragraph = source.slice(source.indexOf('\n') + 1).trim().split(/\n\s*\n/)[0]
  const description = paragraph.replace(/\[([^\]]+)\]\([^)]+\)/g, '$1').replace(/[`*_]/g, '').replace(/\s+/g, ' ').slice(0, 160)
  return { title, description, editUrl: `${repository}/edit/main/${path.relative(root, file).split(path.sep).join('/')}` }
}

// Preserve GitHub-friendly source links; only their rendered website URLs change.
export function resolveLink(url, file) {
  if (/^(?:[a-z][a-z\d+.-]*:|\/|#)/i.test(url)) return url
  const split = url.search(/[?#]/)
  const pathname = split === -1 ? url : url.slice(0, split)
  const suffix = split === -1 ? '' : url.slice(split)
  const target = path.resolve(path.dirname(file), decodeURIComponent(pathname))
  const relative = path.relative(root, target).split(path.sep).join('/')
  if (relative.startsWith('../') || !existsSync(target)) throw new Error(`Broken source link in ${file}: ${url}`)
  if (target.startsWith(docsRoot + path.sep) && target.endsWith('.md')) {
    return `${base}${docId(path.relative(docsRoot, target))}/${suffix}`
  }
  const kind = statSync(target).isDirectory() ? 'tree' : 'blob'
  return `${repository}/${kind}/main/${relative}${suffix}`
}

const alerts = { NOTE: 'note', TIP: 'tip', IMPORTANT: 'note', WARNING: 'caution', CAUTION: 'danger' }

// GitHub alerts (`> [!NOTE]` …) render as Starlight asides, so one source reads well in both places.
export function alertOf(node) {
  if (node.type !== 'blockquote') return null
  const text = node.children[0]?.type === 'paragraph' ? node.children[0].children[0] : null
  const match = text?.type === 'text' && text.value.match(/^\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]\s*/)
  if (!match) return null
  text.value = text.value.slice(match[0].length)
  const label = match[1][0] + match[1].slice(1).toLowerCase()
  return { kind: alerts[match[1]], label }
}

function aside(node, { kind, label }) {
  const first = node.children[0]
  if (first.children[0].value === '') first.children.shift()
  if (first.children[0]?.type === 'break') first.children.shift()
  if (first.children.length === 0) node.children.shift()
  node.data = { hName: 'aside', hProperties: { 'aria-label': label, className: ['starlight-aside', `starlight-aside--${kind}`] } }
  node.children = [
    { type: 'paragraph', data: { hProperties: { className: ['starlight-aside__title'], ariaHidden: 'true' } }, children: [{ type: 'text', value: label }] },
    { type: 'blockquote', data: { hName: 'div', hProperties: { className: ['starlight-aside__content'] } }, children: node.children },
  ]
}

export function remarkDocs() {
  return (tree, file) => {
    if (!file.path?.startsWith(docsRoot + path.sep)) return
    // Starlight renders the title as #_top; avoid a duplicate H1.
    const heading = tree.children.find(node => node.type === 'heading' && node.depth === 1)
    if (heading) tree.children.splice(tree.children.indexOf(heading), 1)
    visit(tree, node => {
      if (node.type === 'link' || node.type === 'definition') node.url = resolveLink(node.url, file.path)
      const alert = alertOf(node)
      if (alert) aside(node, alert)
      // Images are left relative so Astro fingerprints the docs/assets screenshots.
    })
  }
}
