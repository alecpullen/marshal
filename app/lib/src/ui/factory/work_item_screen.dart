import '../../model/age.dart';
import '../../model/pulls_model.dart';
import '../../model/thread_ref.dart';
import '../../model/work_item_model.dart';
import '../kit/kit.dart';
import '../pulls/pull_facts.dart';
import 'agents_tab.dart';
import 'item_asks.dart';
import 'checks_tab.dart';
import 'plan_tab.dart';
import 'recovery_form.dart';
import 'pulls_tab.dart';

/// A work item's thread (F6 decisions 4–6): its stage, what it waits on, the
/// pull request it reached, its activity, and its five inspector tabs.
///
/// It is the item's activity rather than a transcript: the planner, the
/// implementer, the reviewer and the user are named in words, one entry per
/// thing that happened. The asks it is blocked on are pinned above it, and the
/// tabs read its plan, its workers, its checks, the change on its branch and
/// its pull requests.
///
/// The screen takes a [WorkItemView] and callbacks and never fetches (§5):
/// the store it projects from, the pull requests and the change tab are the
/// host's.
///
/// Its order is deliberate: the asks it is blocked on come first, then the
/// tabs that read its plan, workers, checks, change and pull requests, and
/// its history last. What needs a person, and what they might revise, are not
/// buried under the timeline.
class WorkItemScreen extends StatefulWidget {
  const WorkItemScreen({
    super.key,
    required this.item,
    this.state,
    this.now,
    this.pulls = const [],
    this.changes,
    this.center,
    this.onBack,
    this.onRetry,
    this.conflict,
    this.onApprove,
    this.onAnswer,
    this.onRecover,
    this.onComplete,
    this.onMergeAndComplete,
    this.onReopen,
    this.onStop,
    this.onOpenWorker,
    this.onProposeChange,
    this.pullActionsFor,
  });

  final WorkItemView item;

  /// The store's data state, when it is not ready (§7).
  final StateKind? state;

  /// What a relative age is measured against.
  final DateTime? now;

  /// The pull request cards this item's thread shows, from the pulls model.
  final List<PullView> pulls;

  /// F4's Changes tab, for the item's branch. Null says the change is not
  /// being read rather than drawing an empty one.
  final Widget? changes;

  /// The worker's own conversation, when one is open: the centre swaps for it
  /// and [onBack] returns to the activity (F6 decision 6).
  final Widget? center;

  /// Returns from a worker's conversation to the item's activity.
  final VoidCallback? onBack;

  final VoidCallback? onRetry;

  /// The bridge's refusal of the last action, when it was a version
  /// conflict: the item moved, and the action is still available.
  final String? conflict;

  /// Approves a draft and starts it.
  final Future<void> Function()? onApprove;

  /// Answers the decision with the person's words.
  final Future<void> Function(String answer)? onAnswer;

  /// Recovers the item with the choice its form collected.
  final Future<void> Function(RecoveryChoice choice)? onRecover;

  /// Says the work counts as done.
  final Future<void> Function()? onComplete;

  /// Merges the reviewed pull request and completes the item.
  final Future<void> Function()? onMergeAndComplete;

  /// Reopens a completed item.
  final Future<void> Function()? onReopen;

  /// Stops the work, after the confirmation the header's menu opens.
  final Future<void> Function()? onStop;

  /// Opens a worker's own conversation (F6 plan 1, task 5).
  final void Function(WorkItemWorker worker)? onOpenWorker;

  /// Opens the revision sheet (F6 plan 1, task 5).
  final VoidCallback? onProposeChange;

  /// What each pull request card's buttons do.
  final PrActions Function(BuildContext context, PullView pull)? pullActionsFor;

  @override
  State<WorkItemScreen> createState() => _WorkItemScreenState();
}

class _WorkItemScreenState extends State<WorkItemScreen> {
  int _tab = 0;

  /// What the person has typed on an ask and what is in flight. It lives here
  /// rather than in the cards so a poll, or a worker drill-in that swaps the
  /// body out and back, does not lose it.
  final _askDraft = WorkItemAskDraft();

  @override
  void dispose() {
    _askDraft.dispose();
    super.dispose();
  }

  static const _tabs = ['Plan', 'Agents', 'Checks', 'Changes', 'Pull requests'];

  DateTime get _now => widget.now ?? DateTime.now();

