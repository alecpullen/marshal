<script lang="ts">
  import AgentCard from '../AgentCard.svelte'
  import { sortAttentionFirst, type AgentRow } from '../fleet'

  /**
   * The agents no factory item holds. Ordering is the same attention-first
   * order the fleet uses everywhere, so a parked agent stays at the top.
   *
   * Which agents are independent is decided by the caller (C2
   * `factoryWorkerIds`): this list never guesses, and it renders nothing when
   * there is nothing to show.
   */
  export interface IndependentAgentListProps {
    agents: AgentRow[]
    onOpen: (id: string) => void
  }

  let { agents, onOpen }: IndependentAgentListProps = $props()

  const sorted = $derived(sortAttentionFirst(agents))
</script>

{#if sorted.length > 0}
  <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3" data-testid="independent-agents">
    {#each sorted as a (a.id)}
      <AgentCard agent={a} {onOpen} />
    {/each}
  </div>
{/if}
