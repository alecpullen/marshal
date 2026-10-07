import { afterAll, beforeEach } from 'vitest'

/*
  Node 26 defines an experimental `localStorage` global that resolves to
  undefined unless the process was started with `--localstorage-file`.
  Vitest's jsdom environment installs jsdom's window onto globalThis but
  leaves that existing key alone, so `window.localStorage` is undefined in
  every test — and any test that touches it fails with "Cannot read
  properties of undefined (reading 'clear')".

  jsdom itself provides a working Storage (verified against jsdom 30), so
  the loss is purely from Node's global taking precedence. Install a small
  in-memory Storage when the real one is missing, and never replace a
  working implementation.
*/
class MemoryStorage implements Storage {
  #data = new Map<string, string>()

  get length(): number {
    return this.#data.size
  }

  clear(): void {
    this.#data.clear()
  }

  getItem(key: string): string | null {
    return this.#data.get(String(key)) ?? null
  }

  key(index: number): string | null {
    return [...this.#data.keys()][index] ?? null
  }

  removeItem(key: string): void {
    this.#data.delete(String(key))
  }

  setItem(key: string, value: string): void {
    this.#data.set(String(key), String(value))
  }
}

function ensureStorage(name: 'localStorage' | 'sessionStorage'): void {
  const current = (globalThis as Record<string, unknown>)[name] as Storage | undefined
  if (current && typeof current.getItem === 'function' && typeof current.clear === 'function') return
  Object.defineProperty(globalThis, name, {
    value: new MemoryStorage(),
    configurable: true,
    writable: true,
  })
}

ensureStorage('localStorage')
ensureStorage('sessionStorage')

// Each test file gets a fresh store, matching jsdom's per-file window.
beforeEach(() => {
  globalThis.localStorage.clear()
  globalThis.sessionStorage.clear()
})

/*
  bits-ui's body-scroll-lock restores the body style on a ~24ms timer so a
  destroy-and-recreate in the same tick cannot reset it too early. Vitest
  tears the jsdom environment down the instant a file's last test ends, so
  that timer fired with `document` already gone: vitest reported
  "ReferenceError: document is not defined" as an unhandled error, and the
  whole run exited non-zero even when every assertion passed.

  Draining the timer while the window is still alive keeps the exit status
  meaningful — a red run should mean a failing test, not a stray timer.
*/
afterAll(async () => {
  await new Promise((resolve) => setTimeout(resolve, 50))
})
