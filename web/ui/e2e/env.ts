/**
 * Environment access without a Node type dependency.
 *
 * `web/ui/tsconfig.json` is a browser config, so `process` is not declared and
 * `svelte-check` would flag every read of it. Playwright runs these files under
 * Node, where the global exists; this narrows it to the one shape we use.
 */
const nodeEnv = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env ?? {}

export const env = (name: string, fallback: string): string => {
  const value = nodeEnv[name]
  return value === undefined || value === '' ? fallback : value
}
