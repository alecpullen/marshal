<script lang="ts">
  import Button from '../ui/Button.svelte'
  import Card from '../ui/Card.svelte'
  import type { FactoryCommand, FactoryCreate, FactoryItem } from '../factory'

  /**
   * What a decision or recovery panel hands back with a command kind (design
   * C5). The caller owns request identity, the expected version and the
   * decision ID; a panel supplies only the fields its own controls filled in.
   */
  export interface FactoryCommandInput {
    answer?: string
    reason?: string
    additionalRounds?: number
    replacement?: FactoryCreate
    implementerRouting?: Record<string, unknown>
    reviewerRouting?: Record<string, unknown>
    confirmPreviousWorkerStopped?: boolean
  }

  export type RunFactoryCommand = (kind: FactoryCommand['kind'], input?: FactoryCommandInput) => Promise<void>

  export interface DecisionPanelProps {
    item: FactoryItem
    busy: boolean
    run: RunFactoryCommand
  }

  /**
   * A decision that asks the person a question (design C5): the factory's own
   * question and reason, then the one answer the API can record.
   *
   * The answer lives here, not in the caller, and it is deliberately not keyed
   * to `item.version`: a poll or a stale-version refresh redraws the decision
   * in place, so what someone typed survives it. A decision kind this client
   * cannot act on keeps its question and reason and is given no control —
   * every rendered action must be one the bridge actually executes.
   */
  let { item, busy, run }: DecisionPanelProps = $props()

  const decision = $derived(item.decision)
  const asks = $derived(decision?.kind === 'question')

  let answer = $state('')
</script>

{#if decision}
  <Card class="grid gap-3 border-attention" data-testid="factory-decision">
    <h2 class="font-semibold">Action needed: {decision.kind}</h2>
    {#if decision.question}<p class="whitespace-pre-wrap break-words">{decision.question}</p>{/if}
    {#if decision.reason}<p class="whitespace-pre-wrap break-words text-sm text-muted">{decision.reason}</p>{/if}
    {#if asks}
      <label class="grid gap-1 text-sm">Answer <textarea class="min-h-24 rounded border border-border bg-bg p-2" bind:value={answer}></textarea></label>
      <div><Button disabled={busy || !answer.trim()} onclick={() => void run('answer', { answer })}>Send answer</Button></div>
    {/if}
  </Card>
{/if}
