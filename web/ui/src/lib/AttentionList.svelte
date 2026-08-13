<script lang="ts">
  import Card from './ui/Card.svelte'
  import Button from './ui/Button.svelte'
  import PermissionModal from './PermissionModal.svelte'
  import QuestionModal from './QuestionModal.svelte'
  import { APIError, resolvePermission, resolveQuestion, type Answers, type Decision } from './api'
  import { describePending, toPendingPermission, toPendingQuestion, type AgentRow } from './fleet'

  let {
    agents,
    onOpen,
    onResolved,
  }: {
    agents: AgentRow[]
    onOpen: (id: string) => void
    /** Called after any resolution so the dashboard refetches the snapshot. */
    onResolved: () => void
  } = $props()

  // The agent whose decision is open, if any.
  let deciding = $state<AgentRow | null>(null)
  let notice = $state<string | null>(null)

  const waiting = $derived(agents.filter((a) => a.pending != null))

  const decidingPermission = $derived(
    deciding?.pending?.kind === 'approval' ? toPendingPermission(deciding.pending) : null,
  )
  const decidingQuestion = $derived(
    deciding?.pending?.kind === 'question' ? toPendingQuestion(deciding.id, deciding.pending) : null,
  )

  /**
   * Run a resolution, translating the one error that is not really an
   * error: 410 Gone means the request was already resolved or the agent
   * was cancelled, so the right response is to refresh, not to alarm.
   */
  async function resolve(action: () => Promise<void>) {
    const agent = deciding
    deciding = null
    notice = null
    try {
      await action()
    } catch (e) {
      if (e instanceof APIError && e.status === 410) {
        notice = `${agent?.name || agent?.id} was already resolved elsewhere.`
      } else {
        notice = e instanceof Error ? e.message : String(e)
      }
    } finally {
      onResolved()
    }
  }

  const approve = (id: string, d: Decision) => resolve(() => resolvePermission(id, d))
  const deny = (id: string) => resolve(() => resolvePermission(id, { approved: false }))
  const answer = (id: string, a: Answers) => resolve(() => resolveQuestion(id, a))
  const decline = (id: string) => resolve(() => resolveQuestion(id, { declined: true }))
</script>

{#if notice}
  <Card class="border-attention">
    <div class="flex items-center justify-between gap-3">
      <span class="text-sm">{notice}</span>
      <Button variant="ghost" onclick={() => (notice = null)}>Dismiss</Button>
    </div>
  </Card>
{/if}

{#if waiting.length > 0}
  <Card class="border-attention">
    <div class="mb-3 text-sm font-medium text-attention">
      {waiting.length} agent{waiting.length === 1 ? '' : 's'} waiting on you
    </div>
    <ul class="flex flex-col gap-3">
      {#each waiting as a (a.id)}
        <li class="flex flex-col gap-2 border-t border-border pt-3 first:border-0 first:pt-0 sm:flex-row sm:items-center sm:justify-between">
          <div class="min-w-0">
            <div class="truncate text-sm font-medium">{a.name || a.id}</div>
            <div class="truncate text-xs text-muted">{a.pending ? describePending(a.pending) : ''}</div>
          </div>
          <div class="flex shrink-0 gap-2">
            <Button onclick={() => (deciding = a)}>
              {a.pending?.kind === 'approval' ? 'Review' : 'Answer'}
            </Button>
            <Button variant="ghost" onclick={() => onOpen(a.id)}>Open chat</Button>
          </div>
        </li>
      {/each}
    </ul>
  </Card>
{/if}

<!--
  Reuses the chat's modals rather than duplicating decision UI, so an
  approval resolved from the dashboard behaves identically to one resolved
  in a session — including diff rendering and the edited-command field.
-->
{#if deciding && decidingPermission}
  <PermissionModal
    permission={decidingPermission}
    onResolve={(d) => approve(decidingPermission.toolCallId, d)}
    onDeny={() => deny(decidingPermission.toolCallId)}
  />
{:else if deciding && decidingQuestion}
  <QuestionModal
    question={decidingQuestion}
    onResolve={(a) => answer(decidingQuestion.questionId, a)}
    onDecline={() => decline(decidingQuestion.questionId)}
  />
{/if}
