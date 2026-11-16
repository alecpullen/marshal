# Route parity

Every route the Svelte client calls, and where it lands in the app: the
`BridgeClient` method that calls it (`app/lib/src/client/bridge_client.dart`),
or `dropped:` and why it has no Dart caller.

Sources: `web/ui/src/lib/api.ts`, `factory.ts`, `planner.ts`,
`working-drafts.ts` and `sse.ts`. A templated segment is written `X`.

`app/test/route_parity_test.dart` reads this table: a row with neither a
method nor a `dropped:` reason fails it, and so does a method that is not in
`bridge_client.dart`.

| Method | Path | BridgeClient | Note |
|---|---|---|---|
| GET | `/api/agents` | agents |  |
| POST | `/api/agents` | spawn |  |
| POST | `/api/agents/X/budget/override` | — | dropped: no screen overrides a paused agent’s budget yet |
| GET | `/api/agents/X/commit-draft` | commitDraft |  |
| GET | `/api/agents/X/diff` | diff |  |
| POST | `/api/agents/X/discard` | discard |  |
| POST | `/api/agents/X/exit` | exit |  |
| GET | `/api/agents/X/file` | file |  |
| GET | `/api/agents/X/files` | files |  |
| GET | `/api/agents/X/gate` | gate |  |
| POST | `/api/agents/X/merge` | — | dropped: the app ships through exit; a separate merge route has no caller |
| POST | `/api/agents/X/preview/X` | preview |  |
| GET | `/api/agents/X/review/comments` | reviewComments |  |
| POST | `/api/agents/X/review/comments` | addReviewComment |  |
| POST | `/api/agents/X/review/comments/X/resolve` | resolveReviewComment |  |
| POST | `/api/agents/X/verify` | verify |  |
| GET | `/api/audit` | audit |  |
| GET | `/api/automations/ci/history` | ciHistory |  |
| GET | `/api/automations/ci/history/X` | ciHistoryEntry |  |
| GET | `/api/automations/review/drafts` | reviewDrafts |  |
| POST | `/api/automations/review/run` | runReviewBot |  |
| GET | `/api/budgets` | budgets |  |
| PUT | `/api/budgets` | saveBudgets |  |
| GET | `/api/clients` | clients |  |
| POST | `/api/clients` | createClient |  |
| DELETE | `/api/clients/X` | deleteClient |  |
| GET | `/api/clients/X/factory-grants/X` | factoryGrant |  |
| PUT | `/api/clients/X/factory-grants/X` | putFactoryGrant |  |
| GET | `/api/config` | — | dropped: the app reads providers and presets through /api/models; General is this window only until the config route is mapped |
| GET | `/api/credentials` | credentials |  |
| POST | `/api/credentials` | putCredential |  |
| DELETE | `/api/credentials/X` | deleteCredential |  |
| GET | `/api/disk` | disk |  |
| GET | `/api/factory/items` | — | dropped: the factory screens are F6’s; nothing in the app calls the factory routes yet |
| POST | `/api/factory/items` | — | dropped: the factory screens are F6’s; nothing in the app calls the factory routes yet |
| GET | `/api/factory/items/X` | — | dropped: the factory screens are F6’s; nothing in the app calls the factory routes yet |
| POST | `/api/factory/items/X/commands` | — | dropped: the factory screens are F6’s; nothing in the app calls the factory routes yet |
| GET | `/api/factory/settings-version` | — | dropped: the factory screens are F6’s; nothing in the app calls the factory routes yet |
| GET | `/api/factory/working-drafts` | — | dropped: the factory screens are F6’s; nothing in the app calls the factory routes yet |
| POST | `/api/factory/working-drafts` | — | dropped: the factory screens are F6’s; nothing in the app calls the factory routes yet |
| GET | `/api/factory/working-drafts/installation` | — | dropped: the factory screens are F6’s; nothing in the app calls the factory routes yet |
| GET | `/api/inbox` | inbox |  |
| GET | `/api/library/memory` | memory |  |
| DELETE | `/api/library/memory/X` | deleteMemory |  |
| POST | `/api/library/memory/X/confidence` | setMemoryConfidence |  |
| POST | `/api/library/memory/X/promote` | promoteMemory |  |
| GET | `/api/library/memory/suggestions` | memorySuggestions |  |
| GET | `/api/library/plugins` | plugins |  |
| DELETE | `/api/library/plugins/X` | removePlugin |  |
| POST | `/api/library/plugins/confirm` | confirmPlugin |  |
| POST | `/api/library/plugins/discard` | discardPlugin |  |
| POST | `/api/library/plugins/scan` | scanPlugin |  |
| GET | `/api/library/skills` | skills |  |
| DELETE | `/api/library/skills/X` | removeSkill |  |
| POST | `/api/library/skills/confirm` | confirmSkill |  |
| POST | `/api/library/skills/discard` | discardSkill |  |
| POST | `/api/library/skills/preview` | previewSkill |  |
| GET | `/api/marshal/access?host=…` | — | dropped: the Studio has no access-selection screen; the web client stores that choice itself |
| PUT | `/api/marshal/access/default` | — | dropped: the Studio has no access-selection screen; the web client stores that choice itself |
| PUT | `/api/marshal/access/override` | — | dropped: the Studio has no access-selection screen; the web client stores that choice itself |
| PUT | `/api/marshal/access/selection` | — | dropped: the Studio has no access-selection screen; the web client stores that choice itself |
| GET | `/api/models` | models |  |
| PUT | `/api/models/presets` | setPresets |  |
| POST | `/api/models/probe` | probeProvider |  |
| PUT | `/api/models/providers` | setProviders |  |
| PUT | `/api/models/providers/X/key` | setProviderKey |  |
| PUT | `/api/models/routing` | setRouting |  |
| GET | `/api/network` | networkHosts |  |
| POST | `/api/network/decisions` | networkDecision |  |
| GET | `/api/network/pending` | networkPending |  |
| GET | `/api/notifications` | notifications |  |
| PUT | `/api/notifications` | saveNotifications |  |
| POST | `/api/notifications/test` | testNotifications |  |
| GET | `/api/pending` | pending |  |
| POST | `/api/pending/X/approve` | approveIntake |  |
| POST | `/api/pending/X/deny` | denyIntake |  |
| POST | `/api/permissions/X` | answerPermission |  |
| POST | `/api/planners` | — | dropped: planner delegation is configured through project settings; no screen calls it |
| DELETE | `/api/projects` | — | dropped: projects are discovered by the daemon, never created from the app |
| GET | `/api/projects` | projects |  |
| POST | `/api/projects` | — | dropped: projects are discovered by the daemon, never created from the app |
| GET | `/api/projects/health` | — | dropped: the connection check reads each host’s reach, not a health document |
| GET | `/api/projects/settings` | projectAutomations |  |
| PUT | `/api/projects/settings` | — | dropped: project settings are written by the web client; the app’s editing surfaces save to the user config |
| GET | `/api/prompts/recent` | recentPrompts |  |
| POST | `/api/prune` | prune |  |
| POST | `/api/questions/X` | answerQuestion |  |
| GET | `/api/recipes` | recipes |  |
| DELETE | `/api/recipes/X` | deleteRecipe |  |
| GET | `/api/recipes/X` | — | dropped: the app reads the recipe list; one recipe by name has no caller |
| PUT | `/api/recipes/X` | saveRecipe |  |
| POST | `/api/recipes/X/copy` | copyRecipe |  |
| POST | `/api/recipes/X/run` | runRecipe |  |
| GET | `/api/repos` | repos |  |
| POST | `/api/repos` | registerRepo |  |
| DELETE | `/api/repos/X` | removeRepo |  |
| GET | `/api/repos/X/issues` | issues |  |
| POST | `/api/repos/X/issues/X/spawn` | — | dropped: the new-work dialog starts an agent through spawn; the issue shortcut has no caller |
| GET | `/api/repos/X/preview-environments?ref=…` | — | dropped: preview environments are F5’s; no screen reads them yet |
| POST | `/api/repos/X/webhook-secret` | — | dropped: issuing a webhook secret is the web client’s forge setup |
| POST | `/api/reroutes/X/undo` | undoReroute |  |
| GET | `/api/runs` | — | dropped: the Runs screen is F6’s; the transcript reaches a run through its thread |
| POST | `/api/runs` | — | dropped: the Runs screen is F6’s; the transcript reaches a run through its thread |
| GET | `/api/runs/X` | — | dropped: the Runs screen is F6’s; the transcript reaches a run through its thread |
| POST | `/api/runs/X/answer` | — | dropped: the Runs screen is F6’s; the transcript reaches a run through its thread |
| GET | `/api/schedules` | schedules |  |
| POST | `/api/schedules` | saveSchedule |  |
| DELETE | `/api/schedules/X` | deleteSchedule |  |
| PUT | `/api/schedules/X` | saveSchedule |  |
| POST | `/api/schedules/X/run` | runSchedule |  |
| GET | `/api/secrets` | secrets |  |
| DELETE | `/api/secrets/X` | deleteSecret |  |
| PUT | `/api/secrets/X` | putSecret |  |
| GET | `/api/secrets/status` | secretsStatus |  |
| GET | `/api/sessions` | — | dropped: the app starts work through spawn; the web client’s own session create and list have no caller |
| POST | `/api/sessions` | — | dropped: the app starts work through spawn; the web client’s own session create and list have no caller |
| DELETE | `/api/sessions/X` | — | dropped: ending a session is not built; the app cancels a turn |
| GET | `/api/sessions/X/access` | access |  |
| PATCH | `/api/sessions/X/access` | — | dropped: the PATCH that changes access has no caller; a session’s access is answered through the asks |
| POST | `/api/sessions/X/cancel` | cancel |  |
| GET | `/api/sessions/X/last-request` | lastRequest |  |
| POST | `/api/sessions/X/load` | — | dropped: resuming a stored session is not built |
| POST | `/api/sessions/X/mode` | setMode |  |
| GET | `/api/sessions/X/nodes/X` | node |  |
| POST | `/api/sessions/X/prompt` | prompt |  |
| GET | `/api/sessions/X/roster` | — | dropped: the token roster is the fleet screen’s; no screen reads it |
| GET | `/api/sessions/X/stack` | stack |  |
| POST | `/api/sessions/X/steer` | steer |  |
| GET | `/api/sessions/X/step-diffs` | stepDiffs |  |
| GET | `/api/status-links` | statusLinks |  |
| POST | `/api/status-links` | createStatusLink |  |
| DELETE | `/api/status-links/X` | revokeStatusLink |  |
| GET | `/api/usage` | usage |  |
| GET | `/api/watches` | watches |  |
| POST | `/api/watches` | — | dropped: the app lists watches and undoes a reroute; creating and stopping them is not built |
| DELETE | `/api/watches/X/X` | — | dropped: the app lists watches and undoes a reroute; creating and stopping them is not built |
| GET | `/api/workspaces` | environments |  |
| POST | `/api/workspaces` | createEnvironment |  |
| GET | `/api/workspaces/X?version=…` | environment |  |
| GET | `/api/workspaces/X/builds` | environmentBuilds |  |
| POST | `/api/workspaces/X/builds` | startEnvironmentBuild |  |
| POST | `/api/workspaces/X/ca/rotate` | — | dropped: rotating a template’s certificate is not built |
| GET | `/api/workspaces/X/diff` | — | dropped: a version-to-version diff is not built; the designer shows the source |
| PUT | `/api/workspaces/X/draft` | saveEnvironmentDraft |  |
| POST | `/api/workspaces/X/patch` | — | dropped: the designer saves the whole source; the layer-patch route has no caller |
| PUT | `/api/workspaces/X/pool` | — | dropped: the panel reports the warm pool; sizing it is not built |
| POST | `/api/workspaces/X/publish` | publishEnvironment |  |

