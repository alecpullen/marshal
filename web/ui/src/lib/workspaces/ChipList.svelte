<script lang="ts">
  let {
    items,
    label,
    placeholder = 'add…',
    onChange,
  }: { items: string[]; label: string; placeholder?: string; onChange: (items: string[]) => void } = $props()

  let draft = $state('')

  function add() {
    const v = draft.trim()
    draft = ''
    if (v && !items.includes(v)) onChange([...items, v])
  }
</script>

<div class="flex flex-wrap items-center gap-1" role="group" aria-label={label}>
  {#each items as it (it)}
    <span class="inline-flex items-center gap-1 rounded bg-line px-1.5 py-px font-mono text-[11px]">
      {it}
      <button type="button" class="cursor-pointer text-muted hover:text-err" aria-label="Remove {it}" onclick={() => onChange(items.filter((x) => x !== it))}>×</button>
    </span>
  {/each}
  <input
    class="min-w-24 flex-1 rounded border border-border bg-bg px-1.5 py-0.5 font-mono text-xs"
    {placeholder}
    aria-label="Add to {label}"
    bind:value={draft}
    onkeydown={(e) => {
      if (e.key === 'Enter') {
        e.preventDefault()
        add()
      }
    }}
    onblur={add}
  />
</div>
