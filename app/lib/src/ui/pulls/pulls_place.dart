import '../../model/pulls_model.dart';
import '../kit/kit.dart';
import 'pull_facts.dart';

/// The Pull requests place (F5 decision 10): every open pull request Marshal
/// produced, grouped by what its checks say, in the spec's order — ready for
/// review, checks failing, being fixed, in progress, checks unavailable.
///
/// It draws the groups it is given and asks its host for a card's actions; it
/// never fetches (design-system.md §5). An empty group is not drawn: a heading
/// with nothing under it says nothing.
class PullsPlace extends StatelessWidget {
  const PullsPlace({
    super.key,
    required this.groups,
    required this.now,
    this.actionsFor,
    this.state,
    this.onRetry,
  });

  /// The views by group, in [PullGroup] order.
  final Map<PullGroup, List<PullView>> groups;

  /// What a relative age is measured against.
  final DateTime now;

  /// What each card's buttons do. Null draws them disabled, which is how the
  /// enforcement tests pump the place without a bridge.
  final PrActions Function(BuildContext context, PullView pull)? actionsFor;

  /// The fleet's data state, when it is not ready (§7).
  final StateKind? state;

  /// Reads the fleet again, for the one action a state offers.
  final VoidCallback? onRetry;

  /// What each group is called (decision 10's own words).
  static const labels = {
    PullGroup.ready: 'Ready for review',
    PullGroup.failing: 'Checks failing',
    PullGroup.beingFixed: 'Being fixed by a worker',
    PullGroup.inProgress: 'In progress',
    PullGroup.unavailable: 'Checks unavailable',
  };

  /// The views of one group, empty when the model holds none.
  List<PullView> _in(PullGroup group) => groups[group] ?? const [];

  /// Whether the model holds no pull request at all.
  bool get _isEmpty => PullGroup.values.every((group) => _in(group).isEmpty);

  @override
  Widget build(BuildContext context) {
    final state = this.state;
    if (state != null && state != StateKind.stale) {
      return PageBody(title: 'Pull requests', children: [_stateMessage(state)]);
    }
    return PageBody(
      title: 'Pull requests',
      subtitle: _subtitle(),
      width: PageWidth.list,
      children: [
        if (state == StateKind.stale) ...[
          StateMessage.stale(
            age: 'now',
            detail: 'The fleet has not answered; this is what it last said.',
          ),
          const SizedBox(height: Space.s16),
        ],
        if (_isEmpty)
          _empty()
        else
          for (final group in PullGroup.values) ..._group(context, group),
      ],
    );
  }

  /// One group under its label and count, or nothing when it has none.
  List<Widget> _group(BuildContext context, PullGroup group) {
    final views = _in(group);
    if (views.isEmpty) return const [];
    return [
      Label(labels[group]!, count: views.length),
      const SizedBox(height: Space.s8),
      for (final view in views)
        Padding(
          padding: const EdgeInsets.only(bottom: Space.s12),
          child: PrCard(
            pull: pullFactsOf(view, now: now),
            actions: actionsFor?.call(context, view) ?? const PrActions(),
          ),
        ),
      const SizedBox(height: Space.s8),
    ];
  }

  Widget _empty() => StateMessage.empty(
    message:
        'No open pull request yet: one appears here once an agent, a work '
        'item or an automation ships one.',
    action: StateAction('Read the fleet again', onTap: onRetry ?? () {}),
  );

  /// How much is where, in the design system's words. A group with none is
  /// left out, so the line is what is true rather than a template.
  String _subtitle() {
    final parts = [
      if (_in(PullGroup.ready).isNotEmpty)
        '${_in(PullGroup.ready).length} ready for review',
      if (_in(PullGroup.failing).isNotEmpty)
        '${_in(PullGroup.failing).length} with failing checks',
      if (_in(PullGroup.beingFixed).isNotEmpty)
        '${_in(PullGroup.beingFixed).length} being fixed',
      if (_in(PullGroup.inProgress).isNotEmpty)
        '${_in(PullGroup.inProgress).length} in progress',
      if (_in(PullGroup.unavailable).isNotEmpty)
        '${_in(PullGroup.unavailable).length} with checks unavailable',
    ];
    return parts.isEmpty ? 'No open pull requests' : parts.join(' · ');
  }

  Widget _stateMessage(StateKind state) {
    final retry = StateAction('Read the fleet again', onTap: onRetry ?? () {});
    return switch (state) {
      StateKind.loading => const StateMessage.loading(lines: 4),
      StateKind.empty => StateMessage.empty(
        message:
            'No open pull request yet: one appears here once an agent, a work '
            'item or an automation ships one.',
        action: retry,
      ),
      StateKind.error => StateMessage.error(
        message: 'The pull requests could not be read.',
        retry: retry,
      ),
      StateKind.unauthorized => const StateMessage.unauthorized(),
      StateKind.unavailable => StateMessage.unavailable(
        host: 'The daemon',
        lastSeen: '4m',
        action: retry,
      ),
      StateKind.stale => const StateMessage.stale(age: '4m'),
      StateKind.planned => const StateMessage.planned(
        name: 'Pull requests',
        note: 'Not built yet',
      ),
    };
  }
}
