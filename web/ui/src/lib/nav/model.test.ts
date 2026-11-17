import { describe, expect, it } from 'vitest'
import type { ProjectStatus } from '../api'
import type { FactoryConfig, FactoryItem } from '../factory'
import { buildNavModel, type NavDestination, type NavModel } from './model'

const config: FactoryConfig = {
  repoId: 'acme/widgets',
  projectRoot: '/home/u/alpha',
  hostId: 'local',
  targetBranch: 'main',
  workspace: 'default',
  native: false,
  implementerRouting: {},
  reviewerRouting: {},
  reviewerInstructions: '',
  implementerAuto: false,
  expectedChecks: [],
  noRequiredCiAcknowledged: false,
  maxRounds: 3,
  ciPollSeconds: 30,
  ciWaitSeconds: 600,
}

/** Whole factory items: a factory item is always whole on the wire. */
function item(x: Partial<FactoryItem> & { id: string }): FactoryItem {
  const title = `Work on ${x.id}`
  return {
    title,
    state: 'implementing',
    stage: 'implement',
    repoId: 'acme/widgets',
    projectRoot: '/home/u/alpha',
    hostId: 'local',
    branch: `factory/${x.id}`,
    baseSha: 'base0',
    version: 1,
    draft: { requestId: `req-${x.id}`, title, plan: '', spec: '', config },
    approvals: null,
    grants: null,
    rounds: null,
    workers: null,
    prs: null,
    effects: null,
    decision: null,
    history: null,
    createdAt: '2026-11-01T00:00:00Z',
    approvedAt: '',
    updatedAt: '2026-11-01T00:00:00Z',
    ...x,
  }
}

function project(root: string, over: Partial<ProjectStatus> = {}): ProjectStatus {
  return { root, available: true, trust: 'trusted', isolation: 'available', ...over }
}

function build(over: Partial<Parameters<typeof buildNavModel>[0]> = {}): NavModel {
  return buildNavModel({ route: '#', projects: [], factoryItems: [], needsCount: 0, ...over })
}

/** The destination with this label, from any section of the model. */
function destination(model: NavModel, label: string): NavDestination {
  const found = model.all.find((d) => d.label === label)
  if (!found) throw new Error(`no ${label} destination in ${model.all.map((d) => d.label).join(', ')}`)
  return found
}

