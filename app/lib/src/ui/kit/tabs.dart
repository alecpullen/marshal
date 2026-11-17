import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'press.dart';

/// One tab: its name and, optionally, a count beside it.
@immutable
class TabItem {
  const TabItem(this.label, {this.count});

  final String label;
  final int? count;
}

/// Underlined text tabs (design-system.md §5): views of one object, never
/// navigation between objects. The current tab is underlined in `accent`.
///
/// Tabs that do not fit wrap onto another line rather than scroll sideways
/// (§4: nothing scrolls sideways).
class Tabs extends StatelessWidget {
  const Tabs({
    super.key,
    required this.tabs,
    required this.index,
    required this.onChanged,
  });

  final List<TabItem> tabs;
  final int index;
  final ValueChanged<int> onChanged;

  /// The underline's thickness.
  static const double underline = Space.s2;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Wrap(
      spacing: Space.s16,
      children: [
        for (var i = 0; i < tabs.length; i++)
          Press(
            onTap: () => onChanged(i),
            selected: i == index,
            radius: Radii.bar,
            builder: (context, state) {
              final current = i == index;
              final count = tabs[i].count;
              return Container(
                padding: const EdgeInsets.only(top: Space.s8, bottom: Space.s6),
                decoration: BoxDecoration(
                  border: Border(
                    bottom: BorderSide(
                      color: current ? e.accent : e.none,
                      width: underline,
                    ),
                  ),
                ),
                child: Row(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    Text(
                      tabs[i].label,
                      style: current
                          ? e.bodyStrong
                          : e.body.tint(state.hovered ? e.fg : e.muted),
                    ),
                    if (count != null) ...[
                      const SizedBox(width: Space.s6),
                      Text('$count', style: e.monoSmall.tint(e.muted)),
                    ],
                  ],
                ),
              );
            },
          ),
      ],
    );
  }
}
