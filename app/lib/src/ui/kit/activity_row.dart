import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'tip.dart';

/// One entry of a thread's activity: who acted, what they did, and when
/// (design-system.md §5, F6 decision 4).
///
/// The activity is a timeline, not a transcript: one line per thing that
/// happened, attributed to the person or the role in words. A detail — the
/// bridge's own kind for the event — is drawn small beneath it, so an entry
/// can be read at a glance and still name itself exactly.
class ActivityRow extends StatelessWidget {
  const ActivityRow({
    super.key,
    required this.who,
    required this.text,
    this.age,
    this.ageTip,
    this.detail,
  });

  /// The person or role in words: "You", "The implementer".
  final String who;

  /// What happened, in the bridge's own words.
  final String text;

  /// How long ago, already relative: a row draws it, it does not compute it.
  final String? age;

  /// The exact time behind [age], for its tooltip (design-system.md §8), or
  /// null when there is no exact time to show.
  final String? ageTip;

  /// The event's own kind, when it is worth naming beside the sentence.
  final String? detail;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final at = age;
    final tip = ageTip;
    final note = detail;
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: Space.s6),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(child: Text(who, style: e.bodyStrong)),
              if (at != null)
                tip == null
                    ? Text(at, style: e.monoSmall.tint(e.muted))
                    : Tip(
                        message: tip,
                        child: Text(at, style: e.monoSmall.tint(e.muted)),
                      ),
            ],
          ),
          const SizedBox(height: Space.s2),
          Text(text, style: e.body),
          if (note != null && note.isNotEmpty) ...[
            const SizedBox(height: Space.s2),
            Text(note, style: e.small.tint(e.muted)),
          ],
        ],
      ),
    );
  }
}
