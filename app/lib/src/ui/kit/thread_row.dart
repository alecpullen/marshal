import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'dot.dart';
import 'press.dart';
import 'tip.dart';
import 'vocabulary.dart';

/// A thread in a list (design-system.md §5): its status dot, its title and
/// its kind's icon, which a tooltip names (§6: kinds are told apart by icon
/// and tooltip, never by colour). The same row wherever threads are listed.
class ThreadRow extends StatelessWidget {
  const ThreadRow({
    super.key,
    required this.status,
    required this.title,
    required this.kind,
    this.selected = false,
    this.onTap,
  });

  final Status status;
  final String title;
  final ThreadKind kind;

  /// The thread that is open.
  final bool selected;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Press(
      onTap: onTap,
      selected: selected,
      builder: (context, state) => Container(
        height: Sizes.row,
        padding: const EdgeInsets.symmetric(horizontal: Space.s8),
        decoration: BoxDecoration(
          color: selected ? e.selected : (state.hovered ? e.hover : null),
          borderRadius: BorderRadius.circular(Radii.control),
        ),
        child: Row(
          children: [
            Dot(status),
            const SizedBox(width: Space.s8),
            Expanded(
              child: Text(
                title,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: selected ? e.bodyStrong : e.body,
              ),
            ),
            const SizedBox(width: Space.s8),
            Tip(
              message: kind.word,
              child: Icon(kind.icon, size: Sizes.iconInline, color: e.muted),
            ),
          ],
        ),
      ),
    );
  }
}