describe('buildNavModel destinations', () => {
  it('marksPrimaryAndSubroutesActive', () => {
    expect(build({ route: '' }).primary[0].active).toBe(true)
    expect(build({ route: '#' }).primary[0].active).toBe(true)

    const inbox = build({ route: '#inbox' })
    expect(destination(inbox, 'Inbox').active).toBe(true)
    // `#inbox` is not the fleet home: the empty hash's sub-route rule must not leak.
    expect(destination(inbox, 'Fleet').active).toBe(false)

    // A resource stays lit on every route it owns.
    expect(destination(build({ route: '#runs/agent-1' }), 'Runs').active).toBe(true)
    expect(destination(build({ route: '#live?page=2' }), 'Live').active).toBe(true)
    expect(destination(build({ route: '#library/mcp' }), 'Library').active).toBe(true)
    expect(destination(build({ route: '#usage?tab=audit' }), 'Usage').active).toBe(true)
    expect(destination(build({ route: '#settings/providers' }), 'Settings').active).toBe(true)

    const projects = build({ route: '#projects/%2Fhome%2Fu%2Falpha', projects: [project('/home/u/alpha')] })
    expect(destination(projects, 'Projects').active).toBe(true)
    expect(projects.projects[0].active).toBe(true)

    // A sibling that merely shares a prefix is a different project.
    const sibling = build({ route: '#projects/%2Fhome%2Fu%2Falpha-two', projects: [project('/home/u/alpha')] })
    expect(sibling.projects[0].active).toBe(false)
  })

  it('groupsProjectsAndCurrentWorkItems', () => {
    const model = build({
      route: '#factory/live-1',
      projects: [project('/home/u/alpha'), project('/home/u/beta')],
      factoryItems: [
        item({ id: 'live-1', title: 'Add the cache', projectRoot: '/home/u/alpha', state: 'implementing' }),
        item({ id: 'draft-1', title: 'Approve the plan', projectRoot: '/home/u/alpha', state: 'draft' }),
        item({ id: 'gone-1', title: 'Shipped it', projectRoot: '/home/u/alpha', state: 'closed' }),
        item({ id: 'beta-1', title: 'Beta work', projectRoot: '/home/u/beta' }),
      ],
    })

    const alpha = model.projects.find((p) => p.root === '/home/u/alpha')
    expect(alpha?.label).toBe('alpha')
    expect(alpha?.hash).toBe('#projects/%2Fhome%2Fu%2Falpha/factory')

    // Current work only: the closed item leaves for history.
    expect(alpha?.workItems.map((w) => w.label)).toEqual(['Approve the plan', 'Add the cache'])
    expect(alpha?.workItems.map((w) => w.hash)).toEqual([
      '#factory/draft-1?view=approval',
      '#factory/live-1?view=activity',
    ])
    expect(alpha?.workItems.find((w) => w.label === 'Add the cache')?.active).toBe(true)
    expect(alpha?.workItems.find((w) => w.label === 'Approve the plan')?.active).toBe(false)

    // A project with no factory work is still a destination, with no items.
    expect(model.projects.find((p) => p.root === '/home/u/beta')?.workItems.map((w) => w.label)).toEqual(['Beta work'])

    // Contextual work items stay in their project and in Fleet, not in the all-pages list.
    expect(model.all.some((d) => d.hash.startsWith('#factory/'))).toBe(false)
  })

  it('showsNeedsCount', () => {
    const waiting = build({ needsCount: 3 })
    expect(destination(waiting, 'Inbox').count).toBe(3)
    // Nothing else carries the queue's size.
    expect(waiting.all.filter((d) => d.count !== undefined).map((d) => d.label)).toEqual(['Inbox'])

    const clear = build({ needsCount: 0 })
    expect(destination(clear, 'Inbox').count).toBeUndefined()
  })

  it('marksRoadmapDestinationsPlanned', () => {
    const model = build()
    expect(model.roadmap.map((d) => d.label)).toEqual(['Hosts', 'Environments', 'Connections'])
    expect(model.roadmap.map((d) => d.hash)).toEqual(['#roadmap/hosts', '#roadmap/environments', '#roadmap/connections'])
    expect(model.roadmap.every((d) => d.planned === true)).toBe(true)
    // Nothing operational is planned, and no resource carries a roadmap hash.
    expect(model.resources.some((d) => d.planned)).toBe(false)
    expect(model.all.filter((d) => d.planned).map((d) => d.label)).toEqual(['Hosts', 'Environments', 'Connections'])

    // A planned destination lights only on its own roadmap page.
    const onHosts = build({ route: '#roadmap/hosts' })
    expect(onHosts.roadmap.find((d) => d.label === 'Hosts')?.active).toBe(true)
    expect(onHosts.roadmap.find((d) => d.label === 'Environments')?.active).toBe(false)
  })

  it('deduplicatesAllPages', () => {
    const model = build({ projects: [project('/home/u/alpha'), project('/home/u/beta')] })
    const hashes = model.all.map((d) => d.hash)
    expect(new Set(hashes).size).toBe(hashes.length)

    // Every global and project destination appears exactly once, in render order.
    const expected = [
      ...model.primary.map((d) => d.hash),
      ...model.projects.map((d) => d.hash),
      ...model.resources.map((d) => d.hash),
      ...model.roadmap.map((d) => d.hash),
    ]
    expect(hashes).toEqual(expected)
    expect(model.all).toHaveLength(2 + 2 + 8 + 3)
  })

  it('sortsProjectsByShortNameThenRoot', () => {
    const model = build({
      projects: [project('/work/zulu/alpha'), project('/work/bravo/beta'), project('/work/alpha/alpha')],
    })
    expect(model.projects.map((p) => `${p.label} ${p.root}`)).toEqual([
      'alpha /work/alpha/alpha',
      'alpha /work/zulu/alpha',
      'beta /work/bravo/beta',
    ])
  })
})
