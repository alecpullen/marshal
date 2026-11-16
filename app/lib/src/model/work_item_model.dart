import '../client/contract.g.dart';
import 'inbox_model.dart';
import 'thread_ref.dart';

/// The four stages a work item is drawn on (F6 decision 2), in order.
enum Stage { plan, implement, review, ready }

/// What a list of work items is filtered to (F6 decision 2, F6 plan 1).
enum ItemFilter { current, needsYou, ready, history }

/// Whether [item] is finished as far as a list is concerned: it leaves the
/// current work for history.
bool isHistoricalItem(FactoryItem item) =>
    item.state == 'closed' || item.state == 'stopped';

/// The stage [item] is at.
///
/// The mapping from state is fixed: a draft, a queued or a preparing item is
/// still planning; implementing and repairing are implementing; reviewing and
/// checking are reviewing; ready is ready. A state that interrupts a stage
/// keeps it — `needs_input` and `recovering` continue the stage the bridge
/// last recorded on the item — and a state this build does not know is shown
/// under Plan rather than hidden, because an item that vanished from every
/// stage would be work nobody can see.
Stage stageOf(FactoryItem item) => switch (item.state) {
  'draft' || 'queued' || 'preparing' => Stage.plan,
  'implementing' || 'repairing' => Stage.implement,
  'reviewing' || 'checking' => Stage.review,
  'ready' => Stage.ready,
  'needs_input' || 'recovering' => _interruptedStage(item),
  // An ending item keeps the stage it reached: the end itself is a word
  // beside the strip, not a fifth stage.
  'stopping' || 'stopped' || 'closed' => _endStage(item),
  _ => Stage.plan,
};

/// The stage an interrupting state kept: the one the bridge recorded, or the
/// one an item with a round was necessarily at.
Stage _interruptedStage(FactoryItem item) => switch (item.stage) {
  'plan' => Stage.plan,
  'review' => Stage.review,
  'ready' => Stage.ready,
  'implement' => Stage.implement,
  // An interrupting state is past planning by definition: the item was
  // approved and a worker was running when it was interrupted.
  _ => Stage.implement,
};

/// The stage an ending item is drawn at: the one the bridge recorded, or the
/// first one when it recorded none.
Stage _endStage(FactoryItem item) => switch (item.stage) {
  'implement' => Stage.implement,
  'review' => Stage.review,
  'ready' => Stage.ready,
  _ => Stage.plan,
};

/// The word shown beside the strip for an item that is ending, and null for an
/// item that is working: stopping, stopped and closed are words, never a fifth
/// stage (F6 decision 2).
String? endStateWord(FactoryItem item) => switch (item.state) {
  'stopping' => 'Stopping',
  'stopped' => 'Stopped',
  'closed' => 'Closed',
  _ => null,
};

/// What the thread's dot says (F6 decision 3): needs you while a decision is
/// pending or the item is a draft or waiting on a person; ready when ready;
/// failed when it stopped with an error; idle when it stopped or closed;
/// running otherwise.
ThreadStatus statusOfItem(FactoryItem item) {
  final stopped = item.state == 'stopped';
  if (item.decision != null ||
      item.state == 'draft' ||
      item.state == 'needs_input') {
    return ThreadStatus.needsYou;
  }
  if (item.state == 'ready') return ThreadStatus.ready;
  if (stopped && (item.completion?.stopState == 'failed')) {
    return ThreadStatus.failed;
  }
  if (item.state == 'closed' || stopped) return ThreadStatus.idle;
  return ThreadStatus.running;
}

/// Whether [item] belongs in [filter]. An unknown filter falls back to the
/// current list rather than emptying it.
bool matches(FactoryItem item, ItemFilter filter) => switch (filter) {
  ItemFilter.needsYou =>
    item.decision != null ||
        item.state == 'draft' ||
        item.state == 'needs_input',
  ItemFilter.ready => item.state == 'ready',
  ItemFilter.history => isHistoricalItem(item),
  ItemFilter.current => !isHistoricalItem(item),
};

