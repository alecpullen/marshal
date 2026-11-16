import { describe, expect, it } from 'vitest'
import type { FactoryCheck, FactoryRound } from '../factory'
import type { PreviewDeployment, PreviewEnvironment } from '../preview'
import {
  checkState,
  ciEvidence,
  currentStatusLinks,
  deploymentSegments,
  displayText,
  environmentSegments,
  headBadge,
  isHttpUrl,
  narrationSections,
  reviewVerdict,
} from './reviewModel'

const check = (over: Partial<FactoryCheck> = {}): FactoryCheck =>
  ({ key: { kind: 'check_run', name: 'ci/build', appId: 0 }, id: 'c1', sha: 'abc1234', status: 'completed', conclusion: 'success', url: '', logTail: '', observedAt: '', ...over }) as FactoryCheck

const deployment = (over: Partial<PreviewDeployment> = {}): PreviewDeployment =>
  ({ id: 'd1', environment: 'production', originalEnvironment: 'production', ref: 'feature/x', sha: '', task: 'deploy', state: '', environmentUrl: '', targetUrl: '', logUrl: '', description: '', transient: false, production: false, createdAt: '', updatedAt: '', ...over }) as PreviewDeployment

const environment = (over: Partial<PreviewEnvironment> = {}): PreviewEnvironment =>
  ({ name: 'staging', id: 'e1', htmlUrl: '', branchPolicy: '', protectionRuleCount: 0, ...over }) as PreviewEnvironment

describe('checkState', () => {
  it('reads the conclusion, then the status, then pending', () => {
    expect(checkState(check({ conclusion: 'failure' }))).toBe('failure')
    // A check still running has no conclusion, so the provider's status is what stands in.
    expect(checkState(check({ conclusion: '', status: 'in_progress' }))).toBe('in_progress')
    expect(checkState(check({ conclusion: '', status: '' }))).toBe('pending')
  })
})

describe('narrationSections', () => {
  it('keeps the report order and folds trade-offs into the alternatives section', () => {
    expect(narrationSections({
      issue: 'Issue', approach: 'Approach', existingBehaviour: 'Existing', alternatives: 'Alternatives',
      tradeoffs: 'Trade-offs', choice: 'Choice', verification: 'Verification', limitations: 'Limitations',
    })).toEqual([
      { heading: 'Issue', paragraphs: ['Issue'] },
      { heading: 'Approach', paragraphs: ['Approach'] },
      { heading: 'Existing behaviour', paragraphs: ['Existing'] },
      { heading: 'Alternatives and trade-offs', paragraphs: ['Alternatives', 'Trade-offs'] },
      { heading: 'Implementation choice', paragraphs: ['Choice'] },
      { heading: 'Verification', paragraphs: ['Verification'] },
      { heading: 'Limitations', paragraphs: ['Limitations'] },
    ])
  })

  it('omits a section the worker left empty rather than showing it blank', () => {
    const sections = narrationSections({
      issue: 'Issue', approach: '', existingBehaviour: '', alternatives: '', tradeoffs: 'Trade-offs',
      choice: '', verification: '', limitations: '',
    })
    expect(sections.map((section) => section.heading)).toEqual(['Issue', 'Alternatives and trade-offs'])
    expect(sections[1].paragraphs).toEqual(['Trade-offs'])
    expect(narrationSections(null)).toEqual([])
  })
})

