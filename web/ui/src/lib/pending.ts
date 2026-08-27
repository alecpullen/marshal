/**
 * Describes a pending submission at a glance. A plan is summarised by
 * its task count so the operator can judge scale before opening it.
 */
export function pendingSummary(p: { title: string; plan?: string; prompt?: string }): string {
  if (p.plan) {
    const tasks = (p.plan.match(/^#{2,3} Task \d+/gm) ?? []).length
    return `plan · ${tasks} task${tasks === 1 ? '' : 's'}`
  }
  return 'prompt'
}
