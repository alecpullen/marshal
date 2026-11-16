import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'box.dart';
import 'btn.dart';
import 'press.dart';
import 'tip.dart';
import 'vocabulary.dart';

/// What an ask is. The first four block work and draw as needing you; a
/// review draft and an intake proposal are there to look at.
enum AskKind {
  decision('Decision', Glyphs.arrow_branch, blocking: true),
  question('Question', Glyphs.question_circle, blocking: true),
  approval('Approval', Glyphs.lock_shield, blocking: true),
  network('Network', Glyphs.globe, blocking: true),
  review('Review draft', Glyphs.text_bubble, blocking: false),
  intake('Intake', Glyphs.tray_arrow_down, blocking: false);

  const AskKind(this.word, this.icon, {required this.blocking});

  final String word;
  final IconData icon;

  /// Whether work waits on the answer.
  final bool blocking;
}

/// One answer an ask offers. The first is the primary one.
@immutable
class AskAction {
  const AskAction(this.label, {required this.onTap});

  final String label;
  final VoidCallback? onTap;
}

/// Where an ask is shown, which decides how much of it is drawn.
enum AskPlacement {
  /// The Inbox: everything.
  inbox,

  /// The Overview's "Needs you": the body only for a decision.
  overview,

  /// Pinned in its own thread: no source line, since you are in it.
  thread,
}

/// Something waiting on the user (design-system.md §5): the only way an
/// ask is shown, the same card in the Inbox, on the Overview and pinned in
/// its thread, so answering it anywhere is one action.
///
/// A blocking ask has the `attention` border and a `warn` icon; its kind is
/// also spelled out, so it is never colour alone. Its first action is the
/// primary one (§5: at most one, and first).
class AskCard extends StatelessWidget {
  const AskCard({
    super.key,
    required this.kind,
    required this.title,
    this.body,
    required this.waiting,
    this.waitingSince,
    this.source,
    this.onOpenSource,
    required this.actions,
    this.placement = AskPlacement.inbox,
  });

  final AskKind kind;

  /// The question itself; for an approval, the command to approve.
  final String title;

  /// The context the answer needs.
  final String? body;

  /// How long it has waited, short and relative: "12m".
  final String waiting;

  /// The exact time it was asked, for the tooltip on [waiting] (§8).
  final String? waitingSince;

  /// Where it came from: "marshal › Reliable session recovery".
  final String? source;
  final VoidCallback? onOpenSource;

  final List<AskAction> actions;
  final AskPlacement placement;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final ink = kind.blocking ? e.warn : e.muted;
    final source = placement == AskPlacement.thread ? null : this.source;
    final showBody =
        body != null &&
        (placement != AskPlacement.overview || kind == AskKind.decision);

    return Box(
      tone: kind.blocking ? BoxTone.attention : BoxTone.plain,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(kind.icon, size: Sizes.iconRow, color: ink),
              const SizedBox(width: Space.s6),
              Text(kind.word, style: e.caption.tint(ink)),
              if (source != null) ...[
                const SizedBox(width: Space.s6),
                Text('·', style: e.caption.tint(e.muted)),
                const SizedBox(width: Space.s6),
                Expanded(
                  child: Press(
                    onTap: onOpenSource,
                    builder: (context, state) => Text(
                      source,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: e.small.tint(state.hovered ? e.fg : e.muted),
                    ),
                  ),
                ),
              ] else
                const Spacer(),
              const SizedBox(width: Space.s12),
              _since(
                Text('waiting $waiting', style: e.monoSmall.tint(e.muted)),
              ),
            ],
          ),
          const SizedBox(height: Space.s8),
          Text(title, style: kind == AskKind.approval ? e.mono : e.strong),
          if (showBody) ...[
            const SizedBox(height: Space.s4),
            Text(body!, style: e.small.tint(e.muted)),
          ],
          const SizedBox(height: Space.s12),
          Wrap(
            spacing: Space.s8,
            runSpacing: Space.s8,
            children: [
              for (final (i, action) in actions.indexed)
                i == 0
                    ? Btn.primary(action.label, onTap: action.onTap)
                    : Btn(action.label, onTap: action.onTap),
            ],
          ),
        ],
      ),
    );
  }

  Widget _since(Widget child) {
    final since = waitingSince;
    return since == null ? child : Tip(message: since, child: child);
  }
}
