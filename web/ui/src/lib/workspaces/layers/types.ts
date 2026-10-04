import type { WSDiag, WSDoc } from '../../api'

/** Props every layer card takes. `onPatch` sends the layer's whole new value (patches replace a layer wholesale). */
export interface LayerProps {
  doc: WSDoc
  diags: WSDiag[]
  selected: boolean
  onSelect: () => void
  onPatch: (layer: number, value: unknown) => void
}
