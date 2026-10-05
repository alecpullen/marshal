import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import PreviewTab from './PreviewTab.svelte'
import * as api from '../api'

vi.mock('../api', async (importActual) => {
  const actual = await importActual<typeof import('../api')>()
  return { ...actual, getWorkspace: vi.fn(), openPreview: vi.fn() }
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})
beforeEach(() => {
  vi.mocked(api.openPreview).mockResolvedValue({ url: 'http://127.0.0.1:9001/preview/a1/3000/?t=x' })
})

const ws = { name: 'svc', version: 2, source: 'studio' as const }
const doc = (ports: number[]) => ({ doc: { preview: { ports } } }) as never

describe('PreviewTab', () => {
  it('lists the declared ports from the workspace doc', async () => {
    vi.mocked(api.getWorkspace).mockResolvedValue(doc([3000, 8080]))
    render(PreviewTab, { agentId: 'a1', workspace: ws })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Open :3000' })).toBeTruthy())
    expect(screen.getByRole('button', { name: 'Open :8080' })).toBeTruthy()
    expect(api.getWorkspace).toHaveBeenCalledWith('svc', 2)
  })

  it('open sets the sandboxed iframe src to the returned URL', async () => {
    vi.mocked(api.getWorkspace).mockResolvedValue(doc([3000]))
    render(PreviewTab, { agentId: 'a1', workspace: ws })
    await fireEvent.click(await screen.findByRole('button', { name: 'Open :3000' }))
    const frame = await screen.findByTitle('Preview of port 3000')
    expect(api.openPreview).toHaveBeenCalledWith('a1', 3000)
    expect(frame.getAttribute('src')).toBe('http://127.0.0.1:9001/preview/a1/3000/?t=x')
    expect(frame.getAttribute('sandbox')).toBe('allow-scripts allow-forms allow-same-origin')
    expect(screen.getByRole('link', { name: 'Open in new tab' }).getAttribute('href')).toBe('http://127.0.0.1:9001/preview/a1/3000/?t=x')
  })

  it('hints at declaring ports when there are none', async () => {
    vi.mocked(api.getWorkspace).mockResolvedValue(doc([]))
    render(PreviewTab, { agentId: 'a1', workspace: ws })
    await waitFor(() => expect(screen.getByTestId('preview-empty')).toBeTruthy())
    expect(screen.getByRole('link', { name: 'Open the designer' }).getAttribute('href')).toBe('#workspaces/svc/edit')
  })

  it('hints without a workspace', async () => {
    render(PreviewTab, { agentId: 'a1' })
    await waitFor(() => expect(screen.getByTestId('preview-empty')).toBeTruthy())
    expect(api.getWorkspace).not.toHaveBeenCalled()
  })
})
