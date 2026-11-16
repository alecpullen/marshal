import { describe, expect, it, vi } from 'vitest'
import { createNavigationGuard } from './navigationGuard'

describe('createNavigationGuard', () => {
  it('allowsWithoutGuard', () => {
    // Nothing registered: the shell leaves without asking anyone.
    expect(createNavigationGuard().allow()).toBe(true)
  })

  it('delegatesToGuard', () => {
    const guard = createNavigationGuard()
    const asked = vi.fn(() => false)
    guard.register(asked)
    expect(guard.allow()).toBe(false)
    expect(asked).toHaveBeenCalledTimes(1)
    asked.mockReturnValue(true)
    expect(guard.allow()).toBe(true)
    expect(asked).toHaveBeenCalledTimes(2)
  })

  it('clearsGuard', () => {
    const guard = createNavigationGuard()
    guard.register(() => false)
    expect(guard.allow()).toBe(false)
    // A destroyed view clears itself by registering nothing.
    guard.register(undefined)
    expect(guard.allow()).toBe(true)
  })
})
