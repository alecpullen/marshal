import { describe, it, expect, vi, beforeAll } from 'vitest'
// @ts-expect-error -- vitest runs on node, and this project installs no node type package.
import { webcrypto } from 'node:crypto'
import { createFactoryPoller, factoryPlanHash } from './factory'

/*
  jsdom's crypto carries randomUUID but no SubtleCrypto, so the digest the
  browser has is installed here. In a browser this is the platform's own.
*/
beforeAll(() => {
  if (!globalThis.crypto.subtle) {
    Object.defineProperty(globalThis.crypto, 'subtle', { value: webcrypto.subtle, configurable: true })
  }
})

/*
  The same two fixtures, and the same expected digests, as
  web/bridge/factory_planhash_vectors_test.go (TestFactoryPlanHashVectors).
  Neither side derives them: both assert literals, so a change to the framing,
  the endianness or the encoder on either side fails there.
*/
describe('factoryPlanHash', () => {
  it('matches the bridge for an empty plan and specification', async () => {
    expect(await factoryPlanHash('', '')).toBe('374708fff7719dd5979ec875d56cd2286f6d3cf7ec317a3b25632aab28ec37bb')
  })
  it('hashes UTF-8 bytes, lengths included', async () => {
    expect(await factoryPlanHash('plan v2\n', 'spéc v2')).toBe('a7be07bf8aae8d9356ab7ee4bf6b3d42660fd3ef58e27a870ed64bb95a0b3207')
  })
  it('binds the two parts in order, so a swap is a different candidate', async () => {
    const plan = 'plan v2\n'
    const spec = 'spéc v2'
    const forward = await factoryPlanHash(plan, spec)
    expect(forward).not.toBe(await factoryPlanHash(spec, plan))
    // Whitespace is part of the bytes, exactly as the bridge compares them.
    expect(await factoryPlanHash(plan + '\n', spec)).not.toBe(forward)
  })
})

describe('factory poller', () => {
  it('loads immediately, uses active and idle cadence, and stops', async () => {
    vi.useFakeTimers()
    const load = vi.fn(async () => {})
    let active = true
    const p = createFactoryPoller(load, () => active, vi.fn())
    p.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(load).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(2000)
    expect(load).toHaveBeenCalledTimes(2)
    active = false
    await vi.advanceTimersByTimeAsync(2000)
    await vi.advanceTimersByTimeAsync(15000)
    expect(load).toHaveBeenCalledTimes(4)
    p.stop()
    await vi.advanceTimersByTimeAsync(30000)
    expect(load).toHaveBeenCalledTimes(4)
    vi.useRealTimers()
  })
})

describe('poll visibility and failures', () => {
  it('pauses hidden pages and refreshes immediately on return', async () => {
    vi.useFakeTimers()
    let hidden = false
    const listeners = new Set<EventListenerOrEventListenerObject>()
    const doc = { get hidden() { return hidden }, addEventListener: (_: string, cb: EventListenerOrEventListenerObject) => listeners.add(cb), removeEventListener: (_: string, cb: EventListenerOrEventListenerObject) => listeners.delete(cb) }
    const load = vi.fn(async () => {})
    const p = createFactoryPoller(load, () => true, vi.fn(), { document: doc })
    p.start(); await vi.advanceTimersByTimeAsync(0)
    hidden = true; listeners.forEach((cb) => typeof cb === 'function' && cb(new Event('visibilitychange')))
    await vi.advanceTimersByTimeAsync(10000); expect(load).toHaveBeenCalledTimes(1)
    hidden = false; listeners.forEach((cb) => typeof cb === 'function' && cb(new Event('visibilitychange')))
    await vi.advanceTimersByTimeAsync(0); expect(load).toHaveBeenCalledTimes(2)
    p.stop(); expect(listeners.size).toBe(0)
    vi.useRealTimers()
  })
  it('backs off errors and never overlaps an in-flight read', async () => {
    vi.useFakeTimers()
    let finish!: () => void
    const load = vi.fn().mockRejectedValueOnce(new Error('offline')).mockImplementation(() => new Promise<void>((resolve) => { finish = resolve }))
    const onError = vi.fn()
    const p = createFactoryPoller(load, () => true, onError)
    p.start(); await vi.advanceTimersByTimeAsync(0)
    expect(onError).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(2000); expect(load).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(2000); expect(load).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(10000); expect(load).toHaveBeenCalledTimes(2)
    finish(); await vi.advanceTimersByTimeAsync(0); p.stop(); vi.useRealTimers()
  })
})
