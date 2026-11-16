import 'dart:async';

import '../../model/new_work_model.dart';
import '../kit/kit.dart';

/// The new-work dialog (spec decision 11) as the kit's `Sheet`, ready for
/// `Sheet.show`:
///
/// - the prompt first, because it is the one thing a person must write;
/// - the kind, with "Plan first" and "Work item" drawn disabled and named as
///   F6's;
/// - the chips, each defaulted from the project and each opening a `Menu`;
/// - Issues, Recent prompts and Recipes, which fill the prompt;
/// - Start, which posts the spawn request.
Sheet newWorkDialog(
  NewWorkModel model, {
  VoidCallback? onCancel,
  void Function(String agentId)? onStarted,
}) => Sheet(
  title: 'Start a new agent',
  actions: [
    Btn.primary(
      'Start',
      onTap: () async {
        await model.start();
        final started = model.started;
        if (started != null) onStarted?.call(started.agentID);
      },
    ),
    Btn('Cancel', onTap: onCancel),
  ],
  child: NewWorkForm(model: model),
);

/// The dialog's body: the prompt, the kind, the chips and the three lists.
class NewWorkForm extends StatefulWidget {
  const NewWorkForm({super.key, required this.model});

  final NewWorkModel model;

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
        final failure = model.failure;
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          mainAxisSize: MainAxisSize.min,
          children: [
            Field(
              label: 'What should it do?',
              controller: _prompt,
              hint: 'Describe the work in a sentence or two',
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
                for (final name in model.chipNames)
                  Menu(
                    choices: [
                      MenuChoice(
                        '${model.choices[name]}',
                        'The ${name.toLowerCase()} this agent starts with',
                        onSelect: () =>
                            model.setChoice(name, model.choices[name]!),
                      ),
                    ],
                    anchor: (context, toggle) => Btn(
                      '$name: ${model.choices[name]}',
                      icon: Glyphs.chevron_down,
                      onTap: toggle,
                    ),
                  ),
              ],
            ),
            const SizedBox(height: Space.s16),
            Wrap(
              spacing: Space.s8,
              runSpacing: Space.s8,
              children: [
                _fillMenu('Issues', [
                  for (final issue in model.issues)
                    MenuChoice(
                      issue.title,
                      'Fill the prompt from this issue',
                      onSelect: () => model.prefill(issue.title),
                    ),
                ], empty: 'No issues for this project'),
                _fillMenu('Recent prompts', [
                  for (final prompt in model.recentPrompts)
                    MenuChoice(
                      prompt,
                      'Use this prompt again',
                      onSelect: () => model.prefill(prompt),
                    ),
                ], empty: 'No recent prompts'),
                _fillMenu('Recipes', [
                  for (final recipe in model.recipes)
                    MenuChoice(
                      recipe.name,
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
