<script lang="ts">
  import Button from '../ui/Button.svelte'
  import QuestionModal from '../QuestionModal.svelte'
  import { toPendingQuestion, type AgentRow } from '../fleet'
  import { APIError, resolvePermission, resolveQuestion, type Answers } from '../api'

  /*
    The decision controls for an agent parked on an approval or question:
    Approve and Deny, or Answer (which opens the question dialog) and Open.
    Home's Needs-you rows and the Live wall's tiles share it, so a decision
    reads and behaves the same wherever it is made.
  */
  let {
    agent,
    onResolved,
    onOpen,
  }: {
    agent: AgentRow
    /** Called after any attempt, so the caller refetches what is still pending. */
    onResolved: () => void
    onOpen: () => void
  } = $props()

  let notice = $state('')
  let answering = $state(false)

  async function act(action: () => Promise<unknown>) {
    notice = ''
    try {
      await action()
    } catch (e) {
      // 410 means it was already resolved elsewhere; refreshing is the answer.
      notice = e instanceof APIError && e.status === 410 ? 'That request was already resolved.' : e instanceof Error ? e.message : String(e)
    } finally {
      onResolved()
    }
  }

  const pending = $derived(agent.pending)
  const question = $derived(pending?.kind === 'question' ? toPendingQuestion(agent.id, pending) : null)
</script>

{#if pending?.kind === 'approval'}
  <Button onclick={() => act(() => resolvePermission(pending.id, { approved: true }))}>Approve</Button>
  <Button variant="danger" onclick={() => act(() => resolvePermission(pending.id, { approved: false }))}>Deny</Button>
{:else if pending}
  <Button onclick={() => (answering = true)}>Answer</Button>
  <Button variant="ghost" onclick={onOpen}>Open</Button>
{/if}
{#if notice}<span class="basis-full text-xs text-err" role="status">{notice}</span>{/if}

{#if answering && question}
  <QuestionModal
    {question}
    onResolve={(ans: Answers) => {
      answering = false
      void act(() => resolveQuestion(question.questionId, ans))
    }}
    onDecline={() => {
      answering = false
      void act(() => resolveQuestion(question.questionId, { declined: true }))
    }}
  />
{/if}
