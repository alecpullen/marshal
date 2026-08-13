<script lang="ts">
  import type { PendingQuestion, Question } from './store.js'
  import type { Answers } from './api.js'

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

<div class="overlay">
  <div class="modal">
    <h2>Question</h2>
    {#each question.questions as q, i (i)}
      <div class="question">
        <p>{q.question}</p>
        {#if q.multi && q.options}
          {#each q.options as opt (opt.value)}
            <label class="option">
              <input type="checkbox" bind:group={values[i] as string[]} value={opt.value} />
              {opt.label}
            </label>
          {/each}
        {:else if q.options}
          {#each q.options as opt (opt.value)}
            <label class="option">
              <input type="radio" bind:group={values[i] as string} value={opt.value} />
              {opt.label}
            </label>
          {/each}
          {#if q.allowOther}
            <label class="option">
              <input type="radio" bind:group={values[i] as string} value="__other__" />
              Other
            </label>
            {#if isOtherSelected(i)}
              <input type="text" bind:value={others[i]} placeholder="Your answer" />
            {/if}
          {/if}
        {:else}
          <input type="text" bind:value={values[i] as string} />
        {/if}
      </div>
    {/each}

    <div class="actions">
      <button class="decline" onclick={onDecline}>Decline</button>
      <button class="submit" onclick={submit}>Answer</button>
    </div>
  </div>
</div>

<style>
  .overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.6);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 100;
  }
  .modal {
    background: var(--color-surface);
    padding: 1.5rem;
    border-radius: 12px;
    max-width: 560px;
    width: 90%;
    max-height: 80vh;
    overflow: auto;
  }
  h2 {
    margin-top: 0;
  }
  .question {
    margin-bottom: 1rem;
  }
  .question > p {
    margin: 0 0 0.5rem;
    font-weight: 500;
  }
  .option {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    margin: 0.25rem 0;
  }
  input[type='text'] {
    background: var(--color-bg);
    color: var(--color-fg);
    width: 100%;
    padding: 0.5rem;
    border: 1px solid var(--color-border);
    border-radius: 6px;
    font: inherit;
    margin-top: 0.25rem;
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
  }
  button {
    color: var(--color-fg);
    padding: 0.5rem 1rem;
    border-radius: 6px;
    border: 1px solid var(--color-border);
    background: var(--color-surface);
    cursor: pointer;
    font: inherit;
  }
  .submit {
    background: var(--color-accent);
    border-color: var(--color-accent);
    color: var(--color-bg);
  }
  .decline {
    background: var(--color-danger);
    border-color: var(--color-danger);
    color: var(--color-bg);
  }
</style>