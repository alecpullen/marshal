import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import Designer from './Designer.svelte'
import * as api from '../../lib/api.js'
import type { WSDoc, WSLoaded } from '../../lib/api.js'

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    cols = 80
    rows = 24
    loadAddon() {}
    open() {}
    onData() {}
    onResize() {}
    write() {}
    dispose() {}
  },
}))
vi.mock('@xterm/addon-fit', () => ({ FitAddon: class { fit() {} } }))
vi.mock('../../lib/sse', () => ({ connectSSE: () => () => {} }))

vi.mock('../../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../../lib/api.js')>()
  return {
    ...actual,
    getWorkspace: vi.fn(), patchWorkspace: vi.fn(), saveWorkspaceDraft: vi.fn(), publishWorkspace: vi.fn(), diffWorkspace: vi.fn(),
    listBuilds: vi.fn(), startBuild: vi.fn(), setWorkspacePool: vi.fn(), rotateWorkspaceCA: vi.fn(), getSecretsStatus: vi.fn(),
    getNetworkHosts: vi.fn(), openWorkspaceShell: vi.fn(), closeTerminal: vi.fn().mockResolvedValue(undefined), listRepos: vi.fn(), listProjects: vi.fn(), getProjectHealth: vi.fn(),
  }
})

afterEach(() => {
  cleanup()
  try {
    localStorage.clear()
  } catch {
    // ignore
  }
})

const baseDoc = (): WSDoc => ({
  workspace: { name: 'svc', base: 'debian:12', toolchains: ['go@1.23'], extends: '' },
  packages: { apt: ['git'], go: [], npm: [], pip: [] },
  mounts: [], files: {}, secretsEnv: {}, inject: {},
  network: { mode: 'allowlist', egress: ['proxy.golang.org'] }, resources: { cpu: 0, memory: '', disk: '', timeout: '' },
  policy: { mode: 'ask', allow: [] }, setup: { run: '' },
})

const SRC = '[workspace]\nbase = "debian:12"\ntoolchains = ["go@1.23"]\n\n[packages]\napt = ["git"]\n\n[network]\nmode = "allowlist"\negress = ["proxy.golang.org"]\n'
const sections = [
  { layer: 1, key: 'workspace', startLine: 1, endLine: 3 },
  { layer: 2, key: 'workspace', startLine: 1, endLine: 3 },
  { layer: 3, key: 'packages', startLine: 5, endLine: 6 },
  { layer: 7, key: 'network', startLine: 8, endLine: 10 },
]
const loaded = (over: Partial<WSLoaded> = {}): WSLoaded => ({ source: SRC, doc: baseDoc(), sections, diagnostics: [], ...over })

beforeEach(() => {
  vi.mocked(api.getWorkspace).mockResolvedValue(loaded())
  vi.mocked(api.listBuilds).mockResolvedValue({ versions: [{ n: 1, at: 0, buildStatus: 'ok', sizeBytes: 100 * 1024 * 1024 }], pool: { size: 0, idle: 1, starting: 0 }, starts: { coldMs: 42000, warmMs: 1500 } })
  vi.mocked(api.getSecretsStatus).mockResolvedValue({ backend: 'local', healthy: true })
  vi.mocked(api.getNetworkHosts).mockResolvedValue({ processMode: false, rows: [{ host: 'proxy.golang.org', requests: 7, blocked: 0, bytesUp: 0, bytesDown: 0, lastSeen: 0, decision: 'allow' }] })
  vi.mocked(api.listRepos).mockResolvedValue([{ id: 'lib' }])
  vi.mocked(api.listProjects).mockResolvedValue([{ root: '/p', available: true }] as api.ProjectStatus[])
  vi.mocked(api.getProjectHealth).mockResolvedValue({ verify: { build: 'go build ./...', test: 'go test ./...' } })
})

