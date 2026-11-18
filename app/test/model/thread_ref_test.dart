import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/thread_ref.dart';

import 'support.dart';

/// A thread's status, its last activity and the one line about it: the three
/// rules the sidebar, the overview and the inbox all read (F2 Decisions).
void main() {
  test('agent status table', () {
    // One row per rule in the mapping, in the order the rules apply, so a
    // rule that starts winning over another shows up here.
    final cases = <String, (AgentStatus, ThreadStatus)>{
      'a pending approval': (
        agent(
          status: AgentState.agentAwaitingApproval,
          pending: const PendingRequest(
            kind: PendingKind.pendingApproval,
            id: 'tc-1',
          ),
        ),
        ThreadStatus.needsYou,
      ),
      'a pending question': (
        agent(
          status: AgentState.agentAwaitingQuestion,
          pending: const PendingRequest(
            kind: PendingKind.pendingQuestion,
            id: 'q-1',
          ),
        ),
        ThreadStatus.needsYou,
      ),
      'a request the snapshot has not carried yet': (
        agent(status: AgentState.agentAwaitingApproval),
        ThreadStatus.needsYou,
      ),
      'an error': (agent(status: AgentState.agentError), ThreadStatus.failed),
      'error with changes and a request answered': (
        agent(status: AgentState.agentError, changedFiles: 3),
        ThreadStatus.failed,
      ),
      'a turn in flight': (
        agent(status: AgentState.agentRunning),
        ThreadStatus.running,
      ),
      'a turn in flight with changes': (
        agent(status: AgentState.agentRunning, changedFiles: 2),
        ThreadStatus.running,
      ),
      'finished with changes unshipped': (
        agent(status: AgentState.agentIdle, changedFiles: 3),
        ThreadStatus.ready,
      ),
      'finished with a pull request open': (
        agent(
          status: AgentState.agentIdle,
          changedFiles: 3,
          prUrl: 'https://forge.example/pr/7',
        ),
        ThreadStatus.idle,
      ),
      'finished with nothing to ship': (
        agent(status: AgentState.agentIdle),
        ThreadStatus.idle,
      ),
      'a state this build does not know': (
        agent(status: AgentState.unknown, changedFiles: 1),
        ThreadStatus.idle,
      ),
    };

    for (final MapEntry(key: what, value: (given, want)) in cases.entries) {
      expect(statusOfAgent(given), want, reason: what);
    }
  });

  test('a thread carries its host, its project and its kind', () {
    final thread = threadFor('studio', agent(id: 'a-9', project: '/w/alpha'));
    expect(thread.host, 'studio');
    expect(thread.project, '/w/alpha');
    expect(thread.kind, ThreadKind.agent);
  });

  test('an unnamed agent is called by its title', () {
    // What it was asked is a better row than its id: the bridge keeps the
    // first line of the spawn prompt for exactly this.
    expect(
      threadFor('local', unnamed(title: '  Fix the flaky test  ')).title,
      'Fix the flaky test',
    );
  });

  test('an agent with neither is called by its short id', () {
    // A raw id is not a name, so only eight characters of it are shown, as
    // a session's are.
    expect(
      threadFor('local', unnamed(id: '03423874cb70374a')).title,
      'Agent 03423874',
    );
    // An id shorter than that is taken as it is.
    expect(threadFor('local', unnamed(id: 'a-1')).title, 'Agent a-1');
  });

  test('a name wins over a title', () {
    expect(
      threadFor(
        'local',
        unnamed(name: '  Cache the reads  ', title: 'Read the cache layer'),
      ).title,
      'Cache the reads',
    );
  });

  test("the last activity is the agent's own update", () {
    final at = DateTime.utc(2026, 11, 16, 9, 30);
    expect(lastActiveOf(agent(lastActive: at)), at);
    // An unreadable time is the oldest there is, so the thread lands with
    // the earlier work rather than claiming to be current.
    expect(
      lastActiveOf(
        AgentStatus(
          id: 'a-1',
          project: '/home/u/alpha',
          status: AgentState.agentIdle,
          updatedAt: 'not a time',
        ),
      ),
      DateTime.fromMillisecondsSinceEpoch(0, isUtc: true),
    );
  });

  test('a pending request is described in one line', () {
    String describe(String? kind, Object? params) => describePending(
      PendingRequest(
        kind: kind == 'question'
            ? PendingKind.pendingQuestion
            : PendingKind.pendingApproval,
        id: 'r-1',
        params: params,
      ),
    );

    // An approval is about a command, so the command is what it says.
    expect(
      describe('approval', {'toolName': 'shell', 'command': 'rm -rf build'}),
      'rm -rf build',
    );
    expect(describe('approval', {'toolName': 'shell'}), 'shell');
    expect(describe('approval', null), 'Approval required');
    expect(describe('approval', 'a string'), 'Approval required');
    expect(describe('approval', {'command': '  '}), 'Approval required');

    // A question is about its first question.
    expect(
      describe('question', {
        'questions': [
          {'question': 'Which branch?'},
        ],
      }),
      'Which branch?',
    );
    expect(describe('question', {'questions': []}), 'Answer required');
    expect(
      describe('question', {
        'questions': ['nonsense'],
      }),
      'Answer required',
    );
  });

  test('a headline says what the agent is doing', () {
    expect(
      headlineFor(
        agent(
          pending: const PendingRequest(
            kind: PendingKind.pendingApproval,
            id: 'tc-1',
            params: {'command': 'git push'},
          ),
        ),
      ),
      'git push',
    );
    expect(
      headlineFor(agent(activity: 'Reading the tests')),
      'Reading the tests',
    );
    expect(headlineFor(agent(changedFiles: 1)), '1 file changed');
    expect(headlineFor(agent(changedFiles: 4)), '4 files changed');
    expect(headlineFor(agent()), '');
  });

  test("a thread carries the turn's steps", () {
    final thread = threadFor(
      'local',
      AgentStatus(
        id: 'a-1',
        project: '/home/u/alpha',
        status: AgentState.agentRunning,
        stepsDone: 3,
        updatedAt: DateTime.utc(2026, 11, 17, 4).toIso8601String(),
      ),
    );
    expect(thread.stepsDone, 3);

    // An agent that has not reported a step carries none: its tile draws
    // nothing rather than "step 0".
    final quiet = threadFor(
      'local',
      AgentStatus(
        id: 'a-2',
        project: '/home/u/alpha',
        status: AgentState.agentRunning,
        updatedAt: DateTime.utc(2026, 11, 17, 4).toIso8601String(),
      ),
    );
    expect(quiet.stepsDone, isNull);
  });

  test('a session with a pending request needs you', () {
    // The request wins over the turn in flight, as a parked agent's does.
    expect(
      statusOfSession(
        sessionRow(
          busy: true,
          pending: const PendingRequest(
            kind: PendingKind.pendingQuestion,
            id: 'q-1',
          ),
        ),
      ),
      ThreadStatus.needsYou,
    );
  });

  test('a busy session is running', () {
    expect(statusOfSession(sessionRow(busy: true)), ThreadStatus.running);
  });

  test('an idle open session is idle', () {
    expect(statusOfSession(sessionRow()), ThreadStatus.idle);
  });

  test('an untitled session is named by its short id', () {
    // The id's own prefix is not part of the name, and only eight characters
    // of what is left are.
    expect(
      threadOfSession(
        'local',
        '/home/u/alpha',
        sessionRow(id: 'sess_0123456789abcdef'),
      ).title,
      'Session 01234567',
    );
    // An empty or whitespace title is no title.
    expect(
      threadOfSession('local', '/home/u/alpha', sessionRow(title: '   ')).title,
      'Session 01234567',
    );
    // An id with no prefix is taken as it is.
    expect(
      threadOfSession('local', '/home/u/alpha', sessionRow(id: 'abc')).title,
      'Session abc',
    );
    // A title is what the session is called.
    expect(
      threadOfSession(
        'local',
        '/home/u/alpha',
        sessionRow(title: '  Fix the parser  '),
      ).title,
      'Fix the parser',
    );
    // The rest of the thread is the row's: its host, its root, its kind.
    final thread = threadOfSession(
      'studio',
      '/srv/alpha',
      sessionRow(id: 'sess_0123456789abcdef'),
    );
    expect(thread.host, 'studio');
    expect(thread.project, '/srv/alpha');
    expect(thread.id, 'sess_0123456789abcdef');
    expect(thread.kind, ThreadKind.session);
    expect(thread.lastActive, DateTime.utc(2026, 11, 17, 4));
  });

  test('an attached terminal is the headline', () {
    expect(
      threadOfSession(
        'local',
        '/home/u/alpha',
        sessionRow(viewers: 1),
      ).headline,
      'A terminal is attached',
    );
    expect(
      threadOfSession('local', '/home/u/alpha', sessionRow()).headline,
      '',
    );
  });

  test('a closed row is a closed thread', () {
    expect(
      threadOfSession('local', '/home/u/alpha', sessionRow(open: false)).closed,
      isTrue,
    );
    expect(
      threadOfSession('local', '/home/u/alpha', sessionRow()).closed,
      isFalse,
    );
  });
}

/// One agent as a test writes one when its name or its title is the subject:
/// the quietest agent there is, except for how it is called.
AgentStatus unnamed({String id = 'a-1', String? name, String? title}) =>
    AgentStatus(
      id: id,
      project: '/home/u/alpha',
      name: name,
      title: title,
      status: AgentState.agentIdle,
      updatedAt: DateTime.utc(2026, 11, 17, 4).toIso8601String(),
    );

/// One session row as a test writes one: open, idle, and untouched, except
/// where a test says otherwise.
SessionRow sessionRow({
  String id = 'sess_0123456789abcdef',
  String? title,
  DateTime? lastActive,
  bool open = true,
  bool busy = false,
  int viewers = 0,
  PendingRequest? pending,
}) => SessionRow(
  sessionID: id,
  title: title,
  updatedAt:
      (lastActive ?? DateTime.utc(2026, 11, 17, 4)).millisecondsSinceEpoch,
  open: open,
  viewers: viewers,
  busy: busy,
  pending: pending,
);
