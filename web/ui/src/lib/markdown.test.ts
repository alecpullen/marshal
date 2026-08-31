import { describe, it, expect, beforeAll } from 'vitest'
import { renderMarkdown, initHighlighter } from './markdown'

beforeAll(async () => {
  await initHighlighter()
})

describe('renderMarkdown', () => {
  it('renders structure an agent actually emits', () => {
    const html = renderMarkdown('# Title\n\n- one\n- two\n\nSome `inline` code.')
    expect(html).toContain('<h1')
    expect(html).toContain('<li>one</li>')
    expect(html).toContain('<code>inline</code>')
  })

  it('highlights a fenced go block', () => {
    const html = renderMarkdown('```go\nfunc main() {}\n```')
    // Shiki emits inline styles on spans; the point is that the block was
    // tokenised rather than dumped as plain text.
    expect(html).toContain('<pre')
    expect(html).toContain('style=')
    expect(html).toContain('func')
  })

  it('falls back to a plain block for an unknown language', () => {
    const html = renderMarkdown('```brainfuck\n+[-]\n```')
    expect(html).toContain('<pre')
    expect(html).toContain('+[-]')
  })

  it('renders a fence with no language', () => {
    const html = renderMarkdown('```\nplain text\n```')
    expect(html).toContain('<pre')
    expect(html).toContain('plain text')
  })

  // Agent output is model- and tool-generated. It reaches the DOM through
  // {@html}, so anything executable must not survive.
  it('strips script tags', () => {
    const html = renderMarkdown('hello <script>alert(1)</script> world')
    expect(html).not.toContain('<script')
    expect(html).not.toContain('alert(1)')
  })

  it('strips inline event handlers', () => {
    const html = renderMarkdown('<img src=x onerror="alert(1)">')
    expect(html).not.toContain('onerror')
  })

  it('strips javascript: urls', () => {
    const html = renderMarkdown('[click](javascript:alert(1))')
    expect(html).not.toContain('javascript:')
  })

  it('keeps an ordinary link', () => {
    const html = renderMarkdown('[docs](https://example.com)')
    expect(html).toContain('href="https://example.com"')
  })

  it('escapes html in a code fence rather than executing it', () => {
    const html = renderMarkdown('```\n<script>alert(1)</script>\n```')
    expect(html).not.toContain('<script>alert(1)</script>')
    expect(html).toContain('&lt;script&gt;')
  })

  it('returns an empty string for empty input', () => {
    expect(renderMarkdown('')).toBe('')
  })
})
