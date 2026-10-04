/**
 * Subsequence match for the command palette. Returns 0 for no match;
 * otherwise a score that favours an exact match, then a prefix, then
 * consecutive runs and word-start hits over scattered characters.
 */
export function score(query: string, text: string): number {
  const q = query.toLowerCase()
  const t = text.toLowerCase()
  if (q === '') return 1
  if (t === q) return 1000
  if (t.startsWith(q)) return 500 - Math.min(t.length - q.length, 100)

  const greedy = subsequence(q, t, false)
  const anchored = subsequence(q, t, true)
  const best = Math.max(greedy, anchored)
  // A shorter haystack means a tighter match.
  return best === 0 ? 0 : Math.max(1, best - Math.floor(t.length / 8))
}

const isBoundary = (t: string, at: number) => at === 0 || /[\s/_.:-]/.test(t[at - 1])

// Walks q through t in order. With `anchored`, each char prefers the next
// word-start occurrence, so "ho" scores a hit on the "h" of "the home".
function subsequence(q: string, t: string, anchored: boolean): number {
  let ti = 0
  let total = 0
  let run = 0
  for (const ch of q) {
    let at = t.indexOf(ch, ti)
    if (at < 0) return 0
    if (anchored) {
      let w = at
      while (w >= 0 && !isBoundary(t, w)) w = t.indexOf(ch, w + 1)
      if (w >= 0) at = w
    }
    run = at === ti && ti > 0 ? run + 1 : 0
    total += 1 + (isBoundary(t, at) ? 8 : 0) + run * 3
    ti = at + 1
  }
  return total
}
