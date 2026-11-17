import 'package:cupertino_ui/cupertino_ui.dart' show CupertinoIcons;
import 'package:flutter/widgets.dart';

import '../tokens.dart';

/// design-system.md §6: the five statuses, used for threads, agents, checks
/// and pull requests alike. Each has a colour, a shape and a word; the word
/// is what a tooltip says, so status is never colour alone.
enum Status {
  needsYou('Needs you'),
  running('Running'),
  ready('Ready'),
  failed('Failed'),
  idle('Idle');

  const Status(this.word);

  /// What the status is called wherever it is spelled out.
  final String word;

  /// Its colour: amber needs you, neutral running (the pulse shows it), green
  /// ready, red failed, faint idle (drawn hollow, so never colour alone).
  Color color(Ember e) => switch (this) {
    Status.needsYou => e.warn,
    Status.running => e.fg,
    Status.ready => e.ok,
    Status.failed => e.err,
    Status.idle => e.faint,
  };
}

/// design-system.md §6: the three kinds of thread, told apart by icon and
/// tooltip, never by colour.
enum ThreadKind {
  agent('Agent', CupertinoIcons.chat_bubble),
  workItem('Work item', CupertinoIcons.square_stack_3d_up),
  planRun('Plan run', CupertinoIcons.arrow_branch);

  const ThreadKind(this.word, this.icon);

  final String word;
  final IconData icon;
}

/// The state of one CI check against the current head.
enum CheckState {
  passed('Passed', CupertinoIcons.checkmark_circle_fill),
  failed('Failed', CupertinoIcons.xmark_circle_fill),
  running('Running', CupertinoIcons.circle_fill),
  pending('Pending', CupertinoIcons.circle);

  const CheckState(this.word, this.icon);

  final String word;
  final IconData icon;

  /// Passed is `ok`, failed `err`, running `fg` and pending `border`: a
  /// pending mark is decoration until it runs.
  Color color(Ember e) => switch (this) {
    CheckState.passed => e.ok,
    CheckState.failed => e.err,
    CheckState.running => e.fg,
    CheckState.pending => e.border,
  };
}

/// One CI check, by the name the forge gives it.
@immutable
class Check {
  const Check(this.name, this.state);

  final String name;
  final CheckState state;
}

/// The icons the Studio draws. One set (Cupertino's, bundled with the app
/// by the `cupertino_icons` package), reached through the kit so a screen
/// never imports Cupertino itself.
typedef Glyphs = CupertinoIcons;
