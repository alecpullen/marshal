import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'btn.dart';
import 'hairline.dart';
import 'icon_btn.dart';
import 'vocabulary.dart';

/// A dialog, or on the narrow layout a bottom sheet (design-system.md §5).
/// One per screen at most.
///
/// It has a title with a close button, its content, and a row of actions in
/// which at most one is primary and it comes first. Open it with
/// [Sheet.show]; Escape and a click outside close it too. It dims what is
/// behind it with the `scrim` token.
class Sheet extends StatelessWidget {
  const Sheet({
    super.key,
    required this.title,
    required this.child,
    this.actions = const [],
  });

  final String title;
  final Widget child;
  final List<Widget> actions;

  /// The widest a sheet grows: the reading column.
  static const double maxWidth = Layout.reading;

  /// Shows [sheet] over the current screen: centred on a wide window, along
  /// the bottom edge on a narrow one. Nothing animates (§9).
  static Future<T?> show<T>(BuildContext context, Sheet sheet) {
    final e = Ember.of(context);
    return showGeneralDialog<T>(
      context: context,
      barrierDismissible: true,
      barrierLabel: 'Close',
      barrierColor: e.scrim,
      transitionDuration: Duration.zero,
      pageBuilder: (context, _, _) {
        final narrow = MediaQuery.sizeOf(context).width < Layout.narrow;
        return SafeArea(
          child: Align(
            alignment: narrow ? Alignment.bottomCenter : Alignment.center,
            child: Padding(
              padding: EdgeInsets.all(narrow ? 0 : Space.s24),
              child: ConstrainedBox(
                constraints: const BoxConstraints(maxWidth: maxWidth),
                child: _Surface(narrow: narrow, child: sheet),
              ),
            ),
          ),
        );
      },
    );
  }

  @override
  Widget build(BuildContext context) {
    assert(() {
      final primaries = [
        for (final (i, a) in actions.indexed)
          if (a is Btn && a.primary) i,
      ];
      return primaries.isEmpty || (primaries.length == 1 && primaries[0] == 0);
    }(), 'Sheet: at most one primary action, and it comes first');

    final e = Ember.of(context);
    return Column(
      mainAxisSize: MainAxisSize.min,
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(
            Space.s16,
            Space.s12,
            Space.s8,
            Space.s12,
          ),
          child: Row(
            children: [
              Expanded(child: Text(title, style: e.title)),
              IconBtn(
                Glyphs.xmark,
                tooltip: 'Close',
                onTap: () => Navigator.of(context).maybePop(),
              ),
            ],
          ),
        ),
        const Hairline(),
        Flexible(
          child: SingleChildScrollView(
            padding: const EdgeInsets.all(Space.s16),
            child: child,
          ),
        ),
        if (actions.isNotEmpty) ...[
          const Hairline(),
          Padding(
            padding: const EdgeInsets.all(Space.s12),
            child: Wrap(
              spacing: Space.s8,
              runSpacing: Space.s8,
              children: actions,
            ),
          ),
        ],
      ],
    );
  }
}

/// The sheet's frame: the one background and a border. Along the bottom
/// edge only its top corners are round.
class _Surface extends StatelessWidget {
  const _Surface({required this.narrow, required this.child});

  final bool narrow;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final radius = narrow
        ? const BorderRadius.vertical(top: Radius.circular(Radii.panel))
        : BorderRadius.circular(Radii.panel);
    return DecoratedBox(
      decoration: BoxDecoration(
        color: e.bg,
        border: Border.all(color: e.border, width: Sizes.border),
        borderRadius: radius,
      ),
      child: ClipRRect(
        borderRadius: radius,
        child: DefaultTextStyle(style: e.body, child: child),
      ),
    );
  }
}
