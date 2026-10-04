import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import Library from './Library.svelte'
import * as api from '../lib/api.js'

vi.mock('../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../lib/api.js')>()
  return {
    ...actual,
    listProjects: vi.fn(),
    listSkills: vi.fn(),
    previewSkill: vi.fn(),
    confirmSkill: vi.fn(),
    discardSkill: vi.fn(),
    removeSkill: vi.fn(),
    listPlugins: vi.fn(),
    scanPlugin: vi.fn(),
    confirmPlugin: vi.fn(),
    discardPlugin: vi.fn(),
    removePlugin: vi.fn(),
    listMemory: vi.fn(),
    deleteMemory: vi.fn(),
    setMemoryConfidence: vi.fn(),
    listClients: vi.fn(),
  }
})

afterEach(cleanup)

const projects = [
  { root: '/work/alpha', available: true },
  { root: '/work/beta', available: true },
]

beforeEach(() => {
  vi.clearAllMocks()
  ;(api.listProjects as Mock).mockResolvedValue(projects)
  ;(api.listSkills as Mock).mockResolvedValue([{ name: 'tdd', description: 'Test first', risk: 'low', scope: 'global' }])
  ;(api.listPlugins as Mock).mockResolvedValue([{ name: 'lint', source: 'github.com/x/lint', commit: 'abcdef0123456789', contentHash: 'h', installedAt: '', scope: 'global' }])
  ;(api.listMemory as Mock).mockResolvedValue([
    { id: 7, kind: 'fact', content: 'Uses pnpm', confidence: 'tentative', sourceSessionId: 's9', createdAt: '', updatedAt: '' },
  ])
  ;(api.listClients as Mock).mockResolvedValue([])
})

const mount = (tab: 'skills' | 'plugins' | 'mcp' | 'memory', project?: string) =>
  render(Library, { route: { tab, project }, onNavigate: vi.fn() })

describe('Library skills', () => {
  it('lists skills with risk and scope', async () => {
    mount('skills')
    const row = await screen.findByTestId('skill-row')
    expect(within(row).getByText('tdd')).toBeTruthy()
    expect(within(row).getByText('low')).toBeTruthy()
    expect(api.listSkills).toHaveBeenCalledWith('global', undefined)
  })

  it('previews, then confirms with the token and scope', async () => {
    ;(api.previewSkill as Mock).mockResolvedValue({ stagingToken: 'tok1', name: 'newskill', description: 'Does things', risk: 'medium', source: 'git@x' })
    ;(api.confirmSkill as Mock).mockResolvedValue(undefined)
    mount('skills')
    await screen.findByTestId('skill-row')
    await fireEvent.input(screen.getByLabelText('Skill source'), { target: { value: 'git@x' } })
    await fireEvent.click(screen.getByText('Preview'))
    expect(await screen.findByTestId('skill-preview')).toBeTruthy()
    expect(api.previewSkill).toHaveBeenCalledWith('git@x', 'global', undefined)
    await fireEvent.click(screen.getByText('Confirm'))
    await waitFor(() => expect(api.confirmSkill).toHaveBeenCalledWith('tok1', 'global', undefined))
    await waitFor(() => expect(screen.queryByTestId('skill-preview')).toBeNull())
  })

  it('discards a preview', async () => {
    ;(api.previewSkill as Mock).mockResolvedValue({ stagingToken: 'tok2', name: 'n', description: '', risk: 'low', source: 's' })
    mount('skills')
    await screen.findByTestId('skill-row')
    await fireEvent.input(screen.getByLabelText('Skill source'), { target: { value: 's' } })
    await fireEvent.click(screen.getByText('Preview'))
    await fireEvent.click(await screen.findByText('Discard'))
    expect(api.discardSkill).toHaveBeenCalledWith('tok2')
    expect(screen.queryByTestId('skill-preview')).toBeNull()
  })

  it('asks before removing', async () => {
    ;(api.removeSkill as Mock).mockResolvedValue(undefined)
    mount('skills')
    await fireEvent.click(await screen.findByText('Remove'))
    expect(api.removeSkill).not.toHaveBeenCalled()
    await fireEvent.click(screen.getByText('Confirm remove'))
    await waitFor(() => expect(api.removeSkill).toHaveBeenCalledWith('tdd', 'global', undefined))
  })

  it('scopes to a project and explains an unsupported project library', async () => {
    ;(api.listSkills as Mock).mockImplementation(async (scope: string) => (scope === 'project' ? 'unsupported' : []))
    mount('skills', '/work/beta')
    expect(await screen.findByText(/managed from this project's agents in container mode/)).toBeTruthy()
    expect(api.listSkills).toHaveBeenCalledWith('project', '/work/beta')
  })
})

describe('Library plugins', () => {
  it('shows the scan contents counts and confirms the scan token', async () => {
    ;(api.scanPlugin as Mock).mockResolvedValue({
      scanToken: 'scan1',
      name: 'fmt',
      source: 'github.com/x/fmt',
      commit: '0123456789abcdef',
      contents: { hasManifest: true, skillCount: 2, commandCount: 3, hookCount: 1, mcpServerCount: 0, mcpPolicyCount: 0 },
    })
    ;(api.confirmPlugin as Mock).mockResolvedValue(undefined)
    mount('plugins')
    expect(await screen.findByTestId('plugin-row')).toBeTruthy()
    await fireEvent.input(screen.getByLabelText('Plugin source'), { target: { value: 'github.com/x/fmt' } })
    await fireEvent.click(screen.getByText('Scan'))
    const card = await screen.findByTestId('plugin-scan')
    expect(within(card).getByText('2 skills')).toBeTruthy()
    expect(within(card).getByText('1 hooks')).toBeTruthy()
    await fireEvent.click(within(card).getByText('Confirm'))
    await waitFor(() => expect(api.confirmPlugin).toHaveBeenCalledWith('scan1', 'global', undefined))
  })
})

describe('Library memory', () => {
  it('lists a project’s memories and posts a confidence change', async () => {
    ;(api.setMemoryConfidence as Mock).mockResolvedValue(undefined)
    mount('memory')
    const row = await screen.findByTestId('memory-row')
    expect(within(row).getByText('Uses pnpm')).toBeTruthy()
    expect(api.listMemory).toHaveBeenCalledWith('/work/alpha')
    await fireEvent.change(screen.getByLabelText('Confidence for memory 7'), { target: { value: 'confirmed' } })
    await waitFor(() => expect(api.setMemoryConfidence).toHaveBeenCalledWith(7, '/work/alpha', 'confirmed'))
  })

  it('deletes after confirmation', async () => {
    ;(api.deleteMemory as Mock).mockResolvedValue(undefined)
    mount('memory')
    await fireEvent.click(await screen.findByText('Delete'))
    expect(api.deleteMemory).not.toHaveBeenCalled()
    await fireEvent.click(screen.getByText('Confirm delete'))
    await waitFor(() => expect(api.deleteMemory).toHaveBeenCalledWith(7, '/work/alpha'))
    await waitFor(() => expect(screen.queryByTestId('memory-row')).toBeNull())
  })
})

describe('Library MCP', () => {
  it('renders the clients panel', async () => {
    mount('mcp')
    expect(await screen.findByText(/No MCP clients|MCP clients|Clients/i)).toBeTruthy()
    expect(api.listClients).toHaveBeenCalled()
  })
})
