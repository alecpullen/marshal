import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  adoptCodeFromURL,
  adoptTokenFromURL,
  clearToken,
  getToken,
  setToken,
} from './api'

/** atURL points the document at path, as a fresh navigation would. */
function atURL(path: string): void {
  window.history.replaceState(null, '', path)
}

describe('adoptTokenFromURL', () => {
  beforeEach(() => {
    clearToken()
  })
  afterEach(() => {
    clearToken()
    atURL('/')
  })

  it('adopts the token and strips it from the address bar', () => {
    atURL('/?token=abc123')
    adoptTokenFromURL()
    expect(getToken()).toBe('abc123')
    // The URL a user could copy or find in history holds no credential.
    expect(window.location.search).toBe('')
  })

  it('reduces a pasted header form to the bare token', () => {
    atURL('/?token=' + encodeURIComponent('Bearer abc123'))
    adoptTokenFromURL()
    expect(getToken()).toBe('abc123')
  })

  it('leaves a URL with no token alone', () => {
    setToken('existing')
    atURL('/#chat/s-1')
    adoptTokenFromURL()
    expect(getToken()).toBe('existing')
    expect(window.location.hash).toBe('#chat/s-1')
  })

  it('removes an empty token without adopting it', () => {
    atURL('/?token=')
    adoptTokenFromURL()
    expect(getToken()).toBeNull()
    expect(window.location.search).toBe('')
  })

  it('keeps the other query parameters', () => {
    atURL('/?token=abc&project=%2Ftmp%2Fp')
    adoptTokenFromURL()
    expect(getToken()).toBe('abc')
    expect(new URLSearchParams(window.location.search).get('project')).toBe('/tmp/p')
  })
})

describe('adoptCodeFromURL', () => {
  const token = 'daemon-token'

  /** reply is the shape of the answer the exchange reads. */
  function reply(status: number, body: unknown): Response {
    return {
      ok: status >= 200 && status < 300,
      status,
      json: async () => body,
    } as Response
  }

  beforeEach(() => {
    clearToken()
    atURL('/')
  })
  afterEach(() => {
    clearToken()
    vi.unstubAllGlobals()
    atURL('/')
  })

  it('swaps the fragment code for the token and cleans the address bar', async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: unknown, init?: RequestInit) => {
        calls.push({ url: String(url), init })
        return reply(200, { token })
      }),
    )

    atURL('/#code=one-time')
    await adoptCodeFromURL()

    expect(getToken()).toBe(token)
    // The code is spent, and the address bar a person might copy holds none.
    expect(window.location.hash).toBe('')
    expect(calls).toHaveLength(1)
    expect(calls[0].url).toBe('/api/studio/session')
    expect(calls[0].init?.method).toBe('POST')
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({ code: 'one-time' })
  })

  it('keeps the fragment parameters that are not the code', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => reply(200, { token })))
    atURL('/#project=%2Ftmp%2Fp&code=one-time')
    await adoptCodeFromURL()
    expect(new URLSearchParams(window.location.hash.slice(1)).get('project')).toBe('/tmp/p')
    expect(window.location.hash).not.toContain('code')
  })

  it('leaves the page signed out when the daemon refuses the code', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => reply(401, { error: 'expired' })))
    atURL('/#code=stale')
    await adoptCodeFromURL()
    expect(getToken()).toBeNull()
    expect(window.location.hash).toBe('')
  })

  it('does not touch a fragment with no code', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    atURL('/#chat/s-1')
    await adoptCodeFromURL()
    expect(fetchMock).not.toHaveBeenCalled()
    expect(window.location.hash).toBe('#chat/s-1')
  })

  it('survives a network failure', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new Error('offline')
      }),
    )
    atURL('/#code=one-time')
    await expect(adoptCodeFromURL()).resolves.toBeUndefined()
    expect(getToken()).toBeNull()
  })
})
