import 'package:flutter/widgets.dart';

import '../tokens.dart';

/// A block of output the agent produced: a command's tail, a file's
/// contents, a diff's body (design-system.md §5).
///
/// It is the only filled block in the transcript, in `codeBg`, and it is
/// mono at every density: output is read for its characters, never for its
/// shape. A capped block says so in words, so nothing looks complete that
/// was cut.
class OutputBlock extends StatelessWidget {
  const OutputBlock({super.key, required this.text, this.truncated = false});

  /// The output, verbatim. Newlines are kept.
  final String text;

  /// Whether the agent capped the output it sent. The cap's line says so.
  final bool truncated;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return DecoratedBox(
      decoration: BoxDecoration(
        color: e.codeBg,
        borderRadius: BorderRadius.circular(Radii.control),
      ),
      child: Padding(
        padding: const EdgeInsets.all(Space.s8),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(text, style: e.mono),
            if (truncated)
              Padding(
                padding: const EdgeInsets.only(top: Space.s4),
                child: Text(
                  'Output truncated',
                  style: e.monoSmall.tint(e.muted),
                ),
              ),
          ],
        ),
      ),
    );
  }
}
