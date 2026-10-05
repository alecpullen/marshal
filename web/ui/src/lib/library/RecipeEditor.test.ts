import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import RecipeEditor from './RecipeEditor.svelte'
import RecipesTab from './RecipesTab.svelte'
import * as api from '../api'

vi.mock('../api', async (importActual) => {
  const actual = await importActual<typeof import('../api')>()
  return { ...actual, listWorkspaces: vi.fn(), saveRecipe: vi.fn(), listRecipes: vi.fn(), runRecipe: vi.fn(), copyRecipe: vi.fn(), deleteRecipe: vi.fn() }
})

beforeEach(() => {
  ;(api.listWorkspaces as Mock).mockResolvedValue([])
  ;(api.saveRecipe as Mock).mockImplementation(async (r) => r)
})
afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

const base = { name: 'my-recipe', title: 'Mine', kind: 'prompt', prompt: 'Do {{thing}}', inputs: [] as { name: string }[] }

describe('RecipeEditor', () => {
  it('rejects an undeclared placeholder', async () => {
    const onSaved = vi.fn()
    render(RecipeEditor, { recipe: base, isNew: true, onSaved, onCancel: vi.fn() })
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect((await screen.findByRole('alert')).textContent).toContain('{{thing}}')
    expect(api.saveRecipe).not.toHaveBeenCalled()
  })

  it('saves once every placeholder is declared', async () => {
    const onSaved = vi.fn()
    render(RecipeEditor, { recipe: { ...base, inputs: [{ name: 'thing' }] }, isNew: true, onSaved, onCancel: vi.fn() })
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSaved).toHaveBeenCalled())
    expect(api.saveRecipe).toHaveBeenCalledWith(expect.objectContaining({ name: 'my-recipe', prompt: 'Do {{thing}}', builtin: false }))
  })

  it('highlights the tokens', () => {
    render(RecipeEditor, { recipe: { ...base, inputs: [{ name: 'thing' }] }, onSaved: vi.fn(), onCancel: vi.fn() })
    expect(screen.getByTestId('recipe-tokens').querySelector('mark')?.textContent).toBe('{{thing}}')
  })
})

describe('RecipesTab', () => {
  const recipes = [
    { name: 'review-pr', title: 'Review a PR', kind: 'prompt', mode: 'plan', prompt: 'Review {{pr}}', inputs: [{ name: 'pr', label: 'PR number', required: true }], builtin: true },
    { name: 'mine', title: 'Mine', kind: 'prompt', prompt: 'x', builtin: false },
  ]
  const projects = [{ root: '/work/alpha', available: true }] as api.ProjectStatus[]

  it('built-ins offer Copy, not Edit; user recipes offer Edit', async () => {
    ;(api.listRecipes as Mock).mockResolvedValue(recipes)
    render(RecipesTab, { projects, onToast: vi.fn(), onNavigate: vi.fn() })
    const rows = await screen.findAllByTestId('recipe-row')
    expect(rows[0].textContent).toContain('built-in')
    expect(rows[0].textContent).toContain('Copy')
    expect(rows[0].textContent).not.toContain('Edit')
    expect(rows[1].textContent).toContain('Edit')
  })

  it('Run posts the project and inputs, then opens the agent', async () => {
    ;(api.listRecipes as Mock).mockResolvedValue(recipes)
    ;(api.runRecipe as Mock).mockResolvedValue({ agentId: 'a7' })
    const onNavigate = vi.fn()
    render(RecipesTab, { projects, onToast: vi.fn(), onNavigate })
    await fireEvent.click((await screen.findAllByRole('button', { name: 'Run' }))[0])
    await fireEvent.input(await screen.findByLabelText(/PR number/), { target: { value: '12' } })
    const run = screen.getAllByRole('button', { name: 'Run' }).at(-1)!
    await fireEvent.click(run)
    await waitFor(() => expect(api.runRecipe).toHaveBeenCalledWith('review-pr', { project: '/work/alpha', inputs: { pr: '12' } }))
    expect(onNavigate).toHaveBeenCalledWith('#chat/a7')
  })

  it('Run asks for required inputs first', async () => {
    ;(api.listRecipes as Mock).mockResolvedValue(recipes)
    render(RecipesTab, { projects, onToast: vi.fn(), onNavigate: vi.fn() })
    await fireEvent.click((await screen.findAllByRole('button', { name: 'Run' }))[0])
    await fireEvent.click(screen.getAllByRole('button', { name: 'Run' }).at(-1)!)
    expect((await screen.findByRole('alert')).textContent).toContain('pr')
    expect(api.runRecipe).not.toHaveBeenCalled()
  })
})
