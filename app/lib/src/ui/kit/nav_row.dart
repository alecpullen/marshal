import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'press.dart';
import 'tip.dart';
import 'vocabulary.dart';

/// A row of the sidebar (design-system.md §5): an icon, a label, and an
/// optional count at the right end. For a project it draws a disclosure
/// chevron instead of an icon.
///
/// The selected row is filled with `selected` and its icon takes `accent`;
/// nothing else on the row is coloured (§3: colour means status, and a row of
/// navigation carries none). A row that shows a thread is a `ThreadRow`, not a
/// `NavRow`: a thread has a status, and this row has none.
class NavRow extends StatelessWidget {
  const NavRow({
    super.key,
    required this.label,
    required this.onTap,
    this.icon,
    this.count,
    this.selected = false,
    this.expanded,
    this.onToggle,
    this.indent = false,
    this.mono = false,
  });

  /// What the row says.
  final String label;

  /// Set the label in mono: for a row that names a path or another
  /// identifier (§3), as `Pill.mono` does for a chip.
  final bool mono;

  /// Opens what the row names.
  final VoidCallback onTap;

  /// The row's mark; a project's row has a chevron instead.
  final IconData? icon;

  /// What the row counts, drawn at its right end in mono: the Inbox's queue.
  final int? count;

  /// This row is where the person is.
  final bool selected;

  /// Whether what this row discloses is open. Null for a row that discloses
  /// nothing, which draws no chevron.
  final bool? expanded;

  /// Opens and closes what the row discloses. The chevron is its own control,
  /// so opening a project is not the same click as opening the project.
  final VoidCallback? onToggle;

  /// Indented one step, as a disclosure's rows are.
  final bool indent;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final expanded = this.expanded;
    return Press(
      onTap: onTap,
      selected: selected,
      builder: (context, state) => Container(
        constraints: const BoxConstraints(minHeight: Sizes.row),
        padding: EdgeInsets.only(
          left: indent ? Space.s16 : Space.s2,
          right: Space.s8,
        ),
        decoration: BoxDecoration(
          color: selected ? e.selected : (state.hovered ? e.hover : null),
          borderRadius: BorderRadius.circular(Radii.control),
        ),
        child: Row(
          children: [
            if (expanded != null) _disclosure(e, expanded),
            if (icon != null) ...[
              Icon(
                icon,
                size: Sizes.iconNav,
                color: selected ? e.accent : e.muted,
              ),
              const SizedBox(width: Space.s8),
            ],
            Expanded(
              child: Text(
                label,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: mono
                    ? e.mono.tint(selected ? e.fg : e.muted)
                    : (selected ? e.bodyStrong : e.body),
              ),
            ),
            if (count != null) ...[
              const SizedBox(width: Space.s8),
              Text(
                '$count',
                style: e.monoSmall.tint(selected ? e.fg : e.muted),
              ),
            ],
          ],
        ),
      ),
    );
  }

  /// The chevron that opens and closes a project. It has no words, so its
  /// tooltip names it (§9).
  Widget _disclosure(Ember e, bool open) {
    final message = open ? 'Hide its threads' : 'Show its threads';
    return Tip(
      message: message,
      child: Press(
        onTap: onToggle,
        semanticLabel: message,
        builder: (context, state) => SizedBox(
          width: Space.s24,
          height: Sizes.row,
          child: Icon(
            open ? Glyphs.chevron_down : Glyphs.chevron_right,
            size: Sizes.iconInline,
            color: e.faint,
          ),
        ),
      ),
    );
  }
}
