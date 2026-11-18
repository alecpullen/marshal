import '../../model/models_model.dart';
import '../kit/kit.dart';
import '../sections/section_page.dart';

/// Settings' Models and routing section (F7 decision 7): the role-to-model
/// map, the presets, the caps and what happens at them, and each provider's
/// write-only key.
///
/// The note above the rows says where a save goes, because it is the rule
/// people are most surprised by: providers and presets are the user's global
/// config, never a project's.
class ModelsSection extends StatelessWidget {
  const ModelsSection({super.key, required this.model, this.state});

  final ModelsModel model;

  /// The state the shell knows, when it knows more than the model does.
  final StateKind? state;

  @override
  Widget build(BuildContext context) {
    return SectionPage<ModelRow>(
      title: 'Models and routing',
      subtitle: 'What each role runs, and what it may spend',
      model: model,
      state: state,
      emptyMessage: 'No provider is configured yet.',
      header: const [_GlobalConfigNote()],
      group: (row) => switch (row.kind) {
        ModelRowKind.role => 'Roles',
        ModelRowKind.preset => 'Presets',
        ModelRowKind.key => 'Keys',
        ModelRowKind.capAmount || ModelRowKind.capBehaviour => 'Spending',
      },
      row: (row, onTap) => RowItem(
        name: row.name,
        description: row.description,
        value: row.shownValue,
        onTap: onTap,
      ),
      detail: (context, row) => _ModelDetail(model: model, row: row),
    );
  }
}

/// Where these settings are stored: the user's global config, never a
/// project's.
class _GlobalConfigNote extends StatelessWidget {
  const _GlobalConfigNote();

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Box(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text('Saved to your global config', style: e.body),
          const SizedBox(height: Space.s4),
          Text(
            'Providers, presets, roles and caps are the user’s, not a '
            'project’s: a change here applies to every project, and a project '
            'config never carries them. A provider’s key is write-only — it is '
            'written and never read back.',
            style: e.small.tint(e.muted),
          ),
        ],
      ),
    );
  }
}

/// One row's detail, by how it is edited.
class _ModelDetail extends StatelessWidget {
  const _ModelDetail({required this.model, required this.row});

  final ModelsModel model;
  final ModelRow row;

  @override
  Widget build(BuildContext context) {
    switch (row.editor) {
      case ModelEditor.choices:
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Rows([
              for (final (choice, about) in row.choices)
                RowItem(
                  name: choice,
                  description: about,
                  value: choice == row.value ? 'Current' : null,
                  selected: choice == row.value,
                  onTap: () => model.change(row.withValue(choice)),
                ),
            ]),
            if (row.note != null) ...[
              const SizedBox(height: Space.s12),
              Text(
                row.note!,
                style: Ember.of(context).small.tint(Ember.of(context).muted),
              ),
            ],
          ],
        );
      case ModelEditor.amount:
        return _EditField(
          key: ValueKey(row.id),
          label: 'Dollars',
          hint: '0.00',
          initial: row.value,
          onChanged: (text) => model.change(row.withValue(text)),
          note: row.note,
        );
      case ModelEditor.secret:
        return _EditField(
          key: ValueKey(row.id),
          label: 'New key',
          hint: 'Paste the key; it is never shown again',
          initial: '',
          onChanged: (text) => model.change(row.withValue(text)),
          note: row.note,
        );
      case ModelEditor.readOnly:
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Box(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  for (final (term, meaning) in row.facts) _Fact(term, meaning),
                ],
              ),
            ),
            if (row.note != null) ...[
              const SizedBox(height: Space.s12),
              Text(
                row.note!,
                style: Ember.of(context).small.tint(Ember.of(context).muted),
              ),
            ],
          ],
        );
    }
  }
}

/// A row edited by typing: a cap in dollars, or a key that is never shown
/// again.
class _EditField extends StatefulWidget {
  const _EditField({
    super.key,
    required this.label,
    required this.hint,
    required this.initial,
    required this.onChanged,
    this.note,
  });

  final String label;
  final String hint;
  final String initial;
  final ValueChanged<String> onChanged;
  final String? note;

  @override
  State<_EditField> createState() => _EditFieldState();
}

class _EditFieldState extends State<_EditField> {
  late final _controller = TextEditingController(text: widget.initial);

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final note = widget.note;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Field(
          label: widget.label,
          hint: widget.hint,
          controller: _controller,
          onChanged: widget.onChanged,
        ),
        if (note != null) ...[
          const SizedBox(height: Space.s12),
          Text(note, style: e.small.tint(e.muted)),
        ],
      ],
    );
  }
}

/// One named fact of a preset.
class _Fact extends StatelessWidget {
  const _Fact(this.term, this.meaning);

  final String term;
  final String meaning;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Padding(
      padding: const EdgeInsets.only(bottom: Space.s12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(term, style: e.small.tint(e.muted)),
          const SizedBox(height: Space.s2),
          Text(meaning, style: e.body),
        ],
      ),
    );
  }
}
