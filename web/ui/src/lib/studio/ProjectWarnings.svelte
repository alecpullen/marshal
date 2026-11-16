<script lang="ts">
  import type { ProjectStatus } from '../api'
  import type { AgentRow } from '../fleet'

  /**
   * The fleet's project warnings: a project the bridge cannot reach, one it
   * does not trust, worktrees no agent claims, and checkouts several
   * non-isolated agents share.
   *
   * This markup was Dashboard's and moved here unchanged so Fleet can show
   * the same warnings before anything else. The copy and the conditions are
   * the ones that shipped; this is a move, not a rewrite.
   */
  let {
    projects,
    agents,
  }: {
    projects: ProjectStatus[]
    agents: AgentRow[]
  } = $props()

  const unavailable = $derived(projects.filter((p) => !p.available))
  const untrusted = $derived(projects.filter((p) => p.available && p.trust === 'untrusted'))

  const collisions = $derived(
    Object.entries(
      agents
        .filter((a) => !a.isolated && a.status !== 'idle')
        .reduce<Record<string, number>>((acc, a) => ({ ...acc, [a.project]: (acc[a.project] ?? 0) + 1 }), {}),
    ).filter(([, n]) => n > 1),
  )
</script>

{#each unavailable as p (p.root)}
  <div class="rounded-md border border-danger bg-danger/10 p-3 text-sm">
    <div class="font-medium">{p.root} is unavailable</div>
    <div class="mt-1 break-words text-muted">{p.error}</div>
  </div>
{/each}

{#each untrusted as p (p.root)}
  <div class="rounded-md border border-attention bg-attention/10 p-3 text-sm">
    <div class="font-medium">{p.root} is not trusted</div>
    <div class="mt-1 text-muted">
      Its <code>.marshal/config.toml</code> and project-scope plugins are being ignored. Open the project in the
      marshal TUI once to trust it — this cannot be granted from the browser.
    </div>
  </div>
{/each}

{#each projects.filter((p) => (p.orphanWorktrees ?? []).length > 0) as p (p.root)}
  <div class="rounded-md border border-attention bg-attention/10 p-3 text-sm">
    <div class="font-medium">
      {p.orphanWorktrees!.length} unclaimed worktree{p.orphanWorktrees!.length === 1 ? '' : 's'} in {p.root}
    </div>
    <div class="mt-1 text-muted">
      No live agent owns {p.orphanWorktrees!.length === 1 ? 'it' : 'them'}. They were left behind by an earlier
      run and may hold uncommitted work, so nothing was deleted. Remove them by hand when you are sure:
      <code class="break-all">{p.orphanWorktrees!.join(', ')}</code>
    </div>
  </div>
{/each}

{#each collisions as [project, n] (project)}
  <div class="rounded-md border border-attention bg-attention/10 p-3 text-sm">
    <div class="font-medium">{n} non-isolated agents are active in {project}</div>
    <div class="mt-1 text-muted">
      They share the same checkout and can overwrite each other's edits. Spawn agents with isolation to give
      each its own worktree.
    </div>
  </div>
{/each}
