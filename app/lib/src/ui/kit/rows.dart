import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'box.dart';
import 'hairline.dart';
import 'press.dart';
import 'spark.dart';
import 'state_message.dart';
import 'vocabulary.dart';

/// A row's one-day spark: the values, and what they are of for assistive
/// technology (design-system.md §5, `Spark`).
@immutable
class RowSpark {
  const RowSpark(this.values, {required this.label});

  final List<double> values;

  /// What the shape is of, for a reader who cannot see it.
  final String label;
}

/// One row of a [Rows] list: a name, what it is, and its current value.
@immutable
class RowItem {
  /// A row that may open somewhere: with [onTap] it shows a chevron.
  const RowItem({
    required this.name,
    required this.description,
    this.value,
    this.spark,
    this.onTap,
  }) : planned = false;

  /// A capability that does not exist yet, drawn as a disabled row with
  /// [description] saying so (design-system.md §7).
  const RowItem.planned({required this.name, required this.description})
    : value = null,
      spark = null,
      onTap = null,
      planned = true;

  final String name;
  final String description;
  final String? value;

  /// A day's readings, drawn beside the value: a watch's samples.
  final RowSpark? spark;

  final VoidCallback? onTap;
  final bool planned;
}

/// A list of name, description and value in one `Box` (design-system.md
/// §5): every settings-style list.
class Rows extends StatelessWidget {
  const Rows(this.rows, {super.key});

  final List<RowItem> rows;

  @override
  Widget build(BuildContext context) {
    return Box.flush(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          for (var i = 0; i < rows.length; i++) ...[
            if (i > 0) const Hairline(),
            if (rows[i].planned)
              StateMessage.planned(
                name: rows[i].name,
                note: rows[i].description,
              )
            else
              _Row(rows[i]),
          ],
        ],
      ),
    );
  }
}

class _Row extends StatelessWidget {
  const _Row(this.row);

  final RowItem row;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final value = row.value;
    return Press(
      onTap: row.onTap,
      ringInside: true,
      builder: (context, state) => Container(
        color: state.hovered ? e.hover : null,
        padding: const EdgeInsets.symmetric(
          horizontal: Space.s16,
          vertical: Space.s12,
        ),
        child: Row(
          children: [
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(row.name, style: e.bodyStrong),
                  const SizedBox(height: Space.s2),
                  Text(row.description, style: e.small.tint(e.muted)),
                ],
              ),
            ),
            if (row.spark case final spark?) ...[
              const SizedBox(width: Space.s12),
              SizedBox(
                width: Sizes.touch * 2,
                child: Spark(spark.values, semanticLabel: spark.label),
              ),
            ],
            if (value != null) ...[
              const SizedBox(width: Space.s12),
              // At the right edge, and never wider than the sidebar, so a
              // long value ellipsizes rather than crowding out the name.
              ConstrainedBox(
                constraints: const BoxConstraints(maxWidth: Layout.sidebar),
                child: Text(
                  value,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  textAlign: TextAlign.end,
                  style: e.small.tint(e.muted),
                ),
              ),
            ],
            if (state.enabled) ...[
              const SizedBox(width: Space.s8),
              Icon(
                Glyphs.chevron_right,
                size: Sizes.iconInline,
                color: e.faint,
              ),
            ],
          ],
        ),
      ),
    );
  }
}
