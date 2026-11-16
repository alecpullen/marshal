import '../../model/age.dart';
import '../../model/inbox_model.dart' as model;
import '../../model/pulls_model.dart';
import '../../model/thread_ref.dart';
import '../../model/work_item_model.dart';
import '../kit/kit.dart';
import '../pulls/pull_facts.dart';
import 'agents_tab.dart';
import 'checks_tab.dart';
import 'plan_tab.dart';
import 'pulls_tab.dart';

/// Answers an ask from the item's own thread. The reply is the person's words
/// for a decision that takes them; a task without one is a choice.
typedef AskHandler =
    void Function(model.Ask ask, model.AskAction action, {String? reply});

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
class WorkItemScreen extends StatefulWidget {
  const WorkItemScreen({
    super.key,
    required this.item,
    this.state,
    this.now,
    this.pulls = const [],
    this.changes,
    this.onRetry,
    this.onAnswer,
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

  final VoidCallback? onRetry;

  /// Answers one of the pinned asks. Null draws its actions disabled.
  final AskHandler? onAnswer;

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

  static const _tabs = ['Plan', 'Agents', 'Checks', 'Changes', 'Pull requests'];

  DateTime get _now => widget.now ?? DateTime.now();

  @override
  Widget build(BuildContext context) {
    final item = widget.item;
    final state = widget.state;
    if (state != null && state != StateKind.stale) {
      return PageBody(title: item.title, children: [_stateMessage(state)]);
    }
    return PageBody(
      title: item.title,
      subtitle: item.stageWords,
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
          for (final ask in item.asks) ...[
            _askCard(ask),
            const SizedBox(height: Space.s8),
          ],
        ],
        if (widget.pulls.isNotEmpty) ...[
          const SizedBox(height: Space.s12),
          PrCard(
            pull: pullFactsOf(widget.pulls.first, now: _now),
            actions: widget.pullActionsFor?.call(context, widget.pulls.first) ??
                const PrActions(),
          ),
        ],
        const SizedBox(height: Space.s16),
        Label('Activity', count: item.activity.length),
        const SizedBox(height: Space.s4),
        _activity(),
        const SizedBox(height: Space.s16),
        Tabs(
          tabs: [for (final label in _tabs) TabItem(label)],
          index: _tab,
          onChanged: (index) => setState(() => _tab = index),
        ),
        const SizedBox(height: Space.s12),
        _tabContent(context),
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
              child: ActivityRow(
                who: entry.who,
                text: entry.text,
                age: relativeAge(entry.at, _now),
                detail: entry.detail,
              ),
            ),
          ],
        ],
      ),
    );
  }

  /// One ask, pinned in its own thread (§5: the same card everywhere).
  Widget _askCard(model.Ask ask) => AskCard(
    kind: _askKind(ask.kind),
    placement: AskPlacement.thread,
    title: ask.title,
    body: ask.body.isEmpty ? null : ask.body,
    waiting: relativeAge(ask.since, _now),
    actions: [
      for (final action in ask.actions)
        AskAction(
          action.label,
          onTap: widget.onAnswer == null
              ? null
              : () => widget.onAnswer!(ask, action),
        ),
    ],
  );

  Widget _tabContent(BuildContext context) => switch (_tab) {
    0 => PlanTab(item: widget.item, onProposeChange: widget.onProposeChange),
    1 => AgentsTab(item: widget.item, onOpen: widget.onOpenWorker),
    2 => ChecksTab(item: widget.item),
    3 =>
      widget.changes ??
          StateMessage.unavailable(
            host: widget.item.host,
            lastSeen: 'now',
            action: StateAction(
              'Read the item again',
              onTap: widget.onRetry ?? () {},
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

  /// A work item's four asks are drawn with the kit's existing kinds (F6):
  /// the card's title and body say what is being decided.
  static AskKind _askKind(model.AskKind kind) => switch (kind) {
    model.AskKind.approvalDraft => AskKind.approval,
    model.AskKind.decision => AskKind.decision,
    model.AskKind.recovery => AskKind.decision,
    model.AskKind.completion => AskKind.decision,
    model.AskKind.approval => AskKind.approval,
    model.AskKind.question => AskKind.question,
    model.AskKind.intake => AskKind.intake,
    model.AskKind.pull => AskKind.pull,
    model.AskKind.reviewDraft => AskKind.review,
  };

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