/// The items in [filter], needs-you first, then ready, then newest, with the
/// id breaking a tie (web/ui/src/lib/studio/model.ts's `filterFactoryItems`).
List<FactoryItem> filterItems(List<FactoryItem> items, ItemFilter filter) {
  final kept = [
    for (final item in items)
      if (matches(item, filter)) item,
  ];
  kept.sort((a, b) {
    final byRank = _rank(a).compareTo(_rank(b));
    if (byRank != 0) return byRank;
    final byTime = lastActiveOfItem(b).compareTo(lastActiveOfItem(a));
    return byTime != 0 ? byTime : a.id.compareTo(b.id);
  });
  return List.unmodifiable(kept);
}

int _rank(FactoryItem item) {
  if (matches(item, ItemFilter.needsYou)) return 0;
  return item.state == 'ready' ? 1 : 2;
}

/// When the item last did anything. A time this build cannot read is the
/// oldest possible, so it sorts with the earlier work rather than claiming to
/// be current.
DateTime lastActiveOfItem(FactoryItem item) =>
    DateTime.tryParse(item.updatedAt) ??
    DateTime.fromMillisecondsSinceEpoch(0, isUtc: true);

/// The thread [item] is, on [host]: what the sidebar, the overview and the
/// inbox draw it as (F6 decision 13).
ThreadRef threadOfItem(String host, FactoryItem item) => ThreadRef(
  host: host,
  id: item.id,
  project: item.projectRoot,
  title: item.title.trim().isEmpty ? item.id : item.title.trim(),
  kind: ThreadKind.workItem,
  status: statusOfItem(item),
  headline: _headline(item),
  lastActive: lastActiveOfItem(item),
);

/// One line about what the item is doing: the decision it waits on, else the
/// stage it is at. Empty when the state says it all.
String _headline(FactoryItem item) {
  final decision = item.decision;
  if (decision != null && decision.question.trim().isNotEmpty) {
    return decision.question.trim();
  }
  return switch (item.state) {
    'draft' => 'Waiting for approval',
    'implementing' || 'repairing' => 'Implementing',
    'reviewing' || 'checking' => 'Reviewing',
    'ready' => 'Ready for review',
    'stopped' => 'Stopped',
    'closed' => 'Closed',
    _ => '',
  };
}

/// One entry of a work item's activity: who acted, what they did, and the
/// detail the bridge kept beside it.
class ActivityEntry {
  const ActivityEntry({
    required this.who,
    required this.text,
    required this.at,
    this.detail,
  });

  /// The person or role, in words: "You", "The planner", "The implementer",
  /// "The reviewer".
  final String who;

  /// What happened, in the bridge's own words.
  final String text;

  /// Anything the entry carried beyond its message, or null.
  final String? detail;

  final DateTime at;

  @override
  String toString() => 'ActivityEntry($who, $text, $at)';
}

/// The item's history as a timeline, oldest first (F6 decision 4).
///
/// Every entry is attributed to a role in words. An event that names an effect
/// is attributed to that effect's worker role; one that does not is read from
/// its kind, and anything else is the user's own action, which is what an
/// approval, an answer and a completion are.
List<ActivityEntry> activityOf(FactoryItem item) {
  final events = [...item.history]
    ..sort((a, b) {
      final bySequence = a.sequence.compareTo(b.sequence);
      return bySequence != 0 ? bySequence : a.at.compareTo(b.at);
    });
  return List.unmodifiable([
    for (final event in events)
      ActivityEntry(
        who: whoActed(item, event),
        text: event.message.trim().isEmpty ? event.kind : event.message.trim(),
        detail: event.kind,
        at:
            DateTime.tryParse(event.at) ??
            DateTime.fromMillisecondsSinceEpoch(0, isUtc: true),
      ),
  ]);
}

