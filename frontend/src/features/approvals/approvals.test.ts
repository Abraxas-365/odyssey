import { describe, expect, it } from 'vitest'
import { approvalsQuery, isPending, inboxFilter } from '@/lib/approvals'
import { errorMessage, ApiError } from '@/lib/api'
import type { Approval } from '@/lib/types'
import { bellText, canCancel, canDecide, countLabel, expiryText, needsAnotherTry, outcomeText, requesterName, ruleNames, statusInfo, tabStatuses } from './approvals'
import { bandejaFilter } from '@/pages/bandeja'

const NOW = Date.parse('2026-10-27T12:00:00Z')

const approval = (over: Partial<Approval> = {}): Approval => ({
  id: 'a1',
  action: 'set_access',
  project_id: 'p',
  environment_id: 'e',
  summary: 'Hacer pública «Inventario»',
  payload: {},
  requested_by: 'u1',
  reason: 'Hacer pública una app requiere aprobación de TI',
  rules: [{ id: 'r1', name: 'Hacer pública una app requiere aprobación de TI' }],
  approvers: ['odyssey:approvals:decide'],
  status: 'pending',
  decided_by: null,
  decided_at: null,
  decision_note: '',
  expires_at: '2026-11-03T12:00:00Z',
  consumed_at: null,
  created_at: '2026-10-27T11:00:00Z',
  mine: false,
  can_decide: true,
  ...over,
})

describe('the 202 of a held action', () => {
  it('is recognised by its status, and nothing else is', () => {
    expect(isPending({ status: 'pending_approval', request_id: 'x', message: 'm' })).toBe(true)
    expect(isPending({ deployment_id: null, status: 'ok' })).toBe(false)
    expect(isPending(null)).toBe(false)
    expect(isPending(undefined)).toBe(false)
    expect(isPending('pending_approval')).toBe(false)
  })
})

describe('GET /approvals query', () => {
  it('repeats status and keeps only the true booleans', () => {
    expect(approvalsQuery({})).toBe('')
    expect(approvalsQuery({ status: ['approved', 'rejected'], mine: true })).toBe('?status=approved&status=rejected&mine=true')
    expect(approvalsQuery({ decidable: false, mine: false })).toBe('')
    expect(approvalsQuery(inboxFilter)).toBe('?status=pending&decidable=true&limit=5')
  })

  it('asks each Bandeja tab for the right requests', () => {
    expect(bandejaFilter('inbox', 'pending')).toEqual({ decidable: true, status: ['pending'] })
    expect(bandejaFilter('mine', 'pending')).toEqual({ mine: true, status: ['pending'] })
    // decided ones: everything that is not pending, whoever may see them
    expect(bandejaFilter('inbox', 'decided').status).toEqual(tabStatuses.decided)
    expect(tabStatuses.decided).not.toContain('pending')
    expect(bandejaFilter('inbox', 'decided').decidable).toBeUndefined()
  })
})

