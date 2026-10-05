import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from 'vitest'
import { render, cleanup, screen, fireEvent, waitFor } from '@testing-library/svelte'
import Review from './Review.svelte'
import * as api from '../lib/api.js'
import type { AgentRow } from '../lib/fleet'

vi.mock('../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../lib/api.js')>()
  return {
    ...actual,
    getDiff: vi.fn(),
    getStack: vi.fn(),
    getStepDiffs: vi.fn(),
    getGate: vi.fn(),
    runGate: vi.fn(),
    getCommitDraft: vi.fn(),
    listReviewComments: vi.fn(),
    postReviewComment: vi.fn(),
    resolveReviewComment: vi.fn(),
    exitAgent: vi.fn(),
    getProjectSettings: vi.fn(),
    runReviewBot: vi.fn(),
    mergeAgent: vi.fn(),
    discardAgent: vi.fn(),
  }
})
vi.mock('../lib/sse.js', () => ({ connectSSE: vi.fn(() => () => {}) }))
vi.mock('../lib/markdown.js', () => ({ renderMarkdown: vi.fn((s: string) => s), initHighlighter: vi.fn(() => Promise.resolve()) }))

const A_DIFF = `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,3 +1,3 @@
 package a
-var x = 1
+var x = 2
 func f() {}
`
const B_DIFF = `diff --git a/b.go b/b.go
--- a/b.go
+++ b/b.go
@@ -1,1 +1,2 @@
 package b
+var y = 1
`

function agent(over: Partial<AgentRow> = {}): AgentRow {
  return {
    id: 'a1', project: '/p', name: 'fix it', mode: 'edit', status: 'idle', activity: '', contextPct: 0, changedFiles: 2,
    interrupted: false, updatedAt: '2026-10-04T00:00:00Z', isolated: true, sourceKind: 'git', readOnly: false, ...over,
  }
}

const finalNode = (id: string, at: number, content: string) => ({ id, kind: 'final', message: { role: 'assistant', content, final: true, at } })

function stackWith(...nodes: object[]) {
  return { rev: 1, roots: nodes.map((n) => (n as { id: string }).id), nodes }
}

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  ;(api.getDiff as Mock).mockImplementation(async (_id: string, path?: string) => {
    if (path === 'a.go') return { files: [], diff: A_DIFF }
    if (path === 'b.go') return { files: [], diff: B_DIFF }
    return { files: [{ path: 'a.go', added: 1, removed: 1 }, { path: 'b.go', added: 1, removed: 0 }] }
  })
  ;(api.getStack as Mock).mockResolvedValue(stackWith(finalNode('f1', 1000, 'Done: fixed x')))
  ;(api.getGate as Mock).mockResolvedValue(null)
  ;(api.getCommitDraft as Mock).mockResolvedValue('fix: set x to 2')
  ;(api.listReviewComments as Mock).mockResolvedValue([])
  ;(api.getStepDiffs as Mock).mockResolvedValue('unsupported')
  ;(api.getProjectSettings as Mock).mockResolvedValue({ intake: {} })
})
afterEach(cleanup)

