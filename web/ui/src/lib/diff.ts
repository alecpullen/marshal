import type { DiffFile, MergeResult } from './api'

/** Aggregate counts for a changed-file list, for confirmations and badges. */
export function diffTotals(files: DiffFile[]): { files: number; added: number; removed: number } {
  return files.reduce(
    (acc, f) => ({ files: acc.files + 1, added: acc.added + f.added, removed: acc.removed + f.removed }),
    { files: 0, added: 0, removed: 0 },
  )
}

/**
 * Turn a merge refusal into a sentence that says what to do next. Each
 * reason has a different remedy, so a generic "merge failed" would be
 * useless.
 */
export function mergeRefusalMessage(r: MergeResult): string {
  if (r.merged) return ''
  switch (r.reason) {
    case 'dirty':
      return 'The agent has uncommitted changes. Provide a commit message to commit them and merge.'
    case 'project_dirty':
      return 'Your project checkout has uncommitted changes. Commit or stash them, then merge.'
    case 'target_moved':
      return `The project is no longer on ${r.target}, which this agent branched from. Switch back to ${r.target} to merge.`
    case 'conflicts':
      return `Merging ${r.branch} into ${r.target} conflicts in ${(r.conflicts ?? []).join(', ') || 'one or more files'}. Nothing was changed — resolve it in your editor.`
    default:
      return `Merge refused${r.reason ? ` (${r.reason})` : ''}.`
  }
}
