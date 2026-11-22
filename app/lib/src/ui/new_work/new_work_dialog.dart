import 'dart:async';

import '../../model/drafts_model.dart';
import '../../model/new_work_model.dart';
import '../kit/kit.dart';

/// The new-work dialog (spec decision 11) as the kit's `Sheet`, ready for
/// `Sheet.show`:
///
/// - the prompt first, because it is the one thing a person must write — and
///   for an agent it is the one thing they may leave blank, because the thread
///   can take the first instruction once there is one;
/// - the kind, with "Plan first" and "Work item" drawn disabled and named as
///   F6's;
/// - the chips, each defaulted from the project and each opening a `Menu`;
/// - Issues, Recent prompts and Recipes, which fill the prompt;
/// - Start, which posts the spawn request.
Sheet newWorkDialog(
  NewWorkModel model, {
  VoidCallback? onCancel,
  void Function(String agentId)? onStarted,
  void Function(StartedPlanner planner)? onPlanned,
  void Function(String prompt)? onWorkItem,
  List<DraftVersion> drafts = const [],
  void Function(DraftVersion draft)? onOpenDraft,
}) => Sheet(
  title: 'Start a new agent',
  actions: [
    Btn.primary(
      'Start',
      onTap: () async {
        if (model.kind == NewWorkKind.workItem) {
          // "Work item" opens the handoff review with the prompt as the
          // plan's starting text (F6 decision 9); nothing is spawned here.
          // A work item whose box is blank has no plan to review, so the
          // model says what it needs instead of opening an empty review.
          if (!model.canStart) {
            await model.start();
            return;
          }
          onWorkItem?.call(model.prompt.trim());
          return;
        }
        await model.start();
        final planner = model.startedPlanner;
        if (planner != null) {
          onPlanned?.call(planner);
          return;
        }
        final started = model.started;
        if (started != null) onStarted?.call(started.agentID);
      },
    ),
    Btn('Cancel', onTap: onCancel),
  ],
  child: NewWorkForm(model: model, drafts: drafts, onOpenDraft: onOpenDraft),
);

/// The dialog's body: the prompt, the kind, the chips and the three lists.
class NewWorkForm extends StatefulWidget {
  const NewWorkForm({
    super.key,
    required this.model,
    this.drafts = const [],
    this.onOpenDraft,
  });

  final NewWorkModel model;

  /// The project's unfinished drafts (F6 decision 10): a draft is reopened
  /// from here rather than retyped.
  final List<DraftVersion> drafts;
  final void Function(DraftVersion draft)? onOpenDraft;

  @override
  State<NewWorkForm> createState() => _NewWorkFormState();
}

