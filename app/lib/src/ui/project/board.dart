import '../../model/work_item_model.dart';
import '../kit/kit.dart';
import 'status.dart';

/// A project's work items in the factory's four stages (F6 decision 12): one
/// column per stage, left to right, stacked under the reading width.
///
/// A column is a fact, not a control: each item is the kit's [ThreadRow], the
/// same row every list of threads draws, and choosing one opens its thread.
/// The board takes the items the factory store holds, read as views; it never
/// reads a store or a wire type itself (design-system.md §5).
class ProjectBoard extends StatelessWidget {
  const ProjectBoard({super.key, required this.items, this.onOpen});

  /// The project's work items, in the bridge's own order.
  final List<WorkItemView> items;

  /// Opens one item's thread. Null leaves the rows as facts.
  final void Function(WorkItemView item)? onOpen;

  @override
  Widget build(BuildContext context) {
    final staged = <Stage, List<WorkItemView>>{
      for (final stage in Stage.values) stage: <WorkItemView>[],
    };
    for (final item in items) {
      staged[item.stage]!.add(item);
    }
    final columns = [
      for (final stage in Stage.values)
        _Column(
          key: ValueKey(stage),
          stage: stage,
          items: staged[stage]!,
          onOpen: onOpen,
        ),
    ];
    return LayoutBuilder(
      builder: (context, constraints) {
        // Below the reading width the four columns are one under another: a
        // board that scrolled sideways would break §4.
        if (constraints.maxWidth < Layout.reading) {
          return Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              for (final (index, column) in columns.indexed) ...[
                if (index > 0) const SizedBox(height: Space.s16),
                column,
              ],
            ],
          );
        }
        return Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            for (final (index, column) in columns.indexed) ...[
              if (index > 0) const SizedBox(width: Space.s12),
              Expanded(child: column),
            ],
          ],
        );
      },
    );
  }
}

/// One stage of the board.
class _Column extends StatelessWidget {
  const _Column({
    super.key,
    required this.stage,
    required this.items,
    required this.onOpen,
  });

  final Stage stage;
  final List<WorkItemView> items;
  final void Function(WorkItemView item)? onOpen;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Label(_stageWord(stage), count: items.length),
        const SizedBox(height: Space.s8),
        if (items.isEmpty)
          Padding(
            padding: const EdgeInsets.symmetric(vertical: Space.s6),
            child: Text(
              'Nothing in ${_stageWord(stage).toLowerCase()}.',
              style: e.small.tint(e.muted),
            ),
          )
        else
          Box.flush(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                for (final (index, item) in items.indexed) ...[
                  if (index > 0) const Hairline(),
                  ThreadRow(
                    status: threadStatusOf(item.status),
                    title: item.title,
                    kind: ThreadKind.workItem,
                    onTap: onOpen == null ? null : () => onOpen!(item),
                  ),
                ],
              ],
            ),
          ),
      ],
    );
  }

  /// The stage in the design system's words, as the strip names it.
  static String _stageWord(Stage stage) => switch (stage) {
    Stage.plan => 'Plan',
    Stage.implement => 'Implement',
    Stage.review => 'Review',
    Stage.ready => 'Ready',
  };
}
