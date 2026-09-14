<script lang="ts">
  import { onMount } from 'svelte'
  import Card from './ui/Card.svelte'
  import Button from './ui/Button.svelte'
  import Badge from './ui/Badge.svelte'
  import { listProjects, listSessions, deleteSession, errMessage, type ProjectStatus, type SessionSummary } from './api'
  import { shortName } from './utils'

  /*
    Resume is pure navigation: the SPA already routes `#chat/<id>`, and
    Chat.svelte mounts and calls its store's load() (which is
    api.loadSession) itself. The panel performs no session-load API call.
  */
  interface Props {
    project?: string
  }
  let { project = '' }: Props = $props()

  let projects = $state<ProjectStatus[]>([])
  /* root seeds from the prop but is then owned by the picker — picking
     or going back must not be undone by a re-render, so the initial
     capture is deliberate. */
  // svelte-ignore state_referenced_locally
  let root = $state(project)
  let sessions = $state<SessionSummary[]>([])
  let loading = $state(false)
  let error = $state<string | null>(null)
  /* Inline two-step delete: only one row arms a confirmation at a
     time, and Cancel disarms without a round-trip. */
  let confirmingId = $state<string | null>(null)

  /* The bridge passes the child agent's session/list JSON through
     verbatim, so every field but sessionId is read defensively. A
     non-parseable `updated` renders as the raw string; a missing one
     renders nothing. */
  function formatUpdated(raw: string | undefined): string {
    if (!raw) return ''
    const d = new Date(raw)
    if (isNaN(d.getTime())) return raw
    return d.toLocaleString()
  }

  async function loadProjects() {
    loading = true
    error = null
    try {
      projects = await listProjects()
    } catch (e) {
      error = errMessage(e)
    } finally {
      loading = false
    }
  }

  async function loadSessions() {
    loading = true
    error = null
    try {
      sessions = await listSessions(root)
    } catch (e) {
      error = errMessage(e)
    } finally {
      loading = false
    }
  }

  function pick(p: ProjectStatus) {
    root = p.root
    confirmingId = null
    loadSessions()
  }

  function resume(id: string) {
    window.location.hash = '#chat/' + encodeURIComponent(id)
  }

  async function remove(id: string) {
    error = null
    confirmingId = null
    try {
      await deleteSession(id)
      await loadSessions()
    } catch (e) {
      error = errMessage(e)
    }
  }

  /* Pickers and session lists are two different fetches, so the mount
     effect depends on which one is needed; a later `project` prop change
     re-runs the session list. */
  onMount(() => {
    if (root) loadSessions()
    else loadProjects()
  })

  /* A parent (routing unit) may set the project prop after mount. */
  $effect(() => {
    if (project && project !== root) {
      root = project
      confirmingId = null
      loadSessions()
    }
  })
</script>

<Card>
  {#if !root}
    <div class="mb-4 flex items-center justify-between">
      <h2 class="text-sm font-semibold">Sessions</h2>
      <Button variant="ghost" onclick={loadProjects} disabled={loading}>Refresh</Button>
    </div>

    {#if error}
      <div class="mb-3 rounded-md border border-danger bg-danger/10 p-3 text-sm text-danger">{error}</div>
    {/if}

    {#if projects.length === 0 && !loading}
      <p class="text-sm text-muted">No projects registered.</p>
    {/if}

    <ul class="flex flex-col gap-2">
      {#each projects as p (p.root)}
        <li>
          <button
            type="button"
            class="w-full rounded-md border border-border bg-bg px-3 py-2 text-left text-sm hover:border-accent"
            title={p.root}
            onclick={() => pick(p)}
          >
            {shortName(p.root)}
            {#if !p.available}
              <span class="text-danger">— unavailable</span>
            {/if}
          </button>
        </li>
      {/each}
    </ul>
  {:else}
    <div class="mb-4 flex items-center justify-between">
      <h2 class="text-sm font-semibold">Sessions — {shortName(root)}</h2>
      <Button variant="ghost" onclick={loadSessions} disabled={loading}>Refresh</Button>
    </div>

    {#if error}
      <div class="mb-3 rounded-md border border-danger bg-danger/10 p-3 text-sm text-danger">{error}</div>
    {/if}

    <p class="mb-3 text-xs text-muted">Only the project's live agent session can be resumed; other sessions are historical.</p>

    {#if !project}
      <div class="mb-4">
        <Button variant="ghost" onclick={() => { root = ''; sessions = []; confirmingId = null }}>← All projects</Button>
      </div>
    {/if}

    {#if sessions.length === 0 && !loading}
      <p class="text-sm text-muted">No sessions for this project.</p>
    {/if}

    <ul class="flex flex-col gap-2">
      {#each sessions as s (s.sessionId)}
        <li class="flex items-center justify-between gap-3 border-t border-border pt-2 first:border-0 first:pt-0">
          <div class="min-w-0">
            <div class="truncate text-sm font-medium">{s.title || s.sessionId}</div>
            <div class="mt-1 flex flex-wrap gap-2 text-xs text-muted">
              {#if formatUpdated(s.updated)}
                <span>{formatUpdated(s.updated)}</span>
              {/if}
              {#if s.messageCount !== undefined}
                <Badge tone="muted">{s.messageCount} message{s.messageCount === 1 ? '' : 's'}</Badge>
              {/if}
            </div>
          </div>
          <div class="flex shrink-0 items-center gap-2">
            <Button variant="ghost" onclick={() => resume(s.sessionId)}>Resume</Button>
            {#if confirmingId === s.sessionId}
              <Button variant="danger" onclick={() => remove(s.sessionId)}>Confirm delete</Button>
              <Button variant="ghost" onclick={() => (confirmingId = null)}>Cancel</Button>
            {:else}
              <Button variant="ghost" onclick={() => (confirmingId = s.sessionId)}>Delete</Button>
            {/if}
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</Card>
