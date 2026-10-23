<script lang="ts">
  import { accessLabel, MODES, type AccessPatch, type AccessState, type Mode } from './interaction.js'

  /*
    The live controls. The component is a pure projection of the committed
    access state: it keeps no local copy, so a click never moves the UI ahead
    of the agent's answer. Until the committed tuple arrives, the controls
    read as unavailable rather than guessing at a default.
  */
  interface Props {
    access: AccessState | null
    onChange: (patch: AccessPatch) => void | Promise<void>
  }

  let { access, onChange }: Props = $props()

  const mode = $derived(access?.mode ?? null)
  const label = $derived(access ? accessLabel(access) : 'access unavailable')
  const autoEnabled = $derived(access != null && access.mode === 'act')

  function pick(next: Mode) {
    if (!access || next === access.mode) return
    void onChange({ mode: next })
  }

  function toggleAuto() {
    if (!access || !autoEnabled) return
    void onChange({ auto: !access.auto })
  }

  function toggleSystem() {
    if (!access) return
    void onChange({ system_access: !access.system })
  }
</script>

<div class="modes" role="group" aria-label="Interaction mode">
  {#each MODES as m (m)}
    <button
      type="button"
      class="mode"
      aria-pressed={mode === m}
      disabled={!access}
      onclick={() => pick(m)}
    >
      {m === 'ask' ? 'Ask' : 'Act'}
    </button>
  {/each}
  <button
    type="button"
    class="modifier"
    aria-pressed={access?.auto ?? false}
    disabled={!autoEnabled}
    title="Auto preauthorizes destructive workspace commands; it is an Act modifier"
    onclick={toggleAuto}
  >
    Auto
  </button>
  <button
    type="button"
    class="modifier"
    aria-pressed={access?.system ?? false}
    disabled={!access}
    title="Broader filesystem access for this session"
    onclick={toggleSystem}
  >
    System
  </button>
  <span class="label" data-testid="access-label">{label}</span>
</div>

<style>
  .modes {
    display: flex;
    align-items: center;
    gap: 0.25rem;
    padding: 0.15rem;
    border: 1px solid var(--color-border);
    border-radius: 6px;
    background: var(--color-surface);
  }
  button {
    color: var(--color-fg);
    padding: 0.3rem 0.5rem;
    border: 1px solid transparent;
    border-radius: 4px;
    background: transparent;
    font: inherit;
    line-height: 1;
    cursor: pointer;
  }
  button[aria-pressed='true'] {
    border-color: var(--color-border);
    background: var(--color-bg);
    font-weight: 600;
  }
  button:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }
  .modifier {
    font-size: 0.85em;
  }
  .label {
    margin-left: 0.35rem;
    color: var(--color-muted);
    font-size: 0.75rem;
    white-space: nowrap;
  }
</style>
