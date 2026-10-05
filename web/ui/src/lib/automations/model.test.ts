import { describe, expect, it } from 'vitest'
import { ciTone, forgeFileUrl, groupBySeverity, prNumber, recentFixes } from './model'

describe('automations model', () => {
  it('groups findings most severe first and puts unknown severities last', () => {
    const f = (id: string, severity: string) => ({ id, severity, title: id, body: '' })
    expect(groupBySeverity([f('a', 'nit'), f('b', 'weird'), f('c', 'blocker'), f('d', 'nit')]).map((g) => [g.severity, g.findings.map((x) => x.id)])).toEqual([
      ['blocker', ['c']],
      ['nit', ['a', 'd']],
      ['weird', ['b']],
    ])
  })

  it('reads the PR number from forge URLs', () => {
    expect(prNumber('https://github.com/o/n/pull/12')).toBe(12)
    expect(prNumber('https://gitea.x/o/n/pulls/3?x=1')).toBe(3)
    expect(prNumber('https://x/pr/1')).toBe(1)
    expect(prNumber('https://github.com/o/n')).toBeUndefined()
    expect(prNumber(undefined)).toBeUndefined()
  })

  it('builds file links per forge from https and ssh URLs', () => {
    expect(forgeFileUrl({ url: 'https://github.com/o/n.git', forge: 'github' }, 'abc', 'src/a b.go', 7)).toBe('https://github.com/o/n/blob/abc/src/a%20b.go#L7')
    expect(forgeFileUrl({ url: 'git@git.example.com:o/n.git', forge: 'gitea' }, 'abc', 'a.go')).toBe('https://git.example.com/o/n/src/commit/abc/a.go')
    expect(forgeFileUrl({ url: 'ssh://git@host:2222/o/n.git', forge: 'github' }, 'abc', 'a.go')).toBe('https://host/o/n/blob/abc/a.go')
    expect(forgeFileUrl({ url: '/local/path', forge: 'github' }, 'abc', 'a.go')).toBeNull()
    expect(forgeFileUrl(undefined, 'abc', 'a.go')).toBeNull()
  })

  it('maps CI statuses to tones', () => {
    expect([ciTone('fixed'), ciTone("didn't reproduce"), ciTone('gave up'), ciTone('gave up: gate failed')]).toEqual(['ok', 'neutral', 'err', 'err'])
  })

  it('keeps only fresh fixed results that opened a PR', () => {
    const now = Date.parse('2026-10-05T12:00:00Z')
    const h = (id: string, status: string, createdAt: string | undefined, prUrl: string | null = 'https://x/pull/1') => ({ id, repoId: 'r', sha: 's', check: 'c', status, reason: '', costUsd: 0, createdAt, prUrl: prUrl ?? undefined })
    expect(recentFixes([h('a', 'fixed', '2026-10-05T01:00:00Z'), h('b', 'fixed', '2026-10-03T01:00:00Z'), h('c', 'gave up', '2026-10-05T01:00:00Z'), h('d', 'fixed', '2026-10-05T01:00:00Z', null)], now).map((x) => x.id)).toEqual(['a'])
  })
})
