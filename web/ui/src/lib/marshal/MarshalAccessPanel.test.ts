import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import MarshalAccessPanel from './MarshalAccessPanel.svelte'
import * as api from '../api.js'
import { MarshalAccessConflictError, type MarshalAccessView } from '../api.js'

vi.mock('../api.js', async (importActual) => {
  const actual = await importActual<typeof import('../api.js')>()
  return {
    ...actual,
    getMarshalAccess: vi.fn(),
    putMarshalAccessDefault: vi.fn(),
    putMarshalAccessOverride: vi.fn(),
    putMarshalSelection: vi.fn(),
  }
})

afterEach(cleanup)

/** accessView is what a read answers: one session of one project with both levels stored. */
const accessView = (over: Partial<MarshalAccessView> = {}): MarshalAccessView => ({
  state: 'connected',
  instanceId: '/run/user/501/marshal/daemon.sock',
  generation: '4242:1700000000000',
  projectId: 'project-1',
  sessionId: 's1',
  default: { access: { read: true, createDraft: true }, version: 'default-1' },
  override: { access: { editDraft: false }, version: 'session-1' },
  overrideVersion: 'session-1',
  effective: { read: true, createDraft: true, editDraft: false, approveAndQueue: false, answerQuestion: false, approvalAuthority: 'none' },
  source: { project: 'override', session: 'override' },
  editable: true,
  ...over,
})

const mount = (over: Record<string, unknown> = {}) =>
  render(MarshalAccessPanel, { projectId: 'project-1', sessionId: 's1', levels: ['project', 'session'], ...over })

/** effectiveRow is the effective-access line for one capability. */
const effectiveRow = (label: string): HTMLElement => {
  const rows = within(screen.getByTestId('marshal-access-effective')).getAllByRole('listitem')
  const found = rows.find((row) => row.textContent?.includes(label))
  if (!found) throw new Error(`no effective row for ${label}`)
  return found
}

beforeEach(() => {
  vi.clearAllMocks()
  ;(api.getMarshalAccess as Mock).mockResolvedValue(accessView())
  ;(api.putMarshalAccessDefault as Mock).mockImplementation(async (_p: string, access: api.MarshalAccess, version: string) =>
    accessView({ default: { access, version: `${version}+` } }),
  )
  ;(api.putMarshalAccessOverride as Mock).mockImplementation(async (_p: string, _s: string, access: api.MarshalAccess, version: string) =>
    accessView({ override: { access, version: `${version}+` }, overrideVersion: `${version}+` }),
  )
  ;(api.putMarshalSelection as Mock).mockImplementation(async () => accessView({ selectedProjectId: 'project-2' }))
})

describe('MarshalAccessPanel inheritance and sources', () => {
  it('shows each effective value with the level it came from, and each level’s stored value', async () => {
    mount()
    await waitFor(() => expect(screen.getByTestId('marshal-access-project')).toBeTruthy())

    // The project grants create-draft; the session denies edit-draft. Each
    // effective row names the level that decided it.
    expect(within(effectiveRow('Create drafts')).getByText('allow')).toBeTruthy()
    expect(within(effectiveRow('Create drafts')).getByText('project')).toBeTruthy()
    expect(within(effectiveRow('Edit drafts')).getByText('deny')).toBeTruthy()
    expect(within(effectiveRow('Edit drafts')).getByText('session')).toBeTruthy()
    // A field neither level stores is the built-in default's.
    expect(within(effectiveRow('Answer questions')).getByText('built-in default')).toBeTruthy()

    // The editors show what each level stores: the inherited field reads
    // "inherit" rather than the effective value it resolves to.
    expect((screen.getByLabelText('Project Create drafts') as HTMLSelectElement).value).toBe('allow')
    expect((screen.getByLabelText('Project Edit drafts') as HTMLSelectElement).value).toBe('inherit')
    expect((screen.getByLabelText('Session Edit drafts') as HTMLSelectElement).value).toBe('deny')
    expect((screen.getByLabelText('Session Read') as HTMLSelectElement).value).toBe('inherit')
  })

  it('shows a disabled read as a denial without hiding the other rows', async () => {
    ;(api.getMarshalAccess as Mock).mockResolvedValue(
      accessView({
        default: { access: { read: false }, version: 'default-1' },
        override: undefined,
        overrideVersion: 'session-absent',
        effective: { read: false, createDraft: false, editDraft: false, answerQuestion: false, approveAndQueue: false, approvalAuthority: 'none' },
      }),
    )
    mount()
    await waitFor(() => expect(screen.getByTestId('marshal-access-project')).toBeTruthy())

    expect(screen.getByText('read denied')).toBeTruthy()
    expect(within(effectiveRow('Read')).getByText('deny')).toBeTruthy()
    // The rows that cannot act are still there: a denial is one field, not a lock.
    expect(screen.getByLabelText('Project Create drafts')).toBeTruthy()
    expect((screen.getByLabelText('Project Read') as HTMLSelectElement).value).toBe('deny')
  })
})

