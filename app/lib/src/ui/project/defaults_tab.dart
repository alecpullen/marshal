import 'dart:async';

import '../../model/age.dart';
import '../../model/expected_checks.dart';
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
  const DefaultsTab({
    super.key,
    required this.model,
    this.state,
    this.now,
    this.observedChecks = const <ObservedCheck>[],
  });

  final ProjectModel model;

  /// The tab's data state, when it is not ready (§7).
  final StateKind? state;

  /// What a relative age is measured against.
  final DateTime? now;

  /// The checks this project's recorded work has observed, from the factory
  /// store: evidence the checks editor may offer when asked, never a
  /// requirement and never filled in for the person.
  final List<ObservedCheck> observedChecks;

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
            name: 'Expected checks',
            description:
                'The checks an item must pass; none stated follows the app\'s',
            value: _checksWords(draft),
            onTap: () => _checks(context),
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

  /// The expected-checks editor: one draft, seen as rows or as raw lines.
  void _checks(BuildContext context) => unawaited(
    Sheet.show<Object>(
      context,
      Sheet(
        title: 'Expected checks',
        actions: [Btn('Done', onTap: () => Navigator.maybePop(context))],
        child: _ChecksSheet(
          model: model,
          observedChecks: observedChecks,
          now: _now,
        ),
      ),
    ),
  );

  /// What the Expected checks row says the draft holds: whether it follows the
  /// app, cannot be saved as typed, or states this many checks.
  static String _checksWords(ProjectDraft draft) {
    if (draft.expectedChecksChoice == ExpectedChecksChoice.inherit) {
      return 'Following the defaults';
    }
    if (parseExpectedChecks(draft.expectedChecksText).error != null) {
      return 'Not valid';
    }
    final count = draft.expectedChecks.length;
    if (count == 0) return 'None stated';
    return '$count ${count == 1 ? 'check' : 'checks'}';
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

/// The expected-checks editor: the same draft seen as one row per check or as
/// the raw lines it stores, with what this project's recorded work observed
/// offered last and only when the person asks.
///
/// Every render reads [ProjectModel.draft], and every edit is one model call,
/// so switching from one mode to the other never drops a keystroke and raw
/// mode always shows the draft's own text. Rows mode can only show what that
/// text parses to: while it cannot be read as any check at all, rows mode
/// states the parser's reason and offers the way back to the text instead of
/// a list and an add form, which would rewrite the text from what parsed and
/// lose the rest. The sheet listens to the model itself because it lives in
/// an overlay route, outside the tab's rebuild.
///
/// The recorded checks are evidence, never the requirement: each is listed
/// with the round that saw it and how long ago, it is added only when chosen,
/// and a project with nothing recorded says so instead of looking for any.
class _ChecksSheet extends StatefulWidget {
  const _ChecksSheet({
    required this.model,
    required this.observedChecks,
    required this.now,
  });

  final ProjectModel model;

  /// The checks the project's recorded work observed, as the factory store
  /// reported them; empty when it observed none.
  final List<ObservedCheck> observedChecks;

  /// What a relative age is measured against.
  final DateTime now;

  @override
  State<_ChecksSheet> createState() => _ChecksSheetState();
}

class _ChecksSheetState extends State<_ChecksSheet> {
  /// Whether the editor shows the raw lines rather than one row per check. It
  /// is this sheet's view of the one draft, not a second copy of it.
  bool _raw = false;

  /// Whether the person asked to see what earlier work recorded.
  bool _showSuggestions = false;

  /// The add form's kind, the two fields behind it and what stops it adding.
  String _kind = ExpectedCheck.checkRunKind;
  final _name = TextEditingController();
  final _appID = TextEditingController();
  String? _addError;

  /// The raw lines the field edits: refilled from the draft whenever rows mode
  /// hands over, so what raw mode shows is what the draft holds.
  final _rawText = TextEditingController();

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_changed);
  }

  @override
  void dispose() {
    widget.model.removeListener(_changed);
    _name.dispose();
    _appID.dispose();
    _rawText.dispose();
    super.dispose();
  }

  void _changed() {
    if (mounted) setState(() {});
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final draft = widget.model.draft;
    if (draft == null) {
      return Text(
        'The settings have not been read yet, so no check can be edited here.',
        style: e.small.tint(e.muted),
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Align(
          alignment: Alignment.centerLeft,
          child: Menu(
            choices: [
              MenuChoice(
                'Rows',
                'One row per check',
                onSelect: () => _mode(raw: false),
              ),
              MenuChoice(
                'Raw text',
                'One check per line, as it is stored',
                onSelect: () => _mode(raw: true),
              ),
            ],
            anchor: (context, toggle) => Btn(
              _raw ? 'Raw text' : 'Rows',
              icon: Glyphs.chevron_down,
              onTap: toggle,
            ),
          ),
        ),
        const SizedBox(height: Space.s12),
        if (_raw) _rawMode(context, draft) else _rowsMode(context, draft),
        // Text that cannot be read offers no mutation: adding a check here
        // would rewrite it from the empty parse and lose what was typed.
        if (_raw || !_unreadable(draft)) ...[
          const SizedBox(height: Space.s20),
          ..._suggestions(context),
        ],
      ],
    );
  }

  /// Whether the draft states its own checks in text that cannot be read as
  /// any check. Rows mode has nothing to list for it and nothing safe to add
  /// to it.
  static bool _unreadable(ProjectDraft draft) =>
      draft.expectedChecksChoice == ExpectedChecksChoice.custom &&
      parseExpectedChecks(draft.expectedChecksText).error != null;

  /// Switches how the one draft is shown. Nothing is reset: rows mode reads
  /// the draft's checks and raw mode reads its text, and both are the draft.
  void _mode({required bool raw}) {
    setState(() {
      _raw = raw;
      if (raw) _rawText.text = widget.model.draft?.expectedChecksText ?? '';
    });
  }

  /// The draft's checks as rows, then the form that adds one. Text that cannot
  /// be read as any check is stated in the parser's own words instead, with
  /// the one way back to the text that would fix it.
  Widget _rowsMode(BuildContext context, ProjectDraft draft) {
    final e = Ember.of(context);
    final unreadable = _unreadable(draft);
    if (unreadable) {
      return Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(
            'The stored text cannot be read as checks:',
            style: e.small.tint(e.muted),
          ),
          const SizedBox(height: Space.s4),
          Text(
            parseExpectedChecks(draft.expectedChecksText).error!,
            style: e.small.tint(e.err),
          ),
          const SizedBox(height: Space.s8),
          Align(
            alignment: Alignment.centerLeft,
            child: Btn(
              'Edit the text as raw lines',
              onTap: () => _mode(raw: true),
            ),
          ),
        ],
      );
    }
    final checks = draft.expectedChecks;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        if (checks.isEmpty)
          Text('No check is stated.', style: e.small.tint(e.muted))
        else
          Rows([
            for (final (at, check) in checks.indexed)
              RowItem(
                name: check.words,
                description: check.kind == ExpectedCheck.checkRunKind
                    ? 'A check run'
                    : 'A commit status',
                value: 'Remove',
                onTap: () => widget.model.setExpectedChecks([
                  for (final (i, each) in checks.indexed)
                    if (i != at) each,
                ]),
              ),
          ]),
        if (draft.expectedChecksChoice == ExpectedChecksChoice.custom) ...[
          const SizedBox(height: Space.s8),
          Align(
            alignment: Alignment.centerLeft,
            child: Btn(
              'Follow the defaults instead',
              onTap: widget.model.clearExpectedChecks,
            ),
          ),
        ],
        const SizedBox(height: Space.s16),
        _addForm(context),
      ],
    );
  }

  /// The draft's checks as the canonical lines it stores, and the rule they
  /// follow or the line that breaks it.
  Widget _rawMode(BuildContext context, ProjectDraft draft) {
    final e = Ember.of(context);
    final error = parseExpectedChecks(draft.expectedChecksText).error;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Field.multiline(
          label: 'Checks',
          controller: _rawText,
          mono: true,
          hint: 'check_run:build:42\ncommit_status:ci/lint',
          onChanged: widget.model.setExpectedChecksText,
        ),
        const SizedBox(height: Space.s8),
        Text(
          error ??
              'One check per line: check_run:<name>:<app id>, or '
                  'commit_status:<context>.',
          style: e.small.tint(error == null ? e.muted : e.err),
        ),
      ],
    );
  }

  /// The form that adds one check: its kind, its name and, for a check run,
  /// the app that runs it.
  Widget _addForm(BuildContext context) {
    final e = Ember.of(context);
    final checkRun = _kind == ExpectedCheck.checkRunKind;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Menu(
          choices: [
            MenuChoice(
              _kindWords(ExpectedCheck.checkRunKind),
              'An app check, told apart by the app that runs it',
              onSelect: () => setState(() {
                _kind = ExpectedCheck.checkRunKind;
                _addError = null;
              }),
            ),
            MenuChoice(
              _kindWords(ExpectedCheck.commitStatusKind),
              'A status the forge reports under its own context',
              onSelect: () => setState(() {
                _kind = ExpectedCheck.commitStatusKind;
                _addError = null;
              }),
            ),
          ],
          anchor: (context, toggle) =>
              Btn(_kindWords(_kind), icon: Glyphs.chevron_down, onTap: toggle),
        ),
        const SizedBox(height: Space.s12),
        Field(
          label: 'Name',
          controller: _name,
          hint: 'build',
          onChanged: (_) => _clearAddError(),
        ),
        if (checkRun) ...[
          const SizedBox(height: Space.s12),
          Field(
            label: 'App id',
            controller: _appID,
            hint: '42',
            onChanged: (_) => _clearAddError(),
          ),
          const SizedBox(height: Space.s6),
          Text(
            'Two apps can run a check of the same name; the app id tells them '
            'apart.',
            style: e.small.tint(e.muted),
          ),
        ],
        if (_addError != null) ...[
          const SizedBox(height: Space.s6),
          Text(_addError!, style: e.small.tint(e.err)),
        ],
        const SizedBox(height: Space.s12),
        Align(
          alignment: Alignment.centerLeft,
          child: Btn('Add the check', onTap: _add),
        ),
      ],
    );
  }

  void _clearAddError() {
    if (_addError != null) setState(() => _addError = null);
  }

  /// The words a check's kind is chosen by. The wire's spelling stays in
  /// [_kind], which is what the check stores.
  static String _kindWords(String kind) =>
      kind == ExpectedCheck.commitStatusKind ? 'Commit status' : 'Check run';

  /// Adds the form's check, or says why it cannot be added: nothing reaches
  /// the draft until the check is one the project can store.
  void _add() {
    final name = _name.text.trim();
    final checkRun = _kind == ExpectedCheck.checkRunKind;
    final appID = checkRun ? int.tryParse(_appID.text.trim()) ?? 0 : 0;
    final error = name.isEmpty
        ? 'A check needs a name.'
        : checkRun && appID <= 0
        ? 'A check run needs the app id that runs it, as a positive number.'
        : null;
    if (error != null) {
      setState(() => _addError = error);
      return;
    }
    final draft = widget.model.draft;
    if (draft == null) return;
    final check = ExpectedCheck(kind: _kind, name: name, appID: appID);
    _name.clear();
    _appID.clear();
    setState(() => _addError = null);
    widget.model.setExpectedChecks([...draft.expectedChecks, check]);
  }

  /// What earlier work recorded, offered only when asked for: the evidence
  /// with its provenance, or the words for having none.
  List<Widget> _suggestions(BuildContext context) {
    final e = Ember.of(context);
    if (!_showSuggestions) {
      return [
        Align(
          alignment: Alignment.centerLeft,
          child: Btn(
            'Show checks observed in this project',
            onTap: () => setState(() => _showSuggestions = true),
          ),
        ),
      ];
    }
    if (widget.observedChecks.isEmpty) {
      return [
        Text(
          'No check has been observed in this project yet, so add them by '
          'hand.',
          style: e.small.tint(e.muted),
        ),
      ];
    }
    return [
      Label('Previously observed'),
      const SizedBox(height: Space.s8),
      Rows([
        for (final observed in widget.observedChecks)
          RowItem(
            name: observed.check.words,
            description: observed.provenance(widget.now),
            value: 'Add',
            onTap: () => _addObserved(observed.check),
          ),
      ]),
      const SizedBox(height: Space.s8),
      Text(
        'These were recorded by earlier work; they are not a complete list of '
        'what the forge requires.',
        style: e.small.tint(e.muted),
      ),
    ];
  }

  /// Adds the one recorded check the person chose, and nothing else.
  void _addObserved(ExpectedCheck check) {
    final draft = widget.model.draft;
    if (draft == null) return;
    final checks = [...draft.expectedChecks];
    if (!checks.contains(check)) checks.add(check);
    widget.model.setExpectedChecks(checks);
  }
}
