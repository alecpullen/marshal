<script lang="ts">
  import { untrack } from 'svelte'
  import Card from '../ui/Card.svelte'
  import Button from '../ui/Button.svelte'
  import Tag from '../ui/Tag.svelte'
  import ScopeBar from './ScopeBar.svelte'
  import {
    listPlugins,
    scanPlugin,
    confirmPlugin,
    discardPlugin,
    removePlugin,
    errMessage,
    type LibraryScope,
    type PluginEntry,
    type PluginScan,
    type ProjectStatus,
  } from '../api'

  let { projects, project: initialProject = '', onToast }: { projects: ProjectStatus[]; project?: string; onToast: (t: string) => void } = $props()

  // The route seeds the initial scope; later changes are the tab's own.
  // svelte-ignore state_referenced_locally
  let scope = $state<LibraryScope>(initialProject ? 'project' : 'global')
  // svelte-ignore state_referenced_locally
  let project = $state(initialProject)
  let plugins = $state<PluginEntry[]>([])
  let unsupported = $state(false)
  let error = $state('')
  let loaded = $state(false)
  let source = $state('')
  let ref = $state('')
  let scan = $state<PluginScan | null>(null)
  let busy = $state(false)
  let removing = $state<string | null>(null)

  $effect(() => {
    if (scope === 'project' && !project) project = projects.find((p) => p.available)?.root ?? ''
  })

  async function load() {
    error = ''
    if (scope === 'project' && !project) {
      plugins = []
      loaded = true
      return
    }
    try {
      const r = await listPlugins(scope, project || undefined)
      unsupported = r === 'unsupported'
      plugins = r === 'unsupported' ? [] : r
    } catch (e) {
      error = errMessage(e)
    } finally {
      loaded = true
    }
  }
  $effect(() => {
    void scope
    void project
    untrack(() => void load())
  })

  async function doScan() {
    if (!source.trim()) return
    busy = true
    error = ''
    try {
      const r = await scanPlugin(source.trim(), ref.trim() || undefined, scope, scope === 'project' ? project : undefined)
      if (r === 'unsupported') error = 'This agent cannot install plugins.'
      else scan = r
    } catch (e) {
      error = errMessage(e)
    } finally {
      busy = false
    }
  }

  async function confirm() {
    if (!scan) return
    busy = true
    error = ''
    try {
      await confirmPlugin(scan.scanToken, scope, scope === 'project' ? project : undefined)
      onToast(`Installed ${scan.name}`)
      scan = null
      source = ''
      ref = ''
      await load()
    } catch (e) {
      error = errMessage(e)
    } finally {
      busy = false
    }
  }

  async function discard() {
    const s = scan
    scan = null
    if (!s) return
    try {
      await discardPlugin(s.scanToken)
    } catch {
      // A scan that outlives the page is cleaned up by the agent.
    }
  }

  async function remove(name: string) {
    removing = null
    error = ''
    try {
      await removePlugin(name, scope, scope === 'project' ? project : undefined)
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
    <p class="text-sm text-muted">Project plugins are managed from this project's agents in container mode.</p>
  {:else}
    <div class="flex flex-col gap-1">
      {#each plugins as p (p.scope + p.name)}
        <div class="flex items-center gap-3 rounded-md border border-border bg-surface px-3 py-2" data-testid="plugin-row">
          <span class="font-mono text-sm">{p.name}</span>
          <span class="min-w-0 flex-1 truncate font-mono text-xs text-muted">{p.source}{p.ref ? `@${p.ref}` : ''} · {p.commit.slice(0, 8)}</span>
          <Tag tone="neutral">{p.scope}</Tag>
          {#if removing === p.name}
            <span class="text-xs text-muted">Remove {p.name}?</span>
            <Button variant="danger" class="min-h-8 px-2 py-1 text-xs" onclick={() => remove(p.name)}>Confirm remove</Button>
            <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => (removing = null)}>Cancel</Button>
          {:else}
            <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => (removing = p.name)}>Remove</Button>
          {/if}
        </div>
      {:else}
        {#if loaded}<p class="text-sm text-muted">No plugins in this scope.</p>{/if}
      {/each}
    </div>

    <form class="flex flex-wrap items-center gap-2" onsubmit={(e) => { e.preventDefault(); void doScan() }}>
      <input bind:value={source} placeholder="Plugin source (git URL)" aria-label="Plugin source" class="min-w-64 flex-1 rounded border border-border bg-bg p-2 text-sm" />
      <input bind:value={ref} placeholder="ref (optional)" aria-label="Plugin ref" class="w-36 rounded border border-border bg-bg p-2 text-sm" />
      <Button type="submit" disabled={busy || !source.trim() || (scope === 'project' && !project)}>Scan</Button>
    </form>

    {#if scan}
      <Card class="flex flex-col gap-2" data-testid="plugin-scan">
        <div class="flex items-center gap-2">
          <span class="font-mono text-sm font-medium">{scan.name}</span>
          <span class="font-mono text-xs text-muted">{scan.commit.slice(0, 8)}</span>
        </div>
        <ul class="flex flex-wrap gap-2 text-xs">
          <li><Tag tone="info">{scan.contents.skillCount} skills</Tag></li>
          <li><Tag tone="info">{scan.contents.commandCount} commands</Tag></li>
          <li><Tag tone="info">{scan.contents.hookCount} hooks</Tag></li>
          <li><Tag tone="info">{scan.contents.mcpServerCount} MCP servers</Tag></li>
          <li><Tag tone="info">{scan.contents.mcpPolicyCount} MCP policies</Tag></li>
        </ul>
        {#if scan.contents.hookCount > 0 || scan.contents.mcpServerCount > 0}
          <p class="text-xs text-warn">This plugin runs code: hooks and MCP servers execute on this machine.</p>
        {/if}
        <div class="flex gap-2">
          <Button disabled={busy} onclick={confirm}>Confirm</Button>
          <Button variant="ghost" onclick={discard}>Discard</Button>
        </div>
      </Card>
    {/if}
  {/if}
</div>
