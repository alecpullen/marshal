# Marshal Studio visual and interaction foundation

Updated: 2026-11-18. The 2026-11-17 product direction was approved; this Flutter port is proposed for review. [Specification](studio-visual-foundation-spec.md) · [Plan](plans/2026-11-17-studio-visual-foundation-plan.md).

## Direction

Make Studio more coherent, readable and usable on phone and desktop by improving its existing Flutter design system and shared components first. Use the [Flutter design system](../studio-flutter/design-system.md) as the source of truth: Ember tokens, Geist type, shared kit, semantic states and the prescribed narrow/medium/wide layouts. Screens compose the kit; any system change follows §10 and includes documentation, kit, gallery, component tests and intentional goldens in the same change. Do not introduce a competing theme or retrofit the retired Svelte visual language.

Audit the rendered Flutter app and existing tests before changing it. Prioritize defects that actually reproduce in the current build: navigation and focus at narrow widths, text/controls and contrast, shared states and agent headers. Preserve accessibility, keyboard operation, responsiveness and reduced-motion behavior. An old Svelte bug is a test scenario, not proof that Flutter has the same bug. A design-system token change requires measured evidence, coordinated documentation and tests; do not preselect replacement colors.

## Historical evidence

The following captures show the **Svelte** Studio at `47fa29d7` on 2026-11-17, before the Flutter port. They are retained to explain the original audit, not as Flutter baselines. The diagnostic is a temporary Svelte preview with a navigation background removed; it is not an unmodified product capture.

- [Desktop Fleet](screenshots/ui-foundation-2026-11-17/fleet-desktop.png)
- [Phone navigation overlay](screenshots/ui-foundation-2026-11-17/mobile-overlay-baseline.png)
- [Phone diagnostic](screenshots/ui-foundation-2026-11-17/fleet-phone-diagnostic.png)
- [Desktop work item](screenshots/ui-foundation-2026-11-17/work-item-active-desktop.png)

Capture and label fresh Flutter evidence during implementation, recording revision, viewport, theme and fixture/live data. Mock designs in the Flutter programme are not current application screenshots. The [cutover record](../studio-flutter/f8-cutover.md) identifies the served route at the time of testing.
