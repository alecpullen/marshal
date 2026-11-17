import '../../model/work_item_model.dart';
import '../kit/kit.dart';

/// The work item's Agents tab (F6 decision 6): each worker the factory
/// started, with its role, what it is doing, and the way into its own
/// conversation.
///
/// Opening one is the host's: the row calls [onOpen] with the worker, and the
/// screen behind it swaps the centre for that agent's thread. A host that
/// passes none draws the workers as facts, which is what the read-only thread
/// needs.
class AgentsTab extends StatelessWidget {
  const AgentsTab({super.key, required this.item, this.onOpen});

  final WorkItemView item;

  /// Opens a worker's conversation. Null leaves the rows closed.
  final void Function(WorkItemWorker worker)? onOpen;

  @override
  Widget build(BuildContext context) {
    final open = onOpen;
    if (item.workers.isEmpty) {
      return StateMessage.empty(
        message:
            'No worker yet: the factory starts one when the plan is approved.',
        action: StateAction('Nothing to do', onTap: () {}),
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Label('Agents', count: item.workers.length),
        const SizedBox(height: Space.s6),
        Rows([
          for (final worker in item.workers)
            RowItem(
              name: worker.words,
              description: worker.parked ? 'Parked' : 'Working',
              value: worker.agentID,
              mono: true,
              onTap: open == null ? null : () => open(worker),
            ),
        ]),
      ],
    );
  }
}
