import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  clearToken,
  ensureToken,
  getToken,
  isTokenRejected,
  listAgents,
  normalizeToken,
  requestToken,
  setToken,
} from './api'

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

/** A fetch that refuses every request the way the bridge does. */
function unauthorizedReply() {
  return vi.fn().mockResolvedValue({
    ok: false,
    status: 401,
    text: async () => JSON.stringify({ error: 'unauthorized' }),
  })
}

/*
  A rejected token is not a missing one. The server refusing what we sent
  means the value itself is wrong — a stale token, a revoked one, or a token
  belonging to a different bridge. Re-prompting cannot fix it: the same value
  is sent again and the same 401 comes back, forever. The client must stop
  and let the user supply a different token deliberately.
*/
describe('a token the server rejects', () => {
  beforeEach(() => clearToken())
  afterEach(() => {
    clearToken()
    vi.unstubAllGlobals()
  })

  it('stops the loop instead of re-prompting for the refused value', async () => {
    setToken('stale-token')
    // The prompt stands ready to hand back the same refused value.
    const prompts = promptOnce('stale-token')
    const fetchMock = unauthorizedReply()
    vi.stubGlobal('fetch', fetchMock)

    await expect(listAgents()).rejects.toThrow()

    // The several requests in flight at mount, and the pollers after them,
    // must all fail fast. Before this, each one re-prompted and then sent
    // the refused token again.
    for (let i = 0; i < 5; i++) await expect(listAgents()).rejects.toThrow()
    expect(prompts).toHaveLength(0)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('is forgotten when the user enters a different token', async () => {
    setToken('stale-token')
    vi.stubGlobal('fetch', unauthorizedReply())
    await expect(listAgents()).rejects.toThrow()
    expect(isTokenRejected()).toBe(true)

    // The UI's "enter a different token" affordance.
    const prompts = promptOnce('fresh-token')
    expect(requestToken()).toBe('fresh-token')
    expect(isTokenRejected()).toBe(false)
    expect(getToken()).toBe('fresh-token')
    expect(prompts).toHaveLength(1)
  })

  it('stays rejected when the user cancels the re-entry', async () => {
    setToken('stale-token')
    vi.stubGlobal('fetch', unauthorizedReply())
    await expect(listAgents()).rejects.toThrow()

    promptOnce(null)
    expect(() => requestToken()).toThrow()
    expect(isTokenRejected()).toBe(true)
  })
})