describe('Request review bot', () => {
  const botOn = { intake: {}, automations: { reviewBot: { enabled: true, repoId: 'r1', skipDrafts: true, autoPost: false, holdSeverities: [] } } }
  const push = async () => {
    await fireEvent.click(screen.getByText('Push & open PR'))
    await fireEvent.click(await screen.findByText('Push'))
  }

  it('is disabled when the project has no review bot', async () => {
    render(Review, { agentId: 'a1', agent: agent() })
    await waitFor(() => expect(api.getProjectSettings).toHaveBeenCalledWith('/p'))
    expect((screen.getByLabelText('Request review bot') as HTMLInputElement).disabled).toBe(true)
  })

  it('runs the bot on the new PR after a successful push', async () => {
    ;(api.getProjectSettings as Mock).mockResolvedValue(botOn)
    ;(api.exitAgent as Mock).mockResolvedValue({ destination: 'push', prUrl: 'https://github.com/o/n/pull/12' })
    ;(api.runReviewBot as Mock).mockResolvedValue(undefined)
    const onShipped = vi.fn()
    render(Review, { agentId: 'a1', agent: agent(), onShipped })
    const box = screen.getByLabelText('Request review bot') as HTMLInputElement
    await waitFor(() => expect(box.disabled).toBe(false))
    await fireEvent.click(box)
    await waitFor(() => expect((screen.getByLabelText('Commit message') as HTMLTextAreaElement).value).toBe('fix: set x to 2'))
    await push()
    await waitFor(() => expect(api.runReviewBot).toHaveBeenCalledWith('r1', 12))
    await waitFor(() => expect(onShipped).toHaveBeenCalledWith({ kind: 'pushed', message: 'Pushed. Pull request opened. Review bot requested.', href: 'https://github.com/o/n/pull/12' }))
  })

  it('does not run the bot when the box is unchecked, and reports a failed run without failing the push', async () => {
    ;(api.getProjectSettings as Mock).mockResolvedValue(botOn)
    ;(api.exitAgent as Mock).mockResolvedValue({ destination: 'push', prUrl: 'https://github.com/o/n/pull/12' })
    const onShipped = vi.fn()
    render(Review, { agentId: 'a1', agent: agent(), onShipped })
    await waitFor(() => expect((screen.getByLabelText('Request review bot') as HTMLInputElement).disabled).toBe(false))
    await waitFor(() => expect((screen.getByLabelText('Commit message') as HTMLTextAreaElement).value).toBe('fix: set x to 2'))
    await push()
    await waitFor(() => expect(onShipped).toHaveBeenCalled())
    expect(api.runReviewBot).not.toHaveBeenCalled()
    cleanup()
    ;(api.runReviewBot as Mock).mockRejectedValue(new api.APIError(409, { error: 'review bot is off' }))
    const again = vi.fn()
    render(Review, { agentId: 'a1', agent: agent(), onShipped: again })
    const box = screen.getByLabelText('Request review bot') as HTMLInputElement
    await waitFor(() => expect(box.disabled).toBe(false))
    await fireEvent.click(box)
    await waitFor(() => expect((screen.getByLabelText('Commit message') as HTMLTextAreaElement).value).toBe('fix: set x to 2'))
    await push()
    await waitFor(() => expect(again).toHaveBeenCalledWith(expect.objectContaining({ kind: 'pushed', message: 'Pushed. Pull request opened. Review bot not started: review bot is off' })))
  })
})

