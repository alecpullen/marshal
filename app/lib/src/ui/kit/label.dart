import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'press.dart';
import 'vocabulary.dart';

/// A section label's one trailing link: "Open inbox", "All pull requests".
@immutable
class LabelLink {
  const LabelLink(this.text, {required this.onTap});

  final String text;
  final VoidCallback onTap;
}

/// An uppercase section label (design-system.md §5): the only uppercase
/// text there is (§3). It may carry a count and one trailing link.
class Label extends StatelessWidget {
  const Label(this.text, {super.key, this.count, this.link});

  final String text;
  final int? count;
  final LabelLink? link;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final link = this.link;
    return Row(
      children: [
        Flexible(
          child: Text(
            text.toUpperCase(),
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            style: e.label.tint(e.muted),
          ),
        ),
        if (count != null) ...[
          const SizedBox(width: Space.s6),
          Text('$count', style: e.monoSmall.tint(e.muted)),
        ],
        const Spacer(),
        if (link != null)
          Press(
            onTap: link.onTap,
            builder: (context, state) {
              final ink = state.hovered ? e.fg : e.muted;
              return Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Text(link.text, style: e.small.tint(ink)),
                  const SizedBox(width: Space.s4),
                  Icon(Glyphs.arrow_right, size: Sizes.iconInline, color: ink),
                ],
              );
            },
          ),
      ],
    );
  }
}
