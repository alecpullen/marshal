<script lang="ts">
  import { untrack } from 'svelte'
  import LayerCard from '../LayerCard.svelte'
  import type { RepoInfo, WSMount } from '../../api'
  import type { LayerProps } from './types'

  let { doc, diags, selected, onSelect, onPatch, repos }: LayerProps & { repos: RepoInfo[] } = $props()

  interface Row { kind: 'repo' | 'volume'; name: string; target: string; readonly: boolean }
  const fromDoc = (m: WSMount[]): Row[] => m.map((x) => ({ kind: x.repo ? 'repo' : 'volume', name: x.repo || x.volume, target: x.target, readonly: x.readonly }))
  const complete = (r: Row) => r.name.trim() !== '' && r.target.trim() !== ''

  // svelte-ignore state_referenced_locally
  let rows = $state<Row[]>(fromDoc(doc.mounts))
  // A rewrite from the source or a patch response re-seeds the rows; half-filled new rows survive it.
  $effect(() => {
    const m = doc.mounts
    untrack(() => (rows = [...fromDoc(m), ...rows.filter((r) => !complete(r))]))
  })

  const shape = (m: WSMount) => ({ repo: m.repo, volume: m.volume, target: m.target, readonly: m.readonly })
  function commit() {
    const out = rows.filter(complete).map((r) => ({ repo: r.kind === 'repo' ? r.name : '', volume: r.kind === 'volume' ? r.name : '', target: r.target, readonly: r.readonly }))
    // Editing a half-filled row changes nothing the bridge knows about.
    if (JSON.stringify(out) !== JSON.stringify(doc.mounts.map(shape))) onPatch(4, out)
  }
</script>

<LayerCard layer={4} title="Mounts" {selected} {diags} {onSelect}>
  {#each rows as r, i (i)}
    <div class="flex flex-wrap items-center gap-1" data-testid="mount-row">
      <select class="rounded border border-border bg-bg px-1.5 py-1 text-xs" bind:value={r.kind} aria-label="Mount kind" onchange={() => {
          r.name = ''
        }}>
        <option value="repo">repo</option>
        <option value="volume">volume</option>
      </select>
      {#if r.kind === 'repo'}
        <select class="rounded border border-border bg-bg px-1.5 py-1 text-xs" bind:value={r.name} aria-label="Repo" onchange={commit}>
          <option value="">pick a repo</option>
          {#each repos as repo (repo.id)}<option value={repo.id}>{repo.id}</option>{/each}
        </select>
      {:else}
        <input class="w-28 rounded border border-border bg-bg px-1.5 py-1 font-mono text-xs" placeholder="volume" aria-label="Volume" bind:value={r.name} onchange={commit} />
      {/if}
      <input class="w-32 rounded border border-border bg-bg px-1.5 py-1 font-mono text-xs" placeholder="/target" aria-label="Mount target" bind:value={r.target} onchange={commit} />
      <label class="flex items-center gap-1 text-xs text-muted"><input type="checkbox" bind:checked={r.readonly} onchange={commit} /> read-only</label>
      <button type="button" class="cursor-pointer text-muted hover:text-err" aria-label="Remove mount" onclick={() => ((rows = rows.filter((_, j) => j !== i)), commit())}>×</button>
    </div>
  {/each}
  <button type="button" class="w-fit cursor-pointer rounded border border-border px-2 py-1 text-xs hover:bg-hover" onclick={() => rows.push({ kind: 'repo', name: '', target: '', readonly: true })}>Add mount</button>
</LayerCard>
