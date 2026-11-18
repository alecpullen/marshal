# Studio visual and interaction foundation: specification

Updated: 2026-11-18. The product direction was approved on 2026-11-17; this Flutter port is proposed for review. [Design](studio-visual-foundation-design.md) · [Plan](plans/2026-11-17-studio-visual-foundation-plan.md).

## Scope and behavior

Audit and improve the Flutter Studio's shared visual and interaction foundation, with phone usability first where the current app demonstrates a defect. Use `app/lib/src/ui/tokens.dart` and `app/lib/src/ui/kit/kit.dart` and obey every rule in [the design system](../studio-flutter/design-system.md), including §10 for changes. Its colors, type roles, spacing, breakpoints, states and no-panel-fill/no-shadow rules supersede the former Svelte-specific palette and layout prescriptions. Keep existing user journeys and authorization.

Verify navigation, representative Fleet, work-item and thread screens at narrow and desktop widths, in both themes. Ensure navigation does not obscure or trap the content, controls retain accessible names and focus, and long labels and phone-sized viewports remain usable. Make only evidence-backed shared fixes; preserve exact timestamps via the design system's relative-time and tooltip convention. Implement missing reusable behavior in the kit and gallery rather than introducing screen-specific alternatives. No theme or token adjustment is authorized solely by the historical Svelte screenshots.

Record actual Flutter screenshots and fixture provenance before and after. Run the design-system enforcement and component tests, check keyboard and semantics, and separately identify real bridge, browser and physical-device coverage. Follow the Flutter cutover's current build and hosting instructions.
