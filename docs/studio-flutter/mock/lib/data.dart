/// Illustrative data only. Nothing here is read from a daemon or a bridge.
library;

enum Status { needsYou, running, ready, idle, failed }

/// The three kinds of thread. They share one screen shape: a conversation in
/// the centre and an inspector on the right.
enum Kind { agent, work, run }

class Project {
  const Project(this.id, this.name, this.repo, this.env, this.spend, this.ci);
  final String id;
  final String name;
  final String repo;
  final String env;
  final String spend;
  final String ci;
}

class Thread {
  const Thread({
    required this.id,
    required this.project,
    required this.title,
    required this.kind,
    required this.status,
    required this.headline,
    this.model = 'qwen3.5-9b',
    this.env = 'go-service',
    this.branch = 'main',
    this.elapsed = '',
    this.done = 0,
    this.total = 0,
    this.stage = 0,
    this.recent = const [],
    this.host = 'Local daemon',
  });
  final String id;
  final String project;
  final String title;
  final Kind kind;
  final Status status;
  final String headline;
  final String model;
  final String env;
  final String branch;
  final String elapsed;
  final int done;
  final int total;

  /// Work items only: 0 Plan, 1 Implement, 2 Review, 3 Ready.
  final int stage;
  final List<String> recent;
  final String host;
}

const projects = [
  Project('marshal', 'marshal', 'alecpullen/marshal', 'go-service', r'$4.12', 'passing'),
  Project('portal', 'av-portal', 'alecpullen/av-portal', 'node-web', r'$1.80', '1 failing'),
  Project('horizon', 'horizondata', 'git.home/alec/horizondata', 'python-data', r'$0.00', 'passing'),
];

const stages = ['Plan', 'Implement', 'Review', 'Ready'];

const threads = [
  Thread(
    id: 'recovery',
    project: 'marshal',
    title: 'Reliable session recovery',
    kind: Kind.work,
    status: Status.needsYou,
    headline: 'Waiting on a scope decision',
    model: 'opus plans · sonnet implements',
    branch: 'factory/session-recovery',
    elapsed: '1h 12m',
    done: 2,
    total: 5,
    stage: 1,
    host: 'studio-box',
    recent: ['Drafted PR #121: reconnect replay', 'Reviewer flagged approval persistence', 'Planner raised a scope decision'],
  ),
  Thread(
    id: 'voice',
    project: 'marshal',
    title: 'Voice planning',
    kind: Kind.work,
    status: Status.running,
    headline: 'Implementing quiet capture mode',
    model: 'opus plans · sonnet implements',
    branch: 'factory/voice-planning',
    elapsed: '38m',
    done: 3,
    total: 6,
    stage: 1,
    host: 'studio-box',
    recent: ['Added recorder state machine', 'Wired planner questions to pause', 'Running widget tests'],
  ),
  Thread(
    id: 'cireview',
    project: 'marshal',
    title: 'CI-first change review',
    kind: Kind.work,
    status: Status.ready,
    headline: '9 checks and 2 reviews passed',
    branch: 'factory/ci-first-review',
    elapsed: '2h 40m',
    done: 4,
    total: 4,
    stage: 3,
    host: 'studio-box',
  ),
  Thread(
    id: 'fixtures',
    project: 'marshal',
    title: 'Fixture cleanup',
    kind: Kind.agent,
    status: Status.needsYou,
    headline: 'Asks: keep the legacy test fixtures?',
    branch: 'agent/fixture-cleanup',
    elapsed: '8m',
    done: 4,
    total: 7,
    recent: ['Listed fixtures under testdata/', 'Found 14 unreferenced files', 'Asked before deleting'],
  ),
  Thread(
    id: 'p6',
    project: 'marshal',
    title: 'P6 plan 4: daemon upgrade',
    kind: Kind.run,
    status: Status.running,
    headline: 'Task 3 of 7: reviewer on fix round 1',
    model: 'deepseek implements · opus reviews',
    branch: 'p6-4-upgrade',
    elapsed: '54m',
    done: 2,
    total: 7,
    recent: ['Task 2 committed 9206749b', 'Task 3 implemented', 'Reviewer asked for one fix'],
  ),
  Thread(
    id: 'booking',
    project: 'portal',
    title: 'Booking updates',
    kind: Kind.work,
    status: Status.running,
    headline: 'Blocked request to api.stripe.com',
    env: 'node-web',
    branch: 'factory/booking-updates',
    elapsed: '21m',
    done: 1,
    total: 4,
    stage: 1,
    host: 'studio-box',
    recent: ['Scaffolded booking form', 'Added price preview', 'Network request blocked'],
  ),
  Thread(
    id: 'flaky',
    project: 'portal',
    title: 'Fix flaky calendar test',
    kind: Kind.agent,
    status: Status.ready,
    headline: 'Gate passed · +18 −6 in 2 files',
    env: 'node-web',
    branch: 'agent/flaky-calendar',
    elapsed: '14m',
    done: 5,
    total: 5,
    host: 'studio-box',
  ),
  Thread(
    id: 'caching',
    project: 'horizon',
    title: 'Explore caching approaches',
    kind: Kind.agent,
    status: Status.running,
    headline: 'Comparing invalidation strategies',
    env: 'python-data',
    elapsed: '6m',
    done: 2,
    total: 5,
    recent: ['Read query layer', 'Benchmarked LRU against TTL', 'Writing comparison'],
  ),
];

