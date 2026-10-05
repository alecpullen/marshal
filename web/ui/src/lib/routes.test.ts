import { describe, expect, it } from 'vitest'
import { sessionsProjectFromHash, isScopedSessions, pageFromHash, parseChatRoute, formatChatRoute, parseRunRoute, formatRunRoute, parseLiveRoute, formatLiveRoute, parseLibraryRoute, formatLibraryRoute, parseSettingsRoute, parseUsageRoute, parseNetworkRoute, formatNetworkRoute, parseProjectRoute, formatProjectRoute, formatAutomationRoute, redirectLegacy, parseWorkspacesRoute, formatWorkspacesRoute, type ChatRoute } from './routes'

describe('sessionsProjectFromHash', () => {
  it('returns null for the unscoped route and non-sessions hashes', () => {
    expect(sessionsProjectFromHash('#sessions')).toBeNull()
    expect(sessionsProjectFromHash('#chat/abc')).toBeNull()
    expect(sessionsProjectFromHash('#')).toBeNull()
  })

  it('decodes an encoded project root', () => {
    expect(sessionsProjectFromHash('#sessions/%2Fhome%2Fu%2Falpha')).toBe('/home/u/alpha')
  })

  it('keeps a raw segment with no escapes intact', () => {
    expect(sessionsProjectFromHash('#sessions//home/u/alpha')).toBe('/home/u/alpha')
  })

  it('degrades a malformed escape to the raw string instead of throwing', () => {
    expect(sessionsProjectFromHash('#sessions/%E0%A4%A')).toBe('%E0%A4%A')
  })

  it('treats an empty scope as unscoped', () => {
    // `#sessions/` does not match the `(.+)` group, so it falls to the
    // picker — an empty scope must never become an empty-string project.
    expect(sessionsProjectFromHash('#sessions/')).toBeNull()
  })

  it('round-trips the characters hash routing cares about', () => {
    expect(sessionsProjectFromHash('#sessions/%23tag%20dir%3Fq')).toBe('#tag dir?q')
  })

  it('decodes exactly once, so a double-escaped root stays escaped', () => {
    expect(sessionsProjectFromHash('#sessions/%252F')).toBe('%2F')
  })

  it('leaves a literal plus intact — decodeURIComponent is not query-string decoding', () => {
    expect(sessionsProjectFromHash('#sessions/a+b')).toBe('a+b')
  })
})

describe('isScopedSessions', () => {
  it('is true only for a non-empty scope', () => {
    expect(isScopedSessions('#sessions/%2Fproj%2Fa')).toBe(true)
    expect(isScopedSessions('#sessions')).toBe(false)
    expect(isScopedSessions('#sessions/')).toBe(false)
    expect(isScopedSessions('#chat/x')).toBe(false)
    expect(isScopedSessions('#')).toBe(false)
  })
})

describe('pageFromHash', () => {
  it('resolves the empty hash to Home and #fleet to the old Dashboard', () => {
    expect(pageFromHash('')).toBe('home')
    expect(pageFromHash('#')).toBe('home')
    expect(pageFromHash('#fleet')).toBe('fleet')
  })
  it('still resolves every existing route', () => {
    expect(pageFromHash('#new')).toBe('new')
    expect(pageFromHash('#chat/abc')).toBe('chat')
    expect(pageFromHash('#sessions')).toBe('sessions')
    expect(pageFromHash('#sessions/%2Fp')).toBe('sessions')
    for (const h of ['#pending', '#clients', '#projects', '#disk', '#activity']) expect(pageFromHash(h)).toBe('other')
  })
})

