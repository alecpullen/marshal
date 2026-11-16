import { describe, expect, it, vi, beforeEach } from 'vitest'
import * as api from '../api'
import { loadAutomationSummaries } from './summary'
import type { CIHistory, ProjectStatus, ReviewDraft } from '../api'

vi.mock('../api', async (importActual) => {
  const actual = await importActual<typeof import('../api')>()
  return {
    ...actual,
    listReviewDrafts: vi.fn().mockResolvedValue([]),
    listCIHistory: vi.fn().mockResolvedValue([]),
  }
})

beforeEach(() => {
  vi.mocked(api.listReviewDrafts).mockReset().mockResolvedValue([])
  vi.mocked(api.listCIHistory).mockReset().mockResolvedValue([])
})

const project = (root: string): ProjectStatus => ({ root, available: true, trust: 'trusted', isolation: 'available' })
const projects = [project('/home/u/alpha'), project('/home/u/beta')]

const draft = (id: string, number: number, status: ReviewDraft['status'] = 'draft'): ReviewDraft => ({ id, repoId: 'r1', number, headSha: 'abc', findings: [], summary: '', status, createdAt: '2026-10-05T00:00:00Z' })
const fix = (id: string, createdAt: string, status = 'fixed'): CIHistory => ({ id, repoId: 'r1', sha: 's', check: 'go test', status, reason: 'ok', prUrl: 'https://github.com/o/n/pull/1', costUsd: 0, createdAt })

describe('loadAutomationSummaries', () => {
  it('loadsDraftsAndRecentFixesAcrossProjects', async () => {
    vi.mocked(api.listReviewDrafts).mockImplementation(async (opts) =>
      opts?.project === '/home/u/alpha' ? [draft('d1', 12), draft('d2', 13, 'posted')] : [draft('d3', 14)],
    )
    vi.mocked(api.listCIHistory).mockImplementation(async (opts) =>
      opts?.project === '/home/u/alpha' ? [fix('c1', new Date(Date.now() - 3600 * 1000).toISOString())] : [],
    )
    const s = await loadAutomationSummaries(projects)
    expect(api.listReviewDrafts).toHaveBeenCalledWith({ project: '/home/u/alpha', status: 'draft' })
    expect(api.listReviewDrafts).toHaveBeenCalledWith({ project: '/home/u/beta', status: 'draft' })
    expect(api.listCIHistory).toHaveBeenCalledWith({ project: '/home/u/beta' })
    // Posted drafts are not waiting, so only the drafts come through, in project order.
    expect(s.drafts.map((d) => [d.root, d.draft.id])).toEqual([
      ['/home/u/alpha', 'd1'],
      ['/home/u/beta', 'd3'],
    ])
    expect(s.fixes.map((f) => f.id)).toEqual(['c1'])
    expect(s.errors).toEqual([])
  })

  it('keepsSuccessfulProjectsWhenOneProjectFails', async () => {
    vi.mocked(api.listReviewDrafts).mockImplementation(async (opts) => {
      if (opts?.project === '/home/u/beta') throw new api.APIError(500, { error: 'automations are down' })
      return [draft('d1', 12)]
    })
    const s = await loadAutomationSummaries(projects)
    expect(s.drafts.map((d) => d.draft.id)).toEqual(['d1'])
    expect(s.errors).toEqual([{ root: '/home/u/beta', message: 'automations are down' }])
  })

  it('returnsEmptyAfterSuccessfulEmptyReads', async () => {
    const s = await loadAutomationSummaries([project('/home/u/alpha')])
    expect(s).toEqual({ drafts: [], fixes: [], errors: [] })
  })

  it('usesRecentFixesWindow', async () => {
    vi.mocked(api.listCIHistory).mockResolvedValue([
      fix('fresh', new Date(Date.now() - 3600 * 1000).toISOString()),
      fix('old', new Date(Date.now() - 48 * 3600 * 1000).toISOString()),
    ])
    const s = await loadAutomationSummaries([project('/home/u/alpha')])
    expect(s.fixes.map((f) => f.id)).toEqual(['fresh'])
  })
})
