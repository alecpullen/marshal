<script lang="ts">
  import Modal from '../ui/Modal.svelte'
  import Button from '../ui/Button.svelte'
  import { createWorkspace, listProjects, errMessage, APIError, type ProjectStatus, type WorkspaceFrom } from '../api'
  import type { AgentRow } from '../fleet'
  import { shortName } from '../utils'
  import { NAME_RE, STARTERS } from './model'

  let { agents, onCreated, onClose }: { agents: AgentRow[]; onCreated: (name: string) => void; onClose: () => void } = $props()

  type Kind = 'blank' | 'starter' | 'devcontainer' | 'snapshot'
  let name = $state('')
  let kind = $state<Kind>('blank')
  let starter = $state<string>(STARTERS[0].id)
  let project = $state('')
  let agentId = $state('')
  let projects = $state<ProjectStatus[]>([])
  let busy = $state(false)
  let error = $state('')

  $effect(() => {
    void listProjects().then(
      (p) => (projects = p),
      () => (projects = []),
    )
  })

  const nameOk = $derived(NAME_RE.test(name))
  const from = $derived<WorkspaceFrom | null>(
    kind === 'blank' ? 'blank' : kind === 'starter' ? `starter:${starter}` : kind === 'devcontainer' ? (project ? `devcontainer:${project}` : null) : agentId ? `snapshot:${agentId}` : null,
  )

  async function create() {
    if (!nameOk || !from) return
    busy = true
    error = ''
    try {
      await createWorkspace(name, from)
      onCreated(name)
    } catch (e) {
      // The bridge's refusal text (a devcontainer `build`, a non-container agent) is the useful part.
      const body = e instanceof APIError ? (e.body as { error?: string; message?: string } | undefined) : undefined
      error = body?.message ?? body?.error ?? errMessage(e)
    } finally {
      busy = false
    }
  }
</script>

<Modal title="New workspace" description="A template agents run in: base image, toolchains, packages, mounts and network." onDismiss={onClose}>
  <label class="flex flex-col gap-1 text-xs text-muted">
    Name
    <input class="rounded-md border border-border bg-bg px-2 py-1.5 text-sm text-fg" placeholder="go-service" bind:value={name} aria-label="Workspace name" />
  </label>
  {#if name && !nameOk}
    <p class="text-xs text-warn">Lowercase letters, digits and dashes; up to 41 characters, starting with a letter or digit.</p>
  {/if}

  <fieldset class="flex flex-col gap-2 text-sm">
    <legend class="mb-1 text-xs text-muted">Start from</legend>
    <label class="flex items-center gap-2"><input type="radio" bind:group={kind} value="blank" /> Blank</label>
    <label class="flex items-center gap-2"><input type="radio" bind:group={kind} value="starter" /> A starter</label>
    {#if kind === 'starter'}
      <div class="grid grid-cols-2 gap-2 pl-6 sm:grid-cols-3" role="group" aria-label="Starters">
        {#each STARTERS as s (s.id)}
          <button
            type="button"
            class="rounded-md border px-2 py-1.5 text-left text-xs {starter === s.id ? 'border-accent bg-raise' : 'border-border hover:bg-hover'}"
            aria-pressed={starter === s.id}
            onclick={() => (starter = s.id)}>{s.label}</button
          >
        {/each}
      </div>
    {/if}
    <label class="flex items-center gap-2"><input type="radio" bind:group={kind} value="devcontainer" /> Import devcontainer.json</label>
    {#if kind === 'devcontainer'}
      <select class="ml-6 rounded-md border border-border bg-bg px-2 py-1.5 text-sm" bind:value={project} aria-label="Project">
        <option value="">Pick a project</option>
        {#each projects as p (p.root)}<option value={p.root}>{shortName(p.root)}</option>{/each}
      </select>
    {/if}
    <label class="flex items-center gap-2"><input type="radio" bind:group={kind} value="snapshot" /> Snapshot a running agent</label>
    {#if kind === 'snapshot'}
      <select class="ml-6 rounded-md border border-border bg-bg px-2 py-1.5 text-sm" bind:value={agentId} aria-label="Agent">
        <option value="">Pick a container agent</option>
        {#each agents as a (a.id)}<option value={a.id}>{a.name || a.id}</option>{/each}
      </select>
    {/if}
  </fieldset>

  {#if error}<p role="alert" class="text-sm text-err">{error}</p>{/if}

  {#snippet footer()}
    <Button variant="ghost" onclick={onClose}>Cancel</Button>
    <Button onclick={create} disabled={busy || !nameOk || !from}>{busy ? 'Creating…' : 'Create'}</Button>
  {/snippet}
</Modal>
