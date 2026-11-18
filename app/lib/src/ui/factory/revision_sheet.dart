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
/// the state that knows. Text that was written and never sent is not thrown
/// away by a close, a barrier tap or Escape: the sheet asks first, in place
/// (design-system.md §9).
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

  /// Whether the sheet is asking before it discards what was written.
  bool _confirmDiscard = false;

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
    // Leaving with a proposal that was never sent loses it, so every way out
    // of the sheet — its Close, the barrier, Escape — asks in place first.
    // A send in flight is not a loss and is not interrupted.
    return PopScope<bool>(
      canPop: !widget.model.changed && !widget.model.sending,
      onPopInvokedWithResult: (didPop, result) {
        if (didPop) return;
        if (widget.model.sending || !widget.model.changed) return;
        setState(() => _confirmDiscard = true);
      },
      child: _confirmDiscard ? _discard(context) : _editor(context),
    );
  }

  /// The proposal as it is written: the plan in force, the field, and the one
  /// action that sends it.
  Widget _editor(BuildContext context) {
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
            Btn('Cancel', onTap: model.sending ? null : () => _cancel(context)),
          ],
        ),
      ],
    );
  }

  /// Asks, in this sheet rather than a second one, before the proposal is
  /// lost. It names what discarding costs and offers the way back first.
  Widget _discard(BuildContext context) {
    final e = Ember.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text(
          'Your proposal has not been sent. Discarding it loses what you wrote.',
          style: e.body,
        ),
        const SizedBox(height: Space.s16),
        Wrap(
          spacing: Space.s8,
          runSpacing: Space.s8,
          children: [
            Btn.primary(
              'Keep writing',
              onTap: () => setState(() => _confirmDiscard = false),
            ),
            Btn(
              'Discard the proposal',
              onTap: () => Navigator.of(context).pop(false),
            ),
          ],
        ),
      ],
    );
  }

  /// Leaves the sheet, or asks first when there is text that would be lost.
  void _cancel(BuildContext context) {
    final model = widget.model;
    if (model.sending) return;
    if (model.changed) {
      setState(() => _confirmDiscard = true);
      return;
    }
    Navigator.maybePop(context);
  }

  Future<void> _send(BuildContext context) async {
    await widget.model.send();
    if (!context.mounted) return;
    if (widget.model.error == null) {
      // A sent proposal closes the sheet without the discard question: what
      // was typed went out, so nothing is lost, and a pop is never vetoed by
      // the confirmation above.
      final navigator = Navigator.of(context);
      if (navigator.canPop()) navigator.pop(true);
    }
  }
}
