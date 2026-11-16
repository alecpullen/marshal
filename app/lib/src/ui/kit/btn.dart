import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'press.dart';

/// A text button (design-system.md §5): plain, or primary in `accent`.
///
/// At most one primary in any group of actions, and it comes first. A
/// destructive action is plain and confirms before it acts. The label is a
/// verb, or a verb and an object (§8). With no [onTap] the button is
/// disabled: outlined, its label muted, whatever its kind.
class Btn extends StatelessWidget {
  /// A plain button: outlined, filled only on hover.
  const Btn(this.label, {super.key, this.onTap, this.icon}) : primary = false;

  /// The one primary action in its group: filled with `accent`.
  const Btn.primary(this.label, {super.key, this.onTap, this.icon})
    : primary = true;

  final String label;
  final VoidCallback? onTap;

  /// An optional icon before the label.
  final IconData? icon;

  /// Whether this is the group's primary action.
  final bool primary;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Press(
      onTap: onTap,
      builder: (context, state) {
        final filled = primary && state.enabled;
        final ink = !state.enabled
            ? e.muted
            : filled
            ? e.onAccent
            : e.fg;
        Widget body = Container(
          // At least 28; taller only when the text is scaled up.
          constraints: const BoxConstraints(minHeight: Sizes.control),
          padding: const EdgeInsets.symmetric(horizontal: Space.s12),
          decoration: BoxDecoration(
            color: filled ? e.accent : (state.hovered ? e.hover : null),
            border: Border.all(
              color: filled ? e.accent : e.border,
              width: Sizes.border,
            ),
            borderRadius: BorderRadius.circular(Radii.control),
          ),
          child: Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              if (icon != null) ...[
                Icon(icon, size: Sizes.iconRow, color: ink),
                const SizedBox(width: Space.s6),
              ],
              Flexible(
                child: Text(
                  label,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: e.body.tint(ink),
                ),
              ),
            ],
          ),
        );
        if (filled && state.hovered) {
          // The hover fill, laid over the accent so a primary button answers
          // the pointer like every other control does.
          body = DecoratedBox(
            position: DecorationPosition.foreground,
            decoration: BoxDecoration(
              color: e.hover,
              borderRadius: BorderRadius.circular(Radii.control),
            ),
            child: body,
          );
        }
        return body;
      },
    );
  }
}
