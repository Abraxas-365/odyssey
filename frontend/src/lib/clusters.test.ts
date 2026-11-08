import { describe, expect, it } from 'vitest'
import { ApiError, errorMessage } from './api'
import { AGENT_INSTALL, appCount, awaitingAgent, cidrProblem, clusterState, internalEntries, internalProblem, moveTargets, parseAllowList, secretCommand } from './clusters'
import type { Cluster } from './types'

describe('moving apps', () => {
  it('a destination whose agent is too old for a recipe says to update it, in plain words', () => {
    const text = errorMessage(new ApiError('raw', 422, 'BUSINESS', 'RESOURCES_UNSUPPORTED', { environment: 'e', resource: 'db', missing: 'spec/v1' }))
    expect(text).toMatch(/Actualiza su agente/)
    expect(text).not.toMatch(/spec\/v1|raw/)
  })
})

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

describe('cidrProblem', () => {
  it('accepts addresses and networks, IPv4 and IPv6', () => {
    for (const ok of ['203.0.113.7', '10.20.0.0/16', '192.168.1.1/32', '2001:db8::/64', '2001:db8::1', '::1', '::ffff:10.0.0.1', 'fe80::1/10']) {
      expect(cidrProblem(ok), ok).toBeUndefined()
    }
  })
  it('refuses what the server refuses, in plain words', () => {
    for (const bad of ['example.com', '10.0.0', '10.0.0.256', '10.0.0.0/33', '10.0.0.0/x', '10.0.0.0/8/1', '1:2:3', '2001:db8::/129', ':::', '1::2::3']) {
      expect(cidrProblem(bad), bad).toBeTruthy()
    }
    expect(cidrProblem('0.0.0.0/0')).toMatch(/Internet pública/)
    expect(cidrProblem('::/0')).toMatch(/Internet pública/)
  })
})

describe('parseAllowList', () => {
  it('reads one entry per line, drops blanks and repeats, and names the bad ones', () => {
    const r = parseAllowList('10.0.0.0/8\n\n 203.0.113.7 ,10.0.0.0/8\nnope\n')
    expect(r.entries).toEqual(['10.0.0.0/8', '203.0.113.7'])
    expect(r.problems.map(p => p.entry)).toEqual(['nope'])
  })
})

describe('internalProblem', () => {
  it('flags what is inside, or covers, a private or reserved range', () => {
    for (const internal of ['10.42.0.0/16', '10.43.0.1', '192.168.65.254', '172.20.0.0/14', '100.64.1.1', '169.254.169.254', '127.0.0.1', '0.0.0.0/8',
      '224.0.0.1', '240.0.0.0/4', '198.18.0.0/15', '10.0.0.0/7', '0.0.0.0/1', 'fd00::/8', 'fe80::1', '::1', '::ffff:10.0.0.1', 'ff02::1', '64:ff9b::1.2.3.4']) {
      expect(internalProblem(internal), internal).toBeTruthy()
    }
  })
  it('leaves public addresses alone', () => {
    for (const pub of ['203.0.113.7', '198.51.100.0/24', '8.8.8.8', '172.32.0.1', '172.15.255.255', '100.128.0.1', '2001:db8::/64', '2606:4700::1', '::ffff:8.8.8.8']) {
      expect(internalProblem(pub), pub).toBeUndefined()
    }
  })
  it('lists the entries to confirm', () => {
    expect(internalEntries(['203.0.113.7', '10.20.0.0/16', 'fc00::1']).map(e => e.entry)).toEqual(['10.20.0.0/16', 'fc00::1'])
    expect(internalEntries([])).toEqual([])
  })
})
