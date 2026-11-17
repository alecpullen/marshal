import 'dart:async';

import 'package:flutter/services.dart';

import '../../model/changes_model.dart';
import '../kit/kit.dart';
import 'ship_sheet.dart';

/// The Changes tab (f4-inspector.md decision 5): the gate, the files with
/// their counts, the selected file's diff with its comment threads, and the
/// footer that ships the work.
///
/// It reads its model and draws it; it never fetches. A comment is posted
/// through the model, which is the one thing here the agent hears.
class ChangesTab extends StatelessWidget {
  const ChangesTab({super.key, required this.model, this.state, this.shipped});

  final ChangesModel model;

  /// The pull request this change shipped as, once it has (F5 decision 14):
  /// one line naming it and what its checks say. Null before a ship, when
  /// there is nothing to say.
  final String? shipped;

  /// The data state to draw instead of the change set (§7). Null is the
  /// ready state, from the model.
  final StateKind? state;

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: model,
      builder: (context, _) {
        final state = this.state;
        return state == null ? _body(context) : _stateOnly(state);
      },
    );
  }

  /// The five states every screen that shows data has (§7).
  Widget _stateOnly(StateKind state) => switch (state) {
    StateKind.loading => const StateMessage.loading(lines: 6),
    StateKind.empty => StateMessage.empty(
      message: 'The agent has not changed a file yet.',
      action: StateAction('Run the gate', onTap: model.runGate),
    ),
    StateKind.stale => const StateMessage.stale(
      age: '4m',
      detail: 'The changes may be behind',
    ),
    StateKind.error => StateMessage.error(
      message: 'The changes could not be read.',
      retry: StateAction('Try again', onTap: model.refresh),
    ),
    StateKind.unavailable => StateMessage.unavailable(
      host: 'The bridge',
      lastSeen: '4m',
      action: StateAction('Try again', onTap: model.refresh),
    ),
    StateKind.planned => const StateMessage.planned(
      name: 'A side-by-side diff',
      note: 'This phase reads a unified diff only',
    ),
  };

  Widget _body(BuildContext context) {
    switch (model.status) {
      case ChangesStatus.loading:
        return const StateMessage.loading(lines: 6);
      case ChangesStatus.failed:
        return StateMessage.error(
          message: model.failureMessage ?? 'The changes could not be read.',
          retry: StateAction('Try again', onTap: model.refresh),
        );
      case ChangesStatus.ready:
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Expanded(
              child: ListView(
                padding: const EdgeInsets.all(Space.s16),
                children: [
                  _gate(context),
                  const SizedBox(height: Space.s16),
                  if (model.files.isEmpty)
                    StateMessage.empty(
                      message: 'The agent has not changed a file yet.',
                      action: StateAction('Run the gate', onTap: model.runGate),
                    )
                  else ...[
                    _files(context),
                    const SizedBox(height: Space.s12),
                    _diff(context),
                  ],
                ],
              ),
            ),
            const Hairline(),
            _footer(context),
          ],
        );
    }
  }

  /// The gate first: what it found, when it ran, and the way to run it.
  Widget _gate(BuildContext context) {
    final e = Ember.of(context);
    final age = model.gateAge;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Row(
          children: [
            const Expanded(child: Label('Gate')),
            if (age.isNotEmpty) ...[
              const SizedBox(width: Space.s8),
              Text('ran $age', style: e.small.tint(e.muted)),
            ],
          ],
        ),
        const SizedBox(height: Space.s6),
        GateList(gateChecks(model.gateChecks)),
        const SizedBox(height: Space.s8),
        Row(
          children: [
            Btn(
              model.runningGate ? 'Running the gate' : 'Run gate',
              onTap: model.runningGate ? null : model.runGate,
            ),
          ],
        ),
      ],
    );
  }

  /// The files, whole or grouped by the step that changed them.
  Widget _files(BuildContext context) {
    final e = Ember.of(context);
    final groups = model.groups;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Row(
          children: [
            Expanded(child: Label('Files', count: model.files.length)),
            Menu(
              choices: [
                MenuChoice(
                  'All changes',
                  'One list of every file',
                  onSelect: () => model.setByStep(false),
                ),
                MenuChoice(
                  'By step',
                  'Grouped by the step that changed them',
                  onSelect: () => model.setByStep(true),
                ),
              ],
              anchor: (context, toggle) => Btn(
                model.byStep ? 'By step' : 'All changes',
                icon: Glyphs.chevron_down,
                onTap: toggle,
              ),
            ),
          ],
        ),
        const SizedBox(height: Space.s6),
        for (final group in groups) ...[
          if (group.label.isNotEmpty) ...[
            const SizedBox(height: Space.s8),
            Text(group.label, style: e.small.tint(e.muted), maxLines: 2),
            const SizedBox(height: Space.s4),
          ],
          Rows([
            for (final path in group.files)
              RowItem(
                name: path,
                description: '',
                value: _countsFor(path),
                mono: true,
                selected: path == model.activeFile,
                onTap: () => model.select(path),
              ),
          ]),
        ],
      ],
    );
  }

  /// `✓ +3 −11`: the mark a person put on a file they have read, then the
  /// counts (`0` is left out, so a new file reads `+12`).
  String _countsFor(String path) {
    final mark = model.isViewed(path) ? '✓ ' : '';
    for (final file in model.files) {
      if (file.path != path) continue;
      final added = file.added == 0 ? '' : '+${file.added}';
      final removed = file.removed == 0 ? '' : '−${file.removed}';
      final counts = [
        if (added.isNotEmpty) added,
        if (removed.isNotEmpty) removed,
      ].join(' ');
      return '$mark${counts.isEmpty ? '0' : counts}';
    }
    return mark;
  }

  /// The selected file's diff, its comment threads, and the mark that says it
  /// has been read.
  Widget _diff(BuildContext context) {
    final e = Ember.of(context);
    final path = model.activeFile;
    if (path == null) return const SizedBox.shrink();
    final viewed = model.isViewed(path);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Row(
          children: [
            Expanded(
              child: Text(
                path,
                style: e.monoSmall.tint(e.muted),
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
              ),
            ),
            const SizedBox(width: Space.s8),
            Btn(
              viewed ? 'Viewed' : 'Mark viewed',
              icon: viewed ? Glyphs.checkmark : null,
              onTap: () => model.toggleViewed(path),
            ),
          ],
        ),
        const SizedBox(height: Space.s8),
        DiffView(
          diff: model.diffOf(path),
          onComment: (line) => unawaited(_commentOn(context, path, line)),
        ),
        const SizedBox(height: Space.s8),
        Text(
          'Comment on a line to steer the agent. Its reply and new commits '
          'appear in the thread.',
          style: e.small.tint(e.muted),
        ),
        _threads(context, path),
      ],
    );
  }

  /// The threads left on [path], each with its way to close.
  Widget _threads(BuildContext context, String path) {
    final e = Ember.of(context);
    final comments = model.commentsAt(path);
    if (comments.isEmpty) return const SizedBox.shrink();
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        const SizedBox(height: Space.s16),
        Label('Comments', count: comments.length),
        const SizedBox(height: Space.s6),
        for (final comment in comments) ...[
          Box(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                Row(
                  children: [
                    Expanded(
                      child: Text(
                        'line ${comment.line} · ${comment.side}',
                        style: e.monoSmall.tint(e.muted),
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                      ),
                    ),
                    if (comment.resolved)
                      Text('Resolved', style: e.small.tint(e.ok))
                    else
                      Btn(
                        'Resolve',
                        onTap: () => unawaited(model.resolve(comment.id)),
                      ),
                  ],
                ),
                if (comment.quote.isNotEmpty) ...[
                  const SizedBox(height: Space.s6),
                  Text(
                    comment.quote,
                    style: e.mono.tint(e.muted),
                    maxLines: 2,
                    overflow: TextOverflow.ellipsis,
                  ),
                ],
                const SizedBox(height: Space.s6),
                Text(comment.body, style: e.body),
                const SizedBox(height: Space.s6),
                Text(
                  comment.sent
                      ? 'Sent to the agent as steering'
                      : 'Not sent to the agent yet',
                  style: e.caption.tint(e.muted),
                ),
              ],
            ),
          ),
          const SizedBox(height: Space.s8),
        ],
      ],
    );
  }

  /// What the gate said, in one line, then the actions that end the work.
  Widget _footer(BuildContext context) {
    final e = Ember.of(context);
    return Padding(
      padding: const EdgeInsets.all(Space.s12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          // Once the change has shipped, the footer says what became of it:
          // the pull request and what its checks say (F5 decision 14).
          if (shipped != null) ...[
            Text(shipped!, style: e.small, maxLines: 2),
            const SizedBox(height: Space.s4),
          ],
          Text(_gateLine, style: e.small.tint(e.muted), maxLines: 2),
          const SizedBox(height: Space.s8),
          Wrap(
            spacing: Space.s8,
            runSpacing: Space.s8,
            children: [
              Btn.primary(
                'Review & ship',
                onTap: () => unawaited(_ship(context)),
              ),
              Btn(
                'Export patch',
                onTap: () => unawaited(_exportPatch(context)),
              ),
              Btn('Discard', onTap: () => unawaited(_discard(context))),
            ],
          ),
        ],
      ),
    );
  }

  String get _gateLine {
    final gate = model.gate;
    if (gate == null) return 'The gate has not run';
    if (gate.skipped) return 'Gate skipped: no verify command';
    if (gate.ok) return 'Gate passed';
    final command = gate.failedCommand ?? '';
    return command.isEmpty ? 'Gate failed' : 'Gate failed: $command';
  }

  /// Leaves a comment on [line]: a sheet for the words, then the model, which
  /// posts it as steering.
  Future<void> _commentOn(
    BuildContext context,
    String path,
    DiffLine line,
  ) async {
    final e = Ember.of(context);
    final controller = TextEditingController();
    final quote = '${line.marker}${line.text}';
    final body = await Sheet.show<String>(
      context,
      Sheet(
        title: 'Comment on line ${line.number ?? 0}',
        actions: [
          Btn.primary(
            'Comment',
            onTap: () => Navigator.maybePop(context, controller.text.trim()),
          ),
        ],
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            OutputBlock(text: quote),
            const SizedBox(height: Space.s12),
            Field(
              label: 'What should the agent change?',
              controller: controller,
              hint: 'Trim this list to what the tests load',
            ),
            const SizedBox(height: Space.s8),
            Text(
              'The agent reads it as steering in this turn.',
              style: e.small.tint(e.muted),
            ),
          ],
        ),
      ),
    );
    final text = body?.trim() ?? '';
    controller.dispose();
    if (text.isEmpty) return;
    await model.comment(
      path,
      line.number ?? 0,
      text,
      side: (line.side ?? DiffSide.after).wire,
      quote: quote,
    );
  }

  /// The ship flow, in one sheet (decision 6).
  Future<void> _ship(BuildContext context) async {
    await showShipSheet(context, model);
    await model.refresh();
  }

  /// The change set as a patch: shown, and copied, because the app's lanes
  /// have no file to write it to.
  Future<void> _exportPatch(BuildContext context) async {
    final patch = await model.exportPatch();
    if (!context.mounted) return;
    await Sheet.show<void>(
      context,
      Sheet(
        title: 'The patch',
        child: patch.isEmpty
            ? StateMessage.empty(
                message: 'The agent has not changed a file to export.',
                action: StateAction(
                  'Close',
                  onTap: () => Navigator.maybePop(context),
                ),
              )
            : Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  OutputBlock(text: patch),
                  const SizedBox(height: Space.s12),
                  Row(
                    children: [
                      Btn(
                        'Copy patch',
                        icon: Glyphs.doc_on_doc,
                        onTap: () => unawaited(
                          Clipboard.setData(ClipboardData(text: patch)),
                        ),
                      ),
                    ],
                  ),
                ],
              ),
      ),
    );
  }

  /// Discard, which confirms and names what is lost before it acts.
  Future<void> _discard(BuildContext context) async {
    final e = Ember.of(context);
    final count = model.files.length;
    final files = count == 1 ? '1 changed file' : '$count changed files';
    final confirmed = await Sheet.show<bool>(
      context,
      Sheet(
        title: 'Discard this work?',
        actions: [
          Btn.primary(
            'Keep the work',
            onTap: () => Navigator.maybePop(context, false),
          ),
          Btn(
            'Discard the work',
            onTap: () => Navigator.maybePop(context, true),
          ),
        ],
        child: Text(
          'This throws away the agent’s $files and every change it has made '
          'to them. It cannot be undone.',
          style: e.body,
        ),
      ),
    );
    if (confirmed != true) return;
    await model.discard();
  }
}

/// The model's gate checks in the kit's vocabulary.
List<GateCheck> gateChecks(List<GateCheckView> checks) => [
  for (final check in checks)
    GateCheck(check.name, _stateOf(check.state), detail: check.detail),
];

CheckState _stateOf(GateState state) => switch (state) {
  GateState.notRun || GateState.skipped => CheckState.pending,
  GateState.passed => CheckState.passed,
  GateState.failed => CheckState.failed,
};
