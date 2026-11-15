<script lang="ts">
  import Button from './ui/Button.svelte'
  import type { WorkingDraftChecksMode, WorkingDraftContent } from './working-drafts'

  /**
   * The field-level merge for a draft two editors changed.
   *
   * The groups below are atomic: a value is taken whole from one side, never
   * half of one side and half of the other. `title`, `plan`, `spec`, the repo,
   * the target branch, the workspace, both profiles, the reviewer
   * instructions, each numeric limit, the checks mode with its text, the
   * native choice with the flag that recorded it, implementer auto and the
   * no-CI acknowledgement are all one group each. The project is not here: it
   * is what made the two versions comparable in the first place.
   *
   * The combined result starts as the local input. A per-field choice copies
   * one side in, and every group stays editable afterwards, so a merge is
   * never limited to picking a whole side. It is a plain local value the host
   * saves against the newest server version; nothing is written from here.
   *
   * A second conflict updates `server` while this component stays mounted, so
   * the in-progress combined result survives it — only the copy of the newest
   * server version on screen changes.
   */
  let {
    base = null,
    local,
    server,
    onResolve,
    onKeepLocal,
    onDiscardLocal,
  }: {
    /** The last version both sides agreed on, shown for context only. */
    base?: WorkingDraftContent | null
    local: WorkingDraftContent
    server: WorkingDraftContent
    onResolve: (combined: WorkingDraftContent) => void
    onKeepLocal: () => void
    onDiscardLocal: () => void
  } = $props()

  type Choice = 'local' | 'server'
  type TextField =
    | 'title'
    | 'plan'
    | 'spec'
    | 'repoId'
    | 'targetBranchOverride'
    | 'workspaceOverride'
    | 'implementerOverride'
    | 'reviewerOverride'
    | 'reviewerInstructions'
    | 'maxRoundsOverride'
    | 'ciPollOverride'
    | 'ciWaitOverride'

  const textFields: { key: TextField; label: string; textarea?: boolean; mono?: boolean }[] = [
    { key: 'title', label: 'Title' },
    { key: 'plan', label: 'Plan', textarea: true },
    { key: 'spec', label: 'Specification', textarea: true },
    { key: 'repoId', label: 'Repository', mono: true },
    { key: 'targetBranchOverride', label: 'Target branch', mono: true },
    { key: 'workspaceOverride', label: 'Workspace', mono: true },
    { key: 'implementerOverride', label: 'Implementer profile', mono: true },
    { key: 'reviewerOverride', label: 'Reviewer profile', mono: true },
    { key: 'reviewerInstructions', label: 'Reviewer instructions', textarea: true },
    { key: 'maxRoundsOverride', label: 'Total rounds' },
    { key: 'ciPollOverride', label: 'CI poll seconds' },
    { key: 'ciWaitOverride', label: 'CI wait seconds' },
  ]

  let choices = $state<Record<TextField, Choice>>({
    title: 'local',
    plan: 'local',
    spec: 'local',
    repoId: 'local',
    targetBranchOverride: 'local',
    workspaceOverride: 'local',
    implementerOverride: 'local',
    reviewerOverride: 'local',
    reviewerInstructions: 'local',
    maxRoundsOverride: 'local',
    ciPollOverride: 'local',
    ciWaitOverride: 'local',
  })
  let checksChoice = $state<Choice>('local')
  let flagsChoice = $state<Choice>('local')

  /** The combined result. It starts as the local input and outlives a second conflict. */
  let combined = $state<WorkingDraftContent>({ ...local })

  const side = (from: Choice): WorkingDraftContent => (from === 'local' ? local : server)

  function chooseField(key: TextField, from: Choice) {
    choices[key] = from
    combined[key] = side(from)[key]
  }
  function chooseChecks(from: Choice) {
    checksChoice = from
    // The mode and the text are one choice: "none" with the other side's
    // check list would be a state neither editor ever wrote.
    combined.checksMode = side(from).checksMode
    combined.expectedChecksText = side(from).expectedChecksText
  }
  function chooseFlags(from: Choice) {
    flagsChoice = from
    const picked = side(from)
    combined.native = picked.native
    combined.nativeChosen = picked.nativeChosen
    combined.implementerAuto = picked.implementerAuto
    combined.noRequiredCiAcknowledged = picked.noRequiredCiAcknowledged
  }
  function setChecksMode(mode: string) {
    combined.checksMode = mode as WorkingDraftChecksMode
  }
  function setNative(on: boolean) {
    combined.native = on
    // Setting the environment is a choice, exactly as in the editor.
    combined.nativeChosen = true
  }

  const CHECK_MODE_TEXT: Record<WorkingDraftChecksMode, string> = {
    inherit: 'Inherit the project defaults',
    none: 'No required checks',
    custom: 'This draft’s own checks',
  }
  const show = (value: string): string => (value.trim() ? value : '(empty)')
  const checksText = (c: WorkingDraftContent): string =>
    `${CHECK_MODE_TEXT[c.checksMode]}${c.checksMode === 'custom' ? `: ${show(c.expectedChecksText)}` : ''}`
  const flagsText = (c: WorkingDraftContent): string =>
    [
      c.native ? 'Native environment' : 'Workspace environment',
      c.implementerAuto ? 'Implementer auto on' : 'Implementer auto off',
      c.noRequiredCiAcknowledged ? 'No required CI acknowledged' : 'Required CI not acknowledged',
    ].join(' · ')

  const field = 'w-full min-w-0 rounded border border-border bg-bg p-2 text-sm'
  const preview = 'min-w-0 max-h-40 overflow-y-auto overflow-x-hidden whitespace-pre-wrap break-words rounded border border-border bg-surface p-2 font-mono text-xs'