describe('isHttpUrl', () => {
  it('accepts only literal absolute http(s) URLs with a host', () => {
    expect(isHttpUrl('https://example.test/x')).toBe(true)
    expect(isHttpUrl('http://example.test')).toBe(true)
    expect(isHttpUrl('HTTPS://EXAMPLE.TEST/x')).toBe(true)
  })

  it('rejects every URL the bridge validator factoryURLClickable rejects, without repairing it', () => {
    for (const value of [
      '', 'example.test/x', '/relative', 'javascript:alert(1)', 'data:text/html,<b>x</b>', 'gopher://example.test',
      'https:foo', 'https://', ' https://example.test/x', 'https://example.test/x ', 'https://exa mple.test',
      'https://example.test/x\u0085', 'https://example.test/x\u0000', 'https://example.test/`x`', 'https://example.test/<x>',
    ]) {
      // No trimming, no escaping: a near-miss stays text.
      expect(isHttpUrl(value), value).toBe(false)
    }
  })

  it('bounds the URL at 2048 UTF-8 bytes', () => {
    const base = 'https://example.test/'
    expect(isHttpUrl(base + 'a'.repeat(2048 - base.length))).toBe(true)
    expect(isHttpUrl(base + 'a'.repeat(2049 - base.length))).toBe(false)
    // Four bytes per character, so the byte bound bites long before the character bound.
    expect(isHttpUrl(base + '😀'.repeat(600))).toBe(false)
  })
})

describe('displayText', () => {
  it('bounds an unstyled provider string without cutting a character in half', () => {
    expect(displayText('short')).toBe('short')
    const long = '😀'.repeat(600)
    const bounded = displayText(long)
    expect([...bounded]).toHaveLength(513)
    expect(bounded.endsWith('…')).toBe(true)
    expect(displayText('😀'.repeat(512))).toBe('😀'.repeat(512))
  })
})

describe('headBadge', () => {
  it('compares against the last factory-recorded head case-insensitively', () => {
    expect(headBadge('ABC1234', 'abc1234')).toEqual({ label: 'matches the last factory-recorded PR head', warning: false })
    expect(headBadge('abc1234', 'fedcba9')).toEqual({ label: 'differs from the last factory-recorded PR head', warning: true })
  })

  it('says nothing when there is no recorded head to compare with', () => {
    expect(headBadge('abc1234', '')).toBeNull()
    expect(headBadge('', 'abc1234')).toBeNull()
  })
})

describe('preview segments', () => {
  it('describes a deployment with its environment, state, revision, link and description', () => {
    expect(deploymentSegments(deployment({ sha: 'abc1234', state: 'success', environmentUrl: 'https://preview.test', logUrl: 'https://ci.test/log', description: 'ready' }), 'abc1234'))
      .toEqual([
        { kind: 'code', text: 'production' },
        { kind: 'code', text: 'success' },
        { kind: 'code', text: 'abc1234' },
        { kind: 'badge', label: 'matches the last factory-recorded PR head', sha: 'abc1234', warning: false },
        { kind: 'link', url: 'https://preview.test', logs: 'https://ci.test/log' },
        { kind: 'code', text: 'ready' },
      ])
  })

  it('says what the provider did not report instead of inventing it', () => {
    expect(deploymentSegments(deployment({ sha: 'abc1234' }), 'fedcba9')).toEqual([
      { kind: 'code', text: 'production' },
      { kind: 'text', text: '(no status reported by the provider)' },
      { kind: 'code', text: 'abc1234' },
      // A row for another revision is a warning badge, not a match.
      { kind: 'badge', label: 'differs from the last factory-recorded PR head', sha: 'fedcba9', warning: true },
    ])
    expect(deploymentSegments(deployment({ sha: '' }), 'abc1234')).toEqual([
      { kind: 'code', text: 'production' },
      { kind: 'text', text: '(no status reported by the provider)' },
      { kind: 'text', text: 'revision not reported by the provider' },
    ])
  })

  it('describes a configured environment with or without a link', () => {
    expect(environmentSegments(environment({ htmlUrl: 'https://staging.test' }))).toEqual([
      { kind: 'code', text: 'staging' },
      { kind: 'link', url: 'https://staging.test', logs: '' },
    ])
    expect(environmentSegments(environment())).toEqual([{ kind: 'code', text: 'staging' }])
  })
})

