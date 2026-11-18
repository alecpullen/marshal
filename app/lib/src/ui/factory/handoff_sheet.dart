import 'dart:async';

import '../../model/expected_checks.dart';
import '../../model/handoff_model.dart';
import '../kit/kit.dart';

/// Shows the handoff review (F6 decision 8) and returns the item it created,
/// if approving created one.
///
/// One sheet, fields in the order the plan states them, each marked as
/// inherited from the project or overridden here, and one primary action that
/// is disabled — with a tooltip saying why — while a blocker stands.
Future<String?> showHandoffSheet(BuildContext context, HandoffModel model) =>
    Sheet.show<String>(context, handoffSheet(context, model));

/// The handoff review as the kit's `Sheet`, ready for `Sheet.show`.
Sheet handoffSheet(BuildContext context, HandoffModel model) => Sheet(
  title: 'Hand to the factory',
  actions: [
    _ApproveAction(model: model),
    Btn('Cancel', onTap: () => Navigator.of(context).maybePop()),
  ],
  child: HandoffForm(model: model),
);

/// The sheet's one primary action: creating the factory draft, disabled while
/// the model says what stops it, and saying that in its tooltip.
class _ApproveAction extends StatelessWidget {
  const _ApproveAction({required this.model});

  final HandoffModel model;

  @override
  Widget build(BuildContext context) => AnimatedBuilder(
    animation: model,
    builder: (context, _) => _build(context),
  );

  Widget _build(BuildContext context) {
    final blocker = model.blockers.isEmpty ? null : model.blockers.first;
    final approve = Btn.primary(
      // Creating a factory item is not starting work: the item runs when it is
      // approved in the factory, so the label says what the request does.
      'Create the factory draft',
      onTap: blocker != null
          ? null
          : () async {
              final item = await model.approve();
              if (item != null && context.mounted) {
                Navigator.of(context).maybePop(item.id);
              }
            },
    );
    if (blocker == null) return approve;
    return Tip(message: blocker, child: approve);
  }
}

/// The sheet's body: every field in order, the blockers, the consequence, and
/// the failure the bridge last gave.
class HandoffForm extends StatefulWidget {
  const HandoffForm({super.key, required this.model});

  final HandoffModel model;

  @override
  State<HandoffForm> createState() => _HandoffFormState();
}

class _HandoffFormState extends State<HandoffForm> {
  late final Map<String, TextEditingController> _controllers = {
    for (final field in const [
      HandoffField.repository,
      HandoffField.implementer,
      HandoffField.reviewer,
      HandoffField.checks,
    ])
      field: TextEditingController(text: _text(field)),
    HandoffField.rounds: TextEditingController(
      text: '${widget.model.valueOf(HandoffField.rounds)}',
    ),
  };

