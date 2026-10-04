<script lang="ts">
  import { untrack } from 'svelte'
  import Card from '../ui/Card.svelte'
  import Button from '../ui/Button.svelte'
  import { listMemory, deleteMemory, setMemoryConfidence, errMessage, type MemoryEntry, type ProjectStatus } from '../api'
  import { shortName } from '../utils'

  let {
    projects,
    project: initialProject = '',
    onProject,
    onToast,
  }: { projects: ProjectStatus[]; project?: string; onProject: (root: string) => void; onToast: (t: string) => void } = $props()

  const CONFIDENCE = ['tentative', 'confirmed', 'stale']

  // svelte-ignore state_referenced_locally
  let project = $state(initialProject)
  let entries = $state<MemoryEntry[]>([])
  let unsupported = $state(false)
  let error = $state('')
  let loaded = $state(false)
  let deleting = $state<number | null>(null)

  $effect(() => {
    if (!project) project = projects.find((p) => p.available)?.root ?? ''
  })

  async function load() {
    error = ''
    if (!project) {
      entries = []
      loaded = true
      return
    }
    try {
      const r = await listMemory(project)
      unsupported = r === 'unsupported'
      entries = r === 'unsupported' ? [] : r
    } catch (e) {
      error = errMessage(e)
    } finally {
      loaded = true
    }
  }
  $effect(() => {
    void project
    untrack(() => void load())
  })

  async function setConfidence(e: MemoryEntry, confidence: string) {
    error = ''
    try {
      await setMemoryConfidence(e.id, project, confidence)
      entries = entries.map((x) => (x.id === e.id ? { ...x, confidence } : x))
    } catch (err) {
      error = errMessage(err)
      await load()
    }
  }

  async function remove(id: number) {
    deleting = null
    error = ''
    try {
      await deleteMemory(id, project)
      entries = entries.filter((x) => x.id !== id)
      onToast('Deleted memory')
    } catch (e) {
      error = errMessage(e)
    }
  }
</script>

<div class="flex flex-col gap-4">
  <select
    aria-label="Project"
    class="w-fit rounded border border-border bg-bg px-2 py-1.5 text-sm"
    value={project}
    onchange={(e) => {
      project = e.currentTarget.value
      onProject(project)
    }}
  >
    {#each projects.filter((p) => p.available) as p (p.root)}
      <option value={p.root}>{shortName(p.root)}</option>
    {/each}
  </select>

  {#if error}<Card class="border-attention text-sm" role="alert">{error}</Card>{/if}

  {#if unsupported}
    <p class="text-sm text-muted">This project's memory is managed from its agents in container mode.</p>
  {:else if entries.length === 0}
    {#if loaded}<p class="text-sm text-muted">No memories for this project.</p>{/if}
  {:else}
    <table class="w-full text-left text-sm">
      <thead class="text-xs text-muted">
        <tr><th class="py-1 pr-3">Kind</th><th class="pr-3">Content</th><th class="pr-3">Confidence</th><th class="pr-3">Source session</th><th></th></tr>
      </thead>
      <tbody>
        {#each entries as e (e.id)}
          <tr class="border-t border-border align-top" data-testid="memory-row">
            <td class="py-2 pr-3 font-mono text-xs">{e.kind}</td>
            <td class="pr-3">{e.content}</td>
            <td class="pr-3">
              <select
                aria-label="Confidence for memory {e.id}"
                class="rounded border border-border bg-bg px-1.5 py-1 text-xs"
                value={e.confidence}
                onchange={(ev) => setConfidence(e, ev.currentTarget.value)}
              >
                {#each CONFIDENCE as c (c)}<option value={c}>{c}</option>{/each}
              </select>
            </td>
            <td class="pr-3 font-mono text-xs text-muted">{e.sourceSessionId ?? ''}</td>
            <td class="text-right whitespace-nowrap">
              {#if deleting === e.id}
                <Button variant="danger" class="min-h-8 px-2 py-1 text-xs" onclick={() => remove(e.id)}>Confirm delete</Button>
                <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => (deleting = null)}>Cancel</Button>
              {:else}
                <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => (deleting = e.id)}>Delete</Button>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>
