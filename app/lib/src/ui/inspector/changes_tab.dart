import '../../model/changes_model.dart';
import '../kit/kit.dart';

/// The Changes tab (f4-inspector.md decision 5): the gate, the files with
/// their counts, and the selected file's diff.
///
/// It reads its model and draws it; it never fetches. The footer that ships
/// the work arrives with Task 5.
class ChangesTab extends StatelessWidget {
  const ChangesTab({
    super.key,
    required this.model,
    this.state,
    this.onComment,
  });

  final ChangesModel model;

  /// The data state to draw instead of the change set (§7). Null is the
  /// ready state, from the model.
  final StateKind? state;

  /// Leaves a comment on a line, when the tab can. Null draws the diff
  /// without a comment control.
  final void Function(String path, int line, String side)? onComment;

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
      name: 'Comments on a line',
      note: 'Steering the agent from a diff arrives in a later task',
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
        return ListView(
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

  /// The selected file's diff, and the mark that says it has been read.
  Widget _diff(BuildContext context) {
    final e = Ember.of(context);
    final path = model.activeFile;
    if (path == null) return const SizedBox.shrink();
    final viewed = model.isViewed(path);
    final comment = onComment;
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
          onComment: comment == null
              ? null
              : (line) => comment(path, line.number ?? 0, line.side!.wire),
        ),
        const SizedBox(height: Space.s8),
        Text(
          'Comment on a line to steer the agent. Its reply and new commits '
          'appear in the thread.',
          style: e.small.tint(e.muted),
        ),
      ],
    );
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
