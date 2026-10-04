import { describe, expect, it } from 'vitest'
import { contentChips, gateRunnable, latestVersion, poolLabel, workspaceRef } from './model'
import type { WSDoc } from '../api'

describe('gateRunnable', () => {
  it.each([
    ['go test ./...', ['go@1.23'], 'runnable'],
    ['npm test', ['node@22'], 'runnable'],
    ['node build.js', ['node@22'], 'runnable'],
    ['pytest -q', ['python@3.12'], 'runnable'],
    ['python -m unittest', ['python@3.12'], 'runnable'],
    ['cargo test', ['rust@1.80'], 'runnable'],
    ['go test ./...', ['node@22'], 'may-skip'],
    ['cargo test', [], 'may-skip'],
  ])('%s with %j is %s', (cmd, tc, want) => {
    expect(gateRunnable(tc, cmd)).toBe(want)
  })
  it('is unknown without a command or for an unmapped first word', () => {
    expect(gateRunnable(['go@1'], undefined)).toBe('unknown')
    expect(gateRunnable(['go@1'], '  ')).toBe('unknown')
    expect(gateRunnable(['go@1'], 'make test')).toBe('unknown')
  })
})

describe('model helpers', () => {
  it('contentChips lists base, toolchains and a package count', () => {
    const doc = { workspace: { base: 'debian:12', toolchains: ['go@1.23'] }, packages: { apt: ['git'], go: [], npm: [], pip: [] } } as unknown as WSDoc
    expect(contentChips(doc)).toEqual(['debian:12', 'go@1.23', '1 package'])
    expect(contentChips(undefined)).toEqual([])
  })
  it('workspaceRef is bare for Studio and repo:<name> for repo templates', () => {
    expect(workspaceRef({ source: 'studio', name: 'a' })).toBe('a')
    expect(workspaceRef({ source: 'repo', name: 'a' })).toBe('repo:a')
  })
  it('poolLabel and latestVersion', () => {
    expect(poolLabel(2)).toBe('2 warm')
    expect(poolLabel(0)).toBe('')
    expect(latestVersion({ published: 1, versions: [{ n: 1, at: 0, buildStatus: 'ok' }, { n: 2, at: 0, buildStatus: 'pending' }] })?.n).toBe(1)
    expect(latestVersion({})).toBeNull()
  })
})