describe('Review left column', () => {
  it('renders gate, files, summary and ship from fixtures', async () => {
    render(Review, { agentId: 'a1', agent: agent() })
    expect(await screen.findByText('gate not run')).toBeTruthy()
    expect((await screen.findAllByText('a.go')).length).toBeGreaterThan(0)
    expect(screen.getAllByText('b.go').length).toBeGreaterThan(0)
    expect(await screen.findByText('Done: fixed x')).toBeTruthy()
    expect(screen.getByText('Push & open PR')).toBeTruthy()
    expect(screen.getByLabelText('Request review bot')).toBeTruthy()
  })

  it('prefills the commit draft and pushes with the edited message', async () => {
    ;(api.exitAgent as Mock).mockResolvedValue({ destination: 'push', prUrl: 'https://x/pr/1' })
    const onShipped = vi.fn()
    render(Review, { agentId: 'a1', agent: agent(), onShipped })
    const box = (await screen.findByLabelText('Commit message')) as HTMLTextAreaElement
    await waitFor(() => expect(box.value).toBe('fix: set x to 2'))
    await fireEvent.input(box, { target: { value: 'feat: edited' } })
    await fireEvent.click(screen.getByText('Push & open PR'))
    expect(api.exitAgent).not.toHaveBeenCalled()
    await fireEvent.click(await screen.findByText('Push'))
    await waitFor(() => expect(api.exitAgent).toHaveBeenCalledWith('a1', { commitMessage: 'feat: edited' }))
    await waitFor(() => expect(onShipped).toHaveBeenCalledWith({ kind: 'pushed', message: 'Pushed. Pull request opened.', href: 'https://x/pr/1' }))
  })

  it('shows the override when a push is blocked and re-calls with the reason', async () => {
    ;(api.exitAgent as Mock)
      .mockResolvedValueOnce({ destination: 'push', blocked: true, verify: { ok: false, skipped: false, failedCommand: 'go test', output: 'FAIL' } })
      .mockResolvedValueOnce({ destination: 'push', prUrl: 'u' })
    render(Review, { agentId: 'a1', agent: agent() })
    const box = (await screen.findByLabelText('Commit message')) as HTMLTextAreaElement
    await waitFor(() => expect(box.value).toBe('fix: set x to 2'))
    await fireEvent.click(screen.getByText('Push & open PR'))
    await fireEvent.click(await screen.findByText('Push'))
    expect(await screen.findAllByText('Gate failed')).toHaveLength(1)
    await fireEvent.input(screen.getByPlaceholderText(/flaky test/), { target: { value: 'unrelated' } })
    await fireEvent.click(screen.getByText('Override and push'))
    await waitFor(() => expect(api.exitAgent).toHaveBeenLastCalledWith('a1', { commitMessage: 'fix: set x to 2', override: { reason: 'unrelated' } }))
  })

  it('local agents merge locally and show a refusal', async () => {
    ;(api.mergeAgent as Mock).mockResolvedValue({ merged: false, branch: 'b', target: 'main', reason: 'project_dirty' })
    render(Review, { agentId: 'a1', agent: agent({ sourceKind: 'local' }) })
    await fireEvent.click(await screen.findByText('Merge locally'))
    expect(api.mergeAgent).not.toHaveBeenCalled()
    await fireEvent.click(await screen.findByText('Merge'))
    expect(await screen.findByText(/uncommitted changes/)).toBeTruthy()
  })

  it('a push needs a commit message', async () => {
    ;(api.getCommitDraft as Mock).mockResolvedValue('unsupported')
    render(Review, { agentId: 'a1', agent: agent() })
    const btn = (await screen.findByText('Push & open PR')).closest('button') as HTMLButtonElement
    expect(btn.disabled).toBe(true)
  })

  it('asks before discarding', async () => {
    ;(api.discardAgent as Mock).mockResolvedValue(undefined)
    render(Review, { agentId: 'a1', agent: agent() })
    await fireEvent.click(await screen.findByText('Discard'))
    expect(api.discardAgent).not.toHaveBeenCalled()
    const buttons = await screen.findAllByText('Discard')
    await fireEvent.click(buttons[buttons.length - 1])
    await waitFor(() => expect(api.discardAgent).toHaveBeenCalledWith('a1'))
  })
})

