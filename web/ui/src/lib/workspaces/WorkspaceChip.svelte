<script lang="ts">
  import { onMount } from 'svelte'
  import { getProjectHealth, getProjectSettings, listWorkspaces, type WorkspaceListItem } from '../api'
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
  }: {
    project: string
    value: string
    onChange: (ref: string) => void
  } = $props()

  let items = $state<WorkspaceListItem[]>([])
  let defaultRef = $state('')
  let gateCommands = $state<string[]>([])
  let loaded = $state(false)

  // Studio entries need a successful build to spawn; repo entries without version info are left to the bridge.
  const unbuilt = (w: WorkspaceListItem) => {
    const v = latestVersion(w)
    return v ? v.buildStatus !== 'ok' : w.source === 'studio'
  }
  const refOf = workspaceRef

  // The project default can be pinned (`name@3`); its option carries the pin and is judged by that version.
  const pinned = $derived(/^(.*)@(\d+)$/.exec(defaultRef))
  const defBase = $derived(pinned ? pinned[1] : defaultRef)
  const isDefault = (w: WorkspaceListItem) => defBase !== '' && refOf(w) === defBase
  const valueOf = (w: WorkspaceListItem) => (isDefault(w) && pinned ? defaultRef : refOf(w))
  const blocked = (w: WorkspaceListItem) => {
    if (isDefault(w) && pinned) return (w.versions ?? []).find((v) => v.n === Number(pinned[2]))?.buildStatus !== 'ok'
    return unbuilt(w)
  }
  const ordered = $derived([...items].sort((a, b) => Number(isDefault(b)) - Number(isDefault(a))))
  const chosen = $derived(items.find((w) => valueOf(w) === value))
  const defaultItem = $derived(items.find(isDefault))
  // Choosing nothing makes the bridge use the project default, so an unbuilt one fails the spawn.
  const defaultBlocked = $derived(!!defaultRef && loaded && (!defaultItem || blocked(defaultItem)))
  const gate = $derived(chosen?.doc ? gateRunnable(chosen.doc.workspace.toolchains, gateCommands) : 'unknown')

  const summary = (w: WorkspaceListItem) =>
    [isDefault(w) ? 'default' : '', blocked(w) ? 'Build first' : '', ...contentChips(w.doc).slice(0, 3), poolLabel(w.pool)].filter(Boolean).join(' · ')

  onMount(async () => {
    const [list, settings, health] = await Promise.allSettled([listWorkspaces(), getProjectSettings(project), getProjectHealth(project)])
    if (health.status === 'fulfilled' && health.value.verify) gateCommands = [health.value.verify.build, health.value.verify.test]
    items = list.status === 'fulfilled' ? list.value.filter((w) => w.source === 'studio' || w.project === project) : []
    defaultRef = settings.status === 'fulfilled' ? (settings.value.workspace ?? '') : ''
    loaded = true
    // Preselect the project default when nothing usable was chosen and it can run.
    const d = items.find(isDefault)
    const current = items.find((w) => valueOf(w) === value)
    // A remembered choice that no longer exists or can no longer run falls back to the default.
    if (!value || !current || blocked(current)) {
      if (d && !blocked(d)) onChange(valueOf(d))
      else if (value) onChange('')
    }
  })
</script>

<label class="flex flex-col gap-1 text-xs text-muted">
  Workspace
  <select class="rounded-md border border-border bg-bg px-2 py-1.5 text-sm text-fg" aria-label="Workspace" {value} onchange={(e) => onChange(e.currentTarget.value)}>
    <!-- With a project default, "none" would still mean the default, so it is not offered. -->
    {#if defaultRef}
      <option value="" disabled>Choose a workspace</option>
    {:else}
      <option value="">None (profile image)</option>
    {/if}
    {#each ordered as w (`${w.source}:${w.name}`)}
      <option value={valueOf(w)} disabled={blocked(w)}>{w.name} — {summary(w)}</option>
    {/each}
  </select>
</label>
{#if defaultBlocked}
  <p role="alert" class="text-xs text-warn" data-testid="default-warning">
    The project default workspace ({defaultRef}) {defaultItem ? 'is not built' : 'was not found'}. Build it first, or pick another workspace; starting without one will fail.
  </p>
{/if}
{#if gate === 'may-skip'}
  <p class="text-xs text-warn" data-testid="gate-warning">Verify gate may be skipped: this workspace has no toolchain for the project's gate command.</p>
{/if}
