<script lang="ts">
  import { keepUnavailable } from './config'

  /**
   * One model-profile choice (design C5): a follow option where the caller
   * supplies one, the real profiles in a stable order, and a saved profile the
   * list no longer offers — kept visible so touching the picker cannot
   * silently replace a value the person never meant to change.
   */
  export interface ProfileSelectProps {
    label: string
    value: string
    profiles: string[]
    followLabel?: string
    disabled?: boolean
    /** Marked when a validation failure names this picker. */
    invalid?: boolean
    /** The id of the element describing this picker's failure. */
    describedBy?: string
  }

  let { label, value = $bindable(''), profiles, followLabel, disabled = false, invalid = false, describedBy }: ProfileSelectProps = $props()

  const choices = $derived(keepUnavailable([...profiles].sort().map((name) => ({ value: name, label: name })), value))
</script>

<label class="grid gap-1 text-sm">{label}
  <select class="rounded border border-border bg-bg p-2" bind:value {disabled} aria-invalid={invalid ? 'true' : undefined} aria-describedby={describedBy}>
    {#if followLabel}<option value="">{followLabel}</option>{/if}
    {#each choices as choice (choice.value)}<option value={choice.value}>{choice.label}</option>{/each}
  </select>
</label>
