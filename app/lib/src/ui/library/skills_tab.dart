import '../../model/fleet_store.dart' show describeFailure;
import '../../model/library_model.dart';
import '../kit/kit.dart';
import '../sections/section_page.dart';
import '../automations/confirm_action.dart';
import 'library_place.dart' show LibraryTabs;

/// The Skills tab of Library (F7 decision 5): the installed skills of one
/// scope. Adding one goes through a preview, and only the preview's Confirm
/// installs it.
class SkillsTab extends StatelessWidget {
  const SkillsTab({super.key, required this.model, this.tabs, this.state});

  final SkillsModel model;
  final LibraryTabs? tabs;
  final StateKind? state;

  @override
  Widget build(BuildContext context) {
    return SectionPage<SkillEntry>(
      title: 'Skills',
      subtitle: 'Instruction sets an agent can use, in this scope',
      model: model,
      tabs: tabs?.items,
      tabIndex: tabs?.index ?? 0,
      onTab: tabs?.onChanged,
      state: state,
      readOnly: true,
      createLabel: 'Add a skill',
      blank: SkillsModel.blank,
      detailTitle: (skill) => skill.name.isEmpty ? 'Add a skill' : skill.name,
      emptyMessage:
          'A skill you add appears here. Nothing is installed until its '
          'preview is confirmed.',
      row: (skill, onTap) => RowItem(
        name: skill.name,
        description: skill.description,
        value: '${skill.scope} · ${skill.risk} risk',
        onTap: onTap,
      ),
      detail: (context, skill) => skill.name.isEmpty
          ? _Install(model: model)
          : _Installed(model: model, skill: skill),
    );
  }
}

/// The install form: a source, its preview, and then Confirm or Discard.
class _Install extends StatefulWidget {
  const _Install({required this.model});

  final SkillsModel model;

  @override
  State<_Install> createState() => _InstallState();
}

class _InstallState extends State<_Install> {
  final _source = TextEditingController();
  SkillPreview? _preview;
  var _busy = false;
  String? _error;

  @override
  void dispose() {
    _source.dispose();
    super.dispose();
  }

  Future<void> _stage() async {
    final source = _source.text.trim();
    if (source.isEmpty) {
      setState(() => _error = 'Name a source first.');
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
      _preview = null;
    });
    try {
      final preview = await widget.model.preview(source);
      if (!mounted) return;
      setState(() {
        _busy = false;
        _preview = preview;
      });
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _busy = false;
        _error = describeFailure(error);
      });
    }
  }

  Future<void> _confirm() async {
    final preview = _preview!;
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await widget.model.confirm(preview);
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _busy = false;
        _error = describeFailure(error);
      });
      return;
    }
    if (!mounted) return;
    Navigator.of(context).maybePop();
  }

  Future<void> _discard() async {
    final preview = _preview!;
    try {
      await widget.model.discard(preview);
    } catch (error) {
      if (!mounted) return;
      setState(() => _error = describeFailure(error));
      return;
    }
    if (!mounted) return;
    setState(() => _preview = null);
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final preview = _preview;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text(
          'A skill is previewed before it is installed: its name, what it is '
          'for and its risk are shown, and nothing is installed until you '
          'confirm them.',
          style: e.small.tint(e.muted),
        ),
        const SizedBox(height: Space.s16),
        Field(
          label: 'Source',
          controller: _source,
          hint: 'github.com/acme/skills/recovery',
        ),
        const SizedBox(height: Space.s12),
        Align(
          alignment: Alignment.centerLeft,
          child: Btn(
            _busy ? 'Reading' : 'Preview',
            onTap: _busy ? null : _stage,
          ),
        ),
        if (preview != null) ...[
          const SizedBox(height: Space.s20),
          _Fact(label: 'Name', value: preview.name),
          _Fact(label: 'What it is for', value: preview.description),
          _Fact(label: 'Risk', value: preview.risk),
          _Fact(label: 'Source', value: preview.source),
          const SizedBox(height: Space.s12),
          Wrap(
            spacing: Space.s8,
            runSpacing: Space.s8,
            children: [
              Btn.primary(
                _busy ? 'Installing' : 'Confirm',
                onTap: _busy ? null : _confirm,
              ),
              Btn('Discard', onTap: _busy ? null : _discard),
            ],
          ),
        ],
        if (_error != null) ...[
          const SizedBox(height: Space.s12),
          Text(_error!, style: e.small.tint(e.err)),
        ],
      ],
    );
  }
}

/// An installed skill: what it is, and removing it behind a confirmation.
class _Installed extends StatefulWidget {
  const _Installed({required this.model, required this.skill});

  final SkillsModel model;
  final SkillEntry skill;

  @override
  State<_Installed> createState() => _InstalledState();
}

class _InstalledState extends State<_Installed> {
  var _confirming = false;
  String? _error;

  Future<void> _remove() async {
    setState(() => _error = null);
    try {
      await widget.model.remove(widget.skill);
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
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _Fact(label: 'What it is for', value: widget.skill.description),
        _Fact(label: 'Risk', value: widget.skill.risk),
        _Fact(label: 'Scope', value: widget.skill.scope),
        if (_error != null) ...[
          const SizedBox(height: Space.s12),
          Text(_error!, style: e.small.tint(e.err)),
        ],
        const SizedBox(height: Space.s20),
        if (_confirming)
          ConfirmAction(
            question: 'Remove ${widget.skill.name}?',
            loss:
                'The skill is deleted from this scope; a copy elsewhere stays.',
            confirmLabel: 'Remove it',
            onConfirm: _remove,
            onCancel: () => setState(() => _confirming = false),
          )
        else
          Align(
            alignment: Alignment.centerLeft,
            child: Btn(
              'Remove this skill',
              onTap: () => setState(() => _confirming = true),
            ),
          ),
      ],
    );
  }
}

/// One labelled fact of a read-only detail.
class _Fact extends StatelessWidget {
  const _Fact({required this.label, required this.value});

  final String label;
  final String value;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Padding(
      padding: const EdgeInsets.only(bottom: Space.s8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(label, style: e.small.tint(e.muted)),
          const SizedBox(height: Space.s2),
          Text(value, style: e.body),
        ],
      ),
    );
  }
}
