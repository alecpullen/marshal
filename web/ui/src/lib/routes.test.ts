import { describe, expect, it } from 'vitest'
import { sessionsProjectFromHash, isScopedSessions, pageFromHash, parseChatRoute, formatChatRoute, parseRunRoute, formatRunRoute, parseLiveRoute, formatLiveRoute, parseLibraryRoute, formatLibraryRoute, parseSettingsRoute, parseUsageRoute, parseNetworkRoute, formatNetworkRoute, parseProjectRoute, formatProjectRoute, formatAutomationRoute, redirectLegacy, parseWorkspacesRoute, formatWorkspacesRoute, parseFactoryRoute, formatFactoryRoute, parseNewAgentRoute, parseRoadmapRoute, ROADMAP_AREAS, type ChatRoute } from './routes'
import type { FactoryRoute } from './factory'

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
  it('resolves the empty hash, # and #fleet to the fleet screen, and #inbox to its attention list', () => {
    expect(pageFromHash('')).toBe('fleet')
    expect(pageFromHash('#')).toBe('fleet')
    expect(pageFromHash('#fleet')).toBe('fleet')
    expect(pageFromHash('#inbox')).toBe('inbox')
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
    expect(parseChatRoute('#chat/a?tab=tasks')).toEqual({ id: 'a', view: 'session', tab: 'tasks' })
    expect(parseChatRoute('#fleet')).toBeNull()
    expect(parseChatRoute('#chat/')).toBeNull()
  })

  it('chat route round-trips a validated factory return', () => {
    const r: ChatRoute = { id: 'a1', view: 'session', node: 'step:2', tab: 'inspect', from: '#factory/x%2Fy' }
    expect(parseChatRoute(formatChatRoute(r))).toEqual(r)
    expect(parseChatRoute('#chat/a?from=%23factory%2Fx%252Fy')).toEqual({ id: 'a', view: 'session', from: '#factory/x%2Fy' })
    // The return route may coexist with the review view and the dock.
    expect(parseChatRoute('#chat/a/review?dock=docked&from=%23factory%2Fb')).toEqual({ id: 'a', view: 'review', dock: 'docked', from: '#factory/b' })
    expect(formatChatRoute({ id: 'a', view: 'review', from: '#factory/b' })).toBe('#chat/a/review?from=%23factory%2Fb')
  })

  it('chat route drops an invalid return', () => {
    expect(parseChatRoute('#chat/a?from=%23fleet')).toEqual({ id: 'a', view: 'session' })
    expect(parseChatRoute('#chat/a?from=%23factory')).toEqual({ id: 'a', view: 'session' })
    expect(parseChatRoute('#chat/a?from=%23factory%2F')).toEqual({ id: 'a', view: 'session' })
    expect(parseChatRoute('#chat/a?from=%23factory%2Fa%2Fb')).toEqual({ id: 'a', view: 'session' })
    expect(parseChatRoute('#chat/a?from=')).toEqual({ id: 'a', view: 'session' })
  })

  it('chat route encodes and decodes agent IDs', () => {
    expect(parseChatRoute('#chat/a%2Fb')).toEqual({ id: 'a/b', view: 'session' })
    expect(parseChatRoute('#chat/a%20b/review')).toEqual({ id: 'a b', view: 'review' })
    expect(parseChatRoute('#chat/a%2Fb%20c?tab=files')).toEqual({ id: 'a/b c', view: 'session', tab: 'files' })
    expect(formatChatRoute({ id: 'a/b c', view: 'session' })).toBe('#chat/a%2Fb%20c')
    expect(formatChatRoute({ id: 'a b', view: 'review' })).toBe('#chat/a%20b/review')
    expect(parseChatRoute('#chat/%E0%A4%A')).toEqual({ id: '%E0%A4%A', view: 'session' })
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
    expect(redirectLegacy('#fleet')).toBe('#')
    expect(redirectLegacy('#clients')).toBe('#library/mcp')
    expect(redirectLegacy('#disk')).toBe('#usage?tab=disk')
    expect(redirectLegacy('#activity')).toBe('#usage?tab=audit')
    expect(redirectLegacy('#runs')).toBeNull()
    expect(redirectLegacy('#inbox')).toBeNull()
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

import { parseWatchesRoute as _pw, parseSettingsRoute as _ps, parseLibraryRoute as _pl, parseChatRoute as _pc } from './routes'
describe('W5.5 routes', () => {
  it('watches has a schedules tab', () => {
    expect(_pw('#watches')).toEqual({ tab: 'watches' })
    expect(_pw('#watches/schedules')).toEqual({ tab: 'schedules' })
    expect(_pw('#watches/x')).toBeNull()
  })
  it('settings has notifications and status-links', () => {
    expect(_ps('#settings/notifications')).toEqual({ tab: 'notifications' })
    expect(_ps('#settings/status-links')).toEqual({ tab: 'status-links' })
  })
  it('library has recipes', () => {
    expect(_pl('#library/recipes')).toEqual({ tab: 'recipes', project: undefined })
  })
  it('chat accepts the terminal and preview dock tabs', () => {
    expect(_pc('#chat/a1?tab=terminal')?.tab).toBe('terminal')
    expect(_pc('#chat/a1?tab=preview')?.tab).toBe('preview')
  })
})

describe('factory routes', () => {
  it('parses list, creation and encoded item IDs', async () => {
    const { parseFactoryRoute, formatFactoryRoute, pageFromHash } = await import('./routes')
    expect(parseFactoryRoute('#factory')).toEqual({})
    expect(parseFactoryRoute('#factory/new')).toEqual({ create: true })
    expect(parseFactoryRoute('#factory/a%2Fb')).toEqual({ itemId: 'a/b' })
    expect(formatFactoryRoute({ itemId: 'a/b' })).toBe('#factory/a%2Fb')
    expect(parseFactoryRoute('#factory/a/b')).toBeNull()
    expect(pageFromHash('#factory/new')).toBe('factory')
  })

  it('factory routes parse scoped filters and item drill-ins', () => {
    expect(parseFactoryRoute('#projects/%2Fhome%2Fu%2Falpha/factory')).toEqual({ projectRoot: '/home/u/alpha' })
    expect(parseFactoryRoute('#projects/%2Fp/factory?filter=needs-you')).toEqual({ projectRoot: '/p', filter: 'needs-you' })
    expect(parseFactoryRoute('#projects/%E0%A4%A/factory')).toEqual({ projectRoot: '%E0%A4%A' })
    expect(parseFactoryRoute('#factory?filter=ready')).toEqual({ filter: 'ready' })
    expect(parseFactoryRoute('#factory/new?project=%2Fp')).toEqual({ create: true, projectRoot: '/p' })
    expect(parseFactoryRoute('#factory/a%2Fb?view=approval')).toEqual({ itemId: 'a/b', view: 'approval' })
    expect(parseFactoryRoute('#factory/a?view=review&agent=w%2F1')).toEqual({ itemId: 'a', view: 'review', agent: 'w/1' })
    expect(parseFactoryRoute('#factory/a?agent=w1')).toEqual({ itemId: 'a', view: 'activity', agent: 'w1' })
    expect(parseFactoryRoute('#factory/%E0%A4%A')).toEqual({ itemId: '%E0%A4%A' })
    // The scoped list is one factory route, not a project page.
    expect(parseProjectRoute('#projects/%2Fp/factory')).toBeNull()
    expect(pageFromHash('#projects/%2Fp/factory')).toBe('factory')
    expect(pageFromHash('#factory?filter=history')).toBe('factory')
  })

  it('factory routes canonicalize defaults and reject malformed paths', () => {
    expect(parseFactoryRoute('#factory?filter=current')).toEqual({})
    expect(parseFactoryRoute('#factory?filter=bogus')).toEqual({})
    expect(parseFactoryRoute('#factory/a?view=bogus')).toEqual({ itemId: 'a' })
    expect(formatFactoryRoute({})).toBe('#factory')
    expect(formatFactoryRoute({ filter: 'current' })).toBe('#factory')
    expect(formatFactoryRoute({ filter: 'history' })).toBe('#factory?filter=history')
    expect(formatFactoryRoute({ projectRoot: '/p q' })).toBe('#projects/%2Fp%20q/factory')
    expect(formatFactoryRoute({ projectRoot: '/p', filter: 'needs-you' })).toBe('#projects/%2Fp/factory?filter=needs-you')
    expect(formatFactoryRoute({ create: true })).toBe('#factory/new')
    expect(formatFactoryRoute({ create: true, projectRoot: '/p q' })).toBe('#factory/new?project=%2Fp%20q')
    expect(formatFactoryRoute({ itemId: 'a/b', view: 'approval' })).toBe('#factory/a%2Fb?view=approval')
    expect(formatFactoryRoute({ itemId: 'a/b', view: 'prs', agent: 'w 1' })).toBe('#factory/a%2Fb?view=prs&agent=w+1')
    // An agent without an explicit view is formatted as its activity tab.
    expect(formatFactoryRoute({ itemId: 'a', agent: 'w1' })).toBe('#factory/a?view=activity&agent=w1')
    const cases: FactoryRoute[] = [
      {},
      { filter: 'ready' },
      { projectRoot: '/p q' },
      { projectRoot: '/p', filter: 'history' },
      { create: true },
      { create: true, projectRoot: '/p' },
      { itemId: 'a/b' },
      { itemId: 'a/b', view: 'documents' },
      { itemId: 'a', view: 'activity', agent: 'w/1' },
    ]
    for (const route of cases) expect(parseFactoryRoute(formatFactoryRoute(route))).toEqual(route)
    expect(parseFactoryRoute('#factory/')).toBeNull()
    expect(parseFactoryRoute('#factories')).toBeNull()
    expect(parseFactoryRoute('#factory/new/x')).toBeNull()
    expect(parseFactoryRoute('#projects/%2Fp/factory/x')).toBeNull()
    expect(parseFactoryRoute('#projects/%2Fp/factory/a')).toBeNull()
    expect(parseFactoryRoute('#projects/%2Fp')).toBeNull()
  })

  it('factory routes decode the agent param exactly once, like the chat return', () => {
    // URLSearchParams.get has already decoded once, so an id whose own text
    // holds a percent escape survives the round trip unchanged.
    expect(formatFactoryRoute({ itemId: 'a', view: 'activity', agent: 'w%2F1' })).toBe('#factory/a?view=activity&agent=w%252F1')
    expect(parseFactoryRoute(formatFactoryRoute({ itemId: 'a', view: 'activity', agent: 'w%2F1' }))).toEqual({ itemId: 'a', view: 'activity', agent: 'w%2F1' })
    // A plain id still round-trips.
    expect(formatFactoryRoute({ itemId: 'a', view: 'review', agent: 'w1' })).toBe('#factory/a?view=review&agent=w1')
    expect(parseFactoryRoute(formatFactoryRoute({ itemId: 'a', view: 'review', agent: 'w1' }))).toEqual({ itemId: 'a', view: 'review', agent: 'w1' })
  })
})

describe('new agent route', () => {
  it('new agent route reads the project query', () => {
    expect(parseNewAgentRoute('#new')).toEqual({})
    expect(parseNewAgentRoute('#new?project=%2Fhome%2Fu%2Falpha')).toEqual({ projectRoot: '/home/u/alpha' })
    expect(parseNewAgentRoute('#new?project=')).toEqual({})
    expect(parseNewAgentRoute('#newer')).toBeNull()
    expect(parseNewAgentRoute('#new/')).toBeNull()
    expect(pageFromHash('#new?project=%2Fp')).toBe('new')
  })
})

describe('roadmap route', () => {
  it('roadmap route accepts only named areas', () => {
    for (const area of ROADMAP_AREAS) expect(parseRoadmapRoute(`#roadmap/${area}`)).toEqual({ area })
    expect(parseRoadmapRoute('#roadmap')).toBeNull()
    expect(parseRoadmapRoute('#roadmap/billing')).toBeNull()
    expect(parseRoadmapRoute('#roadmap/planning/x')).toBeNull()
    expect(pageFromHash('#roadmap/planning')).toBe('roadmap')
    expect(pageFromHash('#roadmap')).toBe('other')
  })
})
