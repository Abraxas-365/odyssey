import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { permissions } from './permissions'

// The backend generates internal/permission from iam/permissions.json; the
// console's names must stay a subset of it.
describe('permissions', () => {
  it('match iam/permissions.json', () => {
    const file = JSON.parse(readFileSync(resolve(__dirname, '../../../iam/permissions.json'), 'utf8'))
    const names = new Set<string>(JSON.stringify(file).match(/odyssey:[a-z_]+:[a-z_]+/g) ?? [])
    for (const p of Object.values(permissions)) expect(names, p).toContain(p)
  })
})
