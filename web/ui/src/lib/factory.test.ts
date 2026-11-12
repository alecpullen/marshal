import { describe, it, expect, vi } from 'vitest'
import { createFactoryPoller } from './factory'

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
