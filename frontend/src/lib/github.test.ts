import { describe, expect, it } from 'vitest'
import { ApiError, errorMessage } from './api'
import { githubErrorText, githubMessages } from './github-errors'
import { connectBody, DEFAULT_PORT, manageOnGitHubURL, pushActive, safeReturnTo } from './github'
import type { GitHubRepository } from './types'

describe('GitHub error codes', () => {
  it('map to plain Spanish', () => {
    expect(githubErrorText('GITHUB_STATE_INVALID')).toMatch(/venció/)
    expect(githubErrorText('GITHUB_INSTALLATION_TAKEN')).toMatch(/otra organización de Odicea/)
    expect(githubErrorText('GITHUB_NOT_YOURS')).toMatch(/dueño/)
    expect(githubErrorText('GITHUB_NOT_CONFIGURED')).toMatch(/Pide a TI/)
    expect(githubErrorText('GITHUB_UNAVAILABLE')).toMatch(/no respondió/)
    expect(githubErrorText('algo-nuevo')).toMatch(/de nuestro lado/)
  })
  it('are the words errorMessage shows for an API error with that code', () => {
    const e = new ApiError('raw', 409, 'CONFLICT', 'GITHUB_INSTALLATION_TAKEN')
    expect(errorMessage(e)).toBe(githubMessages.GITHUB_INSTALLATION_TAKEN)
    expect(errorMessage(new ApiError('raw', 422, 'BUSINESS', 'GITHUB_STATE_INVALID'))).toBe(githubMessages.GITHUB_STATE_INVALID)
  })
})

describe('GitHub links', () => {
  it('manage on GitHub: the user settings, or the organization’s', () => {
    expect(manageOnGitHubURL({ account: 'ana', account_type: 'User' })).toBe('https://github.com/settings/installations')
    expect(manageOnGitHubURL({ account: 'acme corp', account_type: 'Organization' })).toBe('https://github.com/organizations/acme%20corp/settings/installations')
  })
  it('return_to only stays inside the console', () => {
    expect(safeReturnTo('/apps/1')).toBe('/apps/1')
    expect(safeReturnTo('//evil.example')).toBe('/configuracion/github')
    expect(safeReturnTo('https://evil.example')).toBe('/configuracion/github')
    expect(safeReturnTo(undefined)).toBe('/configuracion/github')
  })
})

describe('connecting', () => {
  const repo = { id: 42, full_name: 'acme/visitas', default_branch: 'main', private: true, installation_id: 'inst' } as GitHubRepository
  it('sends the installation, repository id, branch and trimmed folder', () => {
    expect(connectBody(repo, 'dev', ' apps/web ')).toEqual({ installation_id: 'inst', repository_id: 42, branch: 'dev', root: 'apps/web', port: DEFAULT_PORT })
  })
  it('polls only while a push is on its way', () => {
    expect(['queued', 'importing', 'deployed', 'failed', 'skipped', undefined].map(s => pushActive(s as never))).toEqual([true, true, false, false, false, false])
  })
})
