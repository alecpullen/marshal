import { errMessage, listCIHistory, listReviewDrafts } from '../api'
import { recentFixes } from './model'
import type { CIHistory, ProjectStatus, ReviewDraft } from '../api'

/**
 * What the inbox shows from the automations service, one project at a time.
 * `errors` names the projects whose read failed, in project order.
 */
export interface AutomationSummary {
  drafts: { root: string; draft: ReviewDraft }[]
  fixes: CIHistory[]
  errors: { root: string; message: string }[]
}

/** One project's read, a failed call contributing its fallback and the reason. */
async function settled<T>(load: Promise<T>, fallback: T): Promise<{ value: T; error?: string }> {
  try {
    return { value: await load }
  } catch (e) {
    return { value: fallback, error: errMessage(e) }
  }
}

/**
 * Review drafts and fresh CI fixes across the given projects. A project the
 * bridge cannot answer for is reported in `errors` and dropped from nothing
 * else: one failing read must not empty the whole inbox.
 */
export async function loadAutomationSummaries(projects: ProjectStatus[]): Promise<AutomationSummary> {
  const perProject = await Promise.all(
    projects.map(async (p) => {
      const [drafts, history] = await Promise.all([
        settled(listReviewDrafts({ project: p.root, status: 'draft' }), [] as ReviewDraft[]),
        settled(listCIHistory({ project: p.root }), [] as CIHistory[]),
      ])
      return { root: p.root, drafts: drafts.value, history: history.value, error: drafts.error ?? history.error }
    }),
  )
  const now = Date.now()
  return {
    drafts: perProject.flatMap((p) => p.drafts.filter((d) => d.status === 'draft').map((draft) => ({ root: p.root, draft }))),
    fixes: perProject.flatMap((p) => recentFixes(p.history, now)),
    errors: perProject.flatMap((p) => (p.error ? [{ root: p.root, message: p.error }] : [])),
  }
}