describe('approval words', () => {
  it('says who asked without ids', () => {
    expect(requesterName(approval({ mine: true }))).toBe('Tú')
    expect(requesterName(approval())).toBe('Alguien de tu equipo')
    expect(requesterName(approval({ requested_by: '', requested_system: 'github:ana' }))).toBe('Un cambio en GitHub de ana')
    expect(requesterName(approval({ requested_by: '', requested_system: 'github' }))).toBe('Un cambio en GitHub')
  })

  it('names the rules that asked', () => {
    expect(ruleNames({ rules: [] })).toBe('')
    expect(ruleNames({ rules: [{ id: 'a', name: 'A' }] })).toBe('«A»')
    expect(ruleNames({ rules: [{ id: 'a', name: 'A' }, { id: 'b', name: 'B' }, { id: 'c', name: 'C' }] })).toBe('«A», «B» y «C»')
  })

  it('tells when it expires only while pending', () => {
    expect(expiryText(approval(), NOW)).toMatch(/^Vence /)
    expect(expiryText(approval({ expires_at: '2026-10-27T11:59:00Z' }), NOW)).toBe('Vence en un momento')
    expect(expiryText(approval({ status: 'approved' }), NOW)).toBe('')
  })

  it('tells how a decided one ended', () => {
    const at = '2026-10-27T10:00:00Z'
    expect(outcomeText(approval({ status: 'pending' }), NOW)).toBe('')
    expect(outcomeText(approval({ status: 'approved', decided_at: at }), NOW)).toMatch(/^Aprobada por TI · /)
    expect(outcomeText(approval({ status: 'rejected', decided_at: at }), NOW)).toMatch(/^Rechazada por TI · /)
    expect(outcomeText(approval({ status: 'expired' }), NOW)).toMatch(/^Nadie la decidió a tiempo · /)
    expect(outcomeText(approval({ status: 'cancelled', mine: true, decided_at: at }), NOW)).toMatch(/^La cancelaste · /)
    expect(outcomeText(approval({ status: 'cancelled', mine: false, decided_at: at }), NOW)).toMatch(/^La canceló quien la pidió · /)
  })

  it('has a plain label for every status', () => {
    for (const s of Object.keys(statusInfo)) expect(statusInfo[s as keyof typeof statusInfo].label).not.toMatch(/pending|approved|rejected|expired|cancelled/)
  })

  it('counts for the bell', () => {
    expect(bellText(0)).toBe('No hay solicitudes esperando tu decisión.')
    expect(bellText(1)).toBe('1 solicitud espera tu decisión')
    expect(bellText(3)).toBe('3 solicitudes esperan tu decisión')
    expect(countLabel(4)).toBe('4')
    expect(countLabel(10)).toBe('9+')
  })
})

describe('who may do what', () => {
  it('lets the requester cancel only their own pending request', () => {
    expect(canCancel(approval({ mine: true }))).toBe(true)
    expect(canCancel(approval({ mine: false }))).toBe(false)
    expect(canCancel(approval({ mine: true, status: 'approved' }))).toBe(false)
  })

  it('shows Aprobar / Rechazar only when the API says the viewer may decide it now', () => {
    expect(canDecide(approval())).toBe(true)
    expect(canDecide(approval({ can_decide: false }))).toBe(false)
    expect(canDecide(approval({ status: 'rejected' }))).toBe(false)
    // your own request: the API says can_decide false (APPROVAL_SELF), so it is never offered
    expect(canDecide(approval({ mine: true, can_decide: false }))).toBe(false)
  })

  it('flags an approved request that could not run', () => {
    expect(needsAnotherTry(approval({ status: 'approved', execution_error: 'Ya no tienes permiso.' }))).toBe(true)
    expect(needsAnotherTry(approval({ status: 'approved' }))).toBe(false)
    expect(needsAnotherTry(approval({ status: 'pending', execution_error: 'x' }))).toBe(false)
  })
})

describe('approval errors', () => {
  it('say what happened in plain Spanish, not the code', () => {
    const e = (code: string, status: number, type: 'AUTHORIZATION' | 'CONFLICT') => new ApiError('x', status, type, code)
    expect(errorMessage(e('APPROVAL_SELF', 403, 'AUTHORIZATION'))).toMatch(/propia solicitud/)
    expect(errorMessage(e('APPROVAL_DECIDED', 409, 'CONFLICT'))).toMatch(/ya se decidió/)
    expect(errorMessage(e('APPROVAL_NOT_USABLE', 409, 'CONFLICT'))).toMatch(/ya no sirve/)
  })

  it('no longer has a special case for the retired APPROVAL_REQUIRED (it is a plain conflict now)', () => {
    expect(errorMessage(new ApiError('Esta acción necesita aprobación', 409, 'CONFLICT', 'APPROVAL_REQUIRED'))).toMatch(/al mismo tiempo/)
  })
})
