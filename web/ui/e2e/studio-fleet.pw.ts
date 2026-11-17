import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import {
  AT,
  ROOT,
  agentStatus,
  currentHash,
  expectNoHorizontalOverflow,
  factoryItem,
  inboxOf,
  mockFleet,
  open,
  projectName,
  projectStatus,
  setTheme,
  tabTo,
  worker,
} from './fixtures'

/*
  MOCKED PRESENTATION COVERAGE, not live-factory proof.

  Every case here answers the fleet and factory reads with the fixtures in
  `fixtures.ts`. That is deliberate: the real fixture bridge has no GitHub
  credential, so it cannot hold a mixed set of work items, a failed factory
  read, an unavailable project or a worker agent. What these cases prove is
  that the shipped Fleet renders those states correctly — never that the
  bridge produced them. Nothing here creates, approves, retries or recovers
  anything.

  The real-bridge cases live in `studio-shell.pw.ts` and `project-settings.pw.ts`.
*/

/** A fleet holding one of everything Fleet groups differently. */
const mixed = {
  agents: [
    agentStatus({ id: 'worker-1', name: 'worker one', status: 'running', activity: 'editing src/intake.go' }),
    agentStatus({ id: 'needs-1', name: 'needs one', status: 'awaiting-approval', pending: { kind: 'approval', id: 'call-1' } }),
    agentStatus({ id: 'ready-1', name: 'ready one', status: 'idle', branch: 'agent/ready-1', changedFiles: 2 }),
    agentStatus({ id: 'indep-1', name: 'independent one', status: 'idle', changedFiles: 1 }),
  ],
  projects: [projectStatus(), projectStatus({ root: `${ROOT}-two` })],
  inbox: inboxOf(
    { needsYou: ['needs-1'], running: ['worker-1'], ready: ['ready-1'] },
    [{ kind: 'agent', agentId: 'needs-1', at: AT }],
  ),
  factoryItems: [
    factoryItem({ id: 'item-draft', title: 'Draft the intake plan', state: 'draft', projectRoot: ROOT }),
    factoryItem({
      id: 'item-ready',
      title: 'Ready for review',
      state: 'ready',
      projectRoot: ROOT,
      branch: 'factory/item-ready',
    }),
    factoryItem({
      id: 'item-running',
      title: 'Extend the rate limiter',
      state: 'running',
      stage: 'implement',
      projectRoot: `${ROOT}-two`,
      workers: [worker('worker-1')],
    }),
  ],
}

/** Take one of the reviewed screenshots, named for its viewport project. */
async function capture(page: Page, name: string) {
  // Playwright's own test output is the artifact: it lands beside the trace
  // for this case and project, and is copied to `.docs-archive/studio-uiux/`
  // after review. It is not written into the tracked tree.
  await page.screenshot({ path: test.info().outputPath(`${name}-${test.info().project.name}.png`), fullPage: true })
}

