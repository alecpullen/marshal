<script lang="ts">
  import { onMount } from 'svelte'
  import { getProjectSettings, listWorkspaces, type WorkspaceListItem } from '../api'
  import { contentChips, gateRunnable, latestVersion, poolLabel, workspaceRef } from './model'

  /*
    The New agent workspace chip (spec §8.2): Studio and repo templates, the
    project default first and marked, unbuilt entries disabled, and a warning
    when the project's gate would be skipped. The choice is the reference a
    spawn body carries as `workspace`; '' keeps today's profile behaviour.
  */
  let {
    project,
    value,
    onChange,
    gateCommand,
  }: {
    project: string
    value: string
    onChange: (ref: string) => void
    /** The project's verify-gate command, when the bridge exposes one. */
    gateCommand?: string
  } = $props()

  let items = $state<WorkspaceListItem[]>([])
  let defaultRef = $state('')

  // Studio entries need a successful build to spawn; repo entries without version info are left to the bridge.
  const unbuilt = (w: WorkspaceListItem) => {
    const v = latestVersion(w)
    return v ? v.buildStatus !== 'ok' : w.source === 'studio'
  }
  const refOf = workspaceRef
  const ordered = $derived([...items].sort((a, b) => Number(refOf(b) === defaultRef) - Number(refOf(a) === defaultRef)))
  const chosen = $derived(items.find((w) => refOf(w) === value))
  const gate = $derived(chosen?.doc ? gateRunnable(chosen.doc.workspace.toolchains, gateCommand) : 'unknown')

  const summary = (w: WorkspaceListItem) =>
    [refOf(w) === defaultRef ? 'default' : '', unbuilt(w) ? 'Build first' : '', ...contentChips(w.doc).slice(0, 3), poolLabel(w.pool)].filter(Boolean).join(' · ')

  onMount(async () => {
    const [list, settings] = await Promise.allSettled([listWorkspaces(), getProjectSettings(project)])
    items = list.status === 'fulfilled' ? list.value.filter((w) => w.source === 'studio' || w.project === project) : []
    defaultRef = settings.status === 'fulfilled' ? (settings.value.workspace ?? '') : ''
    // Preselect the project default when nothing was chosen and it can run.
    const d = items.find((w) => refOf(w) === defaultRef)
    if (!value && d && !unbuilt(d)) onChange(refOf(d))
  })
</script>

<label class="flex flex-col gap-1 text-xs text-muted">
  Workspace
  <select class="rounded-md border border-border bg-bg px-2 py-1.5 text-sm text-fg" aria-label="Workspace" {value} onchange={(e) => onChange(e.currentTarget.value)}>
    <option value="">None (profile image)</option>
    {#each ordered as w (`${w.source}:${w.name}`)}
      <option value={refOf(w)} disabled={unbuilt(w)}>{w.name} — {summary(w)}</option>
    {/each}
  </select>
</label>
{#if gate === 'may-skip'}
  <p class="text-xs text-warn" data-testid="gate-warning">Verify gate may be skipped: this workspace has no toolchain for the project's gate command.</p>
{/if}
