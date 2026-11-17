import '../../model/work_item_model.dart';
import '../kit/kit.dart';

/// The work item's Checks tab (F6 decision 6): the CI observed on the current
/// head, and what the reviewer found.
///
/// The checks are the forge's own, in its order; a check whose conclusion has
/// not arrived is pending rather than failed. The review's findings are the
/// last round's, each with the file it names.
class ChecksTab extends StatelessWidget {
  const ChecksTab({super.key, required this.item});

  final WorkItemView item;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    if (!item.hasRound) {
      return StateMessage.empty(
        message: 'No check yet: they are observed once the first round runs.',
        action: StateAction('Nothing to do', onTap: () {}),
      );
    }
    final checks = [for (final check in item.checks) _check(check)];
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Label('Checks', count: checks.length),
        const SizedBox(height: Space.s6),
        if (checks.isEmpty)
          Text(
            'No check was observed on this head.',
            style: e.body.tint(e.muted),
          )
        else ...[
          CheckStrip(checks),
          const SizedBox(height: Space.s8),
          Rows([
            for (final check in checks)
              RowItem(
                name: check.name,
                description: check.state.word,
                value: check.state == CheckState.failed ? 'Failed' : null,
              ),
          ]),
        ],
        const SizedBox(height: Space.s12),
        Label('Review', count: item.findings.length),
        const SizedBox(height: Space.s6),
        if (item.findings.isEmpty)
          Text('The reviewer found nothing.', style: e.body.tint(e.muted))
        else
          Rows([
            for (final finding in item.findings)
              RowItem(name: finding.title, description: finding.detail),
          ]),
      ],
    );
  }

  /// One model check in the kit's vocabulary.
  static Check _check(WorkItemCheck check) =>
      Check(check.name, switch (check.state) {
        WorkItemCheckState.passed => CheckState.passed,
        WorkItemCheckState.failed => CheckState.failed,
        WorkItemCheckState.running => CheckState.running,
        WorkItemCheckState.pending => CheckState.pending,
      });
}