describe('MarshalAccessPanel writes', () => {
  it('writes the whole project layer with the version it read, and keeps a stale edit', async () => {
    mount()
    await waitFor(() => expect(screen.getByTestId('marshal-access-project')).toBeTruthy())

    await fireEvent.change(screen.getByLabelText('Project Create drafts'), { target: { value: 'deny' } })
    await fireEvent.click(screen.getByTestId('marshal-access-project-save'))
    await waitFor(() => expect(api.putMarshalAccessDefault).toHaveBeenCalled())
    expect(api.putMarshalAccessDefault).toHaveBeenCalledWith('project-1', { read: true, createDraft: false }, 'default-1')
    expect((await screen.findByRole('status')).textContent).toContain('Project defaults saved')

    // A later editor won the race: the answer carries the value that won, the
    // draft the user typed stays, and the row says why.
    const winner = accessView({ default: { access: { read: true, createDraft: true }, version: 'default-2' } })
    ;(api.putMarshalAccessDefault as Mock).mockRejectedValueOnce(new MarshalAccessConflictError(winner))
    await fireEvent.change(screen.getByLabelText('Project Create drafts'), { target: { value: 'deny' } })
    await fireEvent.click(screen.getByTestId('marshal-access-project-save'))
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('changed elsewhere'))
    expect((screen.getByLabelText('Project Create drafts') as HTMLSelectElement).value).toBe('deny')
  })

  it('writes a session override, and revoking it writes a level that inherits', async () => {
    mount()
    await waitFor(() => expect(screen.getByTestId('marshal-access-session')).toBeTruthy())

    await fireEvent.change(screen.getByLabelText('Session Answer questions'), { target: { value: 'allow' } })
    await fireEvent.click(screen.getByTestId('marshal-access-session-save'))
    await waitFor(() => expect(api.putMarshalAccessOverride).toHaveBeenCalled())
    expect(api.putMarshalAccessOverride).toHaveBeenCalledWith('project-1', 's1', { editDraft: false, answerQuestion: true }, 'session-1', '')

    await fireEvent.click(screen.getByTestId('marshal-access-session-revoke'))
    await waitFor(() => expect(api.putMarshalAccessOverride).toHaveBeenCalledTimes(2))
    // The revoke names the version the first write left behind, not the one it
    // replaced: a write is always conditional on what the surface last saw.
    expect((api.putMarshalAccessOverride as Mock).mock.calls[1]).toEqual(['project-1', 's1', {}, 'session-1+', ''])
  })

  it('lets a person choose standing approval authority explicitly', async () => {
    mount()
    await waitFor(() => expect(screen.getByTestId('marshal-access-project')).toBeTruthy())

    await fireEvent.change(screen.getByLabelText('Project approval authority'), { target: { value: 'standing' } })
    await fireEvent.click(screen.getByTestId('marshal-access-project-save'))
    await waitFor(() => expect(api.putMarshalAccessDefault).toHaveBeenCalled())
    expect(api.putMarshalAccessDefault).toHaveBeenCalledWith(
      'project-1',
      { read: true, createDraft: true, approvalAuthority: 'standing' },
      'default-1',
    )
  })

  it('records the project an ambiguous session acts on, against the selection version', async () => {
    ;(api.getMarshalAccess as Mock).mockResolvedValue(
      accessView({
        state: 'ambiguous',
        projectId: undefined,
        candidates: ['project-1', 'project-2'],
        selectionVersion: 'selection-1',
        selectedProjectId: '',
      }),
    )
    mount()
    await waitFor(() => expect(screen.getByTestId('marshal-access-candidates')).toBeTruthy())
    expect(screen.getByTestId('marshal-access-candidates').textContent).toContain('project-1, project-2')

    await fireEvent.change(screen.getByLabelText('Studio instance'), { target: { value: 'project-2' } })
    await fireEvent.click(screen.getByTestId('marshal-access-select-save'))
    await waitFor(() => expect(api.putMarshalSelection).toHaveBeenCalled())
    expect(api.putMarshalSelection).toHaveBeenCalledWith('s1', 'project-2', 'selection-1')
    expect((await screen.findByRole('status')).textContent).toContain('Instance selected')
  })

  it('keeps the policy rows usable while the connection is offline', async () => {
    ;(api.getMarshalAccess as Mock).mockResolvedValue(
      accessView({ state: 'unavailable', reason: 'the daemon does not track this session' }),
    )
    mount()
    await waitFor(() => expect(screen.getByTestId('marshal-access-reason')).toBeTruthy())
    expect(screen.getByTestId('marshal-access-reason').textContent).toContain('the daemon does not track this session')

    // An offline state is a state, not a lock: the policy is a stored setting.
    await fireEvent.change(screen.getByLabelText('Project Read'), { target: { value: 'deny' } })
    await fireEvent.click(screen.getByTestId('marshal-access-project-save'))
    await waitFor(() => expect(api.putMarshalAccessDefault).toHaveBeenCalled())
  })

  it('says plainly when a state leaves nothing to edit', async () => {
    ;(api.getMarshalAccess as Mock).mockResolvedValue(
      accessView({ state: 'unauthorized', editable: false, projectId: undefined }),
    )
    mount()
    await waitFor(() => expect(screen.getByTestId('marshal-access-read-only')).toBeTruthy())
    expect(screen.getByTestId('marshal-access-read-only').textContent).toContain('does not track this session')
    expect(screen.queryByTestId('marshal-access-project-save')).toBeNull()
  })
})

describe('MarshalAccessPanel keyboard and mobile', () => {
  it('exposes every control as a labelled, keyboard-reachable form control', async () => {
    mount()
    await waitFor(() => expect(screen.getByTestId('marshal-access-project')).toBeTruthy())

    // Native selects and buttons: reachable by tab, operable by arrow keys and
    // enter, and named for a screen reader.
    for (const label of ['Project Read', 'Project Create drafts', 'Project approval authority', 'Session Edit drafts', 'Session approval authority']) {
      const control = screen.getByLabelText(label)
      expect(control.tagName).toBe('SELECT')
      expect(control.closest('label')).toBeTruthy()
      expect((control as HTMLSelectElement).options.length).toBeGreaterThan(1)
    }
    for (const testId of ['marshal-access-project-save', 'marshal-access-project-reset', 'marshal-access-session-save', 'marshal-access-session-revoke']) {
      expect(screen.getByTestId(testId).tagName).toBe('BUTTON')
    }
    // A narrow viewport stacks the editors instead of squeezing them.
    expect(screen.getByLabelText('Project Read').closest('.grid')?.className).toContain('sm:grid-cols-2')
  })
})
