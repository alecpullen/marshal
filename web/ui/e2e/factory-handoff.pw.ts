import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import type { ProjectSettings } from '../src/lib/api'
import {
  AT,
  ROOT,
  currentHash,
  expectNoHorizontalOverflow,
  factoryConfig,
  factoryItem,
  mockFleet,
  open,
  projectName,
  projectStatus,
  setTheme,
  tabTo,
} from './fixtures'

/*
  Slice 2's browser evidence: the project factory dashboard (design C5) and the
  handoff / saved-draft approval.

  Two fixture styles run here and they are not interchangeable, exactly as in
  the slice-1 suites:

  - real bridge (`project factory and handoff (real bridge)`): the routes,
    scope, filters, inherited defaults, field-linked validation and the route
    guard, driven against the SPA and API the fixture `webbridge` really serves.
    Nothing here saves, approves, retries or recovers factory work: the bridge
    has no GitHub credential and `factoryAPI.preflight` requires one, so a
    create would be proving the credential's absence rather than the UI.
  - mocked presentation (`project factory and handoff (presentation)` and the
    accessibility describe below): the fleet, factory, project-settings and
    model-profile reads are answered from `fixtures.ts`, and create / command
    requests are recorded and answered there too. Those cases prove what the
    shipped factory and handoff render for a saved draft, and that the payloads
    the SPA sends are right — never that the bridge accepted them.

  A mocked save is presentation coverage, not live factory proof.
*/

const encodedRoot = encodeURIComponent(ROOT)
const projectHash = `#projects/${encodedRoot}`
const scopedFactory = `${projectHash}/factory`
const scopedFilter = (filter: string) => `${scopedFactory}?filter=${filter}`
const handoffHash = `#factory/new?project=${encodedRoot}`

/** Take one reviewed screenshot into Playwright's test output for this project. */
async function capture(page: Page, name: string) {
  // Playwright's own test output is the artifact: it lands beside the trace for
  // this case and project, and the reviewed copies are copied to the gitignored
  // `.docs-archive/studio-uiux/`. Nothing is written into the tracked tree.
  await page.screenshot({ path: test.info().outputPath(`${name}-${test.info().project.name}.png`), fullPage: true })
}

/**
 * On a phone the handoff's sections are disclosures that start closed; a case
 * that types into one opens it first, exactly as a person would. On tablet and
 * desktop they are already open and this does nothing.
 */
async function openHandoffSection(page: Page, name: string) {
  const details = page.locator('details').filter({ has: page.getByRole('heading', { name }) })
  await expect(details, `${name} is not a single disclosure`).toHaveCount(1)
  // The section's own summary, not the `Advanced` disclosure nested in it.
  const summary = details.locator(':scope > summary')
  if (!(await details.evaluate((el) => (el as HTMLDetailsElement).open))) await summary.click()
  await expect.poll(() => details.evaluate((el) => (el as HTMLDetailsElement).open)).toBe(true)
}

/**
 * Wait until the handoff's mocked project settings have resolved.
 *
 * The form reads repositories, workspaces and models before it reads the
 * project's settings, so a summary assertion can otherwise race that read and
 * report the no-settings fallback. The repository row is the honest signal.
 */
async function expectSettingsResolved(page: Page) {
  await expect(page.getByTestId('config-repository')).toHaveText('acme/rocket', { timeout: 15_000 })
}

