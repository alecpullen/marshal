import type { Recipe } from '../api'

const TOKEN = /\{\{\s*([A-Za-z0-9_.-]+)\s*\}\}/g

/** The distinct `{{name}}` tokens in a prompt, in order of first use. */
export function placeholders(prompt: string): string[] {
  const out: string[] = []
  for (const m of prompt.matchAll(TOKEN)) if (!out.includes(m[1])) out.push(m[1])
  return out
}

/** Placeholders the recipe uses but does not declare as inputs. */
export function undeclared(r: Pick<Recipe, 'prompt' | 'inputs'>): string[] {
  const declared = new Set((r.inputs ?? []).map((i) => i.name))
  return placeholders(r.prompt).filter((p) => !declared.has(p))
}

/** Names of required inputs that have no value yet. */
export function missingRequired(r: Pick<Recipe, 'inputs'>, values: Record<string, string>): string[] {
  return (r.inputs ?? []).filter((i) => i.required && !(values[i.name] ?? '').trim()).map((i) => i.name)
}

/** The prompt with each known input substituted; unknown tokens stay as written. */
export function fillPrompt(prompt: string, values: Record<string, string>): string {
  return prompt.replace(TOKEN, (whole, name: string) => (name in values && values[name] !== '' ? values[name] : whole))
}

/** Splits a prompt into plain and `{{token}}` runs, for highlighting. */
export function tokenRuns(prompt: string): { text: string; token: boolean }[] {
  const runs: { text: string; token: boolean }[] = []
  let last = 0
  for (const m of prompt.matchAll(TOKEN)) {
    const at = m.index ?? 0
    if (at > last) runs.push({ text: prompt.slice(last, at), token: false })
    runs.push({ text: m[0], token: true })
    last = at + m[0].length
  }
  if (last < prompt.length) runs.push({ text: prompt.slice(last), token: false })
  return runs
}

export function limitsLabel(r: Pick<Recipe, 'limits'>): string {
  const parts: string[] = []
  if (r.limits?.maxMinutes) parts.push(`${r.limits.maxMinutes} min`)
  if (r.limits?.maxUsd) parts.push(`$${r.limits.maxUsd}`)
  return parts.join(' · ') || 'no limits'
}
