import 'package:flutter/widgets.dart';

import '../tokens.dart';

/// The one tooltip (design-system.md §9). The kit's icon-only controls,
/// dots, chips with icons, bars and strips put one on themselves; a screen
/// uses this for anything else it must explain, such as the exact time
/// behind a relative one (§8).
///
/// It names the control or states the consequence, and does not repeat
/// visible text. It appears after a short hover and does not animate (§9:
/// only the running dot moves).
class Tip extends StatelessWidget {
  const Tip({super.key, required this.message, required this.child});

  final String message;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    return RawTooltip(
      semanticsTooltip: message,
      hoverDelay: const Duration(milliseconds: 500),
      animationStyle: AnimationStyle.noAnimation,
      tooltipBuilder: (context, animation) => _Bubble(message),
      child: child,
    );
  }
}

class _Bubble extends StatelessWidget {
  const _Bubble(this.message);

  final String message;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return ConstrainedBox(
      // A tooltip is a line or two, never wider than the sidebar.
      constraints: const BoxConstraints(maxWidth: Layout.sidebar),
      child: DecoratedBox(
        decoration: BoxDecoration(
          color: e.bg,
          border: Border.all(color: e.border, width: Sizes.border),
          borderRadius: BorderRadius.circular(Radii.control),
        ),
        child: Padding(
          padding: const EdgeInsets.symmetric(
            horizontal: Space.s8,
            vertical: Space.s4,
          ),
          child: Text(message, style: e.small),
        ),
      ),
    );
  }
}
