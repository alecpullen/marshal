<script lang="ts">
  import Button from '../ui/Button.svelte'
  import Card from '../ui/Card.svelte'
  import EffectiveConfigSummary from './EffectiveConfigSummary.svelte'
  import {
    DRAFT_SUBAGENT_SOURCES,
    describeSubagentSource,
    frozenSubagentRows,
    resolvedConfigRows,
    settingsVersionMoved,
    subagentConflictNotice,
    subagentUnsupportedNotice,
    type ConfigSummaryRow,
  } from './config'
  import { getVersionedProjectSettings } from '../api'
  import type { FactoryItem, FactorySubagentOverrides } from '../factory'
  import { resolveSubagentRole, subagentOverridesOf } from '../factory'
  import type { WireSubagentControls } from '../stack'

  /**
   * The saved draft's approval (design C5): the same resolved configuration
   * creation previews, then the one decision that authorizes it. Approving is
   * the explicit act — nothing here starts work, so the copy states what the
   * approval authorizes rather than what the run has done or will do.
   *
   * The caller owns the command lifecycle: `busy` is passed in and `onApprove`
   * is the only way out, so this component never holds the command's state.
   */
  /** What a review needs from the project: the version it is bound to, and the defaults it inherits. */
  export interface ApprovalSettingsRead {
    version: string
    projectDefaults?: { implementerSubagents?: FactorySubagentOverrides; reviewerSubagents?: FactorySubagentOverrides } | null
  }
  export type ApprovalSettingsReader = (root: string) => Promise<ApprovalSettingsRead>

  /** A settings-version conflict: the version the review displayed, and the one now in force. */
  export interface ApprovalConflict {
    reviewed?: string
    current?: string
  }

  export interface DraftApprovalProps {
    item: FactoryItem
    busy: boolean
    onApprove: () => void
    /** The session's reported child controls; absent means unknown, never unsupported. */
    capabilities?: WireSubagentControls | null
    /** The settings version this review is bound to, when the caller already read it. */
    settingsVersion?: string
    /** A conflict the bridge reported. Absent leaves detection to this surface. */
    conflict?: ApprovalConflict | null
    /** Re-reads the item, so a conflict can be reviewed again. */
    onReload?: () => void
    /** Injected for tests and for a caller that already read the project's settings. */
    readSettings?: ApprovalSettingsReader
  }

  let {
    item,
    busy,
    onApprove,
    capabilities = null,
    settingsVersion = '',
    conflict = null,
    onReload,
    readSettings = readProjectSettings,
  }: DraftApprovalProps = $props()

  async function readProjectSettings(root: string): Promise<ApprovalSettingsRead> {
    const versioned = await getVersionedProjectSettings(root)
    return { version: versioned.etag, projectDefaults: versioned.settings.factoryDefaults ?? null }
  }

  /** The project's own record, as this review last read it. */
  let project = $state<ApprovalSettingsRead | null>(null)
  let readFailed = $state(false)
  /** The version this review displays; an approval is only safe against this one. */
  let reviewed = $state(settingsVersion)
  let moved = $state(false)

  /*
    The settings a review is bound to move without this page reloading, so the
    version is read here and read again whenever the item moves: an approval
    that would freeze settings nobody read is a conflict, not a failure.
  */
  $effect(() => {
    const root = item.draft.config.projectRoot
    // The item's version is a dependency on purpose: the shell polls it, so a
    // settings change made while this review is open is noticed here.
    const seen = item.version
    if (!root) {
      readFailed = true
      return
    }
    let live = true
    void (async () => {
      try {
        const next = await readSettings(root)
        if (!live) return
        project = next
        readFailed = false
        if (!reviewed) reviewed = next.version
        moved = settingsVersionMoved(reviewed, next.version)
      } catch {
        if (!live) return
        // An unreadable version is stated, never guessed: the review says it is
        // not bound to one rather than showing a version it invented.
        readFailed = true
      }
    })()
    return () => {
      live = false
      void seen
    }
  })

  /** Re-arm on the version just read, then let the caller refresh the item. */
  function reload() {
    reviewed = project?.version ?? ''
    moved = false
    onReload?.()
  }

  const rows = $derived(resolvedConfigRows(item.draft.config))
  const projectDefaults = $derived(project?.projectDefaults ?? null)
  const implementer = $derived(resolveSubagentRole('implementer', {
    item: subagentOverridesOf(item.draft.config, 'implementer'),
    project: subagentOverridesOf(projectDefaults, 'implementer'),
  }))
  const reviewer = $derived(resolveSubagentRole('reviewer', {
    item: subagentOverridesOf(item.draft.config, 'reviewer'),
    project: subagentOverridesOf(projectDefaults, 'reviewer'),
  }))
  const roles = $derived([implementer, reviewer])
  /** The newest approval, which is the revision work runs under. */
  const approved = $derived(item.approvals && item.approvals.length ? item.approvals[item.approvals.length - 1] : null)
  const frozenRows = $derived<ConfigSummaryRow[]>(approved ? frozenSubagentRows(approved) : [])
  const unsupported = $derived(subagentUnsupportedNotice(capabilities))
  const conflictInfo = $derived<ApprovalConflict | null>(
    conflict ?? (moved ? { reviewed, current: project?.version ?? '' } : null),
  )
  const versionLine = $derived(
    project
      ? `Reviewed against project settings version ${reviewed || project.version || 'unversioned'}`
      : readFailed
        ? 'The project settings version could not be read, so this review is not bound to one. Reload before approving if that matters to you.'
        : 'Reading the project settings version…',
  )
  /** A stable hook per frozen row, so a test or a scan reads a labelled value. */
  const idOf = (label: string): string => label.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/(^-|-$)/g, '')
