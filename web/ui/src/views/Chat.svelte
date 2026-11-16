<script lang="ts">
  import AgentConversation from '../lib/session/AgentConversation.svelte'
  import type { ChatRoute } from '../lib/routes'
  import type { AgentRow } from '../lib/fleet'

  /*
    The full-session page is the embeddable conversation plus the chrome that
    only belongs to a page of its own: the dock, the exit panel, and the URL it
    tracks. The whole behaviour lives in lib/session so a factory item can embed
    the same conversation instead of a second, thinner one.
  */
  interface Props {
    sessionId: string
    onBack: () => void
    /** The parsed #chat route; read once, for the dock state it carries. */
    route?: ChatRoute | null
    /** This agent's live fleet row, for its gate and change count. */
    agent?: AgentRow
  }

  let { sessionId, onBack, route = null, agent = undefined }: Props = $props()
</script>

<AgentConversation {sessionId} {onBack} {route} {agent} embedded={false} />
