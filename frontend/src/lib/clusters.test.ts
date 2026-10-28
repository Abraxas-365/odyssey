import { describe, expect, it } from 'vitest'
import { AGENT_INSTALL, appCount, awaitingAgent, clusterState, moveTargets, secretCommand } from './clusters'
import type { Cluster } from './types'

describe('moveTargets', () => {
  const c = (id: string, status: Cluster['status']) => ({ id, status }) as Cluster
  it('offers only the other active clusters', () => {
    const all = [c('a', 'active'), c('b', 'active'), c('p', 'pending'), c('r', 'revoked')]
    expect(moveTargets({ id: 'a' }, all).map(t => t.id)).toEqual(['b'])
    expect(moveTargets({ id: 'r' }, all).map(t => t.id)).toEqual(['a', 'b'])
  })
  it('counts apps in plain words', () => {
    expect(appCount(1)).toBe('1 app')
    expect(appCount(3)).toBe('3 apps')
  })
})

describe('clusterState', () => {
  it('maps status and connection to the four states TI sees', () => {
    expect(clusterState({ status: 'pending', connected: false })).toBe('waiting')
    expect(clusterState({ status: 'active', connected: true })).toBe('connected')
    expect(clusterState({ status: 'active', connected: false })).toBe('offline')
    expect(clusterState({ status: 'revoked', connected: true })).toBe('revoked')
  })
})

describe('awaitingAgent', () => {
  const now = Date.parse('2026-10-28T12:00:00Z')
  it('waits only for pending clusters whose token still works', () => {
    expect(awaitingAgent({ status: 'pending', token_expires_at: '2026-10-28T13:00:00Z' }, now)).toBe(true)
    expect(awaitingAgent({ status: 'pending', token_expires_at: '2026-10-28T11:00:00Z' }, now)).toBe(false)
    expect(awaitingAgent({ status: 'pending', token_expires_at: null }, now)).toBe(false)
    expect(awaitingAgent({ status: 'active', token_expires_at: '2026-10-28T13:00:00Z' }, now)).toBe(false)
  })
})

describe('secretCommand', () => {
  it('stores exactly the keys the agent reads, idempotently, without the token in argv', () => {
    const cmd = secretCommand({ token: 'odyssey-enroll-abc', gateway: 'gw.example:8443', ca_pin: 'sha256:00ff', expires_at: '2026-10-29T00:00:00Z' })
    expect(cmd).toContain('-n odyssey-system create secret generic odyssey-agent')
    expect(cmd).toContain(`printf '%s' 'odyssey-enroll-abc' | kubectl`)
    expect(cmd).toContain('--from-file=token=/dev/stdin')
    expect(cmd).not.toContain('--from-literal=token=')
    expect(cmd).toContain('--from-literal=gateway=gw.example:8443')
    expect(cmd).toContain('--from-literal=ca-pin=sha256:00ff')
    // re-running it (a new token) updates the Secret instead of failing
    expect(cmd.match(/--dry-run=client -o yaml \| kubectl apply -f -/g)).toHaveLength(2)
    expect(AGENT_INSTALL).toBe('kubectl apply -k agent/install')
  })
})
