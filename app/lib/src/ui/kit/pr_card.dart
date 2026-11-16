import 'package:flutter/widgets.dart';

import '../tokens.dart';
import 'box.dart';
import 'btn.dart';
import 'check_strip.dart';
import 'dot.dart';
import 'icon_btn.dart';
import 'menu.dart';
import 'pill.dart';
import 'press.dart';
import 'tip.dart';
import 'vocabulary.dart';

/// Where a pull request stands, as its card shows it (F5 decision 11).
enum PrState {
  /// Every check passed; it waits for review.
  ready,

  /// A check failed and nobody is fixing it yet.
  failing,

  /// Checks are still running.
  inProgress,

  /// A worker took the failure and is on it.
  fixing,

  /// The checks cannot be read at all, and the card says why. It is its own
  /// state, never shown as passing (decision 15).
  unavailable,
}

/// The facts a [PrCard] shows: one pull request against its current head.
@immutable
class PullRequestFacts {
  const PullRequestFacts({
    required this.number,
    required this.title,
    required this.repo,
    required this.branch,
    this.base = 'main',
    required this.adds,
    required this.dels,
    required this.checks,
    required this.reviews,
    required this.age,
    required this.state,
    this.draft = false,
    this.failureTail,
    required this.checkSummary,
    this.sending = false,
    this.dispatchError,
    required this.sourceKind,
    required this.sourceTitle,
  });

  final int number;
  final String title;
  final String repo;
  final String branch;
  final String base;
  final int adds;
  final int dels;

  /// In the forge's order.
  final List<Check> checks;

  /// The reviews, in words: "2 reviews passed", "no reviewers configured".
  final String reviews;

  /// Since the last push, short and relative: "26m".
  final String age;
  final PrState state;

  /// Not yet marked ready on the forge.
  final bool draft;

  /// The last lines of the failing check's log.
  final String? failureTail;

  /// The checks in one line, in the words the model built: "9 of 9 checks
  /// passed", "1 failing: unit", "2 of 5 passed, the rest running", "Checks
  /// unavailable: …". The card renders it and never re-derives it, so the one
  /// sentence has one spelling (F5 decision 11).
  final String checkSummary;

  /// A dispatch for this pull request is in flight: the primary action is
  /// acknowledged at once and shows as pending (§7), never as done work.
  final bool sending;

  /// Why the last dispatch was refused, in the bridge's words.
  final String? dispatchError;

  /// The thread this pull request came from.
  final ThreadKind sourceKind;
  final String sourceTitle;
}

/// What a [PrCard]'s buttons do. A card draws only the buttons its state
/// offers, and a button whose callback is null is drawn disabled.
@immutable
class PrActions {
  const PrActions({
    this.review,
    this.merge,
    this.openOnForge,
    this.fix,
    this.fixChoices = const [],
    this.fullLog,
    this.watch,
    this.stop,
    this.openSource,
  });

  final VoidCallback? review;
  final VoidCallback? merge;
  final VoidCallback? openOnForge;

  /// Fix with a worker: one click, no form.
  final VoidCallback? fix;

  /// The few variations behind the caret beside Fix with a worker.
  final List<MenuChoice> fixChoices;
  final VoidCallback? fullLog;

  /// While a worker fixes it.
  final VoidCallback? watch;
  final VoidCallback? stop;

  /// Open the thread this came from.
  final VoidCallback? openSource;
}

/// A pull request with its CI (design-system.md §5): the only way a pull
/// request is shown in a list. The pull request, not the thread, is the
/// unit of review.
///
/// A failing one carries the failure and one primary button, Fix with a
/// worker; once a worker has it, the card says so with Watch and Stop.
class PrCard extends StatelessWidget {
  const PrCard({
    super.key,
    required this.pull,
    this.actions = const PrActions(),
  });

  final PullRequestFacts pull;
  final PrActions actions;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final failing = pull.state == PrState.failing;

