import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte'
import PermissionModal from './PermissionModal.svelte'
import type { PendingPermission } from './store'

afterEach(cleanup)

const capability = (over: Partial<PendingPermission> = {}): PendingPermission => ({
  toolCallId: 'tc-1',
  toolName: 'mode.request',
  reason: 'need act',
  exactOnly: false,
  modeAccess: {
    requestId: 'r-1',
    sessionId: 's-1',
    reason: 'need act',
    current: { mode: 'ask', auto: false, system_access: false, access_revision: 2 },
    missingEditing: true,
    missingSystem: true,
  },
  ...over,
})

describe('PermissionModal', () => {
  it('grants only the requested capability bundle', async () => {
    const onResolve = vi.fn()
    const onDeny = vi.fn()
    render(PermissionModal, { permission: capability(), onResolve, onDeny })

    expect(screen.getByText('need act')).toBeTruthy()
    expect(screen.getByText('Ask')).toBeTruthy()
    expect(screen.getByText('Act + System')).toBeTruthy()
    // Auto is never offered as a capability choice.
    expect(screen.queryByRole('button', { name: /Auto/ })).toBeNull()

    await fireEvent.click(screen.getByRole('button', { name: 'Allow Act + System' }))
    expect(onDeny).not.toHaveBeenCalled()
    expect(onResolve).toHaveBeenCalledWith({
      approved: true,
      modeAccess: {
        requestId: 'r-1',
        sessionId: 's-1',
        current: { mode: 'ask', auto: false, system_access: false, access_revision: 2 },
        mode: 'act',
      },
    })
  })

  it('preserves the mode and Auto for a System-only approval', async () => {
    const onResolve = vi.fn()
    render(PermissionModal, {
      permission: capability({
        reason: 'broader access',
        modeAccess: {
          requestId: 'r-2',
          sessionId: 's-1',
          reason: 'broader access',
          current: { mode: 'act', auto: true, system_access: false, access_revision: 3 },
          missingEditing: false,
          missingSystem: true,
        },
      }),
      onResolve,
      onDeny: vi.fn(),
    })

    expect(screen.getByText('Act + Auto')).toBeTruthy()
    expect(screen.getByText('System')).toBeTruthy()
    await fireEvent.click(screen.getByRole('button', { name: 'Allow System' }))
    // No mode is carried, so the committed Act + Auto stays exactly as it was.
    expect(onResolve).toHaveBeenCalledWith({
      approved: true,
      modeAccess: {
        requestId: 'r-2',
        sessionId: 's-1',
        current: { mode: 'act', auto: true, system_access: false, access_revision: 3 },
      },
    })
  })

  it('denies a refreshed presentation by default', async () => {
    const onResolve = vi.fn()
    const onDeny = vi.fn()
    // A refreshed presentation (a new revision, rendered afresh) must not
    // grant anything on its own; the default action is the negative one.
    render(PermissionModal, {
      permission: capability({
        modeAccess: {
          ...capability().modeAccess!,
          current: { mode: 'ask', auto: false, system_access: false, access_revision: 9 },
        },
      }),
      onResolve,
      onDeny,
    })
    expect(onResolve).not.toHaveBeenCalled()

    await fireEvent.click(screen.getByRole('button', { name: 'Deny' }))
    expect(onDeny).toHaveBeenCalled()
    expect(onResolve).not.toHaveBeenCalled()
  })

  it('shows the exact command without standing grants for an exact-only approval', () => {
    render(PermissionModal, {
      permission: { toolCallId: 'tc', toolName: 'shell.run', command: 'rm -rf build', reason: 'destructive', exactOnly: true },
      onResolve: vi.fn(),
      onDeny: vi.fn(),
    })

    expect(screen.getByText('rm -rf build')).toBeTruthy()
    expect(screen.getByText(/grants no standing capability/)).toBeTruthy()
    expect(screen.queryByTestId('capability-scope')).toBeNull()
    expect(screen.queryByRole('button', { name: /^Allow/ })).toBeNull()
    expect(screen.getByRole('button', { name: 'Approve' })).toBeTruthy()
  })
})
