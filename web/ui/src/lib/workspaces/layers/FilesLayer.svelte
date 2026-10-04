<script lang="ts">
  import { untrack } from 'svelte'
  import LayerCard from '../LayerCard.svelte'
  import type { LayerProps } from './types'

  let { doc, diags, selected, onSelect, onPatch }: LayerProps = $props()

  interface Row { source: string; target: string; readonly: boolean }
  const fromDoc = (f: LayerProps['doc']['files']): Row[] => Object.entries(f).map(([source, v]) => ({ source, target: v.target, readonly: v.readonly }))
  const complete = (r: Row) => r.source.trim() !== '' && r.target.trim() !== ''

  // svelte-ignore state_referenced_locally
  let rows = $state<Row[]>(fromDoc(doc.files))
  $effect(() => {
    const f = doc.files
    untrack(() => (rows = [...fromDoc(f), ...rows.filter((r) => !complete(r))]))
  })

  function commit() {
    const out = Object.fromEntries(rows.filter(complete).map((r) => [r.source, { target: r.target, readonly: r.readonly }]))
    if (JSON.stringify(out) !== JSON.stringify(Object.fromEntries(Object.entries(doc.files).map(([k, v]) => [k, { target: v.target, readonly: v.readonly }])))) onPatch(5, out)
  }
</script>

<LayerCard layer={5} title="Files" {selected} {diags} {onSelect}>
  {#each rows as r, i (i)}
    <div class="flex flex-wrap items-center gap-1" data-testid="file-row">
      <input class="w-36 rounded border border-border bg-bg px-1.5 py-1 font-mono text-xs" placeholder="source" aria-label="File source" bind:value={r.source} onchange={commit} />
      <input class="w-36 rounded border border-border bg-bg px-1.5 py-1 font-mono text-xs" placeholder="/target" aria-label="File target" bind:value={r.target} onchange={commit} />
      <label class="flex items-center gap-1 text-xs text-muted"><input type="checkbox" bind:checked={r.readonly} onchange={commit} /> read-only</label>
      <button type="button" class="cursor-pointer text-muted hover:text-err" aria-label="Remove file" onclick={() => ((rows = rows.filter((_, j) => j !== i)), commit())}>×</button>
    </div>
  {/each}
  <button type="button" class="w-fit cursor-pointer rounded border border-border px-2 py-1 text-xs hover:bg-hover" onclick={() => rows.push({ source: '', target: '', readonly: true })}>Add file</button>
  <p class="text-xs text-muted">Upload is not in the UI yet: place source files under the template's <code>files/</code> folder in the state store.</p>
</LayerCard>