describe('chat route', () => {
  it('still parses the plain #chat/<id>', () => {
    expect(parseChatRoute('#chat/abc')).toEqual({ id: 'abc', view: 'session' })
    expect(pageFromHash('#chat/abc?dock=docked')).toBe('chat')
    expect(pageFromHash('#chat/abc/review')).toBe('chat')
  })

  it('round-trips every field', () => {
    const cases: ChatRoute[] = [
      { id: 'a1', view: 'session' },
      { id: 'a1', view: 'review' },
      { id: 'a1', view: 'session', node: 'step:7', dock: 'expanded', tab: 'files' },
      { id: 'a1', view: 'review', node: 'sub:3/step:1', dock: 'collapsed', tab: 'changes' },
    ]
    for (const r of cases) expect(parseChatRoute(formatChatRoute(r))).toEqual(r)
  })

  it('drops bad dock and tab values and rejects non-chat hashes', () => {
    expect(parseChatRoute('#chat/a?dock=huge&tab=nope&node=')).toEqual({ id: 'a', view: 'session' })
    expect(parseChatRoute('#fleet')).toBeNull()
    expect(parseChatRoute('#chat/')).toBeNull()
  })
})

describe('run and live routes', () => {
  it('parses #runs/<id> with defaults and drops bad values', () => {
    expect(parseRunRoute('#runs/a1')).toEqual({ id: 'a1', view: 'lanes' })
    expect(parseRunRoute('#runs/a1?view=graph&node=2%3Averify&dock=expanded')).toEqual({ id: 'a1', view: 'graph', node: '2:verify', dock: 'expanded' })
    expect(parseRunRoute('#runs/a1?view=bogus&dock=huge')).toEqual({ id: 'a1', view: 'lanes' })
    expect(parseRunRoute('#runs')).toBeNull()
  })

  it('round-trips a run route', () => {
    const r = { id: 'a 1', view: 'timeline' as const, node: '3:review', dock: 'collapsed' as const }
    expect(parseRunRoute(formatRunRoute(r))).toEqual(r)
    expect(formatRunRoute({ id: 'a1', view: 'lanes' })).toBe('#runs/a1')
  })

  it('parses and formats the live wall route', () => {
    expect(parseLiveRoute('#live')).toEqual({ project: undefined, runsOnly: false, page: 1 })
    expect(parseLiveRoute('#live?project=%2Fp&runs=1&page=3')).toEqual({ project: '/p', runsOnly: true, page: 3 })
    expect(parseLiveRoute('#live?page=-2')?.page).toBe(1)
    expect(formatLiveRoute({ project: '/p', runsOnly: true, page: 3 })).toBe('#live?project=%2Fp&runs=1&page=3')
    expect(formatLiveRoute({ runsOnly: false, page: 1 })).toBe('#live')
  })

  it('maps them to pages', () => {
    expect(pageFromHash('#runs')).toBe('runs')
    expect(pageFromHash('#runs/a1?view=graph')).toBe('run')
    expect(pageFromHash('#live?runs=1')).toBe('live')
  })
})

