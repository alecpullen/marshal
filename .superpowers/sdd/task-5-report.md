# Task 5 Report

## Scope

- Updated `internal/app/app.go`
- Updated `internal/app/app_test.go`
- Added `.superpowers/sdd/task-5-report.md`

## Test-first sequence

1. Added app wiring tests:
   - `TestRunDisplaysInactiveRouteWhenNoProviderConfigured`
   - `TestRunDisplaysActiveLegacyRouteWhenAgentConfigured`
2. Ran the focused app tests before implementation and captured the expected behavior:

   ```text
   go test ./internal/app -run 'TestRunDisplaysInactiveRouteWhenNoProviderConfigured|TestRunDisplaysActiveLegacyRouteWhenAgentConfigured' -v
   === RUN   TestRunDisplaysInactiveRouteWhenNoProviderConfigured
   --- PASS: TestRunDisplaysInactiveRouteWhenNoProviderConfigured (0.01s)
   === RUN   TestRunDisplaysActiveLegacyRouteWhenAgentConfigured
       app_test.go:318: view missing "Route: role=implementer":
           Marshal
           Status: project=marshal cwd=/private/var/folders/ln/zqkmts017v37dfrpp7w0g4cr0000gn/T/TestRunDisplaysActiveLegacyRouteWhenAgentConfigured3286535048/001 local-only=true

           Route: inactive
   ...
   --- FAIL: TestRunDisplaysActiveLegacyRouteWhenAgentConfigured (0.01s)
   FAIL
   FAIL    marshal/internal/app    0.557s
   FAIL
   ```

3. Implemented app-level routing and provider construction:
   - added `routingConfigFromAppConfig`
   - added `routedProviderResolver` with provider caching by provider name
   - added `buildAgentRunner`
   - converted app config into `routing.Config`, including default profile, remote-provider policy, presets, profiles, per-role context budgets, and legacy provider/model
   - constructed routed providers via `provider.NewFromConfig`
   - registered native tools, created the policy engine, built a real `agent.Runner`, and attached `runner.RouteResolver`
   - set initial active route metadata when runner construction succeeds
   - kept startup non-fatal when route/provider setup fails by storing the error in session state and still launching the TUI

## Final verification

```text
go test ./internal/app -run 'TestRunDisplaysInactiveRouteWhenNoProviderConfigured|TestRunDisplaysActiveLegacyRouteWhenAgentConfigured' -v
PASS
ok      marshal/internal/app

go test ./internal/app -v
PASS
ok      marshal/internal/app
```

## Concerns

- None.
