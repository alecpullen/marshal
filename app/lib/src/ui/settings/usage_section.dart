import '../../model/usage_model.dart';
import '../kit/kit.dart';
import '../sections/section_page.dart';

/// Settings' Usage and audit section (F7 decision 7): cost and tokens by
/// project, role or model over a window, the audit log, and the disk with the
/// prune that frees it.
///
/// The chart above the rows is the design's second exception to the one
/// pattern (decision 8): one kit [Chart], `fg` for the single series, its bars
/// labelled directly.
class UsageSection extends StatelessWidget {
  const UsageSection({super.key, required this.model, this.state});

  final UsageModel model;

  /// The state the shell knows, when it knows more than the model does.
  final StateKind? state;

  @override
  Widget build(BuildContext context) {
    return SectionPage<UsageRow>(
      title: 'Usage and audit',
      subtitle: 'What the fleet spent, what it did, and the disk it took',
      model: model,
      state: state,
      readOnly: true,
      emptyMessage: 'No spend has been recorded yet.',
      header: [_SeriesChart(model: model)],
      row: (row, onTap) => RowItem(
        name: row.name,
        description: row.description,
        value: row.value,
        onTap: onTap,
      ),
      detail: (context, row) => _UsageDetail(model: model, row: row),
    );
  }
}

/// The window's series as one chart: cost per key, in `fg`, each bar labelled
/// with its own name and value.
class _SeriesChart extends StatelessWidget {
  const _SeriesChart({required this.model});

  final UsageModel model;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return AnimatedBuilder(
      animation: model,
      builder: (context, _) {
        final series = model.chartSeries;
        if (series.isEmpty) return const SizedBox.shrink();
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Text('Cost by ${model.by}', style: e.small.tint(e.muted)),
            const SizedBox(height: Space.s8),
            Chart(
              unit: 'usd',
              bars: [
                for (final (key, cost) in series)
                  ChartBar(label: key, value: cost),
              ],
            ),
          ],
        );
      },
    );
  }
}

/// One row's detail: the filter's choices, what a total or an event holds, or
/// the disk and its prune.
class _UsageDetail extends StatelessWidget {
  const _UsageDetail({required this.model, required this.row});

  final UsageModel model;
  final UsageRow row;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    switch (row.kind) {
      case UsageRowKind.filter:
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Text('Window', style: e.small.tint(e.muted)),
            const SizedBox(height: Space.s8),
            Rows([
              for (final (value, label) in usageRanges)
                RowItem(
                  name: label,
                  description: value == '7d'
                      ? 'A week of spend'
                      : 'A month of spend',
                  value: model.range == value ? 'Current' : null,
                  selected: model.range == value,
                  onTap: () => model.setRange(value),
                ),
            ]),
            const SizedBox(height: Space.s20),
            Text('Grouping', style: e.small.tint(e.muted)),
            const SizedBox(height: Space.s8),
            Rows([
              for (final (value, label) in usageGroupings)
                RowItem(
                  name: label,
                  description: 'One series entry per $value',
                  value: model.by == value ? 'Current' : null,
                  selected: model.by == value,
                  onTap: () => model.groupBy(value),
                ),
            ]),
            const SizedBox(height: Space.s20),
            Text('Audit kind', style: e.small.tint(e.muted)),
            const SizedBox(height: Space.s8),
            Rows([
              RowItem(
                name: 'Every kind',
                description: 'The whole audit log, newest first',
                value: model.auditKind.isEmpty ? 'Current' : null,
                selected: model.auditKind.isEmpty,
                onTap: () => model.filterAudit(''),
              ),
              for (final kind in model.auditKinds)
                RowItem(
                  name: kind,
                  description: 'Only $kind records',
                  value: model.auditKind == kind ? 'Current' : null,
                  selected: model.auditKind == kind,
                  onTap: () => model.filterAudit(kind),
                ),
            ]),
          ],
        );
      case UsageRowKind.disk:
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Box(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  for (final (term, meaning) in row.facts) _Fact(term, meaning),
                ],
              ),
            ),
            const SizedBox(height: Space.s16),
            Align(
              alignment: Alignment.centerLeft,
              child: Btn('Prune…', onTap: () => _prune(context)),
            ),
          ],
        );
      case UsageRowKind.spend:
      case UsageRowKind.series:
      case UsageRowKind.audit:
        return Box(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              for (final (term, meaning) in row.facts) _Fact(term, meaning),
            ],
          ),
        );
    }
  }

  /// Confirms the prune, naming what goes and what stays, and then runs it.
  Future<void> _prune(BuildContext context) async {
    final confirmed = await Sheet.show<bool>(
      context,
      Sheet(
        title: 'Prune the disk',
        actions: [Btn('Cancel', onTap: () => Navigator.maybePop(context))],
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text('Prune removes only what nothing refers to:'),
            const SizedBox(height: Space.s8),
            Text('· mirror checkouts no project names any more'),
            Text('· work directories whose agent is gone'),
            const SizedBox(height: Space.s8),
            Text('What a project, an agent or a pool still uses is kept.'),
            const SizedBox(height: Space.s16),
            Align(
              alignment: Alignment.centerLeft,
              child: Btn.primary(
                'Prune',
                onTap: () => Navigator.maybePop(context, true),
              ),
            ),
          ],
        ),
      ),
    );
    if (confirmed != true) return;
    await model.prune();
  }
}

/// One named fact of a total, a series entry or a disk.
class _Fact extends StatelessWidget {
  const _Fact(this.term, this.meaning);

  final String term;
  final String meaning;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Padding(
      padding: const EdgeInsets.only(bottom: Space.s12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(term, style: e.small.tint(e.muted)),
          const SizedBox(height: Space.s2),
          Text(meaning, style: e.body),
        ],
      ),
    );
  }
}
