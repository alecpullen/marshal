/**
 * The review view's model (design: CI-first review). Everything the review
 * route renders is decided here — which checks read as what, how a round's
 * evidence is judged, what a preview row says about its revision, and which
 * strings are safe to make clickable — so the components stay markup.
 *
 * Judgements are the bridge's, not this file's invention: readiness is bound
 * to a revision, so missing, incomplete and stale-head evidence can never be
 * presented as success, and a review that did not complete cleanly is never
 * described as a clean review.
 */
import type { FactoryCheck, FactoryNarration, FactoryRound } from '../factory'
import type { PreviewDeployment, PreviewEnvironment } from '../preview'

/** How a check reads while its conclusion is still unknown: the forge's own status. */
export function checkState(check: FactoryCheck): string {
  return check.conclusion || check.status || 'pending'
}

/** A seven-character revision, or the absence of one, as the UI spells it. */
export function shortSha(sha: string): string {
  return sha ? sha.slice(0, 7) : 'unknown'
}

export interface NarrationSection {
  heading: string
  paragraphs: string[]
}

/**
 * The narration's sections in the report's fixed order. A field the worker
 * left empty is omitted rather than rendered blank, and the two halves of
 * alternatives/trade-offs share one section because they are one argument.
 */
export function narrationSections(narration?: FactoryNarration | null): NarrationSection[] {
  if (!narration) return []
  const sections: NarrationSection[] = []
  const add = (heading: string, ...paragraphs: string[]) => {
    const kept = paragraphs.filter((paragraph) => paragraph)
    if (kept.length) sections.push({ heading, paragraphs: kept })
  }
  add('Issue', narration.issue)
  add('Approach', narration.approach)
  add('Existing behaviour', narration.existingBehaviour)
  add('Alternatives and trade-offs', narration.alternatives, narration.tradeoffs)
  add('Implementation choice', narration.choice)
  add('Verification', narration.verification)
  add('Limitations', narration.limitations)
  return sections
}

