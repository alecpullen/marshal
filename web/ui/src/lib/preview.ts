/** Preview-environment API shapes mirror web/bridge/preview_environment.go. */

export interface PreviewCapability { key: string; state: string; detail: string }
export interface PreviewGap { key: string; section: string; state: string; detail: string }
export interface PreviewEnvironment { name: string; id: string; htmlUrl: string; branchPolicy: string; protectionRuleCount: number }
export interface PreviewDeployment { id: string; environment: string; originalEnvironment: string; ref: string; sha: string; task: string; state: string; environmentUrl: string; targetUrl: string; logUrl: string; description: string; transient: boolean; production: boolean; createdAt: string; updatedAt: string }
export interface PreviewReport { repoId: string; forge: string; status: string; error?: string; ref: string; sha: string; capabilities: PreviewCapability[]; environments: PreviewEnvironment[]; deployments: PreviewDeployment[]; gaps: PreviewGap[] }
