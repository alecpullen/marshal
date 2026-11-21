import 'dart:async';

import '../../model/drafts_model.dart';
import '../../model/nav_model.dart' show projectName;
import '../../model/new_work_model.dart';
import '../kit/kit.dart';

/// The project composer (agent-first Studio, task 2): the one entry a person
/// starts ordinary work from.
///
/// The sidebar's project row, New beside the search field, the narrow app
/// bar's plus and `⌘N` all open this screen for one project: the project's
/// identity above the input, the prompt prominent, and everything that is not
/// the prompt — the overrides, Issues, Recipes, Recent prompts, Drafts and the
/// explicit **Plan first** and **Work item** paths — behind [optionsLabel].
///
/// Nothing is created before Send: the screen reads the lists that fill the
/// prompt and posts nothing until a person presses it, and a failed spawn
/// keeps the prompt and says what the bridge said.
class ProjectComposer extends StatefulWidget {
  const ProjectComposer({
    super.key,
    required this.model,
    required this.project,
    required this.host,
    this.drafts = const [],
    this.onOpenDraft,
    this.onStarted,
    this.onPlanned,
    this.onWorkItem,
  });

  final NewWorkModel model;

  /// The root the composer opens on. The Project chip may move it, and the
  /// identity above the input follows the chip.
  final String project;

  final String host;

  /// The project's unfinished drafts (F6 decision 10): a draft is reopened
  /// from Options rather than retyped.
  final List<DraftVersion> drafts;
  final void Function(DraftVersion draft)? onOpenDraft;

  /// What Send started: the agent, a planner, or — for a work item — the
  /// prompt the handoff review is opened with.
  final void Function(String agentId)? onStarted;
  final void Function(StartedPlanner planner)? onPlanned;
  final void Function(String prompt)? onWorkItem;

  /// The label the non-prompt controls sit behind (design-system.md §5: at
  /// most one primary action, and the prompt is what a person came to write).
  static const optionsLabel = 'Options';

  @override
  State<ProjectComposer> createState() => _ProjectComposerState();
}

class _ProjectComposerState extends State<ProjectComposer> {
  late final TextEditingController _prompt = TextEditingController(
    text: widget.model.prompt,
  );

  /// Whether the controls that are not the prompt are open. They start
  /// closed: the composer is a conversation, not a settings form.
  bool _options = false;

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

  /// Posts what the composer is set to, and hands back what it started.
  ///
  /// A work item opens the handoff review with the prompt as its plan (F6
  /// decision 9), and "Plan first" starts a planner (F6 decision 8); neither
  /// is spawned here, which is the behaviour the new-work dialog had and this
  /// entry keeps.
  ///
  /// The returned thread is bound here, not prompted again (agent-first
  /// Studio, task 3): the first send carried the prompt, so opening the thread
  /// is a binding — a second turn would be the same instruction twice.
  Future<void> _send() async {
    final model = widget.model;
    // A second press while the first request is in flight would be a second
    // ask; the model refuses it too, and the button is disabled for the same
    // reason.
    if (model.starting) return;
    if (model.kind == NewWorkKind.workItem) {
      if (!model.canStart) {
        await model.start();
        return;
      }
      widget.onWorkItem?.call(model.prompt.trim());
      return;
    }
    await model.start();
    final planner = model.startedPlanner;
    if (planner != null) {
      widget.onPlanned?.call(planner);
      return;
    }
    final started = model.started;
    if (started != null) widget.onStarted?.call(started.agentID);
  }

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: widget.model,
      builder: (context, _) {
        final model = widget.model;
        final e = Ember.of(context);
        final root = model.choices['Project'] ?? widget.project;
        final failure = model.failure;
        return PageBody(
          // The page's own name is what it does; the project it does it in
          // sits above the input, where a person checks it before typing.
          title: 'Start a new agent',
          subtitle:
              '${projectName(root)} on ${model.choices['Host'] ?? widget.host}',
          children: [
            _identity(e, root),
            const SizedBox(height: Space.s12),
            Field.multiline(
              label: model.promptOptional
                  ? 'What should it do? (optional)'
                  : 'What should it do?',
              controller: _prompt,
              hint: 'Describe the work in a sentence or two',
              error: model.promptError,
              onChanged: model.prefill,
            ),
            if (failure != null) ...[
              const SizedBox(height: Space.s8),
              Text(failure, style: e.small.tint(e.err)),
            ],
            // Uncertainty is said out loud: the bridge may have started the
            // agent even though its answer never arrived, and pressing Send
            // again resolves that same request rather than starting a second
            // one. The draft stays, which is what makes that possible.
            if (model.outcomeUnknown) ...[
              const SizedBox(height: Space.s8),
              Text(
                'The bridge did not confirm this request. Send it again — the '
                'same request is resolved first, so it will not start a second '
                'agent.',
                style: e.small.tint(e.muted),
              ),
            ],
            const SizedBox(height: Space.s12),
            Wrap(
              spacing: Space.s8,
              runSpacing: Space.s8,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: [
                // A request in flight is not asked twice: the button goes
                // quiet until the bridge answers.
                Btn.primary(
                  model.starting ? 'Starting…' : 'Send',
                  onTap: model.starting ? null : () => unawaited(_send()),
                ),
                Btn(
                  ProjectComposer.optionsLabel,
                  icon: _options ? Glyphs.chevron_down : Glyphs.chevron_right,
                  onTap: () => setState(() => _options = !_options),
                ),
              ],
            ),
            if (_options) ...[
              const SizedBox(height: Space.s16),
              _optionsBody(e, model),
            ],
          ],
        );
      },
    );
  }

  /// The project this conversation is in, above the input: its name, its root
  /// and the host that serves it, so two projects with the same name read as
  /// two projects.
  Widget _identity(Ember e, String root) => Wrap(
    spacing: Space.s8,
    runSpacing: Space.s4,
    crossAxisAlignment: WrapCrossAlignment.center,
    children: [
      Text(projectName(root), style: e.bodyStrong),
      Text(root, style: e.small.tint(e.muted)),
    ],
  );

  /// Everything the prompt is not: the kind (with the explicit Plan first and
  /// Work item paths), the overrides, and the lists that fill the prompt.
  Widget _optionsBody(Ember e, NewWorkModel model) => Column(
    crossAxisAlignment: CrossAxisAlignment.stretch,
    mainAxisSize: MainAxisSize.min,
    children: [
      Label('This run'),
      const SizedBox(height: Space.s8),
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
                ? Btn.primary(kind.word, onTap: () => model.selectKind(kind))
                : Btn(kind.word, onTap: () => model.selectKind(kind)),
        ],
      ),
      const SizedBox(height: Space.s16),
      Label('Overrides'),
      const SizedBox(height: Space.s8),
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
                              ? 'The ${chip.name.toLowerCase()} this agent '
                                    'starts with'
                              : 'Change the '
                                    '${chip.name.toLowerCase()} to $option',
                          onSelect: () => model.setChoice(chip.name, option),
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
      Label('Fill the prompt'),
      const SizedBox(height: Space.s8),
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
    ],
  );

  /// One of the lists that fill the prompt, as a button that opens it. A list
  /// with nothing in it opens one line saying which kind of empty it is.
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
