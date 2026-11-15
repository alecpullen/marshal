import type { RoadmapArea } from './routes'

/**
 * The alignment roadmap (design C4): one honest entry per area the Studio
 * UI/UX alignment names as Planned.
 *
 * This is a static registry, deliberately not a service call. The point of
 * the roadmap page is to say what exists today and what does not; a page that
 * fetched "roadmap status" would have to invent timestamps, sample agents or
 * capacity to look alive, and the whole surface is meant to promise nothing.
 * Every field is prose the plan fixed, so the page cannot drift from it
 * without this file changing.
 */
export interface RoadmapAction {
  label: string
  hash: string
}

export interface RoadmapEntry {
  area: RoadmapArea
  title: string
  summary: string
  /** What the app can do here now, in the operator's words. */
  current: string
  /** Planned capabilities, each phrased as the thing that is missing. */
  planned: string[]
  /** Working destinations that do part of this job today; empty when none do. */
  alternatives: RoadmapAction[]
  /** The design notes this area is specified in. */
  designHref?: string
}

const DESIGN_HREF = 'https://github.com/alecpullen/marshal/blob/main/docs/studio-fleet-factory/design-notes.md'

export const ROADMAP: Record<RoadmapArea, RoadmapEntry> = {
  planning: {
    area: 'planning',
    title: 'Planning',
    summary: 'A plan you can read back, revise and hand on — instead of a session you have to re-read.',
    current: 'New agent and New factory work item are the only planning entry points.',
    planned: [
      'A durable overview of the plan that outlives its session',
      'Decisions you can edit after they are made',
      'Document revisions',
      'A separate handoff step',
    ],
    alternatives: [
      { label: 'New agent', hash: '#new' },
      { label: 'New factory work item', hash: '#factory/new' },
    ],
    designHref: DESIGN_HREF,
  },
  voice: {
    area: 'voice',
    title: 'Voice',
    summary: 'Talking to an agent rather than typing at one, without losing the transcript you can edit.',
    current: 'Text composers in the chat and new-agent pages.',
    planned: [
      'Recording instead of typing',
      'Questions that do not interrupt the agent mid-turn',
      'An editable transcript',
      'Quiet capture',
    ],
    alternatives: [{ label: 'New agent', hash: '#new' }],
    designHref: DESIGN_HREF,
  },
  workflow: {
    area: 'workflow',
    title: 'Workflow',
    summary: 'Structured stages, loops and parallel reviews, with a planner proposal you can inspect.',
    current: 'Project defaults and handoff overrides.',
    planned: [
      'Structured stages',
      'Loops over a stage',
      'Parallel reviews',
      'Inspectable planner proposals',
    ],
    alternatives: [
      { label: 'Projects', hash: '#projects' },
      { label: 'Factory', hash: '#factory' },
    ],
    designHref: DESIGN_HREF,
  },
  hosts: {
    area: 'hosts',
    title: 'Hosts',
    summary: 'Hosts you enrol and can see, rather than a runtime you configure by hand.',
    current: 'A configured local or bridge runtime; no in-app enrolment or admission.',
    planned: [
      'Connecting a host from the app',
      'Reported capabilities',
      'Reported capacity',
      'Pausing new work on a host',
    ],
    // Nothing in the app enrols or admits a host today, so there is no
    // working destination to offer in place of the planned one.
    alternatives: [],
    designHref: DESIGN_HREF,
  },
  environments: {
    area: 'environments',
    title: 'Environments',
    summary: 'Trial environments for a change, and the services those trials need.',
    current: 'The Workspaces gallery, designer and builds.',
    planned: ['A pull-request trial flow for a repository', 'Companion-service lifecycle'],
    alternatives: [{ label: 'Workspaces', hash: '#workspaces' }],
    designHref: DESIGN_HREF,
  },
  connections: {
    area: 'connections',
    title: 'Connections',
    summary: 'One forge connection that reports what it can do, instead of settings split three ways.',
    current: 'Repository, provider and credential settings.',
    planned: ['A unified forge connection', 'Capability discovery'],
    alternatives: [
      { label: 'Repositories', hash: '#settings/repos' },
      { label: 'Providers', hash: '#settings/providers' },
      { label: 'Credentials', hash: '#settings/credentials' },
    ],
    designHref: DESIGN_HREF,
  },
}
