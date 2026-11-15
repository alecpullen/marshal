import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor, within } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import type { ProjectStatus } from '../api'
import type { FactoryConfig, FactoryItem } from '../factory'
import { buildNavModel, type NavModel, type NavModelInput } from './model'
import MobileNav from './MobileNav.svelte'

beforeEach(() => {
  /*
    bits-ui's body scroll lock sets `pointer-events: none` on the body and
    restores it on a ~24 ms timer, which src/test-setup.ts deliberately never
    awaits. A drawer test that ends before that timer fires would leave the next
    test's page unclickable, so each test starts from a clean body.
  */
  document.body.removeAttribute('style')
})

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

describe('MobileNav', () => {
  it('mobilePrimaryDestinations', async () => {
    const onNavigate = vi.fn()
    render(MobileNav, { model: model({ route: '#inbox', needsCount: 4 }), onNavigate })

    // Fleet, Inbox, Library and Settings are the whole bottom bar.
    const primary = screen.getByRole('navigation', { name: 'Primary' })
    expect(primary.querySelectorAll('button')).toHaveLength(4)
    for (const label of ['Fleet', 'Inbox', 'Library', 'Settings']) {
      expect(screen.getByRole('button', { name: new RegExp(`^${label}`) })).toBeTruthy()
    }
    // The current destination and the needs count are both on the bar.
    const inbox = screen.getByRole('button', { name: /^Inbox/ })
    expect(inbox.getAttribute('aria-current')).toBe('page')
    expect(inbox.textContent).toContain('4')
    expect(screen.getByRole('button', { name: /^Fleet/ }).getAttribute('aria-current')).toBeNull()

    // The drawer stays shut until it is asked for.
    expect(screen.queryByRole('dialog')).toBeNull()

    await userEvent.click(screen.getByRole('button', { name: /^Fleet/ }))
    expect(onNavigate).toHaveBeenCalledWith('#')
  })

  it('drawerOpensClosesAndRestoresFocus', async () => {
    const onNavigate = vi.fn()
    render(MobileNav, { model: model({ route: '#', factoryItems: [item({ id: 'live-1', title: 'Add the cache' })] }), onNavigate })

    await userEvent.click(screen.getByRole('button', { name: 'Open navigation' }))
    const drawer = await screen.findByRole('dialog')
    expect(drawer.textContent).toContain('All pages')

    // Every destination the desktop shows is reachable here.
    const inDrawer = within(drawer)
    expect(inDrawer.getByText('Project factories')).toBeTruthy()
    expect(inDrawer.getByRole('button', { name: /^Hosts/ }).textContent).toContain('Planned')
    expect(inDrawer.getByRole('button', { name: /^Settings/ })).toBeTruthy()

    // Closing returns focus to the menu button.
    await userEvent.click(screen.getByRole('button', { name: 'Close navigation' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Open navigation' })))
    // The body scroll lock releases on a short timer; the page must be
    // clickable again before the drawer is reopened.
    await waitFor(() => expect(document.body.style.pointerEvents).not.toBe('none'))

    // Navigating from the drawer does both: it routes, then closes the drawer.
    const menu = screen.getByRole('button', { name: 'Open navigation' })
    await userEvent.click(menu)
    await screen.findByRole('dialog')
    await userEvent.click(screen.getByRole('button', { name: /^Workspaces/ }))
    expect(onNavigate).toHaveBeenCalledWith('#workspaces')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(menu))
  })

  it('drawerEscapeClosesWithoutResolvingAgentRequests', async () => {
    const onNavigate = vi.fn()
    const fetchSpy = vi.spyOn(globalThis, 'fetch')
    render(MobileNav, { model: model(), onNavigate })

    const menu = screen.getByRole('button', { name: 'Open navigation' })
    await userEvent.click(menu)
    await screen.findByRole('dialog')

    await userEvent.keyboard('{Escape}')

    // Escape closes the drawer and nothing else: it resolves no agent request,
    // navigates nowhere and calls no API — the decision Modal's job, not this.
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(onNavigate).not.toHaveBeenCalled()
    expect(fetchSpy).not.toHaveBeenCalled()
    await waitFor(() => expect(document.activeElement).toBe(menu))

    fetchSpy.mockRestore()
  })
})
