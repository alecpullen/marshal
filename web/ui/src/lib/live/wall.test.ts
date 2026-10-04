import { describe, expect, it } from 'vitest'
import type { AgentRow } from '../fleet'
import { paginate, wallAgents } from './wall'

const row = (x: Partial<AgentRow> & { id: string }): AgentRow =>
  ({ project: '/p', status: 'idle', updatedAt: '2026-01-01T00:00:00Z', name: '', mode: '', activity: '', contextPct: 0, changedFiles: 0, interrupted: false, ...x }) as AgentRow

describe('wallAgents', () => {
  const agents = [
    row({ id: 'idle-new', updatedAt: '2026-01-03T00:00:00Z' }),
    row({ id: 'running', status: 'running', updatedAt: '2026-01-01T00:00:00Z', run: { kind: 'swarm' } }),
    row({ id: 'needs', pending: { kind: 'approval', id: 'x' }, updatedAt: '2026-01-00T00:00:00Z', project: '/q' }),
    row({ id: 'idle-old', updatedAt: '2026-01-02T00:00:00Z' }),
  ]

  it('sorts needs-you, then running, then newest', () => {
    expect(wallAgents(agents, { runsOnly: false }).map((a) => a.id)).toEqual(['needs', 'running', 'idle-new', 'idle-old'])
  })

  it('filters by project and by runs only without mutating the input', () => {
    expect(wallAgents(agents, { project: '/q', runsOnly: false }).map((a) => a.id)).toEqual(['needs'])
    expect(wallAgents(agents, { runsOnly: true }).map((a) => a.id)).toEqual(['running'])
    expect(agents[0].id).toBe('idle-new')
  })
})

describe('paginate', () => {
  const items = Array.from({ length: 25 }, (_, i) => i)
  it('slices 12 per page and clamps', () => {
    expect(paginate(items, 1)).toMatchObject({ pages: 3, page: 1 })
    expect(paginate(items, 3).items).toEqual([24])
    expect(paginate(items, 9).page).toBe(3)
    expect(paginate(items, 0).page).toBe(1)
    expect(paginate([], 1)).toEqual({ items: [], page: 1, pages: 1 })
  })
})