async function open() {
  const nav = vi.fn()
  const r = render(Designer, { name: 'svc', onNavigate: nav })
  await screen.findAllByTestId('layer-card')
  return { nav, ...r }
}
const card = (n: number) => document.querySelector(`[data-testid="layer-card"][data-layer="${n}"]`) as HTMLElement

describe('Designer', () => {
  it('loads the workspace and shows nine layer cards in order', async () => {
    await open()
    expect(screen.getAllByTestId('layer-card').map((c) => c.getAttribute('data-layer'))).toEqual(['1', '2', '3', '4', '5', '6', '7', '8', '9'])
    expect(api.getWorkspace).toHaveBeenCalledWith('svc')
  })

  it('a layer edit posts the patch, replaces the source and flashes the changed lines', async () => {
    const next = SRC.replace('apt = ["git"]', 'apt = ["git", "make"]')
    vi.mocked(api.patchWorkspace).mockResolvedValue(loaded({ source: next, doc: { ...baseDoc(), packages: { apt: ['git', 'make'], go: [], npm: [], pip: [] } } }))
    await open()
    const input = within(card(3)).getByLabelText('Add to apt packages')
    await fireEvent.input(input, { target: { value: 'make' } })
    await fireEvent.keyDown(input, { key: 'Enter' })
    await waitFor(() => expect(api.patchWorkspace).toHaveBeenCalledWith('svc', 3, { apt: ['git', 'make'], go: [], npm: [], pip: [] }))
    await waitFor(() => expect((screen.getByLabelText('Workspace source') as HTMLTextAreaElement).value).toBe(next))
    const flashed = document.querySelectorAll('[data-testid="mirror"] [data-flash]')
    expect([...flashed].map((e) => e.getAttribute('data-line'))).toEqual(['6'])
    expect(card(3).hasAttribute('data-selected')).toBe(true)
    // The layer's section range is tinted in the source pane.
    expect([...document.querySelectorAll('[data-testid="mirror"] [data-highlight]')].map((e) => e.getAttribute('data-line'))).toEqual(['5', '6'])
  })

  it('a patch for layer 1 keeps the toolchains (patches replace a layer wholesale)', async () => {
    vi.mocked(api.patchWorkspace).mockResolvedValue(loaded())
    await open()
    const input = within(card(1)).getByPlaceholderText('debian:12')
    await fireEvent.input(input, { target: { value: 'ubuntu:24.04' } })
    await fireEvent.change(input)
    await waitFor(() => expect(api.patchWorkspace).toHaveBeenCalledWith('svc', 1, { name: 'svc', base: 'ubuntu:24.04', toolchains: ['go@1.23'], extends: '' }))
  })

  it('a cursor move in the source selects the right card', async () => {
    await open()
    const ta = screen.getByLabelText('Workspace source') as HTMLTextAreaElement
    ta.focus()
    ta.setSelectionRange(SRC.indexOf('[network]'), SRC.indexOf('[network]'))
    await fireEvent.click(ta)
    expect(card(7).hasAttribute('data-selected')).toBe(true)
    expect(card(3).hasAttribute('data-selected')).toBe(false)
  })

  it('shows a diagnostic on its card and in the gutter, not on other cards', async () => {
    vi.mocked(api.getWorkspace).mockResolvedValue(loaded({ diagnostics: [{ line: 6, message: 'unknown package key', severity: 'error' }] }))
    await open()
    expect(within(card(3)).getByTestId('card-diag').textContent).toContain('unknown package key')
    expect(within(card(7)).queryByTestId('card-diag')).toBeNull()
    expect(screen.getAllByTestId('gutter-diag')).toHaveLength(1)
  })

  it('source edits go through PUT draft after the debounce and update the cards', async () => {
    const typed = SRC.replace('debian:12', 'alpine')
    vi.mocked(api.saveWorkspaceDraft).mockResolvedValue({ doc: { ...baseDoc(), workspace: { ...baseDoc().workspace, base: 'alpine' } }, sections, diagnostics: [] } as unknown as WSLoaded)
    await open()
    await fireEvent.input(screen.getByLabelText('Workspace source'), { target: { value: typed } })
    await waitFor(() => expect(api.saveWorkspaceDraft).toHaveBeenCalledWith('svc', typed), { timeout: 2000 })
    await waitFor(() => expect((within(card(1)).getByPlaceholderText('debian:12') as HTMLInputElement).value).toBe('alpine'))
    // The typed text is not overwritten by the parse response.
    expect((screen.getByLabelText('Workspace source') as HTMLTextAreaElement).value).toBe(typed)
  })

  it('publish asks first and shows the diff from the published version', async () => {
    vi.mocked(api.diffWorkspace).mockResolvedValue('@@ -1 +1 @@\n-apt = ["git"]\n+apt = ["git", "make"]')
    vi.mocked(api.publishWorkspace).mockResolvedValue({ n: 2, at: '', buildStatus: 'pending' })
    await open()
    await waitFor(() => expect(api.listBuilds).toHaveBeenCalled())
    await fireEvent.click(await screen.findByRole('button', { name: 'Publish' }))
    expect(api.diffWorkspace).toHaveBeenCalledWith('svc', 1, 0)
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByTestId('diff-lines').textContent).toContain('+apt = ["git", "make"]')
    expect(api.publishWorkspace).not.toHaveBeenCalled()
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Publish' }))
    await waitFor(() => expect(api.publishWorkspace).toHaveBeenCalledWith('svc'))
  })

  it('the pool select posts the size', async () => {
    vi.mocked(api.setWorkspacePool).mockResolvedValue()
    await open()
    await fireEvent.change(await screen.findByLabelText('Pool size'), { target: { value: '2' } })
    await waitFor(() => expect(api.setWorkspacePool).toHaveBeenCalledWith('svc', 2))
  })

  it('Build starts the published version and opens the builds page; Test shell is enabled once published', async () => {
    vi.mocked(api.startBuild).mockResolvedValue({ version: 1 })
    const { nav } = await open()
    const buildBtn = () => screen.getByRole('button', { name: 'Build' })
    await waitFor(() => expect(buildBtn().hasAttribute('disabled')).toBe(false))
    await fireEvent.click(buildBtn())
    await waitFor(() => expect(api.startBuild).toHaveBeenCalledWith('svc', 1))
    await waitFor(() => expect(nav).toHaveBeenCalledWith('#workspaces/svc/builds'))
    expect(screen.getByRole('button', { name: 'Test shell' }).hasAttribute('disabled')).toBe(false)
  })

  it('Test shell opens a terminal on the workspace route', async () => {
    vi.mocked(api.openWorkspaceShell).mockResolvedValue({ terminalId: 't9' })
    await open()
    const btn = () => screen.getByRole('button', { name: 'Test shell' })
    await waitFor(() => expect(btn().hasAttribute('disabled')).toBe(false))
    await fireEvent.click(btn())
    await waitFor(() => expect(api.openWorkspaceShell).toHaveBeenCalledWith('svc', { cols: 80, rows: 24 }))
    expect(screen.getByTestId('test-shell')).toBeTruthy()
  })

  it('the side panel shows start times, and rotating the CA asks first', async () => {
    vi.mocked(api.rotateWorkspaceCA).mockResolvedValue()
    await open()
    const build = await screen.findByTestId('side-build')
    await waitFor(() => expect(build.textContent).toContain('cold 42.0 s'))
    expect(build.textContent).toContain('warm 1.5 s')
    await fireEvent.click(screen.getByText('Rotate CA…'))
    expect(api.rotateWorkspaceCA).not.toHaveBeenCalled()
    await fireEvent.click(screen.getByRole('button', { name: 'Rotate' }))
    await waitFor(() => expect(api.rotateWorkspaceCA).toHaveBeenCalledWith('svc'))
  })

  it('warns when injection needs a vault backend and the secrets backend is env', async () => {
    vi.mocked(api.getSecretsStatus).mockResolvedValue({ backend: 'env', healthy: true })
    await open()
    await waitFor(() => expect(screen.getByTestId('secrets-warning').textContent).toContain('local or OpenBao'))
  })

  it('the network card shows per-host usage and marks hosts new in the draft', async () => {
    vi.mocked(api.getWorkspace).mockImplementation(async (_n, v) =>
      v === 1 ? loaded({ doc: { ...baseDoc(), network: { mode: 'allowlist', egress: [] } } }) : loaded({ doc: { ...baseDoc(), network: { mode: 'allowlist', egress: ['proxy.golang.org'] } } }),
    )
    await open()
    await waitFor(() => expect(within(card(7)).getByText('seen 7×')).toBeTruthy())
    await waitFor(() => expect(within(card(7)).getByText('new in draft')).toBeTruthy())
  })

  it('the view switch persists and hides the layers in Source view', async () => {
    await open()
    await fireEvent.click(screen.getByText('Source'))
    expect(screen.queryByTestId('layers')).toBeNull()
    expect(localStorage.getItem('marshal.ui.ws.view')).toBe('source')
  })

  it('restores the remembered view', async () => {
    localStorage.setItem('marshal.ui.ws.view', 'layers')
    await open()
    expect(screen.queryByLabelText('Workspace source')).toBeNull()
  })

  it('adding a toolchain posts layer 2 with the language@version', async () => {
    vi.mocked(api.patchWorkspace).mockResolvedValue(loaded())
    await open()
    await fireEvent.change(within(card(2)).getByLabelText('Language'), { target: { value: 'node' } })
    await fireEvent.input(within(card(2)).getByLabelText('Toolchain version'), { target: { value: '22' } })
    await fireEvent.click(within(card(2)).getByText('Add'))
    await waitFor(() => expect(api.patchWorkspace).toHaveBeenCalledWith('svc', 2, { name: 'svc', base: 'debian:12', toolchains: ['go@1.23', 'node@22'], extends: '' }))
  })

  it('a mount row is sent once it has a repo and a target', async () => {
    vi.mocked(api.patchWorkspace).mockResolvedValue(loaded())
    await open()
    await waitFor(() => expect(api.listRepos).toHaveBeenCalled())
    await fireEvent.click(within(card(4)).getByText('Add mount'))
    await fireEvent.change(await within(card(4)).findByLabelText('Repo'), { target: { value: 'lib' } })
    expect(api.patchWorkspace).not.toHaveBeenCalledWith('svc', 4, expect.anything())
    await fireEvent.input(within(card(4)).getByLabelText('Mount target'), { target: { value: '/lib' } })
    await fireEvent.change(within(card(4)).getByLabelText('Mount target'))
    await waitFor(() => expect(api.patchWorkspace).toHaveBeenCalledWith('svc', 4, [{ repo: 'lib', volume: '', target: '/lib', readonly: true }]))
  })

  it('checks the verify gate against a picked project, using its build and test commands', async () => {
    await open()
    const gate = await screen.findByTestId('side-gate')
    expect(gate.textContent).toContain('Pick a project')
    await fireEvent.change(await within(gate).findByLabelText('Gate project'), { target: { value: '/p' } })
    await waitFor(() => expect(within(gate).getByText('runnable')).toBeTruthy())
    expect(api.getProjectHealth).toHaveBeenCalledWith('/p')
    vi.mocked(api.getProjectHealth).mockResolvedValue({ verify: { build: 'npm run build', test: 'npm test' } })
    await fireEvent.change(within(gate).getByLabelText('Gate project'), { target: { value: '' } })
    await fireEvent.change(within(gate).getByLabelText('Gate project'), { target: { value: '/p' } })
    await waitFor(() => expect(within(gate).getByText('may be skipped')).toBeTruthy())
  })
})