/// Who a history entry is attributed to, in words (F6 decision 4).
String whoActed(FactoryItem item, FactoryEvent event) {
  for (final effect in item.effects) {
    if (effect.id == event.effectID && effect.workerRole.isNotEmpty) {
      return roleWords(effect.workerRole);
    }
  }
  final kind = event.kind;
  if (kind.startsWith('plan')) return 'The planner';
  if (kind.startsWith('review')) return 'The reviewer';
  if (kind.startsWith('implement') ||
      kind.startsWith('repair') ||
      kind.startsWith('check') ||
      kind.startsWith('continuation')) {
    return 'The implementer';
  }
  return 'You';
}

/// One worker role in the words the design system uses.
String roleWords(String role) => switch (role) {
  'planner' => 'The planner',
  'implementer' => 'The implementer',
  'reviewer' => 'The reviewer',
  _ => role.isEmpty ? 'A worker' : 'The ${role.toLowerCase()}',
};

/// What the item waits on a person for, as the asks its thread pins at the
/// top (F6 decision 5): a draft awaiting approval, a decision, a recovery, a
/// completion assessment. At most one of each, and none for an item that is
/// simply working.
List<Ask> asksOf(String host, FactoryItem item) {
  final asks = <Ask>[];
  final thread = threadOfItem(host, item);
  final since = lastActiveOfItem(item);
  final created = DateTime.tryParse(item.createdAt) ?? since;

  if (item.state == 'draft') {
    asks.add(
      Ask(
        host: host,
        id: '${item.id}:approval',
        kind: AskKind.approvalDraft,
        thread: thread,
        title: 'Approve the plan',
        body: planSummary(item.draft.plan),
        actions: const [
          AskAction('Approve and start', primary: true),
          AskAction('Propose a change', kind: AskActionKind.reply),
        ],
        since: created,
      ),
    );
  }

  final decision = item.decision;
  if (decision != null) {
    asks.add(
      Ask(
        host: host,
        id: '${item.id}:decision',
        kind: AskKind.decision,
        thread: thread,
        title: decision.question.trim().isEmpty
            ? 'The worker needs a decision'
            : decision.question.trim(),
        body: decision.reason.trim(),
        actions: const [
          AskAction('Answer', primary: true, kind: AskActionKind.reply),
        ],
        since: DateTime.tryParse(decision.createdAt) ?? since,
      ),
    );
  }

  if (item.state == 'needs_input' && decision == null) {
    asks.add(
      Ask(
        host: host,
        id: '${item.id}:recovery',
        kind: AskKind.recovery,
        thread: thread,
        title: 'The work needs a recovery',
        body: recoveryDetail(item),
        actions: const [
          AskAction('Continue', primary: true),
          AskAction('Start fresh', kind: AskActionKind.reply),
        ],
        since: since,
      ),
    );
  }

  final completion = item.completion;
  final open = (completion?.status ?? '') != 'complete';
  if (item.state == 'ready' && open) {
    asks.add(
      Ask(
        host: host,
        id: '${item.id}:completion',
        kind: AskKind.completion,
        thread: thread,
        title: 'The work is ready to finish',
        body: completionDetail(item),
        actions: const [
          AskAction('Complete', primary: true),
          AskAction('Merge and complete'),
          AskAction('Reopen', kind: AskActionKind.reply),
        ],
        since: since,
      ),
    );
  }
  return List.unmodifiable(asks);
}

/// The plan in one line: how many tasks it holds, and its first line of prose.
///
/// A plan is summarised by its task count so the scale is visible before it is
/// read (web/ui/src/lib/pending.ts).
String planSummary(String plan) {
  final text = plan.trim();
  if (text.isEmpty) return 'No plan text';
  final tasks = RegExp(
    r'^#{2,3} Task \d+',
    multiLine: true,
  ).allMatches(text).length;
  final first = [
    for (final line in text.split('\n'))
      if (line.trim().isNotEmpty && !line.trim().startsWith('#')) line.trim(),
  ].firstOrNull;
  final count = tasks == 0 ? 'plan' : '$tasks ${tasks == 1 ? 'task' : 'tasks'}';
  return first == null ? count : '$count · $first';
}

