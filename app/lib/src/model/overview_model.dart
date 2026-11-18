import 'package:flutter/foundation.dart';

import '../client/contract.g.dart' show AuditEvent, UsagePoint, UsageReport;
import '../client/host.dart';
import 'fleet_store.dart';
import 'inbox_model.dart';
import 'nav_model.dart' show projectName;
import 'pulls_model.dart' show PullGroup, PullView;
import 'source_state.dart';
import 'thread_ref.dart';

// The activity feed is the bridge's audit log, drawn as it answered it. The
// contract type is re-exported the way schedules_model.dart re-exports
// Schedule, so a screen can name it without reaching into client/.
export '../client/contract.g.dart' show AuditEvent, UsagePoint, UsageReport;

/// How many asks the Overview shows before it hands over to the Inbox.
const overviewAsks = 3;

/// How many audit events the Overview shows.
const overviewActivity = 6;

/// One project's work, as the Overview's table shows it: a count per status,
/// and nothing inferred (F2 decision 8).
@immutable
class ProjectSummary {
  const ProjectSummary({
    required this.id,
    required this.name,
    required this.needsYou,
    required this.running,
    required this.ready,
    required this.failed,
    required this.idle,
    this.ciReady = 0,
    this.ciFailing = 0,
    this.spendUSD = 0,
  });

  /// The project's root, as its threads name it.
  final String id;

  /// Its directory's name.
  final String name;

  final int needsYou;
  final int running;
  final int ready;
  final int failed;
  final int idle;

  /// Ready pull requests from this project, and failing ones.
  final int ciReady;
  final int ciFailing;

  /// What this project's agents spent over the usage window, in dollars.
  final double spendUSD;

  /// Every thread it has.
  int get threads => needsYou + running + ready + failed + idle;

  @override
  String toString() => 'ProjectSummary($name, $threads threads)';
}

/// What one host is running now, and whether it answers.
///
/// The fleet contract carries no slot counts, so capacity is what a person
/// can actually see: how many threads a host runs, how many of them wait on
/// someone, and whether the host is reachable at all.
@immutable
class HostCapacity {
  const HostCapacity({
    required this.host,
    required this.reach,
    required this.lastContact,
    required this.threads,
    required this.needsYou,
  });

  final String host;
  final HostReach reach;
  final DateTime? lastContact;
  final int threads;
  final int needsYou;

  @override
  String toString() => 'HostCapacity($host, $threads threads, ${reach.name})';
}

/// The day's spend against the daily cap, over every host that reported one.
@immutable
class SpendSummary {
  const SpendSummary({
    required this.day,
    required this.spentUSD,
    required this.capUSD,
    required this.blocked,
  });

  /// The day the spend is counted from.
  final String day;

  final double spentUSD;
  final double capUSD;

  /// Some host reported that the cap is blocking work.
  final bool blocked;

  /// How much of the cap is spent, from 0 to 1.
  double get fraction => capUSD <= 0 ? 0 : (spentUSD / capUSD).clamp(0, 1);

  @override
  String toString() => 'SpendSummary($spentUSD of $capUSD)';
}

/// The Overview, projected from the store and the inbox (F2 decision 8).
@immutable
class OverviewModel {
  const OverviewModel({
    required this.needsYou,
    required this.running,
    required this.needsYouTotal,
    required this.sources,
    required this.projects,
    required this.capacity,
    required this.spend,
    this.activity = const [],
    this.spendDays = const [],
    this.spendByRole = const [],
    this.moreInProgress = 0,
  });

  /// The oldest asks, at most [overviewAsks] of them.
  final List<Ask> needsYou;

  /// Every thread with a turn in flight, most recently active first.
  final List<ThreadRef> running;

  /// Every ask, including the ones past [needsYou]'s cap, so a count and a
  /// label agree with what exists rather than with what is drawn.
  final int needsYouTotal;

  /// Each source's freshness, by host then source.
  final List<SourceState> sources;

  /// Every project, in name order.
  final List<ProjectSummary> projects;

  /// Every host, in discovery order.
  final List<HostCapacity> capacity;

  /// Today's spend, or null when no host reported one.
  final SpendSummary? spend;

  /// The newest audit events, most recent first, at most [overviewActivity].
  final List<AuditEvent> activity;

  /// The day series, oldest first: what the spend spark draws.
  final List<UsagePoint> spendDays;

  /// The window's spend by role, largest first: what the split names.
  final List<UsagePoint> spendByRole;

  /// Pull requests in progress beyond the ready and failing ones on screen.
  final int moreInProgress;
}

