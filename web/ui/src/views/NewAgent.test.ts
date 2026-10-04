import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from 'vitest'
import { render, cleanup, screen, fireEvent, waitFor } from '@testing-library/svelte'
import NewAgent from './NewAgent.svelte'
import * as api from '../lib/api.js'

vi.mock('../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../lib/api.js')>()
  return { ...actual, listProjects: vi.fn(), spawnAgent: vi.fn(), recentPrompts: vi.fn(), listIssues: vi.fn() }
})

const projects = [
  { root: '/work/alpha', available: true, isolation: 'available' },
  { root: '/work/plain', available: true, isolation: 'not a git repository' },
  { root: '/work/gone', available: false },
]

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  ;(api.listProjects as Mock).mockResolvedValue(projects)
  ;(api.recentPrompts as Mock).mockResolvedValue(['earlier prompt one', 'earlier prompt two'])
  ;(api.spawnAgent as Mock).mockResolvedValue({ agentId: 'a9' })
})
afterEach(cleanup)

describe('NewAgent', () => {
  it('shows the defaults on its chips', async () => {
    render(NewAgent, { onDone: vi.fn() })
    expect(await screen.findByText('alpha')).toBeTruthy()
    expect(screen.getByText('edit')).toBeTruthy()
    expect(screen.getByText('worktree')).toBeTruthy()
    expect(document.activeElement).toBe(screen.getByLabelText('Prompt'))
  })

  it('spawns with ⌘Enter using the chosen values, then remembers them', async () => {
    const onDone = vi.fn()
    render(NewAgent, { onDone })
    await screen.findByText('alpha')
    const box = screen.getByLabelText('Prompt')
    await fireEvent.input(box, { target: { value: 'do the thing' } })
    await fireEvent.click(screen.getByText('edit'))
    await fireEvent.click(screen.getByLabelText('plan'))
    await fireEvent.keyDown(box, { key: 'Enter', metaKey: true })
    await waitFor(() =>
      expect(api.spawnAgent).toHaveBeenCalledWith({ project: '/work/alpha', prompt: 'do the thing', mode: 'plan', isolated: true, branch: undefined, baseRef: undefined }),
    )
    await waitFor(() => expect(onDone).toHaveBeenCalledWith('a9', undefined))
    expect(JSON.parse(localStorage.getItem('marshal.ui.newagent')!)).toMatchObject({ project: '/work/alpha', mode: 'plan' })
  })

  it('passes a spawn warning on to the caller', async () => {
    ;(api.spawnAgent as Mock).mockResolvedValue({ agentId: 'a9', warning: 'worktree reused' })
    const onDone = vi.fn()
    render(NewAgent, { onDone })
    await screen.findByText('alpha')
    await fireEvent.click(screen.getByText('Create agent'))
    await waitFor(() => expect(onDone).toHaveBeenCalledWith('a9', 'worktree reused'))
  })

  it('keeps the prompt and shows the error when spawning fails', async () => {
    ;(api.spawnAgent as Mock).mockRejectedValue(new Error('boom'))
    const onDone = vi.fn()
    render(NewAgent, { onDone })
    await screen.findByText('alpha')
    const box = screen.getByLabelText('Prompt') as HTMLTextAreaElement
    await fireEvent.input(box, { target: { value: 'keep me' } })
    await fireEvent.click(screen.getByText('Create agent'))
    expect((await screen.findByRole('alert')).textContent).toContain('boom')
    expect(box.value).toBe('keep me')
    expect(onDone).not.toHaveBeenCalled()
  })

  it('fills the prompt from a recent prompt', async () => {
    render(NewAgent, { onDone: vi.fn() })
    await fireEvent.click(await screen.findByText('earlier prompt two'))
    expect((screen.getByLabelText('Prompt') as HTMLTextAreaElement).value).toBe('earlier prompt two')
  })

  it('disables isolation for a project that cannot isolate and unavailable projects', async () => {
    render(NewAgent, { onDone: vi.fn() })
    await screen.findByText('alpha')
    await fireEvent.click(screen.getByText('alpha'))
    expect((screen.getByText(/gone/).closest('button') as HTMLButtonElement).disabled).toBe(true)
    await fireEvent.click(screen.getByText(/\/work\/plain/))
    await waitFor(() => expect(screen.getByText('off')).toBeTruthy())
    await fireEvent.click(screen.getByText('off'))
    expect((screen.getByLabelText('Isolate in a git worktree') as HTMLInputElement).disabled).toBe(true)
    expect(screen.getByText(/Unavailable: not a git repository/)).toBeTruthy()
  })

  it('an issue fills the prompt', async () => {
    ;(api.listIssues as Mock).mockResolvedValue([{ number: 7, title: 'Crash on save', body: 'steps…', url: 'u', labels: [] }])
    render(NewAgent, { onDone: vi.fn() })
    await fireEvent.click(await screen.findByText('Issues'))
    await fireEvent.input(screen.getByPlaceholderText('Repo ID'), { target: { value: 'r1' } })
    await fireEvent.click(screen.getByText('List issues'))
    await fireEvent.click(await screen.findByText('Use'))
    expect((screen.getByLabelText('Prompt') as HTMLTextAreaElement).value).toBe('Crash on save\n\nsteps…')
  })
})
