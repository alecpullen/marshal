<script lang="ts">
  import Card from '../ui/Card.svelte'
  import Tag from '../ui/Tag.svelte'
  import type { Finding } from '../api'
  import { SEVERITIES, severityTone } from './model'

  let {
    finding = $bindable(),
    fileUrl = null,
    evidence = null,
    editing = false,
    selectable = false,
    selected = false,
    onSelect = () => {},
  }: {
    finding: Finding
    /** The forge's page for `path:line` at the head SHA. */
    fileUrl?: string | null
    /** `#chat/<agentId>?node=<stepNode>` when the finding names the step that produced it. */
    evidence?: string | null
    editing?: boolean
    selectable?: boolean
    selected?: boolean
    onSelect?: (on: boolean) => void
  } = $props()

  const where = $derived(finding.path ? `${finding.path}${finding.line ? `:${finding.line}` : ''}` : '')
  const field = 'rounded border border-border bg-bg p-1.5 text-sm'
</script>

<Card class="flex flex-col gap-2 p-3" data-testid="finding">
  {#if editing}
    <div class="flex flex-wrap items-center gap-2">
      <select aria-label="Severity" bind:value={finding.severity} class={field}>
        {#each SEVERITIES as s (s)}<option value={s}>{s}</option>{/each}
        {#if !(SEVERITIES as readonly string[]).includes(finding.severity)}<option value={finding.severity}>{finding.severity}</option>{/if}
      </select>
      <input aria-label="Title" bind:value={finding.title} class="{field} min-w-0 flex-1" />
    </div>
    <textarea aria-label="Body" rows="4" bind:value={finding.body} class="{field} w-full"></textarea>
  {:else}
    <div class="flex flex-wrap items-center gap-2">
      {#if selectable}<input type="checkbox" aria-label="Send {finding.title}" checked={selected} onchange={(e) => onSelect(e.currentTarget.checked)} />{/if}
      <Tag tone={severityTone(finding.severity)}>{finding.severity}</Tag>
      <span class="min-w-0 flex-1 text-sm font-medium">{finding.title}</span>
    </div>
    <p class="text-sm whitespace-pre-wrap text-muted">{finding.body}</p>
  {/if}
  <div class="flex flex-wrap items-center gap-3 text-xs">
    {#if where}
      {#if fileUrl}<a class="font-mono text-accent hover:underline" href={fileUrl} target="_blank" rel="noopener noreferrer">{where}</a>{:else}<span class="font-mono text-muted">{where}</span>{/if}
    {/if}
    {#if evidence}<a class="text-accent hover:underline" href={evidence}>Evidence</a>{/if}
  </div>
</Card>