/// The Overview for what [store] holds and what [inbox] counts as waiting.
///
/// Every number here is a count of real items: nothing is estimated, and a
/// host that has not answered contributes what it last said, never a guess.
OverviewModel buildOverview(
  FleetStore store,
  InboxModel inbox, {
  List<AuditEvent> audit = const [],
  List<PullView> pulls = const [],
  UsageReport? usageByProject,
  UsageReport? usageByDay,
  UsageReport? usageByRole,
}) {
  final running = <ThreadRef>[];
  final projects = <String, List<ThreadRef>>{};
  final capacity = <HostCapacity>[];

  for (final host in store.hosts) {
    final threads = [
      for (final agent in store.agents(host: host.name))
        threadFor(host.name, agent),
    ];
    for (final thread in threads) {
      projects.putIfAbsent(thread.project, () => []).add(thread);
    }
    running.addAll(
      threads.where((thread) => thread.status == ThreadStatus.running),
    );
    capacity.add(
      HostCapacity(
        host: host.name,
        reach: host.reach,
        lastContact: host.lastContact,
        threads: threads.length,
        needsYou: threads
            .where((thread) => thread.status == ThreadStatus.needsYou)
            .length,
      ),
    );
  }
  running.sort((a, b) {
    final byTime = b.lastActive.compareTo(a.lastActive);
    return byTime != 0 ? byTime : a.id.compareTo(b.id);
  });

  // The pulls and the usage ledger are read by the Studio, not here (F9
  // decision 7): this joins them on the project root each one names.
  final spend = <String, double>{
    for (final point in usageByProject?.series ?? const <UsagePoint>[])
      point.key: point.costUSD,
  };
  final summaries = [
    for (final MapEntry(key: id, value: threads) in _byName(
      store,
      projects,
    ).entries)
      _summary(id, threads, pulls: pulls, spendUSD: spend[id] ?? 0),
  ];

  return OverviewModel(
    needsYou: List.unmodifiable(inbox.asks.take(overviewAsks)),
    needsYouTotal: inbox.asks.length,
    sources: sourceStates(store, factory: inbox.factory),
    running: List.unmodifiable(running),
    projects: List.unmodifiable(summaries),
    capacity: List.unmodifiable(capacity),
    spend: _spend(store),
    // The bridge answers oldest first (web/bridge/audit.go: Tail returns the
    // most recent records oldest first, and /api/audit hands them on as read),
    // so the card is the tail of the feed reversed: the newest event leads.
    // Taking its head would draw the oldest events of the window. No event is
    // filtered: a feed that hides kinds is a second, wrong feed.
    activity: List.unmodifiable(audit.reversed.take(overviewActivity)),
    // The bridge answers the day series oldest first, which is the direction
    // a spark reads; the roles are ranked here, largest first, because the
    // split names the biggest.
    spendDays: List.unmodifiable(
      usageByDay?.series ?? const <UsagePoint>[],
    ),
    spendByRole: List.unmodifiable(
      [
        for (final role in usageByRole?.series ?? const <UsagePoint>[]) role,
      ]..sort((a, b) => b.costUSD.compareTo(a.costUSD)),
    ),
    // Every pull request the two cards do not show: being fixed, in progress,
    // or with checks the bridge cannot read.
    moreInProgress: pulls
        .where(
          (pull) =>
              pull.group != PullGroup.ready && pull.group != PullGroup.failing,
        )
        .length,
  );
}

/// The projects in name order, so the table reads like the sidebar's tree.
Map<String, List<ThreadRef>> _byName(
  FleetStore store,
  Map<String, List<ThreadRef>> grouped,
) {
  final names = {
    for (final project in store.projects())
      project.root: projectName(project.root),
  };
  final ids = grouped.keys.toList()
    ..sort((a, b) {
      final byName = (names[a] ?? projectName(a)).toLowerCase().compareTo(
        (names[b] ?? projectName(b)).toLowerCase(),
      );
      return byName != 0 ? byName : a.compareTo(b);
    });
  return {for (final id in ids) id: grouped[id]!};
}

ProjectSummary _summary(
  String id,
  List<ThreadRef> threads, {
  List<PullView> pulls = const [],
  double spendUSD = 0,
}) {
  int count(ThreadStatus status) =>
      threads.where((thread) => thread.status == status).length;
  int countPulls(PullGroup group) => pulls
      .where((pull) => pull.projectRoot == id && pull.group == group)
      .length;
  return ProjectSummary(
    id: id,
    name: projectName(id),
    needsYou: count(ThreadStatus.needsYou),
    running: count(ThreadStatus.running),
    ready: count(ThreadStatus.ready),
    failed: count(ThreadStatus.failed),
    idle: count(ThreadStatus.idle),
    ciReady: countPulls(PullGroup.ready),
    ciFailing: countPulls(PullGroup.failing),
    spendUSD: spendUSD,
  );
}

/// Today's spend over every host that reported a loaded budget.
SpendSummary? _spend(FleetStore store) {
  var spent = 0.0;
  var cap = 0.0;
  var blocked = false;
  String? day;
  var reported = false;
  for (final host in store.hosts) {
    final report = store.budgets(host.name);
    if (report == null || !report.loaded) continue;
    reported = true;
    spent += report.daily.spentUSD;
    cap += report.budgets.dailyUSD;
    blocked = blocked || report.daily.blocked;
    day ??= report.daily.day;
  }
  if (!reported) return null;
  return SpendSummary(
    day: day ?? '',
    spentUSD: spent,
    capUSD: cap,
    blocked: blocked,
  );
}
