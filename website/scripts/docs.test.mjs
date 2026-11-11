import { test } from 'node:test'
import assert from 'node:assert/strict'
import path from 'node:path'
import { unified } from 'unified'
import remarkParse from 'remark-parse'
import { docId, docsRoot, metadata, resolveLink, repository, base, allSlugs, sidebarSlugs, alertOf } from './docs.mjs'

const tutorial = path.join(docsRoot, 'tutorials/first-app.md')
test('routes preserve directories and collapse README/index', () => {
  assert.equal(docId('README.md'), 'docs')
  assert.equal(docId('contributing/README.md'), 'docs/contributing')
  assert.equal(docId('guides/it/rules.md'), 'docs/guides/it/rules')
})
test('metadata comes from source Markdown and edit link points to source', () => {
  const data = metadata(tutorial)
  assert.equal(data.title, 'Your first app')
  assert.ok(data.description.length > 20 && data.description.length <= 160)
  assert.equal(data.editUrl, `${repository}/edit/main/docs/tutorials/first-app.md`)
})
test('rewrites document links while preserving anchors and external URLs', () => {
  assert.equal(base, process.env.SITE_BASE ? `/${process.env.SITE_BASE.replace(/^\/+|\/+$/g, '')}/` : '/')
  const b = base.slice(0, -1)
  assert.equal(resolveLink('../guides/publish.md', tutorial), `${b}/docs/guides/publish/`)
  assert.equal(resolveLink('../reference/configuration.md#api', tutorial), `${b}/docs/reference/configuration/#api`)
  assert.equal(resolveLink('#before-you-start', tutorial), '#before-you-start')
  assert.equal(resolveLink('https://example.com/a.md', tutorial), 'https://example.com/a.md')
  assert.equal(resolveLink('../README.md', tutorial), `${b}/docs/`)
  assert.equal(resolveLink('../../AGENTS.md', tutorial), `${repository}/blob/main/AGENTS.md`)
  assert.equal(resolveLink('../../deploy/', tutorial), `${repository}/tree/main/deploy`)
})
test('curated sidebar lists every document exactly once', async () => {
  const { sidebar } = await import('../src/sidebar.mjs')
  const listed = sidebarSlugs(sidebar)
  assert.deepEqual([...new Set(listed)].sort(), allSlugs().sort())
  assert.equal(listed.length, new Set(listed).size, 'a document appears twice in the sidebar')
})
test('fails on missing source files rather than publishing broken links', () => {
  assert.throws(() => resolveLink('../missing.md', tutorial), /Broken source link/)
})
test('GitHub alerts become asides; plain quotes stay quotes', () => {
  const [note] = unified().use(remarkParse).parse('> [!WARNING]\n> Keep the key.').children
  assert.deepEqual(alertOf(note), { kind: 'caution', label: 'Warning' })
  const [quote] = unified().use(remarkParse).parse('> Just a quote.').children
  assert.equal(alertOf(quote), null)
})
