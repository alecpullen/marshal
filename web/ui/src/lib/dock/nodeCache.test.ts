import { describe, expect, it, vi } from 'vitest'
import { createNodeCache } from './nodeCache'
import type { WireNode } from '../stack'

const wire = (o: Partial<WireNode> = {}): WireNode => ({ id: 'step:1', kind: 'step', ...o })
const resp = { node: wire(), detail: {} }

describe('node cache', () => {
  it('fetches once per wire version', async () => {
    const fetch = vi.fn().mockResolvedValue(resp)
    const c = createNodeCache(fetch)
    await c.get('s', 'step:1', wire())
    await c.get('s', 'step:1', wire())
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('refetches when the wire node changed', async () => {
    const fetch = vi.fn().mockResolvedValue(resp)
    const c = createNodeCache(fetch)
    await c.get('s', 'step:1', wire())
    await c.get('s', 'step:1', wire({ live: true }))
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('keys subagent transcripts apart', async () => {
    const fetch = vi.fn().mockResolvedValue(resp)
    const c = createNodeCache(fetch)
    await c.get('s', 'step:1', wire())
    await c.get('s', 'step:1', wire(), 3)
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(fetch).toHaveBeenLastCalledWith('s', 'step:1', 3)
  })

  it('evicts the least recently used past 100 entries', async () => {
    const fetch = vi.fn().mockResolvedValue(resp)
    const c = createNodeCache(fetch)
    for (let i = 0; i < 100; i++) await c.get('s', `n${i}`, wire({ id: `n${i}` }))
    await c.get('s', 'n0', wire({ id: 'n0' })) // refresh n0
    await c.get('s', 'extra', wire({ id: 'extra' })) // evicts n1
    expect(c.size()).toBe(100)
    fetch.mockClear()
    await c.get('s', 'n0', wire({ id: 'n0' }))
    expect(fetch).not.toHaveBeenCalled()
    await c.get('s', 'n1', wire({ id: 'n1' }))
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('does not cache a failure', async () => {
    const fetch = vi.fn().mockRejectedValueOnce(new Error('x')).mockResolvedValue(resp)
    const c = createNodeCache(fetch)
    await expect(c.get('s', 'step:1', wire())).rejects.toThrow()
    await c.get('s', 'step:1', wire())
    expect(fetch).toHaveBeenCalledTimes(2)
  })
})
