import { afterEach, describe, expect, it } from 'vitest'
import { initialDock, load, persistable, reduce, save, type DockState } from './dock'

const d = (o: Partial<DockState> = {}) => initialDock(o)

describe('dock reducer', () => {
  it('cycles collapsed → docked → expanded → collapsed', () => {
    let s = d({ size: 'collapsed' })
    const seen = []
    for (let i = 0; i < 3; i++) {
      s = reduce(s, { type: 'cycleSize' })
      seen.push(s.size)
    }
    expect(seen).toEqual(['docked', 'expanded', 'collapsed'])
  })

  it('setSize sets the size', () => {
    expect(reduce(d(), { type: 'setSize', size: 'expanded' }).size).toBe('expanded')
  })

  it('clamps the width to 320–720', () => {
    expect(reduce(d(), { type: 'resize', px: 100 }).width).toBe(320)
    expect(reduce(d(), { type: 'resize', px: 900 }).width).toBe(720)
    expect(reduce(d(), { type: 'resize', px: 500 }).width).toBe(500)
  })

  it('select enters select mode and switches to inspect for inspectable kinds', () => {
    for (const kind of ['tool', 'step', 'task', 'subagent']) {
      const s = reduce(d({ tab: 'files' }), { type: 'select', nodeId: 'n', kind })
      expect(s).toMatchObject({ mode: 'select', selected: 'n', tab: 'inspect' })
    }
    expect(reduce(d({ tab: 'files' }), { type: 'select', nodeId: 'm', kind: 'message' }).tab).toBe('files')
  })

  it('a pinned tab does not auto-switch on select', () => {
    const s = reduce(d({ tab: 'changes', pinned: true }), { type: 'select', nodeId: 'n', kind: 'tool' })
    expect(s.tab).toBe('changes')
    expect(s.selected).toBe('n')
  })

  it('select docks a collapsed dock unless told not to reveal', () => {
    expect(reduce(d({ size: 'collapsed' }), { type: 'select', nodeId: 'n', kind: 'tool' }).size).toBe('docked')
    expect(reduce(d({ size: 'collapsed' }), { type: 'select', nodeId: 'n', kind: 'tool', reveal: false }).size).toBe('collapsed')
  })

  it('backToLive follows and clears the selection', () => {
    const s = reduce(d({ mode: 'select', selected: 'n' }), { type: 'backToLive' })
    expect(s).toMatchObject({ mode: 'follow', selected: undefined })
  })

  it('openTab opens the tab, docks a collapsed dock and clears unseen on changes', () => {
    const s = reduce(d({ size: 'collapsed', unseenChanges: true }), { type: 'openTab', tab: 'changes' })
    expect(s).toMatchObject({ tab: 'changes', size: 'docked', unseenChanges: false })
    expect(reduce(d({ unseenChanges: true }), { type: 'openTab', tab: 'files' }).unseenChanges).toBe(true)
  })

  it('togglePin toggles', () => {
    expect(reduce(d(), { type: 'togglePin' }).pinned).toBe(true)
    expect(reduce(d({ pinned: true }), { type: 'togglePin' }).pinned).toBe(false)
  })

  it('liveEdit switches a following, unpinned, visible dock to changes', () => {
    expect(reduce(d({ tab: 'inspect' }), { type: 'liveEdit' })).toMatchObject({ tab: 'changes', unseenChanges: false })
  })

  it.each([
    ['pinned', { pinned: true }],
    ['selecting', { mode: 'select' as const }],
    ['collapsed', { size: 'collapsed' as const }],
  ])('liveEdit only marks unseen when %s', (_n, over) => {
    const s = reduce(d({ tab: 'inspect', ...over }), { type: 'liveEdit' })
    expect(s.tab).toBe('inspect')
    expect(s.unseenChanges).toBe(true)
  })

  it('liveEdit leaves a visible changes tab alone', () => {
    const s = d({ tab: 'changes', pinned: true })
    expect(reduce(s, { type: 'liveEdit' })).toBe(s)
  })
})

describe('dock persistence', () => {
  afterEach(() => localStorage.clear())

  it('persists only size and width', () => {
    expect(persistable(d({ size: 'expanded', width: 500, pinned: true, selected: 'x' }))).toEqual({ size: 'expanded', width: 500 })
  })

  it('round-trips and defaults', () => {
    expect(load()).toEqual({ size: 'docked', width: 440 })
    save(d({ size: 'collapsed', width: 600 }))
    expect(load()).toEqual({ size: 'collapsed', width: 600 })
  })

  it('ignores garbage and clamps a stored width', () => {
    localStorage.setItem('marshal.ui.dock', '{"size":"huge","width":5000}')
    expect(load()).toEqual({ size: 'docked', width: 720 })
    localStorage.setItem('marshal.ui.dock', 'not json')
    expect(load()).toEqual({ size: 'docked', width: 440 })
  })
})

describe('terminal unread mark', () => {
  it('is set by output while the tab is not showing and cleared by opening it', () => {
    let s = initialDock({ tab: 'inspect' })
    s = reduce(s, { type: 'terminalOutput' })
    expect(s.unseenTerminal).toBe(true)
    s = reduce(s, { type: 'openTab', tab: 'terminal' })
    expect(s.unseenTerminal).toBe(false)
    expect(reduce(s, { type: 'terminalOutput' }).unseenTerminal).toBe(false)
    expect(reduce({ ...s, size: 'collapsed' }, { type: 'terminalOutput' }).unseenTerminal).toBe(true)
  })
})
