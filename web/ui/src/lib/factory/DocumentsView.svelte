<script lang="ts">
  import Card from '../ui/Card.svelte'
  import Tag from '../ui/Tag.svelte'
  import type { FactoryItem } from '../factory'

  /**
   * One work item's documents (design C5): the plan and specification the work
   * is being done against. An approved item is judged against the latest
   * recorded approval revision; a draft against what was handed off. Earlier
   * revisions stay readable — an approval is a record, so the reason a plan
   * changed has to remain visible — but they are collapsed, because the
   * current document is the one that governs.
   *
   * It renders `item` only: nothing here decides which revision is current.
   */
  export interface DocumentsViewProps {
    item: FactoryItem
  }

  let { item }: DocumentsViewProps = $props()

  const approvals = $derived(item.approvals ?? [])
  const latest = $derived(approvals.at(-1) ?? null)
  const prior = $derived(approvals.slice(0, -1).reverse())
  // The approval carries its own plan and spec; the draft carries the handoff's.
  const current = $derived(
    latest
      ? { revision: latest.revision, plan: latest.plan, spec: latest.spec, approvedAt: latest.approvedAt }
      : { revision: 0, plan: item.draft?.plan ?? '', spec: item.draft?.spec ?? '', approvedAt: '' },
  )
  const heading = $derived(latest ? `Approved revision ${current.revision}` : 'Draft handoff')
</script>

<Card class="grid min-w-0 gap-3" data-testid="documents-view">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <h2 class="font-semibold">Documents</h2>
    <Tag tone={latest ? 'ok' : 'neutral'}>{heading}</Tag>
  </div>
  {#if latest?.approvedAt}<p class="text-xs text-muted">Approved {latest.approvedAt}</p>{/if}
  {#if !current.plan && !current.spec}
    <p class="text-sm text-muted">No plan or specification recorded.</p>
  {:else}
    {#if current.plan}
      <section class="grid gap-1" data-testid="document-plan">
        <h3 class="text-sm font-semibold">Plan</h3>
        <p class="whitespace-pre-wrap break-words text-sm">{current.plan}</p>
      </section>
    {/if}
    {#if current.spec}
      <section class="grid gap-1" data-testid="document-spec">
        <h3 class="text-sm font-semibold">Specification</h3>
        <p class="whitespace-pre-wrap break-words text-sm">{current.spec}</p>
      </section>
    {/if}
  {/if}
  {#if prior.length}
    <div class="grid gap-2">
      <h3 class="text-sm font-semibold">Earlier revisions</h3>
      {#each prior as approval (approval.revision)}
        <details data-testid="previous-revision">
          <summary class="text-sm text-accent">Revision {approval.revision}{#if approval.approvedAt} · approved {approval.approvedAt}{/if}</summary>
          <div class="grid gap-2 pt-2">
            <p class="whitespace-pre-wrap break-words text-sm">{approval.plan}</p>
            {#if approval.spec}<p class="whitespace-pre-wrap break-words text-sm text-muted">{approval.spec}</p>{/if}
          </div>
        </details>
      {/each}
    </div>
  {/if}
</Card>
