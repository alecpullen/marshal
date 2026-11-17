import 'dart:async';

import '../../model/pulls_model.dart';
import '../kit/kit.dart';
import 'merge_control.dart';
import 'pull_facts.dart';

/// The fix control behind a failing pull request's card (F5 decision 12): one
/// primary action that posts the dispatch with no further input, and a caret
/// with exactly three ways to say more before it starts.
///
/// It is the one place a card's actions are built, so the place, the review
/// head and the Inbox all dispatch the same way. The card itself draws the
/// button and the caret (the kit's `PrCard`); this file decides what they do.
PrActions pullCardActions(
  BuildContext context, {
  required PullView pull,
  required PullsModel model,
  VoidCallback? review,
  VoidCallback? merge,
  VoidCallback? openOnForge,
  VoidCallback? fullLog,
  VoidCallback? watch,
  VoidCallback? openSource,
}) => PrActions(
  review: review,
  // A merge always confirms first, naming the pull request and its base
  // branch, because it cannot be undone (§9). A caller that passes its own
  // still confirms; this is only the default.
  //
  // A factory item's pull request is the exception: the only route that merges
  // it needs the reviewed node id a pull record does not carry, so Merge is
  // drawn disabled with the reason rather than offered and refused (F5
  // review; F6 brings the work item that holds it).
  merge: pull.fromFactoryItem
      ? null
      : merge ??
            () => unawaited(confirmMerge(context, pull: pull, model: model)),
  mergeNote: pull.fromFactoryItem ? workItemMergeNote : null,
  openOnForge: openOnForge,
  fullLog: fullLog,
  watch: watch,
  openSource: openSource,
  // One click: the mode is a fix and the check is the one that is failing, so
  // nothing else is asked (decision 12).
  fix: pull.sending ? null : () => unawaited(model.fix(pull)),
  fixChoices: pull.sending
      ? const []
      : [
          MenuChoice(
            'Investigate only',
            'Report the cause, change nothing',
            onSelect: () => unawaited(model.fix(pull, mode: 'investigate')),
          ),
          MenuChoice(
            'Fix with a note',
            'Tell the worker what you suspect',
            onSelect: () => unawaited(_fixWithNote(context, pull, model)),
          ),
          MenuChoice(
            'Always fix failures automatically in ${_projectName(pull)}',
            'For every pull request in this project',
            onSelect: () => unawaited(_autoFix(context, pull, model)),
          ),
        ],
  stop: () => unawaited(model.stopFix(pull)),
);

/// A note reaches the worker as part of the recipe's own input, so it is the
/// same dispatch with one more sentence from the person.
Future<void> _fixWithNote(
  BuildContext context,
  PullView pull,
  PullsModel model,
) async {
  final e = Ember.of(context);
  final controller = TextEditingController();
  final note = await Sheet.show<String>(
    context,
    Sheet(
      title: 'Fix with a note',
      actions: [
        Btn.primary(
          'Send it',
          onTap: () => Navigator.maybePop(context, controller.text.trim()),
        ),
      ],
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Field(
            label: 'What should the worker know?',
            controller: controller,
            hint: 'The clock is pinned in the fixture',
          ),
          const SizedBox(height: Space.s8),
          Text(
            'It reaches the worker with the failing check.',
            style: e.small.tint(e.muted),
          ),
        ],
      ),
    ),
  );
  final text = note?.trim() ?? '';
  controller.dispose();
  if (text.isEmpty) return;
  await model.fix(pull, note: text);
}

/// The setting is the project's, and the sheet says what happened: a bridge
/// that changes a pull request on its own is a surprise unless the project
/// asked for it (decision 7).
Future<void> _autoFix(
  BuildContext context,
  PullView pull,
  PullsModel model,
) async {
  final e = Ember.of(context);
  final project = _projectName(pull);
  try {
    await model.setAutoFix(pull.projectRoot, true);
  } catch (error) {
    if (!context.mounted) return;
    await Sheet.show<void>(
      context,
      Sheet(
        title: 'The setting did not change',
        child: Text('$error', style: e.body.tint(e.err)),
      ),
    );
    return;
  }
  if (!context.mounted) return;
  await Sheet.show<void>(
    context,
    Sheet(
      title: 'Automatic fixing is on',
      child: Text(
        'A failing check on $project’s pull requests is sent to a worker '
        'without asking. Factory items are unaffected: they already repair '
        'inside their own allowance.',
        style: e.body,
      ),
    ),
  );
}

/// What a pull request's project is called: its directory's name.
String _projectName(PullView pull) {
  final root = pull.projectRoot;
  final parts = [
    for (final part in root.split('/'))
      if (part.isNotEmpty) part,
  ];
  return parts.isEmpty ? root : parts.last;
}