/// Why the item needs a recovery: the last round's finding, in its own words.
String recoveryDetail(FactoryItem item) {
  final round = item.rounds.lastOrNull;
  final review = round?.review;
  final summary = review?.summary.trim() ?? '';
  if (summary.isNotEmpty) return summary;
  final reason = review?.reason.trim() ?? '';
  if (reason.isNotEmpty) return reason;
  final attempts = item.rounds.length;
  return attempts == 0
      ? 'The work stalled before its first round finished.'
      : '$attempts ${attempts == 1 ? 'round' : 'rounds'} so far.';
}

/// The completion assessment in one line: what was judged, and against what.
String completionDetail(FactoryItem item) {
  final assessment = item.completion?.assessment;
  final summary = assessment?.summary?.trim() ?? '';
  if (summary.isNotEmpty) return summary;
  final prs = item.pRs.length;
  final checks = item.rounds.lastOrNull?.ci?.checks.length ?? 0;
  return [
    if (prs > 0) '$prs ${prs == 1 ? 'pull request' : 'pull requests'}',
    if (checks > 0) '$checks ${checks == 1 ? 'check' : 'checks'}',
  ].join(' · ');
}

/// One worker as a work item's screen draws it.
class WorkItemWorker {
  const WorkItemWorker({
    required this.agentID,
    required this.sessionID,
    required this.role,
    required this.parked,
  });

  final String agentID;

  /// The session this worker's conversation is read from.
  final String sessionID;

  /// The bridge's word for the role: planner, implementer, reviewer.
  final String role;

  final bool parked;

  /// The role in the design system's words.
  String get words => roleWords(role);
}

/// One CI check as a work item's screen draws it (F6 decision 6).
enum WorkItemCheckState { passed, failed, running, pending }

class WorkItemCheck {
  const WorkItemCheck(this.name, this.state);

  final String name;
  final WorkItemCheckState state;
}

/// One review finding as a work item's screen draws it.
class WorkItemFinding {
  const WorkItemFinding(this.title, this.detail);

  final String title;

  /// Severity and the file it names, in one line.
  final String detail;
}

/// One of the approved revision's settings, in words.
class WorkItemSetting {
  const WorkItemSetting(this.name, this.description, this.value);

  final String name;
  final String description;
  final String value;
}

/// A work item as its screen draws it: everything the thread needs, read from
/// the wire once here.
///
/// The screen takes this and never the bridge's own type: `ui/` reads `model/`
/// and never touches `client/` (F1's boundary), so the projection lives beside
/// the rules it applies.
class WorkItemView {
  const WorkItemView({
    required this.host,
    required this.id,
    required this.title,
    required this.state,
    required this.stage,
    required this.status,
    required this.endWord,
    required this.lastActive,
    required this.version,
    required this.decisionID,
    required this.revision,
    required this.baseHash,
    required this.plan,
    required this.spec,
    required this.settings,
    required this.workers,
    required this.checks,
    required this.hasRound,
    required this.findings,
    required this.activity,
    required this.asks,
    required this.proposals,
  });

  /// Reads [item] as [host]'s screen draws it.
  factory WorkItemView.of(String host, FactoryItem item) {
    final approval = item.approvals.isEmpty ? null : item.approvals.last;
    final config = approval?.config ?? item.draft.config;
    final round = item.rounds.isEmpty ? null : item.rounds.last;
    final review = round?.review;
    return WorkItemView(
      host: host,
      id: item.id,
      title: item.title.trim().isEmpty ? item.id : item.title.trim(),
      state: item.state,
      stage: stageOf(item),
      status: statusOfItem(item),
      endWord: endStateWord(item),
      lastActive: lastActiveOfItem(item),
      version: item.version,
      decisionID: item.decision?.id ?? '',
      revision: approval?.revision ?? 0,
      baseHash: approval?.planHash ?? '',
      plan: approval?.plan ?? item.draft.plan,
      spec: approval?.spec ?? item.draft.spec,
      settings: _settings(config, approval),
      workers: [
        for (final worker in item.workers)
          WorkItemWorker(
            agentID: worker.agentID,
            sessionID: worker.sessionID,
            role: worker.role,
            parked: worker.parked,
          ),
      ],
      checks: [
        for (final check in round?.ci?.checks ?? const <FactoryCheck>[])
          _check(check),
      ],
      hasRound: round != null,
      findings: [
        for (final finding in review?.findings ?? const <ReviewFinding>[])
          WorkItemFinding(
            finding.title,
            [
              finding.severity,
              if ((finding.path ?? '').isNotEmpty)
                '${finding.path}${(finding.line ?? 0) > 0 ? ':${finding.line}' : ''}',
            ].join(' · '),
          ),
      ],
      activity: activityOf(item),
      asks: asksOf(host, item),
      proposals: [
        for (final proposal
            in item.proposals ?? const <FactoryRevisionProposal>[])
          'Revision ${proposal.baseRevision + 1} · '
              '${proposal.status.replaceAll('_', ' ')}',
      ],
    );
  }