test.describe('project factory and handoff (real bridge)', () => {
  test('projectFactoryLinksPreserveScope', async ({ page }) => {
    await open(page, scopedFactory)
    await expect(page.getByRole('heading', { name: 'Factory', level: 1 })).toBeVisible()

    // The scope is visible: breadcrumbs back to Projects and to this project,
    // with the project root as the subtitle.
    const crumbs = page.getByRole('navigation', { name: 'Breadcrumb' })
    await expect(crumbs.getByRole('link', { name: 'Projects', exact: true })).toHaveAttribute('href', '#projects')
    await expect(crumbs.getByRole('link', { name: projectName(), exact: true })).toHaveAttribute('href', projectHash)
    await expect(page.getByRole('heading', { name: 'Factory', level: 1 }).locator('..')).toContainText(ROOT)

    // The empty project says what is missing, in this scope's own words.
    await expect(page.getByTestId('factory-counts')).toHaveText('0 current · 0 needs you · 0 ready')
    await expect(page.getByText('No factory work items in this project yet.')).toBeVisible()

    // A filter is a route that keeps the project scope, and `current` — the
    // default — emits no parameter at all.
    const filters = page.getByRole('group', { name: 'Factory filters' })
    await filters.getByRole('button', { name: 'Needs you' }).click()
    await expect.poll(() => currentHash(page)).toBe(scopedFilter('needs-you'))
    await expect(filters.getByRole('button', { name: 'Needs you' })).toHaveAttribute('aria-pressed', 'true')

    await filters.getByRole('button', { name: 'Current' }).click()
    await expect.poll(() => currentHash(page)).toBe(scopedFactory)
    await expect(filters.getByRole('button', { name: 'Current' })).toHaveAttribute('aria-pressed', 'true')

    // The dashboard's own actions start from this scope.
    const header = page.locator('main header')
    await header.getByRole('button', { name: 'New work item' }).click()
    await expect(page.getByRole('heading', { name: 'New factory work item', level: 1 })).toBeVisible()
    expect(await currentHash(page)).toBe(handoffHash)

    await open(page, scopedFactory)
    await expect(page.getByRole('heading', { name: 'Factory', level: 1 })).toBeVisible()
    await page.locator('main header').getByRole('button', { name: 'Start agent' }).click()
    await expect(page.getByRole('heading', { name: 'New agent', level: 1 })).toBeVisible()
    expect(await currentHash(page)).toBe(`#new?project=${encodedRoot}`)

    // The unscoped list is the same component with no scope: a Fleet crumb, no
    // filter parameter, its own empty state, and a project-less handoff.
    await open(page, '#factory')
    await expect(page.getByRole('heading', { name: 'Factory', level: 1 })).toBeVisible()
    const globalCrumbs = page.getByRole('navigation', { name: 'Breadcrumb' })
    await expect(globalCrumbs.getByRole('link', { name: 'Fleet', exact: true })).toHaveAttribute('href', '#')
    await expect(page.getByText('No factory work items yet.')).toBeVisible()
    await page.getByRole('button', { name: 'Create a draft' }).click()
    await expect(page.getByRole('heading', { name: 'New factory work item', level: 1 })).toBeVisible()
    expect(await currentHash(page)).toBe('#factory/new')
  })

  test('handoffLoadsRealDefaultsAndValidation', async ({ page }) => {
    // A create under the fixture bridge would prove only that preflight has no
    // credential. The count proves that nothing tried.
    const creates: string[] = []
    page.on('request', (request) => {
      if (request.method() === 'POST') creates.push(new URL(request.url()).pathname)
    })

    await open(page, handoffHash)
    await expect(page.getByRole('heading', { name: 'New factory work item', level: 1 })).toBeVisible()

    // The summary is filled from the project the bridge really answered for:
    // nothing configured inherits the repository branch and the built-in
    // limits, and an empty inherited check list is stated as a decision still
    // to be acknowledged rather than silently treated as "none".
    const summary = page.getByTestId('effective-config')
    await expect(summary).toBeVisible()
    await expect(page.getByTestId('config-project')).toHaveText(ROOT)
    await expect(page.getByTestId('config-target-branch')).toContainText('main')
    await expect(page.getByTestId('config-target-branch')).toContainText('Repository branch')
    await expect(page.getByTestId('config-environment')).toContainText('Native')
    await expect(page.getByTestId('config-total-rounds')).toContainText('5')
    await expect(page.getByTestId('config-total-rounds')).toContainText('Built-in')
    await expect(page.getByTestId('config-ci-poll')).toContainText('15s')
    await expect(page.getByTestId('config-expected-checks')).toHaveText(/None/)
    await expect(page.getByTestId('config-expected-checks')).toHaveAttribute('data-warning', 'true')
    await expect(page.getByTestId('config-consequence')).toContainText(
      'No work starts until you review the saved draft and approve it.',
    )
    // A settings read that succeeded must not also warn.
    await expect(page.getByTestId('settings-warning')).toHaveCount(0)

    // Validation is field-linked and focuses the first invalid control, in the
    // order the controls appear — and it never posts a create.
    const title = page.getByLabel('Title', { exact: true })
    const plan = page.getByLabel('Plan', { exact: true })
    await page.getByRole('button', { name: 'Save draft' }).click()
    await expect(page.getByTestId('error-title')).toHaveText('A title is required')
    await expect(title).toHaveAttribute('aria-invalid', 'true')
    await expect(title).toHaveAttribute('aria-describedby', 'error-title')
    await expect(title).toBeFocused()
    expect(creates, 'validation must not reach the create endpoint').toEqual([])

    await title.fill('Rate limit the intake route')
    await page.getByRole('button', { name: 'Save draft' }).click()
    await expect(page.getByTestId('error-plan')).toHaveText('A plan is required')
    await expect(plan).toHaveAttribute('aria-invalid', 'true')
    await expect(plan).toBeFocused()
    expect(creates, 'validation must not reach the create endpoint').toEqual([])
  })

  test('handoffRouteGuardRetainsInput', async ({ page }) => {
    await open(page, handoffHash)
    const title = page.getByLabel('Title', { exact: true })
    await expect(title).toBeVisible()
    await title.fill('Harden the intake route')

    /*
      The guard is the shell's, asked before the route changes. The dialog
      arrives from the shell's own handler, so the only honest way to observe
      it is to wait for the event.
    */
    const declineNext = () =>
      new Promise<string>((resolve) => {
        page.once('dialog', async (dialog) => {
          const kind = dialog.type()
          await dialog.dismiss()
          resolve(kind)
        })
      })
    const acceptNext = () =>
      new Promise<void>((resolve) => {
        page.once('dialog', async (dialog) => {
          await dialog.accept()
          resolve()
        })
      })

    // Declining keeps the screen and every bit of the handoff.
    const declined = declineNext()
    await page.locator('main').getByRole('link', { name: 'Factory', exact: true }).click()
    expect(await declined).toBe('confirm')
    await expect(title).toHaveValue('Harden the intake route')
    expect(await currentHash(page)).toBe(handoffHash)

    // Accepting navigates, and the form it protected is gone.
    const accepted = acceptNext()
    await page.locator('main').getByRole('link', { name: 'Factory', exact: true }).click()
    await accepted
    await expect(page.getByRole('heading', { name: 'Factory', level: 1 })).toBeVisible()
    expect(await currentHash(page)).toBe('#factory')
    await expect(page.getByLabel('Title', { exact: true })).toHaveCount(0)
  })

  test('handoffHasNoOverflow', async ({ page }) => {
    await open(page, handoffHash)
    await expect(page.getByRole('heading', { name: 'New factory work item', level: 1 })).toBeVisible()
    await expect(page.getByTestId('effective-config')).toBeVisible()

    // No sideways scrolling at any width, and the controls every width shows
    // stay inside the viewport.
    await expectNoHorizontalOverflow(page, ['Title', 'Plan'])
    await expect(page.getByTestId('config-consequence')).toBeVisible()

    const phone = test.info().project.name === 'phone'
    const section = (name: string) => page.locator('details').filter({ has: page.getByRole('heading', { name }) })

    // Phone discloses sections 2–4; tablet and desktop show them in full.
    for (const name of ['Repository and environment', 'Roles and review', 'Authority and limits']) {
      const details = section(name)
      await expect(details, `${name} is not a single disclosure`).toHaveCount(1)
      expect(await details.evaluate((el) => (el as HTMLDetailsElement).open), `${name} disclosure state`).toBe(!phone)
    }
    await expect(page.getByTestId('handoff-scope')).toBeVisible()
    if (phone) await expect(page.getByLabel('Target branch')).toBeHidden()
    else await expect(page.getByLabel('Target branch')).toBeVisible()

    // The summary sits beside the form where there is room, and below it where
    // there is not.
    const scope = await page.getByTestId('handoff-scope').boundingBox()
    const summary = await page.getByTestId('effective-config').boundingBox()
    const wide = test.info().project.name === 'desktop'
    if (wide) expect(summary!.x).toBeGreaterThanOrEqual(scope!.x + scope!.width - 1)
    else expect(summary!.y).toBeGreaterThanOrEqual(scope!.y + scope!.height - 1)
  })
})

