import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'press.dart';

/// Something in a conversation that is not the agent talking to the person
/// (design-system.md §5): a harness notice, the agent's thinking, a run event,
/// a finished job.
///
/// It is one quiet line — its first, in `small` tinted `muted`, wrapping but
/// never truncated — and whatever it went on to say is folded
/// behind *Details*, so six lines of paths never read as six lines of the
/// agent's prose. No icon, fill or border: a notice is quieter than a row,
/// not louder than one.
class Notice extends StatefulWidget {
  const Notice({super.key, required this.text});

  /// The notice, one line per fact. Blank lines are dropped.
  final String text;

  @override
  State<Notice> createState() => _NoticeState();
}

class _NoticeState extends State<Notice> {
  bool _open = false;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    // CRLF and a lone CR end a line as LF does, so a message that carries
    // them keeps no carriage return in a drawn line or in the copied text.
    final lines = [
      for (final line in widget.text.split(RegExp(r'\r\n|\r|\n')))
        if (line.trim().isNotEmpty) line,
    ];
    if (lines.isEmpty) return const SizedBox.shrink();
    final rest = lines.skip(1).toList();
    return Padding(
      padding: const EdgeInsets.only(bottom: Space.s12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(lines.first, style: e.small.tint(e.muted)),
          if (_open)
            for (final line in rest)
              Padding(
                padding: const EdgeInsets.only(top: Space.s2),
                child: Text(line, style: e.small.tint(e.muted)),
              ),
          if (rest.isNotEmpty)
            Padding(
              padding: const EdgeInsets.only(top: Space.s4),
              // Open, the control follows the lines it reveals, so the
              // notice is read in the order the harness wrote it (§5).
              child: Press(
                onTap: () => setState(() => _open = !_open),
                semanticLabel: _open
                    ? 'Hide the notice\'s details'
                    : 'Show the notice\'s details',
                builder: (context, state) => Text(
                  _open ? 'Hide details' : 'Details',
                  style: e.small.tint(state.hovered ? e.fg : e.muted),
                ),
              ),
            ),
        ],
      ),
    );
  }
}
