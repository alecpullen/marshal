<script lang="ts">
  import Card from '../../lib/ui/Card.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Tag from '../../lib/ui/Tag.svelte'
  import DraftView from '../../lib/automations/DraftView.svelte'
  import WebhookSetup from '../../lib/automations/WebhookSetup.svelte'
  import { SEVERITIES, severityCounts, severityTone } from '../../lib/automations/model'
  import { errMessage, getProjectSettings, listRepos, listReviewDrafts, putProjectSettings, type ProjectSettings, type RepoRow, type ReviewBotSettings, type ReviewDraft } from '../../lib/api'
  import { formatAutomationRoute, formatProjectRoute } from '../../lib/routes'
  import { shortName } from '../../lib/utils'

  let { root, draftId, refreshTick = 0, onNavigate }: { root: string; draftId?: string; /** Bumped when an `automation` delta arrives. */ refreshTick?: number; onNavigate: (hash: string) => void } = $props()

  const blank: ReviewBotSettings = { enabled: false, repoId: '', skipDrafts: true, autoPost: false, holdSeverities: [] }
  let saved = $state<ProjectSettings>({ intake: {} })
  let repos = $state<RepoRow[]>([])
  let drafts = $state<ReviewDraft[]>([])
  let loaded = $state(false)
  let saving = $state(false)
  let error = $state('')
  let notice = $state('')

  let enabled = $state(false)
  let repoId = $state('')
  let labels = $state('')
  let skipDrafts = $state(true)
  let authors = $state('')
  let autoPost = $state(false)
  let hold = $state<string[]>([])

  const list = (s: string) => s.split(',').map((x) => x.trim()).filter(Boolean)

  function adopt(s: ProjectSettings) {
    saved = s
    const b = s.automations?.reviewBot ?? blank
    enabled = b.enabled
    repoId = b.repoId
    labels = (b.labels ?? []).join(', ')
    skipDrafts = b.skipDrafts
    authors = (b.authors ?? []).join(', ')
    autoPost = b.autoPost
    hold = [...(b.holdSeverities ?? [])]
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
  async function loadDrafts() {
    try {
      drafts = (await listReviewDrafts({ project: root })).sort((a, b) => b.createdAt.localeCompare(a.createdAt))
    } catch (e) {
      error = errMessage(e)
    }
  }

  $effect(() => {
    void load()
  })
  $effect(() => {
    void refreshTick
    void loadDrafts()
  })

  async function save() {
    saving = true
    error = ''
    notice = ''
    // Routing has no editor on this page; the saved value rides along unchanged.
    const reviewBot: ReviewBotSettings = {
      ...saved.automations?.reviewBot,
      enabled,
      repoId,
      labels: list(labels),
      skipDrafts,
      authors: list(authors),
      autoPost,
      holdSeverities: hold,
    }
    try {
      adopt(await putProjectSettings(root, { ...saved, automations: { ...saved.automations, reviewBot } }))
      notice = 'Review bot settings saved'
    } catch (e) {
      error = errMessage(e)
    } finally {
      saving = false
    }
  }

  const toggleHold = (s: string, on: boolean) => (hold = on ? [...new Set([...hold, s])] : hold.filter((x) => x !== s))
  const repoChoices = $derived([...new Set([...repos.map((r) => r.id), ...(repoId ? [repoId] : [])])])
  const open = $derived(drafts.find((d) => d.id === draftId))
  const repoOf = (id: string) => repos.find((r) => r.id === id)
  const prLink = (d: ReviewDraft) => {
    const url = repoOf(d.repoId)?.url
    return url && /^https?:\/\//.test(url) ? `${url.replace(/\.git$/, '').replace(/\/$/, '')}/pull/${d.number}` : null
  }

  // A post or discard changes the draft: replace it in the list, or drop it.
  function changed(id: string, d: ReviewDraft | null) {
    drafts = d ? drafts.map((x) => (x.id === id ? d : x)) : drafts.map((x) => (x.id === id ? { ...x, status: 'discarded' as const } : x))
  }
  const field = 'rounded border border-border bg-bg p-2 text-sm'
</script>

<div class="mx-auto flex max-w-5xl flex-col gap-4 p-6">
  <header>
    <p class="text-xs text-muted"><a class="text-accent hover:underline" href="#projects">Projects</a> / <a class="text-accent hover:underline" href={formatProjectRoute(root)}>{shortName(root)}</a> /</p>
    <h1 class="text-lg font-semibold">Review bot</h1>
  </header>

  {#if error}<Card class="border-attention text-sm" role="alert">{error}</Card>{/if}
  {#if notice}<Card class="text-sm" role="status">{notice}</Card>{/if}

  <div class="grid gap-4 lg:grid-cols-2">
    <Card class="flex flex-col gap-3" data-testid="review-settings">
      <h2 class="text-sm font-semibold">Settings</h2>
      <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={enabled} /> Review new pull requests</label>
      <label class="flex flex-col gap-1 text-xs">Repo
        <select aria-label="Review repo" bind:value={repoId} class={field}>
          <option value="">None</option>
          {#each repoChoices as r (r)}<option value={r}>{r}</option>{/each}
        </select>
      </label>
      <label class="flex flex-col gap-1 text-xs">Labels
        <input aria-label="Labels" bind:value={labels} placeholder="comma separated; empty reviews every PR" class={field} />
      </label>
      <label class="flex flex-col gap-1 text-xs">Authors
        <input aria-label="Authors" bind:value={authors} placeholder="comma separated; empty means anyone" class={field} />
      </label>
      <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={skipDrafts} /> Skip draft pull requests</label>
      <p class="text-xs text-muted">Model: {saved.automations?.reviewBot?.routing?.profile ?? 'the project default'}</p>
      <label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={autoPost} /> Post reviews automatically</label>
      <fieldset class="flex flex-col gap-1 text-xs" disabled={!autoPost}>
        <legend class="mb-1">Hold for me when a finding is</legend>
        <div class="flex flex-wrap gap-3">
          {#each SEVERITIES as s (s)}
            <label class="flex items-center gap-1.5 text-sm"><input type="checkbox" checked={hold.includes(s)} onchange={(e) => toggleHold(s, e.currentTarget.checked)} /> {s}</label>
          {/each}
        </div>
      </fieldset>
      <WebhookSetup {repoId} />
      <div><Button onclick={save} disabled={saving || !loaded}>Save settings</Button></div>
    </Card>

    <Card class="flex flex-col gap-3" data-testid="drafts-card">
      <h2 class="text-sm font-semibold">Recent reviews</h2>
      <ul class="flex flex-col gap-2">
        {#each drafts as d (d.id)}
          <li>
            <a
              class="flex flex-wrap items-center gap-2 rounded-md border border-border p-2 text-sm hover:bg-surface {d.id === draftId ? 'bg-surface' : ''}"
              href={formatAutomationRoute(root, 'review-bot', d.id)}
              data-testid="draft-row"
              onclick={(e) => { e.preventDefault(); onNavigate(formatAutomationRoute(root, 'review-bot', d.id)) }}
            >
              <span class="font-medium">PR #{d.number}</span>
              <Tag tone={d.status === 'posted' ? 'ok' : d.status === 'draft' ? 'accent' : 'neutral'}>{d.status}</Tag>
              {#each severityCounts(d.findings) as c (c.severity)}<Tag tone={severityTone(c.severity)}>{c.count} {c.severity}</Tag>{/each}
              <span class="flex-1"></span>
              <span class="text-xs text-muted">{new Date(d.createdAt).toLocaleString()}</span>
            </a>
            {#if prLink(d)}<a class="ml-2 text-xs text-accent hover:underline" href={prLink(d)} target="_blank" rel="noopener noreferrer">Open PR on the forge</a>{/if}
          </li>
        {:else}
          <li class="text-sm text-muted">No reviews yet.</li>
        {/each}
      </ul>
    </Card>
  </div>

  {#if draftId}
    <Card class="p-4">
      {#if open}
        {#key open.id}<DraftView draft={open} repo={repoOf(open.repoId)} onChange={(d) => changed(open.id, d)} />{/key}
      {:else}
        <p class="text-sm text-muted">That review is not in this project's list.</p>
      {/if}
    </Card>
  {/if}
</div>
