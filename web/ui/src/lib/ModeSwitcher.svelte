<script lang="ts">
  import { MODES, type Mode } from './store.js'

  interface Props {
    mode: Mode
    onChange: (mode: Mode) => void | Promise<void>
  }

  let { mode, onChange }: Props = $props()
</script>

<select value={mode} onchange={(e) => onChange(e.currentTarget.value as Mode)}>
  {#each MODES as m (m)}
    <option value={m}>{m}</option>
  {/each}
</select>

<style>
  select {
    color: var(--color-fg);
    padding: 0.4rem 0.5rem;
    border: 1px solid var(--color-border);
    border-radius: 6px;
    background: var(--color-surface);
    font: inherit;
    text-transform: capitalize;
  }
</style>