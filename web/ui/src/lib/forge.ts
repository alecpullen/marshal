export type ForgeCapabilities = {
  richPRs: boolean
  issueIntake: boolean
  /** Empty when everything is available; otherwise why it is not. */
  reason: string
}

/**
 * Mirrors the server's degradation rules so the UI can explain them at
 * registration time rather than letting an exit fail an hour later.
 *
 * An SSH deploy key cannot call an HTTP API — that is a property of the
 * credential, not a misconfiguration.
 */
export function forgeCapabilities(r: { forge: string; credKind: string }): ForgeCapabilities {
  if (!r.forge) {
    return { richPRs: false, issueIntake: false, reason: 'No forge declared for this repo.' }
  }
  if (r.credKind !== 'pat') {
    return {
      richPRs: false,
      issueIntake: false,
      reason: 'An SSH key cannot call the forge API. Register a token credential for rich pull requests and issue intake.',
    }
  }
  return { richPRs: true, issueIntake: true, reason: '' }
}
