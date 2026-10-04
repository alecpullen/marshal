<script lang="ts">
  import Card from '../ui/Card.svelte'
  import Button from '../ui/Button.svelte'
  import Tag from '../ui/Tag.svelte'
  import Sparkline from './Sparkline.svelte'
  import { thresholdOf } from './spark'
  import { stopWatch, errMessage, type WatchInfo } from '../api'

  let {
    watch,
    ownerName,
    onStopped,
    onClose,
    onOpenAgent,
  }: { watch: WatchInfo; ownerName: string; onStopped: () => void; onClose: () => void; onOpenAgent: (id: string) => void } = $props()

  let error = $state('')
  let busy = $state(false)
  const threshold = $derived(thresholdOf(watch.condition))
  const trips = $derived(watch.samples.filter((s) => s.tripped))

  async function stop() {
    busy = true
    error = ''
    try {
      await stopWatch(watch.agentId, watch.id)
      onStopped()
    } catch (e) {
      error = errMessage(e)
    } finally {
      busy = false
    }
  }
</script>

<Card class="flex flex-col gap-3" data-testid="watch-detail">
  <div class="flex items-center gap-2">
    <h2 class="text-sm font-semibold">{watch.name}</h2>
    <Tag tone={watch.state === 'fired' ? 'warn' : watch.state === 'error' ? 'err' : 'ok'}>{watch.state}</Tag>
    <span class="flex-1"></span>
    <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={onClose}>Close</Button>
  </div>

  {#if error}<p class="text-sm text-err" role="alert">{error}</p>{/if}

  <Sparkline samples={watch.samples} w={640} h={120} {threshold} marks label="Samples over the last 24 hours" />
  <p class="text-xs text-muted">
    {#if threshold !== null}Dashed line: threshold {threshold}. {/if}{trips.length} tripped sample{trips.length === 1 ? '' : 's'} in the last 24 hours (red).
  </p>

  <dl class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
    <dt class="text-muted">Condition</dt><dd class="font-mono text-xs">{watch.condition || 'change'}</dd>
    <dt class="text-muted">Owner</dt>
    <dd>
      {#if watch.agentId === 'studio'}Studio{:else}<button class="cursor-pointer text-accent underline" onclick={() => onOpenAgent(watch.agentId)}>{ownerName}</button>{/if}
    </dd>
    <dt class="text-muted">Fired</dt><dd>{watch.fireCount} time{watch.fireCount === 1 ? '' : 's'}</dd>
    <dt class="text-muted">Last sample</dt>
    <dd class="max-h-32 overflow-auto font-mono text-xs break-all whitespace-pre-wrap" data-testid="last-sample">{watch.lastError || watch.lastSample || 'none yet'}</dd>
  </dl>

  <div><Button variant="danger" disabled={busy || watch.state === 'stopped'} onclick={stop}>Stop</Button></div>
</Card>
