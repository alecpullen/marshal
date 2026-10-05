import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { loadPrefs, maybeNotify, savePrefs, show, type NotifyPrefs } from './notify'

const on = (): NotifyPrefs => loadPrefs()

beforeEach(() => localStorage.clear())
afterEach(() => vi.unstubAllGlobals())

describe('maybeNotify', () => {
  const nameOf = (id: string) => (id === 'a1' ? 'Fixer' : id)

  it('maps a pending delta to needs_you', () => {
    expect(maybeNotify({ kind: 'pending', sessionId: 'a1', pendingKind: 'approval' }, on(), false, { nameOf })).toEqual({
      event: 'needs_you', title: 'Fixer needs you', body: 'Fixer is waiting for an approval.', url: '#chat/a1',
    })
  })

  it('announces a finished run once', () => {
    const d = { kind: 'run', sessionId: 'a1', run: { sdd: { finished: true, succeeded: true, planName: 'p', endedAt: 5 } } }
    const ctx = { nameOf, seen: new Set<string>() }
    expect(maybeNotify(d, on(), false, ctx)).toMatchObject({ event: 'run_finished', title: 'Fixer run succeeded', url: '#runs/a1' })
    expect(maybeNotify(d, on(), false, ctx)).toBeNull()
  })

  it('ignores a run that has not finished', () => {
    expect(maybeNotify({ kind: 'run', sessionId: 'a1', run: { sdd: { finished: false } } }, on(), false)).toBeNull()
  })

  it('maps a budget delta, flat or nested', () => {
    expect(maybeNotify({ kind: 'budget', scope: 'daily', spentUsd: 5, capUsd: 5 }, on(), false)).toMatchObject({ event: 'budget', body: 'The daily budget reached $5.00 of its $5.00 cap.', url: '#usage' })
    expect(maybeNotify({ kind: 'budget', budget: { scope: 'agent', spentUsd: 1, capUsd: 2 } }, on(), false)?.body).toContain('agent budget')
  })

  it('maps an automation delta', () => {
    expect(maybeNotify({ kind: 'automation', title: 'Nightly ran', text: 'Opened 2 PRs' }, on(), false)).toMatchObject({ event: 'automation', title: 'Nightly ran', body: 'Opened 2 PRs' })
  })

  it('maps a watch only when it fired', () => {
    expect(maybeNotify({ kind: 'watch', event: { name: 'errors', state: 'fired' } }, on(), false)).toMatchObject({ event: 'watch_fired', url: '#watches' })
    expect(maybeNotify({ kind: 'watch', event: { name: 'errors', state: 'ok' } }, on(), false)).toBeNull()
  })

  it('maps a network block', () => {
    expect(maybeNotify({ kind: 'network_block', sessionId: 'a1', host: 'evil.test' }, on(), false, { nameOf })).toMatchObject({
      event: 'network_block', body: 'Fixer tried to reach evil.test, which its policy blocks.', url: '#network?agent=a1',
    })
  })

  it('stays quiet for other kinds, while the tab is visible, and for events switched off', () => {
    expect(maybeNotify({ kind: 'activity', sessionId: 'a1' }, on(), false)).toBeNull()
    expect(maybeNotify({ kind: 'pending', sessionId: 'a1' }, on(), true)).toBeNull()
    expect(maybeNotify({ kind: 'pending', sessionId: 'a1' }, { ...on(), needs_you: false }, false)).toBeNull()
  })
})

describe('prefs', () => {
  it('default to all on and persist', () => {
    expect(Object.values(loadPrefs()).every(Boolean)).toBe(true)
    savePrefs({ ...loadPrefs(), budget: false })
    expect(loadPrefs().budget).toBe(false)
    expect(loadPrefs().needs_you).toBe(true)
  })

  it('survive unreadable storage', () => {
    localStorage.setItem('marshal.ui.notify', '{not json')
    expect(loadPrefs().needs_you).toBe(true)
  })
})

describe('show', () => {
  it('constructs a notification when permitted and navigates on click', () => {
    const made: { title: string; body?: string; onclick?: () => void; close: () => void }[] = []
    class N {
      static permission = 'granted'
      onclick?: () => void
      close = vi.fn()
      constructor(public title: string, o?: { body?: string }) {
        made.push(Object.assign(this, { body: o?.body }))
      }
    }
    vi.stubGlobal('Notification', N)
    show({ title: 'T', body: 'B', url: '#usage' })
    expect(made[0]).toMatchObject({ title: 'T', body: 'B' })
    made[0].onclick!()
    expect(window.location.hash).toBe('#usage')
  })

  it('does nothing without permission', () => {
    const ctor = vi.fn()
    vi.stubGlobal('Notification', Object.assign(ctor, { permission: 'default' }))
    show({ title: 'T', body: 'B', url: '#' })
    expect(ctor).not.toHaveBeenCalled()
  })
})
