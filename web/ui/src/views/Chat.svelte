<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import { createSessionStore, type Mode } from '../lib/store.js'
  import Composer from '../lib/Composer.svelte'
  import ModeSwitcher from '../lib/ModeSwitcher.svelte'
  import ToolCallCard from '../lib/ToolCallCard.svelte'
  import PermissionModal from '../lib/PermissionModal.svelte'
  import QuestionModal from '../lib/QuestionModal.svelte'

  interface Props {
    sessionId: string
    onBack: () => void
  }

  let { sessionId, onBack }: Props = $props()

  // Chat instances are keyed by session route, so this store intentionally
  // captures the session ID once for the lifetime of the component.
  // svelte-ignore state_referenced_locally
  const { state: session, actions } = createSessionStore(sessionId, '/')

  onMount(() => {
    actions.connect()
    actions.load().catch(() => {
      // load failures are surfaced via the session error if severe; the SSE
      // stream will still deliver live events.
    })
  })

  onDestroy(() => {
    actions.disconnect()
  })

  async function send(text: string) {
    if ($session.busy) {
      await actions.steer(text)
    } else {
      await actions.prompt(text)
    }
  }

  async function changeMode(mode: Mode) {
    await actions.setMode(mode)
  }
</script>

<div class="chat">
  <header>
    <button class="back" onclick={onBack}>← Sessions</button>
    <div class="title">{sessionId}</div>
    <ModeSwitcher mode={$session.mode} onChange={changeMode} />
    <span class="connection" class:connected={$session.connected} title={$session.connected ? 'connected' : 'disconnected'}>
      {$session.connected ? '●' : '○'}
    </span>
  </header>

  <div class="transcript">
    {#each $session.messages as message (message.id)}
      <div class="message {message.role}">
        <div class="bubble">
          {#if message.reasoning}
            <details class="reasoning">
              <summary>Thought</summary>
              <pre>{message.reasoning}</pre>
            </details>
          {/if}
          <div class="text">{message.text}</div>
        </div>
      </div>
    {/each}

    {#each $session.toolCalls as tc (tc.id)}
      <ToolCallCard toolCall={tc} />
    {/each}

    {#if $session.busy}
      <div class="typing">Marshal is working…</div>
    {/if}

    {#if $session.error}
      <div class="error-banner">
        {$session.error}
        <button onclick={() => actions.dismissError()}>Dismiss</button>
      </div>
    {/if}
  </div>

  <Composer busy={$session.busy} onSend={send} onCancel={actions.cancel} />
</div>

{#if $session.pendingPermission}
  <PermissionModal permission={$session.pendingPermission} onResolve={actions.resolvePermission} onDeny={() => actions.resolvePermission({ approved: false })} />
{/if}

{#if $session.pendingQuestion}
  <QuestionModal question={$session.pendingQuestion} onResolve={actions.resolveQuestion} onDecline={() => actions.resolveQuestion({ declined: true })} />
{/if}

<style>
  .chat {
    display: flex;
    flex-direction: column;
    height: 100vh;
    max-width: 960px;
    margin: 0 auto;
    background: white;
  }
  header {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.75rem 1rem;
    border-bottom: 1px solid #e5e5e5;
  }
  .back {
    background: transparent;
    border: none;
    cursor: pointer;
    font: inherit;
    color: #555;
  }
  .title {
    flex: 1;
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .connection {
    color: #ef4444;
  }
  .connection.connected {
    color: #22c55e;
  }
  .transcript {
    flex: 1;
    overflow-y: auto;
    padding: 1rem;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }
  .message {
    display: flex;
  }
  .message.user {
    justify-content: flex-end;
  }
  .message.assistant {
    justify-content: flex-start;
  }
  .bubble {
    max-width: 80%;
    padding: 0.75rem 1rem;
    border-radius: 12px;
    background: #f3f4f6;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .message.user .bubble {
    background: #2563eb;
    color: white;
  }
  .reasoning {
    margin-bottom: 0.5rem;
    font-size: 0.85rem;
    color: #666;
  }
  .reasoning pre {
    margin: 0.25rem 0 0;
    white-space: pre-wrap;
    font-family: inherit;
  }
  .typing {
    color: #666;
    font-style: italic;
  }
  .error-banner {
    background: #fee2e2;
    color: #991b1b;
    padding: 0.75rem;
    border-radius: 6px;
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .error-banner button {
    background: white;
    border: 1px solid #fca5a5;
    border-radius: 4px;
    cursor: pointer;
  }
</style>