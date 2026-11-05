import { describe, expect, it } from 'vitest'
import { inventoryQuery } from './inventory'

describe('inventoryQuery', () => {
  it('repeats tier (OR) and leaves empty filters out', () => {
    expect(inventoryQuery({})).toBe('')
    expect(inventoryQuery({ tier: ['production', 'sandbox'] })).toBe('?tier=production&tier=sandbox')
    expect(inventoryQuery({ tier: ['internal'], needs_attention: true, limit: 25, offset: 0, search: ' ' })).toBe('?needs_attention=true&limit=25&offset=0&search=+&tier=internal')
    expect(inventoryQuery({ cluster: 'c1', format: 'csv' })).toBe('?cluster=c1&format=csv')
  })
})
