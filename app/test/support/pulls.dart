import 'package:marshal_app/src/client/contract.g.dart' as contract;
import 'package:marshal_app/src/model/pulls_model.dart';
import 'package:marshal_app/src/model/thread_ref.dart';

/// One pull request as a test writes one: the record a card reads and the view
/// a screen draws, so every screen test spells one the same way.
PullView pullView({
  int number = 131,
  String title = 'Quiet capture mode',
  PullGroup group = PullGroup.ready,
  String repo = 'acme/widgets',
  String branch = 'agent/quiet-capture',
  String base = 'main',
  String head = 'a1b2c3d4',
  int adds = 412,
  int dels = 96,
  String reviews = '2 reviews passed',
  List<PullCheckView> checks = const [
    PullCheckView('build', PullCheckState.passed),
    PullCheckView('unit', PullCheckState.passed),
  ],
  String? checkSummary,
  String? failureTail,
  String? report,
  bool draft = false,
  bool sending = false,
  String? error,
  ThreadRef? thread,
  String? agentID = 'a-1',
  String? factoryItem,
  DateTime? updatedAt,
}) {
  final state = switch (group) {
    PullGroup.ready => contract.PullState.pullReady,
    PullGroup.failing => contract.PullState.pullFailing,
    PullGroup.beingFixed => contract.PullState.pullBeingFixed,
    PullGroup.inProgress => contract.PullState.pullInProgress,
    PullGroup.unavailable => contract.PullState.pullChecksUnavailable,
  };
  final at =
      (updatedAt ?? DateTime.utc(2026, 11, 17, 12)).millisecondsSinceEpoch;
  return PullView(
    record: contract.PullRecord(
      id: '$repo#$number',
      repoID: repo,
      projectRoot: '/home/u/marshal',
      number: number,
      url: 'https://forge.example/$repo/pull/$number',
      title: title,
      headRef: branch,
      baseRef: base,
      headSHA: head,
      draft: draft,
      additions: adds,
      deletions: dels,
      origin: factoryItem != null
          ? contract.PullOrigin.pullOriginFactory
          : agentID != null
          ? contract.PullOrigin.pullOriginAgent
          : contract.PullOrigin.pullOriginAutomation,
      agentID: agentID,
      factoryItem: factoryItem,
      state: state,
      checks: [
        for (final check in checks)
          contract.PullCheck(
            name: check.name,
            state: switch (check.state) {
              PullCheckState.passed => contract.CheckState.checkPassed,
              PullCheckState.failed => contract.CheckState.checkFailed,
              PullCheckState.running => contract.CheckState.checkRunning,
              PullCheckState.pending => contract.CheckState.checkPending,
            },
            logTail: check.logTail,
          ),
      ],
      reviews: reviews,
      report: report,
      observedAt: at,
      updatedAt: at,
    ),
    thread: thread,
    group: group,
    checkSummary: checkSummary ?? _summary(checks, group),
    checks: checks,
    failureTail: failureTail,
    sending: sending,
    error: error,
  );
}

/// The one-line summary a group's checks would produce, in the model's words.
String _summary(List<PullCheckView> checks, PullGroup group) {
  if (group == PullGroup.unavailable) {
    return 'Checks unavailable: the credential is an SSH key, which cannot '
        'call the API';
  }
  final failed = [
    for (final check in checks)
      if (check.state == PullCheckState.failed) check.name,
  ];
  if (failed.isNotEmpty)
    return '${failed.length} failing: ${failed.join(', ')}';
  final passed = checks
      .where((check) => check.state == PullCheckState.passed)
      .length;
  if (passed == checks.length)
    return '$passed of ${checks.length} checks passed';
  return '$passed of ${checks.length} checks passed, the rest running';
}

/// One thread a pull request came from.
ThreadRef threadRef({
  String host = 'local',
  String id = 'a-1',
  String project = '/home/u/marshal',
  String title = 'Quiet capture mode',
  ThreadKind kind = ThreadKind.agent,
  ThreadStatus status = ThreadStatus.ready,
  DateTime? lastActive,
}) => ThreadRef(
  host: host,
  id: id,
  project: project,
  title: title,
  kind: kind,
  status: status,
  headline: 'Removing the flaky fixture',
  lastActive: lastActive ?? DateTime.utc(2026, 11, 17, 11, 52),
);
