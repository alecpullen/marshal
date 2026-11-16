import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'press.dart';
import 'tip.dart';

/// An icon-only button (design-system.md §5). It has no words of its own,
/// so its [tooltip] is required: it names the control or states what it
/// does, and is also its name for assistive technology.
class IconBtn extends StatelessWidget {
  const IconBtn(this.icon, {super.key, required this.tooltip, this.onTap});

  final IconData icon;
  final String tooltip;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Tip(
      message: tooltip,
      child: Press(
        onTap: onTap,
        semanticLabel: tooltip,
        builder: (context, state) => Container(
          width: Sizes.control,
          height: Sizes.control,
          decoration: BoxDecoration(
            color: state.hovered ? e.hover : null,
            borderRadius: BorderRadius.circular(Radii.control),
          ),
          child: Icon(
            icon,
            size: Sizes.iconRow,
            color: !state.enabled
                ? e.faint
                : state.hovered
                ? e.fg
                : e.muted,
          ),
        ),
      ),
    );
  }
}