describe('currentStatusLinks', () => {
  const round = (over: Partial<FactoryRound> = {}): FactoryRound =>
    ({ number: 1, approvalRevision: 1, implementEffectId: 'e', baseSha: 'base', headSha: 'abc1234', treeSha: 'tree', ci: null, implementation: null, review: null, startedAt: '', finishedAt: '', ...over }) as FactoryRound

  it('keeps this revision\'s commit-status links in named order', () => {
    const links = currentStatusLinks(round({ ci: { headSha: 'abc1234', complete: true, observedAt: '', checks: [
      check({ id: 'b', key: { kind: 'commit_status', name: 'zeta', appId: 0 }, url: 'https://z.test' }),
      check({ id: 'a', key: { kind: 'commit_status', name: 'alpha', appId: 0 }, url: 'https://a.test' }),
      // A check run is not a commit status, and a linkless status is nothing to open.
      check({ id: 'c', key: { kind: 'check_run', name: 'ci/build', appId: 0 }, url: 'https://ci.test' }),
      check({ id: 'd', key: { kind: 'commit_status', name: 'beta', appId: 0 }, url: '' }),
    ] } }))
    expect(links.map((link) => link.id)).toEqual(['a', 'b'])
  })

  it('offers nothing when the evidence is for another revision or absent', () => {
    expect(currentStatusLinks(round({ ci: { headSha: 'older0', complete: true, observedAt: '', checks: [check({ key: { kind: 'commit_status', name: 'x', appId: 0 }, url: 'https://x.test' })] } }))).toEqual([])
    expect(currentStatusLinks(round())).toEqual([])
    expect(currentStatusLinks(undefined)).toEqual([])
  })
})

describe('ciEvidence', () => {
  const round = (over: Record<string, unknown> = {}) => over as unknown as FactoryRound

  it('treats missing, incomplete and stale-head evidence as warnings', () => {
    expect(ciEvidence(undefined)).toMatchObject({ state: 'missing', warning: true })
    expect(ciEvidence(round({ headSha: 'abc1234', ci: { headSha: 'abc1234', complete: false, observedAt: '', checks: [] } })))
      .toEqual({ state: 'incomplete', warning: true, text: 'CI evidence unavailable or incomplete. Readiness is not established.' })
    expect(ciEvidence(round({ headSha: 'abc1234', ci: { headSha: 'older0', complete: true, observedAt: '', checks: [] } })))
      .toMatchObject({ state: 'stale', warning: true })
  })

  it('distinguishes a complete observation with no checks from a warning', () => {
    expect(ciEvidence(round({ headSha: '', ci: { headSha: '', complete: true, observedAt: '', checks: [] } })))
      .toEqual({ state: 'complete', warning: false, text: 'Observation complete; no checks reported.' })
    expect(ciEvidence(round({ headSha: 'abc1234', ci: { headSha: 'abc1234', complete: true, observedAt: '', checks: [check()] } })))
      .toMatchObject({ state: 'complete', warning: false })
  })
})

describe('reviewVerdict', () => {
  const review = (over: Record<string, unknown> = {}) =>
    ({ outcome: 'complete', inputsAccessible: true, findings: [], summary: '', question: '', reason: '', ...over })
  const round = (over: Record<string, unknown> = {}) => over as unknown as FactoryRound

  it('is clean only for a complete review with accessible inputs and no findings', () => {
    expect(reviewVerdict(round({ review: review() }))).toEqual({ clean: true, warning: false, text: 'Review complete with no findings.' })
  })

  it('never reports a failed, missing or finding-bearing review as clean', () => {
    expect(reviewVerdict(round({ review: review({ outcome: 'needs_input' }) }))).toMatchObject({ clean: false, warning: true })
    expect(reviewVerdict(round({ review: review({ inputsAccessible: false }) }))).toMatchObject({ clean: false, warning: true })
    expect(reviewVerdict(round({ implementation: { summary: 'done' } }))).toMatchObject({ clean: false, warning: true })
    expect(reviewVerdict(round({ review: review({ findings: [{ severity: 'high', title: 'Unsafe call' }] }) })))
      .toEqual({ clean: false, warning: true, text: '1 review finding needs repair before this review is clean.' })
    expect(reviewVerdict(undefined).clean).toBe(false)
  })
})