test.describe('fleet presentation', () => {
  test('mixedFleetGroupsRealShapedWork', async ({ page }) => {
    const mocked = await mockFleet(page, mixed)
    try {
      await open(page, '#')
      await expect(page.getByRole('heading', { name: 'Fleet', level: 1 })).toBeVisible()

      // The approved order: warnings (none here), needs you, ready for review,
      // project factories, independent agents, resources. The factories
      // heading shares a row with its refresh control, so the walk is over
      // every h2 in the page rather than direct section children.
      await expect(page.locator('main h2')).toHaveText([
        /^Needs you · 2$/,
        /^Ready for review · 2$/,
        /^Project factories · 2$/,
        /^Independent agents · 3$/,
        'Fleet resources',
      ])

      // Needs you carries the factory draft and the parked agent, both with a
      // direct way to open them.
      const needs = page.locator('section', { has: page.getByRole('heading', { name: /^Needs you/ }) })
      await expect(needs.getByTestId('attention-card')).toHaveCount(2)
      await expect(needs).toContainText('Draft the intake plan')
      await expect(needs).toContainText('needs one')
      await expect(needs.getByRole('button', { name: 'Review draft' })).toHaveCount(1)

      // Ready for review carries the ready item and the ready agent.
      const ready = page.locator('section', { has: page.getByRole('heading', { name: /^Ready for review/ }) })
      await expect(ready.getByTestId('attention-card')).toHaveCount(2)
      await expect(ready).toContainText('Ready for review')

      // Both projects get a card, with real counts rather than a spinner.
      const factories = page.getByTestId('project-factory-card')
      await expect(factories).toHaveCount(2)
      await expect(page.getByText(projectName(), { exact: true }).first()).toBeVisible()

      // The agent a factory item holds is not independent; the others are.
      const independents = page.getByTestId('independent-agents')
      await expect(independents).toContainText('independent one')
      await expect(independents).not.toContainText('worker one')

      // The factory's own route renders from the same shell store.
      await page.evaluate((hash) => (window.location.hash = hash), `#projects/${encodeURIComponent(ROOT)}/factory`)
      await expect(page.getByRole('heading', { name: 'Factory', level: 1 })).toBeVisible()
      await expect(page.getByRole('heading', { name: 'Draft the intake plan' })).toBeVisible()
    } finally {
      await mocked.release()
    }
  })

  test('factoryFailureDoesNotHideAgents', async ({ page }) => {
    const mocked = await mockFleet(page, {
      agents: [agentStatus({ id: 'indep-1', name: 'independent one', status: 'idle', changedFiles: 1 })],
      projects: [projectStatus()],
      factoryStatus: 500,
    })
    try {
      await open(page, '#')

      // The failed read is reported where the work would have been, with a retry.
      const error = page.getByTestId('factory-error')
      await expect(error).toBeVisible()
      await expect(error).toContainText('Factory work could not be refreshed')
      await expect(error.getByRole('button', { name: 'Retry' })).toBeVisible()

      // A failed factory read is not an empty fleet: the agents still render,
      // and the page never claims nothing needs a person.
      await expect(page.getByTestId('independent-agents')).toContainText('independent one')
      await expect(page.getByText('Nothing needs you')).toHaveCount(0)
      await expect(page.getByTestId('factory-loading')).toHaveCount(0)
    } finally {
      await mocked.release()
    }
  })

  test('factoryWorkersAreNotIndependent', async ({ page }) => {
    const mocked = await mockFleet(page, {
      agents: [
        agentStatus({ id: 'worker-1', name: 'worker one', status: 'running' }),
        agentStatus({ id: 'indep-1', name: 'independent one', status: 'idle', changedFiles: 1 }),
      ],
      projects: [projectStatus()],
      inbox: inboxOf({ running: ['worker-1'] }),
      factoryItems: [factoryItem({ id: 'item-running', state: 'running', workers: [worker('worker-1')] })],
    })
    try {
      await open(page, '#')

      // The worker belongs to the factory item, so Fleet links it there.
      const card = page.getByTestId('project-factory-card')
      await expect(card.getByTestId('count-linked-agent')).toHaveText('1')
      await expect(page.getByTestId('independent-agents')).toContainText('independent one')
      await expect(page.getByTestId('independent-agents')).not.toContainText('worker one')

      // The project's factory list shows the item the agent drives.
      await page.evaluate((hash) => (window.location.hash = hash), `#projects/${encodeURIComponent(ROOT)}/factory`)
      await expect(page.getByRole('heading', { name: 'Add rate limiting to the intake route' })).toBeVisible()
    } finally {
      await mocked.release()
    }
  })

  test('unavailableProjectCannotStart', async ({ page }) => {
    const mocked = await mockFleet(page, {
      projects: [projectStatus({ available: false, error: 'not a git repository' })],
    })
    try {
      await open(page, '#')
      // The warning comes before anything else Fleet has to say.
      await expect(page.getByText(`${ROOT} is unavailable`)).toBeVisible()
      await expect(page.getByText('not a git repository')).toBeVisible()

      // The prefill link names it, so the form selects it and refuses to start.
      await open(page, `#new?project=${encodeURIComponent(ROOT)}`)
      await expect(page.getByTestId('route-project-notice')).toBeVisible()
      await expect(page.getByTestId('route-project-notice')).toContainText('is unavailable')
      await expect(page.getByRole('button', { name: /^Create agent/ })).toBeDisabled()
    } finally {
      await mocked.release()
    }
  })

  test('fleetAttentionAndProjectLinksSurviveReload', async ({ page }) => {
    const mocked = await mockFleet(page, mixed)
    try {
      await open(page, '#')
      const expectFleet = async () => {
        await expect(page.getByRole('heading', { name: /^Needs you/ })).toBeVisible()
        await expect(page.getByTestId('attention-card')).toHaveCount(4)
        await expect(page.getByTestId('project-factory-card')).toHaveCount(2)
        await expect(page.getByRole('button', { name: 'Review draft' })).toBeVisible()
      }

      await expectFleet()

      // The draft's action opens that item, not the project.
      await page.getByRole('button', { name: 'Review draft' }).click()
      await expect(page).toHaveURL(/#factory\/item-draft/)

      // A reload rebuilds every store from the same responses: the counts, the
      // attention list and the links must be identical, because nothing was
      // carried in memory.
      await page.evaluate(() => (window.location.hash = '#'))
      await page.reload()
      await expect(page.getByRole('heading', { name: 'Fleet', level: 1 })).toBeVisible()
      await expectFleet()
    } finally {
      await mocked.release()
    }
  })

  test('oneFleetStreamAndFactoryPollerSurviveNavigation', async ({ page }) => {
    const mocked = await mockFleet(page, {
      projects: [projectStatus()],
      factoryItems: [factoryItem({ id: 'item-ready', state: 'ready', title: 'Ready for review' })],
    })
    try {
      await open(page, '#')
      await expect(page.getByTestId('project-factory-card')).toBeVisible()

      // Shell mount opens exactly one stream and reads the list once.
      await expect.poll(() => mocked.fleetStreams()).toBe(1)
      await expect.poll(() => mocked.factoryReads()).toBe(1)

      // Fleet → Inbox → project factory → Fleet, through the shell's own route
      // handler. The navigation is left as a hash change on purpose: the
      // shell's polls are on a timer, and the point of this case is that the
      // mount of a route does not add a stream or an immediate list read.
      for (const hash of ['#inbox', `#projects/${encodeURIComponent(ROOT)}/factory`, '#']) {
        await page.evaluate((h) => (window.location.hash = h), hash)
        // Assigning '#' leaves location.hash empty, so the comparison is the
        // shell's own route — the same one App.svelte reads.
        await expect.poll(() => currentHash(page)).toBe(hash)
      }
      await expect(page.getByRole('heading', { name: 'Fleet', level: 1 })).toBeVisible()

      expect(mocked.fleetStreams(), 'a route mount opened a second fleet stream').toBe(1)
      expect(mocked.factoryReads(), 'a route mount read the factory list again').toBe(1)
    } finally {
      await mocked.release()
    }
  })
})

/*
  Accessibility and visual evidence for Fleet (design §9, §10). The screenshots
  are the reviewed artifacts copied to `.docs-archive/studio-uiux/`; they are
  the mixed presentation fixture, not a live factory.
*/
test.describe('fleet accessibility', () => {
  /*
    Two pre-existing palette defects this slice found and may not fix: the
    fixes belong in `web/ui/src/app.css` and `web/ui/src/lib/ui/Button.svelte`,
    which are outside this task's files. An implementation record notes both
    for the palette owner.

    1. `Button`'s filled variants ask for `text-bg`, but app.css sets an
       unlayered `button { color: inherit }` (so form controls inherit the page
       colour). Unlayered rules outrank Tailwind's utility layer, so `text-bg` is
       lost and an accent-filled button draws `--color-fg` on `--color-accent`
       (1.95:1) instead of `--color-bg` on it.
    2. The light variant redefines only the neutrals, so every signal colour
       keeps its dark-theme value on a light background: accent and attention
       headings land at 2.23:1 and 1.71:1, a muted `Planned` badge on
       `--color-line` at 3.78:1, and a tinted attention badge at 1.44:1.

    They are pinned by the contrast pair axe reports, not by node selector: the
    same defect lands on different elements at different widths, so a selector
    list would either miss one or fail for the wrong reason. Any violation whose
    rule is not `color-contrast`, or whose pair is not listed, fails the scan.

    `allowed` is the whole family, because the layout decides which of its
    nodes a given width paints. `required` is the part every width reports, so
    fixing a defect is noticed here rather than staying pinned forever.
  */
  const KNOWN_CONTRAST_PAIRS: Record<'dark' | 'light', { required: string[]; allowed: string[] }> = {
    dark: {
      // `--color-fg` on the filled accent/danger buttons (defect 1).
      required: ['#ece9e4 on #ff875f', '#ece9e4 on #ff87af'],
      allowed: ['#ece9e4 on #ff875f', '#ece9e4 on #ff87af'],
    },
    light: {
      // Signal colours on the light background (defect 2).
      required: ['#ff875f on #faf8f5', '#ffaf5f on #faf8f5'],
      // Plus the tinted-background variants: muted on `--color-line` for the
      // sidebar's `Planned` badge, and attention on its own tint for a parked
      // agent's status badge.
      allowed: ['#ff875f on #faf8f5', '#ffaf5f on #faf8f5', '#6f6a62 on #ddd8cf', '#ffaf5f on #f5e2ce'],
    },
  }

  test('fleetFitsAndScansCleanInBothThemes', async ({ page }) => {
    const mocked = await mockFleet(page, mixed)
    try {
      await open(page, '#')
      await expect(page.getByTestId('project-factory-card')).toHaveCount(2)

      // No sideways scrolling at any of the three widths.
      await expectNoHorizontalOverflow(page)

      for (const theme of ['dark', 'light'] as const) {
        await setTheme(page, theme)
        const { violations } = await new AxeBuilder({ page }).analyze()
        // The pair axe measured, as `foreground on background`: it survives a
        // change of node, which a selector key would not.
        const pairOf = (summary: string) => {
          const m = /foreground color: (#[0-9a-f]{6}), background color: (#[0-9a-f]{6})/i.exec(summary)
          return m ? `${m[1].toLowerCase()} on ${m[2].toLowerCase()}` : ''
        }
        const pinned = KNOWN_CONTRAST_PAIRS[theme]
        const found = violations.flatMap((v) =>
          v.nodes.map((n) => {
            const target = n.target.join(' ')
            const pair = pairOf(n.failureSummary ?? '')
            return {
              pair,
              known: v.id === 'color-contrast' && pinned.allowed.includes(pair),
              detail: `${v.id} (${v.impact}) ${target} :: ${n.html}\n    ${(n.failureSummary ?? '').split('\n').join('\n    ')}`,
            }
          }),
        )

        // Anything not on the pinned list — including a new rule — fails here.
        const unexpected = found.filter((f) => !f.known)
        expect(unexpected.map((f) => f.detail), `axe found violations in the ${theme} theme`).toEqual([])

        // and the always-drawn defects are still present, so fixing one is
        // noticed here rather than staying pinned forever.
        const seen = new Set(found.map((f) => f.pair))
        expect(pinned.required.filter((p) => !seen.has(p)), `the pinned ${theme}-theme contrast pairs changed`).toEqual([])
      }
      await setTheme(page, 'dark')

      await capture(page, 'fleet')
    } finally {
      await mocked.release()
    }
  })

  test('keyboardOnlyNavigationReachesTheShell', async ({ page }) => {
    const mocked = await mockFleet(page, { projects: [projectStatus()] })
    try {
      await open(page, '#')
      await expect(page.getByRole('heading', { name: 'Fleet', level: 1 })).toBeVisible()

      // Tab alone reaches the Inbox destination, and Enter follows it.
      await tabTo(page, /Inbox/)
      await page.keyboard.press('Enter')
      await expect(page.getByRole('heading', { name: 'Inbox', level: 1 })).toBeVisible()
      expect(await page.evaluate(() => window.location.hash)).toBe('#inbox')

      // Tab alone reaches the Fleet resources directory, which is a link list.
      await page.evaluate(() => (window.location.hash = '#'))
      await tabTo(page, /Projects/)
      await page.keyboard.press('Enter')
      await expect(page.getByRole('heading', { name: 'Projects', level: 1 })).toBeVisible()
    } finally {
      await mocked.release()
    }
  })

  test('reducedMotionKeepsTheShellUsable', async ({ page }) => {
    const mocked = await mockFleet(page, mixed)
    try {
      await page.emulateMedia({ reducedMotion: 'reduce' })
      await open(page, '#')
      expect(await page.evaluate(() => window.matchMedia('(prefers-reduced-motion: reduce)').matches)).toBe(true)

      // Nothing on the page animates, and every destination is still reachable.
      await expect(page.getByTestId('project-factory-card')).toHaveCount(2)
      const running = await page.evaluate(() => document.getAnimations().filter((a) => a.playState === 'running').length)
      expect(running, 'the page runs an animation under reduced motion').toBe(0)

      await tabTo(page, /Inbox/)
      await page.keyboard.press('Enter')
      await expect(page.getByRole('heading', { name: 'Inbox', level: 1 })).toBeVisible()
    } finally {
      await mocked.release()
    }
  })
})
