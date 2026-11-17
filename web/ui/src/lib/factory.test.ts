import { describe, it, expect, vi, beforeAll } from 'vitest'
// @ts-expect-error -- vitest runs on node, and this project installs no node type package.
import { webcrypto } from 'node:crypto'
import { createFactoryPoller, factoryPlanHash, completeCommand, reopenCommand, linkPRCommand, refreshEvidenceCommand, assessCompletionCommand, mergePRsCommand, reviewedPR } from './factory'
import type { FactoryPRLink } from './factory'

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

/** One linked pull request at the identity a user reviewed. */
function link(n: number): FactoryPRLink {
  return {
    repoId: 'repo',
    pr: {
      number: n,
      url: `https://github.com/owner/repo/pull/${n}`,
      nodeId: `node-${n}`,
      headRef: 'marshal/factory/item',
      baseRef: 'main',
      headSha: `head-${n}`,
      baseSha: 'base',
      state: 'open',
      draft: false,
    },
    origin: 'user',
    observedAt: '2026-05-10T12:00:00Z',
    fresh: true,
  }
}

/*
  These helpers only shape a request: they carry the caller's own request id
  and the version it displayed, because a helper that invented either would
  turn a lost retry into a second decision.
*/
describe('completion requests', () => {
  it('carry the request id and the displayed version unchanged', () => {
    expect(completeCommand('r1', 4)).toEqual({ requestId: 'r1', kind: 'complete', expectedVersion: 4 })
    expect(reopenCommand('r2', 5)).toEqual({ requestId: 'r2', kind: 'reopen', expectedVersion: 5 })
    expect(refreshEvidenceCommand('r3', 6)).toEqual({ requestId: 'r3', kind: 'refresh_pr_evidence', expectedVersion: 6 })
    expect(assessCompletionCommand('r4', 7)).toEqual({ requestId: 'r4', kind: 'assess_completion', expectedVersion: 7 })
    expect(linkPRCommand('r5', 8, '12')).toEqual({ requestId: 'r5', kind: 'link_pr', expectedVersion: 8, completion: { link: '12' } })
  })

  it('derive the merge kind from the intent, which the bridge refuses to see contradicted', () => {
    const prs = [reviewedPR(link(12))]
    expect(mergePRsCommand('r6', 9, prs, 'squash', false)).toMatchObject({
      requestId: 'r6', kind: 'merge_pr', expectedVersion: 9, completion: { strategy: 'squash', completeAfter: false },
    })
    expect(mergePRsCommand('r7', 9, prs, 'rebase', true)).toMatchObject({
      requestId: 'r7', kind: 'merge_and_complete', expectedVersion: 9, completion: { strategy: 'rebase', completeAfter: true },
    })
  })

  it('send a run choice only when the user made one', () => {
    const prs = [reviewedPR(link(12))]
    expect(mergePRsCommand('r8', 9, prs, 'merge', true).completion).not.toHaveProperty('runChoice')
    expect(mergePRsCommand('r9', 9, prs, 'merge', true, 'request_stop').completion).toMatchObject({ runChoice: 'request_stop' })
  })

  it('review exactly the evidence the link showed', () => {
    const shown = link(12)
    const reviewed = reviewedPR(shown)
    expect(reviewed).toEqual({ repoId: 'repo', number: 12, nodeId: 'node-12', headSha: 'head-12', baseRef: 'main' })
    // The identity is copied, not referenced: a link that moves after the
    // review cannot change what the caller authorized.
    shown.pr.headSha = 'head-moved'
    expect(reviewed.headSha).toBe('head-12')
  })
})
