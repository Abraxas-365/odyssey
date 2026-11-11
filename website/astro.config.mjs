import { defineConfig } from 'astro/config'
import starlight from '@astrojs/starlight'
import { unified } from '@astrojs/markdown-remark'
import { remarkDocs, repository, site, base } from './scripts/docs.mjs'
import { sidebar } from './src/sidebar.mjs'

export default defineConfig({
  // GitHub Pages serves the project at https://abraxas-365.github.io/odyssey/ (SITE_BASE=/odyssey in CI).
  site,
  base,
  trailingSlash: 'always',
  markdown: { processor: unified({ remarkPlugins: [remarkDocs] }) },
  integrations: [starlight({
    title: 'Odicea',
    description: 'A governed platform for your company\'s internal apps: people publish with their coding assistant, IT sets the rules, and apps run on the company\'s own clusters.',
    logo: { src: './src/assets/odicea-mark.svg' },
    favicon: '/favicon.svg',
    social: [{ icon: 'github', label: 'GitHub', href: repository }],
    customCss: ['./src/styles/theme.css'],
    expressiveCode: { themes: ['github-dark-dimmed', 'github-light'], styleOverrides: { borderRadius: '0.5rem' } },
    sidebar,
  })],
})