describe('Review diff', () => {
  it('renders unified, then split, and collapses a viewed file', async () => {
    render(Review, { agentId: 'a1', agent: agent() })
    await screen.findByText('var x = 2')
    expect(screen.queryAllByTestId('split-row')).toHaveLength(0)
    await fireEvent.click(screen.getByText('Split'))
    await waitFor(() => expect(screen.getAllByTestId('split-row').length).toBeGreaterThan(0))
    expect(localStorage.getItem('marshal.ui.review.view')).toBe('split')

    const sections = screen.getAllByTestId('file-diff')
    const section = sections.find((s) => s.getAttribute('data-path') === 'a.go')!
    await fireEvent.click(section.querySelector('input[type=checkbox]')!)
    await waitFor(() => expect(screen.queryByText('var x = 2')).toBeNull())
  })

  it('posts a comment with path, line, side and quote', async () => {
    ;(api.postReviewComment as Mock).mockResolvedValue({})
    render(Review, { agentId: 'a1', agent: agent() })
    await screen.findByText('var x = 2')
    // Added line 2 on the new side.
    await fireEvent.click(screen.getAllByLabelText('Comment on new line 2')[0])
    await fireEvent.input(await screen.findByPlaceholderText('Comment for the agent'), { target: { value: 'why 2?' } })
    await fireEvent.click(screen.getByText('Send to agent'))
    await waitFor(() =>
      expect(api.postReviewComment).toHaveBeenCalledWith('a1', { path: 'a.go', line: 2, side: 'new', quote: 'package a\nvar x = 2', body: 'why 2?' }),
    )
  })

  it('a deleted line comments on the old side', async () => {
    ;(api.postReviewComment as Mock).mockResolvedValue({})
    render(Review, { agentId: 'a1', agent: agent() })
    await screen.findByText('var x = 1')
    await fireEvent.click(screen.getAllByLabelText('Comment on old line 2')[0])
    await fireEvent.input(await screen.findByPlaceholderText('Comment for the agent'), { target: { value: 'why drop?' } })
    await fireEvent.click(screen.getByText('Send to agent'))
    await waitFor(() =>
      expect(api.postReviewComment).toHaveBeenCalledWith('a1', { path: 'a.go', line: 2, side: 'old', quote: 'package a\nvar x = 1', body: 'why drop?' }),
    )
  })

  it('shows the agent reply once a later final node exists', async () => {
    const sent = Date.parse('2026-10-04T00:00:10Z')
    ;(api.listReviewComments as Mock).mockResolvedValue([
      { id: 'c1', agentId: 'a1', path: 'a.go', line: 2, side: 'new', quote: 'q', body: 'why 2?', createdAt: '2026-10-04T00:00:10Z', sentAt: '2026-10-04T00:00:10Z' },
    ])
    ;(api.getStack as Mock).mockResolvedValue(stackWith(finalNode('f1', sent - 5000, 'earlier'), finalNode('f2', sent + 5000, 'Because 2 is right')))
    render(Review, { agentId: 'a1', agent: agent() })
    const thread = await screen.findByTestId('comment-thread')
    await waitFor(() => expect(thread.textContent).toContain('Because 2 is right'))
    expect(thread.textContent).not.toContain('earlier')
    expect(thread.querySelector('a')!.getAttribute('href')).toBe('#chat/a1?node=f2')
  })

  it('shows waiting until the agent answers, and resolves', async () => {
    ;(api.listReviewComments as Mock).mockResolvedValue([
      { id: 'c1', agentId: 'a1', path: 'a.go', line: 2, side: 'new', quote: 'q', body: 'why 2?', createdAt: '2026-10-04T00:00:10Z', sentAt: '2026-10-04T00:00:10Z' },
    ])
    ;(api.resolveReviewComment as Mock).mockResolvedValue(undefined)
    render(Review, { agentId: 'a1', agent: agent() })
    expect(await screen.findByText('Waiting for the agent…')).toBeTruthy()
    await fireEvent.click(screen.getByText('Resolve'))
    await waitFor(() => expect(api.resolveReviewComment).toHaveBeenCalledWith('a1', 'c1'))
  })
})

describe('Review by step', () => {
  it('badges a hunk a later step rewrote', async () => {
    const d1 = 'diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,2 @@\n package a\n+var x = 1\n'
    const d2 = 'diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n package a\n-var x = 1\n+var x = 2\n'
    ;(api.getStepDiffs as Mock).mockResolvedValue([
      { stepNode: 's1', turnNode: 't1', taskNode: 'k1', headline: 'add x', files: ['a.go'], diff: d1 },
      { stepNode: 's2', turnNode: 't1', taskNode: 'k1', headline: 'tweak x', files: ['a.go'], diff: d2 },
    ])
    render(Review, { agentId: 'a1', agent: agent() })
    await screen.findByText('var x = 2')
    await fireEvent.click(screen.getByText('By step'))
    expect(await screen.findByText('rewritten in tweak x')).toBeTruthy()
    expect(screen.getAllByText('Open step')).toHaveLength(2)
    expect(screen.getByText('No task')).toBeTruthy()
  })

  it('falls back to net effect when unsupported', async () => {
    render(Review, { agentId: 'a1', agent: agent() })
    await screen.findByText('var x = 2')
    await fireEvent.click(screen.getByText('By step'))
    await waitFor(() => expect(screen.queryByTestId('by-step')).toBeNull())
    await waitFor(() => expect(screen.getByText('Net effect').getAttribute('aria-pressed')).toBe('true'))
  })
})
