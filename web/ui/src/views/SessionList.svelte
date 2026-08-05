<script lang="ts">
  import { onMount } from 'svelte'
  import * as api from '../lib/api.js'

  interface Session {
    sessionId: string
    title?: string
    updated?: string
    messageCount?: number
  }

  let sessions = $state<Session[]>([])
  let cwdRoot = $state<string>('')
  let loading = $state(true)
  let error = $state<string | null>(null)

  async function refresh() {
    if (!cwdRoot) return
    try {
      sessions = (await api.listSessions(cwdRoot)) as Session[]
      error = null
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    }
  }

  async function createSession() {
    if (!cwdRoot) return
    try {
      const res = await api.newSession(cwdRoot)
      window.location.hash = `#chat/${encodeURIComponent(res.sessionId)}`
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    }
  }

  async function loadSession(id: string) {
    window.location.hash = `#chat/${encodeURIComponent(id)}`
  }

  async function removeSession(id: string) {
    if (!confirm('Delete this session?')) return
    try {
      await api.deleteSession(id)
      await refresh()
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    }
  }

  onMount(async () => {
    try {
      const cfg = await api.getConfig()
      cwdRoot = cfg.cwdRoot
      await refresh()
    } catch (e) {
      error = e instanceof Error ? e.message : String(e)
    } finally {
      loading = false
    }
  })
</script>

<div class="session-list">
  <header>
    <h1>Marshal Sessions</h1>
    <button onclick={createSession}>New session</button>
  </header>

  {#if error}
    <div class="error">{error}</div>
  {/if}

  {#if loading}
    <p>Loading sessions…</p>
  {:else if sessions.length === 0}
    <p class="empty">No sessions yet for {cwdRoot || 'this workspace'}.</p>
  {:else}
    <ul>
      {#each sessions as session (session.sessionId)}
        <li>
          <button class="session" onclick={() => loadSession(session.sessionId)}>
            <span class="title">{session.title || session.sessionId}</span>
            <span class="meta">{session.messageCount ?? 0} messages · {session.updated || 'unknown'}</span>
          </button>
          <button class="danger" onclick={() => removeSession(session.sessionId)}>Delete</button>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .session-list {
    max-width: 720px;
    margin: 0 auto;
    padding: 1.5rem;
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 1rem;
  }
  h1 {
    margin: 0;
    font-size: 1.5rem;
  }
  button {
    cursor: pointer;
    padding: 0.5rem 0.75rem;
    border: 1px solid #ccc;
    border-radius: 6px;
    background: white;
    font: inherit;
  }
  button:hover {
    background: #f0f0f0;
  }
  ul {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  li {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    margin-bottom: 0.5rem;
  }
  .session {
    flex: 1;
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    text-align: left;
  }
  .title {
    font-weight: 500;
  }
  .meta {
    font-size: 0.85rem;
    color: #666;
  }
  .danger {
    color: #b91c1c;
    border-color: #fca5a5;
  }
  .danger:hover {
    background: #fee2e2;
  }
  .error {
    color: #b91c1c;
    background: #fee2e2;
    padding: 0.75rem;
    border-radius: 6px;
    margin-bottom: 1rem;
  }
  .empty {
    color: #666;
  }
</style>