</script>

<div class="grid gap-3">
  <h2 class="font-semibold">Review draft before starting</h2>
  <EffectiveConfigSummary
    {rows}
    title="Resolved configuration"
    consequence="Approving this draft authorizes execution under the resolved configuration above. Recording the approval starts nothing by itself."
    plan={item.draft.plan}
    spec={item.draft.spec}
  />
  <Card class="flex flex-col gap-2" data-testid="subagent-settings">
    <h3 class="text-sm font-semibold">Subagent settings</h3>
    <p class="text-xs text-muted">
      Each role resolves through this draft's own overrides, then the project's factory defaults, then the session's
      reported defaults. Approving freezes what is resolved here on the approved revision; a later default change does
      not reach it.
    </p>
    {#each roles as role (role.role)}
      <section class="flex flex-col gap-1" data-testid={'subagent-' + role.role}>
        <h4 class="text-xs font-medium">
          <span class="sm:hidden">{role.shortLabel}</span>
          <span class="hidden sm:inline">{role.label}</span>
        </h4>
        <dl class="grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[auto_1fr]">
          {#each role.fields as field (field.field)}
            <dt class="text-xs text-muted sm:text-sm">{field.label}</dt>
            <dd class="min-w-0 break-words" data-testid={'subagent-' + role.role + '-' + field.field}>
              <span>{field.value}</span>
              {#if field.explicit}
                <span class="ml-1 text-xs text-accent" data-testid={'subagent-' + role.role + '-' + field.field + '-explicit'}>stated here</span>
              {:else}
                <span class="ml-1 text-xs text-muted">inherited</span>
              {/if}
              <span class="block text-xs text-muted">{describeSubagentSource(field.source, DRAFT_SUBAGENT_SOURCES)}</span>
            </dd>
          {/each}
        </dl>
      </section>
    {/each}
    <p class="text-xs text-muted" data-testid="subagent-settings-version">{versionLine}</p>
  </Card>
  {#if conflictInfo}
    <Card class="flex flex-col gap-2 border-attention text-sm" role="alert" data-testid="subagent-conflict">
      <h3 class="font-semibold">These settings changed after this review was opened</h3>
      <p>{subagentConflictNotice(conflictInfo.reviewed || reviewed, conflictInfo.current || project?.version || '')}</p>
      <div><Button variant="ghost" onclick={reload}>Reload and review again</Button></div>
    </Card>
  {/if}
  {#if unsupported}
    <Card class="border-attention text-sm" data-testid="subagent-unsupported"><p>{unsupported}</p></Card>
  {/if}
  {#if approved}
    <Card class="flex flex-col gap-2" data-testid="approved-subagents">
      <h3 class="text-sm font-semibold">Frozen on approved revision {approved.revision}</h3>
      <p class="text-xs text-muted">
        The revision in force keeps the settings it froze. A change to the project's defaults does not reach work that
        was already approved.
      </p>
      <dl class="grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[auto_1fr]">
        {#each frozenRows as row (row.label)}
          <dt class="text-xs text-muted sm:text-sm">{row.label}</dt>
          <dd class="min-w-0 break-words" data-testid={'frozen-' + idOf(row.label)}>{row.value}</dd>
        {/each}
      </dl>
    </Card>
  {/if}
  <div><Button disabled={busy || !!conflictInfo} onclick={onApprove}>Approve &amp; start</Button></div>
</div>
