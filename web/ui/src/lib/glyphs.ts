// The TUI glyph vocabulary, copied from internal/app/tui/glyph. Each glyph has
// one meaning; do not add new ones here without adding them there first.
export const glyph = {
  Rail: '▍',
  User: '❯',
  Running: '▸',
  Ambient: '·',
  OK: '✓',
  Error: '✗',
  Warning: '⚠',
  Question: '?',
  Thinking: '⚙',
  Edit: '✎',
  File: '≡',
  Shell: '›',
  Search: '◈',
  Agent: '⧉',
  Web: '◇',
  Brand: '●',
  CustomAgent: '◆',
  DisclosureCollapsed: '▹',
  DisclosureExpanded: '▿',
  Job: '┆',
  Watch: '○',
  FollowDown: '↓',
  ProgressFull: '▰',
  ProgressEmpty: '▱',
} as const

// Mirrors toolCategoryGlyphs in internal/app/tui/toolnames.go. Order matters:
// the first matching prefix wins, so write tools precede the broader file.
const toolCategoryGlyphs: [string, string][] = [
  ['file.write', glyph.Edit],
  ['patch.', glyph.Edit],
  ['file.', glyph.File],
  ['shell.', glyph.Shell],
  ['test.', glyph.Shell],
  ['repo.search', glyph.Search],
  ['codebase.search', glyph.Search],
  ['json.', glyph.Search],
  ['csv.', glyph.Search],
  ['symbols.', glyph.Search],
  ['agent.', glyph.Agent],
  ['web.', glyph.Web],
  ['browser.', glyph.Web],
]

export function toolGlyph(name: string): string {
  for (const [prefix, g] of toolCategoryGlyphs) {
    if (name.startsWith(prefix)) return g
  }
  return glyph.Ambient
}

export type Tone = 'ok' | 'err' | 'warn' | 'info' | 'accent' | 'violet' | 'gold' | 'neutral'

// Design §6: implementer gold, reviewer violet, planner and branch reviewer
// info, anything else neutral.
export function roleTone(role?: string): Tone {
  switch ((role ?? '').toLowerCase().replace(/[\s_]+/g, '-')) {
    case 'implementer':
      return 'gold'
    case 'reviewer':
      return 'violet'
    case 'planner':
    case 'branch-reviewer':
      return 'info'
    default:
      return 'neutral'
  }
}