    return Box(
      tone: failing ? BoxTone.failing : BoxTone.plain,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          _title(e),
          const SizedBox(height: Space.s4),
          _branch(e),
          const SizedBox(height: Space.s12),
          _checks(e),
          if (pull.failureTail != null && pull.state != PrState.ready) ...[
            const SizedBox(height: Space.s12),
            Container(
              width: double.infinity,
              padding: const EdgeInsets.symmetric(
                horizontal: Space.s12,
                vertical: Space.s8,
              ),
              decoration: BoxDecoration(
                color: e.codeBg,
                borderRadius: BorderRadius.circular(Radii.control),
              ),
              child: Text(pull.failureTail!, style: e.mono.tint(e.muted)),
            ),
          ],
          const SizedBox(height: Space.s12),
          if (pull.state == PrState.fixing) _fixing(e) else _foot(context, e),
        ],
      ),
    );
  }

  Widget _title(Ember e) {
    final (icon, colour, word) = switch (pull.state) {
      PrState.ready => (CheckState.passed.icon, e.ok, 'Ready for review'),
      PrState.failing => (CheckState.failed.icon, e.err, 'Checks failing'),
      PrState.inProgress => (CheckState.running.icon, e.fg, 'In progress'),
      PrState.fixing => (
        CheckState.running.icon,
        e.fg,
        'A worker is fixing the failure',
      ),
      PrState.unavailable => (
        CheckState.pending.icon,
        e.muted,
        'Checks unavailable',
      ),
    };
    return Row(
      children: [
        Tip(
          message: word,
          child: Icon(icon, size: Sizes.iconRow, color: colour),
        ),
        const SizedBox(width: Space.s8),
        Text('#${pull.number}', style: e.mono.tint(e.muted)),
        const SizedBox(width: Space.s8),
        Expanded(
          child: Text(
            pull.title,
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            style: e.strong,
          ),
        ),
        if (pull.draft) ...[
          const SizedBox(width: Space.s8),
          const Pill('Draft', tooltip: 'Not yet marked ready on the forge'),
        ],
        const SizedBox(width: Space.s12),
        Tip(
          message: 'Since the last push',
          child: Text(pull.age, style: e.monoSmall.tint(e.muted)),
        ),
      ],
    );
  }

  Widget _branch(Ember e) => Row(
    children: [
      Flexible(
        child: Text(
          '${pull.repo} · ${pull.branch} → ${pull.base}',
          maxLines: 1,
          overflow: TextOverflow.ellipsis,
          style: e.monoSmall.tint(e.muted),
        ),
      ),
      const SizedBox(width: Space.s12),
      Text('+${pull.adds}', style: e.monoSmall.tint(e.ok)),
      const SizedBox(width: Space.s6),
      Text('−${pull.dels}', style: e.monoSmall.tint(e.err)),
    ],
  );

  Widget _checks(Ember e) {
    final failed = pull.checks.any((c) => c.state == CheckState.failed);
    final ink = switch (pull.state) {
      PrState.failing || PrState.fixing when failed => e.err,
      PrState.unavailable => e.muted,
      _ => e.fg,
    };
    return Wrap(
      spacing: Space.s8,
      runSpacing: Space.s4,
      crossAxisAlignment: WrapCrossAlignment.center,
      children: [
        if (pull.checks.isNotEmpty) CheckStrip(pull.checks),
        Text(pull.checkSummary, style: e.body.tint(ink)),
        Text('· ${pull.reviews}', style: e.body.tint(e.muted)),
      ],
    );
  }

  Widget _fixing(Ember e) => Row(
    crossAxisAlignment: CrossAxisAlignment.start,
    children: [
      const Padding(
        padding: EdgeInsets.only(top: Space.s4),
        child: Dot(Status.running, tooltip: 'Worker running'),
      ),
      const SizedBox(width: Space.s8),
      Expanded(
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text('A worker is on it', style: e.bodyStrong),
            const SizedBox(height: Space.s2),
            Text(
              'It reproduces the failure, fixes it, runs the gate and pushes '
              'to this pull request. It comes back to you only if it cannot.',
              style: e.small.tint(e.muted),
            ),
            const SizedBox(height: Space.s12),
            Wrap(
              spacing: Space.s8,
              runSpacing: Space.s8,
              children: [
                Btn('Watch', onTap: actions.watch),
                Btn('Stop', onTap: actions.stop),
              ],
            ),
          ],
        ),
      ),
    ],
  );

  Widget _foot(BuildContext context, Ember e) {
    final buttons = <Widget>[
      if (pull.state == PrState.ready) ...[
        Btn.primary('Review', onTap: actions.review),
        Btn('Merge', onTap: actions.merge),
      ] else if (pull.state == PrState.failing) ...[
        Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Tip(
              message: pull.sending
                  ? 'The dispatch is on its way to the bridge'
                  : 'A worker reproduces the failure, fixes it and pushes to '
                        'this pull request',
              child: Btn.primary(
                pull.sending ? 'Sending…' : 'Fix with a worker',
                icon: Glyphs.wrench,
                onTap: pull.sending ? null : actions.fix,
              ),
            ),
            if (actions.fixChoices.isNotEmpty) ...[
              const SizedBox(width: Space.s2),
              Menu(
                choices: actions.fixChoices,
                anchor: (context, toggle) => IconBtn(
                  Glyphs.chevron_down,
                  tooltip: 'Other ways to send this',
                  onTap: pull.sending ? null : toggle,
                ),
              ),
            ],
          ],
        ),
        Btn('Full log', onTap: actions.fullLog),
      ],
      Btn(
        'Open on forge',
        icon: Glyphs.arrow_up_right,
        onTap: actions.openOnForge,
      ),
    ];

    final source = Tip(
      message: 'Open the ${pull.sourceKind.word.toLowerCase()} this came from',
      child: Press(
        onTap: actions.openSource,
        builder: (context, state) {
          final ink = state.hovered ? e.fg : e.muted;
          return Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              Icon(pull.sourceKind.icon, size: Sizes.iconInline, color: ink),
              const SizedBox(width: Space.s6),
              Flexible(
                child: Text(
                  pull.sourceTitle,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: e.small.tint(ink),
                ),
              ),
            ],
          );
        },
      ),
    );

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        // A dispatch the bridge refused keeps the card as it was and says why;
        // the button above is the retry (§7).
        if (pull.dispatchError != null) ...[
          Text(
            pull.dispatchError!,
            style: e.small.tint(e.err),
            maxLines: 2,
            overflow: TextOverflow.ellipsis,
          ),
          const SizedBox(height: Space.s8),
        ],
        LayoutBuilder(
          builder: (context, box) {
            final row = Wrap(
              spacing: Space.s8,
              runSpacing: Space.s8,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: buttons,
            );
            if (box.maxWidth < Layout.reading) {
              return Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  row,
                  const SizedBox(height: Space.s12),
                  source,
                ],
              );
            }
            return Row(
              children: [
                row,
                const SizedBox(width: Space.s16),
                Expanded(
                  child: Align(alignment: Alignment.centerRight, child: source),
                ),
              ],
            );
          },
        ),
      ],
    );
  }
}
