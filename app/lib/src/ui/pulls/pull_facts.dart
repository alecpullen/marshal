import '../../model/age.dart';
import '../../model/pulls_model.dart';
import '../../model/thread_ref.dart' as model;
import '../kit/kit.dart';

/// Why a factory-origin pull request's Merge is drawn disabled here.
///
/// The factory's own `merge_and_complete` command names the pull request's
/// reviewed node id, and the bridge refuses a blank one
/// (web/bridge/factory_completion_batch.go); a [PullView] carries no node id,
/// because `contract.PullRecord` has none to carry. The reviewed identity lives
/// on the work item, so the merge is performed from its thread (F6), and this
/// card says where rather than offering a button that would fail. One sentence,
/// used by the card and by the thread's review head.
/// `test/ui/factory/item_asks_test.dart` covers the thread's own merge.
const workItemMergeNote =
    'Merge this from its work item: only the work item holds the reviewed '
    'pull request’s identity';

/// One pull request as a [PrCard] draws it: the facts a view holds, in the
/// kit's vocabulary.
///
/// It is the one place a record becomes a card, so the place, the review head
/// and the Inbox cannot spell one differently. The check summary is the
/// model's own sentence (F5 decision 11), never re-derived here.
///
/// It reads `model/` and never `client/`: `PullView` already speaks the
/// model's vocabulary, so a wire type reaches no screen (F1's boundary).
PullRequestFacts pullFactsOf(PullView view, {required DateTime now}) =>
    PullRequestFacts(
      number: view.number,
      title: view.title,
      repo: view.repo,
      branch: view.branch,
      base: view.base,
      adds: view.adds,
      dels: view.dels,
      checks: [
        for (final check in view.checks)
          Check(check.name, _checkOf(check.state)),
      ],
      reviews: view.reviews,
      age: relativeAge(view.updatedAt, now),
      state: _stateOf(view.group),
      draft: view.draft,
      failureTail: view.failureTail,
      checkSummary: view.checkSummary,
      sending: view.sending,
      dispatchError: view.error,
      sourceKind: _kindOf(view),
      sourceTitle: view.sourceTitle,
    );

/// One check in the kit's vocabulary.
CheckState _checkOf(PullCheckState state) => switch (state) {
  PullCheckState.passed => CheckState.passed,
  PullCheckState.failed => CheckState.failed,
  PullCheckState.running => CheckState.running,
  PullCheckState.pending => CheckState.pending,
};

/// A group is what the card shows; a state is how it draws it.
PrState _stateOf(PullGroup group) => switch (group) {
  PullGroup.ready => PrState.ready,
  PullGroup.failing => PrState.failing,
  PullGroup.beingFixed => PrState.fixing,
  PullGroup.inProgress => PrState.inProgress,
  PullGroup.unavailable => PrState.unavailable,
};

/// The kind of thread the card says it came from (design-system.md §6: told
/// apart by icon and tooltip, never by colour).
///
/// A record whose thread the fleet no longer lists still says where it came
/// from: a factory item is a work item, anything else an agent's. A plan run's
/// record names the agent that ran it.
ThreadKind _kindOf(PullView view) {
  final thread = view.thread;
  if (thread != null) {
    return switch (thread.kind) {
      model.ThreadKind.agent => ThreadKind.agent,
      model.ThreadKind.workItem => ThreadKind.workItem,
      model.ThreadKind.planRun => ThreadKind.planRun,
      model.ThreadKind.session => ThreadKind.session,
    };
  }
  return view.fromFactoryItem ? ThreadKind.workItem : ThreadKind.agent;
}
