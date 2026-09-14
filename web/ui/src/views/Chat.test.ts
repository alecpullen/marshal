import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from 'vitest'
import { render, cleanup, screen } from '@testing-library/svelte'
import Chat from './Chat.svelte'
import * as api from '../lib/api.js'

/*
  Chat is the whole room: header, transcript, composer, exit panel, and a
  session store whose load() is the bridge's verdict on whether the
  routed session still maps to a live agent. The api mock keeps all of
  that off fetch while the real APIError comes along, so the 404 the
  component matches with instanceof is the same class the mock throws.
  listAgents resolving [] also keeps ExitPanel from finding an isolated
  agent, so it renders nothing rather than a diff view.
*/
vi.mock('../lib/api.js', async (importActual) => {
  const actual = await importActual<typeof import('../lib/api.js')>()
  return {
    ...actual,
    listAgents: vi.fn(),
    loadSession: vi.fn(),
  }
})

/*
  The real connectSSE opens a fetch-and-reconnect loop; the store only
  needs the connect/disconnect pair to exist. The chat SSE stream is not
  what this test is about.
*/
vi.mock('../lib/sse.js', () => ({
  connectSSE: vi.fn(() => () => {}),
}))

/*
  Shiki's grammars are a heavyweight async load with nothing to do with
  the 404 path, and the transcript it highlights is empty here anyway.
*/
vi.mock('../lib/markdown.js', () => ({
  renderMarkdown: vi.fn((s: string) => s),
  initHighlighter: vi.fn(() => Promise.resolve()),
}))

afterEach(cleanup)

describe('Chat', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    ;(api.listAgents as Mock).mockResolvedValue([])
  })

  it('replaces the transcript with a could-not-be-resumed note when the load 404s', async () => {
    ;(api.loadSession as Mock).mockRejectedValue(
      new api.APIError(404, { error: 'no such session' }),
    )

    render(Chat, { sessionId: 'gone', onBack: () => {} })

    expect(
      await screen.findByText(
        'This session could not be resumed — its agent is no longer tracked by the bridge.',
      ),
    ).toBeTruthy()
    // The header stays reachable so the operator is not stranded in the note.
    expect(screen.getByRole('button', { name: '← Fleet' })).toBeTruthy()
    // The transcript itself is replaced, not supplemented.
    expect(screen.queryByText('No messages yet.')).toBeNull()
  })

  it('keeps the ordinary empty transcript when the load fails for a non-404 reason', async () => {
    ;(api.loadSession as Mock).mockRejectedValue(new api.APIError(500, { error: 'boom' }))

    render(Chat, { sessionId: 's1', onBack: () => {} })

    expect(await screen.findByText('No messages yet.')).toBeTruthy()
    expect(
      screen.queryByText(
        'This session could not be resumed — its agent is no longer tracked by the bridge.',
      ),
    ).toBeNull()
  })
})
