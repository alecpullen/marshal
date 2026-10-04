<script lang="ts">
  import Self from './TurnNode.svelte'
  import type { TranscriptCtx } from './ctx'
  import { effective, visible } from './density'
  import TaskRow from './TaskRow.svelte'
  import StepHeader from './StepHeader.svelte'
  import ToolRow from './ToolRow.svelte'
  import MessageNode from './MessageNode.svelte'
  import ThinkingRow from './ThinkingRow.svelte'
  import SubagentCard from './SubagentCard.svelte'
  import RunEventRow from './RunEventRow.svelte'
  import JobExitRow from './JobExitRow.svelte'
  import ReceiptLine from './ReceiptLine.svelte'

  let { id, ctx }: { id: string; ctx: TranscriptCtx } = $props()

  const node = $derived(ctx.nodes.get(id))
  const density = $derived(effective(id, ctx.overrides, (n) => ctx.nodes.get(n)?.parent, ctx.global))
  const isCursor = $derived(ctx.cursor === id)
  const folded = $derived(
    !!node?.task && ctx.foldTasks && node.task.status === 'completed' && !node.task.unresolvedFailure && !node.live && !ctx.unfolded.has(id),
  )
  const children = $derived(node?.children ?? [])
</script>

{#snippet kids()}
  {#each children as c (c)}
    <Self id={c} {ctx} />
  {/each}
{/snippet}

{#if node && visible(node.kind, density)}
  {#if node.kind === 'turn' || node.kind === 'passthrough'}
    <section data-node-id={id} class="flex flex-col {node.kind === 'turn' ? 'pb-4' : ''}">
      {@render kids()}
    </section>
  {:else if node.kind === 'task' && node.task}
    <div data-node-id={id} class={isCursor ? 'border-l-2 border-violet pl-2 -ml-2.5' : ''}>
      <TaskRow task={node.task} {folded} live={!!node.live} now={ctx.now} onToggle={() => ctx.onToggleFold?.(id)} />
    </div>
    {#if !folded}<div class="ml-2">{@render kids()}</div>{/if}
  {:else if node.kind === 'step' && node.step}
    <div data-node-id={id} class={isCursor ? 'border-l-2 border-violet pl-2 -ml-2.5' : ''}>
      <StepHeader step={node.step} live={!!node.live} {density} now={ctx.now} />
    </div>
    <div class="ml-5">{@render kids()}</div>
  {:else if node.kind === 'tool' && node.tool}
    <div data-node-id={id} class="ml-5 {isCursor ? 'border-l-2 border-violet pl-2 -ml-2.5' : ''}">
      <ToolRow tool={node.tool} {density} now={ctx.now} />
    </div>
  {:else if (node.kind === 'message' || node.kind === 'final') && node.message}
    <div data-node-id={id} class={isCursor ? 'border-l-2 border-violet pl-2 -ml-2.5' : ''}>
      <MessageNode message={node.message} />
    </div>
  {:else if node.kind === 'thinking' && node.thinking}
    <div data-node-id={id} class={isCursor ? 'border-l-2 border-violet pl-2' : ''}>
      <ThinkingRow thinking={node.thinking} live={!!node.live} open={density === 'full'} />
    </div>
  {:else if node.kind === 'subagent' && node.subagent}
    <div data-node-id={id}><SubagentCard sub={node.subagent} /></div>
  {:else if node.kind === 'runEvent' && node.runEvent}
    <div data-node-id={id}><RunEventRow event={node.runEvent} /></div>
  {:else if node.kind === 'jobExit' && node.jobExit}
    <div data-node-id={id}><JobExitRow job={node.jobExit} open={density === 'full'} /></div>
  {:else if node.kind === 'receipt' && node.receipt}
    <div data-node-id={id}><ReceiptLine receipt={node.receipt} /></div>
  {:else}
    {@render kids()}
  {/if}
{/if}
