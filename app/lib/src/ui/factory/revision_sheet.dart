import '../../model/revision_model.dart';
import '../kit/kit.dart';

/// Opens the plan-revision sheet (F6 decision 6): the approved plan against
/// the proposed one, and the one action that sends it.
///
/// It answers true when a proposal was sent, so the caller can tell a
/// dismissal from a change.
Future<bool?> showRevisionSheet(BuildContext context, RevisionModel model) =>
    Sheet.show<bool>(
      context,
      Sheet(
        title: 'Propose a change',
        child: RevisionSheet(model: model),
      ),
    );

/// The proposal, shown against what is in force.
///
/// It is the sheet's body rather than the sheet itself: its actions depend on
/// what has been typed and on whether a send is in flight, so they live with
/// the state that knows.
class RevisionSheet extends StatefulWidget {
  const RevisionSheet({super.key, required this.model});

  final RevisionModel model;

  @override
  State<RevisionSheet> createState() => _RevisionSheetState();
}

class _RevisionSheetState extends State<RevisionSheet> {
  late final TextEditingController _plan = TextEditingController(
    text: widget.model.proposal,
  );

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_changed);
  }

  @override
  void dispose() {
    widget.model.removeListener(_changed);
    _plan.dispose();
    super.dispose();
  }

  void _changed() {
    if (mounted) setState(() {});
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final model = widget.model;
    final error = model.error;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text(
          'The workers keep the revision they were started under until this '
          'one is authorized.',
          style: e.small.tint(e.muted),
        ),
        const SizedBox(height: Space.s12),
        const Label('Approved'),
        const SizedBox(height: Space.s6),
        OutputBlock(text: model.approved.trim()),
        const SizedBox(height: Space.s12),
        Field.multiline(
          label: 'Proposed',
          controller: _plan,
          hint: 'What should change, in the plan’s own words',
          onChanged: model.change,
        ),
        if (error != null && error.isNotEmpty) ...[
          const SizedBox(height: Space.s8),
          Text(error, style: e.small.tint(e.err)),
        ],
        const SizedBox(height: Space.s16),
        Row(
          children: [
            Btn.primary(
              model.sending ? 'Sending…' : 'Send the proposal',
              onTap: model.canSend ? () => _send(context) : null,
            ),
            const SizedBox(width: Space.s8),
            Btn('Cancel', onTap: () => Navigator.maybePop(context)),
          ],
        ),
      ],
    );
  }

  Future<void> _send(BuildContext context) async {
    await widget.model.send();
    if (!context.mounted) return;
    if (widget.model.error == null) Navigator.maybePop(context, true);
  }
}
