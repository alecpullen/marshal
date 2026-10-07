import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { clearToken, ensureToken, getToken, normalizeToken, setToken } from './api'

/** A prompt that answers with `value` and counts how many times it was shown. */
function promptOnce(value: string | null) {
  const calls: string[] = []
  vi.stubGlobal('prompt', (msg?: string) => {
    calls.push(msg ?? '')
    return value
  })
  return calls
}

describe('token entry', () => {
  beforeEach(() => clearToken())
  afterEach(() => {
    clearToken()
    vi.unstubAllGlobals()
  })

  it('normalizeToken strips what a clipboard adds around a token', () => {
    expect(normalizeToken('w5testtoken')).toBe('w5testtoken')
    expect(normalizeToken('  w5testtoken  ')).toBe('w5testtoken')
    expect(normalizeToken('Bearer w5testtoken')).toBe('w5testtoken')
    expect(normalizeToken('bearer w5testtoken')).toBe('w5testtoken')
    expect(normalizeToken('Bearer  w5testtoken')).toBe('w5testtoken')
    expect(normalizeToken('Authorization: Bearer w5testtoken')).toBe('w5testtoken')
    expect(normalizeToken('Authorization: Bearer w5testtoken\r\n')).toBe('w5testtoken')
  })

  it('setToken stores the normalized value', () => {
    setToken('Bearer w5testtoken')
    expect(getToken()).toBe('w5testtoken')
  })

  it('an entry of only "Bearer " stores nothing', () => {
    setToken('Bearer ')
    expect(getToken()).toBeNull()
  })

  it('ensureToken accepts a pasted Authorization line and stores the bare token', () => {
    // This is the loop: the header goes out as "Bearer Bearer x", the bridge
    // 401s byte-for-byte, the token is cleared, and the prompt returns.
    promptOnce('Bearer w5testtoken')
    expect(ensureToken()).toBe('w5testtoken')
    expect(getToken()).toBe('w5testtoken')
    expect(ensureToken()).toBe('w5testtoken')
  })

  it('a declined prompt does not re-prompt on every later request', () => {
    const prompts = promptOnce(null)
    expect(() => ensureToken()).toThrow()
    expect(prompts).toHaveLength(1)
    // Twenty in-flight requests used to mean twenty dialogs. They now fail
    // fast instead of storming the user with prompts.
    for (let i = 0; i < 20; i++) expect(() => ensureToken()).toThrow()
    expect(prompts).toHaveLength(1)
  })

  it('an explicit token re-arms prompting after a decline', () => {
    const prompts = promptOnce(null)
    expect(() => ensureToken()).toThrow()
    setToken('w5testtoken')
    expect(getToken()).toBe('w5testtoken')
    clearToken()
    promptOnce('w5testtoken')
    expect(ensureToken()).toBe('w5testtoken')
    expect(prompts).toHaveLength(1)
  })
})