  final String host;
  final String id;
  final String title;

  /// The bridge's state word, as it reported it.
  final String state;

  final Stage stage;
  final ThreadStatus status;

  /// The word beside the strip for an ending item, or null.
  final String? endWord;

  final DateTime lastActive;

  /// The version a command must name as the one on screen.
  final int version;

  /// The decision the item is blocked on, or "" when it is blocked on none:
  /// what an answer names.
  final String decisionID;

  /// The approved revision a plan change is proposed against, and its hash:
  /// a proposal that names a base the item no longer holds is refused rather
  /// than rebased.
  final int revision;
  final String baseHash;

  final String plan;
  final String spec;

  final List<WorkItemSetting> settings;
  final List<WorkItemWorker> workers;
  final List<WorkItemCheck> checks;

  /// Whether the item has run a round yet: what its checks tab says.
  final bool hasRound;

  final List<WorkItemFinding> findings;
  final List<ActivityEntry> activity;

  /// What it waits on a person for, pinned above its activity.
  final List<Ask> asks;

  /// Its proposed plan revisions, in words.
  final List<String> proposals;

  /// The stage in the design system's words, with the end word beside it.
  String get stageWords {
    final words = switch (stage) {
      Stage.plan => 'Planning',
      Stage.implement => 'Implementing',
      Stage.review => 'Reviewing',
      Stage.ready => 'Ready for review',
    };
    final end = endWord;
    if (end != null) return '$words · $end';
    if (state == 'needs_input') return '$words · Needs you';
    return words;
  }

  static List<WorkItemSetting> _settings(
    FactoryConfig config,
    FactoryApproval? approval,
  ) => [
    WorkItemSetting('Repository', 'What the work is on', config.repoID),
    WorkItemSetting(
      'Target branch',
      'What the pull request lands on',
      config.targetBranch,
    ),
    WorkItemSetting(
      'Where it runs',
      config.native ? 'On this host' : 'In a workspace',
      config.native
          ? 'Native'
          : (config.workspace.isEmpty ? 'Workspace' : config.workspace),
    ),
    WorkItemSetting(
      'Rounds',
      'How many repair rounds it may take',
      '${config.maxRounds}',
    ),
    WorkItemSetting(
      'Expected checks',
      'What must pass before it is ready',
      config.expectedChecks.isEmpty
          ? 'None required'
          : config.expectedChecks.map((check) => check.name).join(', '),
    ),
    if (approval != null)
      WorkItemSetting(
        'Approved revision',
        'The settings the workers were started with',
        '${approval.revision}',
      ),
  ];

  /// One wire check in the model's vocabulary: only a finished conclusion can
  /// pass or fail, and anything else is still on its way.
  static WorkItemCheck _check(FactoryCheck check) => WorkItemCheck(
    check.key.name.isEmpty ? check.id : check.key.name,
    switch (check.conclusion) {
      'success' || 'neutral' || 'skipped' => WorkItemCheckState.passed,
      '' =>
        check.status == 'in_progress' || check.status == 'queued'
            ? WorkItemCheckState.running
            : WorkItemCheckState.pending,
      _ => WorkItemCheckState.failed,
    },
  );
}