Thread threadById(String id) => threads.firstWhere((t) => t.id == id);
Project projectById(String id) => projects.firstWhere((p) => p.id == id);

/// An open pull request. `checks` holds one state per CI check:
/// 0 passed, 2 failed, 3 running, 4 not started.
class Pull {
  const Pull(this.number, this.title, this.thread, this.checks, this.reviews, this.adds, this.dels, this.age,
      {this.ready = false, this.failing = ''});
  final int number;
  final String title;
  final String thread;
  final List<int> checks;
  final String reviews;
  final int adds;
  final int dels;
  final String age;
  final bool ready;
  final String failing;

  bool get failed => checks.contains(2);
  bool get running => !failed && checks.any((c) => c == 3 || c == 4);
  int get passed => checks.where((c) => c == 0).length;
}

const checkNames = [
  'build (linux, cgo)',
  'test: go suite',
  'test: usability scripted',
  'web ui: check and build',
  'lint',
  'vet',
  'boundary tests',
  'e2e: fleet',
  'docs links',
];

const pulls = [
  Pull(118, 'CI-first change review', 'cireview', [0, 0, 0, 0, 0, 0, 0, 0, 0], '2 reviews passed', 412, 96, '26m',
      ready: true),
  Pull(120, 'Fix flaky calendar test', 'flaky', [0, 0, 0, 0, 0, 0], 'no reviewers configured', 18, 6, '14m',
      ready: true),
  Pull(123, 'Voice planning: quiet capture mode', 'voice', [0, 0, 2, 0], 'waiting on checks', 288, 41, '4m',
      failing: 'test: usability scripted'),
  Pull(121, 'Session recovery: reconnect replay', 'recovery', [0, 0, 0, 0], '1 finding, repaired', 214, 38, '19m'),
  Pull(124, 'Session recovery: approval persistence', 'recovery', [0, 3, 4, 4], 'not started', 96, 12, '7m'),
];

enum Ask { decision, question, approval, network, review, intake }

class InboxItem {
  const InboxItem(this.kind, this.thread, this.title, this.body, this.actions, this.waiting);
  final Ask kind;
  final String thread;
  final String title;
  final String body;
  final List<String> actions;
  final String waiting;
}

const inbox = [
  InboxItem(
    Ask.decision,
    'recovery',
    'Should recovery survive a daemon restart?',
    'The plan covers reconnects. Persisting approvals across a restart widens the scope by one PR. The planner recommends keeping this change to reconnects.',
    ['Keep to reconnects', 'Expand scope', 'Discuss'],
    '12m',
  ),
  InboxItem(
    Ask.question,
    'fixtures',
    'Keep the legacy test fixtures?',
    '14 files under testdata/legacy are referenced by no test. The agent wants your call before removing them.',
    ['Remove them', 'Keep them', 'Reply'],
    '8m',
  ),
  InboxItem(
    Ask.network,
    'booking',
    'Allow api.stripe.com?',
    'The implementer tried to reach api.stripe.com, which is not on the node-web allowlist.',
    ['Allow for this agent', 'Add to environment', 'Block'],
    '3m',
  ),
  InboxItem(
    Ask.approval,
    'caching',
    r'Run: pip install redis==5.2',
    'Shell command outside the approved patterns. Sandbox: container, network allowlist.',
    ['Approve once', 'Always allow pip install', 'Deny'],
    '1m',
  ),
  InboxItem(
    Ask.review,
    'cireview',
    'Review bot draft on PR #118',
    '3 findings: 1 should fix, 2 nits. Nothing blocking.',
    ['Open draft', 'Post', 'Discard'],
    '26m',
  ),
  InboxItem(
    Ask.intake,
    'p6',
    'Claude Code wants to start an agent',
    'MCP client "claude-code" submitted: "Add a --json flag to daemon pin". Project marshal, read-write.',
    ['Start', 'Edit first', 'Decline'],
    '40m',
  ),
];

const askLabel = {
  Ask.decision: 'Decision',
  Ask.question: 'Question',
  Ask.approval: 'Approval',
  Ask.network: 'Network',
  Ask.review: 'Review draft',
  Ask.intake: 'Intake',
};
