<script lang="ts">
  import ChipList from './ChipList.svelte'
  import Tag from '../ui/Tag.svelte'
  import Button from '../ui/Button.svelte'
  import DiffLines from '../DiffLines.svelte'
  import { diffWorkspace, errMessage, getProjectHealth, listProjects, type BuildsInfo, type ProjectStatus, type WSDoc } from '../api'
  import { shortName } from '../utils'
  import { buildTone, gateRunnable } from './model'

  let {
    name,
    doc,
    builds,
    onPatch,
    onPool,
    onRotateCA,
    onNavigate,
  }: {
    name: string
    doc: WSDoc
    builds: BuildsInfo | null
    onPatch: (layer: number, value: unknown) => void
    onPool: (size: number) => void
    onRotateCA: () => Promise<void>
    onNavigate: (hash: string) => void
  } = $props()

  const versions = $derived(builds?.versions ?? [])
  const latest = $derived(versions.length ? versions[versions.length - 1] : undefined)
  // The designer is not project-scoped, so the gate is checked against a project the user picks.
  let projects = $state<ProjectStatus[]>([])
  let gateProject = $state('')
  let gateCommands = $state<string[]>([])
  $effect(() => {
    void listProjects().then(
      (p) => (projects = p),
      () => (projects = []),
    )
  })
  $effect(() => {
    const root = gateProject
    gateCommands = []
    if (!root) return
    let live = true
    getProjectHealth(root).then(
      (h) => live && (gateCommands = h.verify ? [h.verify.build, h.verify.test] : []),
      () => {},
    )
    return () => {
      live = false
    }
  })
  const gate = $derived(gateRunnable(doc.workspace.toolchains, gateCommands))
  const injected = $derived(Object.keys(doc.inject).length)
  const mb = (b?: number) => (b ? `${(b / 1024 / 1024).toFixed(0)} MB` : '—')
  const secs = (ms?: number) => (ms ? `${(ms / 1000).toFixed(1)} s` : '—')

  let picked = $state<number[]>([])
  let diff = $state<string | null>(null)
  let diffError = $state('')
  let rotating = $state(false)
  let rotateError = $state('')

  function toggle(n: number) {
    picked = picked.includes(n) ? picked.filter((x) => x !== n) : [...picked, n].slice(-2)
    diff = null
    diffError = ''
  }

  $effect(() => {
    if (picked.length !== 2) return
    const [a, b] = [...picked].sort((x, y) => x - y)
    let live = true
    diffWorkspace(name, a, b).then(
      (d) => live && (diff = d),
      (e) => live && (diffError = errMessage(e)),
    )
    return () => {
      live = false
    }
  })

  async function rotate() {
    rotateError = ''
    try {
      await onRotateCA()
      rotating = false
    } catch (e) {
      rotateError = errMessage(e)
    }
  }
  const h = 'text-xs font-medium text-muted'
</script>

<aside class="flex flex-col gap-4 text-sm" aria-label="Workspace details">
  <section class="flex flex-col gap-1.5" data-testid="side-build">
    <h3 class={h}>Build</h3>
    {#if latest}
      <div class="flex items-center gap-2"><Tag tone={buildTone(latest.buildStatus)}>{latest.buildStatus}</Tag><span class="text-xs text-muted">v{latest.n} · {mb(latest.sizeBytes)}</span></div>
    {:else}
      <p class="text-xs text-muted">Not published yet.</p>
    {/if}
    {#if builds?.starts}
      <p class="text-xs text-muted">Start: cold {secs(builds.starts.coldMs)} · warm {secs(builds.starts.warmMs)}</p>
    {/if}
    <label class="flex items-center gap-2 text-xs text-muted">
      Warm pool
      <select class="rounded border border-border bg-bg px-1.5 py-1 text-fg" aria-label="Pool size" value={String(builds?.pool?.size ?? 0)} onchange={(e) => onPool(Number(e.currentTarget.value))}>
        {#each [0, 1, 2, 3, 4] as n (n)}<option value={String(n)}>{n}</option>{/each}
      </select>
      {#if builds?.pool}<span>{builds.pool.idle} idle</span>{/if}
    </label>
    <button type="button" class="w-fit cursor-pointer text-xs text-accent hover:underline" onclick={() => onNavigate(`#workspaces/${encodeURIComponent(name)}/builds`)}>Builds</button>
  </section>

  <section class="flex flex-col gap-1.5" data-testid="side-gate">
    <h3 class={h}>Verify gate</h3>
    {#if gate === 'runnable'}<Tag tone="ok">runnable</Tag>
    {:else if gate === 'may-skip'}<Tag tone="warn">may be skipped</Tag>
    {:else}<p class="text-xs text-muted">{gateProject ? 'This project has no gate command to check.' : 'Pick a project to check its gate command.'}</p>{/if}
    <select class="rounded border border-border bg-bg px-1.5 py-1 text-xs" aria-label="Gate project" bind:value={gateProject}>
      <option value="">Project…</option>
      {#each projects as p (p.root)}<option value={p.root}>{shortName(p.root)}</option>{/each}
    </select>
  </section>

  <section class="flex flex-col gap-1.5" data-testid="side-policy">
    <h3 class={h}>Policy</h3>
    <label class="flex items-center gap-2 text-xs text-muted">
      Mode
      <input
        class="w-28 rounded border border-border bg-bg px-1.5 py-1 font-mono text-fg"
        aria-label="Policy mode"
        value={doc.policy.mode}
        onchange={(e) => onPatch(0, { ...doc.policy, mode: e.currentTarget.value })}
      />
    </label>
    <ChipList items={doc.policy.allow} label="allow rules" placeholder="go test *" onChange={(allow) => onPatch(0, { ...doc.policy, allow })} />
  </section>

  <section class="flex flex-col gap-1.5" data-testid="side-history">
    <h3 class={h}>History</h3>
    {#each [...versions].reverse() as v (v.n)}
      <label class="flex items-center gap-2 text-xs">
        <input type="checkbox" checked={picked.includes(v.n)} onchange={() => toggle(v.n)} aria-label="Select v{v.n}" />
        <span class="font-mono">v{v.n}</span>
        <Tag tone={buildTone(v.buildStatus)}>{v.buildStatus}</Tag>
      </label>
    {:else}
      <p class="text-xs text-muted">No versions yet.</p>
    {/each}
    {#if picked.length === 1}<p class="text-xs text-muted">Pick a second version to compare.</p>{/if}
    {#if diffError}<p role="alert" class="text-xs text-err">{diffError}</p>{/if}
    {#if diff !== null}<DiffLines diff={diff} />{/if}
  </section>

  <section class="flex flex-col gap-1.5" data-testid="side-ca">
    <h3 class={h}>CA</h3>
    <p class="text-xs text-muted">{injected} injected host{injected === 1 ? '' : 's'}</p>
    {#if rotating}
      <p class="text-xs">Rotate the CA? Agents started afterwards use the new one; no rebuild is needed.</p>
      <div class="flex gap-2">
        <Button variant="danger" class="min-h-0 px-2 py-1 text-xs" onclick={rotate}>Rotate</Button>
        <Button variant="ghost" class="min-h-0 px-2 py-1 text-xs" onclick={() => (rotating = false)}>Cancel</Button>
      </div>
      {#if rotateError}<p role="alert" class="text-xs text-err">{rotateError}</p>{/if}
    {:else}
      <Button variant="ghost" class="min-h-0 w-fit px-2 py-1 text-xs" onclick={() => (rotating = true)}>Rotate CA…</Button>
    {/if}
  </section>
</aside>
