import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { adoptTokenFromURL, clearToken, getToken, setToken } from './api'

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
