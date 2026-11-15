import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import {
  HEAD_SHA,
  activeImplementation,
  agentStatus,
  currentHash,
  exhaustedRetry,
  expectNoHorizontalOverflow,
  failedCI,
  factoryItem,
  mockFleet,
  multiWorkerPRs,
  namedWorkerRecovery,
  open,
  pendingQuestion,
  previewDeployment,
  previewEnvironment,
  previewReport,
  projectStatus,
  readyReview,
  setTheme,
  tabTo,
  worker,
} from './fixtures'

/*
  MOCKED PRESENTATION COVERAGE, not live-factory proof.

  Every case here answers the fleet, factory and session reads with the
  fixtures in `fixtures.ts`. That is deliberate: the real fixture bridge has no
  GitHub credential, so `factoryAPI.preflight` refuses and no work item can be
  created, approved, retried or recovered against it. What these cases prove is
  that the shipped work-item views render, navigate and answer those states
  correctly — never that the bridge produced the state or persisted a command.

  The session reads `/api/sessions/<id>/{load,stack,access,prompt,steer,cancel}`
  and the session event stream are answered from the fixture too, so a drill-in
  and a return through the full session are not obscured by reads that belong
  to the full session rather than to the work item.

  The real-bridge cases live in `studio-shell.pw.ts`, `project-settings.pw.ts`
  and `factory-handoff.pw.ts`.
*/

/** Take one of the reviewed screenshots, named for its viewport project. */
async function capture(page: Page, name: string) {
  // Playwright's own test output is the artifact: it lands beside the trace
  // for this case and project, and is copied to `.docs-archive/studio-uiux/`
  // after review. It is not written into the tracked tree.
  await page.screenshot({ path: test.info().outputPath(`${name}-${test.info().project.name}.png`), fullPage: true })
}

/** The factory a case's work item sits in: one trusted project, no failures. */
const FLEET = { projects: [projectStatus()] }

