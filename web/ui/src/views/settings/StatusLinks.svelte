<script lang="ts">
  import { onMount } from 'svelte'
  import Card from '../../lib/ui/Card.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Tag from '../../lib/ui/Tag.svelte'
  import { errMessage, listStatusLinks, revokeStatusLink, type StatusLink } from '../../lib/api'

  let { onToast }: { onToast: (t: string) => void } = $props()

  let links = $state<StatusLink[]>([])
  let loaded = $state(false)
  let error = $state('')

  const linkState = (l: StatusLink): 'revoked' | 'expired' | 'active' =>
    l.revokedAt ? 'revoked' : Date.parse(l.expiresAt) <= Date.now() ? 'expired' : 'active'
  const day = (iso: string) => (Number.isFinite(Date.parse(iso)) ? new Date(iso).toLocaleString() : iso)

  async function load() {
    try {
      links = await listStatusLinks()
      error = ''
    } catch (e) {
      error = errMessage(e)
    } finally {
      loaded = true
    }
  }
  onMount(load)

  async function revoke(l: StatusLink) {
    try {
      await revokeStatusLink(l.id)
      links = links.map((x) => (x.id === l.id ? { ...x, revokedAt: new Date().toISOString() } : x))
      onToast('Link revoked')
    } catch (e) {
      error = errMessage(e)
    }
  }
</script>

<div class="flex flex-col gap-3">
  {#if error}<Card class="border-attention text-sm" role="alert">{error}</Card>{/if}
  <p class="text-xs text-muted">Status links show an agent's progress to someone without the bearer token. Create one from a session or run with Share status.</p>
  {#if loaded && links.length === 0}
    <p class="text-sm text-muted">No status links.</p>
  {:else if links.length}
    <table class="w-full text-left text-sm">
      <thead class="text-xs text-muted"><tr><th class="py-1 pr-3">Agent</th><th class="pr-3">Created</th><th class="pr-3">Expires</th><th class="pr-3">State</th><th></th></tr></thead>
      <tbody>
        {#each links as l (l.id)}
          <tr class="border-t border-border" data-testid="status-link">
            <td class="py-2 pr-3"><a class="text-accent underline" href="#chat/{encodeURIComponent(l.agentId)}">{l.agentId}</a></td>
            <td class="pr-3 text-xs">{day(l.createdAt)}</td>
            <td class="pr-3 text-xs">{day(l.expiresAt)}</td>
            <td class="pr-3"><Tag tone={linkState(l) === 'active' ? 'ok' : 'neutral'}>{linkState(l)}</Tag></td>
            <td class="text-right">
              {#if linkState(l) === 'active'}<Button variant="ghost" class="min-h-8 px-2 py-1 text-xs" onclick={() => revoke(l)}>Revoke</Button>{/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>
