import { describe, expect, it } from 'vitest'
import { ROADMAP } from './roadmap'
import { ROADMAP_AREAS } from './routes'

const entries = Object.values(ROADMAP)

describe('roadmap registry', () => {
  it('contains exactly the six approved areas', () => {
    // The registry is a Record, so a missing entry is a type error but an
    // extra one is not: this keeps the page's destinations to the six the
    // plan approved rather than whatever a later edit adds.
    expect(Object.keys(ROADMAP).sort()).toEqual([...ROADMAP_AREAS].sort())
    expect(ROADMAP_AREAS).toHaveLength(6)
    for (const area of ROADMAP_AREAS) {
      expect(ROADMAP[area].area).toBe(area)
      expect(ROADMAP[area].title.length).toBeGreaterThan(0)
      expect(ROADMAP[area].summary.length).toBeGreaterThan(0)
      expect(ROADMAP[area].current.length).toBeGreaterThan(0)
      expect(ROADMAP[area].planned.length).toBeGreaterThan(0)
    }
  })

  it('uses only valid internal alternatives', () => {
    // An alternative is a working destination offered in place of the
    // planned feature, so it has to be an in-app hash. A stray absolute URL
    // here would navigate the whole page out of the SPA, and an empty hash
    // would land on the fleet screen while reading as a specific page.
    for (const entry of entries) {
      for (const action of entry.alternatives) {
        expect(action.label.length).toBeGreaterThan(0)
        expect(action.hash).toMatch(/^#[a-z][a-z0-9/-]*$/)
        // #projects is a real page even though parseProjectRoute needs a root,
        // so this asserts shape rather than a parsed route.
        expect(action.hash.startsWith('#/')).toBe(false)
      }
    }
  })

  it('uses safe https design links', () => {
    // The design link leaves the app, so it must be https and it must be a
    // documentation page — never a javascript: or plain-http target.
    for (const entry of entries) {
      expect(entry.designHref).toBe('https://github.com/alecpullen/marshal/blob/main/docs/studio-fleet-factory/design-notes.md')
      const url = new URL(entry.designHref as string)
      expect(url.protocol).toBe('https:')
      expect(url.host).toBe('github.com')
    }
  })
})
