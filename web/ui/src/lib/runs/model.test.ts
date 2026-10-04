import { describe, expect, it } from 'vitest'
import type { RunTask, SDDRun } from '../api'
import type { WireNode } from '../stack'
import { activeStage, criticalPath, lanes, layout, pathEdges, roleBars, stageSteps } from './model'

const stages = (impl: string, verify = 'pending', review = 'pending', commit = 'pending') => [
  { name: 'Implement', state: impl },
  { name: 'Verify', state: verify },
  { name: 'Review', state: review },
  { name: 'Commit', state: commit },
] as RunTask['stages']

const task = (n: number, deps: number[], startedAt?: number, endedAt?: number, extra: Partial<RunTask> = {}): RunTask => ({
  n,
  title: `t${n}`,
  dependsOn: deps,
  status: endedAt ? 'done' : startedAt ? 'active' : 'pending',
  startedAt,
  endedAt,
  fixRounds: 0,
  stages: stages('pending'),
  ...extra,
})

describe('lanes', () => {
  it('maps stages to cells with fix rounds and a short sha', () => {
    const run = {
      tasks: [
        task(1, [], 1, 2, { fixRounds: 2, commit: { base: 'a', head: 'abcdef0123456' }, stages: stages('done', 'done', 'done', 'done') }),
        task(2, [1], 3, undefined, { stages: [{ name: 'Implement', state: 'active', detail: 'editing' }] }),
      ],
    } as SDDRun
    const got = lanes(run)
    expect(got[0].cells.review.fixRounds).toBe(2)
    expect(got[0].cells.commit).toEqual({ state: 'done', sha: 'abcdef0' })
    expect(got[1].deps).toEqual([1])
    expect(got[1].cells.implement).toEqual({ state: 'active', detail: 'editing' })
    // Missing stages read as pending rather than undefined.
    expect(got[1].cells.verify).toEqual({ state: 'pending' })
  })
})

describe('layout', () => {
  const diamond = [{ n: 1, dependsOn: [] }, { n: 2, dependsOn: [1] }, { n: 3, dependsOn: [1] }, { n: 4, dependsOn: [2, 3] }]

  it('layers a diamond by longest path and orders by n', () => {
    const { nodes, edges } = layout(diamond)
    expect(nodes).toEqual([
      { n: 1, layer: 0, index: 0 },
      { n: 2, layer: 1, index: 0 },
      { n: 3, layer: 1, index: 1 },
      { n: 4, layer: 2, index: 0 },
    ])
    expect(edges).toHaveLength(4)
  })

  it('uses the longest path, not the shortest', () => {
    const { nodes } = layout([{ n: 1, dependsOn: [] }, { n: 2, dependsOn: [1] }, { n: 3, dependsOn: [1, 2] }])
    expect(nodes.find((x) => x.n === 3)?.layer).toBe(2)
  })

  it('ignores unknown dependencies and survives a cycle', () => {
    expect(layout([{ n: 1, dependsOn: [9] }]).nodes[0].layer).toBe(0)
    expect(layout([{ n: 1, dependsOn: [2] }, { n: 2, dependsOn: [1] }]).nodes).toHaveLength(2)
  })
})

describe('criticalPath', () => {
  it('picks the longer branch of a diamond', () => {
    const tasks = [task(1, [], 0, 10), task(2, [1], 10, 20), task(3, [1], 10, 50), task(4, [2, 3], 50, 60)]
    expect(criticalPath(tasks, 100)).toEqual([1, 3, 4])
  })

  it('counts elapsed time for an active task and nothing for a pending one', () => {
    const tasks = [task(1, [], 0, 10), task(2, [1], 10), task(3, [1])]
    expect(criticalPath(tasks, 40)).toEqual([1, 2])
  })

  it('is stable on ties and empty on no tasks', () => {
    expect(criticalPath([task(1, []), task(2, [])], 0)).toEqual([1])
    expect(criticalPath([], 0)).toEqual([])
  })

  it('names the dashed edges', () => {
    expect([...pathEdges([1, 3, 4])]).toEqual(['1>3', '3>4'])
  })
})

const step = (id: string, role: string | undefined, startedAt: number, endedAt?: number, children: string[] = []): WireNode => ({
  id,
  kind: 'step',
  step: { headline: id, role, startedAt, endedAt },
  children,
})

describe('roleBars', () => {
  it('clips segments to the range, closes open steps at now and drops outsiders', () => {
    const nodes = [
      step('a', 'sdd_implementer', 1000, 1050),
      step('b', 'sdd_implementer', 1060, undefined),
      step('c', 'sdd_reviewer', 1200, 1300),
      step('d', undefined, 1010, 1020),
      { id: 'm', kind: 'message' } as WireNode,
    ]
    const bars = roleBars(nodes, 1020, 1100, 1080)
    expect(bars.map((b) => b.role)).toEqual(['sdd_implementer'])
    expect(bars[0].segments).toEqual([
      { start: 1020, end: 1050, stepId: 'a' },
      { start: 1060, end: 1080, stepId: 'b' },
    ])
  })
})

describe('stageSteps', () => {
  const nodes = new Map<string, WireNode>(
    [
      step('impl', 'sdd_implementer', 1005, 1008),
      step('rev', 'sdd_reviewer', 1009, 1012),
      step('late', 'sdd_implementer', 1099, 1100),
      step('v', undefined, 1013, 1014, ['sh']),
      { id: 'sh', kind: 'tool', tool: { name: 'shell.run', display: 'x' } } as WireNode,
      step('plain', undefined, 1015, 1016, ['rd']),
      { id: 'rd', kind: 'tool', tool: { name: 'file.read', display: 'x' } } as WireNode,
    ].map((n) => [n.id, n]),
  )
  const t = task(2, [], 1000, 1020)

  it('matches role and window for implement and review', () => {
    expect(stageSteps(nodes, t, 'implement', 1050)).toEqual(['impl'])
    expect(stageSteps(nodes, t, 'review', 1050)).toEqual(['rev'])
  })
  it('takes shell-family steps for verify and none for commit', () => {
    expect(stageSteps(nodes, t, 'verify', 1050)).toEqual(['v'])
    expect(stageSteps(nodes, t, 'commit', 1050)).toEqual([])
  })
})

describe('activeStage', () => {
  it('prefers the active or failed stage, then the last done one', () => {
    expect(activeStage(task(1, [], 1, undefined, { stages: stages('done', 'active') }))).toBe('verify')
    expect(activeStage(task(1, [], 1, 2, { stages: stages('done', 'done', 'done', 'done') }))).toBe('commit')
    expect(activeStage(task(1, []))).toBe('implement')
  })
})
