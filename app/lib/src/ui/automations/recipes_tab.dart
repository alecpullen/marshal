import '../../model/fleet_store.dart' show describeFailure;
import '../../model/recipes_model.dart';
import '../kit/kit.dart';
import '../sections/section_page.dart';
import 'automations_place.dart' show AutomationsTabs;
import 'confirm_action.dart';

/// The Recipes tab of Automations (F7 decision 4): the bridge's built-in
/// recipes and the user's own, each with copy, edit and run.
///
/// A built-in recipe cannot be edited or deleted — the bridge refuses both —
/// so its detail is read only and offers copying it under a new name.
class RecipesTab extends StatelessWidget {
  const RecipesTab({super.key, required this.model, this.tabs, this.state});

  final RecipesModel model;

  /// The tab bar the section page draws; null when the tab is shown alone.
  final AutomationsTabs? tabs;

  /// The state the shell knows.
  final StateKind? state;

  @override
  Widget build(BuildContext context) {
    return SectionPage<Recipe>(
      title: 'Recipes',
      subtitle: 'A saved prompt, with its inputs and limits',
      model: model,
      tabs: tabs?.items,
      tabIndex: tabs?.index ?? 0,
      onTab: tabs?.onChanged,
      state: state,
      emptyMessage:
          'A recipe appears here once the bridge has one; copy a built-in '
          'recipe to write your own.',
      row: (recipe, onTap) => RowItem(
        name: _name(recipe),
        description: _about(recipe),
        value: (recipe.builtin ?? false) ? 'Built-in' : 'Yours',
        onTap: onTap,
      ),
      detail: (context, recipe) => _RecipeForm(model: model, recipe: recipe),
    );
  }

  /// What the recipe is called: its title when it has one, else its name.
  static String _name(Recipe recipe) {
    final title = recipe.title ?? '';
    return title.isEmpty ? recipe.name : title;
  }

  /// Its name and kind, in one line.
  static String _about(Recipe recipe) {
    final diagnostic = recipe.diagnostic ?? '';
    if (diagnostic.isNotEmpty) return '$diagnostic · ${recipe.name}';
    return '${recipe.name} · ${recipe.kind}';
  }
}

/// A recipe's detail. The fields are the edit, so the shared Save stores it;
/// running and copying are the section's own actions, and deleting confirms.
class _RecipeForm extends StatefulWidget {
  const _RecipeForm({required this.model, required this.recipe});

  final RecipesModel model;
  final Recipe recipe;

  @override
  State<_RecipeForm> createState() => _RecipeFormState();
}

class _RecipeFormState extends State<_RecipeForm> {
  late final _title = TextEditingController(text: widget.recipe.title ?? '');
  late final _prompt = TextEditingController(text: widget.recipe.prompt);
  final _project = TextEditingController();
  final _copyAs = TextEditingController();
  var _confirming = false;
  var _running = false;
  var _copying = false;
  String? _note;
  String? _error;

  bool get _builtin => widget.recipe.builtin ?? false;

  @override
  void dispose() {
    _title.dispose();
    _prompt.dispose();
    _project.dispose();
    _copyAs.dispose();
    super.dispose();
  }

  /// The open item with what the fields hold now. A new title that is empty
  /// is no title at all, which is what the wire means by an absent one.
  Recipe _edited() {
    final held = widget.recipe;
    final title = _title.text;
    return Recipe(
      name: held.name,
      title: title.isEmpty ? null : title,
      description: held.description,
      kind: held.kind,
      mode: held.mode,
      auto: held.auto,
      workspace: held.workspace,
      routing: held.routing,
      inputs: held.inputs,
      prompt: _prompt.text,
      limits: held.limits,
      output: held.output,
      builtin: held.builtin,
      diagnostic: held.diagnostic,
    );
  }

  void _changed() {
    setState(() {
      _note = null;
      _error = null;
    });
    widget.model.change(_edited());
  }

  Future<void> _run() async {
    setState(() {
      _running = true;
      _note = null;
      _error = null;
    });
    try {
      await widget.model.run(widget.recipe, project: _project.text.trim());
      if (!mounted) return;
      setState(() {
        _running = false;
        _note = 'Started. Its agent is in the sidebar while it runs.';
      });
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _running = false;
        _error = describeFailure(error);
      });
    }
  }

  Future<void> _copy() async {
    final as = _copyAs.text.trim();
    if (as.isEmpty) {
      setState(() => _error = 'Give the copy a name first.');
      return;
    }
    setState(() {
      _copying = true;
      _note = null;
      _error = null;
    });
    try {
      final copied = await widget.model.copy(widget.recipe, as);
      if (!mounted) return;
      _copyAs.clear();
      setState(() {
        _copying = false;
        _note = 'Copied to ${copied.name}. It can be edited now.';
      });
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _copying = false;
        _error = describeFailure(error);
      });
    }
  }

  Future<void> _delete() async {
    setState(() => _error = null);
    try {
      await widget.model.delete(widget.recipe);
    } catch (error) {
      if (!mounted) return;
      setState(() => _error = describeFailure(error));
      return;
    }
    if (!mounted) return;
    Navigator.of(context).maybePop();
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final recipe = widget.recipe;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        if (_builtin) ...[
          Text(
            'A built-in recipe cannot be changed or deleted. Copy it under a '
            'name of your own, then edit the copy.',
            style: e.small.tint(e.muted),
          ),
          const SizedBox(height: Space.s12),
        ],
        Field(
          label: 'Title',
          controller: _title,
          enabled: !_builtin,
          hint: 'Nightly gate',
          onChanged: (_) => _changed(),
        ),
        const SizedBox(height: Space.s12),
        Field.multiline(
          label: 'Prompt',
          controller: _prompt,
          enabled: !_builtin,
          hint: 'Run the gate on {{ branch }}',
          onChanged: (_) => _changed(),
        ),
        const SizedBox(height: Space.s16),
        Field(
          label: 'Project to run it in',
          controller: _project,
          hint: '/home/you/marshal',
        ),
        const SizedBox(height: Space.s12),
        Align(
          alignment: Alignment.centerLeft,
          child: Btn('Run it', onTap: _running ? null : _run),
        ),
        if (_note != null) ...[
          const SizedBox(height: Space.s12),
          Text(_note!, style: e.small.tint(e.muted)),
        ],
        if (_error != null) ...[
          const SizedBox(height: Space.s12),
          Text(_error!, style: e.small.tint(e.err)),
        ],
        const SizedBox(height: Space.s20),
        Field(label: 'Copy it as', controller: _copyAs, hint: 'nightly-gate'),
        const SizedBox(height: Space.s12),
        Align(
          alignment: Alignment.centerLeft,
          child: Btn(
            _copying ? 'Copying' : 'Copy it',
            onTap: _copying ? null : _copy,
          ),
        ),
        if (!_builtin) ...[
          const SizedBox(height: Space.s20),
          if (_confirming)
            ConfirmAction(
              question: 'Delete ${recipe.name}?',
              loss:
                  'The recipe is deleted. Schedules that name it keep running '
                  'nothing until they name another.',
              confirmLabel: 'Delete it',
              onConfirm: _delete,
              onCancel: () => setState(() => _confirming = false),
            )
          else
            Align(
              alignment: Alignment.centerLeft,
              child: Btn(
                'Delete this recipe',
                onTap: () => setState(() => _confirming = true),
              ),
            ),
        ],
      ],
    );
  }
}
