import { describe, expect, it } from 'vitest'
import { glyph, roleTone, toolGlyph } from './glyphs'

describe('toolGlyph', () => {
  const cases: [string, string][] = [
    ['file.write_patch', glyph.Edit],
    ['patch.apply', glyph.Edit],
    ['file.read', glyph.File],
    ['shell.run', glyph.Shell],
    ['test.run', glyph.Shell],
    ['repo.search', glyph.Search],
    ['codebase.search', glyph.Search],
    ['json.query', glyph.Search],
    ['csv.inspect', glyph.Search],
    ['symbols.find', glyph.Search],
    ['agent.await', glyph.Agent],
    ['web.search', glyph.Web],
    ['browser.open', glyph.Web],
  ]
  it.each(cases)('%s', (name, want) => expect(toolGlyph(name)).toBe(want))
  it('falls back to the ambient dot', () => expect(toolGlyph('git.status')).toBe(glyph.Ambient))
})

describe('roleTone', () => {
  it('maps roles to tones', () => {
    expect(roleTone('implementer')).toBe('gold')
    expect(roleTone('reviewer')).toBe('violet')
    expect(roleTone('planner')).toBe('info')
    expect(roleTone('branch reviewer')).toBe('info')
    expect(roleTone('')).toBe('neutral')
    expect(roleTone(undefined)).toBe('neutral')
    expect(roleTone('explorer')).toBe('neutral')
  })
})
