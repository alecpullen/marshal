import { Marked } from 'marked'
import DOMPurify from 'dompurify'
import { createHighlighterCore, type HighlighterCore } from 'shiki/core'
import { createJavaScriptRegexEngine } from 'shiki/engine/javascript'

/*
  Agent output is markdown, and marshal is a coding agent, so fenced code
  is the majority of what a reader is here to read. It is rendered through
  {@html}, which means sanitising is a correctness requirement rather than
  a precaution: the text is produced by a model and by tool results, and
  neither is trusted input.

  The language set is deliberately bounded. It matches what
  internal/repo/symbols.go extracts, plus the shells an agent runs
  commands in, so the grammar list has a reason to be this size rather
  than being whatever seemed useful. Every grammar is bytes in the Go
  binary — web/bridge/assets.go embeds the built SPA.
*/
const LANGS = ['go', 'typescript', 'javascript', 'python', 'rust', 'bash', 'shell'] as const

// Aliases an agent is likely to write in a fence info string.
const ALIASES: Record<string, string> = {
  ts: 'typescript',
  tsx: 'typescript',
  js: 'javascript',
  jsx: 'javascript',
  py: 'python',
  rs: 'rust',
  golang: 'go',
  sh: 'bash',
  zsh: 'bash',
  console: 'shell',
}

let highlighter: HighlighterCore | null = null

/*
  Shiki's highlighter is async to build but synchronous to use. Building it
  once up front keeps renderMarkdown synchronous, which matters because it
  is called from a $derived in the transcript: an async render there would
  make every message a promise and reorder streaming output.

  The JavaScript regex engine is used rather than the default oniguruma
  one so no WASM blob is shipped.
*/
export async function initHighlighter(): Promise<void> {
  if (highlighter) return
  highlighter = await createHighlighterCore({
    themes: [import('shiki/themes/github-dark-default.mjs')],
    langs: [
      import('shiki/langs/go.mjs'),
      import('shiki/langs/typescript.mjs'),
      import('shiki/langs/javascript.mjs'),
      import('shiki/langs/python.mjs'),
      import('shiki/langs/rust.mjs'),
      import('shiki/langs/bash.mjs'),
      import('shiki/langs/shell.mjs'),
    ],
    engine: createJavaScriptRegexEngine(),
  })
}

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}

/*
  A fence whose language we do not carry, or any fence before the
  highlighter has loaded, still has to render — as an escaped plain block.
  Dropping it or showing a spinner would lose the content that matters
  most.
*/
function renderCode(code: string, lang: string): string {
  const resolved = ALIASES[lang] ?? lang
  const known = (LANGS as readonly string[]).includes(resolved)
  if (!highlighter || !known) {
    return `<pre class="shiki-fallback"><code>${escapeHtml(code)}</code></pre>`
  }
  return highlighter.codeToHtml(code, { lang: resolved, theme: 'github-dark-default' })
}

const marked = new Marked({
  gfm: true,
  breaks: true,
  renderer: {
    code({ text, lang }: { text: string; lang?: string }) {
      return renderCode(text, (lang ?? '').trim().toLowerCase())
    },
  },
})

/*
  Shiki emits colour as inline styles on spans, so the sanitiser has to
  keep `style` and `class` or every code block renders unstyled. That is
  safe: DOMPurify parses and filters style values, and the dangerous
  surface here is script execution and javascript: URLs, both of which
  stay blocked.
*/
const PURIFY_CONFIG = {
  ADD_ATTR: ['style', 'class'],
  FORBID_TAGS: ['style', 'form', 'input', 'button', 'iframe', 'object', 'embed'],
}

export function renderMarkdown(source: string): string {
  if (!source) return ''
  const raw = marked.parse(source, { async: false }) as string
  return DOMPurify.sanitize(raw, PURIFY_CONFIG)
}
