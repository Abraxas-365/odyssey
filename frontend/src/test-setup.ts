import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

// Tests in the node environment (`// @vitest-environment node`) have no DOM.
const dom = typeof window !== 'undefined'

afterEach(() => {
  if (!dom) return
  cleanup()
  localStorage.clear()
  sessionStorage.clear()
})

// jsdom lacks these; base-ui and next-themes touch them.
if (dom) {
  window.matchMedia ??= (query: string) =>
    ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false }) as MediaQueryList
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} }
  Element.prototype.scrollIntoView ??= function () {}
}
