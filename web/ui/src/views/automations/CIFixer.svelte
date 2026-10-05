<script lang="ts">
  import Card from '../../lib/ui/Card.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Tag from '../../lib/ui/Tag.svelte'
  import WebhookSetup from '../../lib/automations/WebhookSetup.svelte'
  import { ciTone } from '../../lib/automations/model'
  import { errMessage, getProjectSettings, listCIHistory, listRepos, putProjectSettings, type CIFixerSettings, type CIHistory, type ProjectSettings, type RepoRow } from '../../lib/api'
  import type { AgentRow } from '../../lib/fleet'
  import { formatProjectRoute } from '../../lib/routes'
  import { shortName } from '../../lib/utils'

  let { root, agents = [], refreshTick = 0, onNavigate }: { root: string; agents?: AgentRow[]; /** Bumped when an `automation` delta arrives. */ refreshTick?: number; onNavigate: (hash: string) => void } = $props()

  const blank: CIFixerSettings = { enabled: false, repoId: '', branches: [], maxMinutes: 20, maxUsd: 2, push: false, pushBranches: [] }
  let saved = $state<ProjectSettings>({ intake: {} })
  let repos = $state<RepoRow[]>([])
  let history = $state<CIHistory[]>([])
  let loaded = $state(false)
  let saving = $state(false)
  let error = $state('')
  let notice = $state('')

  let enabled = $state(false)
  let repoId = $state('')
  let branches = $state('')
  let maxMinutes = $state(20)
  let maxUsd = $state(2)
  let push = $state(false)
  let pushBranches = $state('')

  const list = (s: string) => s.split(',').map((x) => x.trim()).filter(Boolean)

  function adopt(s: ProjectSettings) {
    saved = s
    const c = s.automations?.ciFixer ?? blank
    enabled = c.enabled
    repoId = c.repoId
    branches = (c.branches ?? []).join(', ')
    maxMinutes = c.maxMinutes || blank.maxMinutes
    maxUsd = c.maxUsd || blank.maxUsd
    push = c.push
    pushBranches = (c.pushBranches ?? []).join(', ')
  }

  async function load() {
    try {
      adopt(await getProjectSettings(root))
    } catch (e) {
      error = errMessage(e)
    }
    loaded = true
    repos = await listRepos().catch(() => [])
  }
  async function loadHistory() {
    try {
      history = [...(await listCIHistory({ project: root }))].sort((a, b) => (b.createdAt ?? '').localeCompare(a.createdAt ?? ''))
    } catch (e) {
      error = errMessage(e)
    }
  }
  $effect(() => {
    void load()
  })
  $effect(() => {
    void refreshTick
    void loadHistory()
  })

  async function save() {
    saving = true
    error = ''
    notice = ''
    const ciFixer: CIFixerSettings = { enabled, repoId, branches: list(branches), maxMinutes: Number(maxMinutes), maxUsd: Number(maxUsd), push, pushBranches: push ? list(pushBranches) : [] }
    try {
      adopt(await putProjectSettings(root, { ...saved, automations: { ...saved.automations, ciFixer } }))
      notice = 'CI fixer settings saved'
    } catch (e) {
      error = errMessage(e)
    } finally {
      saving = false
    }
  }

  const repoChoices = $derived([...new Set([...repos.map((r) => r.id), ...(repoId ? [repoId] : [])])])
  const alive = (h: CIHistory) => (h.agentId && agents.some((a) => a.id === h.agentId) ? h.agentId : undefined)
  const field = 'rounded border border-border bg-bg p-2 text-sm'
</script>

<div class="mx-auto flex max-w-5xl flex-col gap-4 p-6">
  <header>
    <p class="text-xs text-muted"><a class="text-accent hover:underline" href="#projects">Projects</a> / <a class="text-accent hover:underline" href={formatProjectRoute(root)}>{shortName(root)}</a> /</p>
    <h1 class="text-lg font-semibold">CI fixer</h1>
  </header>

  {#if error}<Card class="border-attention text-sm" role="alert">{error}</Card>{/if}
  {#if notice}<Card class="text-sm" role="status">{notice}</Card>{/if}

  <Card class="flex flex-col gap-3" data-testid="ci-settings">
    <h2 class="text-sm font-semibold">Settings</h2>
    <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={enabled} /> Try to fix failing checks</label>
    <label class="flex flex-col gap-1 text-xs">Repo
      <select aria-label="CI repo" bind:value={repoId} class={field}>
        <option value="">None</option>
        {#each repoChoices as r (r)}<option value={r}>{r}</option>{/each}
      </select>
    </label>
    <label class="flex flex-col gap-1 text-xs">Branches
      <input aria-label="Branches" bind:value={branches} placeholder="comma separated, e.g. main" class={field} />
    </label>
    <div class="flex flex-wrap gap-3">
      <label class="flex flex-col gap-1 text-xs">Max minutes
        <input aria-label="Max minutes" type="number" min="1" bind:value={maxMinutes} class="{field} w-28" />
      </label>
      <label class="flex flex-col gap-1 text-xs">Max USD
        <input aria-label="Max USD" type="number" min="0" step="0.5" bind:value={maxUsd} class="{field} w-28" />
      </label>
    </div>
    <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={push} /> Push fixes to the branch instead of opening a PR</label>
    {#if push}
      <label class="flex flex-col gap-1 text-xs">Push branches
        <input aria-label="Push branches" bind:value={pushBranches} placeholder="comma separated; only these are pushed to" class={field} />
      </label>
      <p class="rounded-md border border-attention bg-attention/10 p-2 text-sm" role="note">Pushing skips human review of the fix. The verify gate still has to pass, and the bridge never overrides it.</p>
    {/if}
    <WebhookSetup {repoId} />
    <div><Button onclick={save} disabled={saving || !loaded}>Save settings</Button></div>
  </Card>

  <Card class="flex flex-col gap-3" data-testid="ci-history">
    <h2 class="text-sm font-semibold">History</h2>
    <div class="overflow-x-auto">
      <table class="w-full text-left text-sm">
        <thead class="text-xs text-muted"><tr><th class="py-1 pr-3">SHA</th><th class="pr-3">Check</th><th class="pr-3">Status</th><th class="pr-3">Reason</th><th class="pr-3">PR</th><th class="text-right">Cost</th></tr></thead>
        <tbody>
          {#each history as h (h.id)}
            {@const agent = alive(h)}
            <tr class="border-t border-border {agent ? 'cursor-pointer hover:bg-surface' : ''}" data-testid="ci-row" onclick={() => agent && onNavigate(`#chat/${encodeURIComponent(agent)}`)}>
              <td class="py-1.5 pr-3 font-mono text-xs">{h.sha.slice(0, 8)}</td>
              <td class="pr-3">{h.check}</td>
              <td class="pr-3"><Tag tone={ciTone(h.status)}>{h.status}</Tag></td>
              <td class="pr-3 text-muted">{h.reason}</td>
              <td class="pr-3">{#if h.prUrl}<a class="text-accent hover:underline" href={h.prUrl} target="_blank" rel="noopener noreferrer" onclick={(e) => e.stopPropagation()}>PR</a>{/if}</td>
              <td class="text-right font-mono text-xs">${h.costUsd.toFixed(2)}</td>
            </tr>
          {:else}
            <tr><td colspan="6" class="py-2 text-muted">No failing checks have been looked at yet.</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </Card>
</div>
