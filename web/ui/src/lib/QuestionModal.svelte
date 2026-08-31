<script lang="ts">
  import type { PendingQuestion, Question } from './store.js'
  import type { Answers } from './api.js'
  import Modal from './ui/Modal.svelte'
  import Button from './ui/Button.svelte'

  interface Props {
    question: PendingQuestion
    onResolve: (a: Answers) => void | Promise<void>
    onDecline: () => void | Promise<void>
  }

  let { question, onResolve, onDecline }: Props = $props()
  let values = $state<(string | string[])[]>([])
  let others = $state<string[]>([])

  $effect(() => {
    if (values.length !== question.questions.length) {
      values = question.questions.map(initValue)
      others = question.questions.map(() => '')
    }
  })

  function initValue(q: Question): string | string[] {
    if (q.multi) return []
    if (q.options && q.allowOther) return ''
    if (q.options) return q.options[0]?.value ?? ''
    return ''
  }

  function isOtherSelected(i: number): boolean {
    const v = values[i]
    return typeof v === 'string' && v === '__other__'
  }

  async function submit() {
    const answers = question.questions.map((q, i) => {
      let answer: string | string[] = values[i]
      if (q.options && q.allowOther && isOtherSelected(i)) {
        answer = others[i]
      }
      return { question: q.question, answer }
    })
    await onResolve({ answers })
  }
</script>

<Modal title="Question" onDismiss={onDecline}>
  {#each question.questions as q, i (i)}
    <div class="flex flex-col gap-1.5">
      <p class="text-sm font-medium">{q.question}</p>
      {#if q.multi && q.options}
        {#each q.options as opt (opt.value)}
          <label class="flex items-center gap-2 text-sm">
            <input type="checkbox" bind:group={values[i] as string[]} value={opt.value} />
            {opt.label}
          </label>
        {/each}
      {:else if q.options}
        {#each q.options as opt (opt.value)}
          <label class="flex items-center gap-2 text-sm">
            <input type="radio" bind:group={values[i] as string} value={opt.value} />
            {opt.label}
          </label>
        {/each}
        {#if q.allowOther}
          <label class="flex items-center gap-2 text-sm">
            <input type="radio" bind:group={values[i] as string} value="__other__" />
            Other
          </label>
          {#if isOtherSelected(i)}
            <input
              type="text"
              bind:value={others[i]}
              placeholder="Your answer"
              class="min-h-11 rounded-md border border-border bg-bg px-3 py-2 text-sm"
            />
          {/if}
        {/if}
      {:else}
        <input
          type="text"
          bind:value={values[i] as string}
          class="min-h-11 rounded-md border border-border bg-bg px-3 py-2 text-sm"
        />
      {/if}
    </div>
  {/each}

  {#snippet footer()}
    <Button variant="ghost" onclick={onDecline}>Decline</Button>
    <Button onclick={submit}>Answer</Button>
  {/snippet}
</Modal>
