import '../../model/secrets_model.dart';
import '../kit/kit.dart';
import '../sections/section_page.dart';

/// Settings' Secrets section (F7 decision 7): every reference the backend
/// holds, the credential that names each, and a value that is written and
/// never read back.
class SecretsSection extends StatelessWidget {
  const SecretsSection({super.key, required this.model, this.state});

  final SecretsModel model;

  /// The state the shell knows, when it knows more than the model does.
  final StateKind? state;

  @override
  Widget build(BuildContext context) {
    return SectionPage<SecretRow>(
      title: 'Secrets',
      subtitle: 'The values Marshal injects, and where each is used',
      model: model,
      state: state,
      createLabel: 'New secret',
      blank: SecretsModel.blank,
      detailTitle: (row) => row.ref.isEmpty ? 'New secret' : row.ref,
      emptyMessage: 'No secret is stored yet.',
      row: (row, onTap) => RowItem(
        name: row.name.isEmpty ? 'New secret' : row.name,
        description: row.description,
        value: row.value,
        onTap: onTap,
      ),
      detail: (context, row) => _SecretDetail(model: model, row: row),
    );
  }
}

/// One secret's detail: the reference it is stored under, and a value to
/// write. The value field starts empty every time, because a value cannot be
/// read back to show.
class _SecretDetail extends StatefulWidget {
  const _SecretDetail({required this.model, required this.row});

  final SecretsModel model;
  final SecretRow row;

  @override
  State<_SecretDetail> createState() => _SecretDetailState();
}

class _SecretDetailState extends State<_SecretDetail> {
  final _reference = TextEditingController();
  final _value = TextEditingController();

  @override
  void initState() {
    super.initState();
    _reference.text = widget.row.ref;
  }

  @override
  void dispose() {
    _reference.dispose();
    _value.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final row = widget.row;
    if (row.kind == SecretRowKind.status) {
      return Text(
        'The bridge keeps the values in its own backend. This row reports '
        'whether that backend answers; nothing here is editable.',
        style: e.body.tint(e.muted),
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        if (row.kind == SecretRowKind.newSecret) ...[
          Field(
            label: 'Reference',
            hint: 'forge/github-token',
            controller: _reference,
            onChanged: (text) => widget.model.change(row.withRef(text)),
          ),
          const SizedBox(height: Space.s16),
        ],
        Field(
          label: 'New value',
          hint: 'The value is written and never shown again',
          controller: _value,
          onChanged: (text) => widget.model.change(row.withValue(text)),
        ),
        const SizedBox(height: Space.s12),
        Text(
          'A value cannot be read back: the bridge answers references only, '
          'so writing a new one replaces what is there.',
          style: e.small.tint(e.muted),
        ),
      ],
    );
  }
}
