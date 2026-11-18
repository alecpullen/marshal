# Route parity

Every route the bridge serves, and where it lands in the Studio: the
`BridgeClient` method that calls it (`app/lib/src/client/bridge_client.dart`),
or `dropped:` and why it has no Dart caller.

The app's routes are read from `bridge_client.dart` itself. A templated segment
is written `X`, and a query string is dropped before a route is compared.

Until F8's cutover the table was a parity of two clients, and a row said which
of them reached a route. The Svelte client is gone, so a row now answers one
question: does the Studio reach this route, and if not, why not.

`app/test/route_parity_test.dart` checks the table against the client: a row
with neither a method nor a `dropped:` reason fails it, so does a method that is
not in `bridge_client.dart`, so does a `dropped:` row the client actually
issues, and so does a route the client issues that has no row.

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
| POST | `/api/agents/X/merge` | mergeAgent |  |
| GET | `/api/agents/X/patch` | patch |  |
| POST | `/api/agents/X/preview/X` | preview |  |
| GET | `/api/agents/X/review/comments` | reviewComments |  |
| POST | `/api/agents/X/review/comments` | addReviewComment |  |
| POST | `/api/agents/X/review/comments/X/resolve` | resolveReviewComment |  |
| POST | `/api/agents/X/terminal` | openTerminal |  |
| DELETE | `/api/agents/X/terminal/X` | closeTerminal |  |
| GET | `/api/agents/X/terminal/X/events` | terminalEvents |  |
| POST | `/api/agents/X/terminal/X/input` | terminalInput |  |
| POST | `/api/agents/X/terminal/X/release` | terminalRelease |  |
| POST | `/api/agents/X/terminal/X/resize` | terminalResize |  |
| POST | `/api/agents/X/verify` | verify |  |
| GET | `/api/audit` | audit |  |
| GET | `/api/automations/ci/history` | ciHistory |  |
| GET | `/api/automations/ci/history/X` | ciHistoryEntry |  |
| GET | `/api/automations/review/drafts` | reviewDrafts |  |
| GET | `/api/automations/review/drafts/X` | reviewDraft |  |
| PUT | `/api/automations/review/drafts/X` | editReviewDraft |  |
| POST | `/api/automations/review/drafts/X/discard` | discardReviewDraft |  |
| POST | `/api/automations/review/drafts/X/post` | postReviewDraft |  |
| POST | `/api/automations/review/drafts/X/send-to-author` | sendDraftToAuthor |  |
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
| GET | `/api/daemon` | daemon |  |
| GET | `/api/disk` | disk |  |
| GET | `/api/events` | fleetEvents | sessionEvents streams it too |
| GET | `/api/factory/items` | factoryItems |  |
| POST | `/api/factory/items` | createFactoryItem |  |
| GET | `/api/factory/items/X` | factoryItem |  |
| POST | `/api/factory/items/X/commands` | factoryCommand |  |
| GET | `/api/factory/settings-version` | factorySettingsVersion |  |
| GET | `/api/factory/working-drafts` | workingDrafts |  |
| POST | `/api/factory/working-drafts` | createWorkingDraft |  |
| GET | `/api/factory/working-drafts/X` | workingDraft |  |
| PUT | `/api/factory/working-drafts/X` | saveWorkingDraft |  |
| DELETE | `/api/factory/working-drafts/X` | deleteWorkingDraft |  |
| GET | `/api/factory/working-drafts/installation` | — | dropped: the installation draft is the web client’s onboarding; the app has no onboarding screen |
| * | `/api/hosts/X/…` | — | dropped: the relayed transport prefixes every route; no method names it |
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
| GET | `/api/marshal/access?host=…` | — | dropped: nothing in the Studio sets the marshal-wide access choice |
| PUT | `/api/marshal/access/default` | — | dropped: nothing in the Studio sets the marshal-wide access choice |
| PUT | `/api/marshal/access/override` | — | dropped: nothing in the Studio sets the marshal-wide access choice |
| PUT | `/api/marshal/access/selection` | — | dropped: nothing in the Studio sets the marshal-wide access choice |
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
| POST | `/api/planners` | createPlanner |  |
| GET | `/api/planners/X` | planner |  |
| POST | `/api/planners/X/cancel` | cancelPlanner |  |
| GET | `/api/planners/X/events` | plannerEvents |  |
| POST | `/api/planners/X/prompt` | promptPlanner |  |
| POST | `/api/planners/X/requests/X` | answerPlanner |  |
| POST | `/api/planners/X/snapshots` | createPlannerSnapshot |  |
| GET | `/api/planners/X/snapshots/X` | plannerSnapshot |  |
| POST | `/api/planners/X/snapshots/X/handoff` | handoffPlannerSnapshot |  |
| DELETE | `/api/projects` | — | dropped: projects are discovered by the daemon, never created from the app |
| GET | `/api/projects` | projects |  |
| POST | `/api/projects` | — | dropped: projects are discovered by the daemon, never created from the app |
| GET | `/api/projects/health` | projectHealth |  |
| GET | `/api/projects/settings` | projectSettings |  |
| PUT | `/api/projects/settings` | putProjectSettings |  |
| GET | `/api/prompts/recent` | recentPrompts |  |
| POST | `/api/prune` | prune |  |
| GET | `/api/pulls` | pulls |  |
| GET | `/api/pulls/X` | pull |  |
| POST | `/api/pulls/X/fix` | fixPull |  |
| POST | `/api/pulls/X/fix/stop` | stopPullFix |  |
| POST | `/api/questions/X` | answerQuestion |  |
| GET | `/api/recipes` | recipes |  |
| DELETE | `/api/recipes/X` | deleteRecipe |  |
| GET | `/api/recipes/X` | — | dropped: the app reads the recipe list; one recipe by name has no caller |
| PUT | `/api/recipes/X` | saveRecipe |  |
| POST | `/api/recipes/X/copy` | copyRecipe |  |
| POST | `/api/recipes/X/run` | runRecipe |  |
| GET | `/api/relay/hosts` | — | dropped: the Studio reads it from P8 plan 4 |
| POST | `/api/relay/join` | — | dropped: a daemon joins; the Studio never does |
| GET | `/api/repos` | repos |  |
| POST | `/api/repos` | registerRepo |  |
| DELETE | `/api/repos/X` | removeRepo |  |
| POST | `/api/repos/X/discover` | discoverRepo |  |
| GET | `/api/repos/X/issues` | issues |  |
| POST | `/api/repos/X/issues/X/spawn` | — | dropped: the new-work dialog starts an agent through spawn; the issue shortcut has no caller |
| GET | `/api/repos/X/preview-environments?ref=…` | — | dropped: no screen reads preview environments; a preview is opened through the agent’s own /preview route |
| POST | `/api/repos/X/webhook-secret` | issueWebhookSecret |  |
| POST | `/api/reroutes/X/undo` | undoReroute |  |
| GET | `/api/runs` | runs |  |
| POST | `/api/runs` | — | dropped: the app lists a run and answers the question it stopped on; it never starts one |
| GET | `/api/runs/X` | — | dropped: a run is reached through its thread and its list entry’s digest; reading one by id has no caller |
| POST | `/api/runs/X/answer` | answerRun |  |
| GET | `/api/schedules` | schedules |  |
| POST | `/api/schedules` | saveSchedule |  |
| GET | `/api/schedules/X` | saveSchedule |  |
| DELETE | `/api/schedules/X` | deleteSchedule |  |
| PUT | `/api/schedules/X` | saveSchedule |  |
| POST | `/api/schedules/X/run` | runSchedule |  |
| GET | `/api/secrets` | secrets |  |
| DELETE | `/api/secrets/X` | deleteSecret |  |
| PUT | `/api/secrets/X` | putSecret |  |
| GET | `/api/secrets/status` | secretsStatus |  |
| GET | `/api/sessions` | — | dropped: the app starts work through spawn; the web client’s own session create and list have no caller |
| POST | `/api/sessions` | — | dropped: the app starts work through spawn; the web client’s own session create and list have no caller |
| GET | `/api/sessions/all` | — | dropped: the Studio reads it from P7 plan 2 |
| DELETE | `/api/sessions/X` | — | dropped: ending a session is not built; the app cancels a turn |
| GET | `/api/sessions/X/access` | access |  |
| PATCH | `/api/sessions/X/access` | — | dropped: the PATCH that changes access has no caller; a session’s access is answered through the asks |
| POST | `/api/sessions/X/cancel` | cancel |  |
| GET | `/api/sessions/X/last-request` | lastRequest |  |
| POST | `/api/sessions/X/load` | — | dropped: resuming a stored session is not built |
| POST | `/api/sessions/X/mode` | setMode |  |
| GET | `/api/sessions/X/nodes/X` | node |  |
| POST | `/api/sessions/X/prompt` | prompt |  |
| GET | `/api/sessions/X/roster` | sessionRoster |  |
| GET | `/api/sessions/X/stack` | stack |  |
| POST | `/api/sessions/X/steer` | steer |  |
| GET | `/api/sessions/X/step-diffs` | stepDiffs |  |
| GET | `/api/status-links` | statusLinks |  |
| POST | `/api/status-links` | createStatusLink |  |
| DELETE | `/api/status-links/X` | revokeStatusLink |  |
| GET | `/api/usage` | usage |  |
| GET | `/api/watches` | watches |  |
| POST | `/api/watches` | createWatch |  |
| DELETE | `/api/watches/X/X` | stopWatch |  |
| GET | `/api/workspaces` | environments |  |
| POST | `/api/workspaces` | createEnvironment |  |
| GET | `/api/workspaces/X?version=…` | environment |  |
| DELETE | `/api/workspaces/X` | deleteEnvironment |  |
| GET | `/api/workspaces/X/builds` | environmentBuilds |  |
| POST | `/api/workspaces/X/builds` | startEnvironmentBuild |  |
| GET | `/api/workspaces/X/builds/X/events` | environmentBuildLog |  |
| POST | `/api/workspaces/X/ca/rotate` | — | dropped: rotating a template’s certificate is not built |
| GET | `/api/workspaces/X/diff` | — | dropped: a version-to-version diff is not built; the designer shows the source |
| PUT | `/api/workspaces/X/draft` | saveEnvironmentDraft |  |
| POST | `/api/workspaces/X/patch` | — | dropped: the designer saves the whole source; the layer-patch route has no caller |
| PUT | `/api/workspaces/X/pool` | — | dropped: the panel reports the warm pool; sizing it is not built |
| POST | `/api/workspaces/X/publish` | publishEnvironment |  |
| POST | `/api/workspaces/X/shell` | — | dropped: the Svelte client opened a test shell in a workspace’s container, and the app’s terminal is an agent’s (`openTerminal` takes an agent id), so neither this nor the shell’s events, input, resize and close routes have a caller |
