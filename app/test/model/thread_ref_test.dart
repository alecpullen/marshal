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

  test('a thread is named for its agent, or its id', () {
    expect(
      threadFor('local', agent(id: 'a-1', name: 'Cache the reads')).title,
      'Cache the reads',
    );
    expect(threadFor('local', agent(id: 'a-1', name: '  ')).title, 'a-1');
    final thread = threadFor('studio', agent(id: 'a-9', project: '/w/alpha'));
    expect(thread.host, 'studio');
    expect(thread.project, '/w/alpha');
    expect(thread.kind, ThreadKind.agent);
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
}
