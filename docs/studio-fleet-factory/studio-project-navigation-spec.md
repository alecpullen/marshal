# Studio project settings and navigation

Status: product direction approved 2026-11-17; Flutter port proposed for review 2026-11-18. See the linked implementation plan.

## Flutter port constraints

The target is Flutter `app/`, not the retiring Svelte Studio. Follow the [normative Flutter design system](../studio-flutter/design-system.md), existing kit and UI/model/client boundaries. The implementation plan identifies current screens and tests. Recheck in-flight changes before implementing and preserve already-working behavior; fixture evidence is not a live-service claim.

## Intent and scope

Help users understand where they are in Studio, move between project, factory, item and agent work, and configure project/factory defaults without reading machine-oriented identifiers. Retain the combined settings screen grouped by concept, project-specific agent and factory defaults, override/inheritance behavior, and existing project and factory routes. Build on the [visual foundation](studio-visual-foundation-spec.md); this proposal does not create a separate factory-wide settings model or change backend permissions.

## Project orientation

Make the existing project overview's primary paths—open its factory, create factory work, start an independent agent, and change project settings—easy to distinguish. Keep contextual breadcrumbs and a visible project identity on project, factory and work-item pages. Breadcrumbs and links preserve encoded project roots, item IDs, selected tab/view and back navigation; a missing project or stale link must be called out rather than silently changing scope. Preserve the current project settings grouping (Workspace, Model, Factory, Session, Intake, Planner delegation, Health and Policy) and their separate save semantics. Improve section navigation with descriptive in-page links that work on phone and keyboard; do not move settings into a second source of truth or discard unsaved edits on section jumps.

## CI checks editor

Add an accessible structured expected-checks editor to the existing Factory defaults section. The Flutter defaults screen currently has no expected-checks editor; `ProjectDraft` must retain and serialize the existing setting without loss. Each row shows check type, name/context and GitHub App ID where applicable; explain why check runs include an app identity. Preserve an advanced raw text entry mode for existing power users. Both modes edit the same draft; switching modes must not discard malformed or unrecognized input. Round-trip existing `FactoryCheckKey` values exactly, including case, punctuation, check type and app ID. In raw mode, `commit_status:` consumes the remaining line as its context and `check_run:` uses the last colon to separate the numeric app ID, so names can contain colons. Existing invalid raw input remains visible and blocks saving with a specific error; it must not silently become an empty inherited setting.

Offer optional suggestions only from already available, authorized factory items' recorded CI evidence in the same project and repository. Label these as previously observed at a given revision/time, not as a complete or authoritative list of required checks. Dedupe by `(kind, name, appId)`, preserve same-name checks from different apps as distinct, and make selection explicit. A new project or unavailable factory feed still supports manual entry. No forge call or check discovery backend is added in this slice. Do not auto-select checks, acknowledge no-required-CI, or change settings when a user only views suggestions.

Keep absent defaults distinct from a deliberate choice at handoff: clearing the project field restores the existing follow/inherit behavior; the handoff requires a separate, explicit no-required-CI acknowledgement; the current Flutter model derives it from an empty list, so the port must correct that behavior. Preserve unavailable saved values and unrelated settings, existing version/ETag checks, route guard, conflict message and current per-section save behavior. A settings refresh or suggestion update never overwrites unsaved input.

## Verification

Exercise desktop and phone paths project → factory → item → worker → back, including special characters in project/item IDs and missing/unavailable projects. Test raw/structured round trips, empty/inherited settings, malformed input, names with punctuation and identical names from different apps, stale/partial/no CI evidence, user selection, ETag conflict and unsaved edit guards. Tests must assert exact API payloads and no mutation from browsing. Check keyboard, screen-reader labels, dark/light themes, focus and viewport behavior. Distinguish fixture-driven presentation from live forge behavior.
