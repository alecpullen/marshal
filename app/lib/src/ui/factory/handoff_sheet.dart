import 'dart:async';

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

/// The sheet's one primary action: Approve, disabled while the model says
/// what stops it, and saying that in its tooltip.
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
      'Approve and start',
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

  String _text(String field) {
    final value = widget.model.valueOf(field);
    if (value is List<String>) return value.join(', ');
    return '$value';
  }

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_changed);
  }

  @override
  void dispose() {
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
        Box.flush(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              for (final (index, field) in _fields.entries.indexed)
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
        Label('What approving authorises'),
        const SizedBox(height: Space.s4),
        Text(model.consequence, style: e.body),
        if (model.failure != null) ...[
          const SizedBox(height: Space.s8),
          Text(model.failure!, style: e.small.tint(e.err)),
        ],
      ],
    );
  }

  /// The fields the sheet states itself: the scope is drawn above, and the
  /// environment and authority are menus rather than typed values.
  Map<String, Widget> get _fields => {
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
    HandoffField.checks: _field(
      HandoffField.checks,
      hint: 'Comma separated, e.g. build, unit',
    ),
    HandoffField.rounds: _field(HandoffField.rounds, hint: '3'),
    HandoffField.authority: _menu(
      HandoffField.authority,
      HandoffAuthority.options,
    ),
  };

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

  /// The value a typed field means: the round allowance is a number, the
  /// expected checks are a list, and everything else is its own text.
  Object _valueOf(String field, String text) => switch (field) {
    HandoffField.rounds => int.tryParse(text.trim()) ?? 0,
    HandoffField.checks => [
      for (final name in text.split(','))
        if (name.trim().isNotEmpty) name.trim(),
    ],
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
