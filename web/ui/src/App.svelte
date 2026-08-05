<script lang="ts">
  import { onMount } from 'svelte'
  import SessionList from './views/SessionList.svelte'
  import Chat from './views/Chat.svelte'

  let hash = $state('#')

  onMount(() => {
    const update = () => (hash = window.location.hash || '#')
    window.addEventListener('hashchange', update)
    update()
    return () => window.removeEventListener('hashchange', update)
  })

  const chatMatch = $derived(/^#chat\/(.+)$/.exec(hash))
  const chatSessionId = $derived(chatMatch?.[1] ?? null)
</script>

<main>
  {#if chatSessionId}
    <Chat sessionId={chatSessionId} onBack={() => (window.location.hash = '#')} />
  {:else}
    <SessionList />
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