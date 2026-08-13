<script lang="ts">
  import { onMount } from 'svelte'
  import Dashboard from './views/Dashboard.svelte'
  import NewAgent from './views/NewAgent.svelte'
  import Chat from './views/Chat.svelte'
  import { connectFleetSSE } from './lib/sse'

  let hash = $state('#')

  onMount(() => {
    const update = () => (hash = window.location.hash || '#')
    window.addEventListener('hashchange', update)
    update()
    const disconnect = connectFleetSSE({
      onDelta: (d) => { if (d.kind === 'project_removed') window.location.hash = '#' },
      onOverflow: () => {},
    })
    return () => { window.removeEventListener('hashchange', update); disconnect() }
  })

  const chatMatch = $derived(/^#chat\/(.+)$/.exec(hash))
  const chatSessionId = $derived(chatMatch?.[1] ?? null)
  const isNew = $derived(hash === '#new')
</script>

<main>
  {#if chatSessionId}
    <Chat sessionId={chatSessionId} onBack={() => (window.location.hash = '#')} />
  {:else if isNew}
    <NewAgent onDone={(id) => (window.location.hash = id ? `#chat/${id}` : '#')} />
  {:else}
    <Dashboard onOpenAgent={(id) => (window.location.hash = `#chat/${id}`)} onNewAgent={() => (window.location.hash = '#new')} />
  {/if}
</main>

<style>
  :global(*) {
    box-sizing: border-box;
  }
  :global(body) {
    margin: 0;
    font-family: system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
    background: #f7f7f8;
    color: #111;
  }
  main {
    min-height: 100vh;
  }
</style>