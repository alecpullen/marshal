/** Dollars for a tile or table cell; sub-cent spend keeps its digits instead of reading $0.00. */
export function usd(n: number): string {
  if (n > 0 && n < 0.01) return `$${n.toFixed(4)}`
  return `$${n.toFixed(2)}`
}

export function compactTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(Math.round(n))
}
