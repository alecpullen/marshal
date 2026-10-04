import { describe, expect, it } from 'vitest'
import { editCalls, latestEdit, latestStepId, liveStep, nodeFile, splitTarget, subagentIdOf } from './nodes'
import type { WireNode } from '../stack'

const mk = (list: WireNode[], roots = ['turn:1']) => ({ roots, nodes: new Map(list.map((n) => [n.id, n])) })

const tree = mk([
  { id: 'turn:1', kind: 'turn', children: ['step:1', 'step:2'] },
  { id: 'step:1', kind: 'step', parent: 'turn:1', step: { headline: 'one' }, children: ['tool:1'] },
  { id: 'tool:1', kind: 'tool', parent: 'step:1', tool: { name: 'file.write_patch', display: 'edit', calls: [{ files: ['a.go'] }] } },
  { id: 'step:2', kind: 'step', parent: 'turn:1', step: { headline: 'two' }, children: ['tool:2'] },
  { id: 'tool:2', kind: 'tool', parent: 'step:2', tool: { name: 'file.read', display: 'read', calls: [{ target: 'b.go:42' }] } },
])

describe('dock node helpers', () => {
  it('liveStep prefers a live step, else the last', () => {
    expect(liveStep(tree)?.id).toBe('step:2')
    tree.nodes.set('step:1', { ...tree.nodes.get('step:1')!, live: true })
    expect(liveStep(tree)?.id).toBe('step:1')
    tree.nodes.set('step:1', { ...tree.nodes.get('step:1')!, live: false })
  })

  it('latestStepId is the last step of the last turn', () => {
    expect(latestStepId(tree)).toBe('step:2')
  })

  it('finds edit calls and the latest edit node', () => {
    expect(editCalls(tree.nodes.get('tool:1')!)).toHaveLength(1)
    expect(latestEdit(tree)?.id).toBe('tool:1')
  })

  it('resolves a node to its file and line', () => {
    expect(nodeFile(tree.nodes.get('tool:1')!)).toEqual({ path: 'a.go' })
    expect(nodeFile(tree.nodes.get('tool:2')!)).toEqual({ path: 'b.go', line: 42 })
    expect(nodeFile(tree.nodes.get('step:1')!)).toBeUndefined()
  })

  it('splits targets and subagent keys', () => {
    expect(splitTarget('x/y.ts')).toEqual({ path: 'x/y.ts' })
    expect(splitTarget('x/y.ts:10-20')).toEqual({ path: 'x/y.ts', line: 10 })
    expect(subagentIdOf('sub:12')).toBe(12)
    expect(subagentIdOf('step:12')).toBeUndefined()
  })
})
