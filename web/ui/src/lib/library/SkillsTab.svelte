<script lang="ts">
  import { untrack } from 'svelte'
  import Card from '../ui/Card.svelte'
  import Button from '../ui/Button.svelte'
  import Tag from '../ui/Tag.svelte'
  import ScopeBar from './ScopeBar.svelte'
  import {
    listSkills,
    previewSkill,
    confirmSkill,
    discardSkill,
    removeSkill,
    errMessage,
    type LibraryScope,
    type ProjectStatus,
    type SkillEntry,
    type SkillPreview,
  } from '../api'
  import { riskTone } from './risk'

  let { projects, project: initialProject = '', onToast }: { projects: ProjectStatus[]; project?: string; onToast: (t: string) => void } = $props()

  // The route seeds the initial scope; later changes are the tab's own.
  // svelte-ignore state_referenced_locally
  let scope = $state<LibraryScope>(initialProject ? 'project' : 'global')
  // svelte-ignore state_referenced_locally
  let project = $state(initialProject)
  let skills = $state<SkillEntry[]>([])
  let unsupported = $state(false)
  let error = $state('')
  let loaded = $state(false)
  let source = $state('')
  let preview = $state<SkillPreview | null>(null)
  let busy = $state(false)
  let removing = $state<string | null>(null)

  // A project scope with no choice yet takes the first available project.
  $effect(() => {
    if (scope === 'project' && !project) project = projects.find((p) => p.available)?.root ?? ''
  })

  async function load() {
    error = ''
    if (scope === 'project' && !project) {
      skills = []
      loaded = true
      return
    }
    try {
      const r = await listSkills(scope, project || undefined)
      unsupported = r === 'unsupported'
      skills = r === 'unsupported' ? [] : r
    } catch (e) {
      error = errMessage(e)
    } finally {
      loaded = true
    }
  }
  // Loads on mount and again whenever the scope or project changes.
  $effect(() => {
    void scope
    void project
    untrack(() => void load())
  })

  async function doPreview() {
    if (!source.trim()) return
    busy = true
    error = ''
    try {
      const r = await previewSkill(source.trim())
      if (r === 'unsupported') error = 'This agent cannot install skills.'
      else preview = r
    } catch (e) {
      error = errMessage(e)
    } finally {
      busy = false
    }
  }

  async function confirm() {
    if (!preview) return
    busy = true
    error = ''
    try {
      await confirmSkill(preview.stagingToken, scope, scope === 'project' ? project : undefined)
      onToast(`Installed ${preview.name}`)
      preview = null
      source = ''
      await load()
    } catch (e) {
      error = errMessage(e)
    } finally {
      busy = false
    }
  }

  async function discard() {
    const p = preview
    preview = null
    if (!p) return
    try {
      await discardSkill(p.stagingToken)
    } catch {
      // A staged copy that outlives the page is cleaned up by the agent.
    }
  }

  async function remove(name: string) {
    removing = null
    error = ''
    try {
      await removeSkill(name, scope, scope === 'project' ? project : undefined)
      onToast(`Removed ${name}`)
      await load()
    } catch (e) {
      error = errMessage(e)
    }
  }
</script>

<div class="flex flex-col gap-4">
  <ScopeBar {scope} {project} {projects} onScope={(s) => (scope = s)} onProject={(r) => (project = r)} />

  {#if error}<Card class="border-attention text-sm" role="alert">{error}</Card>{/if}

  {#if unsupported}
    <p class="text-sm text-muted">Project skills are managed from this project's agents in container mode.</p>
  {:else}
    <div class="flex flex-col gap-1">
      {#each skills as s (s.scope + s.name)}
        <div class="flex items-center gap-3 rounded-md border border-border bg-surface px-3 py-2" data-testid="skill-row">
          <span class="font-mono text-sm">{s.name}</span>
          <span class="min-w-0 flex-1 truncate text-sm text-muted">{s.description}</span>
          <Tag tone={riskTone(s.risk)}>{s.risk || 'unknown'}</Tag>
          <Tag tone="neutral">{s.scope}</Tag>
          {#if removing === s.name}
            <span class="text-xs text-muted">Remove {s.name}?</span>
            <Button variant="danger" class="min-h-8 px-2 py-1 text-xs" onclick={() => remove(s.name)}>Confirm remove</Button>
            <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => (removing = null)}>Cancel</Button>
          {:else}
            <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => (removing = s.name)}>Remove</Button>
          {/if}
        </div>
      {:else}
        {#if loaded}<p class="text-sm text-muted">No skills in this scope.</p>{/if}
      {/each}
    </div>

    <form class="flex flex-wrap items-center gap-2" onsubmit={(e) => { e.preventDefault(); void doPreview() }}>
      <input
        bind:value={source}
        placeholder="Skill source (git URL or path)"
        aria-label="Skill source"
        class="min-w-64 flex-1 rounded border border-border bg-bg p-2 text-sm"
      />
      <Button type="submit" disabled={busy || !source.trim() || (scope === 'project' && !project)}>Preview</Button>
    </form>

    {#if preview}
      <Card class="flex flex-col gap-2" data-testid="skill-preview">
        <div class="flex items-center gap-2">
          <span class="font-mono text-sm font-medium">{preview.name}</span>
          <Tag tone={riskTone(preview.risk)}>{preview.risk || 'unknown'}</Tag>
        </div>
        <p class="text-sm text-muted">{preview.description}</p>
        <p class="truncate font-mono text-xs text-dim">{preview.source}</p>
        <div class="flex gap-2">
          <Button disabled={busy} onclick={confirm}>Confirm</Button>
          <Button variant="ghost" onclick={discard}>Discard</Button>
        </div>
      </Card>
    {/if}
  {/if}
</div>
