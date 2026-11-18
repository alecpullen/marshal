import '../../model/pulls_model.dart';
import '../kit/kit.dart';
import '../pulls/pull_facts.dart';

/// The work item's Pull requests tab (F6 decision 6): a card for every pull
/// request the factory recorded for it, in F5's own card and in the item's own
/// order. All of them are shown, not only the one under review: none is
/// dropped, filtered or reordered, because an item can reach several.
///
/// The card is the only way a pull request is shown (§5), so the tab draws
/// what the pulls model holds rather than describing the item's own copy: each
/// card reads its own checks and says what they say now, which the item's
/// snapshot cannot.
class PullsTab extends StatelessWidget {
  const PullsTab({
    super.key,
    required this.pulls,
    required this.now,
    this.actionsFor,
  });

  final List<PullView> pulls;

  /// What a relative age is measured against.
  final DateTime now;

  /// What each card's buttons do. Null draws them disabled, as every screen
  /// does without a host.
  final PrActions Function(BuildContext context, PullView pull)? actionsFor;

  @override
  Widget build(BuildContext context) {
    if (pulls.isEmpty) {
      return StateMessage.empty(
        message:
            'No pull request yet: one appears once the work reaches a push.',
        action: StateAction('Nothing to do', onTap: () {}),
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Label('Pull requests', count: pulls.length),
        const SizedBox(height: Space.s8),
        for (final pull in pulls)
          Padding(
            padding: const EdgeInsets.only(bottom: Space.s12),
            child: PrCard(
              pull: pullFactsOf(pull, now: now),
              actions: actionsFor?.call(context, pull) ?? const PrActions(),
            ),
          ),
      ],
    );
  }
}
