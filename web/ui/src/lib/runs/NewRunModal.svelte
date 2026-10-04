<script lang="ts">
  import Modal from '../ui/Modal.svelte'
  import Button from '../ui/Button.svelte'
  import Segmented from '../ui/Segmented.svelte'
  import { BudgetError, budgetMessage, errMessage, startRun, type ProjectStatus, type RunRequest } from '../api'
  import type { AgentRow } from '../fleet'
  import { shortName } from '../utils'

  let {
    agents,
    projects,
    onClose,
    onStarted,
  }: {
    agents: AgentRow[]
    projects: ProjectStatus[]
    onClose: () => void
    onStarted: (agentId: string) => void
  } = $props()

  // Only an idle agent can take a run; a busy one is mid-turn.
  const idle = $derived(agents.filter((a) => a.status === 'idle'))

  // svelte-ignore state_referenced_locally
  let target = $state<'agent' | 'project'>(idle.length > 0 ? 'agent' : 'project')
  // svelte-ignore state_referenced_locally
  let agentId = $state(idle[0]?.id ?? '')
  // svelte-ignore state_referenced_locally
  let project = $state(projects[0]?.root ?? '')
  let kind = $state<'sdd' | 'swarm'>('sdd')
  let planSource = $state<'paste' | 'path'>('paste')
  let plan = $state('')
  let planPath = $state('')
  let goal = $state('')
  let error = $state('')
  let busy = $state(false)

  function body(): RunRequest | string {
    const req: RunRequest = { kind }
    if (target === 'agent') {
      if (!agentId) return 'Choose an agent.'
      req.agentId = agentId
    } else {
      if (!project) return 'Choose a project.'
      req.project = project
    }
    if (kind === 'sdd') {
      if (planSource === 'paste') {
        if (!plan.trim()) return 'Paste a plan.'
        req.plan = plan
      } else {
        if (!planPath.trim()) return 'Enter the plan path.'
        req.planPath = planPath.trim()
      }
    } else {
      if (!goal.trim()) return 'Describe the goal.'
      req.goal = goal.trim()
    }
    return req
  }

  async function submit() {
    const req = body()
    if (typeof req === 'string') {
      error = req
      return
    }
    busy = true
    error = ''
    try {
      const r = await startRun(req)
      onStarted(r.agentId || req.agentId || '')
    } catch (e) {
      error = e instanceof BudgetError ? budgetMessage(e) : errMessage(e)
    } finally {
      busy = false
    }
  }

  const field = 'w-full rounded-md border border-border bg-bg px-2 py-1.5 text-sm'
</script>

<Modal title="New run" description="Start a plan or swarm run." onDismiss={onClose}>
  <div class="flex flex-col gap-3">
    <Segmented
      label="Target"
      value={target}
      onchange={(v) => (target = v === 'agent' ? 'agent' : 'project')}
      options={[
        { value: 'agent', label: 'Existing agent' },
        { value: 'project', label: 'New agent' },
      ]}
    />
    {#if target === 'agent'}
      <label class="flex flex-col gap-1 text-xs text-muted">
        Agent
        <select class={field} bind:value={agentId} aria-label="Agent">
          {#each idle as a (a.id)}
            <option value={a.id}>{a.name || a.id} · {shortName(a.project)}</option>
          {:else}
            <option value="">No idle agents</option>
          {/each}
        </select>
      </label>
    {:else}
      <label class="flex flex-col gap-1 text-xs text-muted">
        Project
        <select class={field} bind:value={project} aria-label="Project">
          {#each projects as p (p.root)}
            <option value={p.root}>{shortName(p.root)}</option>
          {/each}
        </select>
      </label>
    {/if}

    <Segmented
      label="Kind"
      value={kind}
      onchange={(v) => (kind = v === 'swarm' ? 'swarm' : 'sdd')}
      options={[
        { value: 'sdd', label: 'Plan' },
        { value: 'swarm', label: 'Swarm' },
      ]}
    />
    {#if kind === 'sdd'}
      <Segmented
        label="Plan source"
        value={planSource}
        onchange={(v) => (planSource = v === 'path' ? 'path' : 'paste')}
        options={[
          { value: 'paste', label: 'Paste' },
          { value: 'path', label: 'Path in project' },
        ]}
      />
      {#if planSource === 'paste'}
        <textarea class={field} rows="8" placeholder="Paste the plan (markdown, one ## Task per task)" aria-label="Plan" bind:value={plan}></textarea>
      {:else}
        <input class={field} placeholder="docs/plans/my-plan.md" aria-label="Plan path" bind:value={planPath} />
      {/if}
    {:else}
      <textarea class={field} rows="3" placeholder="What should the swarm achieve?" aria-label="Goal" bind:value={goal}></textarea>
    {/if}

    {#if error}<p class="text-sm text-err" role="alert">{error}</p>{/if}
  </div>
  {#snippet footer()}
    <Button variant="ghost" onclick={onClose}>Cancel</Button>
    <Button onclick={submit} disabled={busy}>Start run</Button>
  {/snippet}
</Modal>