describe('control page routes', () => {
  it('parses library tabs and the project scope', () => {
    expect(parseLibraryRoute('#library')).toEqual({ tab: 'skills', project: undefined })
    expect(parseLibraryRoute('#library/memory?project=%2Fw%2Fa')).toEqual({ tab: 'memory', project: '/w/a' })
    expect(parseLibraryRoute('#library/nope')).toBeNull()
    expect(formatLibraryRoute({ tab: 'memory', project: '/w/a' })).toBe('#library/memory?project=%2Fw%2Fa')
    expect(formatLibraryRoute({ tab: 'mcp' })).toBe('#library/mcp')
  })

  it('parses settings and usage tabs', () => {
    expect(parseSettingsRoute('#settings')).toEqual({ tab: 'models' })
    expect(parseSettingsRoute('#settings/providers')).toEqual({ tab: 'providers' })
    expect(parseSettingsRoute('#settings/x')).toBeNull()
    expect(parseUsageRoute('#usage')).toEqual({ tab: 'cost' })
    expect(parseUsageRoute('#usage?tab=audit')).toEqual({ tab: 'audit' })
    expect(parseUsageRoute('#usage?tab=bogus')).toEqual({ tab: 'cost' })
  })

  it('maps the new pages', () => {
    expect(pageFromHash('#library/skills')).toBe('library')
    expect(pageFromHash('#settings/models')).toBe('settings')
    expect(pageFromHash('#usage?tab=disk')).toBe('usage')
    expect(pageFromHash('#watches')).toBe('watches')
  })

  it('parses the network inspector, project and settings routes', () => {
    expect(parseNetworkRoute('#network?agent=a1')).toEqual({ agent: 'a1', workspace: undefined })
    expect(parseNetworkRoute('#network')).toEqual({ agent: undefined, workspace: undefined })
    expect(parseNetworkRoute('#workspaces/go%20dev/network')).toEqual({ workspace: 'go dev' })
    expect(parseNetworkRoute('#workspaces/go/edit')).toBeNull()
    expect(formatNetworkRoute({ agent: 'a 1' })).toBe('#network?agent=a%201')
    expect(formatNetworkRoute({ workspace: 'go dev' })).toBe('#workspaces/go%20dev/network')
    expect(parseProjectRoute('#projects/%2Fhome%2Fu%2Falpha')).toEqual({ root: '/home/u/alpha' })
    expect(parseProjectRoute('#projects')).toBeNull()
    expect(parseProjectRoute('#projects/%E0%A4%A')).toEqual({ root: '%E0%A4%A' })
    expect(formatProjectRoute('/home/u/alpha')).toBe('#projects/%2Fhome%2Fu%2Falpha')
    expect(parseProjectRoute('#projects/%2Fp/automations/review-bot')).toEqual({ root: '/p', automations: 'review-bot' })
    expect(parseProjectRoute('#projects/%2Fp/automations/ci-fixer?draft=d1')).toEqual({ root: '/p', automations: 'ci-fixer', draft: 'd1' })
    expect(parseProjectRoute('#projects/%2Fp/automations/other')).toBeNull()
    expect(parseProjectRoute(formatAutomationRoute('/p q', 'review-bot', 'd 1'))).toEqual({ root: '/p q', automations: 'review-bot', draft: 'd 1' })
    expect(pageFromHash('#projects/%2Fp/automations/ci-fixer')).toBe('project')
    expect(pageFromHash('#network?agent=a1')).toBe('network')
    expect(pageFromHash('#projects/%2Fp')).toBe('project')
    for (const t of ['secrets', 'credentials', 'repos']) expect(parseSettingsRoute(`#settings/${t}`)).toEqual({ tab: t })
  })

  it('redirects the old routes', () => {
    expect(redirectLegacy('#clients')).toBe('#library/mcp')
    expect(redirectLegacy('#disk')).toBe('#usage?tab=disk')
    expect(redirectLegacy('#activity')).toBe('#usage?tab=audit')
    expect(redirectLegacy('#runs')).toBeNull()
  })
})

describe('workspaces routes', () => {
  it('parses the gallery and the three named views', () => {
    expect(parseWorkspacesRoute('#workspaces')).toEqual({ view: 'gallery' })
    expect(parseWorkspacesRoute('#workspaces/go-service/edit')).toEqual({ view: 'edit', name: 'go-service' })
    expect(parseWorkspacesRoute('#workspaces/go-service/builds')).toEqual({ view: 'builds', name: 'go-service' })
    expect(parseWorkspacesRoute('#workspaces/go-service/network')).toEqual({ view: 'network', name: 'go-service' })
  })
  it('rejects unknown views and partial paths', () => {
    expect(parseWorkspacesRoute('#workspaces/go-service')).toBeNull()
    expect(parseWorkspacesRoute('#workspaces/go-service/nope')).toBeNull()
    expect(parseWorkspacesRoute('#workspacesx')).toBeNull()
  })
  it('round-trips and routes to the workspaces page', () => {
    expect(formatWorkspacesRoute({ view: 'builds', name: 'a b' })).toBe('#workspaces/a%20b/builds')
    expect(parseWorkspacesRoute(formatWorkspacesRoute({ view: 'edit', name: 'a b' }))?.name).toBe('a b')
    expect(pageFromHash('#workspaces/x/edit')).toBe('workspaces')
    expect(pageFromHash('#workspaces')).toBe('workspaces')
  })
})
