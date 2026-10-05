import { describe, expect, it } from 'vitest'
import { fillPrompt, limitsLabel, missingRequired, placeholders, tokenRuns, undeclared } from './recipes'

describe('recipes helpers', () => {
  it('finds distinct placeholders', () => {
    expect(placeholders('Fix {{issue}} in {{ repo }} then {{issue}}')).toEqual(['issue', 'repo'])
  })
  it('reports undeclared placeholders', () => {
    expect(undeclared({ prompt: 'a {{x}} {{y}}', inputs: [{ name: 'x' }] })).toEqual(['y'])
    expect(undeclared({ prompt: 'none' })).toEqual([])
  })
  it('lists missing required inputs', () => {
    const r = { inputs: [{ name: 'a', required: true }, { name: 'b' }, { name: 'c', required: true }] }
    expect(missingRequired(r, { a: ' ', c: 'x' })).toEqual(['a'])
  })
  it('fills known tokens and leaves the rest', () => {
    expect(fillPrompt('Do {{a}} and {{b}}', { a: '1', b: '' })).toBe('Do 1 and {{b}}')
  })
  it('splits runs around tokens', () => {
    expect(tokenRuns('x {{a}} y')).toEqual([{ text: 'x ', token: false }, { text: '{{a}}', token: true }, { text: ' y', token: false }])
  })
  it('labels limits', () => {
    expect(limitsLabel({ limits: { maxMinutes: 30, maxUsd: 2 } })).toBe('30 min · $2')
    expect(limitsLabel({})).toBe('no limits')
  })
})
