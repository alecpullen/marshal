import 'dart:async';

import '../../model/age.dart';
import '../../model/project_model.dart';
import '../../model/section_model.dart' show SaveState;
import '../kit/kit.dart';

/// A project's defaults (F6 decision 12): what new agents of the project run
/// as, and the factory settings a handoff inherits, with the inheritance shown
/// on the row that follows it.
///
/// Every edit is local until Save: the page sends one conditional write naming
/// the ETag it read, so a change another surface made in between is never
/// overwritten. A stale refusal keeps the edit, says so, and offers a reload.
/// The screen draws what the model holds and never fetches (§5).
class DefaultsTab extends StatelessWidget {
  const DefaultsTab({super.key, required this.model, this.state, this.now});

  final ProjectModel model;

  /// The tab's data state, when it is not ready (§7).
  final StateKind? state;

  /// What a relative age is measured against.
  final DateTime? now;

  /// The sentinel a sheet returns for "put this field back".
  static const reset = Object();

  @override
  Widget build(BuildContext context) {
    final state = this.state;
    if (state != null && state != StateKind.stale) {
      return _stateMessage(state);
    }
    final draft = model.draft;
    if (draft == null) {
      // A stale tab says so even before the document it is stale against has
      // arrived: the state is what it knows.
      if (state == StateKind.stale) {
        return StateMessage.stale(age: relativeAge(model.lastRead, _now));
      }
      if (model.loading) return const StateMessage.loading(lines: 5);
      final error = model.error;
      if (error != null) {
        return StateMessage.error(
          message: error,
          retry: StateAction('Read the settings again', onTap: _load),
        );
      }
      return StateMessage.empty(
        message: 'The settings have not been read yet.',
        action: StateAction('Read the settings', onTap: _load),
      );
    }
    final e = Ember.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        if (state == StateKind.stale || model.stale) ...[
          StateMessage.stale(age: relativeAge(model.lastRead, _now)),
          const SizedBox(height: Space.s8),
          Align(
            alignment: Alignment.centerLeft,
            child: Btn(
              'Reload the stored settings',
              icon: Glyphs.arrow_clockwise,
              onTap: () => unawaited(model.reloadAfterStale()),
            ),
          ),
          const SizedBox(height: Space.s16),
        ],
        if (model.saveState == SaveState.failed && model.saveError != null) ...[
          Text(model.saveError!, style: e.small.tint(e.err)),
          const SizedBox(height: Space.s8),
        ],
        Label('Agent defaults'),
        const SizedBox(height: Space.s8),
        Rows([
          RowItem(
            name: 'Model profile',
            description: 'The profile new agents of this project run as',
            value: _words(draft.agentProfile),
            onTap: () => _text(
              context,
              title: 'Model profile',
              label: 'Profile',
              value: draft.agentProfile,
              hint: 'A profile from your model settings',
              onSaved: model.setAgentProfile,
              onReset: () => model.reset(ProjectField.agentProfile),
            ),
          ),
          RowItem(
            name: 'Interaction mode',
            description: 'What a new session may do without asking',
            value: _words(draft.mode),
            onTap: () => _choose(
              context,
              title: 'Interaction mode',
              current: draft.mode,
              choices: [
                for (final mode in projectModes)
                  (mode == 'ask' ? 'Ask first' : 'Act', mode),
              ],
              onChosen: model.setMode,
              onReset: () => model.reset(ProjectField.mode),
            ),
          ),
          RowItem(
            name: 'Auto',
            description: 'Whether a new session starts approving its own work',
            value: _autoWords(draft.auto),
            onTap: () => _choose(
              context,
              title: 'Auto',
              current: _autoWords(draft.auto),
              choices: const [
                ('Inherited', 'inherit'),
                ('On', 'on'),
                ('Off', 'off'),
              ],
              onChosen: (value) => model.setAuto(switch (value) {
                'on' => AutoChoice.on,
                'off' => AutoChoice.off,
                _ => AutoChoice.inherit,
              }),
              onReset: () => model.reset(ProjectField.auto),
            ),
          ),
          RowItem(
            name: 'Isolation',
            description: 'Where a new session runs',
            value: _isolationWords(draft.isolation),
            onTap: () => _choose(
              context,
              title: 'Isolation',
              current: _isolationWords(draft.isolation),
              choices: const [
                ('Inherited', 'inherit'),
                ('Isolated', 'isolated'),
                ('Shared', 'shared'),
              ],
              onChosen: (value) => model.setIsolation(switch (value) {
                'isolated' => IsolationChoice.isolated,
                'shared' => IsolationChoice.shared,
                _ => IsolationChoice.inherit,
              }),
              onReset: () => model.reset(ProjectField.isolation),
            ),
          ),
          RowItem(
            name: 'Ship target',
            description: 'Where a finished change lands',
            value: _words(draft.shipTarget),
            onTap: () => _choose(
              context,
              title: 'Ship target',
              current: draft.shipTarget,
              choices: [
                for (final target in projectShipTargets) (target, target),
              ],
              onChosen: model.setShipTarget,
              onReset: () => model.reset(ProjectField.shipTarget),
            ),
          ),
        ]),
        const SizedBox(height: Space.s20),
        Label('Factory defaults'),
        const SizedBox(height: Space.s8),
        Rows([
          _roleRow(
            context,
            name: 'Implementer profile',
            role: 'implementer',
            own: draft.implementerProfile,
            effective: draft.effectiveImplementer,
            agentDefault: draft.agentProfile,
          ),
          _roleRow(
            context,
            name: 'Reviewer profile',
            role: 'reviewer',
            own: draft.reviewerProfile,
            effective: draft.effectiveReviewer,
            agentDefault: draft.agentProfile,
          ),
          RowItem(
            name: 'Target branch',
            description: 'What a work item\'s pull request lands on',
            value: _words(draft.targetBranch),
            onTap: () => _text(
              context,
              title: 'Target branch',
              label: 'Branch',
              value: draft.targetBranch,
              hint: 'main',
              onSaved: model.setTargetBranch,
              onReset: () => model.reset(ProjectField.targetBranch),
            ),
          ),
          RowItem(
            name: 'Rounds',
            description: 'How many repair rounds an item may take',
            value: _words(draft.rounds),
            onTap: () => _text(
              context,
              title: 'Rounds',
              label: 'Repair rounds',
              value: draft.rounds,
              hint: '3',
              onSaved: model.setRounds,
              onReset: () => model.reset(ProjectField.rounds),
            ),
          ),
          RowItem(
            name: 'CI poll',
            description: 'How often the item reads its checks',
            value: draft.ciPoll.isEmpty ? 'Not set' : '${draft.ciPoll}s',
            onTap: () => _text(
              context,
              title: 'CI poll',
              label: 'Seconds between reads',
              value: draft.ciPoll,
              hint: '30',
              onSaved: model.setCIPoll,
              onReset: () => model.reset(ProjectField.ciPoll),
            ),
          ),
          RowItem(
            name: 'CI wait',
            description: 'How long it waits for the checks before stopping',
            value: draft.ciWait.isEmpty ? 'Not set' : '${draft.ciWait}s',
            onTap: () => _text(
              context,
              title: 'CI wait',
              label: 'Seconds to wait',
              value: draft.ciWait,
              hint: '900',
              onSaved: model.setCIWait,
              onReset: () => model.reset(ProjectField.ciWait),
            ),
          ),
          RowItem(
            name: 'Reviewer instructions',
            description: 'What the reviewer is told on every item',
            value: draft.reviewerInstructions.trim().isEmpty
                ? 'Not set'
                : 'Set',
            onTap: () => _text(
              context,
              title: 'Reviewer instructions',
              label: 'Instructions',
              value: draft.reviewerInstructions,
              hint: 'What the reviewer should look for',
              multiline: true,
              onSaved: model.setReviewerInstructions,
              onReset: () => model.reset(ProjectField.reviewerInstructions),
            ),
          ),
        ]),
        const SizedBox(height: Space.s20),
        Label('Planning'),
        const SizedBox(height: Space.s8),
        Rows([
          RowItem(
            name: 'Planner delegation',
            description:
                'Whether one MCP client may answer this project\'s planner',
            value: draft.plannerDelegation ? 'On' : 'Off',
            onTap: () {
              model.setPlannerDelegation(!draft.plannerDelegation);
            },
          ),
          if (draft.plannerDelegation)
            RowItem(
              name: 'Designated client',
              description: 'The one client that may answer while delegated',
              value: _words(draft.designatedClient),
              onTap: () => _text(
                context,
                title: 'Designated client',
                label: 'Client id',
                value: draft.designatedClient,
                hint: 'The MCP client that may answer',
                onSaved: model.setDesignatedClient,
              ),
            ),
        ]),
      ],
    );
  }

  DateTime get _now => now ?? DateTime.now();

  /// One factory role, with what it resolves to and where that came from.
  RowItem _roleRow(
    BuildContext context, {
    required String name,
    required String role,
    required String own,
    required String effective,
    required String agentDefault,
  }) {
    final follows = own.trim().isEmpty;
    return RowItem(
      name: name,
      description: follows
          ? 'Following the agent default'
                '${agentDefault.isEmpty ? ' (not set)' : ': $agentDefault'}'
          : 'Its own choice',
      value: effective.isEmpty ? 'Not set' : effective,
      onTap: () => _text(
        context,
        title: name,
        label: 'Profile',
        value: own,
        hint: 'Leave empty to follow the agent default',
        onSaved: (value) => model.setRoleProfile(role, value),
        onReset: () => model.resetRole(role),
      ),
    );
  }

  /// A text edit in the kit's sheet, with Reset as its second action.
  Future<void> _text(
    BuildContext context, {
    required String title,
    required String label,
    required String value,
    required String hint,
    required ValueChanged<String> onSaved,
    VoidCallback? onReset,
    bool multiline = false,
  }) async {
    final controller = TextEditingController(text: value);
    final answer = await Sheet.show<Object>(
      context,
      Sheet(
        title: title,
        actions: [
          Btn.primary(
            'Save',
            onTap: () => Navigator.maybePop(context, controller.text.trim()),
          ),
          if (onReset != null)
            Btn('Reset', onTap: () => Navigator.maybePop(context, reset)),
        ],
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            if (multiline)
              Field.multiline(label: label, controller: controller, hint: hint)
            else
              Field(label: label, controller: controller, hint: hint),
            const SizedBox(height: Space.s8),
            Text(
              'Saving writes the project\'s settings and nothing else.',
              style: Ember.of(context).small.tint(Ember.of(context).muted),
            ),
          ],
        ),
      ),
    );
    controller.dispose();
    if (answer == reset) {
      onReset?.call();
    } else if (answer is String) {
      onSaved(answer);
    }
  }

  /// A choice in the kit's sheet: one row per option, the current one marked.
  Future<void> _choose(
    BuildContext context, {
    required String title,
    required String current,
    required List<(String, String)> choices,
    required ValueChanged<String> onChosen,
    VoidCallback? onReset,
  }) async {
    final answer = await Sheet.show<Object>(
      context,
      Sheet(
        title: title,
        actions: [
          Btn('Cancel', onTap: () => Navigator.maybePop(context)),
          if (onReset != null)
            Btn('Reset', onTap: () => Navigator.maybePop(context, reset)),
        ],
        child: Rows([
          for (final (label, value) in choices)
            RowItem(
              name: label,
              description: '',
              value: value == current ? 'Current' : null,
              onTap: () => Navigator.maybePop(context, value),
            ),
        ]),
      ),
    );
    if (answer == reset) {
      onReset?.call();
    } else if (answer is String) {
      onChosen(answer);
    }
  }

  void _load() => unawaited(model.load());

  static String _words(String value) =>
      value.trim().isEmpty ? 'Not set' : value;

  static String _autoWords(AutoChoice choice) => switch (choice) {
    AutoChoice.inherit => 'Inherited',
    AutoChoice.on => 'On',
    AutoChoice.off => 'Off',
  };

  static String _isolationWords(IsolationChoice choice) => switch (choice) {
    IsolationChoice.inherit => 'Inherited',
    IsolationChoice.isolated => 'Isolated',
    IsolationChoice.shared => 'Shared',
  };

  Widget _stateMessage(StateKind state) {
    final retry = StateAction('Read the settings again', onTap: _load);
    return switch (state) {
      StateKind.loading => const StateMessage.loading(lines: 5),
      StateKind.empty => StateMessage.empty(
        message:
            'This project has no defaults yet: every field follows the '
            'app\'s own.',
        action: retry,
      ),
      StateKind.error => StateMessage.error(
        message: model.error ?? 'The settings could not be read.',
        retry: retry,
      ),
      StateKind.unauthorized => const StateMessage.unauthorized(),
      StateKind.unavailable => StateMessage.unavailable(
        host: model.name,
        lastSeen: relativeAge(model.lastRead, _now),
        action: retry,
      ),
      StateKind.stale => StateMessage.stale(
        age: relativeAge(model.lastRead, _now),
      ),
      StateKind.planned => StateMessage.planned(
        name: 'Project defaults',
        note: 'Not built yet',
      ),
    };
  }
}
