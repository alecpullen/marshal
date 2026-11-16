import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'box.dart';
import 'press.dart';
import 'tip.dart';
import 'vocabulary.dart';

/// What one line of a unified diff is.
///
/// A file's header (`diff --git`, `index`, `---`, `+++`), a hunk's range
/// (`@@ … @@`), and the three body kinds. A `\ No newline at end of file`
/// marker is a header line: it says something about the file, not about a
/// line of it.
enum DiffLineKind { header, hunk, context, added, removed }

/// Which side of a diff a line is on: the file before the change, or after.
/// [wire] is the word a review comment carries for it.
enum DiffSide {
  before('old'),
  after('new');

  const DiffSide(this.wire);

  /// The word the wire uses for this side.
  final String wire;
}

/// One line of a unified diff, with the line number it has on the side it
/// is on.
@immutable
class DiffLine {
  const DiffLine(this.kind, this.text, {this.oldLine, this.newLine});

  final DiffLineKind kind;

  /// The line's text, without its diff marker.
  final String text;

  /// Its number in the file before the change, for a removed or context
  /// line.
  final int? oldLine;

  /// Its number in the file after the change, for an added or context line.
  final int? newLine;

  /// The side a comment on this line is anchored to, or null for a line a
  /// comment cannot be left on.
  DiffSide? get side => switch (kind) {
    DiffLineKind.removed => DiffSide.before,
    DiffLineKind.added => DiffSide.after,
    DiffLineKind.context || DiffLineKind.hunk || DiffLineKind.header => null,
  };

  /// The number shown in the gutter: the line's own side, or the new one.
  int? get number => switch (kind) {
    DiffLineKind.removed => oldLine,
    DiffLineKind.added || DiffLineKind.context => newLine,
    DiffLineKind.hunk || DiffLineKind.header => null,
  };

  /// The character a unified diff marks this line with.
  String get marker => switch (kind) {
    DiffLineKind.added => '+',
    DiffLineKind.removed => '-',
    DiffLineKind.context => ' ',
    DiffLineKind.hunk || DiffLineKind.header => '',
  };
}

/// The hunk header's ranges: `@@ -a,b +c,d @@`.
final _hunk = RegExp(r'^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@');

/// The lines of [diff], as a unified diff writes them.
///
/// A malformed body — a line that starts with none of the markers — is read
/// as context rather than dropped: the diff a person is reading is what the
/// agent sent, not a repair of it.
List<DiffLine> parseDiff(String diff) {
  final lines = <DiffLine>[];
  var oldNo = 0;
  var newNo = 0;
  for (final raw in diff.split('\n')) {
    if (raw.isEmpty) continue;
    final range = _hunk.firstMatch(raw);
    if (range != null) {
      oldNo = int.tryParse(range.group(1)!) ?? oldNo;
      newNo = int.tryParse(range.group(2)!) ?? newNo;
      lines.add(DiffLine(DiffLineKind.hunk, raw));
      continue;
    }
    if (raw.startsWith('diff ') ||
        raw.startsWith('index ') ||
        raw.startsWith('--- ') ||
        raw.startsWith('+++ ') ||
        raw.startsWith('\\ ')) {
      lines.add(DiffLine(DiffLineKind.header, raw));
      continue;
    }
    switch (raw[0]) {
      case '+':
        lines.add(
          DiffLine(DiffLineKind.added, raw.substring(1), newLine: newNo++),
        );
      case '-':
        lines.add(
          DiffLine(DiffLineKind.removed, raw.substring(1), oldLine: oldNo++),
        );
      case ' ':
        lines.add(
          DiffLine(
            DiffLineKind.context,
            raw.substring(1),
            oldLine: oldNo++,
            newLine: newNo++,
          ),
        );
      default:
        lines.add(
          DiffLine(
            DiffLineKind.context,
            raw,
            oldLine: oldNo++,
            newLine: newNo++,
          ),
        );
    }
  }
  return lines;
}

/// A unified diff (design-system.md §5): one line per row, in `mono`, a
/// removed line on `diffRemoved` and an added one on `diffAdded`, with a
/// gutter that shows the line's number and takes a comment.
///
/// A comment is a click on the gutter of a removed or added line: the
/// control is drawn only where [onComment] is given, so a diff that cannot
/// be commented on shows no control that does nothing (§7).
class DiffView extends StatelessWidget {
  const DiffView({
    super.key,
    required this.diff,
    this.onComment,
    this.emptyNote = 'No changes to show',
  });

  /// The diff's unified text.
  final String diff;

  /// Leaves a comment on a line: which line, and which side of the change.
  final void Function(DiffLine line)? onComment;

  /// What the block says when the diff has no lines.
  final String emptyNote;

  /// The gutter's width: the line's number, or its comment control. It is
  /// the touch target's width, so the comment control keeps §4's 44 at every
  /// window size.
  static const double gutter = Sizes.touch;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final lines = parseDiff(diff);
    if (lines.isEmpty) {
      return Text(emptyNote, style: e.small.tint(e.muted));
    }
    return Box.flush(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          for (final line in lines) _Line(line: line, onComment: onComment),
        ],
      ),
    );
  }
}

/// One row of a [DiffView].
class _Line extends StatelessWidget {
  const _Line({required this.line, this.onComment});

  final DiffLine line;
  final void Function(DiffLine line)? onComment;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final fill = switch (line.kind) {
      DiffLineKind.removed => e.diffRemoved,
      DiffLineKind.added => e.diffAdded,
      DiffLineKind.hunk || DiffLineKind.context || DiffLineKind.header => null,
    };
    final ink = switch (line.kind) {
      DiffLineKind.hunk || DiffLineKind.header => e.muted,
      DiffLineKind.added ||
      DiffLineKind.removed ||
      DiffLineKind.context => e.fg,
    };
    final number = line.number;
    final side = line.side;
    final commentable = onComment != null && side != null;
    return DecoratedBox(
      decoration: BoxDecoration(color: fill),
      child: Padding(
        padding: const EdgeInsets.symmetric(
          horizontal: Space.s8,
          vertical: Space.s2,
        ),
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            SizedBox(
              width: DiffView.gutter,
              child: Row(
                children: [
                  if (commentable)
                    _Comment(
                      message: 'Comment on line ${number ?? 0}',
                      onTap: () => onComment!(line),
                    ),
                  if (!commentable)
                    Expanded(
                      child: Text(
                        number == null ? '' : '$number',
                        style: e.monoSmall.tint(e.faint),
                        textAlign: TextAlign.right,
                        maxLines: 1,
                      ),
                    ),
                ],
              ),
            ),
            SizedBox(
              width: Space.s12,
              child: Text(
                line.marker,
                style: e.mono.tint(e.faint),
                maxLines: 1,
              ),
            ),
            Expanded(child: Text(line.text, style: e.mono.tint(ink))),
          ],
        ),
      ),
    );
  }
}

/// The gutter's comment control: an inline glyph on its own tint, with the
/// tooltip §9 requires.
class _Comment extends StatelessWidget {
  const _Comment({required this.message, required this.onTap});

  final String message;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Tip(
      message: message,
      child: Press(
        onTap: onTap,
        radius: Radii.bar,
        semanticLabel: message,
        builder: (context, state) => Icon(
          Glyphs.bubble_left,
          size: Sizes.iconInline,
          color: state.hovered ? e.accent : e.faint,
        ),
      ),
    );
  }
}
