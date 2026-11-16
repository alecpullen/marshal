import 'package:flutter/widgets.dart';

import '../tokens.dart';

/// What the user said (design-system.md §5): one prompt, right-aligned, in a
/// bordered block at the reading role.
///
/// It is a row of the transcript, not a chat bubble: the border is the same
/// `border` every other outline uses, there is no fill, and the alignment is
/// the only thing that tells it from the agent's own words.
class TurnPrompt extends StatelessWidget {
  const TurnPrompt({super.key, required this.text});

  /// The prompt, as the user wrote it. Multi-line text keeps its breaks.
  final String text;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Align(
      alignment: Alignment.centerRight,
      child: Padding(
        padding: const EdgeInsets.only(left: Space.s32, bottom: Space.s16),
        child: DecoratedBox(
          decoration: BoxDecoration(
            border: Border.all(color: e.border, width: Sizes.border),
            borderRadius: BorderRadius.circular(Radii.control),
          ),
          child: Padding(
            padding: const EdgeInsets.symmetric(
              horizontal: Space.s12,
              vertical: Space.s8,
            ),
            child: Text(text, style: e.reading),
          ),
        ),
      ),
    );
  }
}
