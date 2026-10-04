import type { WSDiag, WSDoc, WSSection } from '../api'

/** The nine layer cards, in order. Layer 0 (policy) lives in the side panel. */
export const LAYERS = [
  { n: 1, title: 'Base' },
  { n: 2, title: 'Toolchains' },
  { n: 3, title: 'Packages' },
  { n: 4, title: 'Mounts' },
  { n: 5, title: 'Files' },
  { n: 6, title: 'Secrets' },
  { n: 7, title: 'Network' },
  { n: 8, title: 'Resources' },
  { n: 9, title: 'Setup' },
] as const

export interface DesignerState {
  source: string
  doc: WSDoc | null
  sections: WSSection[]
  diagnostics: WSDiag[]
  selectedLayer: number | null
  /** The selected layer's source range; tinted in the editor. */
  highlight?: { start: number; end: number }
  /** Source text differs from what the bridge last parsed. */
  dirty: boolean
}

export interface ServerResult {
  source: string
  doc: WSDoc
  sections: WSSection[]
  diagnostics: WSDiag[]
}

export const initialState: DesignerState = { source: '', doc: null, sections: [], diagnostics: [], selectedLayer: null, dirty: false }

/** The line span a layer owns: the union of its sections (layer 6 spans `[secrets]` and `[secrets.inject]`). */
export function layerRange(sections: WSSection[], layer: number): { start: number; end: number } | undefined {
  const own = sections.filter((s) => s.layer === layer)
  if (own.length === 0) return undefined
  return { start: Math.min(...own.map((s) => s.startLine)), end: Math.max(...own.map((s) => s.endLine)) }
}

/** Selecting a layer highlights its section range; a layer with no section yet highlights nothing. */
export function selectLayer(s: DesignerState, layer: number | null): DesignerState {
  return { ...s, selectedLayer: layer, highlight: layer === null ? undefined : layerRange(s.sections, layer) }
}

/**
 * Moving the cursor into a section selects its layer. Layers 1 and 2 share
 * `[workspace]`, so a line there keeps the current pick when it is one of the
 * two and otherwise falls to layer 1. A line outside every section leaves the
 * selection alone.
 */
export function cursorLine(s: DesignerState, line: number): DesignerState {
  const hits = s.sections.filter((x) => line >= x.startLine && line <= x.endLine).map((x) => x.layer)
  if (hits.length === 0) return s
  const next = s.selectedLayer !== null && hits.includes(s.selectedLayer) ? s.selectedLayer : Math.min(...hits)
  return selectLayer(s, next)
}

/** A patch or parse result replaces everything; the selection survives and its range is recomputed. */
export function applyServer(s: DesignerState, r: ServerResult): DesignerState {
  return selectLayer({ ...s, source: r.source, doc: r.doc, sections: r.sections, diagnostics: r.diagnostics, dirty: false }, s.selectedLayer)
}

/** A parse result for text the user is still typing: doc, sections and diagnostics update, the text does not. */
export function applyParse(s: DesignerState, r: Omit<ServerResult, 'source'>): DesignerState {
  return selectLayer({ ...s, doc: r.doc, sections: r.sections, diagnostics: r.diagnostics, dirty: false }, s.selectedLayer)
}

/** Local typing: the text moves, the rest waits for the bridge. */
export function editSource(s: DesignerState, source: string): DesignerState {
  return { ...s, source, dirty: true }
}

/** The diagnostics that fall inside a layer's section range (layer 0 and unknown layers get none). */
export function diagsInLayer(s: Pick<DesignerState, 'sections' | 'diagnostics'>, layer: number): WSDiag[] {
  const r = layerRange(s.sections, layer)
  return r ? s.diagnostics.filter((d) => d.line >= r.start && d.line <= r.end) : []
}
