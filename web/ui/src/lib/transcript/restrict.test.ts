import { describe, expect, it } from 'vitest'
import type { WireNode } from '../stack'
import { restrict } from './restrict'

const n = (id: string, kind: string, parent?: string, children: string[] = []): WireNode => ({ id, kind, parent, children })
const tree = {
  roots: ['t1', 't2'],
  nodes: new Map<string, WireNode>(
    [
      n('t1', 'turn', undefined, ['s1', 's2']),
      n('s1', 'step', 't1', ['c1']),
      n('c1', 'tool', 's1'),
      n('s2', 'step', 't1', ['c2']),
      n('c2', 'tool', 's2'),
      n('t2', 'turn', undefined, ['s3']),
      n('s3', 'step', 't2'),
    ].map((x) => [x.id, x]),
  ),
}

describe('restrict', () => {
  it('keeps the named step with its ancestors and descendants only', () => {
    const got = restrict(tree, new Set(['s2']))
    expect([...got.nodes.keys()].sort()).toEqual(['c2', 's2', 't1'])
    expect(got.roots).toEqual(['t1'])
    expect(got.nodes.get('t1')?.children).toEqual(['s2'])
  })

  it('does not mutate the source and ignores unknown ids', () => {
    restrict(tree, new Set(['s2', 'nope']))
    expect(tree.nodes.get('t1')?.children).toEqual(['s1', 's2'])
  })

  it('spans turns and returns an empty tree for an empty set', () => {
    expect(restrict(tree, new Set(['s1', 's3'])).roots).toEqual(['t1', 't2'])
    const empty = restrict(tree, new Set())
    expect(empty.roots).toEqual([])
    expect(empty.nodes.size).toBe(0)
  })
})
