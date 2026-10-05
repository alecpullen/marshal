<script lang="ts">
  import { onMount } from 'svelte'
  import { Terminal } from '@xterm/xterm'
  import { FitAddon } from '@xterm/addon-fit'
  import '@xterm/xterm/css/xterm.css'
  import { closeTerminal, errMessage, terminalEventsUrl, terminalInput, terminalResize, type TerminalOpen, type TerminalSize, type TerminalTarget } from '../api'
  import { connectSSE } from '../sse'

  /*
    One shell, streamed. Output arrives as base64 chunks on an SSE stream the
    bridge replays from the start, so reopening the tab shows what scrolled by.
    Input goes out in order, one request at a time, in pieces the bridge's
    64 KiB cap accepts.
  */
  let {
    target,
    open,
    onClose,
    onInput,
    onOutput,
  }: {
    target: TerminalTarget
    open: (size: TerminalSize) => Promise<TerminalOpen>
    onClose?: () => void
    /** Called on every keystroke batch, so the host can restart its hand-back countdown. */
    onInput?: () => void
    onOutput?: () => void
  } = $props()

  const CHUNK = 16 * 1024

  let host = $state<HTMLDivElement | null>(null)
  let exited = $state<number | null>(null)
  let error = $state('')

  function themeColor(name: string, fallback: string): string {
    try {
      return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback
    } catch {
      return fallback
    }
  }

  function decode(b64: string): Uint8Array {
    const bin = atob(b64)
    const out = new Uint8Array(bin.length)
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
    return out
  }

  onMount(() => {
    if (!host) return
    const term = new Terminal({
      fontFamily: 'Geist Mono Variable, ui-monospace, monospace',
      fontSize: 12,
      cursorBlink: true,
      theme: {
        background: themeColor('--color-bg', '#121113'),
        foreground: themeColor('--color-fg', '#ece9e4'),
        cursor: themeColor('--color-accent', '#ff875f'),
      },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(host)
    try {
      fit.fit()
    } catch {
      // A hidden or zero-sized host fits on the first resize.
    }

    let tid = ''
    let stopped = false
    let stopSSE = () => {}
    let sending: Promise<void> = Promise.resolve()

    const send = (text: string) => {
      onInput?.()
      for (let i = 0; i < text.length; i += CHUNK) {
        const piece = text.slice(i, i + CHUNK)
        sending = sending.then(() => (tid ? terminalInput(target, tid, piece).catch(() => {}) : undefined))
      }
    }
    term.onData(send)
    term.onResize(({ cols, rows }) => {
      if (tid) terminalResize(target, tid, { cols, rows }).catch(() => {})
    })

    const ro = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(() => {
      try {
        fit.fit()
      } catch {
        // Fitting a detached host is a no-op.
      }
    })
    ro?.observe(host)

    open({ cols: term.cols, rows: term.rows })
      .then((r) => {
        if (stopped) {
          closeTerminal(target, r.terminalId).catch(() => {})
          return
        }
        tid = r.terminalId
        stopSSE = connectSSE({
          url: terminalEventsUrl(target, tid),
          // The bridge replays the terminal's retained output only from id 0.
          resume: false,
          onEvent: (e) => {
            if (e.type !== 'message') return
            try {
              const v = JSON.parse(e.message.data) as { data?: string; exit?: number }
              if (typeof v.data === 'string') {
                term.write(decode(v.data))
                onOutput?.()
              } else if (typeof v.exit === 'number') {
                exited = v.exit
                term.write(`\r\n[process exited (${v.exit})]\r\n`)
                stopSSE()
              }
            } catch {
              // A malformed chunk is skipped; the stream carries on.
            }
          },
        })
      })
      .catch((e) => (error = errMessage(e)))

    return () => {
      stopped = true
      stopSSE()
      ro?.disconnect()
      if (tid) closeTerminal(target, tid).catch(() => {})
      term.dispose()
    }
  })
</script>

<div class="flex min-h-0 flex-1 flex-col gap-1" data-testid="terminal">
  {#if error}<p class="rounded border border-border bg-bg p-2 text-xs text-err" role="alert">{error}</p>{/if}
  <div bind:this={host} class="min-h-48 flex-1 overflow-hidden rounded bg-bg p-1"></div>
  {#if exited !== null}
    <div class="flex items-center gap-2 text-xs text-muted">
      <span>process exited ({exited})</span>
      {#if onClose}<button type="button" class="rounded border border-border px-1.5 py-0.5 hover:text-fg" onclick={onClose}>Close</button>{/if}
    </div>
  {/if}
</div>
