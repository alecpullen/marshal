<script lang="ts">
  import { onMount } from 'svelte'
  import { listProjects, spawnAgent, type ProjectStatus } from '../lib/api'
  let { onDone }: { onDone: (id: string | null) => void } = $props()
  let projects = $state<ProjectStatus[]>([]); let project = $state(''); let name = $state(''); let prompt = $state(''); let mode = $state('edit'); let error = $state<string | null>(null); let busy = $state(false)
  let isolated = $state(false)
  let baseRef = $state('')

  const selected = $derived(projects.find((p) => p.root === project))
  const isolationReason = $derived(selected?.isolation ?? 'available')
  const canIsolate = $derived(isolationReason === 'available')

  // Never leave the checkbox ticked for a project that cannot honour it.
  $effect(() => {
    if (!canIsolate) isolated = false
  })

  onMount(async () => { try { projects = await listProjects(); project = projects.find(p => p.available)?.root ?? '' } catch(e) { error = String(e) } })
  async function create() { if (!project) { error = 'Pick a project first.'; return }; busy = true; try { const r = await spawnAgent({project, name: name || undefined, mode, prompt: prompt || undefined, isolated: isolated || undefined, baseRef: isolated && baseRef.trim() ? baseRef.trim() : undefined}); onDone(r.agentId) } catch(e) { error = e instanceof Error ? e.message : String(e) } finally { busy = false } }
</script>
<div class="mx-auto flex max-w-2xl flex-col gap-4 p-4"><header class="flex justify-between"><h1 class="text-lg font-semibold">New agent</h1><button class="rounded border px-3 py-2" onclick={() => onDone(null)}>Cancel</button></header>{#if error}<div class="border border-danger p-3">{error}</div>{/if}<div class="flex flex-col gap-3 rounded border border-border bg-surface p-4"><label>Project<select bind:value={project} class="mt-1 block min-h-11 w-full bg-bg p-2">{#each projects.filter(p=>p.available) as p}<option value={p.root}>{p.root}</option>{/each}</select></label><label>Name<input bind:value={name} class="mt-1 block min-h-11 w-full bg-bg p-2" /></label><label>Mode<select bind:value={mode} class="mt-1 block min-h-11 w-full bg-bg p-2"><option value="edit">edit</option><option value="default">default</option><option value="auto">auto</option></select></label><label class="flex items-start gap-2 text-sm"><input type="checkbox" bind:checked={isolated} disabled={!canIsolate} class="mt-1" /><span>Isolate in a git worktree<span class="block text-xs text-muted">{#if canIsolate}The agent works on its own branch, so it cannot collide with other agents.{:else}Unavailable: {isolationReason}.{/if}</span></span></label>{#if isolated}<label class="flex flex-col gap-1 text-sm">Base ref (optional)<input bind:value={baseRef} placeholder="HEAD" class="min-h-11 rounded-md border border-border bg-bg p-2 text-sm" /></label>{/if}<label>First task<textarea bind:value={prompt} class="mt-1 block w-full bg-bg p-2" rows="4"></textarea></label><button disabled={busy} class="rounded bg-accent p-3 text-bg" onclick={create}>Create agent</button></div></div>
