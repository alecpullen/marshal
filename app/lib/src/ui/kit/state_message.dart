import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'btn.dart';
import 'pill.dart';
import 'vocabulary.dart';

/// The states every screen that shows data has (design-system.md §7).
enum StateKind {
  loading,
  empty,
  stale,
  error,
  unavailable,
  unauthorized,
  planned,
}

/// The one action a state offers: "Try again", "Start an agent".
@immutable
class StateAction {
  const StateAction(this.label, {required this.onTap});

  final String label;
  final VoidCallback onTap;
}

/// The only way loading, empty, stale, error, unavailable and planned are
/// shown (design-system.md §5, §7). Each constructor is one state, with
/// exactly what §7 says that state carries.
class StateMessage extends StatelessWidget {
  /// The layout with quiet placeholders; never a spinner page. [lines] is
  /// how many lines of content it stands in for.
  const StateMessage.loading({super.key, this.lines = 3})
    : kind = StateKind.loading,
      message = null,
      detail = null,
      action = null;

  /// One sentence saying what would appear here, and one action.
  const StateMessage.empty({
    super.key,
    required String this.message,
    required StateAction this.action,
  }) : kind = StateKind.empty,
       detail = null,
       lines = 0;

  /// The last data is still shown, plainly marked with its [age] ("4m").
  /// It never says the data failed: a lost connection is not a failure.
  const StateMessage.stale({super.key, required String age, this.detail})
    : kind = StateKind.stale,
      message = age,
      action = null,
      lines = 0;

  /// What failed, in plain words, and one action to retry.
  const StateMessage.error({
    super.key,
    required String this.message,
    required StateAction retry,
  }) : kind = StateKind.error,
       action = retry,
       detail = null,
       lines = 0;

  /// The host is unreachable: names it and when it was last seen.
  ///
  /// [lastSeen] is null when no read ever made a snapshot, so there is no age
  /// to give: the row says "Not seen yet" rather than printing "unknown ago".
  const StateMessage.unavailable({
    super.key,
    required String host,
    String? lastSeen,
    this.action,
  }) : kind = StateKind.unavailable,
       message = host,
       detail = lastSeen,
       lines = 0;

  /// The operator's proxy refused the request, so the page is signed out:
  /// names the host, and the one thing that clears it. Never drawn as
  /// unreachability — no retry and no amount of waiting turns a refusal into
  /// an answer.
  const StateMessage.unauthorized({super.key, String? host})
    : kind = StateKind.unauthorized,
      message = host,
      detail = signInAgain,
      action = null,
      lines = 0;

  /// A capability that does not exist yet: a disabled row where it will
  /// live, with one line saying so. Never drawn as working.
  const StateMessage.planned({
    super.key,
    required String name,
    required String note,
  }) : kind = StateKind.planned,
       message = name,
       detail = note,
       action = null,
       lines = 0;

  /// What a refused request takes to clear: the operator's proxy signs a
  /// browser in with a page load, so the sentence names that rather than
  /// offering a retry that cannot work.
  static const String signInAgain = 'Reload the page to sign in again.';

  final StateKind kind;
  final String? message;
  final String? detail;
  final StateAction? action;
  final int lines;

  /// A placeholder line's thickness.
  static const double placeholder = Space.s8;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return switch (kind) {
      StateKind.loading => Semantics(
        label: 'Loading',
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            for (var i = 0; i < lines; i++) ...[
              if (i > 0) const SizedBox(height: Space.s12),
              FractionallySizedBox(
                // Ragged, as text is: full, most, then a short last line.
                widthFactor: i == lines - 1 && lines > 1
                    ? 0.4
                    : (i.isEven ? 1 : 0.8),
                child: Container(
                  height: placeholder,
                  decoration: BoxDecoration(
                    color: e.border,
                    borderRadius: BorderRadius.circular(Radii.bar),
                  ),
                ),
              ),
            ],
          ],
        ),
      ),
      StateKind.empty => Padding(
        padding: const EdgeInsets.symmetric(vertical: Space.s24),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(message!, style: e.body.tint(e.muted)),
            const SizedBox(height: Space.s12),
            Btn(action!.label, onTap: action!.onTap),
          ],
        ),
      ),
      StateKind.stale => _line(
        e,
        Glyphs.clock,
        e.muted,
        'Last updated $message ago',
        detail,
      ),
      StateKind.error => _line(
        e,
        Glyphs.exclamationmark_circle,
        e.err,
        message!,
        null,
      ),
      StateKind.unavailable => _line(
        e,
        Glyphs.wifi_slash,
        e.muted,
        '$message is unreachable',
        detail == null ? 'Not seen yet' : 'Last seen $detail ago',
      ),
      // A refusal is not a silence: it needs the reader to sign in again, so
      // it carries the warning colour and the sentence that fixes it.
      StateKind.unauthorized => _line(
        e,
        Glyphs.lock,
        e.warn,
        '${message ?? 'This Studio'} is signed out',
        detail,
      ),
      StateKind.planned => Padding(
        padding: const EdgeInsets.symmetric(
          horizontal: Space.s16,
          vertical: Space.s12,
        ),
        child: Semantics(
          enabled: false,
          child: Row(
            // The pill sits beside the words, not halfway down a tall box.
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    Text(message!, style: e.bodyStrong.tint(e.muted)),
                    const SizedBox(height: Space.s2),
                    Text(detail!, style: e.small.tint(e.muted)),
                  ],
                ),
              ),
              const SizedBox(width: Space.s12),
              const Pill('Planned'),
            ],
          ),
        ),
      ),
    };
  }

  /// An icon and a sentence, with an optional second line and, under them,
  /// the state's action.
  Widget _line(Ember e, IconData icon, Color ink, String text, String? second) {
    final action = this.action;
    return Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Padding(
          padding: const EdgeInsets.only(top: Space.s2),
          child: Icon(icon, size: Sizes.iconRow, color: ink),
        ),
        const SizedBox(width: Space.s8),
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(text, style: e.body),
              if (second != null) ...[
                const SizedBox(height: Space.s2),
                Text(second, style: e.small.tint(e.muted)),
              ],
              if (action != null) ...[
                const SizedBox(height: Space.s12),
                Btn(action.label, onTap: action.onTap),
              ],
            ],
          ),
        ),
      ],
    );
  }
}
