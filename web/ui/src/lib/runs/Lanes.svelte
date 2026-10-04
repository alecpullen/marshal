<script lang="ts">
  import { glyph } from '../glyphs'
  import type { Roster } from '../api'
  import { STAGES, type Lane, type StageKey } from './model'

  export interface Selection { task: number; stage: StageKey }

  let {
    lanes,
    selected = null,
    roster = null,
    onSelect,
  }: {
    lanes: Lane[]
    selected?: Selection | null
    roster?: Roster | null
    onSelect: (s: Selection) => void
  } = $props()

  const mark: Record<string, { glyph: string; tone: string }> = {
    done: { glyph: glyph.OK, tone: 'text-ok' },
    failed: { glyph: glyph.Error, tone: 'text-err' },
    active: { glyph: glyph.Running, tone: 'text-accent' },
    skipped: { glyph: '–', tone: 'text-dim' },
    pending: { glyph: glyph.Ambient, tone: 'text-dim' },
  }
  const label = (k: string) => k[0].toUpperCase() + k.slice(1)
  const sddRoles = $derived((roster?.roles ?? []).filter((r) => r.role.startsWith('sdd') || ['implementer', 'reviewer'].includes(r.role)))
</script>

<div class="overflow-x-auto">
  <table class="w-full border-separate border-spacing-y-1 text-sm" data-testid="lanes">
    <thead>
      <tr class="text-left text-xs text-muted">
        <th class="w-8 px-2 font-normal">#</th>
        <th class="px-2 font-normal">Task</th>
        {#each STAGES as s (s)}<th class="px-2 font-normal">{label(s)}</th>{/each}
      </tr>
    </thead>
    <tbody>
      {#each lanes as l (l.n)}
        <tr class="bg-surface" data-testid="lane-{l.n}">
          <td class="rounded-l-md px-2 py-1.5 font-mono text-xs text-muted">{l.n}</td>
          <td class="max-w-xs px-2 py-1.5">
            <div class="truncate" title={l.title}>{l.title}</div>
            {#if l.deps.length > 0}<span class="font-mono text-[11px] text-dim">after {l.deps.join(', ')}</span>{/if}
          </td>
          {#each STAGES as s, i (s)}
            {@const c = l.cells[s]}
            {@const m = mark[c.state] ?? mark.pending}
            <td class="px-1 py-1 {i === STAGES.length - 1 ? 'rounded-r-md' : ''}">
              <button
                type="button"
                class="flex w-full cursor-pointer items-center gap-1.5 rounded px-2 py-1 text-left hover:bg-hover {selected?.task === l.n && selected.stage === s ? 'bg-raise ring-1 ring-accent' : ''}"
                aria-label="Task {l.n} {label(s)}: {c.state}"
                title={c.detail ?? c.state}
                onclick={() => onSelect({ task: l.n, stage: s })}
              >
                <span class={m.tone} aria-hidden="true">{m.glyph}</span>
                <span class="text-xs {m.tone}">{c.state}</span>
                {#if c.fixRounds}<span class="font-mono text-[11px] text-warn" title="fix rounds">↻{c.fixRounds}</span>{/if}
                {#if c.sha}<span class="font-mono text-[11px] text-muted">{c.sha}</span>{/if}
              </button>
            </td>
          {/each}
        </tr>
      {/each}
    </tbody>
  </table>
</div>

{#if sddRoles.length > 0}
  <section class="mt-4" aria-label="Roles">
    <h3 class="mb-1 text-xs tracking-wide text-muted uppercase">Roles</h3>
    <ul class="flex flex-wrap gap-2 text-xs">
      {#each sddRoles as r (r.role)}
        <li class="rounded-md border border-border bg-surface px-2 py-1">
          <span class="text-sub">{r.role}</span>
          <span class="font-mono text-muted">{r.model ? `${r.provider ? r.provider + '/' : ''}${r.model}` : r.error ? 'unresolved' : 'default'}</span>
        </li>
      {/each}
    </ul>
  </section>
{/if}