const textEncoder = new TextEncoder()
const unsafeUrlPattern = /[\s`<>\u0000-\u001f\u007f-\u009f]/

/**
 * Spec D10: clickable only as a literal absolute http(s) URL with a nonempty
 * host, no whitespace/control (C0+C1)/angle/backtick characters, at most 2048
 * bytes. Matches the bridge validator factoryURLClickable
 * (web/bridge/factory_prbody.go). No repair: a URL that fails any rule is
 * shown as text, never trimmed or escaped into something clickable.
 */
export function isHttpUrl(value: string): boolean {
  if (!value || !/^https?:\/\//i.test(value) || value.length > 2048 || textEncoder.encode(value).length > 2048 || unsafeUrlPattern.test(value)) return false
  try {
    const parsed = new URL(value)
    return (parsed.protocol === 'http:' || parsed.protocol === 'https:') && !!parsed.hostname
  } catch {
    return false
  }
}

/** The display bound every unstyled provider string gets; no truncation is hidden. */
export function displayText(value: string): string {
  const chars = [...value]
  return chars.length > 512 ? chars.slice(0, 512).join('') + '…' : value
}

export interface HeadBadge {
  label: string
  warning: boolean
}

/**
 * A preview revision against the last PR head the factory itself recorded —
 * never the forge's current head, which the studio cannot read. A mismatch is
 * a warning: the row is evidence for some other revision.
 */
export function headBadge(sha: string, head: string): HeadBadge | null {
  if (!sha || !head) return null
  return sha.toLowerCase() === head.toLowerCase()
    ? { label: 'matches the last factory-recorded PR head', warning: false }
    : { label: 'differs from the last factory-recorded PR head', warning: true }
}

export type PreviewSegment =
  | { kind: 'code'; text: string }
  | { kind: 'text'; text: string }
  | { kind: 'link'; url: string; logs: string }
  | { kind: 'badge'; label: string; sha: string; warning: boolean }

/** One deployment row: environment, provider state, revision, link and description. */
export function deploymentSegments(deployment: PreviewDeployment, head: string): PreviewSegment[] {
  const segments: PreviewSegment[] = [
    { kind: 'code', text: deployment.environment },
    deployment.state
      ? { kind: 'code', text: deployment.state }
      : { kind: 'text', text: '(no status reported by the provider)' },
  ]
  if (deployment.sha) {
    segments.push({ kind: 'code', text: deployment.sha.slice(0, 7) })
    const badge = headBadge(deployment.sha, head)
    if (badge) segments.push({ kind: 'badge', label: badge.label, sha: head.slice(0, 7), warning: badge.warning })
  } else {
    segments.push({ kind: 'text', text: 'revision not reported by the provider' })
  }
  if (deployment.environmentUrl) segments.push({ kind: 'link', url: deployment.environmentUrl, logs: deployment.logUrl })
  if (deployment.description) segments.push({ kind: 'code', text: deployment.description })
  return segments
}

/** One configured environment row: its name and, when the provider has one, its link. */
export function environmentSegments(environment: PreviewEnvironment): PreviewSegment[] {
  return environment.htmlUrl
    ? [{ kind: 'code', text: environment.name }, { kind: 'link', url: environment.htmlUrl, logs: '' }]
    : [{ kind: 'code', text: environment.name }]
}

/**
 * The commit-status links for the item's current revision, named order. A
 * round whose CI was observed at another head is not evidence for this one, so
 * its links are not offered at all.
 */
export function currentStatusLinks(round?: FactoryRound): FactoryCheck[] {
  const ci = round?.ci
  if (!round || !ci || ci.headSha !== round.headSha) return []
  return [...(ci.checks ?? [])]
    .filter((check) => check.key.kind === 'commit_status' && check.url)
    .sort((a, b) => (a.key.name < b.key.name ? -1 : a.key.name > b.key.name ? 1 : a.id < b.id ? -1 : a.id > b.id ? 1 : 0))
}

export type CIEvidenceState = 'missing' | 'incomplete' | 'stale' | 'complete'

export interface CIEvidenceSummary {
  state: CIEvidenceState
  warning: boolean
  text: string
}

/**
 * What the round's CI evidence establishes. Absent, incomplete and stale-head
 * evidence are all warnings: none of them is evidence about this revision, so
 * none of them may read as success.
 */
export function ciEvidence(round?: FactoryRound): CIEvidenceSummary {
  const ci = round?.ci
  if (!ci) return { state: 'missing', warning: true, text: 'No CI evidence observed for this revision, so readiness is not established.' }
  if (ci.headSha !== round?.headSha) return { state: 'stale', warning: true, text: 'The recorded CI evidence belongs to a different revision than this round, so it is not evidence for this head.' }
  if (!ci.complete) return { state: 'incomplete', warning: true, text: 'CI evidence unavailable or incomplete. Readiness is not established.' }
  if (!(ci.checks ?? []).length) return { state: 'complete', warning: false, text: 'Observation complete; no checks reported.' }
  return { state: 'complete', warning: false, text: 'CI evidence is recorded for this revision.' }
}

export interface ReviewVerdict {
  clean: boolean
  warning: boolean
  text: string
}

/**
 * Whether this round's review is clean, in the bridge's own terms: a complete
 * review over accessible inputs with no findings. A failed or unavailable
 * review is never rendered as a clean one.
 */
export function reviewVerdict(round?: FactoryRound): ReviewVerdict {
  const review = round?.review ?? null
  const findings = review?.findings ?? []
  if (!review) return { clean: false, warning: true, text: 'No review has been recorded for this round, so this review is not clean.' }
  if (review.outcome !== 'complete' || !review.inputsAccessible) return { clean: false, warning: true, text: 'The review did not complete, so this is not a clean review.' }
  if (findings.length) return { clean: false, warning: true, text: `${findings.length} review ${findings.length === 1 ? 'finding needs' : 'findings need'} repair before this review is clean.` }
  return { clean: true, warning: false, text: 'Review complete with no findings.' }
}