test.describe('factory work item presentation', () => {
  test('activeItemOpensActivity', async ({ page }) => {
    // worker-2 is an independent fleet agent, never a worker of this item: a
    // stream for it would mean the item opened something it does not hold.
    const mocked = await mockFleet(page, {
      ...FLEET,
      agents: [agentStatus({ id: 'worker-1', name: 'worker one', status: 'running' }), agentStatus({ id: 'worker-2', name: 'worker two' })],
      factoryItems: [activeImplementation()],
    })
    try {
      await open(page, '#factory/item-active')

      // Activity is the default view for a running item, and the header states
      // the state, stage and round the item really reports.
      await expect(page.getByTestId('work-item-header')).toBeVisible()
      await expect(page.getByRole('tab', { name: 'Activity', selected: true })).toBeVisible()
      const header = page.getByTestId('work-item-header')
      await expect(header).toContainText('running')
      await expect(header).toContainText('implement')
      await expect(header).toContainText('Round 1 of 3')

      // Every recorded event is rendered, newest last, with its role where the
      // item can map it.
      const events = page.getByTestId('activity-event')
      await expect(events).toHaveCount(6)
      await expect(events.last()).toContainText('test lines reported so far')
      await expect(events.first()).toContainText('Approved revision 1.')

      // The worker is a link into this item, not a second independent agent.
      await expect(page.getByTestId('worker-links')).toContainText('implementer: worker-1')

      // A wide screen opens the item's last worker beside activity; a narrow
      // one opens no conversation the route did not ask for.
      const wide = (page.viewportSize()?.width ?? 0) >= 1200
      if (wide) {
        await expect(page.getByTestId('agent-conversation')).toBeVisible()
        await expect.poll(() => mocked.sessionStreams('worker-1')).toBe(1)
      } else {
        await expect(page.getByTestId('agent-conversation')).toHaveCount(0)
        expect(mocked.sessionStreams(), 'a narrow item opened a conversation nobody asked for').toBe(0)
        // Drilling in is what opens it, and it opens exactly one stream.
        await page.getByRole('link', { name: /implementer: worker-1/ }).click()
        await expect(page.getByTestId('agent-conversation')).toBeVisible()
        await expect.poll(() => mocked.sessionStreams('worker-1')).toBe(1)
      }

      // Exactly one selected-session stream, and none for the worker this item
      // does not hold.
      expect(mocked.sessionStreams('worker-1'), 'the selected worker streamed more than once').toBe(1)
      expect(mocked.sessionStreams('worker-2'), 'an unselected worker opened a stream').toBe(0)
    } finally {
      await mocked.release()
    }
  })

  test('agentDrillInReturnsToSameActivity', async ({ page }) => {
    const mocked = await mockFleet(page, {
      ...FLEET,
      agents: [agentStatus({ id: 'worker-1', name: 'worker one', status: 'running' })],
      factoryItems: [activeImplementation()],
    })
    try {
      // The route names the worker, so the drill-in is a deep link a reload
      // reproduces rather than transient state.
      await open(page, '#factory/item-active?view=activity&agent=worker-1')
      await expect(page.getByTestId('agent-conversation')).toBeVisible()
      await expect.poll(() => mocked.sessionStreams('worker-1')).toBe(1)
      const events = page.getByTestId('activity-event')
      await expect(events).toHaveCount(6)

      // Back drops the agent and nothing else: the item and its view stay.
      await page.getByRole('button', { name: '← Activity' }).click()
      await expect.poll(() => currentHash(page)).toBe('#factory/item-active?view=activity')
      await expect(page.getByTestId('agent-conversation')).toHaveCount(0)
      await expect(page.getByTestId('factory-activity-pane')).toBeVisible()
      await expect(page.getByRole('tab', { name: 'Activity', selected: true })).toBeVisible()

      // The list the reader was on is the list they come back to.
      await expect(events).toHaveCount(6)
      expect(mocked.sessionStreams('worker-1'), 'the conversation was opened more than once').toBe(1)
    } finally {
      await mocked.release()
    }
  })

  test('fullSessionBackReturnsToFactory', async ({ page }) => {
    const mocked = await mockFleet(page, {
      ...FLEET,
      agents: [agentStatus({ id: 'worker-1', name: 'worker one', status: 'running' })],
      factoryItems: [activeImplementation()],
    })
    try {
      await open(page, '#factory/item-active?view=activity&agent=worker-1')
      await expect(page.getByTestId('agent-conversation')).toBeVisible()

      // The full session is a page of its own, and the return is carried to it.
      await page.getByRole('button', { name: 'Open full session' }).click()
      await expect.poll(() => currentHash(page)).toMatch(/#chat\/worker-1/)
      expect(await currentHash(page)).toContain(`from=${encodeURIComponent('#factory/item-active?view=activity&agent=worker-1')}`)

      // The full page owns the dock the embedded copy does not.
      await expect(page.getByTestId('dock')).toBeVisible()
      await expect(page.getByTestId('agent-conversation')).toHaveCount(0)

      // Back consumes `from`, landing on this item, this view and this worker
      // rather than on an unscoped Fleet.
      await page.getByRole('button', { name: '← Fleet' }).click()
      await expect.poll(() => currentHash(page)).toBe('#factory/item-active?view=activity&agent=worker-1')
      await expect(page.getByTestId('agent-conversation')).toBeVisible()
      await expect(page.getByTestId('work-item-header')).toContainText('running')
    } finally {
      await mocked.release()
    }
  })

  test('questionAnswerUsesCurrentDecision', async ({ page }) => {
    const mocked = await mockFleet(page, { ...FLEET, factoryItems: [pendingQuestion()] })
    try {
      await open(page, '#factory/item-question')

      const panel = page.getByTestId('factory-decision')
      await expect(panel).toBeVisible()
      await expect(panel).toContainText('Action needed: question')
      await expect(panel).toContainText('Which one should this item implement?')

      // Nothing is sent until the person answers, and the empty answer is not
      // submittable.
      const send = page.getByRole('button', { name: 'Send answer' })
      await expect(send).toBeDisabled()
      expect(mocked.commands(), 'a decision was sent before it was answered').toEqual([])

      await page.getByLabel('Answer').fill('Reject outright with 429 and a Retry-After header.')
      await expect(send).toBeEnabled()
      await send.click()
      await expect(page.getByRole('status')).toContainText('answer accepted')

      // The command carries the decision it was answered against and the
      // version it was reviewed at, both built from the item — not the panel.
      expect(mocked.commands()).toHaveLength(1)
      const sent = mocked.commands()[0].command
      expect(sent.kind).toBe('answer')
      expect(sent.answer).toBe('Reject outright with 429 and a Retry-After header.')
      expect(sent.decisionId).toBe('decision-question')
      expect(sent.expectedVersion).toBe(5)
      expect(mocked.commands()[0].id).toBe('item-question')
    } finally {
      await mocked.release()
    }
  })

  test('retryUsesFreshRoundCopyAndRealPayload', async ({ page }) => {
    const mocked = await mockFleet(page, {
      ...FLEET,
      factoryItems: [exhaustedRetry()],
      // The real profile list the route pickers read.
      profiles: ['alpha', 'beta', 'gamma'],
    })
    try {
      await open(page, '#factory/item-exhausted')

      const panel = page.getByTestId('factory-recovery')
      await expect(panel).toBeVisible()
      await expect(panel).toContainText('Action needed: exhausted')
      await expect(panel).toContainText('3 rounds used · 0 granted total')

      // The copy describes the fresh round a route-only retry starts, rather
      // than an interrupted conversation being resumed.
      await expect(panel).toContainText(
        "A route-only retry starts a fresh round. The interrupted round's review is not reused, and one granted round is spent.",
      )

      // The pickers offer the bridge's real profiles and start at the routes
      // in force, so the choice shown is the one that runs.
      const implementer = page.getByLabel('Implementer profile')
      await expect(implementer).toHaveValue('alpha')
      await expect(implementer.locator('option')).toHaveText(['Keep the approved route', 'alpha', 'beta', 'gamma'])

      const grant = page.getByRole('button', { name: 'Grant rounds and retry' })
      await expect(grant).toBeDisabled()
      expect(mocked.commands()).toEqual([])

      await page.getByLabel('Reason').fill('Grant one more round to repair the two findings.')
      await page.getByLabel('Additional rounds').fill('2')
      await expect(grant).toBeEnabled()
      await grant.click()
      await expect(page.getByRole('status')).toContainText('retry accepted')

      // A route-only retry sends the grant and the reason, not a replacement
      // document and not a route change nobody asked for.
      expect(mocked.commands()).toHaveLength(1)
      const sent = mocked.commands()[0].command
      expect(sent.kind).toBe('retry')
      expect(sent.reason).toBe('Grant one more round to repair the two findings.')
      expect(sent.additionalRounds).toBe(2)
      expect(sent.replacement).toBeUndefined()
      expect(sent.implementerRouting).toBeUndefined()
      expect(sent.reviewerRouting).toBeUndefined()
      expect(sent.decisionId).toBe('decision-exhausted')
      expect(sent.expectedVersion).toBe(8)
    } finally {
      await mocked.release()
    }
  })

  test('recoveryAttestationRemainsDeliberate', async ({ page }) => {
    const mocked = await mockFleet(page, { ...FLEET, factoryItems: [namedWorkerRecovery()] })
    try {
      await open(page, '#factory/item-recovery')

      const panel = page.getByTestId('factory-recovery')
      await expect(panel).toContainText('Action needed: recovery')
      await expect(panel).toContainText('Attestation cannot prove an unreachable worker stopped.')
      // The ID a person must name is the item's own last worker.
      await expect(panel).toContainText('Last worker agent ID: worker-8')

      const attest = page.getByRole('button', { name: 'Record named-worker attestation' })
      await expect(attest).toBeDisabled()

      // Another worker's ID is not this worker's attestation, and a typed
      // name alone is not either: the confirmation is a separate act.
      await page.getByLabel('Type the exact last worker agent ID').fill('worker-7')
      await page.getByLabel('How you confirmed it').fill('Checked the host process list.')
      await expect(attest).toBeDisabled()
      await page.getByLabel('I confirm this named previous worker has stopped').check()
      await expect(attest).toBeDisabled()

      // Nothing is recorded until every part of the attestation is present.
      expect(mocked.commands(), 'a recovery was recorded before it was attested').toEqual([])

      await page.getByLabel('Type the exact last worker agent ID').fill('worker-8')
      await expect(attest).toBeEnabled()
      await attest.click()
      await expect(page.getByRole('status')).toContainText('recover accepted')

      // The attestation is a boolean the bridge can check, and the reason names
      // the worker and how it was confirmed.
      expect(mocked.commands()).toHaveLength(1)
      const sent = mocked.commands()[0].command
      expect(sent.kind).toBe('recover')
      expect(sent.confirmPreviousWorkerStopped).toBe(true)
      expect(sent.reason).toBe('worker-8: Checked the host process list.')
      expect(sent.decisionId).toBe('decision-recovery')
    } finally {
      await mocked.release()
    }
  })

  test('failedCIPrecedesNarration', async ({ page }) => {
    const mocked = await mockFleet(page, {
      ...FLEET,
      factoryItems: [failedCI()],
      // The forge is unreadable in the fixture bridge, so a case that is not
      // about previews answers the read rather than showing its error.
      preview: previewReport({ deployments: [previewDeployment()] }),
    })
    try {
      await open(page, '#factory/item-failed?view=review')
      // The item renders from the shell's store, so the first paint can be a
      // tick behind the navigation: wait for the review before reading the DOM
      // order, or the walk reads an empty page.
      await expect(page.getByTestId('review-view')).toBeVisible()

      // The order is the approved one: evidence before the report, so a reader
      // meets the failing check before what the worker says it did.
      const order = await page.evaluate(() =>
        [...document.querySelectorAll('[data-testid="ci-evidence"], [data-testid="engineering-report"]')].map((el) =>
          el.getAttribute('data-testid'),
        ),
      )
      expect(order).toEqual(['ci-evidence', 'engineering-report'])

      // Both failures are named, and the passing check is not in the way.
      const failures = page.getByTestId('ci-failure')
      await expect(failures).toHaveCount(2)
      await expect(failures.first()).toContainText('unit: failure')
      await expect(failures.first()).toContainText('intake_test.go:120')
      // A check with neither URL nor log is still reported, with no empty link.
      await expect(failures.last()).toContainText('e2e: failure')
      await expect(failures.last().locator('a')).toHaveCount(0)

      // The findings sit with the failures, because that is what a failed
      // review leaves behind.
      await expect(page.getByTestId('review-finding')).toHaveCount(2)
      await expect(page.getByTestId('review-finding').last()).toContainText('The 429 carries no Retry-After header')

      // The narration is rendered after that, in full, under its headings.
      const report = page.getByTestId('engineering-report')
      await expect(report).toContainText('Existing behaviour')
      await expect(report).toContainText('Alternatives and trade-offs')
      await expect(report).toContainText('Selected code')
      await expect(report).toContainText('func (h *Intake) ServeHTTP')
      await expect(page.getByTestId('factory-narration')).toBeVisible()
    } finally {
      await mocked.release()
    }
  })

  test('readyReviewCompactsPasses', async ({ page }) => {
    const mocked = await mockFleet(page, {
      ...FLEET,
      factoryItems: [readyReview()],
      preview: previewReport({
        deployments: [previewDeployment()],
        environments: [previewEnvironment()],
      }),
    })
    try {
      await open(page, '#factory/item-ready?view=review')

      // Passing evidence is confirmation, not the thing to read first: it is
      // collapsed behind a count that matches the checks itself.
      await expect(page.getByTestId('ci-state')).toHaveText('CI evidence is recorded for this revision.')
      const passes = page.getByTestId('ci-passes')
      await expect(passes).toContainText('4 passing checks')
      await expect(passes).not.toHaveAttribute('open', '')
      await expect(page.getByTestId('review-state')).toHaveText('Review complete with no findings.')
      await expect(page.getByTestId('ci-failure')).toHaveCount(0)

      // Opening it shows the checks, and the evidence stays above the report.
      await passes.locator('summary').click()
      await expect(passes.locator('li')).toHaveText(['build', 'lint', 'unit', 'e2e'])

      // Readiness is evidence about a revision, never a claim it was merged.
      await expect(page.getByRole('button', { name: /merge/i })).toHaveCount(0)
      await expect(page.getByTestId('review-forge-links')).toContainText('PR #42')
      await expect(page.getByTestId('factory-preview')).toContainText('staging')
    } finally {
      await mocked.release()
    }
  })

  test('previewMismatchIsVisible', async ({ page }) => {
    const mocked = await mockFleet(page, {
      ...FLEET,
      factoryItems: [readyReview()],
      // Two deployments for one item: one on the recorded PR head, one on a
      // revision the factory never recorded as this PR's head.
      preview: previewReport({
        deployments: [
          previewDeployment({ id: 'deployment-other', environment: 'preview-old', sha: 'd'.repeat(40) }),
          previewDeployment({ id: 'deployment-head', environment: 'preview', sha: HEAD_SHA }),
          // A deployment the provider reported no revision for at all.
          previewDeployment({ id: 'deployment-unknown', environment: 'preview-unknown', sha: '', environmentUrl: '' }),
        ],
      }),
    })
    try {
      await open(page, '#factory/item-ready?view=review')

      const badges = page.getByTestId('head-badge')
      await expect(badges).toHaveCount(2)

      // The mismatch is stated as a warning rather than hidden, and the match
      // says what it is evidence for.
      const mismatch = page.getByText(/differs from the last factory-recorded PR head/)
      await expect(mismatch).toBeVisible()
      await expect(mismatch).toContainText('bbbbbbb')
      await expect(page.getByText(/matches the last factory-recorded PR head/)).toBeVisible()

      // A deployment with no revision says so instead of borrowing one.
      await expect(page.getByTestId('factory-preview')).toContainText('revision not reported by the provider')
    } finally {
      await mocked.release()
    }
  })

  test('itemStatePollDoesNotChangeSelectedView', async ({ page }) => {
    const mocked = await mockFleet(page, { ...FLEET, factoryItems: [readyReview()] })
    try {
      await open(page, '#factory/item-ready?view=review')
      await expect(page.getByTestId('review-view')).toBeVisible()
      await expect(page.getByRole('tab', { name: 'Review', selected: true })).toBeVisible()
      const before = mocked.factoryReads()

      // The shell's poll now reports a different state for the item being read.
      mocked.setItems([
        factoryItem({
          ...readyReview(),
          state: 'running',
          stage: 'review',
          version: 8,
          rounds: [readyReview().rounds![0]],
          workers: [worker('worker-1')],
        }),
      ])
      await expect.poll(() => mocked.factoryReads(), { timeout: 20000 }).toBeGreaterThan(before)

      // The poll redraws the item in place: the state it now reports appears,
      // and the view the reader chose does not move.
      await expect(page.getByTestId('work-item-header')).toContainText('running')
      await expect(page.getByRole('tab', { name: 'Review', selected: true })).toBeVisible()
      await expect(page.getByTestId('review-view')).toBeVisible()
      expect(await currentHash(page)).toBe('#factory/item-ready?view=review')
    } finally {
      await mocked.release()
    }
  })

  test('severalWorkersAndPRsAreNamedNotAggregated', async ({ page }) => {
    const mocked = await mockFleet(page, { ...FLEET, factoryItems: [multiWorkerPRs()] })
    try {
      await open(page, '#factory/item-multi?view=prs')

      // Every recorded PR is listed, including the one with nothing recorded:
      // the absence says so rather than showing a fabricated link.
      const prs = page.getByTestId('pull-request')
      await expect(prs).toHaveCount(2)
      await expect(prs.first()).toContainText('PR #42')
      await expect(prs.last()).toContainText('PR #43')
      await expect(prs.last()).toContainText('No URL recorded')

      // Each worker is its own row with its own role and parked state.
      await page.getByRole('tab', { name: 'Activity' }).click()
      const links = page.getByTestId('worker-links')
      await expect(links).toContainText('implementer: worker-1')
      await expect(links).toContainText('reviewer: worker-2')
      await expect(links).toContainText('implementer: worker-3 (parked)')
    } finally {
      await mocked.release()
    }
  })
})

/*
  Accessibility and visual evidence for the work item (design §9, §10). The
  screenshots are the reviewed artifacts copied to `.docs-archive/studio-uiux/`;
  they are the presentation fixture, not a live factory.

  The pinned pairs are the palette defects slice 1 found in
  `web/ui/src/app.css` and `web/ui/src/lib/ui/Button.svelte`, files no browser
  task owns: filled buttons draw `--color-fg` on `--color-accent` because an
  unlayered `button { color: inherit }` outranks `text-bg`, and the light
  variant redefines only the neutrals, so the signal colours keep their
  dark-theme values on a light page. They are pinned by the contrast pair axe
  measured, not by node, so the same defect is caught wherever the layout puts
  it. Any other rule, or any pair outside the family, fails the scan.
*/
const KNOWN_CONTRAST_PAIRS: Record<'dark' | 'light', { required: string[]; allowed: string[] }> = {
  dark: {
    // These surfaces draw no filled accent button in the dark theme — the
    // decision and retry actions are disabled until they are answerable, and a
    // disabled button takes the flat muted treatment — so nothing is required
    // here, while the family stays pinned if a width does paint one.
    required: [],
    allowed: [
      '#ece9e4 on #ff875f',
      '#ece9e4 on #ff87af',
      // The empty conversation's hint is `--color-muted` at 75% opacity on the
      // chat surface (defect 2's family in the dark theme).
      '#77736e on #18171a',
    ],
  },
  light: {
    required: ['#ff875f on #faf8f5'],
    allowed: [
      '#ff875f on #faf8f5',
      '#ffaf5f on #faf8f5',
      '#ff875f on #f2efea',
      '#ffaf5f on #f2efea',
      '#6f6a62 on #ddd8cf',
      // A warn-toned status badge: attention on its own 15% tint over the page.
      '#ffaf5f on #fbedde',
      // A gold role badge: the activity timeline's implementer tag.
      '#ffaf00 on #f4e5c7',
      // The empty conversation's hint in the light theme: muted at 75%
      // opacity on the chat surface.
      '#908b84 on #f2efea',
    ],
  },
}

/** Scan one surface in both themes, failing on anything not pinned. */
async function scanBothThemes(page: Page) {
  for (const theme of ['dark', 'light'] as const) {
    await setTheme(page, theme)
    const { violations } = await new AxeBuilder({ page }).analyze()
    const pairOf = (summary: string) => {
      const m = /foreground color: (#[0-9a-f]{6}), background color: (#[0-9a-f]{6})/i.exec(summary)
      return m ? `${m[1].toLowerCase()} on ${m[2].toLowerCase()}` : ''
    }
    const pinned = KNOWN_CONTRAST_PAIRS[theme]
    const found = violations.flatMap((v) =>
      v.nodes.map((n) => {
        const pair = pairOf(n.failureSummary ?? '')
        return {
          pair,
          known: v.id === 'color-contrast' && pinned.allowed.includes(pair),
          detail: `${v.id} (${v.impact}) ${n.target.join(' ')} :: ${n.html}\n    ${(n.failureSummary ?? '').split('\n').join('\n    ')}`,
        }
      }),
    )
    expect(
      found.filter((f) => !f.known).map((f) => f.detail),
      `axe found violations in the ${theme} theme`,
    ).toEqual([])
    const seen = new Set(found.map((f) => f.pair))
    expect(pinned.required.filter((p) => !seen.has(p)), `the pinned ${theme}-theme contrast pairs changed`).toEqual([])
  }
  await setTheme(page, 'dark')
}

test.describe('factory work item accessibility', () => {
  test('activeAndDecisionScanCleanInBothThemes', async ({ page }) => {
    const mocked = await mockFleet(page, {
      ...FLEET,
      agents: [agentStatus({ id: 'worker-1', name: 'worker one', status: 'running' })],
      factoryItems: [activeImplementation(), pendingQuestion()],
      preview: previewReport(),
    })
    try {
      // Active execution first: the activity timeline, the worker list and,
      // on a wide screen, the conversation beside them.
      await open(page, '#factory/item-active')
      await expect(page.getByTestId('activity-timeline')).toBeVisible()
      await expectNoHorizontalOverflow(page)
      await scanBothThemes(page)
      if (['desktop', 'phone'].includes(test.info().project.name)) await capture(page, 'execution')

      // Then the decision that asks a question, whose textarea and action are
      // the only controls a person has.
      await open(page, '#factory/item-question')
      await expect(page.getByTestId('factory-decision')).toBeVisible()
      await expectNoHorizontalOverflow(page)
      await scanBothThemes(page)
      if (test.info().project.name === 'phone') await capture(page, 'decision')
    } finally {
      await mocked.release()
    }
  })

  test('recoveryAndReadyScanCleanInBothThemes', async ({ page }) => {
    const mocked = await mockFleet(page, {
      ...FLEET,
      factoryItems: [namedWorkerRecovery(), readyReview(), exhaustedRetry()],
      profiles: ['alpha', 'beta', 'gamma'],
      preview: previewReport({ deployments: [previewDeployment()], environments: [previewEnvironment()] }),
    })
    try {
      // Recovery: the attestation controls and the retry grant's pickers.
      await open(page, '#factory/item-recovery')
      await expect(page.getByTestId('factory-recovery')).toBeVisible()
      await expectNoHorizontalOverflow(page)
      await scanBothThemes(page)
      if (test.info().project.name === 'desktop') await capture(page, 'recovery')

      // Ready review: the evidence, the report and the forge links.
      await open(page, '#factory/item-ready?view=review')
      await expect(page.getByTestId('review-view')).toBeVisible()
      await expectNoHorizontalOverflow(page)
      await scanBothThemes(page)
      if (['desktop', 'phone'].includes(test.info().project.name)) await capture(page, 'ready-review')
    } finally {
      await mocked.release()
    }
  })

  test('keyboardOnlyDrillInAndReturn', async ({ page }) => {
    const mocked = await mockFleet(page, {
      ...FLEET,
      agents: [agentStatus({ id: 'worker-1', name: 'worker one', status: 'running' })],
      factoryItems: [activeImplementation()],
    })
    try {
      await open(page, '#factory/item-active?view=activity')
      await expect(page.getByTestId('activity-timeline')).toBeVisible()

      // Tab alone reaches the worker, and Enter opens its conversation.
      await tabTo(page, /implementer: worker-1/)
      await page.keyboard.press('Enter')
      await expect(page.getByTestId('agent-conversation')).toBeVisible()
      expect(await currentHash(page)).toContain('agent=worker-1')

      // Tab alone reaches the way back, and Enter returns to the same activity.
      await tabTo(page, /← Activity/)
      await page.keyboard.press('Enter')
      await expect(page.getByTestId('factory-activity-pane')).toBeVisible()
      expect(await currentHash(page)).toBe('#factory/item-active?view=activity')
      /*
        Below 1200px the conversation was the page, so returning to activity
        closes it. A wide screen keeps the item's side-by-side default, so the
        conversation stays beside activity there: the route, not the pane, is
        what proves the keyboard walk went back.
      */
      if ((page.viewportSize()?.width ?? 0) < 1200) {
        await expect(page.getByTestId('agent-conversation')).toHaveCount(0)
      }
    } finally {
      await mocked.release()
    }
  })

  test('reducedMotionKeepsTheItemUsable', async ({ page }) => {
    const mocked = await mockFleet(page, { ...FLEET, factoryItems: [activeImplementation()] })
    try {
      await page.emulateMedia({ reducedMotion: 'reduce' })
      await open(page, '#factory/item-active')
      expect(await page.evaluate(() => window.matchMedia('(prefers-reduced-motion: reduce)').matches)).toBe(true)

      // Nothing animates, and the work is still readable and navigable.
      await expect(page.getByTestId('activity-timeline')).toBeVisible()
      const running = await page.evaluate(() => document.getAnimations().filter((a) => a.playState === 'running').length)
      expect(running, 'the item runs an animation under reduced motion').toBe(0)

      await page.evaluate(() => (window.location.hash = '#factory/item-active?view=review'))
      await expect(page.getByTestId('review-view')).toBeVisible()
    } finally {
      await mocked.release()
    }
  })

  test('codeAndLogsScrollInsideTheirContainers', async ({ page }) => {
    const mocked = await mockFleet(page, {
      ...FLEET,
      factoryItems: [failedCI()],
      preview: previewReport({ deployments: [previewDeployment()] }),
    })
    try {
      await open(page, '#factory/item-failed?view=review')
      await expect(page.getByTestId('ci-failure')).toHaveCount(2)

      // A 120-line log must never make the page scroll sideways: it scrolls
      // inside its own box, which is what the box is for.
      await expectNoHorizontalOverflow(page)
      const log = page.getByTestId('ci-failure').first().locator('pre')
      const box = await log.evaluate((el) => ({
        scrollHeight: el.scrollHeight,
        clientHeight: el.clientHeight,
        scrollWidth: el.scrollWidth,
        clientWidth: el.clientWidth,
      }))
      expect(box.scrollHeight, 'the log does not scroll inside its container').toBeGreaterThan(box.clientHeight)
      expect(box.scrollWidth, 'the log overflows sideways inside its container').toBeLessThanOrEqual(box.clientWidth + 1)

      // The code excerpt is bounded the same way.
      const code = page.getByTestId('engineering-report').locator('pre')
      const codeBox = await code.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
      expect(codeBox.scrollWidth, 'the excerpt overflows sideways inside its container').toBeLessThanOrEqual(codeBox.clientWidth + 1)

      // Every primary control is a 44px touch target (design §9).
      for (const name of ['Request stop', 'Refresh']) {
        const control = page.getByRole('button', { name, exact: true })
        const controlBox = await control.boundingBox()
        expect(controlBox, `${name} is not visible`).not.toBeNull()
        expect(controlBox!.height, `${name} is under the 44px touch target`).toBeGreaterThanOrEqual(44)
      }
    } finally {
      await mocked.release()
    }
  })

  /*
    The 200% browser-zoom review, measured rather than eyeballed: a 1440×900
    window at 200% zoom is a 720×450 CSS viewport, and Playwright can set that
    viewport directly. Every work-item surface has to keep the page from
    scrolling sideways at the zoomed width, and the primary control on the one
    surface that asks for input has to stay whole and touchable.
  */
  test('twoHundredPercentZoomKeepsTheWorkItemUsable', async ({ page }) => {
    test.skip(test.info().project.name !== 'desktop', 'one CSS viewport is the measurement')
    const mocked = await mockFleet(page, {
      ...FLEET,
      factoryItems: [activeImplementation(), pendingQuestion(), namedWorkerRecovery(), readyReview()],
      profiles: ['alpha', 'beta', 'gamma'],
      preview: previewReport({ deployments: [previewDeployment()] }),
    })
    try {
      await page.setViewportSize({ width: 720, height: 450 })
      for (const hash of [
        '#factory/item-active',
        '#factory/item-question',
        '#factory/item-recovery',
        '#factory/item-ready?view=review',
      ]) {
        await open(page, hash)
        await expect(page.getByTestId('work-item-header')).toBeVisible()
        const box = await page.evaluate(() => ({
          client: document.documentElement.clientWidth,
          scroll: document.documentElement.scrollWidth,
          height: window.innerHeight,
        }))
        expect(box.client, `${hash} is not the zoomed CSS viewport`).toBe(720)
        expect(box.scroll, `${hash} scrolls sideways at 200% zoom (${JSON.stringify(box)})`).toBe(box.client)
        expect(box.height).toBe(450)
      }

      // The one surface that collects input keeps its action whole.
      await open(page, '#factory/item-question')
      const send = page.getByRole('button', { name: 'Send answer' })
      await expect(send).toBeVisible()
      const sendBox = await send.boundingBox()
      expect(sendBox!.height, 'the answer action is under the 44px touch target').toBeGreaterThanOrEqual(44)
      expect(sendBox!.x, 'the answer action starts off-screen').toBeGreaterThanOrEqual(0)
      expect(sendBox!.x + sendBox!.width, 'the answer action overflows the zoomed viewport').toBeLessThanOrEqual(721)
    } finally {
      await mocked.release()
    }
  })
})
