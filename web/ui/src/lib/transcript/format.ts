/** 6m40s, 45s, 1h2m: the TUI's compactDuration. */
export function compactDuration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m${s % 60 ? `${s % 60}s` : ''}`
  return `${Math.floor(m / 60)}h${m % 60 ? `${m % 60}m` : ''}`
}

export const plural = (n: number, one: string, many = one + 's') => `${n} ${n === 1 ? one : many}`

/** The turn receipt, as the TUI words it. */
export function receiptText(r: { durationMs: number; tasks: number; steps: number; tools: number; files: number; usage?: string; salvaged?: boolean }): string {
  const parts = [r.salvaged ? 'salvaged' : 'done']
  if (r.durationMs > 0) parts.push(compactDuration(r.durationMs))
  if (r.tasks > 0) parts.push(plural(r.tasks, 'task'))
  parts.push(plural(r.steps, 'step'), plural(r.tools, 'tool'))
  if (r.files > 0) parts.push(`±${plural(r.files, 'file')}`)
  if (r.usage) parts.push(r.usage)
  return parts.join(' · ')
}

/** Tools whose row leads with the subject (file or command) rather than the verb. */
const SUBJECT_FIRST = new Set(['file.write_patch', 'file.write', 'file.read', 'symbols.find', 'shell.run', 'test.run'])
export const subjectFirst = (name: string) => SUBJECT_FIRST.has(name)
