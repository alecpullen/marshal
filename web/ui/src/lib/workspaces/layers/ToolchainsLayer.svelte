<script lang="ts">
  import LayerCard from '../LayerCard.svelte'
  import type { LayerProps } from './types'

  let { doc, diags, selected, onSelect, onPatch }: LayerProps = $props()

  const LANGS = ['go', 'node', 'python', 'rust']
  let lang = $state('go')
  let version = $state('')

  const set = (toolchains: string[]) => onPatch(2, { ...doc.workspace, toolchains })
  function add() {
    const v = version.trim()
    if (!v) return
    version = ''
    const t = `${lang}@${v}`
    if (!doc.workspace.toolchains.includes(t)) set([...doc.workspace.toolchains, t])
  }
</script>

<LayerCard layer={2} title="Toolchains" {selected} {diags} {onSelect}>
  <div class="flex flex-wrap gap-1" role="group" aria-label="Toolchains">
    {#each doc.workspace.toolchains as t (t)}
      <span class="inline-flex items-center gap-1 rounded bg-line px-1.5 py-px font-mono text-[11px]">
        {t}
        <button type="button" class="cursor-pointer text-muted hover:text-err" aria-label="Remove {t}" onclick={() => set(doc.workspace.toolchains.filter((x) => x !== t))}>×</button>
      </span>
    {/each}
  </div>
  <div class="flex items-center gap-1">
    <select class="rounded border border-border bg-bg px-1.5 py-1 text-xs" bind:value={lang} aria-label="Language">
      {#each LANGS as l (l)}<option value={l}>{l}</option>{/each}
    </select>
    <input
      class="w-24 rounded border border-border bg-bg px-1.5 py-1 font-mono text-xs"
      placeholder="version"
      aria-label="Toolchain version"
      bind:value={version}
      onkeydown={(e) => e.key === 'Enter' && add()}
    />
    <button type="button" class="cursor-pointer rounded border border-border px-2 py-1 text-xs hover:bg-hover" onclick={add}>Add</button>
  </div>
</LayerCard>
