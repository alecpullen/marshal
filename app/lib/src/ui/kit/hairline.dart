import 'package:flutter/widgets.dart';

import '../tokens.dart';

/// A one-pixel divider in `border` (design-system.md §5): between rows in a
/// `Box`, and between regions of the shell.
class Hairline extends StatelessWidget {
  /// A horizontal line across its parent.
  const Hairline({super.key}) : _vertical = false;

  /// A vertical line down its parent.
  const Hairline.vertical({super.key}) : _vertical = true;

  final bool _vertical;

  @override
  Widget build(BuildContext context) {
    final line = ColoredBox(color: Ember.of(context).border);
    return _vertical
        ? SizedBox(width: Sizes.border, child: line)
        : SizedBox(height: Sizes.border, child: line);
  }
}
