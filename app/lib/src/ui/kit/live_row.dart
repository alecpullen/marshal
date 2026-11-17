import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'dot.dart';
import 'vocabulary.dart';

/// The row work is happening on (design-system.md §5): the running dot, what
/// the agent is doing now, and the latest line it has produced.
///
/// It is the one row that moves, and only through its dot (§9). The detail is
/// mono when it is output and body when it is a sentence, decided by [mono].
class LiveRow extends StatelessWidget {
  const LiveRow({
    super.key,
    required this.headline,
    this.detail,
    this.mono = false,
    this.failed = false,
  });

  /// What is happening: "Running the tests", "Reading the parser".
  final String headline;

  /// The latest line, while there is one.
  final String? detail;

  /// Whether [detail] is output (mono) rather than a sentence.
  final bool mono;

  /// Whether the work failed. A failed live row still shows its word, so the
  /// status is never colour alone.
  final bool failed;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final detail = this.detail;
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: Space.s4),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: const EdgeInsets.only(top: Space.s4),
            child: Dot(failed ? Status.failed : Status.running),
          ),
          const SizedBox(width: Space.s8),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  headline,
                  style: e.bodyStrong,
                  maxLines: 2,
                  overflow: TextOverflow.ellipsis,
                ),
                if (detail != null) ...[
                  const SizedBox(height: Space.s2),
                  Text(
                    detail,
                    style: (mono ? e.monoSmall : e.small).tint(e.muted),
                    maxLines: 3,
                    overflow: TextOverflow.ellipsis,
                  ),
                ],
              ],
            ),
          ),
        ],
      ),
    );
  }
}
