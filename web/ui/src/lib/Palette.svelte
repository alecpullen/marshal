<script lang="ts">
  import { tick } from 'svelte'
  import type { AgentRow } from './fleet'
  import { score } from './palette'
  import { shortName } from './utils'

  let {
    open,
    agents,
    onClose,
    onNavigate,
  }: { open: boolean; agents: AgentRow[]; onClose: () => void; onNavigate: (hash: string) => void } = $props()

  interface Item {
    label: string
    detail: string
    hash: string
  }

  const pages: Item[] = [
    { label: 'Home', detail: 'Inbox', hash: '#' },
    { label: 'Agents', detail: 'Fleet', hash: '#fleet' },
    { label: 'New agent', detail: 'Start a session', hash: '#new' },
    { label: 'Projects', detail: 'Page', hash: '#projects' },
    { label: 'Sessions', detail: 'Page', hash: '#sessions' },
    { label: 'Pending', detail: 'Page', hash: '#pending' },
    { label: 'Clients', detail: 'Page', hash: '#clients' },
    { label: 'Disk', detail: 'Page', hash: '#disk' },
    { label: 'Activity', detail: 'Page', hash: '#activity' },
  ]

  let query = $state('')
  let cursor = $state(0)
  let input = $state<HTMLInputElement>()

  const items = $derived.by(() => {
    const all: Item[] = [
      ...agents.map((a) => ({ label: a.name || a.id, detail: shortName(a.project), hash: `#chat/${a.id}` })),
      ...pages,
    ]
    return all
      .map((it) => ({ it, s: Math.max(score(query, it.label), score(query, it.detail) / 2) }))
      .filter((x) => x.s > 0)
      .sort((a, b) => b.s - a.s)
      .map((x) => x.it)
      .slice(0, 12)
  })

  $effect(() => {
    if (open) {
      query = ''
      cursor = 0
      tick().then(() => input?.focus())
    }
  })
  $effect(() => {
    query
    cursor = 0
  })

  function choose(it: Item | undefined) {
    if (!it) return
    onNavigate(it.hash)
    onClose()
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      cursor = Math.min(cursor + 1, items.length - 1)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      cursor = Math.max(cursor - 1, 0)
    } else if (e.key === 'Enter') {
      e.preventDefault()
      choose(items[cursor])
    } else if (e.key === 'Escape') {
      e.preventDefault()
      onClose()
    }
  }
</script>

{#if open}
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div class="fixed inset-0 z-100 flex items-start justify-center bg-black/60 pt-[15vh]" onclick={onClose}>
    <div
      role="dialog"
      tabindex="-1"
      aria-label="Command palette"
      class="w-full max-w-lg overflow-hidden rounded-lg border border-border bg-surface shadow-xl"
      onclick={(e) => e.stopPropagation()}
    >
      <input
        bind:this={input}
        bind:value={query}
        onkeydown={onKey}
        placeholder="Jump to an agent or page…"
        aria-label="Search"
        class="w-full border-b border-border bg-transparent px-4 py-3 text-sm outline-none"
      />
      <ul role="listbox" class="max-h-80 overflow-y-auto p-1">
        {#each items as it, i (it.hash)}
          <li role="option" aria-selected={i === cursor}>
            <button
              class="flex w-full cursor-pointer items-center gap-2 rounded px-3 py-2 text-left text-sm {i === cursor ? 'bg-raise' : ''}"
              onmousemove={() => (cursor = i)}
              onclick={() => choose(it)}
            >
              <span class="truncate">{it.label}</span>
              <span class="ml-auto shrink-0 text-xs text-muted">{it.detail}</span>
            </button>
          </li>
        {:else}
          <li class="px-3 py-2 text-sm text-muted">No matches</li>
        {/each}
      </ul>
    </div>
  </div>
{/if}
