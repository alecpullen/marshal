import { describe, expect, it } from 'vitest'
import { applyParse, applyServer, cursorLine, diagsInLayer, editSource, initialState, layerRange, selectLayer, type DesignerState } from './designer'
import type { WSDoc } from '../api'

const doc = {} as WSDoc
// Layers 1 and 2 share [workspace] (lines 1-4); layer 6 spans two sections.
const sections = [
  { layer: 1, key: 'workspace', startLine: 1, endLine: 4 },
  { layer: 2, key: 'workspace', startLine: 1, endLine: 4 },
  { layer: 3, key: 'packages', startLine: 6, endLine: 8 },
  { layer: 6, key: 'secrets', startLine: 10, endLine: 11 },
  { layer: 6, key: 'secrets.inject', startLine: 13, endLine: 15 },
]
const loaded: DesignerState = applyServer(initialState, { source: 'src', doc, sections, diagnostics: [{ line: 7, message: 'bad', severity: 'error' }, { line: 2, message: 'w', severity: 'warning' }] })

describe('designer reducer', () => {
  it('selectLayer sets the highlight to that layer\'s range', () => {
    expect(selectLayer(loaded, 3).highlight).toEqual({ start: 6, end: 8 })
    expect(selectLayer(loaded, 3).selectedLayer).toBe(3)
  })

  it('a layer with no section highlights nothing', () => {
    const s = selectLayer(loaded, 9)
    expect(s.selectedLayer).toBe(9)
    expect(s.highlight).toBeUndefined()
  })

  it('layer 6 highlights the union of its sections', () => {
    expect(layerRange(sections, 6)).toEqual({ start: 10, end: 15 })
  })

  it('cursorLine selects the layer containing the line', () => {
    expect(cursorLine(loaded, 7).selectedLayer).toBe(3)
    expect(cursorLine(loaded, 14).selectedLayer).toBe(6)
  })

  it('layers 1 and 2 share [workspace]: the cursor keeps layer 2 if selected, else picks 1', () => {
    expect(cursorLine(loaded, 2).selectedLayer).toBe(1)
    expect(cursorLine(selectLayer(loaded, 2), 3).selectedLayer).toBe(2)
    expect(cursorLine(selectLayer(loaded, 3), 3).selectedLayer).toBe(1)
  })

  it('a line outside every section keeps the selection', () => {
    const s = selectLayer(loaded, 3)
    expect(cursorLine(s, 5)).toBe(s)
  })

  it('applyServer replaces everything, keeps the selection and re-aims the highlight', () => {
    const s = applyServer(selectLayer(editSource(loaded, 'typed'), 3), {
      source: 'new',
      doc,
      sections: [{ layer: 3, key: 'packages', startLine: 2, endLine: 3 }],
      diagnostics: [],
    })
    expect(s).toMatchObject({ source: 'new', dirty: false, selectedLayer: 3, highlight: { start: 2, end: 3 }, diagnostics: [] })
  })

  it('applyParse updates the parse but leaves the typed text', () => {
    const s = applyParse(editSource(loaded, 'typed'), { doc, sections, diagnostics: [] })
    expect(s.source).toBe('typed')
    expect(s.diagnostics).toEqual([])
  })

  it('diagsInLayer returns only diagnostics inside the layer range', () => {
    expect(diagsInLayer(loaded, 3).map((d) => d.message)).toEqual(['bad'])
    expect(diagsInLayer(loaded, 1).map((d) => d.message)).toEqual(['w'])
    expect(diagsInLayer(loaded, 2).map((d) => d.message)).toEqual(['w'])
    expect(diagsInLayer(loaded, 8)).toEqual([])
  })
})
