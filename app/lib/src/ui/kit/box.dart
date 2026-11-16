import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'press.dart';

/// What a [Box]'s border says. design-system.md §5: border colour is
/// `border`, `attention` or `failing`, and nothing else.
enum BoxTone {
  /// An ordinary group.
  plain,

  /// Something that needs you.
  attention,

  /// Something that failed.
  failing,
}

/// A bordered group: the only container with a border (design-system.md
/// §5). No fill and no shadow; a [Box] with [onTap] shows the hover fill
/// every clickable thing shows.
///
/// [Box] pads its content by 16. [Box.flush] does not, for rows separated
/// by `Hairline`s that pad themselves.
class Box extends StatelessWidget {
  const Box({
    super.key,
    required this.child,
    this.tone = BoxTone.plain,
    this.onTap,
  }) : _padding = const EdgeInsets.all(Space.s16);

  const Box.flush({
    super.key,
    required this.child,
    this.tone = BoxTone.plain,
    this.onTap,
  }) : _padding = EdgeInsets.zero;

  final Widget child;
  final BoxTone tone;
  final VoidCallback? onTap;
  final EdgeInsets _padding;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final edge = switch (tone) {
      BoxTone.plain => e.border,
      BoxTone.attention => e.attention,
      BoxTone.failing => e.failing,
    };

    Widget frame(bool hovered) => DecoratedBox(
      decoration: BoxDecoration(
        color: hovered ? e.hover : null,
        border: Border.all(color: edge, width: Sizes.border),
        borderRadius: BorderRadius.circular(Radii.panel),
      ),
      child: ClipRRect(
        borderRadius: BorderRadius.circular(Radii.panel - Sizes.border),
        child: Padding(
          padding: _padding + const EdgeInsets.all(Sizes.border),
          child: child,
        ),
      ),
    );

    if (onTap == null) return frame(false);
    return Press(
      onTap: onTap,
      radius: Radii.panel,
      builder: (context, state) => frame(state.hovered),
    );
  }
}
