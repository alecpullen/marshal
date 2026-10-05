<script lang="ts">
  import { onMount, untrack } from 'svelte'
  import Card from '../ui/Card.svelte'
  import Button from '../ui/Button.svelte'
  import Segmented from '../ui/Segmented.svelte'
  import { listMemory, listAgents, memorySuggestions, promoteMemory, deleteMemory, setMemoryConfidence, errMessage, type MemoryEntry, type MemoryScope, type MemorySuggestion, type ProjectStatus } from '../api'
  import { dismissKey, loadDismissed, saveDismissed } from '../memory/suggestions'
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
  let scope = $state<'all' | MemoryScope>('all')
  let suggestions = $state<MemorySuggestion[]>([])
  let dismissed = $state<string[]>(loadDismissed())
  let agentIds = $state<Set<string>>(new Set())
  // Every entry seen, so a suggestion can show its text whatever the scope filter is.
  const known = new Map<number, MemoryEntry>()

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
      const r = scope === 'all' ? await listMemory(project) : await listMemory(project, scope)
      unsupported = r === 'unsupported'
      entries = r === 'unsupported' ? [] : r
      for (const e of entries) known.set(e.id, e)
      if (!unsupported) {
        const sg = await memorySuggestions(project).catch(() => [] as MemorySuggestion[])
        suggestions = sg === 'unsupported' ? [] : sg
      }
    } catch (e) {
      error = errMessage(e)
    } finally {
      loaded = true
    }
  }
  $effect(() => {
    void project
    void scope
    untrack(() => void load())
  })
  onMount(() => {
    listAgents()
      .then((a) => (agentIds = new Set(a.map((x) => x.id))))
      .catch(() => {})
  })

  const visible = $derived(suggestions.filter((x) => !dismissed.includes(dismissKey(x))))

  async function promote(x: MemorySuggestion) {
    error = ''
    let scopeKey: string | undefined
    if (x.suggestedScope === 'workspace') {
      scopeKey = window.prompt('Workspace to promote this memory to') ?? ''
      if (!scopeKey) return
    }
    try {
      await promoteMemory(x.memoryId, project, x.suggestedScope as MemoryScope, scopeKey)
      onToast(`Promoted to ${x.suggestedScope}`)
      await load()
    } catch (e) {
      error = errMessage(e)
    }
  }

  function dismiss(x: MemorySuggestion) {
    dismissed = [...dismissed, dismissKey(x)]
    saveDismissed(dismissed)
  }

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

  <Segmented
    label="Memory scope"
    value={scope}
    onchange={(v) => (scope = v as 'all' | MemoryScope)}
    options={[
      { value: 'all', label: 'All' },
      { value: 'project', label: 'Project' },
      { value: 'workspace', label: 'Workspace' },
      { value: 'global', label: 'Global' },
    ]}
  />

  {#if error}<Card class="border-attention text-sm" role="alert">{error}</Card>{/if}

  {#if visible.length}
    <Card class="flex flex-col gap-2 text-sm" data-testid="memory-suggestions">
      <h2 class="text-xs font-medium text-muted">Suggestions</h2>
      {#each visible as x (dismissKey(x))}
        <div class="flex flex-wrap items-center gap-2" data-testid="memory-suggestion">
          <span class="min-w-0 flex-1">{known.get(x.memoryId)?.content ?? `Memory ${x.memoryId}`}
            <span class="text-xs text-muted">· Also learned in {shortName(x.matchProjectRoot)}</span></span>
          <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => promote(x)}>Promote to {x.suggestedScope}</Button>
          <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => dismiss(x)}>Dismiss</Button>
        </div>
      {/each}
    </Card>
  {/if}

  {#if unsupported}
    <p class="text-sm text-muted">This project's memory is managed from its agents in container mode.</p>
  {:else if entries.length === 0}
    {#if loaded}<p class="text-sm text-muted">No memories for this project.</p>{/if}
  {:else}
    <table class="w-full text-left text-sm">
      <thead class="text-xs text-muted">
        <tr><th class="py-1 pr-3">Scope</th><th class="pr-3">Kind</th><th class="pr-3">Content</th><th class="pr-3">Learned in</th><th class="pr-3">Learned by</th><th class="pr-3">Confirmed by</th><th class="pr-3">Confidence</th><th></th></tr>
      </thead>
      <tbody>
        {#each entries as e (e.id)}
          <tr class="border-t border-border align-top" data-testid="memory-row">
            <td class="py-2 pr-3 text-xs" data-testid="memory-scope">{e.scope ?? 'project'}{e.scope === 'workspace' && e.scopeKey ? `: ${e.scopeKey}` : ''}</td>
            <td class="pr-3 font-mono text-xs">{e.kind}</td>
            <td class="pr-3">{e.content}</td>
            <td class="pr-3 text-xs" title={e.learnedProjectRoot}>{e.learnedProjectRoot ? shortName(e.learnedProjectRoot) : ''}</td>
            <td class="pr-3 text-xs">
              {#if e.learnedAgent && agentIds.has(e.learnedAgent)}
                <a class="text-accent underline" href="#chat/{encodeURIComponent(e.learnedAgent)}">{e.learnedAgent}</a>
                {#if e.learnedStep}<a class="ml-1 text-accent underline" href="#chat/{encodeURIComponent(e.learnedAgent)}?node={encodeURIComponent(`step:${e.learnedStep}`)}">step {e.learnedStep}</a>{/if}
              {:else}
                <span class="text-muted">{e.learnedAgent ?? ''}</span>
              {/if}
            </td>
            <td class="pr-3 text-xs" title={(e.confirmedBy ?? []).join(', ')}>{(e.confirmedBy ?? []).length || ''}</td>
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