  @override
  Widget build(BuildContext context) {
    final item = widget.item;
    final state = widget.state;
    final center = widget.center;
    if (center != null) {
      return Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(
              Layout.pageSide,
              Layout.pageTop,
              Layout.pageSide,
              Space.s8,
            ),
            child: Align(
              alignment: Alignment.centerLeft,
              child: Btn(
                'Back to activity',
                icon: Glyphs.chevron_left,
                onTap: widget.onBack,
              ),
            ),
          ),
          Expanded(child: center),
        ],
      );
    }
    if (state != null && state != StateKind.stale) {
      return PageBody(title: item.title, children: [_stateMessage(state)]);
    }
    return PageBody(
      title: item.title,
      subtitle: item.stageWords,
      actions: [
        if (widget.onStop != null)
          Menu(
            choices: [
              MenuChoice(
                'Stop the work',
                'It keeps what it produced',
                onSelect: () {
                  _confirmStop(context);
                },
              ),
            ],
            anchor: (context, toggle) => IconBtn(
              Glyphs.ellipsis,
              tooltip: 'More actions',
              onTap: toggle,
            ),
          ),
      ],
      children: [
        if (state == StateKind.stale) ...[
          StateMessage.stale(
            age: relativeAge(item.lastActive, _now),
            detail: 'The factory has not answered; this is what it last said.',
          ),
          const SizedBox(height: Space.s16),
        ],
        StageStrip(
          labels: const ['Plan', 'Implement', 'Review', 'Ready'],
          current: item.stage.index,
          status: _status(item),
          endWord: item.endWord,
        ),
        if (item.asks.isNotEmpty) ...[
          const SizedBox(height: Space.s16),
          ItemAsks(
            item: item,
            now: _now,
            draft: _askDraft,
            conflict: widget.conflict,
            onApprove: widget.onApprove,
            onAnswer: widget.onAnswer,
            onRecover: widget.onRecover,
            onComplete: widget.onComplete,
            onMergeAndComplete: widget.onMergeAndComplete,
            onReopen: widget.onReopen,
            onProposeChange: widget.onProposeChange,
          ),
        ],
        if (widget.pulls.isNotEmpty) ...[
          const SizedBox(height: Space.s12),
          PrCard(
            pull: pullFactsOf(widget.pulls.first, now: _now),
            actions:
                widget.pullActionsFor?.call(context, widget.pulls.first) ??
                const PrActions(),
          ),
        ],
        const SizedBox(height: Space.s16),
        Tabs(
          tabs: [for (final label in _tabs) TabItem(label)],
          index: _tab,
          onChanged: (index) => setState(() => _tab = index),
        ),
        const SizedBox(height: Space.s12),
        _tabContent(context),
        const SizedBox(height: Space.s16),
        Label('Activity', count: item.activity.length),
        const SizedBox(height: Space.s4),
        _activity(),
      ],
    );
  }

  /// The item's history, oldest first: a timeline with its own end.
  Widget _activity() {
    final entries = widget.item.activity;
    if (entries.isEmpty) {
      final e = Ember.of(context);
      return Text('Nothing has happened yet.', style: e.body.tint(e.muted));
    }
    return Box.flush(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          for (final (index, entry) in entries.indexed) ...[
            if (index > 0) const Hairline(),
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: Space.s16),
              child: _activityRow(entry),
            ),
          ],
        ],
      ),
    );
  }

  /// One entry, with its age relative to now and the exact time behind it.
  /// A time this build cannot read says so rather than claiming an epoch.
  Widget _activityRow(ActivityEntry entry) {
    final at = entry.at;
    final age = at == null ? 'Unknown' : relativeAge(at, _now);
    return ActivityRow(
      who: entry.who,
      text: entry.text,
      age: age,
      ageTip: at == null ? null : exactTimeLabel(at),
      detail: entry.detail,
    );
  }

  Widget _tabContent(BuildContext context) => switch (_tab) {
    0 => PlanTab(item: widget.item, onProposeChange: widget.onProposeChange),
    1 => AgentsTab(item: widget.item, onOpen: widget.onOpenWorker),
    2 => ChecksTab(item: widget.item),
    3 =>
      widget.changes ??
          StateMessage.unavailable(
            host: widget.item.host,
            lastSeen: 'now',
            // Only when there is a read to retry: a control that would do
            // nothing is not drawn (§7).
            action: widget.onRetry == null
                ? null
                : StateAction(
                    'Read the item again',
                    onTap: () => widget.onRetry?.call(),
                  ),
          ),
    _ => PullsTab(
      pulls: widget.pulls,
      now: _now,
      actionsFor: widget.pullActionsFor,
    ),
  };

  /// The item's status in the kit's vocabulary.
  static Status _status(WorkItemView item) => switch (item.status) {
    ThreadStatus.needsYou => Status.needsYou,
    ThreadStatus.running => Status.running,
    ThreadStatus.ready => Status.ready,
    ThreadStatus.failed => Status.failed,
    ThreadStatus.idle => Status.idle,
  };

  /// Stops the work after confirming it: the run ends, and what it produced
  /// stays. It is destructive, so the sheet names what is lost.
  Future<void> _confirmStop(BuildContext context) async {
    final stop = await Sheet.show<bool>(
      context,
      Sheet(
        title: 'Stop the work?',
        actions: [
          Btn.primary(
            'Stop the work',
            onTap: () => Navigator.maybePop(context, true),
          ),
          Btn(
            'Keep it running',
            onTap: () => Navigator.maybePop(context, false),
          ),
        ],
        child: Text(
          'The run ends here. The branch, the commits and the pull request '
          'stay, and you can reopen the item later.',
          style: Ember.of(context).body,
        ),
      ),
    );
    if (stop != true) return;
    await widget.onStop?.call();
  }

  Widget _stateMessage(StateKind state) {
    final retry = StateAction(
      'Read the factory again',
      onTap: widget.onRetry ?? () {},
    );
    return switch (state) {
      StateKind.loading => const StateMessage.loading(lines: 4),
      StateKind.empty => StateMessage.empty(
        message: 'No work item here: it may have been closed elsewhere.',
        action: retry,
      ),
      StateKind.error => StateMessage.error(
        message: 'The work item could not be read.',
        retry: retry,
      ),
      StateKind.unavailable => StateMessage.unavailable(
        host: widget.item.host,
        lastSeen: 'now',
        action: retry,
      ),
      StateKind.stale => const StateMessage.stale(age: 'now'),
      StateKind.planned => StateMessage.planned(
        name: 'Work item',
        note: 'Not built yet',
      ),
    };
  }
}