/*
  MOCKED PRESENTATION COVERAGE, not live-factory proof.

  The create and command requests below are answered by `fixtures.ts`, so the
  saved draft and its approval are states this credential-free bridge cannot
  hold. Asserting a payload here proves what the SPA sent and what it rendered
  from the answer; it is never evidence that the real contract accepted it.
*/

/** The project's factory defaults, as a mocked settings read resolves them. */
const SETTINGS: ProjectSettings = {
  intake: { repoId: 'acme/rocket' },
  routing: { profile: 'mock/alpha' },
  factoryDefaults: {
    targetBranch: 'release/2026',
    implementerRouting: { profile: 'mock/gamma' },
    reviewerRouting: { profile: 'mock/beta' },
    reviewerInstructions: 'Start from the intake route',
    expectedChecks: [{ kind: 'commit_status', name: 'ci/build', appId: 0 }],
    maxRounds: 4,
    ciPollSeconds: 20,
    ciWaitSeconds: 1200,
  },
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/

const decision = {
  id: 'decision-1',
  kind: 'question',
  question: 'Which route should the limiter use?',
  reason: '',
  effectId: 'effect-1',
  createdAt: AT,
}

/** One of everything the project factory projects differently. */
const scopedItems = [
  factoryItem({ id: 'item-draft', title: 'Draft the intake plan', state: 'draft' }),
  factoryItem({ id: 'item-decision', title: 'Answer the reviewer', state: 'running', decision }),
  factoryItem({ id: 'item-ready', title: 'Ready for review', state: 'ready' }),
  factoryItem({ id: 'item-closed', title: 'Closed intake work', state: 'closed' }),
  factoryItem({ id: 'item-other', title: 'Other project work', state: 'running', projectRoot: `${ROOT}-two` }),
]

test.describe('project factory and handoff (presentation)', () => {
  test('projectFactoryFiltersAndDirectActions', async ({ page }) => {
    const mocked = await mockFleet(page, { projects: [projectStatus()], factoryItems: scopedItems, settings: SETTINGS })
    try {
      await open(page, scopedFactory)
      await expect(page.getByRole('heading', { name: 'Factory', level: 1 })).toBeVisible()

      // Counts and defaults come from the scope the route names, not the sibling
      // project's items.
      await expect(page.getByTestId('factory-counts')).toHaveText('3 current · 2 needs you · 1 ready')
      await expect(page.getByTestId('factory-defaults')).toBeVisible()
      await expect(page.getByTestId('defaults-target-branch')).toHaveText('release/2026')
      await expect(page.getByTestId('defaults-implementer')).toHaveText('mock/gamma')

      // Work waiting on a person is lifted above the work list: the draft and
      // the decision sit there, the ready item is the only plain work item, and
      // nothing is listed twice.
      const decisions = page.getByTestId('factory-decisions')
      await expect(decisions.getByRole('heading', { name: 'Needs your decision · 2' })).toBeVisible()
      await expect(decisions).toContainText('Draft the intake plan')
      await expect(decisions).toContainText('Answer the reviewer')
      const work = page.getByTestId('factory-work-items')
      await expect(work).toContainText('Ready for review')
      await expect(work).not.toContainText('Draft the intake plan')
      await expect(work).not.toContainText('Answer the reviewer')
      await expect(page.getByTestId('work-item-card')).toHaveCount(3)
      expect((await decisions.boundingBox())!.y).toBeLessThan((await work.boundingBox())!.y)

      // A card reports what the server reported, with one direct action.
      const draftCard = page.getByTestId('work-item-card').filter({ hasText: 'Draft the intake plan' })
      await expect(draftCard.getByTestId('work-item-stage')).toHaveText('none')
      await expect(draftCard.getByTestId('work-item-prs')).toHaveText('No PRs')
      await expect(draftCard.getByTestId('work-item-checks')).toHaveText('No checks yet')
      await expect(draftCard.getByTestId('work-item-workers')).toHaveText('No agents')
      await expect(draftCard.getByRole('button', { name: 'Review draft' })).toBeVisible()
      await expect(
        page.getByTestId('work-item-card').filter({ hasText: 'Answer the reviewer' }).getByRole('button', { name: 'Resolve decision' }),
      ).toBeVisible()
      await expect(
        page.getByTestId('work-item-card').filter({ hasText: 'Ready for review' }).getByRole('button', { name: 'Review work' }),
      ).toBeVisible()

      // Filters are routes that keep the project scope.
      const filters = page.getByRole('group', { name: 'Factory filters' })
      await filters.getByRole('button', { name: 'Needs you' }).click()
      await expect.poll(() => currentHash(page)).toBe(scopedFilter('needs-you'))
      await expect(page.getByTestId('factory-decisions')).toContainText('Draft the intake plan')
      await expect(page.getByTestId('factory-work-items')).toHaveCount(0)

      await filters.getByRole('button', { name: 'History' }).click()
      await expect.poll(() => currentHash(page)).toBe(scopedFilter('history'))
      // Scoped to the page: the desktop sidebar also lists the project's items.
      const main = page.locator('main')
      await expect(main.getByText('Closed intake work')).toBeVisible()
      await expect(main.getByText('Draft the intake plan')).toHaveCount(0)

      await filters.getByRole('button', { name: 'Ready' }).click()
      await expect.poll(() => currentHash(page)).toBe(scopedFilter('ready'))
      await expect(page.getByTestId('work-item-card')).toHaveCount(1)
      await expect(page.getByRole('button', { name: 'Review work' })).toBeVisible()

      await filters.getByRole('button', { name: 'Current' }).click()
      await expect.poll(() => currentHash(page)).toBe(scopedFactory)
      await expect(page.getByTestId('work-item-card')).toHaveCount(3)

      // Each card's action opens that item, on the tab it asks for.
      await draftCard.getByRole('button', { name: 'Review draft' }).click()
      await expect.poll(() => currentHash(page)).toBe('#factory/item-draft?view=approval')

      await open(page, scopedFactory)
      await page
        .getByTestId('work-item-card')
        .filter({ hasText: 'Answer the reviewer' })
        .getByRole('button', { name: 'Resolve decision' })
        .click()
      await expect.poll(() => currentHash(page)).toBe('#factory/item-decision?view=activity')

      await open(page, scopedFactory)
      await page
        .getByTestId('work-item-card')
        .filter({ hasText: 'Ready for review' })
        .getByRole('button', { name: 'Review work' })
        .click()
      await expect.poll(() => currentHash(page)).toBe('#factory/item-ready?view=review')
    } finally {
      await mocked.release()
    }
  })

  test('handoffSummaryTracksOverrides', async ({ page }) => {
    const mocked = await mockFleet(page, { projects: [projectStatus()], settings: SETTINGS })
    try {
      await open(page, handoffHash)
      await expect(page.getByRole('heading', { name: 'New factory work item', level: 1 })).toBeVisible()
      await expectSettingsResolved(page)

      // Every inherited value names the source it came from, so an override is
      // a visible change rather than a silent one.
      await expect(page.getByTestId('config-target-branch')).toContainText('release/2026')
      await expect(page.getByTestId('config-target-branch')).toContainText('Project factory default')
      await expect(page.getByTestId('config-implementer')).toContainText('mock/gamma')
      await expect(page.getByTestId('config-implementer')).toContainText('Project factory default')
      await expect(page.getByTestId('config-reviewer')).toContainText('mock/beta')
      await expect(page.getByTestId('config-expected-checks')).toContainText('commit_status:ci/build')
      await expect(page.getByTestId('config-expected-checks')).toContainText('Project factory default')
      await expect(page.getByTestId('config-total-rounds')).toHaveText(/4\s*Project factory default/)
      await expect(page.getByTestId('config-reviewer-instructions')).toContainText('Start from the intake route')
      await expect(page.getByTestId('config-ci-poll')).toContainText('20s')

      // A phone shows the sections as disclosures; open the ones this case uses.
      await openHandoffSection(page, 'Repository and environment')
      await openHandoffSection(page, 'Roles and review')
      await openHandoffSection(page, 'Authority and limits')

      // Overrides show up in the summary the moment they are chosen.
      await page.getByLabel('Target branch', { exact: true }).fill('handoff-branch')
      await expect(page.getByTestId('config-target-branch')).toContainText('handoff-branch')
      await expect(page.getByTestId('config-target-branch')).toContainText('This handoff')

      await page.getByLabel('Total rounds', { exact: true }).fill('9')
      await expect(page.getByTestId('config-total-rounds')).toHaveText(/9\s*This handoff/)

      await page.getByLabel('Reviewer instructions', { exact: true }).fill('Read the diff before approving')
      await expect(page.getByTestId('config-reviewer-instructions')).toContainText("This handoff's own instructions")

      // Choosing no required checks is only a decision once it is acknowledged.
      await page.getByLabel('Expected checks source').selectOption('none')
      await expect(page.getByTestId('config-expected-checks')).toHaveText(/None/)
      await expect(page.getByTestId('config-expected-checks')).toContainText('This handoff: no required checks')
      await expect(page.getByTestId('config-expected-checks')).toHaveAttribute('data-warning', 'true')
      await page.getByLabel('This repository has no required CI').check()
      await expect(page.getByTestId('config-expected-checks')).not.toHaveAttribute('data-warning')

      // Following stays following: the routes the project resolved are not
      // written into the handoff, so the payload omits every one of them.
      await expect(page.getByTestId('config-implementer')).toContainText('mock/gamma')
      await expect(page.getByTestId('config-implementer')).toContainText('Project factory default')

      await page.getByLabel('Title', { exact: true }).fill('Harden the intake route')
      await page.getByLabel('Plan', { exact: true }).fill('Add a token bucket to the intake route.')
      await page.getByRole('button', { name: 'Save draft' }).click()

      await expect.poll(() => mocked.creates().length).toBe(1)
      const request = mocked.creates()[0]
      expect(request.requestId).toMatch(UUID)
      expect(request.title).toBe('Harden the intake route')
      expect(request.plan).toBe('Add a token bucket to the intake route.')
      expect(request.spec).toBe('')
      expect(request.config).toMatchObject({
        repoId: 'acme/rocket',
        projectRoot: ROOT,
        native: true,
        implementerAuto: false,
        noRequiredCiAcknowledged: true,
        targetBranch: 'handoff-branch',
        maxRounds: 9,
        reviewerInstructions: 'Read the diff before approving',
        expectedChecks: [],
      })
      for (const following of ['implementerRouting', 'reviewerRouting', 'workspace', 'ciPollSeconds', 'ciWaitSeconds']) {
        expect(request.config, `${following} was materialised from a project default`).not.toHaveProperty(following)
      }

      // Saving records the handoff and starts nothing.
      await expect(page.getByTestId('draft-preview')).toBeVisible()
      await expect(page.getByTestId('draft-preview')).toContainText('Harden the intake route')
      await expect(page.getByTestId('config-consequence')).toContainText('Saving it started nothing.')
      expect(mocked.commands(), 'saving a draft must not approve it').toEqual([])
    } finally {
      await mocked.release()
    }
  })

  test('savedDraftShowsResolvedServerConfiguration', async ({ page }) => {
    // The bridge resolves what the handoff left alone. The response carries
    // values the request never sent, so the preview can only be reading it.
    const mocked = await mockFleet(page, {
      projects: [projectStatus()],
      settings: SETTINGS,
      create: (request) =>
        factoryItem({
          id: 'item-saved',
          title: request.title,
          state: 'draft',
          draft: {
            ...request,
            config: factoryConfig({
              hostId: 'runner-7',
              targetBranch: 'release/2026',
              implementerRouting: { profile: 'mock/gamma' },
              reviewerRouting: { profile: 'mock/beta' },
              reviewerInstructions: 'Start from the intake route',
              expectedChecks: [{ kind: 'check_run', name: 'ci/build', appId: 42 }],
              maxRounds: 7,
              ciPollSeconds: 45,
              ciWaitSeconds: 1200,
            }),
          },
        }),
    })
    try {
      await open(page, handoffHash)
      await expect(page.getByRole('heading', { name: 'New factory work item', level: 1 })).toBeVisible()
      // The settings read fills the repository and the routes; saving before it
      // lands would fail validation rather than create.
      await expectSettingsResolved(page)
      await page.getByLabel('Title', { exact: true }).fill('Rate limit the intake route')
      await page.getByLabel('Plan', { exact: true }).fill('Add a token bucket to the intake route.')
      await page.getByRole('button', { name: 'Save draft' }).click()

      // The payload carries only what the handoff chose; every field the bridge
      // can resolve is omitted rather than sent as an empty value.
      await expect.poll(() => mocked.creates().length).toBe(1)
      const config = mocked.creates()[0].config
      expect(Object.keys(config).sort()).toEqual([
        'implementerAuto',
        'native',
        'noRequiredCiAcknowledged',
        'projectRoot',
        'repoId',
      ])

      // The preview renders the server's resolved configuration: host, rounds
      // and CI timing appear nowhere in the handoff, and the checks are the
      // server's own identity for them.
      const preview = page.getByTestId('draft-preview')
      await expect(preview).toBeVisible()
      await expect(page.getByTestId('config-host')).toHaveText('runner-7')
      await expect(page.getByTestId('config-target-branch')).toHaveText('release/2026')
      await expect(page.getByTestId('config-implementer')).toHaveText('mock/gamma')
      await expect(page.getByTestId('config-expected-checks')).toHaveText('check_run:ci/build:42')
      await expect(page.getByTestId('config-total-rounds')).toHaveText('7')
      await expect(page.getByTestId('config-ci-poll-wait')).toHaveText('45s / 1200s')
      await expect(page.getByTestId('config-consequence')).toContainText(
        'Approving this draft is what authorizes execution under the configuration above. Saving it started nothing.',
      )

      // The plan the person wrote is still there, one disclosure away, and the
      // next step is approval — which has not happened.
      await preview.getByText('Plan', { exact: true }).click()
      await expect(page.getByTestId('summary-plan')).toHaveText('Add a token bucket to the intake route.')
      await expect(preview.getByRole('link', { name: 'Review and approve this draft' })).toHaveAttribute(
        'href',
        '#factory/item-saved',
      )
      expect(mocked.commands(), 'saving a draft must not approve it').toEqual([])
    } finally {
      await mocked.release()
    }
  })

  test('draftApprovalDoesNotStartBeforeClick', async ({ page }) => {
    const draft = factoryItem({
      id: 'item-draft',
      title: 'Draft the intake plan',
      state: 'draft',
      version: 3,
      draft: {
        requestId: 'req-draft',
        title: 'Draft the intake plan',
        plan: 'Plan from the bridge',
        spec: '',
        config: factoryConfig({
          hostId: 'runner-7',
          targetBranch: 'release/2026',
          implementerRouting: { profile: 'mock/gamma' },
          reviewerRouting: { profile: 'mock/beta' },
          expectedChecks: [{ kind: 'commit_status', name: 'ci/build', appId: 0 }],
          maxRounds: 4,
          ciPollSeconds: 20,
        }),
      },
    })
    const mocked = await mockFleet(page, {
      projects: [projectStatus()],
      settings: SETTINGS,
      factoryItems: [draft],
      command: (id, _command, item) => (item ? { ...item, state: 'running', stage: 'implement', version: item.version + 1 } : undefined),
    })
    try {
      await open(page, '#factory/item-draft?view=approval')
      await expect(page.getByRole('heading', { name: 'Draft the intake plan', level: 1 })).toBeVisible()

      // The saved draft's approval shows the same resolved rows creation shows.
      const approval = page.getByTestId('draft-approval')
      await expect(approval).toBeVisible()
      await expect(approval.getByRole('heading', { name: 'Review draft before starting' })).toBeVisible()
      await expect(page.getByTestId('config-implementer')).toHaveText('mock/gamma')
      await expect(page.getByTestId('config-target-branch')).toHaveText('release/2026')
      await expect(page.getByTestId('config-expected-checks')).toHaveText('commit_status:ci/build')
      await expect(page.getByTestId('config-total-rounds')).toHaveText('4')

      // Approval authorizes execution under the snapshot; it does not claim any
      // work has started.
      await expect(page.getByTestId('config-consequence')).toContainText(
        'Approving this draft authorizes execution under the resolved configuration above.',
      )
      await expect(page.getByTestId('config-consequence')).toContainText('Recording the approval starts nothing by itself.')
      await expect(approval).not.toContainText(/work has started|already running|work is running/i)

      // Nothing is sent until the person clicks.
      expect(mocked.commands(), 'opening a draft must not approve it').toEqual([])
      await approval.getByRole('button', { name: /Approve & start/ }).click()

      await expect.poll(() => mocked.commands().length).toBe(1)
      const { id, command } = mocked.commands()[0]
      expect(id).toBe('item-draft')
      expect(command.kind).toBe('approve')
      expect(command.expectedVersion).toBe(3)
      expect(command.decisionId).toBeUndefined()
      expect(command.requestId).toMatch(UUID)

      // The accepted command is what the view shows next, and there is no
      // second command behind it.
      await expect(page.getByRole('status').filter({ hasText: 'approve accepted' })).toBeVisible()
      await expect(page.getByTestId('draft-approval')).toHaveCount(0)
      expect(mocked.commands()).toHaveLength(1)
    } finally {
      await mocked.release()
    }
  })
})

/*
  Accessibility and visual evidence for the project factory, the handoff and
  the saved draft's approval (design §9, §10).

  The palette defects slice 1 found live in `web/ui/src/app.css` and
  `web/ui/src/lib/ui/Button.svelte`, which this task does not own. They reach
  these surfaces too, so the scan pins the exact contrast pair axe measured
  rather than a node: any violation whose rule is not `color-contrast`, or
  whose pair is not listed, fails. The list is the whole family, because which
  member of it a surface paints depends on its content and on which controls
  its width shows — the slice-2 record names the pairs each surface drew.
*/
const PALETTE_DEFECTS: Record<'dark' | 'light', string[]> = {
  // `--color-fg` on a filled accent/danger button: `text-bg` is lost to
  // app.css's unlayered `button { color: inherit }`.
  dark: ['#ece9e4 on #ff875f', '#ece9e4 on #ff87af'],
  // Signal colours keep their dark-theme values on the light background, on
  // both the page background and a card's surface: an accent link, attention
  // text, a muted badge on `--color-line`, and attention on its own tint.
  light: [
    '#ff875f on #faf8f5',
    '#ffaf5f on #faf8f5',
    '#ff875f on #f2efea',
    '#ffaf5f on #f2efea',
    '#6f6a62 on #ddd8cf',
    '#ffaf5f on #f5e2ce',
  ],
}

/** Scan the main region and fail on anything outside the pinned palette pairs. */
async function expectOnlyPinnedPaletteDefects(page: Page) {
  for (const theme of ['dark', 'light'] as const) {
    await setTheme(page, theme)
    const { violations } = await new AxeBuilder({ page }).include('main').analyze()
    // The pair axe measured, as `foreground on background`: it survives a
    // change of node, which a selector key would not.
    const pairOf = (summary: string) => {
      const match = /foreground color: (#[0-9a-f]{6}), background color: (#[0-9a-f]{6})/i.exec(summary)
      return match ? `${match[1].toLowerCase()} on ${match[2].toLowerCase()}` : ''
    }
    const allowed = PALETTE_DEFECTS[theme]
    const found = violations.flatMap((violation) =>
      violation.nodes.map((node) => {
        const pair = pairOf(node.failureSummary ?? '')
        return {
          pair,
          known: violation.id === 'color-contrast' && allowed.includes(pair),
          detail: `${violation.id} (${violation.impact}) ${node.target.join(' ')} :: ${node.html}\n    ${(node.failureSummary ?? '').split('\n').join('\n    ')}`,
        }
      }),
    )
    const unexpected = found.filter((violation) => !violation.known)
    expect(unexpected.map((violation) => violation.detail), `axe found violations in the ${theme} theme`).toEqual([])
  }
  await setTheme(page, 'dark')
}

test.describe('project factory and handoff accessibility (presentation)', () => {
  test('factoryAndDefaultsScanCleanInBothThemes', async ({ page }) => {
    const mocked = await mockFleet(page, { projects: [projectStatus()], factoryItems: scopedItems, settings: SETTINGS })
    try {
      await open(page, scopedFactory)
      await expect(page.getByTestId('work-item-card')).toHaveCount(3)
      await expectNoHorizontalOverflow(page)
      await expectOnlyPinnedPaletteDefects(page)
      await capture(page, 'factory')
    } finally {
      await mocked.release()
    }
  })

  test('handoffScansCleanInBothThemes', async ({ page }) => {
    const mocked = await mockFleet(page, { projects: [projectStatus()], settings: SETTINGS })
    try {
      await open(page, handoffHash)
      await expect(page.getByTestId('effective-config')).toBeVisible()
      await expectSettingsResolved(page)
      await expectNoHorizontalOverflow(page)
      await expectOnlyPinnedPaletteDefects(page)
      await capture(page, 'handoff')
    } finally {
      await mocked.release()
    }
  })

  test('draftApprovalScansCleanInBothThemes', async ({ page }) => {
    const draft = factoryItem({
      id: 'item-draft',
      title: 'Draft the intake plan',
      state: 'draft',
      draft: {
        requestId: 'req-draft',
        title: 'Draft the intake plan',
        plan: 'Plan from the bridge',
        spec: '',
        config: factoryConfig({
          hostId: 'runner-7',
          targetBranch: 'release/2026',
          implementerRouting: { profile: 'mock/gamma' },
          reviewerRouting: { profile: 'mock/beta' },
          expectedChecks: [{ kind: 'commit_status', name: 'ci/build', appId: 0 }],
          maxRounds: 4,
          ciPollSeconds: 20,
        }),
      },
    })
    const mocked = await mockFleet(page, { projects: [projectStatus()], settings: SETTINGS, factoryItems: [draft] })
    try {
      await open(page, '#factory/item-draft?view=approval')
      await expect(page.getByTestId('draft-approval')).toBeVisible()
      await expectNoHorizontalOverflow(page)
      await expectOnlyPinnedPaletteDefects(page)
      await capture(page, 'draft')
    } finally {
      await mocked.release()
    }
  })

  test('handoffKeyboardReachesDisclosuresAndSubmit', async ({ page }) => {
    const mocked = await mockFleet(page, { projects: [projectStatus()], settings: SETTINGS })
    try {
      await open(page, handoffHash)
      await expect(page.getByTestId('effective-config')).toBeVisible()
      await expectSettingsResolved(page)
      const phone = test.info().project.name === 'phone'
      const section = (name: string) => page.locator('details').filter({ has: page.getByRole('heading', { name }) })

      // Phone: the form's own sections are labelled disclosures that start
      // closed, and the keyboard alone opens them.
      if (phone) {
        const roles = section('Roles and review')
        await expect(page.getByLabel('Implementer model profile')).toBeHidden()
        await tabTo(page, /Roles and review/)
        await page.keyboard.press('Enter')
        await expect.poll(() => roles.evaluate((el) => (el as HTMLDetailsElement).open)).toBe(true)
        await expect(page.getByLabel('Implementer model profile')).toBeVisible()

        await tabTo(page, /Authority and limits/)
        await page.keyboard.press('Enter')
      } else {
        await expect(page.getByLabel('Implementer model profile')).toBeVisible()
      }

      // `Advanced` starts closed at every width, and the keyboard opens it.
      await expect(page.getByLabel('CI poll seconds')).toBeHidden()
      await tabTo(page, /^Advanced$/, 140)
      await page.keyboard.press('Enter')
      await expect(page.getByLabel('CI poll seconds')).toBeVisible()

      // Tab alone reaches the one action that records the handoff.
      await tabTo(page, /^Save draft$/, 140)
      await expect(page.getByRole('button', { name: 'Save draft' })).toBeFocused()
    } finally {
      await mocked.release()
    }
  })
})
