<script lang="ts">
  import { onMount } from 'svelte'
  import Card from '../../lib/ui/Card.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import { errMessage, getNotifications, listSecrets, saveNotifications, testNotifications, type NotifyEvent, type Webhook } from '../../lib/api'
  import { NOTIFY_EVENTS, loadPrefs, permission, savePrefs, type NotifyPrefs } from '../../lib/notify'

  let { onToast }: { onToast: (t: string) => void } = $props()

  let prefs = $state<NotifyPrefs>(loadPrefs())
  let perm = $state(permission())
  let webhooks = $state<Webhook[]>([])
  let refs = $state<string[]>([])
  let loaded = $state(false)
  let error = $state('')
  let busy = $state(false)

  let url = $state('')
  let secretRef = $state('')
  let events = $state<NotifyEvent[]>(NOTIFY_EVENTS.map((e) => e.id))

  onMount(async () => {
    try {
      webhooks = (await getNotifications()).webhooks
    } catch (e) {
      error = errMessage(e)
    } finally {
      loaded = true
    }
    refs = await listSecrets().catch(() => [])
  })

  async function request() {
    if (permission() === 'unsupported') return
    perm = await Notification.requestPermission()
  }

  function toggle(id: NotifyEvent, on: boolean) {
    prefs = { ...prefs, [id]: on }
    savePrefs(prefs)
  }

  async function persist(next: Webhook[]) {
    busy = true
    error = ''
    try {
      webhooks = (await saveNotifications({ webhooks: next })).webhooks
      return true
    } catch (e) {
      error = errMessage(e)
      return false
    } finally {
      busy = false
    }
  }

  async function add() {
    if (!/^https?:\/\//.test(url.trim())) {
      error = 'The URL must start with http:// or https://.'
      return
    }
    const hook: Webhook = { url: url.trim(), events: [...events], ...(secretRef ? { secretRef } : {}) }
    if (await persist([...webhooks, hook])) {
      url = ''
      secretRef = ''
      onToast('Webhook added')
    }
  }

  async function remove(i: number) {
    if (await persist(webhooks.filter((_, j) => j !== i))) onToast('Webhook removed')
  }

  async function test() {
    error = ''
    try {
      const r = await testNotifications()
      onToast(r.sent ? `Sent a test to ${r.sent} webhook${r.sent === 1 ? '' : 's'}` : 'No webhooks to test')
    } catch (e) {
      error = errMessage(e)
    }
  }

  function flip(id: NotifyEvent) {
    events = events.includes(id) ? events.filter((e) => e !== id) : [...events, id]
  }
</script>

<div class="flex flex-col gap-6">
  {#if error}<Card class="border-attention text-sm" role="alert">{error}</Card>{/if}

  <section class="flex flex-col gap-3">
    <h2 class="text-sm font-medium">In this browser</h2>
    <p class="text-xs text-muted">Notifications appear only while this tab is in the background.</p>
    <div class="flex items-center gap-2 text-sm" data-testid="permission">
      {#if perm === 'unsupported'}
        <span class="text-muted">This browser does not support notifications.</span>
      {:else if perm === 'granted'}
        <span class="text-ok">Allowed</span>
      {:else if perm === 'denied'}
        <span class="text-err">Blocked in the browser's site settings.</span>
      {:else}
        <span class="text-muted">Not asked yet.</span>
        <Button variant="ghost" onclick={request}>Request permission</Button>
      {/if}
    </div>
    <div class="flex flex-col gap-1">
      {#each NOTIFY_EVENTS as e (e.id)}
        <label class="flex items-center gap-2 text-sm">
          <input type="checkbox" aria-label="Notify: {e.label}" checked={prefs[e.id]} onchange={(ev) => toggle(e.id, ev.currentTarget.checked)} /> {e.label}
        </label>
      {/each}
    </div>
  </section>

  <section class="flex flex-col gap-3">
    <div class="flex items-center gap-2">
      <h2 class="text-sm font-medium">Webhooks</h2>
      <Button variant="ghost" class="ml-auto min-h-8 px-2 py-1 text-xs" onclick={test} disabled={webhooks.length === 0}>Test</Button>
    </div>
    {#if loaded && webhooks.length === 0}
      <p class="text-xs text-muted">No webhooks. A webhook receives a JSON POST for the events you pick, signed when it has a secret.</p>
    {/if}
    {#each webhooks as w, i (w.id ?? i)}
      <div class="flex flex-wrap items-center gap-2 rounded border border-border bg-surface p-2 text-sm" data-testid="webhook">
        <span class="min-w-0 flex-1 truncate font-mono text-xs" title={w.url}>{w.url}</span>
        <span class="text-xs text-muted">{w.events.length === NOTIFY_EVENTS.length ? 'all events' : w.events.join(', ') || 'no events'}</span>
        {#if w.secretRef}<span class="font-mono text-xs text-muted">signed · {w.secretRef}</span>{/if}
        <Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" disabled={busy} onclick={() => remove(i)}>Remove</Button>
      </div>
    {/each}

    <form
      class="flex flex-col gap-2 rounded border border-border p-3"
      aria-label="Add webhook"
      onsubmit={(e) => {
        e.preventDefault()
        void add()
      }}
    >
      <label class="flex flex-col gap-1 text-xs">URL
        <input bind:value={url} placeholder="https://hooks.example.com/…" class="rounded border border-border bg-bg p-1.5 text-sm" />
      </label>
      <label class="flex flex-col gap-1 text-xs">Signing secret
        <select bind:value={secretRef} class="rounded border border-border bg-bg p-1.5 text-sm">
          <option value="">none (unsigned)</option>
          {#each refs as r (r)}<option value={r}>{r}</option>{/each}
        </select>
      </label>
      <fieldset class="flex flex-wrap gap-x-4 gap-y-1">
        <legend class="mb-1 text-xs">Events</legend>
        {#each NOTIFY_EVENTS as e (e.id)}
          <label class="flex items-center gap-1.5 text-xs"><input type="checkbox" aria-label="Webhook event: {e.label}" checked={events.includes(e.id)} onchange={() => flip(e.id)} /> {e.label}</label>
        {/each}
      </fieldset>
      <div><Button type="submit" disabled={busy}>Add webhook</Button></div>
    </form>
  </section>
</div>
