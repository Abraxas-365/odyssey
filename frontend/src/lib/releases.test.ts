// @vitest-environment node
// (WebCrypto rejects jsdom's ArrayBuffers.)
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { Aes256Gcm, CipherSuite, DhkemX25519HkdfSha256, HkdfSha256 } from '@hpke/core'
import { fromBase64, secretInfo } from './secretbox'

const calls: { path: string; options?: { method?: string; body?: unknown } }[] = []
let settings: unknown
vi.mock('./api', async orig => ({
  ...(await orig<typeof import('./api')>()),
  api: vi.fn(async (path: string, options?: { method?: string; body?: unknown }) => {
    calls.push({ path, options })
    return path.endsWith('/settings') ? settings : { settings: {}, deployment: null }
  }),
}))
const { loadSecretValue, currentPollMs, deploymentTurned, CURRENT_IDLE_POLL_MS, PUBLISHING_POLL_MS } = await import('./releases')

// pkg/secretbox's fixed test key (vector_test.go).
const PRIVATE = 'AAgPFh0kKzI5QEdOVVxjanF4f4aNlJuiqbC3vsXM01o='
const PUBLIC = 'yP7Kgb4ZbN8sreq/E8SQPXYy3OSVWqaLbl2a3vVOJhY='
const suite = new CipherSuite({ kem: new DhkemX25519HkdfSha256(), kdf: new HkdfSha256(), aead: new Aes256Gcm() })
const key = { cluster_id: 'c-1', key_id: 'kid-1', public_key: PUBLIC, app: 'portal' }

describe('loadSecretValue', () => {
  beforeEach(() => {
    calls.length = 0
  })

  it('sends only the value sealed to the cluster, for the next version', async () => {
    settings = { secret_key: key, secrets: [{ name: 'API_KEY', source: 'managed', state: 'loaded', version: 3, next_version: 4 }] }
    await loadSecretValue('p-1', 'e-1', 'API_KEY', 'sk_live_very_secret')
    const put = calls.find(c => c.options?.method === 'PUT')!
    expect(put.path).toBe('/projects/p-1/environments/e-1/secrets/API_KEY/value')
    const body = put.options!.body as { version: number; key_id: string; sealed: string }
    expect(body.version).toBe(4)
    expect(body.key_id).toBe('kid-1')
    expect(Object.keys(body).sort()).toEqual(['key_id', 'sealed', 'version'])
    expect(JSON.stringify(calls)).not.toContain('sk_live_very_secret')
    // and the cluster opens it, bound to its own target
    const sealed = fromBase64(body.sealed)
    const recipientKey = await suite.kem.deserializePrivateKey(fromBase64(PRIVATE).slice().buffer)
    const info = secretInfo({ cluster: 'c-1', environment: 'e-1', app: 'portal', name: 'API_KEY', version: 4 }).slice().buffer
    const pt = await suite.open({ recipientKey, enc: sealed.slice(0, 32).buffer, info }, sealed.slice(32).buffer)
    expect(new TextDecoder().decode(pt)).toBe('sk_live_very_secret')
  })

  it('starts at version 1 for a requested secret', async () => {
    settings = { secret_key: key, secrets: [{ name: 'NEW', source: 'managed', state: 'missing', next_version: 1 }] }
    await loadSecretValue('p', 'e', 'NEW', 'x')
    expect((calls.at(-1)!.options!.body as { version: number }).version).toBe(1)
  })

  it('keeps counting for a secret removed and added again (the API\'s next_version)', async () => {
    settings = { secret_key: key, secrets: [{ name: 'AGAIN', source: 'managed', state: 'missing', next_version: 6 }] }
    await loadSecretValue('p', 'e', 'AGAIN', 'x')
    expect((calls.at(-1)!.options!.body as { version: number }).version).toBe(6)
  })

  it('says so, and sends nothing, when the secret no longer exists', async () => {
    settings = { secret_key: key, secrets: [{ name: 'OTHER', source: 'managed', state: 'loaded', version: 1, next_version: 2 }] }
    await expect(loadSecretValue('p', 'e', 'GONE', 'x')).rejects.toMatchObject({
      status: 404, code: 'NOT_FOUND', message: 'Este secreto ya no existe. Recarga la página.',
    })
    expect(calls.some(c => c.options?.method === 'PUT')).toBe(false)
  })

  it('refuses to send anything without a cluster key', async () => {
    settings = { secret_key: null, secrets: [] }
    await expect(loadSecretValue('p', 'e', 'X', 'x')).rejects.toMatchObject({ code: 'SECRETS_UNAVAILABLE' })
    expect(calls.some(c => c.options?.method === 'PUT')).toBe(false)
  })
})

describe('currentPollMs', () => {
  it('polls fast while a deployment is in progress', () => {
    expect(currentPollMs({ active: { id: 'd-1' } } as never)).toBe(PUBLISHING_POLL_MS)
  })

  it('keeps polling slowly when nothing is in progress, so a deployment started elsewhere (an assistant, a push) shows up', () => {
    expect(currentPollMs({ active: null } as never)).toBe(CURRENT_IDLE_POLL_MS)
    expect(currentPollMs(undefined)).toBe(CURRENT_IDLE_POLL_MS)
    expect(CURRENT_IDLE_POLL_MS).toBeGreaterThan(PUBLISHING_POLL_MS)
  })
})

describe('deploymentTurned', () => {
  const at = (id: string | null) => ({ active: id ? { id } : null }) as never

  it('refreshes the environment when a deployment starts or settles, wherever it came from', () => {
    expect(deploymentTurned(null, at('d-1'))).toBe(true)
    expect(deploymentTurned('d-1', at(null))).toBe(true)
    // one replaced by another between two answers
    expect(deploymentTurned('d-1', at('d-2'))).toBe(true)
  })

  it('does nothing on the first answer or while nothing changes', () => {
    expect(deploymentTurned(undefined, at('d-1'))).toBe(false)
    expect(deploymentTurned(null, at(null))).toBe(false)
    expect(deploymentTurned('d-1', at('d-1'))).toBe(false)
    expect(deploymentTurned('d-1', undefined)).toBe(false)
  })
})
