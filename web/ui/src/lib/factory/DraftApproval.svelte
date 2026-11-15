<script lang="ts">
  import Button from '../ui/Button.svelte'
  import EffectiveConfigSummary from './EffectiveConfigSummary.svelte'
  import { resolvedConfigRows } from './config'
  import type { FactoryItem } from '../factory'

  /**
   * The saved draft's approval (design C5): the same resolved configuration
   * creation previews, then the one decision that authorizes it. Approving is
   * the explicit act — nothing here starts work, so the copy states what the
   * approval authorizes rather than what the run has done or will do.
   *
   * The caller owns the command lifecycle: `busy` is passed in and `onApprove`
   * is the only way out, so this component never holds state of its own.
   */
  export interface DraftApprovalProps {
    item: FactoryItem
    busy: boolean
    onApprove: () => void
  }

  let { item, busy, onApprove }: DraftApprovalProps = $props()

  const rows = $derived(resolvedConfigRows(item.draft.config))
</script>

<div class="grid gap-3">
  <h2 class="font-semibold">Review draft before starting</h2>
  <EffectiveConfigSummary
    {rows}
    title="Resolved configuration"
    consequence="Approving this draft authorizes execution under the resolved configuration above. Recording the approval starts nothing by itself."
    plan={item.draft.plan}
    spec={item.draft.spec}
  />
  <div><Button disabled={busy} onclick={onApprove}>Approve &amp; start</Button></div>
</div>
