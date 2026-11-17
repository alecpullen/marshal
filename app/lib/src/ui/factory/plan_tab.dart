import '../../model/work_item_model.dart';
import '../kit/kit.dart';

/// The work item's Plan tab (F6 decision 6): its goal and scope, the settings
/// its approved revision froze, in words, and its documents.
///
/// The plan and the specification are the exact bytes that were approved, so
/// what is read here is what the workers were given. "Propose a change" is
/// offered only when the host can take one; a host that passes none gets a
/// tab that reads and nothing that pretends to write.
class PlanTab extends StatelessWidget {
  const PlanTab({super.key, required this.item, this.onProposeChange});

  final WorkItemView item;

  /// Opens the revision sheet. Null leaves the action out.
  final VoidCallback? onProposeChange;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final plan = item.plan.trim();
    final spec = item.spec.trim();
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Row(
          children: [
            const Expanded(child: Label('Plan')),
            if (onProposeChange != null)
              Btn(
                'Propose a change',
                icon: Glyphs.pencil,
                onTap: onProposeChange,
              ),
          ],
        ),
        const SizedBox(height: Space.s6),
        if (plan.isEmpty)
          Text('No plan text.', style: e.body.tint(e.muted))
        else
          OutputBlock(text: plan),
        if (spec.isNotEmpty) ...[
          const SizedBox(height: Space.s12),
          const Label('Specification'),
          const SizedBox(height: Space.s6),
          Text(spec, style: e.small),
        ],
        const SizedBox(height: Space.s12),
        const Label('Settings'),
        const SizedBox(height: Space.s6),
        Rows([
          for (final setting in item.settings)
            RowItem(
              name: setting.name,
              description: setting.description,
              value: setting.value,
            ),
        ]),
        if (item.proposals.isNotEmpty) ...[
          const SizedBox(height: Space.s12),
          Label('Proposed changes', count: item.proposals.length),
          const SizedBox(height: Space.s6),
          for (final proposal in item.proposals)
            Padding(
              padding: const EdgeInsets.only(bottom: Space.s6),
              child: Text(proposal, style: e.small),
            ),
        ],
      ],
    );
  }
}
