import { describe, expect, it } from 'vitest'
import { matrix, setCell } from './routingMatrix'
import { PROVIDER_TEMPLATES, uniqueName } from './providerTemplates'

const cfg = {
  roles: ['implementer', 'reviewer', 'planner'],
  defaultProfile: 'balanced',
  profiles: {
    cheap: { implementer: { preset: 'small' } },
    balanced: { implementer: { preset: 'big' }, reviewer: { customAgent: 'strict' } },
  },
}

describe('matrix', () => {
  it('has a row per role and a column per profile, default first', () => {
    const m = matrix(cfg)
    expect(m.profiles).toEqual(['balanced', 'cheap'])
    expect(m.rows.map((r) => r.role)).toEqual(['implementer', 'reviewer', 'planner'])
    expect(m.rows[0].cells).toEqual([
      { profile: 'balanced', preset: 'big', customAgent: undefined },
      { profile: 'cheap', preset: 'small', customAgent: undefined },
    ])
    expect(m.rows[1].cells[0]).toEqual({ profile: 'balanced', preset: undefined, customAgent: 'strict' })
    expect(m.rows[2].cells.every((c) => !c.preset && !c.customAgent)).toBe(true)
  })

  it('tolerates an empty config', () => {
    expect(matrix({ roles: [], defaultProfile: '', profiles: {} })).toEqual({ profiles: [], rows: [] })
  })
})

describe('setCell', () => {
  it('returns every profile with only that cell changed', () => {
    const next = setCell(cfg, 'cheap', 'reviewer', 'mid')
    expect(next.cheap).toEqual({ implementer: { preset: 'small' }, reviewer: { preset: 'mid' } })
    expect(next.balanced).toEqual(cfg.profiles.balanced)
  })

  it('does not modify its input', () => {
    const before = JSON.stringify(cfg)
    const next = setCell(cfg, 'balanced', 'implementer', 'small')
    expect(JSON.stringify(cfg)).toBe(before)
    expect(next).not.toBe(cfg.profiles)
    expect(next.balanced).not.toBe(cfg.profiles.balanced)
  })

  it('keeps the default profile even when its last binding is cleared', () => {
    let next = setCell(cfg, 'balanced', 'implementer', '')
    next = setCell({ profiles: next }, 'balanced', 'reviewer', '')
    expect(next.balanced).toEqual({})
    expect(Object.keys(next)).toContain(cfg.defaultProfile)
  })

  it('replaces a custom agent binding with a preset', () => {
    expect(setCell(cfg, 'balanced', 'reviewer', 'big').balanced.reviewer).toEqual({ preset: 'big' })
  })
})

describe('provider templates', () => {
  it('lists the Go templates once each', () => {
    const ids = PROVIDER_TEMPLATES.map((t) => t.id)
    expect(new Set(ids).size).toBe(ids.length)
    expect(ids).toEqual(expect.arrayContaining(['ollama', 'openai', 'anthropic', 'groq', 'openai-codex']))
  })

  it('picks an unused provider name', () => {
    expect(uniqueName('groq', ['openai'])).toBe('groq')
    expect(uniqueName('groq', ['groq', 'groq-2'])).toBe('groq-3')
  })
})
