<script lang="ts">
  import Segmented from '../ui/Segmented.svelte'
  import type { LibraryScope, ProjectStatus } from '../api'
  import { shortName } from '../utils'

  let {
    scope,
    project,
    projects,
    onScope,
    onProject,
  }: {
    scope: LibraryScope
    project: string
    projects: ProjectStatus[]
    onScope: (s: LibraryScope) => void
    onProject: (root: string) => void
  } = $props()
</script>

<div class="flex flex-wrap items-center gap-3">
  <Segmented
    label="Scope"
    value={scope}
    onchange={(v) => onScope(v as LibraryScope)}
    options={[
      { value: 'global', label: 'Global' },
      { value: 'project', label: 'Project' },
    ]}
  />
  {#if scope === 'project'}
    <select
      aria-label="Project"
      class="rounded border border-border bg-bg px-2 py-1.5 text-sm"
      value={project}
      onchange={(e) => onProject(e.currentTarget.value)}
    >
      {#each projects.filter((p) => p.available) as p (p.root)}
        <option value={p.root}>{shortName(p.root)}</option>
      {/each}
    </select>
  {/if}
</div>
