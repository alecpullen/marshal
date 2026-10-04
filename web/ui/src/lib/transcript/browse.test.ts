import { describe, expect, it } from 'vitest'
import { browseKey, flattenVisible, nodeText, type BrowseCtx, type BrowseState } from './browse'
import type { TranscriptCtx } from './ctx'
import type { WireNode } from '../stack'

const ctx: BrowseCtx = { ids: ['a', 'b', 'c', 'd', 'e'], stops: [0, 2, 4], lastLive: true }
const at = (cursor: string | null, follow = false): BrowseState => ({ cursor, follow })

describe('browseKey', () => {
  it('moves one row with j/k and the arrows, clamped at the ends', () => {
    expect(browseKey(at('b'), 'j', ctx).state.cursor).toBe('c')
    expect(browseKey(at('b'), 'ArrowDown', ctx).state.cursor).toBe('c')
    expect(browseKey(at('b'), 'k', ctx).state.cursor).toBe('a')
    expect(browseKey(at('b'), 'ArrowUp', ctx).state.cursor).toBe('a')
    expect(browseKey(at('a'), 'k', ctx).state.cursor).toBe('a')
    expect(browseKey(at('e'), 'j', ctx).state.cursor).toBe('e')
  })

  it('starts at the last row when there is no cursor', () => {
    expect(browseKey(at(null), 'k', ctx).state.cursor).toBe('e')
    expect(browseKey(at(null), 'j', ctx).state.cursor).toBe('e')
  })

  it('jumps to the next and previous stop with J/] and K/[', () => {
    expect(browseKey(at('a'), 'J', ctx).state.cursor).toBe('c')
    expect(browseKey(at('b'), ']', ctx).state.cursor).toBe('c')
    expect(browseKey(at('e'), 'J', ctx).state.cursor).toBe('e')
    expect(browseKey(at('d'), 'K', ctx).state.cursor).toBe('c')
    expect(browseKey(at('c'), '[', ctx).state.cursor).toBe('a')
    expect(browseKey(at('a'), 'K', ctx).state.cursor).toBe('a')
  })

  it('goes to the first and last row, and G on a live tail follows', () => {
    expect(browseKey(at('c'), 'g', ctx).state.cursor).toBe('a')
    expect(browseKey(at('c'), 'Home', ctx).state.cursor).toBe('a')
    expect(browseKey(at('c'), 'G', ctx).state).toEqual({ cursor: 'e', follow: true })
    expect(browseKey(at('c'), 'End', ctx).state.cursor).toBe('e')
    expect(browseKey(at('c'), 'G', { ...ctx, lastLive: false }).state.follow).toBe(false)
  })

  it('reports clamped navigation keys as bound, so they do not exit browse mode', () => {
    expect(browseKey(at('e'), 'j', ctx).bound).toBe(true)
    expect(browseKey(at('a'), 'k', ctx).bound).toBe(true)
    expect(browseKey(at('a'), 'g', ctx).bound).toBe(true)
  })

  it('leaves follow off when moving away', () => {
    expect(browseKey(at('e', true), 'k', ctx).state.follow).toBe(false)
  })

  it('Enter toggles density, z toggles fold, y copies, on the cursor row', () => {
    expect(browseKey(at('b'), 'Enter', ctx).effect).toEqual({ toggleDensity: 'b' })
    expect(browseKey(at('b'), 'z', ctx).effect).toEqual({ toggleFold: 'b' })
    expect(browseKey(at('b'), 'y', ctx).effect).toEqual({ copy: 'b' })
  })

  it('i, o and f say they are coming in W2', () => {
    for (const k of ['i', 'o', 'f']) expect(browseKey(at('b'), k, ctx).effect).toEqual({ toast: 'Coming in W2' })
  })

  it('Escape exits browse mode', () => {
    expect(browseKey(at('b'), 'Escape', ctx).effect).toEqual({ exitBrowse: true })
  })

  it('ignores unbound keys and empty transcripts', () => {
    expect(browseKey(at('b'), 'q', ctx)).toEqual({ state: at('b'), bound: false })
    expect(browseKey(at(null), 'j', { ids: [], stops: [], lastLive: false })).toEqual({ state: at(null), bound: true })
    expect(browseKey(at(null), 'q', { ids: [], stops: [], lastLive: false }).bound).toBe(false)
  })
})

describe('flattenVisible', () => {
  const nodes = new Map<string, WireNode>(
    (
      [
        { id: 'turn', kind: 'turn', children: ['u', 't1', 't2'] },
        { id: 'u', kind: 'message', parent: 'turn', message: { role: 'user', content: 'go' } },
        { id: 't1', kind: 'task', parent: 'turn', children: ['s1'], task: { todoId: '1', content: 'one', status: 'completed', steps: 1, tools: 1, edits: 0 } },
        { id: 's1', kind: 'step', parent: 't1', children: ['x1'], step: { headline: 'h' } },
        { id: 'x1', kind: 'tool', parent: 's1', tool: { name: 'file.read', display: 'Read file', calls: [{ target: 'a' }] } },
        { id: 't2', kind: 'task', parent: 'turn', children: ['s2'], live: true, task: { todoId: '2', content: 'two', status: 'in_progress', steps: 1, tools: 0, edits: 0 } },
        { id: 's2', kind: 'step', parent: 't2', live: true, step: { headline: 'h2' } },
      ] as WireNode[]
    ).map((n) => [n.id, n]),
  )
  const base: TranscriptCtx = { nodes, global: 'steps', overrides: new Map(), foldTasks: true, unfolded: new Set(), cursor: null, now: 0 }

  it('skips containers, folded children and records stops', () => {
    expect(flattenVisible(base, ['turn'])).toEqual({ ids: ['u', 't1', 't2', 's2'], stops: [0, 1, 2], lastLive: true })
  })
  it('lists tool rows when tasks are open, but not at outline', () => {
    expect(flattenVisible({ ...base, foldTasks: false }, ['turn']).ids).toEqual(['u', 't1', 's1', 'x1', 't2', 's2'])
    expect(flattenVisible({ ...base, foldTasks: false, global: 'outline' }, ['turn']).ids).toEqual(['u', 't1', 's1', 't2', 's2'])
  })
})

describe('nodeText', () => {
  it('copies a step, a call and a message', () => {
    expect(nodeText({ id: 'a', kind: 'step', step: { headline: 'Head', rest: 'rest' } })).toBe('Head\nrest')
    expect(nodeText({ id: 'a', kind: 'tool', tool: { name: 'x', display: 'X', calls: [{ target: 'a.go', output: 'out' }] } })).toBe('a.go\nout')
    expect(nodeText({ id: 'a', kind: 'final', message: { role: 'assistant', content: 'hello' } })).toBe('hello')
  })
})
