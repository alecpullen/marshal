import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, cleanup, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import PermissionModal from '../PermissionModal.svelte'

afterEach(cleanup)

const permission = { toolName: 'shell', command: 'rm -rf build', diff: '' } as never

describe('PermissionModal dismissal', () => {
  /*
    Marshal's permission wait has no timeout (web/bridge/registry.go:387),
    so a dismissal that produces no decision parks the agent indefinitely.
    Escape must therefore resolve to a deny the agent actually receives.
  */
  it('denies on Escape rather than closing silently', async () => {
    const onDeny = vi.fn()
    render(PermissionModal, { permission, onResolve: vi.fn(), onDeny })

    await userEvent.keyboard('{Escape}')

    expect(onDeny).toHaveBeenCalledOnce()
  })

  it('does not resolve when the backdrop is clicked', async () => {
    const onDeny = vi.fn()
    const onResolve = vi.fn()
    const { baseElement } = render(PermissionModal, { permission, onResolve, onDeny })

    const overlay = baseElement.querySelector('[data-dialog-overlay]')
    expect(overlay).not.toBeNull()
    await userEvent.click(overlay as Element)

    // An accidental backdrop click must not be able to strand an agent,
    // and must not approve anything either.
    expect(onDeny).not.toHaveBeenCalled()
    expect(onResolve).not.toHaveBeenCalled()
  })

  it('is announced as a modal dialog with an accessible name', () => {
    render(PermissionModal, { permission, onResolve: vi.fn(), onDeny: vi.fn() })
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveProperty('ariaModal', 'true')
    expect(dialog.textContent).toContain('Permission request')
  })
})
