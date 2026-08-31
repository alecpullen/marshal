<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import { createSessionStore, transcriptEntries, type Mode } from '../lib/store.js'
  import Composer from '../lib/Composer.svelte'
  import ModeSwitcher from '../lib/ModeSwitcher.svelte'
  import ToolCallCard from '../lib/ToolCallCard.svelte'
  import PermissionModal from '../lib/PermissionModal.svelte'
  import QuestionModal from '../lib/QuestionModal.svelte'
  import ExitPanel from '../lib/ExitPanel.svelte'
  import { renderMarkdown, initHighlighter } from '../lib/markdown'

  interface Props {
    sessionId: string
    onBack: () => void
  }

  let { sessionId, onBack }: Props = $props()

  // Chat instances are keyed by session route, so this store intentionally
  // captures the session ID once for the lifetime of the component.
  // svelte-ignore state_referenced_locally
  const { state: session, actions } = createSessionStore(sessionId, '/')

  // Shiki loads its grammars asynchronously. Messages render unhighlighted
  // until it is ready, then `ready` flips and the transcript re-renders —
  // rather than withholding the transcript behind a loading state.
  let ready = $state(false)

  let transcriptEl = $state<HTMLDivElement | null>(null)
  // Whether the view is following the tail. Scrolling up to read
  // releases it; returning to the bottom re-arms it.
  let pinned = $state(true)

  const entries = $derived(transcriptEntries($session))

  /*
    Streaming appends to the last message's text without changing the
    entry count, so following the tail cannot key off length alone. This
    signature changes on both a new entry and a growing one.
  */
  const tailSignature = $derived(
    entries.length + ':' + ($session.messages.at(-1)?.text.length ?? 0) + ':' + ($session.busy ? 1 : 0),
  )

  const PIN_THRESHOLD_PX = 48

  function atBottom(el: HTMLElement): boolean {
    return el.scrollHeight - el.scrollTop - el.clientHeight <= PIN_THRESHOLD_PX
  }

  function onScroll() {
    if (transcriptEl) pinned = atBottom(transcriptEl)
  }

  function scrollToLatest() {
    if (!transcriptEl) return
    transcriptEl.scrollTop = transcriptEl.scrollHeight
    pinned = true
  }

  $effect(() => {
    // Referenced so the effect re-runs as the tail grows.
    tailSignature
    if (!pinned || !transcriptEl) return
    // After the DOM has taken the new content, not before.
    requestAnimationFrame(() => {
      if (transcriptEl && pinned) transcriptEl.scrollTop = transcriptEl.scrollHeight
    })
  })

  onMount(() => {
    initHighlighter().then(() => (ready = true)).catch(() => {
      // Highlighting is an enhancement. A failure here leaves fenced code
      // as escaped plain blocks, which is still readable.
    })
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

  <div class="transcript" bind:this={transcriptEl} onscroll={onScroll}>
    {#each entries as entry (entry.key)}
      {#if entry.kind === 'message'}
        {@const message = entry.value}
        <div class="message {message.role}">
          <div class="bubble">
            {#if message.reasoning}
              <details class="reasoning">
                <summary>Thought</summary>
                <pre>{message.reasoning}</pre>
              </details>
            {/if}
            {#if message.role === 'user'}
              <!-- The user's own text is shown as typed; rendering it as
                   markdown would reformat their input under them. -->
              <div class="text">{message.text}</div>
            {:else}
              {#key ready}
                <div class="text prose">{@html renderMarkdown(message.text)}</div>
              {/key}
            {/if}
          </div>
        </div>
      {:else}
        <ToolCallCard toolCall={entry.value} />
      {/if}
    {/each}

    {#if entries.length === 0 && !$session.busy}
      <div class="empty">
        <p>No messages yet.</p>
        <p class="hint">Describe a task below to start this agent working.</p>
      </div>
    {/if}

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

  {#if !pinned && entries.length > 0}
    <div class="jump-wrap">
      <button class="jump" onclick={scrollToLatest}>Jump to latest ↓</button>
    </div>
  {/if}

  <Composer busy={$session.busy} onSend={send} onCancel={actions.cancel} />

  <ExitPanel agentId={sessionId} onDone={onBack} />
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
    background: var(--color-surface);
  }
  header {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.75rem 1rem;
    border-bottom: 1px solid var(--color-border);
  }
  .back {
    background: transparent;
    border: none;
    cursor: pointer;
    font: inherit;
    color: var(--color-muted);
  }
  .title {
    flex: 1;
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .connection {
    color: var(--color-danger);
  }
  .connection.connected {
    color: var(--color-running);
  }
  .transcript {
    flex: 1;
    overflow-y: auto;
    padding: 1rem;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }
  /*
    A flex column shrinks its children to fit before it will overflow, and
    the transcript scrolls instead — so nothing in it may be shrinkable.
    Without this, tool call cards collapse to a 2px line as soon as the
    transcript is taller than the viewport: text present, height gone.
  */
  .transcript > :global(*) {
    flex-shrink: 0;
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
    background: var(--color-bg);
    word-break: break-word;
  }
  /*
    pre-wrap belongs to plain text only. Rendered markdown carries its own
    block elements, and pre-wrap on the container doubles their whitespace.
  */
  .text:not(.prose) {
    white-space: pre-wrap;
  }
  .message.user .bubble {
    background: var(--color-accent);
    color: var(--color-bg);
  }
  .reasoning {
    margin-bottom: 0.5rem;
    font-size: 0.85rem;
    color: var(--color-muted);
  }
  .reasoning pre {
    margin: 0.25rem 0 0;
    white-space: pre-wrap;
    font-family: inherit;
  }
  .typing {
    color: var(--color-muted);
    font-style: italic;
  }
  /*
    Sits above the composer rather than floating over the transcript, so
    it never covers the newest message — the thing it exists to reach.
  */
  .jump-wrap {
    display: flex;
    justify-content: center;
    padding: 0 1rem;
    margin-bottom: -0.5rem;
  }
  .jump {
    border: 1px solid var(--color-border);
    background: var(--color-bg);
    color: var(--color-fg);
    border-radius: 999px;
    padding: 0.35rem 0.9rem;
    font: inherit;
    font-size: 0.8125rem;
    cursor: pointer;
    box-shadow: 0 2px 8px rgb(0 0 0 / 0.35);
  }
  .jump:hover {
    background: var(--color-surface);
  }
  .empty {
    margin: auto;
    text-align: center;
    color: var(--color-muted);
  }
  .empty p {
    margin: 0;
  }
  .empty .hint {
    margin-top: 0.25rem;
    font-size: 0.875rem;
    opacity: 0.75;
  }

  /*
    Markdown styling for agent output. Kept tight rather than airy: a
    transcript is read in sequence, so generous vertical rhythm costs more
    than it buys. :global is required because this HTML is injected with
    {@html} and never passes through Svelte's style scoping.
  */
  .prose :global(> :first-child) {
    margin-top: 0;
  }
  .prose :global(> :last-child) {
    margin-bottom: 0;
  }
  .prose :global(p) {
    margin: 0.5rem 0;
  }
  .prose :global(h1),
  .prose :global(h2),
  .prose :global(h3),
  .prose :global(h4) {
    margin: 1rem 0 0.5rem;
    font-weight: 600;
    line-height: 1.3;
  }
  .prose :global(h1) {
    font-size: 1.25rem;
  }
  .prose :global(h2) {
    font-size: 1.125rem;
  }
  .prose :global(h3),
  .prose :global(h4) {
    font-size: 1rem;
  }
  /*
    Tailwind's preflight sets list-style: none on ul/ol, so markdown lists
    render as unmarked lines unless the marker is restored here. An agent's
    numbered steps losing their numbers is a real loss of meaning.
  */
  .prose :global(ul),
  .prose :global(ol) {
    margin: 0.5rem 0;
    padding-left: 1.5rem;
  }
  .prose :global(ul) {
    list-style: disc;
  }
  .prose :global(ol) {
    list-style: decimal;
  }
  .prose :global(li) {
    margin: 0.125rem 0;
  }
  .prose :global(a) {
    color: var(--color-accent);
    text-decoration: underline;
  }
  .prose :global(code) {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.875em;
    background: var(--color-surface);
    padding: 0.1em 0.35em;
    border-radius: 4px;
  }
  /* Shiki paints its own background and colours on the pre it emits. */
  .prose :global(pre) {
    margin: 0.75rem 0;
    padding: 0.75rem 1rem;
    border-radius: 8px;
    border: 1px solid var(--color-border);
    overflow-x: auto;
  }
  .prose :global(pre code) {
    background: none;
    padding: 0;
    border-radius: 0;
    font-size: 0.8125rem;
    line-height: 1.5;
  }
  .prose :global(pre.shiki-fallback) {
    background: var(--color-surface);
  }
  .prose :global(blockquote) {
    margin: 0.5rem 0;
    padding-left: 0.75rem;
    border-left: 2px solid var(--color-border);
    color: var(--color-muted);
  }
  .prose :global(table) {
    border-collapse: collapse;
    margin: 0.5rem 0;
    font-size: 0.875rem;
    display: block;
    overflow-x: auto;
  }
  .prose :global(th),
  .prose :global(td) {
    border: 1px solid var(--color-border);
    padding: 0.25rem 0.5rem;
    text-align: left;
  }
  .prose :global(hr) {
    border: none;
    border-top: 1px solid var(--color-border);
    margin: 0.75rem 0;
  }
  .error-banner {
    background: color-mix(in oklch, var(--color-danger) 18%, var(--color-bg));
    color: var(--color-danger);
    padding: 0.75rem;
    border-radius: 6px;
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .error-banner button {
    background: var(--color-surface);
    border: 1px solid var(--color-danger);
    border-radius: 4px;
    cursor: pointer;
  }
</style>