class _NewWorkFormState extends State<NewWorkForm> {
  late final TextEditingController _prompt = TextEditingController(
    text: widget.model.prompt,
  );

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_syncPrompt);
    _prompt.addListener(_syncToModel);
    unawaited(widget.model.load());
  }

  @override
  void dispose() {
    widget.model.removeListener(_syncPrompt);
    _prompt
      ..removeListener(_syncToModel)
      ..dispose();
    super.dispose();
  }

  /// A list that fills the prompt writes the model; the field follows it.
  void _syncPrompt() {
    if (_prompt.text == widget.model.prompt) return;
    _prompt.text = widget.model.prompt;
  }

  void _syncToModel() {
    if (widget.model.prompt == _prompt.text) return;
    widget.model.prefill(_prompt.text);
  }

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: widget.model,
      builder: (context, _) {
        final model = widget.model;
        final e = Ember.of(context);
        final promptError = model.promptError;
        final failure = model.failure;
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          mainAxisSize: MainAxisSize.min,
          children: [
            Field(
              // An agent's prompt is optional: the thread takes the first
              // instruction, so the label says so and Start stays live with
              // the box blank. A planning thread and a work item use the
              // prompt as their own text, so theirs is required.
              label: model.promptOptional
                  ? 'What should it do? (optional)'
                  : 'What should it do?',
              controller: _prompt,
              hint: 'Describe the work in a sentence or two',
              error: promptError,
              onChanged: model.prefill,
            ),
            const SizedBox(height: Space.s16),
            Wrap(
              spacing: Space.s8,
              runSpacing: Space.s4,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: [
                for (final kind in NewWorkKind.values)
                  kind.planned
                      ? Tip(
                          message: 'Planned for F6',
                          child: Btn(kind.word, onTap: null),
                        )
                      : model.kind == kind
                      ? Btn.primary(
                          kind.word,
                          onTap: () => model.selectKind(kind),
                        )
                      : Btn(kind.word, onTap: () => model.selectKind(kind)),
              ],
            ),
            const SizedBox(height: Space.s16),
            Wrap(
              spacing: Space.s8,
              runSpacing: Space.s8,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: [
                for (final chip in model.chips)
                  chip.chooseable
                      ? Menu(
                          choices: [
                            for (final option in chip.options)
                              MenuChoice(
                                option,
                                option == chip.value
                                    ? 'The ${chip.name.toLowerCase()} this '
                                          'agent starts with'
                                    : 'Change the '
                                          '${chip.name.toLowerCase()} to '
                                          '$option',
                                onSelect: () =>
                                    model.setChoice(chip.name, option),
                              ),
                          ],
                          anchor: (context, toggle) => Btn(
                            '${chip.name}: ${chip.value}',
                            icon: Glyphs.chevron_down,
                            onTap: toggle,
                          ),
                        )
                      : Pill('${chip.name}: ${chip.value}', tooltip: chip.note),
              ],
            ),
            const SizedBox(height: Space.s16),
            Wrap(
              spacing: Space.s8,
              runSpacing: Space.s8,
              children: [
                _fillMenu(
                  'Issues',
                  [
                    for (final issue in model.issues)
                      MenuChoice(
                        issue.title,
                        'Fill the prompt from this issue',
                        onSelect: () => model.prefill(issue.title),
                      ),
                  ],
                  empty: model.issuesFromRepo
                      ? 'No issues for this project'
                      : 'No registered repo for this project',
                ),
                _fillMenu('Recent prompts', [
                  for (final prompt in model.recentPrompts)
                    MenuChoice(
                      prompt,
                      'Use this prompt again',
                      onSelect: () => model.prefill(prompt),
                    ),
                ], empty: 'No recent prompts'),
                if (widget.onOpenDraft != null)
                  _fillMenu('Drafts', [
                    for (final draft in widget.drafts)
                      MenuChoice(
                        draft.title.trim().isEmpty
                            ? 'Untitled work'
                            : draft.title.trim(),
                        'Open version ${draft.version ?? 'unsaved'}',
                        onSelect: () => widget.onOpenDraft?.call(draft),
                      ),
                  ], empty: 'No unfinished drafts'),
                _fillMenu('Recipes', [
                  for (final recipe in model.recipes)
                    MenuChoice(
                      recipe.title ?? recipe.name,
                      'Fill the prompt from this recipe',
                      onSelect: () => model.prefill(recipe.prompt),
                    ),
                ], empty: 'No recipes yet'),
              ],
            ),
            if (failure != null) ...[
              const SizedBox(height: Space.s8),
              Text(failure, style: e.small.tint(e.err)),
            ],
          ],
        );
      },
    );
  }

  /// One of the three lists, as a button that opens it.
  Widget _fillMenu(
    String label,
    List<MenuChoice> choices, {
    required String empty,
  }) => Menu(
    choices: choices.isEmpty
        ? [
            MenuChoice(
              empty,
              'Nothing to fill the prompt with yet',
              onSelect: () {},
            ),
          ]
        : choices,
    anchor: (context, toggle) =>
        Btn(label, icon: Glyphs.chevron_down, onTap: toggle),
  );
}
