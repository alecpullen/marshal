import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import type { ProjectStatus } from '../api'
import type { FactoryConfig, FactoryItem } from '../factory'
import { buildNavModel, type NavModel, type NavModelInput } from './model'
import StudioNav from './StudioNav.svelte'

afterEach(cleanup)

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

const projects: ProjectStatus[] = [{ root: '/home/u/alpha', available: true, trust: 'trusted', isolation: 'available' }]

function model(over: Partial<NavModelInput> = {}): NavModel {
  return buildNavModel({ route: '#', projects, factoryItems: [], needsCount: 0, ...over })
}

describe('StudioNav', () => {
  it('desktopCollapseAndNavigate', async () => {
    const onNavigate = vi.fn()
    const onToggle = vi.fn()
    const items = [item({ id: 'live-1', title: 'Add the cache' })]

    // Labelled form: sections, destinations and the active project's work show.
    render(StudioNav, {
      model: model({ route: '#factory/live-1', factoryItems: items, needsCount: 2 }),
      open: true,
      onToggle,
      onNavigate,
    })

    for (const label of ['Fleet', 'Inbox', 'Project factories', 'Resources', 'Roadmap', 'Hosts']) {
      expect(screen.getByText(label)).toBeTruthy()
    }
    // The Inbox carries the needs count, and the open work item is current.
    expect(screen.getByRole('button', { name: /^Inbox/ }).textContent).toContain('2')
    const workItem = screen.getByRole('button', { name: /^Add the cache/ })
    expect(workItem.getAttribute('aria-current')).toBe('page')
    expect(screen.getByRole('button', { name: /^Fleet/ }).getAttribute('aria-current')).toBeNull()

    await userEvent.click(workItem)
    expect(onNavigate).toHaveBeenCalledWith('#factory/live-1?view=activity')
    await userEvent.click(screen.getByRole('button', { name: /^Fleet/ }))
    expect(onNavigate).toHaveBeenCalledWith('#')
    await userEvent.click(screen.getByRole('button', { name: 'Collapse navigation' }))
    expect(onToggle).toHaveBeenCalledOnce()

    cleanup()

    // Collapsed form: an icon rail with the same destinations and no label text.
    const collapsedNavigate = vi.fn()
    render(StudioNav, { model: model({ route: '#inbox', needsCount: 2 }), open: false, onToggle, onNavigate: collapsedNavigate })
    expect(screen.queryByText('Inbox')).toBeNull()
    expect(screen.queryByText('Project factories')).toBeNull()

    const inbox = screen.getByRole('button', { name: 'Inbox' })
    expect(inbox.getAttribute('aria-current')).toBe('page')
    // A planned destination keeps saying so once its label is gone.
    expect(screen.getByRole('button', { name: 'Hosts (planned)' })).toBeTruthy()

    await userEvent.click(screen.getByRole('button', { name: 'Fleet' }))
    expect(collapsedNavigate).toHaveBeenCalledWith('#')
  })

  it('plannedLinksOpenRoadmap', async () => {
    const onNavigate = vi.fn()
    render(StudioNav, { model: model(), open: true, onToggle: () => {}, onNavigate })

    const hosts = screen.getByRole('button', { name: /^Hosts/ })
    expect(hosts.textContent).toContain('Planned')
    await userEvent.click(hosts)
    expect(onNavigate).toHaveBeenCalledWith('#roadmap/hosts')

    await userEvent.click(screen.getByRole('button', { name: /^Environments/ }))
    expect(onNavigate).toHaveBeenCalledWith('#roadmap/environments')

    await userEvent.click(screen.getByRole('button', { name: /^Connections/ }))
    expect(onNavigate).toHaveBeenCalledWith('#roadmap/connections')
  })
})
