<script lang="ts">
  import { Dialog } from 'bits-ui'

  /*
    The shared dialog for decisions the agent is blocked on.

    Marshal's permission and question waits have no wall-clock timeout —
    web/bridge/registry.go:387 says so explicitly ("a user reading and
    deciding is not a hung request"). So a modal that can be dismissed
    without producing a decision does not just close a box: it leaves the
    agent parked until the session is cancelled or the child restarts.

    That is why Escape maps to the caller's negative action rather than
    merely closing, and why clicking outside does nothing at all. An
    accidental click on the backdrop must not be able to strand an agent,
    and Escape must always resolve to an answer the agent receives.

    escapeKeydownBehavior is "close" rather than "ignore" because bits-ui
    only invokes onEscapeKeydown for the close behaviours — with "ignore"
    it returns before the callback (use-escape-layer.svelte.js:36-39), so
    the stricter-sounding option is the one that silently strands the
    agent. Nothing is lost: `open` is a fixed prop, so the caller's {#if}
    still owns unmounting, and onDismiss is what actually resolves.
  */
  interface Props {
    title: string
    /** The negative resolution — deny, decline. Escape maps to this. */
    onDismiss: () => void | Promise<void>
    description?: string
    children: import('svelte').Snippet
    footer?: import('svelte').Snippet
  }

  let { title, onDismiss, description, children, footer }: Props = $props()
</script>

<Dialog.Root open={true}>
  <Dialog.Portal>
    <Dialog.Overlay class="fixed inset-0 z-100 bg-black/60" />
    <Dialog.Content
      escapeKeydownBehavior="close"
      interactOutsideBehavior="ignore"
      onEscapeKeydown={() => onDismiss()}
      class="fixed top-1/2 left-1/2 z-100 flex max-h-[80vh] w-[90%] max-w-xl -translate-x-1/2 -translate-y-1/2 flex-col gap-3 overflow-auto rounded-xl border border-border bg-surface p-6 shadow-xl"
    >
      <Dialog.Title class="text-base font-semibold">{title}</Dialog.Title>
      {#if description}
        <Dialog.Description class="text-sm text-muted">{description}</Dialog.Description>
      {/if}

      {@render children()}

      {#if footer}
        <div class="mt-1 flex justify-end gap-2">{@render footer()}</div>
      {/if}
    </Dialog.Content>
  </Dialog.Portal>
</Dialog.Root>