  /// The text a field's controller holds.
  ///
  /// The expected checks are shown as the person typed them — text the parser
  /// refuses included — rather than as the checks that happened to parse.
  String _text(String field) {
    if (field == HandoffField.checks) return widget.model.checksText;
    final value = widget.model.valueOf(field);
    if (value is List<ExpectedCheck>) return formatExpectedChecks(value);
    return '$value';
  }

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_changed);
    // The settings a review is bound to move without this page reloading, so
    // the version is read here and read again at approval: an approval that
    // would freeze settings nobody read is a conflict, not a failure.
    unawaited(widget.model.checkSettings());
  }

  @override
  void dispose() {
    // Closing the sheet saves what the autosave window had not yet written.
    unawaited(widget.model.flush());
    widget.model.removeListener(_changed);
    for (final controller in _controllers.values) {
      controller.dispose();
    }
    super.dispose();
  }

  void _changed() {
    if (!mounted) return;
    // A reset puts the project's value back, and the field follows it.
    for (final entry in _controllers.entries) {
      final next = _text(entry.key);
      if (entry.value.text != next) entry.value.text = next;
    }
    setState(() {});
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final model = widget.model;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      mainAxisSize: MainAxisSize.min,
      children: [
        Text('Scope', style: e.strong),
        const SizedBox(height: Space.s4),
        Text(
          model.title.trim().isEmpty ? 'Untitled work' : model.title.trim(),
          style: e.body,
        ),
        const SizedBox(height: Space.s4),
        Text(_planSummary(model), style: e.small.tint(e.muted)),
        const SizedBox(height: Space.s16),
        _evidence(context, model),
        const SizedBox(height: Space.s16),
        Box.flush(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              for (final (index, field) in _fields(context).entries.indexed)
                _row(context, field.key, field.value, first: index == 0),
            ],
          ),
        ),
        if (model.blockers.isNotEmpty) ...[
          const SizedBox(height: Space.s12),
          Label('What stops approval', count: model.blockers.length),
          const SizedBox(height: Space.s4),
          for (final blocker in model.blockers)
            Padding(
              padding: const EdgeInsets.only(bottom: Space.s2),
              child: Text(blocker, style: e.small.tint(e.err)),
            ),
        ],
        const SizedBox(height: Space.s12),
        Label('What creating the draft does'),
        const SizedBox(height: Space.s4),
        Text(model.consequence, style: e.body),
        if (model.failure != null) ...[
          const SizedBox(height: Space.s8),
          Text(model.failure!, style: e.small.tint(e.err)),
        ],
      ],
    );
  }

  /// What the capture froze, or the plan an unbound review will send: the
  /// evidence the person is reviewing, taken from the capture alone (the sheet
  /// edits no evidence and selects no files).
  Widget _evidence(BuildContext context, HandoffModel model) {
    final e = Ember.of(context);
    final snapshot = model.reviewedSnapshot;
    final plan = model.planToSend.trim();
    final spec = model.specToSend.trim();
    // The text that travels, shown rather than described: a sentence promising
    // "the plan text shown here" is false if the plan text is never drawn.
    final documents = Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        if (plan.isEmpty)
          Text('No plan text to send.', style: e.small.tint(e.muted))
        else
          OutputBlock(text: plan),
        if (spec.isNotEmpty) ...[
          const SizedBox(height: Space.s8),
          Text('Specification', style: e.bodyStrong),
          const SizedBox(height: Space.s4),
          OutputBlock(text: spec),
        ],
      ],
    );
    if (!model.boundToSnapshot || snapshot == null) {
      return Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Label('What will be sent'),
          const SizedBox(height: Space.s4),
          Text(
            'No capture is bound to this review, so this plan text is what the '
            'factory is sent.',
            style: e.small.tint(e.muted),
          ),
          const SizedBox(height: Space.s8),
          documents,
        ],
      );
    }
    final files = snapshot.files;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Label('The frozen capture', count: files.isEmpty ? null : files.length),
        const SizedBox(height: Space.s4),
        Text(
          'Snapshot ${_short(snapshot.id)} · digest ${_short(snapshot.sha256)}',
          style: e.monoSmall.tint(e.muted),
        ),
        const SizedBox(height: Space.s2),
        Text(
          'Base commit ${_short(snapshot.baseCommit)}',
          style: e.monoSmall.tint(e.muted),
        ),
        const SizedBox(height: Space.s8),
        Label('The plan'),
        const SizedBox(height: Space.s4),
        documents,
        const SizedBox(height: Space.s8),
        if (files.isEmpty)
          Text(
            'No files selected: this capture froze the documents only.',
            style: e.small.tint(e.muted),
          )
        else
          for (final file in files) ...[
            Padding(
              padding: const EdgeInsets.only(bottom: Space.s6),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  // A path is an identifier, so it is set in mono (§3).
                  Text(file.path, style: e.monoSmall),
                  const SizedBox(height: Space.s2),
                  Text(
                    _operationWords(file.operation),
                    style: e.small.tint(e.muted),
                  ),
                  const SizedBox(height: Space.s4),
                  OutputBlock(
                    text: file.diff.isEmpty
                        ? 'No difference recorded for this file.'
                        : file.diff,
                  ),
                ],
              ),
            ),
          ],
        if (snapshot.rejected.isNotEmpty) ...[
          const SizedBox(height: Space.s6),
          Label('Not captured', count: snapshot.rejected.length),
          const SizedBox(height: Space.s4),
          for (final rejected in snapshot.rejected)
            Padding(
              padding: const EdgeInsets.only(bottom: Space.s4),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(rejected.path, style: e.monoSmall),
                  const SizedBox(height: Space.s2),
                  Text(rejected.reason, style: e.small.tint(e.muted)),
                ],
              ),
            ),
        ],
        const SizedBox(height: Space.s6),
        Text(
          'The draft is built from these frozen bytes: the bridge rewrites the '
          'plan and the specification from the capture and checks every file '
          'against its recorded hash. Changing the evidence needs a fresh '
          'capture.',
          style: e.small.tint(e.muted),
        ),
      ],
    );
  }

  /// A captured file's operation in the sheet's words.
  static String _operationWords(String operation) => switch (operation) {
    'create' => 'Created',
    'modify' => 'Modified',
    'delete' => 'Deleted',
    _ => operation.isEmpty ? 'Captured' : operation,
  };

  /// An id, a digest or a commit, short enough for one line.
  static String _short(String value) =>
      value.length <= 8 ? value : value.substring(0, 8);

  /// The fields the sheet states itself: the scope is drawn above, and the
  /// environment and authority are menus rather than typed values.
  Map<String, Widget> _fields(BuildContext context) => {
    HandoffField.repository: _field(
      HandoffField.repository,
      hint: 'The repository the work lands in',
    ),
    HandoffField.environment: _menu(
      HandoffField.environment,
      HandoffModel.environments,
    ),
    HandoffField.implementer: _field(
      HandoffField.implementer,
      hint: 'The project default',
    ),
    HandoffField.reviewer: _field(
      HandoffField.reviewer,
      hint: 'The project default',
    ),
    HandoffField.checks: _checksField(context),
    HandoffField.rounds: _field(HandoffField.rounds, hint: '3'),
    HandoffField.authority: _menu(
      HandoffField.authority,
      HandoffAuthority.options,
    ),
  };

  /// The expected checks: the raw lines the person types, the parser's own
  /// words under text it refuses, and the one control that acknowledges the
  /// work needs no check at all. Text the parser refuses is kept as typed,
  /// exactly as the project editor keeps it.
  Widget _checksField(BuildContext context) {
    final e = Ember.of(context);
    final model = widget.model;
    final acknowledged = model.noRequiredCIAcknowledged;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        // The checks are a document, one per line, so this is the kit's
        // multiline field in `mono` — the same field the project editor's raw
        // mode uses — rather than a single line that would join them.
        Field.multiline(
          label: HandoffField.checks,
          controller: _controllers[HandoffField.checks],
          hint: 'One per line, e.g. check_run:build:42',
          mono: true,
          enabled: !acknowledged,
          error: model.checksError,
          onChanged: model.setChecksText,
        ),
        const SizedBox(height: Space.s8),
        Align(
          alignment: Alignment.centerLeft,
          child: Btn(
            acknowledged
                ? 'Withdraw the acknowledgement'
                : 'Acknowledge no check is required',
            onTap: () => model.setNoRequiredCIAcknowledged(!acknowledged),
          ),
        ),
        if (acknowledged) ...[
          const SizedBox(height: Space.s4),
          Text(
            'This work needs no CI check to pass; acknowledging clears the '
            'stated checks.',
            style: e.small.tint(e.muted),
          ),
        ],
      ],
    );
  }

  /// One row of the sheet: the field's name, its control, and where its value
  /// came from.
  Widget _row(
    BuildContext context,
    String field,
    Widget control, {
    required bool first,
  }) {
    final e = Ember.of(context);
    final overridden = widget.model.isOverridden(field);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        if (!first) const Hairline(),
        Padding(
          padding: const EdgeInsets.all(Space.s12),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  Expanded(child: Text(field, style: e.bodyStrong)),
                  Text(
                    widget.model.sourceOf(field).word,
                    style: e.caption.tint(e.muted),
                  ),
                  if (overridden) ...[
                    const SizedBox(width: Space.s6),
                    Btn(
                      'Reset',
                      icon: Glyphs.arrow_counterclockwise,
                      onTap: () => widget.model.reset(field),
                    ),
                  ],
                ],
              ),
              const SizedBox(height: Space.s6),
              control,
            ],
          ),
        ),
      ],
    );
  }

  /// A field the person types into.
  Widget _field(String field, {required String hint}) => Field(
    label: field,
    controller: _controllers[field],
    hint: hint,
    onChanged: (text) => widget.model.set(field, _valueOf(field, text)),
  );

  /// A field with a fixed set of values, as the kit's menu.
  Widget _menu(String field, List<String> options) {
    final current = widget.model.valueOf(field);
    return Menu(
      choices: [
        for (final option in options)
          MenuChoice(
            option,
            option == current ? 'The value in force' : 'Use $option',
            onSelect: () => widget.model.set(field, option),
          ),
      ],
      anchor: (context, toggle) =>
          Btn('$current', icon: Glyphs.chevron_down, onTap: toggle),
    );
  }

  /// The value a typed field means: the round allowance is a number, and
  /// everything else is its own text.
  ///
  /// The expected checks are not here: [_checksField] types them through
  /// [HandoffModel.setChecksText], which keeps the text as typed.
  Object _valueOf(String field, String text) => switch (field) {
    HandoffField.rounds => int.tryParse(text.trim()) ?? 0,
    _ => text.trim(),
  };

  /// The plan in one line: how many tasks it holds.
  static String _planSummary(HandoffModel model) {
    final text = model.plan.trim();
    if (text.isEmpty) return 'No plan text';
    final tasks = RegExp(
      r'^#{2,3} Task \d+',
      multiLine: true,
    ).allMatches(text).length;
    if (tasks == 0) return 'A plan without numbered tasks';
    return '$tasks ${tasks == 1 ? 'task' : 'tasks'}';
  }
}
