import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'tip.dart';

/// A small outlined chip (design-system.md §5): a fact or a setting, never
/// an action that leaves the screen.
///
/// [Pill] is words alone. [Pill.icon] leads with an icon, and because an
/// icon needs naming (§9) its tooltip is required.
class Pill extends StatelessWidget {
  /// A chip of words, with an optional tooltip.
  const Pill(this.label, {super.key, this.tooltip, this.mono = false})
    : icon = null;

  /// A chip led by an icon, which its [tooltip] names.
  const Pill.icon(
    this.label, {
    super.key,
    required IconData this.icon,
    required String this.tooltip,
    this.mono = false,
  });

  final String label;
  final IconData? icon;
  final String? tooltip;

  /// Set the label in mono: for an identifier such as a branch or a model.
  final bool mono;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final pill = Container(
      padding: const EdgeInsets.symmetric(
        horizontal: Space.s8,
        vertical: Space.s2,
      ),
      decoration: BoxDecoration(
        border: Border.all(color: e.border, width: Sizes.border),
        borderRadius: BorderRadius.circular(Radii.full),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          if (icon != null) ...[
            Icon(icon, size: Sizes.iconInline, color: e.muted),
            const SizedBox(width: Space.s4),
          ],
          Flexible(
            child: Text(
              label,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: (mono ? e.monoSmall : e.caption).tint(e.muted),
            ),
          ),
        ],
      ),
    );
    final tip = tooltip;
    return tip == null ? pill : Tip(message: tip, child: pill);
  }
}
