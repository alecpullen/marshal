import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'btn.dart';

/// How wide a page's content may grow (design-system.md §4).
enum PageWidth {
  /// 1180: a page with more than one column of content.
  full(Layout.page),

  /// 860: a page that is a single list.
  list(Layout.pageList);

  const PageWidth(this.max);

  final double max;
}

/// Every page that is not a thread (design-system.md §5): a title, an
/// optional subtitle, its actions, and its content, scrolling vertically
/// and stopping at its maximum width rather than stretching (§4).
///
/// Of the [actions], at most one is a primary `Btn` and it comes first.
class PageBody extends StatelessWidget {
  const PageBody({
    super.key,
    required this.title,
    this.subtitle,
    this.actions = const [],
    this.width = PageWidth.full,
    required this.children,
  });

  final String title;
  final String? subtitle;
  final List<Widget> actions;
  final PageWidth width;
  final List<Widget> children;

  @override
  Widget build(BuildContext context) {
    assert(() {
      final primaries = [
        for (final (i, a) in actions.indexed)
          if (a is Btn && a.primary) i,
      ];
      return primaries.isEmpty || (primaries.length == 1 && primaries[0] == 0);
    }(), 'PageBody: at most one primary action, and it comes first');

    final e = Ember.of(context);
    final narrow = MediaQuery.sizeOf(context).width < Layout.narrow;
    final heading = Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          title,
          maxLines: 2,
          overflow: TextOverflow.ellipsis,
          style: e.heading,
        ),
        if (subtitle != null) ...[
          const SizedBox(height: Space.s4),
          Text(subtitle!, style: e.body.tint(e.muted)),
        ],
      ],
    );
    final buttons = Wrap(
      spacing: Space.s8,
      runSpacing: Space.s8,
      children: actions,
    );

    return SingleChildScrollView(
      padding: const EdgeInsets.fromLTRB(
        Layout.pageSide,
        Layout.pageTop,
        Layout.pageSide,
        Space.s32,
      ),
      child: Center(
        child: ConstrainedBox(
          constraints: BoxConstraints(maxWidth: width.max),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              if (narrow || actions.isEmpty) ...[
                heading,
                if (actions.isNotEmpty) ...[
                  const SizedBox(height: Space.s12),
                  Align(alignment: Alignment.centerLeft, child: buttons),
                ],
              ] else
                Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Expanded(child: heading),
                    const SizedBox(width: Space.s16),
                    buttons,
                  ],
                ),
              const SizedBox(height: Space.s20),
              ...children,
            ],
          ),
        ),
      ),
    );
  }
}
