import '../kit/kit.dart';

/// A destructive action's confirmation, drawn where the action was
/// (design-system.md §9: a destructive action is plain and confirms, and the
/// confirmation names what will be lost).
///
/// It is in place rather than in a second [Sheet], because one screen shows
/// one sheet at most (§5) and the action already lives inside the detail.
class ConfirmAction extends StatelessWidget {
  const ConfirmAction({
    super.key,
    required this.question,
    required this.loss,
    required this.confirmLabel,
    required this.onConfirm,
    required this.onCancel,
  });

  /// What is being done, as a question: "Delete nightly gate?".
  final String question;

  /// What it costs, in one sentence.
  final String loss;

  /// The button that does it. A verb, and never the primary action of its
  /// group: the safe choice stays first.
  final String confirmLabel;

  final VoidCallback onConfirm;
  final VoidCallback onCancel;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Box(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(question, style: e.bodyStrong),
          const SizedBox(height: Space.s4),
          Text(loss, style: e.small.tint(e.muted)),
          const SizedBox(height: Space.s12),
          Wrap(
            spacing: Space.s8,
            runSpacing: Space.s8,
            children: [
              Btn('Keep it', onTap: onCancel),
              Btn(confirmLabel, onTap: onConfirm),
            ],
          ),
        ],
      ),
    );
  }
}