</script>

<div class="flex min-w-0 flex-col gap-4" data-testid="draft-conflict" role="group" aria-label="Resolve the draft conflict">
  <div class="min-w-0">
    <h2 class="text-sm font-semibold">This draft changed elsewhere</h2>
    <p class="text-xs text-muted">
      Nothing is overwritten until you save. Pick a version for each field or edit the combined result, then save it over the
      newest server version.
    </p>
  </div>

  {#each textFields as f (f.key)}
    <fieldset class="flex min-w-0 flex-col gap-2 rounded border border-border p-3">
      <legend class="px-1 text-xs font-medium">{f.label}</legend>
      <div class="grid min-w-0 gap-2 sm:grid-cols-2">
        <div class="min-w-0">
          <p class="text-xs text-muted">Local (not saved yet)</p>
          <p class={preview} data-testid={`local-${f.key}`}>{show(local[f.key])}</p>
        </div>
        <div class="min-w-0">
          <p class="text-xs text-muted">Server (newest)</p>
          <p class={preview} data-testid={`server-${f.key}`}>{show(server[f.key])}</p>
        </div>
      </div>
      {#if base}
        <p class="min-w-0 truncate text-xs text-muted" data-testid={`base-${f.key}`}>Last agreed: {show(base[f.key])}</p>
      {/if}
      <div class="flex flex-wrap gap-2" role="group" aria-label={`Choose the ${f.label} version`}>
        <Button type="button" class="min-h-9 px-2 py-1 text-xs" variant={choices[f.key] === 'local' ? 'default' : 'ghost'} aria-pressed={choices[f.key] === 'local'} onclick={() => chooseField(f.key, 'local')}>Use local {f.label}</Button>
        <Button type="button" class="min-h-9 px-2 py-1 text-xs" variant={choices[f.key] === 'server' ? 'default' : 'ghost'} aria-pressed={choices[f.key] === 'server'} onclick={() => chooseField(f.key, 'server')}>Use server {f.label}</Button>
      </div>
      {#if f.textarea}
        <textarea aria-label={`Combined ${f.label}`} class="{field} min-h-24 {f.mono ? 'font-mono' : ''}" value={combined[f.key]} oninput={(e) => (combined[f.key] = e.currentTarget.value)}></textarea>
      {:else}
        <input aria-label={`Combined ${f.label}`} class="{field} {f.mono ? 'font-mono' : ''}" value={combined[f.key]} oninput={(e) => (combined[f.key] = e.currentTarget.value)} />
      {/if}
    </fieldset>
  {/each}

  <fieldset class="flex min-w-0 flex-col gap-2 rounded border border-border p-3">
    <legend class="px-1 text-xs font-medium">Expected checks</legend>
    <div class="grid min-w-0 gap-2 sm:grid-cols-2">
      <div class="min-w-0">
        <p class="text-xs text-muted">Local (not saved yet)</p>
        <p class={preview} data-testid="local-checks">{checksText(local)}</p>
      </div>
      <div class="min-w-0">
        <p class="text-xs text-muted">Server (newest)</p>
        <p class={preview} data-testid="server-checks">{checksText(server)}</p>
      </div>
    </div>
    <div class="flex flex-wrap gap-2" role="group" aria-label="Choose the expected checks version">
      <Button type="button" class="min-h-9 px-2 py-1 text-xs" variant={checksChoice === 'local' ? 'default' : 'ghost'} aria-pressed={checksChoice === 'local'} onclick={() => chooseChecks('local')}>Use local expected checks</Button>
      <Button type="button" class="min-h-9 px-2 py-1 text-xs" variant={checksChoice === 'server' ? 'default' : 'ghost'} aria-pressed={checksChoice === 'server'} onclick={() => chooseChecks('server')}>Use server expected checks</Button>
    </div>
    <label class="grid gap-1 text-xs">Combined expected checks source
      <select aria-label="Combined expected checks source" class={field} value={combined.checksMode} onchange={(e) => setChecksMode(e.currentTarget.value)}>
        <option value="inherit">Inherit from the project's factory defaults</option>
        <option value="none">No required checks</option>
        <option value="custom">List this draft's checks</option>
      </select>
    </label>
    {#if combined.checksMode === 'custom'}
      <label class="grid gap-1 text-xs">Combined expected CI checks
        <span class="text-muted">One per line: check_run:name:appId or commit_status:context</span>
        <textarea aria-label="Combined expected CI checks" class="{field} min-h-24 font-mono" value={combined.expectedChecksText} oninput={(e) => (combined.expectedChecksText = e.currentTarget.value)}></textarea>
      </label>
    {/if}
  </fieldset>

  <fieldset class="flex min-w-0 flex-col gap-2 rounded border border-border p-3">
    <legend class="px-1 text-xs font-medium">Choices</legend>
    <div class="grid min-w-0 gap-2 sm:grid-cols-2">
      <div class="min-w-0">
        <p class="text-xs text-muted">Local (not saved yet)</p>
        <p class={preview} data-testid="local-flags">{flagsText(local)}</p>
      </div>
      <div class="min-w-0">
        <p class="text-xs text-muted">Server (newest)</p>
        <p class={preview} data-testid="server-flags">{flagsText(server)}</p>
      </div>
    </div>
    <div class="flex flex-wrap gap-2" role="group" aria-label="Choose the environment and acknowledgement version">
      <Button type="button" class="min-h-9 px-2 py-1 text-xs" variant={flagsChoice === 'local' ? 'default' : 'ghost'} aria-pressed={flagsChoice === 'local'} onclick={() => chooseFlags('local')}>Use local choices</Button>
      <Button type="button" class="min-h-9 px-2 py-1 text-xs" variant={flagsChoice === 'server' ? 'default' : 'ghost'} aria-pressed={flagsChoice === 'server'} onclick={() => chooseFlags('server')}>Use server choices</Button>
    </div>
    <label class="flex items-center gap-2 text-sm"><input type="checkbox" aria-label="Combined native environment" checked={combined.native} onchange={(e) => setNative(e.currentTarget.checked)} /> Native environment</label>
    <label class="flex items-center gap-2 text-sm"><input type="checkbox" aria-label="Combined implementer auto" checked={combined.implementerAuto} onchange={(e) => (combined.implementerAuto = e.currentTarget.checked)} /> Implementer auto permission setting</label>
    <label class="flex items-center gap-2 text-sm"><input type="checkbox" aria-label="Combined no required CI" checked={combined.noRequiredCiAcknowledged} onchange={(e) => (combined.noRequiredCiAcknowledged = e.currentTarget.checked)} /> This repository has no required CI</label>
  </fieldset>

  <div class="flex min-w-0 flex-wrap gap-2">
    <Button type="button" onclick={() => onResolve({ ...combined })}>Save combined version</Button>
    <Button type="button" variant="ghost" onclick={onKeepLocal}>Keep my version</Button>
    <Button type="button" variant="ghost" onclick={onDiscardLocal}>Discard my changes</Button>
  </div>
</div>
