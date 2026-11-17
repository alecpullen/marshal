import '../../model/general_model.dart';
import '../kit/kit.dart';
import '../sections/section_page.dart';

/// Settings' General section (F7 decision 7): appearance, the density a
/// transcript opens as, the keyboard map, and the local daemon's state.
///
/// Each row is a setting; its detail either offers the fixed set of choices
/// (appearance, density) or states what it is (the key map, the daemon).
class GeneralSection extends StatelessWidget {
  const GeneralSection({super.key, required this.model, this.state});

  final GeneralModel model;

  /// The state the shell knows, when it knows more than the model does.
  final StateKind? state;

  @override
  Widget build(BuildContext context) {
    return SectionPage<GeneralSetting>(
      title: 'General',
      subtitle: 'This window, its keys, and the local daemon',
      model: model,
      state: state,
      emptyMessage: 'Nothing to set here.',
      row: (setting, onTap) => RowItem(
        name: setting.name,
        description: setting.description,
        value: setting.offersChoices ? setting.chosenLabel : setting.value,
        onTap: onTap,
      ),
      detail: (context, setting) =>
          _SettingDetail(model: model, setting: setting),
    );
  }
}

/// One setting's detail: the choices with the current one marked, or the
/// facts it reports, and the note that says where the setting lives.
class _SettingDetail extends StatelessWidget {
  const _SettingDetail({required this.model, required this.setting});

  final GeneralModel model;
  final GeneralSetting setting;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final note = setting.note;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        if (setting.offersChoices)
          Rows([
            for (final choice in setting.choices)
              RowItem(
                name: choice.label,
                description: choice.about,
                value: choice.value == setting.value ? 'Current' : null,
                selected: choice.value == setting.value,
                onTap: () => model.change(setting.chosen(choice.value)),
              ),
          ])
        else
          Box(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [for (final line in setting.lines) _Line(line)],
            ),
          ),
        if (note != null) ...[
          const SizedBox(height: Space.s12),
          Text(note, style: e.small.tint(e.muted)),
        ],
      ],
    );
  }
}

/// One term and what it means, as the key map and the daemon report them.
class _Line extends StatelessWidget {
  const _Line(this.line);

  final GeneralLine line;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Padding(
      padding: const EdgeInsets.only(bottom: Space.s12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            line.term,
            style: line.term.length <= 4 ? e.mono : e.small.tint(e.muted),
          ),
          const SizedBox(height: Space.s2),
          Text(line.meaning, style: e.body),
        ],
      ),
    );
  }
}
