<script lang="ts">
  import { onMount, tick, untrack } from 'svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Tag from '../../lib/ui/Tag.svelte'
  import { errMessage, listBuilds, startBuild, type BuildStatus, type TemplateVersion } from '../../lib/api'
  import { connectBuildLog } from '../../lib/sse'
  import { buildTone } from '../../lib/workspaces/model'

  let { name, onNavigate }: { name: string; onNavigate: (hash: string) => void } = $props()

  const FAIL_TAIL = 20

  let versions = $state<TemplateVersion[]>([])
  let loaded = $state(false)
  let error = $state('')
  let selected = $state<number | null>(null)
  let lines = $state<string[]>([])
  // The status the stream reports overrides the listed one until the list refreshes.
  let live = $state<BuildStatus | null>(null)
  let follow = $state(true)
  let logEl = $state<HTMLElement | null>(null)
  // Bumped to reconnect the same build after a rebuild.
  let epoch = $state(0)

  async function load() {
    try {
      const b = await listBuilds(name)
      versions = b.versions ?? []
      error = ''
      if (selected === null && versions.length) selected = versions[versions.length - 1].n
    } catch (e) {
      error = errMessage(e)
    } finally {
      loaded = true
    }
  }
  onMount(() => void load())

  const current = $derived(versions.find((v) => v.n === selected))
  const status = $derived<BuildStatus | undefined>(live ?? current?.buildStatus)
  const failed = $derived(status === 'failed')

  $effect(() => {
    const n = selected
    void epoch
    if (n === null) return
    untrack(() => {
      lines = []
      live = null
      follow = true
    })
    const stop = connectBuildLog(name, n, {
      onLine: (l) => {
        lines.push(l)
        if (follow) void tick().then(() => logEl && (logEl.scrollTop = logEl.scrollHeight))
      },
      onDone: (s) => {
        live = s === 'ok' || s === 'failed' ? s : live
        void load()
      },
    })
    return stop
  })

  // Scrolling away from the bottom stops following; returning to it resumes.
  function onScroll() {
    if (!logEl) return
    follow = logEl.scrollHeight - logEl.scrollTop - logEl.clientHeight < 8
  }

  async function rebuild() {
    if (selected === null) return
    try {
      await startBuild(name, selected)
      live = 'building'
      epoch++
    } catch (e) {
      error = errMessage(e)
    }
  }

  const cached = (l: string) => /^cached l\d+/.test(l)
  const inTail = (i: number) => failed && i >= lines.length - FAIL_TAIL
  const mb = (b?: number) => (b ? `${(b / 1024 / 1024).toFixed(0)} MB` : '')
  const dur = (ms?: number) => (ms ? `${(ms / 1000).toFixed(0)} s` : '')
</script>

<div class="mx-auto flex max-w-6xl flex-col gap-4 p-6">
  <header class="flex items-center gap-3">
    <button type="button" class="cursor-pointer text-sm text-muted hover:text-fg" onclick={() => onNavigate(`#workspaces/${encodeURIComponent(name)}/edit`)}>← {name}</button>
    <h1 class="text-lg font-semibold">Builds</h1>
  </header>

  {#if error}<p role="alert" class="text-sm text-err">{error}</p>{/if}
  {#if loaded && versions.length === 0 && !error}<p class="text-sm text-muted">No versions yet. Publish the draft, then build it.</p>{/if}

  <div class="grid gap-4 md:grid-cols-[16rem_minmax(0,1fr)]">
    <ul class="flex flex-col gap-1" aria-label="Versions">
      {#each [...versions].reverse() as v (v.n)}
        <li>
          <button
            type="button"
            class="flex w-full cursor-pointer flex-col gap-0.5 rounded-md border px-2 py-1.5 text-left text-xs {v.n === selected ? 'border-accent bg-raise' : 'border-border hover:bg-hover'}"
            data-testid="build-row"
            aria-pressed={v.n === selected}
            onclick={() => (selected = v.n)}
          >
            <span class="flex items-center gap-2"><span class="font-mono text-sm">v{v.n}</span><Tag tone={buildTone(v.n === selected ? status : v.buildStatus)}>{v.n === selected ? status : v.buildStatus}</Tag></span>
            <span class="text-muted">{[dur(v.buildMs), mb(v.sizeBytes)].filter(Boolean).join(' · ')}</span>
            {#if v.imageTag}<span class="truncate font-mono text-muted" title={v.imageTag}>{v.imageTag}</span>{/if}
          </button>
        </li>
      {/each}
    </ul>

    <div class="flex min-w-0 flex-col gap-2">
      <div class="flex items-center gap-2">
        <span class="text-sm font-medium">{selected === null ? '' : `v${selected} log`}</span>
        {#if status}<Tag tone={buildTone(status)}>{status}</Tag>{/if}
        <span class="flex-1"></span>
        {#if failed}<Button onclick={rebuild}>Rebuild</Button>{/if}
      </div>
      <pre
        bind:this={logEl}
        onscroll={onScroll}
        class="h-96 overflow-auto rounded-md border border-border bg-bg p-2 font-mono text-xs"
        data-testid="build-log"
        aria-label="Build log"
      >{#each lines as l, i (i)}<span class="block whitespace-pre-wrap {cached(l) ? 'text-muted opacity-60' : ''} {inTail(i) ? 'bg-err/10 text-err' : ''}" data-cached={cached(l) ? '' : undefined} data-tail={inTail(i) ? '' : undefined}>{l || ' '}</span>{/each}</pre>
    </div>
  </div>
</div>
