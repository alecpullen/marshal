<script lang="ts">
  import TerminalTab from './TerminalTab.svelte'
  import PreviewTab from './PreviewTab.svelte'
  import type { AgentStatus } from '../api'
  import type { StackState } from '../stack'
  import type { createNodeCache } from './nodeCache'
  import type { DockState } from './dock'

  /*
    The Terminal and Preview panels. Once visited they stay mounted and are
    only hidden, because leaving the tab must not close the shell or reload the
    preview.
  */
  let {
    agentId,
    sessionId,
    subagentId,
    stack,
    dock,
    cache,
    row,
    onTerminalOutput,
  }: {
    agentId: string
    sessionId: string
    subagentId?: number
    stack: StackState
    dock: DockState
    cache: ReturnType<typeof createNodeCache>
    row?: (AgentStatus & { held?: boolean }) | null
    onTerminalOutput: () => void
  } = $props()

  let seenTerminal = $state(false)
  let seenPreview = $state(false)
  $effect(() => {
    if (dock.tab === 'terminal') seenTerminal = true
    if (dock.tab === 'preview') seenPreview = true
  })
</script>

{#if seenTerminal}
  <div class="h-full" hidden={dock.tab !== 'terminal'}>
    <TerminalTab {agentId} {sessionId} {subagentId} {stack} {dock} {cache} held={row?.held ?? false} onOutput={onTerminalOutput} />
  </div>
{/if}
{#if seenPreview}
  <div class="h-full" hidden={dock.tab !== 'preview'}>
    <PreviewTab {agentId} workspace={row?.workspace} />
  </div>
{/if}
